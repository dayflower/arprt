package jobrender

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/controller"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type Reconciler struct {
	Client     client.Client
	Reader     client.Reader
	Repository artifact.Repository
	Store      *Store
	Config     Config
	Recorder   events.EventRecorder
	Now        func() time.Time
	mu         sync.Mutex // Also serializes maintenance with publication and preparation.
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) desired(ctx context.Context, key client.ObjectKey, screenName ...string) (*api.TrmnlDevice, *api.TrmnlScreen, *state, error) {
	var d api.TrmnlDevice
	if err := r.Reader.Get(ctx, key, &d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	if !d.DeletionTimestamp.IsZero() || d.Status.Display == nil {
		return &d, nil, nil, nil
	}
	eligible, err := (assignment.Explicit{Reader: r.Reader}).Eligible(ctx, &d)
	if apierrors.IsNotFound(err) || errors.Is(err, assignment.ErrEmpty) {
		return &d, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	s := eligible.Screens[0]
	if len(screenName) > 0 {
		s = nil
		for _, candidate := range eligible.Screens {
			if candidate.Name == screenName[0] {
				s = candidate
				break
			}
		}
		if s == nil {
			return &d, nil, nil, nil
		}
	}
	if s.Spec.Source.Job == nil || !s.DeletionTimestamp.IsZero() {
		return &d, s, nil, nil
	}
	t := render.Target{Width: d.Status.Display.Width, Height: d.Status.Display.Height, Palette: d.Status.Display.Palette}
	if t.Validate() != nil || t.Palette != "gray4" || t.Width != 800 || t.Height != 480 {
		return &d, s, nil, nil
	}
	revision, err := screen.InputRevision(s.Spec)
	if err != nil {
		return nil, nil, nil, err
	}
	return &d, s, &state{Namespace: d.Namespace, DeviceName: d.Name, DeviceUID: string(d.UID), ScreenName: s.Name, ScreenGeneration: s.Generation, Unit: artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), DeviceUID: string(d.UID), Target: t}, Revision: revision}, nil
}

