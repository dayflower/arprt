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
	"strings"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/controller"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

func (r *Reconciler) desired(ctx context.Context, key client.ObjectKey) (*api.TrmnlDevice, *api.TrmnlScreen, *state, error) {
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
	s, err := (assignment.Explicit{Reader: r.Reader}).Resolve(ctx, &d)
	if apierrors.IsNotFound(err) || errors.Is(err, assignment.ErrPlaylistDeferred) || errors.Is(err, assignment.ErrDiscoveryDeferred) {
		return &d, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
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
	d, s, wanted, err := r.desired(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	st, err := r.Store.load(req.Namespace, req.Name)
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
				if err := os.Remove(r.Store.path(req.Namespace, req.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
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
	_, _, current, err := r.desired(ctx, client.ObjectKeyFromObject(d))
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
		if errors.Is(err, artifact.ErrObsolete) {
			return r.failed(ctx, d, s, st, "ObsoleteAttempt")
		}
		return ctrl.Result{}, err
	}
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
	st.Active = nil
	st.Failures = min(st.Failures+1, 7)
	st.Reason = reason
	delay := min(5*time.Second*time.Duration(1<<uint(st.Failures-1)), 5*time.Minute)
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
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	message := "Job output is pending or failed; check Job status, paths and PNG contract"
	if ready {
		message = "Validated Job image available"
	}
	for _, obj := range []client.Object{d, s} {
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
			if d.Spec.ScreenRef != nil && d.Spec.ScreenRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
			}
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).Named("job-renderer").For(&api.TrmnlDevice{}).Owns(&batchv1.Job{}).Watches(&api.TrmnlScreen{}, enqueue).Complete(r)
}

func (r *Reconciler) NeedLeaderElection() bool { return false }
func (r *Reconciler) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := r.maintenance(ctx); err != nil && ctx.Err() == nil {
			ctrl.LoggerFrom(ctx).Error(err, "Job storage maintenance failed")
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
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.Store.Root, "jobs", entry.Name()))
		if err != nil {
			return err
		}
		var st state
		if err = json.Unmarshal(b, &st); err != nil {
			return err
		}
		_, _, desired, err := r.desired(ctx, client.ObjectKey{Namespace: st.Namespace, Name: st.DeviceName})
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
			if err = os.Remove(r.Store.path(st.Namespace, st.DeviceName)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if st.Active != nil {
			active[st.Active.ID] = true
		}
	}
	var jobs batchv1.JobList
	if err = r.Reader.List(ctx, &jobs, client.InNamespace(r.Config.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
		return err
	}
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
		var devices api.TrmnlDeviceList
		var screens api.TrmnlScreenList
		if err := r.Reader.List(ctx, &devices, client.InNamespace(r.Config.Namespace)); err != nil {
			return err
		}
		if err := r.Reader.List(ctx, &screens, client.InNamespace(r.Config.Namespace)); err != nil {
			return err
		}
		screenUIDs := map[string]string{}
		pairOwned := map[string]bool{}
		for _, s := range screens.Items {
			if s.DeletionTimestamp.IsZero() {
				screenUIDs[s.Name] = string(s.UID)
				pairOwned[s.Name] = s.Spec.Source.Job != nil || (s.Spec.Source.HTTP != nil && s.Spec.Source.HTTP.Method == "POST")
			}
		}
		deviceUIDs := map[string]bool{}
		units := map[artifact.Unit]bool{}
		for _, d := range devices.Items {
			if !d.DeletionTimestamp.IsZero() {
				continue
			}
			deviceUIDs[string(d.UID)] = true
			if d.Spec.ScreenRef == nil || d.Status.Display == nil {
				continue
			}
			screenUID := screenUIDs[d.Spec.ScreenRef.Name]
			if screenUID == "" {
				continue
			}
			u := artifact.Unit{Namespace: d.Namespace, ScreenUID: screenUID, Target: render.Target{Width: d.Status.Display.Width, Height: d.Status.Display.Height, Palette: d.Status.Display.Palette}}
			if pairOwned[d.Spec.ScreenRef.Name] {
				u.DeviceUID = string(d.UID)
			}
			units[u] = true
		}
		return repository.Collect(ctx, func(u artifact.Unit) bool { return units[u] }, deviceUIDs, r.now())
	}
	return nil
}
