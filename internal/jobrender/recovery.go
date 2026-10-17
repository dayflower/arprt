package jobrender

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// prepareAttempt leaves Active nil when work must wait or fails. On success,
// context, sequence and Active are durable before advance can submit a Job.
func (r *Reconciler) prepareAttempt(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state) (ctrl.Result, error) {
	source, err := normalize(*s.Spec.Source.Job)
	if err != nil {
		return r.failed(ctx, d, s, st, "InvalidJobPaths")
	}
	snapshot, err := render.Build(d.Name, st.Unit.Target, devicestate.Observation(d.Status), render.Observation{})
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
	return ctrl.Result{}, nil
}

// submitPrepared only handles an absent Job; existing Jobs recover separately.
func (r *Reconciler) submitPrepared(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state) (ctrl.Result, error) {
	a := st.Active
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
	if err := r.Reader.List(ctx, &jobs, client.InNamespace(st.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
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
	if err := r.Client.Create(ctx, r.Config.job(st)); err != nil && !apierrors.IsAlreadyExists(err) {
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			return r.failed(ctx, d, s, st, "JobCreationFailed")
		}
		return ctrl.Result{}, err
	}
	a.Submitted = true
	if err := r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.report(ctx, d, s, false, "JobPending"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// recoverSubmission verifies ownership before acknowledging a create whose
// Submitted write may have been interrupted.
func (r *Reconciler) recoverSubmission(st *state, job *batchv1.Job) error {
	owner := metav1.GetControllerOf(job)
	if owner == nil || string(owner.UID) != st.DeviceUID || job.Labels[managedLabel] != "true" || job.Labels[deviceLabel] != hash([]byte(st.DeviceUID))[:32] {
		return errors.New("render Job identity conflict")
	}
	if !st.Active.Submitted {
		st.Active.Submitted = true
		if err := r.Store.save(st); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) observeJob(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state, job *batchv1.Job) (ctrl.Result, error) {
	a := st.Active
	complete, failed := terminal(job)
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
	return r.collectSuccessful(ctx, d, s, st, job)
}

func (r *Reconciler) collectSuccessful(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state, job *batchv1.Job) (ctrl.Result, error) {
	a := st.Active
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
	var result ctrl.Result
	err = imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		b, err := r.Store.collectInput(ctx, a)
		if err == nil {
			policy, policyErr := screen.ProcessingPolicy(s.Spec)
			err = policyErr
			if err == nil {
				b, err = render.ProcessPNG(ctx, b, st.Unit.ResolvedTarget(), policy)
			}
		}
		if err != nil {
			result, err = r.failed(ctx, d, s, st, "CollectionFailed")
			return err
		}
		// Recheck desired state after collection and processing, before publication.
		_, _, current, err := r.desired(ctx, client.ObjectKeyFromObject(d), st.ScreenName)
		if err != nil {
			return err
		}
		if !same(st, current) {
			result = ctrl.Result{RequeueAfter: time.Millisecond}
			return nil
		}
		result, err = r.publishCompleted(ctx, d, s, st, job, b)
		return err
	})
	return result, err
}

// publishCompleted publishes before clearing Active so an interrupted save can
// recover the same attempt and publication metadata.
func (r *Reconciler) publishCompleted(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state, job *batchv1.Job, b []byte) (ctrl.Result, error) {
	a := st.Active
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