func same(a, b *state) bool {
	return a != nil && b != nil && a.DeviceUID == b.DeviceUID && a.Unit == b.Unit && a.ScreenGeneration == b.ScreenGeneration && a.Revision == b.Revision
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Process eligible and previously scheduled pairs, including removals.
	names := map[string]bool{}
	var d api.TrmnlDevice
	if err := r.Reader.Get(ctx, req.NamespacedName, &d); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	} else if err == nil {
		set, err := (assignment.Explicit{Reader: r.Reader}).Eligible(ctx, &d)
		if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, assignment.ErrEmpty) {
			return ctrl.Result{}, err
		}
		for _, s := range set.Screens {
			if s.Spec.Source.Job != nil {
				names[s.Name] = true
			}
		}
	}
	records, err := r.Store.records()
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, st := range records {
		if st.Namespace == req.Namespace && st.DeviceName == req.Name {
			names[st.ScreenName] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	recordsByName := map[string]state{}
	for _, st := range records {
		if st.Namespace == req.Namespace && st.DeviceName == req.Name {
			recordsByName[st.ScreenName] = st
		}
	}
	// Recover/collect active attempts first, then prefer the oldest due work.
	sort.Slice(ordered, func(i, j int) bool {
		a, b := recordsByName[ordered[i]], recordsByName[ordered[j]]
		if (a.Active != nil) != (b.Active != nil) {
			return a.Active != nil
		}
		if !a.Due.Equal(b.Due) {
			return a.Due.Before(b.Due)
		}
		return ordered[i] < ordered[j]
	})
	result := ctrl.Result{}
	for _, name := range ordered {
		next, err := r.reconcilePair(ctx, req, name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if next.RequeueAfter > 0 && (result.RequeueAfter == 0 || next.RequeueAfter < result.RequeueAfter) {
			result.RequeueAfter = next.RequeueAfter
		}
	}
	return result, nil
}

func (r *Reconciler) reconcilePair(ctx context.Context, req ctrl.Request, screenName string) (ctrl.Result, error) {
	d, s, wanted, err := r.desired(ctx, req.NamespacedName, screenName)
	if err != nil {
		return ctrl.Result{}, err
	}
	st, err := r.Store.load(req.Namespace, req.Name, screenName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !same(st, wanted) {
		if st != nil && st.Active != nil {
			done, err := r.cancel(ctx, st)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
		}
		if wanted == nil {
			if st != nil {
				if err := os.Remove(r.Store.path(req.Namespace, req.Name, screenName)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		if st == nil {
			if repository, ok := r.Repository.(*artifact.PVC); ok {
				if err := repository.CheckCapacity(ctx, 4096); err != nil {
					if !errors.Is(err, artifact.ErrCapacity) {
						return ctrl.Result{}, err
					}
					if err := r.report(ctx, d, s, false, "StorageCapacityExceeded"); err != nil {
						return ctrl.Result{}, err
					}
					return ctrl.Result{RequeueAfter: time.Minute}, nil
				}
			}
		}
		wanted.Due = r.now()
		if st != nil && st.DeviceUID == wanted.DeviceUID {
			wanted.Sequence = st.Sequence
		}
		old, err := r.Repository.Latest(ctx, wanted.Unit)
		if err != nil && !errors.Is(err, artifact.ErrNotFound) {
			return ctrl.Result{}, err
		}
		if old.Sequence > wanted.Sequence {
			wanted.Sequence = old.Sequence
		}
		if old.InputRevision == wanted.Revision && old.ScreenGeneration == wanted.ScreenGeneration {
			interval, _ := screen.EffectiveInterval(s.Spec)
			wanted.Due = old.RenderedAt.Add(interval)
		}
		st = wanted
		if err := r.Store.save(st); err != nil {
			return ctrl.Result{}, err
		}
	}
	if st.Active != nil {
		return r.advance(ctx, d, s, st)
	}
	if remaining := st.Due.Sub(r.now()); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	// Do not allocate durable attempts/run directories while the global queue is full.
	var jobs batchv1.JobList
	if err := r.Reader.List(ctx, &jobs, client.InNamespace(r.Config.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
		return ctrl.Result{}, err
	}
	active := 0
	for i := range jobs.Items {
		complete, failed := terminal(&jobs.Items[i])
		if !complete && !failed {
			active++
		}
	}
	observe.Jobs.Set(float64(active))
	if active >= r.Config.MaxActive {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	source, err := normalize(*s.Spec.Source.Job)
	if err != nil {
		return r.failed(ctx, d, s, st, "InvalidJobPaths")
	}
	snapshot, err := render.Build(d.Name, st.Unit.Target, controller.Observation(d.Status), render.Observation{})
	if err != nil {
		return ctrl.Result{}, err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return ctrl.Result{}, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return ctrl.Result{}, err
	}
	a := &attempt{ID: hex.EncodeToString(id[:]), Sequence: st.Sequence + 1, StartedAt: r.now(), ContextDigest: hash(b), Source: source}
	candidate := *st
	candidate.Active = a
	encoded, err := json.Marshal(candidate)
	if err != nil || len(encoded) > 1<<20 {
		return r.failed(ctx, d, s, st, "InvalidJobSpec")
	}
	if repository, ok := r.Repository.(*artifact.PVC); ok {
		if err := repository.CheckCapacity(ctx, int64(2*len(encoded)+len(b))+(128<<10)); err != nil {
			if !errors.Is(err, artifact.ErrCapacity) {
				return ctrl.Result{}, err
			}
			if err := r.report(ctx, d, s, false, "StorageCapacityExceeded"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}
	if err = r.Store.prepare(a, b); err != nil {
		return ctrl.Result{}, err
	}
	st.Sequence = a.Sequence
	st.Active = a
	// Both context and attempt are durable before the first Kubernetes write.
	if err = r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	return r.advance(ctx, d, s, st)
}

func terminal(job *batchv1.Job) (complete, failed bool) {
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			if c.Type == batchv1.JobComplete {
				complete = true
			}
			if c.Type == batchv1.JobFailed {
				failed = true
			}
		}
	}
	return
}

func (r *Reconciler) pods(ctx context.Context, st *state) ([]corev1.Pod, error) {
	var pods corev1.PodList
	err := r.Reader.List(ctx, &pods, client.InNamespace(st.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": "arprt-render-" + st.Active.ID})
	return pods.Items, err
}

func (r *Reconciler) cancel(ctx context.Context, st *state) (bool, error) {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: st.Namespace, Name: "arprt-render-" + st.Active.ID}}
	err := r.Client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground))
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	pods, err := r.pods(ctx, st)
	if err != nil {
		return false, err
	}
	if len(pods) > 0 {
		return false, nil
	}
	var current batchv1.Job
	err = r.Reader.Get(ctx, client.ObjectKeyFromObject(job), &current)
	if err == nil {
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func (r *Reconciler) advance(ctx context.Context, d *api.TrmnlDevice, s *api.TrmnlScreen, st *state) (ctrl.Result, error) {
	a := st.Active
	if a.TerminalReason != "" {
		return r.failed(ctx, d, s, st, a.TerminalReason)
	}
	var job batchv1.Job
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: "arprt-render-" + a.ID}, &job)
	if apierrors.IsNotFound(err) {
		if a.Submitted {
			return r.failed(ctx, d, s, st, "JobLost")
		}
		pods, err := r.pods(ctx, st)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(pods) > 0 {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		// A create may have succeeded immediately before the controller crashed.
		// If its Job then disappeared, output cannot prove renderer success.
		entries, err := os.ReadDir(filepath.Join(r.Store.run(a.ID), "output"))
		if err != nil {
			return r.failed(ctx, d, s, st, "RunUnavailable")
		}
		if len(entries) > 0 {
			return r.failed(ctx, d, s, st, "JobLost")
		}
		var jobs batchv1.JobList
		if err = r.Reader.List(ctx, &jobs, client.InNamespace(st.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
			return ctrl.Result{}, err
		}
		active := 0
		for i := range jobs.Items {
			complete, failed := terminal(&jobs.Items[i])
			if !complete && !failed {
				active++
			}
		}
		if active >= r.Config.MaxActive {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if r.now().Sub(a.StartedAt) > time.Duration(r.Config.DeadlineSeconds+120)*time.Second {
			return r.failed(ctx, d, s, st, "JobTimedOut")
		}
		if err = r.Client.Create(ctx, r.Config.job(st)); err != nil && !apierrors.IsAlreadyExists(err) {
			if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
				return r.failed(ctx, d, s, st, "JobCreationFailed")
			}
			return ctrl.Result{}, err
		}
		a.Submitted = true
		if err = r.Store.save(st); err != nil {
			return ctrl.Result{}, err
		}
		if err = r.report(ctx, d, s, false, "JobPending"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	owner := metav1.GetControllerOf(&job)
	if owner == nil || string(owner.UID) != st.DeviceUID || job.Labels[managedLabel] != "true" || job.Labels[deviceLabel] != hash([]byte(st.DeviceUID))[:32] {
		return ctrl.Result{}, errors.New("render Job identity conflict")
	}
	if !a.Submitted {
		a.Submitted = true
		if err = r.Store.save(st); err != nil {
			return ctrl.Result{}, err
		}
	}
	complete, failed := terminal(&job)
	if failed {
		reason := "JobFailed"
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && c.Reason == "DeadlineExceeded" {
				reason = "JobTimedOut"
			}
		}
		return r.failed(ctx, d, s, st, reason)
	}
	if !complete {
		if r.now().Sub(a.StartedAt) > time.Duration(r.Config.DeadlineSeconds+120)*time.Second {
			done, err := r.cancel(ctx, st)
			if err != nil {
				return ctrl.Result{}, err
			}
			if done {
				return r.failed(ctx, d, s, st, "JobTimedOut")
			}
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	pods, err := r.pods(ctx, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	good := false
	for _, pod := range pods {
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.UID != job.UID {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "renderer" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 && pod.Status.Phase == corev1.PodSucceeded {
				good = true
			}
		}
	}
	if !good {
		return r.failed(ctx, d, s, st, "RendererNotSuccessful")
	}
	b, err := r.Store.collect(a, st.Unit.Target)
	if err != nil {
		return r.failed(ctx, d, s, st, "CollectionFailed")
	}
	// Use direct API reads again after collection; status events never override
	// UID, assignment, definition, target, or durable attempt ordering.
	_, _, current, err := r.desired(ctx, client.ObjectKeyFromObject(d), st.ScreenName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !same(st, current) {
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}
	renderedAt := r.now()
	if job.Status.CompletionTime != nil {
		renderedAt = job.Status.CompletionTime.Time
	}
	m, err := r.Repository.Publish(ctx, artifact.Metadata{Unit: st.Unit, ScreenGeneration: st.ScreenGeneration, InputRevision: st.Revision, AttemptID: a.ID, Sequence: a.Sequence, ContextDigest: a.ContextDigest, RenderedAt: renderedAt}, b)
	if err != nil {
		if errors.Is(err, artifact.ErrCapacity) {
			if err := r.report(ctx, d, s, false, "StorageCapacityExceeded"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if errors.Is(err, artifact.ErrObsolete) {
			return r.failed(ctx, d, s, st, "ObsoleteAttempt")
		}
		return ctrl.Result{}, err
	}
	observe.Render("Job", a.StartedAt, nil)
	interval, _ := screen.EffectiveInterval(s.Spec)
	st.Due = m.RenderedAt.Add(interval)
	st.Failures = 0
	st.Reason = ""
	st.Active = nil
	if err = r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	if err = r.report(ctx, d, s, true, "ImageReady"); err != nil {
		return ctrl.Result{}, err
	}
	// Finished Jobs and logs remain for an hour after durable bookkeeping.
	// No Kubernetes TTL may delete their output before collection can recover.
	return ctrl.Result{RequeueAfter: max(time.Millisecond, st.Due.Sub(r.now()))}, nil
}

func (r *Reconciler) failed(ctx context.Context, d *api.TrmnlDevice, s *api.TrmnlScreen, st *state, reason string) (ctrl.Result, error) {
	if st.Active != nil {
		st.Active.TerminalReason = reason
		if err := r.Store.save(st); err != nil {
			return ctrl.Result{}, err
		}
		var job batchv1.Job
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: "arprt-render-" + st.Active.ID}, &job)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		complete, failed := terminal(&job)
		if err != nil || (!complete && !failed) {
			done, err := r.cancel(ctx, st)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
		}
	}
	if st.Active != nil {
		observe.Render("Job", st.Active.StartedAt, errors.New("failed"))
	}
	if reason == "CollectionFailed" {
		observe.CollectionFailures.Inc()
	}
	st.Active = nil
	st.Failures = min(st.Failures+1, 7)
	st.Reason = reason
	delay := min(5*time.Second*time.Duration(1<<uint(st.Failures-1)), 5*time.Minute)
	if st.Failures >= 7 {
		interval, _ := screen.EffectiveInterval(s.Spec)
		delay = max(delay, interval)
	}
	st.Due = r.now().Add(delay)
	if err := r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.report(ctx, d, s, false, reason); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(d, nil, corev1.EventTypeWarning, reason, "Render", "Job rendering failed; compatible prior output is retained")
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

func (r *Reconciler) report(ctx context.Context, d *api.TrmnlDevice, s *api.TrmnlScreen, ready bool, reason string) error {
	ctrl.LoggerFrom(ctx).Info("Job render state", "namespace", d.Namespace, "device", d.Name, "screen", s.Name, "reason", reason)
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	message := "Job output is pending or failed; check Job status, paths and PNG contract"
	if ready {
		message = "Validated Job image available"
	}
	objects := []client.Object{s}
	if d.Spec.ScreenRef != nil {
		objects = append(objects, d)
	}
	for _, obj := range objects {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := obj.DeepCopyObject().(client.Object)
			if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.GetUID() != obj.GetUID() || current.GetGeneration() != obj.GetGeneration() {
				return nil
			}
			before := current.DeepCopyObject().(client.Object)
			condition := metav1.Condition{Type: "RenderReady", Status: status, Reason: reason, Message: message, ObservedGeneration: current.GetGeneration()}
			switch v := current.(type) {
			case *api.TrmnlDevice:
				meta.SetStatusCondition(&v.Status.Conditions, condition)
			case *api.TrmnlScreen:
				condition.Type = "Ready"
				meta.SetStatusCondition(&v.Status.Conditions, condition)
				v.Status.ObservedGeneration = v.Generation
			}
			if reflect.DeepEqual(current, before) {
				return nil
			}
			return r.Client.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueue := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var devices api.TrmnlDeviceList
		if err := r.Client.List(ctx, &devices, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, d := range devices.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).Named("job-renderer").For(&api.TrmnlDevice{}).Owns(&batchv1.Job{}).Watches(&api.TrmnlScreen{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Watches(&api.TrmnlPlaylist{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

func (r *Reconciler) NeedLeaderElection() bool { return false }
func (r *Reconciler) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := r.maintenance(ctx); err != nil && ctx.Err() == nil {
			observe.Maintenance.WithLabelValues("failure").Inc()
			observe.StorageErrors.WithLabelValues("maintenance").Inc()
			ctrl.LoggerFrom(ctx).Info("Job storage maintenance failed; retained data is protected", "reason", "StorageMaintenanceFailed")
		} else if ctx.Err() == nil {
			observe.Maintenance.WithLabelValues("success").Inc()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Reconciler) maintenance(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(r.Store.Root, "jobs"))
	if err != nil {
		return err
	}
	active := map[string]bool{}
	queued := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pending-") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && r.now().Sub(info.ModTime()) >= time.Hour {
				if err := os.Remove(filepath.Join(r.Store.Root, "jobs", entry.Name())); err != nil {
					return err
				}
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readStateFile(filepath.Join(r.Store.Root, "jobs", entry.Name()))
		if err != nil {
			return err
		}
		var st state
		if err = json.Unmarshal(b, &st); err != nil {
			return errors.New("corrupt scheduler metadata")
		}
		if err = validateState(&st); err != nil {
			return err
		}
		if entry.Name() != filepath.Base(r.Store.path(st.Namespace, st.DeviceName, st.ScreenName)) {
			return errors.New("invalid scheduler key")
		}
		if st.Active == nil && !st.Due.After(r.now()) {
			queued++
		}
		_, _, desired, err := r.desired(ctx, client.ObjectKey{Namespace: st.Namespace, Name: st.DeviceName}, st.ScreenName)
		if err != nil {
			return err
		}
		if !same(&st, desired) {
			if st.Active != nil {
				done, err := r.cancel(ctx, &st)
				if err != nil {
					return err
				}
				if !done {
					active[st.Active.ID] = true
					continue
				}
			}
			if err = os.Remove(filepath.Join(r.Store.Root, "jobs", entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if st.Active != nil {
			active[st.Active.ID] = true
		}
	}
	observe.Queue.Set(float64(queued))
	var jobs batchv1.JobList
	if err = r.Reader.List(ctx, &jobs, client.InNamespace(r.Config.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
		return err
	}
	running := 0
	for i := range jobs.Items {
		if complete, failed := terminal(&jobs.Items[i]); !complete && !failed {
			running++
		}
	}
	observe.Jobs.Set(float64(running))
	for i := range jobs.Items {
		job := &jobs.Items[i]
		id := strings.TrimPrefix(job.Name, "arprt-render-")
		if active[id] {
			continue
		}
		complete, failed := terminal(job)
		finished := job.CreationTimestamp.Time
		for _, c := range job.Status.Conditions {
			if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && !c.LastTransitionTime.IsZero() {
				finished = c.LastTransitionTime.Time
			}
		}
		if (complete || failed) && r.now().Sub(finished) < time.Hour {
			active[id] = true
			continue
		}
		if err = r.Client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		active[id] = true // Keep output until Job and Pods have disappeared.
	}
	runs, err := os.ReadDir(filepath.Join(r.Store.Root, "runs"))
	if err != nil {
		return err
	}
	for _, entry := range runs {
		if !validID(entry.Name()) || active[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if r.now().Sub(info.ModTime()) >= time.Hour {
			if err = os.RemoveAll(r.Store.run(entry.Name())); err != nil {
				return err
			}
		}
	}
	if repository, ok := r.Repository.(*artifact.PVC); ok {
		return (&controller.StorageMaintenance{Reader: r.Reader, Repository: repository, Namespace: r.Config.Namespace, Refresh: r.Config.Refresh}).Collect(ctx, r.now())
	}
	return nil
}
