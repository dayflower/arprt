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
	if f := r.flight(st); st.Trigger == OnDemand && f != nil {
		f.attempted = true
		st.Reason = ""
	}
	source, err := normalize(*s.Spec.Source.Job)
	if err != nil {
		return r.failed(ctx, d, s, st, "InvalidJobPaths")
	}
	snapshot, err := render.Build(d.Name, st.Unit.Target, devicestate.Observation(d.Status), render.Observation{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if f := r.flight(st); st.Trigger == OnDemand && f != nil {
		if err := f.ctx.Err(); err != nil {
			return ctrl.Result{}, err
		}
		if err := json.Unmarshal(f.snapshot, &snapshot); err != nil {
			return ctrl.Result{}, err
		}
	}
	var pair *artifact.RendererPair
	if source.State != nil {
		record, err := r.rendererRecord(ctx, st, true)
		if err != nil {
			return ctrl.Result{}, err
		}
		p := record.Pairs[artifact.PairKey(st.Namespace, st.Unit.ScreenUID)]
		if p.CandidateRef != "" {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		state, err := r.Repository.(artifact.StateStore).CommittedState(ctx, record, artifact.PairKey(st.Namespace, st.Unit.ScreenUID))
		if err != nil {
			return ctrl.Result{}, err
		}
		snapshot.RendererState = state
		pair = &p
	} else {
		snapshot.RendererState = nil
	}
	b, err := render.MarshalContext(snapshot)
	if err != nil {
		return ctrl.Result{}, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return ctrl.Result{}, err
	}
	a := &attempt{ID: hex.EncodeToString(id[:]), Sequence: st.Sequence + 1, StartedAt: r.now(), ContextDigest: hash(b), Source: source, RendererPair: pair}
	if st.Trigger == OnDemand {
		f := r.flight(st)
		if f == nil {
			return ctrl.Result{}, context.Canceled
		}
		if err := f.ctx.Err(); err != nil {
			return ctrl.Result{}, err
		}
		a.Trigger = OnDemand
		a.StartedAt = f.started
		a.Deadline, _ = f.ctx.Deadline()
		if !r.now().Before(a.Deadline) {
			return ctrl.Result{}, context.DeadlineExceeded
		}
	}
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
			if st.Trigger == OnDemand {
				return ctrl.Result{}, artifact.ErrCapacity
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return ctrl.Result{}, err
	}
	if err = r.Store.prepare(a, b); err != nil {
		return ctrl.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return ctrl.Result{}, err
	}
	st.Sequence = a.Sequence
	st.Active = a
	// Both context and attempt are durable before the first Kubernetes write.
	if err = r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	if f := r.flight(st); st.Trigger == OnDemand && f != nil {
		f.id = a.ID
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
	active, err := r.activeSlots(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if active >= r.Config.MaxActive {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if r.now().Sub(a.StartedAt) > time.Duration(r.Config.DeadlineSeconds+120)*time.Second {
		return r.failed(ctx, d, s, st, "JobTimedOut")
	}
	if err := ctx.Err(); err != nil {
		return ctrl.Result{}, err
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
	if err := validateJobOwner(st, job); err != nil {
		return err
	}
	if !st.Active.Submitted {
		st.Active.Submitted = true
		if err := r.Store.save(st); err != nil {
			return err
		}
	}
	return nil
}

func validateJobOwner(st *state, job *batchv1.Job) error {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.APIVersion != api.GroupVersion.String() || owner.Kind != "ArprtDevice" || owner.Name != st.DeviceName || string(owner.UID) != st.DeviceUID || job.Labels[managedLabel] != "true" || job.Labels[deviceLabel] != hash([]byte(st.DeviceUID))[:32] {
		return errors.New("render Job identity conflict")
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
			return r.failed(ctx, d, s, st, "JobTimedOut")
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
		var stateResult render.StateResult
		if err == nil {
			stateResult, err = r.Store.collectResult(ctx, a)
		}
		if err == nil {
			policy, policyErr := screen.ProcessingPolicy(s.Spec)
			err = policyErr
			if err == nil {
				b, err = render.ProcessPNG(ctx, b, st.Unit.ResolvedTarget(), policy)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var filesystemError *os.PathError
			if errors.As(err, &filesystemError) && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			result, err = r.failed(ctx, d, s, st, "CollectionFailed")
			return err
		}
		// Recheck desired state after collection and processing, before publication.
		_, _, current, err := r.desired(ctx, client.ObjectKeyFromObject(d), st.ScreenName)
		if err != nil {
			return err
		}
		if !same(st, current) {
			if st.Trigger == OnDemand {
				st.Active.TerminalReason = "ObsoleteAttempt"
				return r.Store.save(st)
			}
			result = ctrl.Result{RequeueAfter: time.Millisecond}
			return nil
		}
		result, err = r.publishCompleted(ctx, d, s, st, job, b, stateResult)
		return err
	})
	return result, err
}

// publishCompleted publishes before clearing Active so an interrupted save can
// recover the same attempt and publication metadata.
func (r *Reconciler) publishCompleted(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state, job *batchv1.Job, b []byte, result render.StateResult) (ctrl.Result, error) {
	a := st.Active
	if a.TerminalReason != "" {
		return ctrl.Result{}, &ExecutionError{Reason: a.TerminalReason}
	}
	if err := ctx.Err(); err != nil {
		return ctrl.Result{}, err
	}
	if a.Trigger == OnDemand && !r.now().Before(a.Deadline) {
		return ctrl.Result{}, context.DeadlineExceeded
	}
	renderedAt := r.now()
	if job.Status.CompletionTime != nil {
		renderedAt = job.Status.CompletionTime.Time
	}
	m := artifact.Metadata{Unit: st.Unit, ScreenGeneration: st.ScreenGeneration, InputRevision: st.Revision, AttemptID: a.ID, Sequence: a.Sequence, ContextDigest: a.ContextDigest, RenderedAt: renderedAt}
	var err error
	if a.RendererPair != nil {
		var record artifact.CornerRecord
		record, err = r.rendererRecord(ctx, st, false)
		if err == nil {
			key := artifact.PairKey(st.Namespace, st.Unit.ScreenUID)
			pair := record.Pairs[key]
			if pair.Epoch != a.RendererPair.Epoch || pair.Generation != a.RendererPair.Generation || pair.Definition != a.RendererPair.Definition {
				err = artifact.ErrObsolete
			} else {
				var candidate artifact.CandidateManifest
				candidate, err = r.Repository.(artifact.StateStore).PublishCandidate(ctx, record, key, m, b, result)
				m = candidate.Metadata
			}
		}
	} else {
		m, err = r.Repository.Publish(ctx, m, b)
	}
	if err != nil {
		if errors.Is(err, artifact.ErrCapacity) {
			if err := r.report(ctx, d, s, false, "StorageCapacityExceeded"); err != nil {
				return ctrl.Result{}, err
			}
			if st.Trigger == OnDemand {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if errors.Is(err, artifact.ErrObsolete) {
			if a.RendererPair != nil {
				// An unrelated corner/pair revision can lose the complete-record CAS
				// without invalidating this input generation. Retry the same attempt.
				record, readErr := r.rendererRecord(ctx, st, false)
				if readErr != nil {
					return ctrl.Result{}, readErr
				}
				pair := record.Pairs[artifact.PairKey(st.Namespace, st.Unit.ScreenUID)]
				if pair.Epoch == a.RendererPair.Epoch && pair.Generation == a.RendererPair.Generation && pair.Definition == a.RendererPair.Definition && pair.DetachedAt == nil && pair.CandidateRef == "" {
					return ctrl.Result{RequeueAfter: time.Millisecond}, nil
				}
			}
			return r.failed(ctx, d, s, st, "ObsoleteAttempt")
		}
		return ctrl.Result{}, err
	}
	return r.complete(ctx, d, s, st, m)
}

func (r *Reconciler) complete(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen, st *state, m artifact.Metadata) (ctrl.Result, error) {
	observe.Render("Job", st.Active.StartedAt, nil)
	interval, _ := screen.EffectiveInterval(s.Spec)
	st.Due = m.RenderedAt.Add(interval)
	if st.Trigger == OnDemand {
		st.Due = time.Time{}
		if f := r.flight(st); f != nil && f.id == m.AttemptID {
			f.metadata = m
		}
	}
	st.Failures = 0
	st.Reason = ""
	st.Active = nil
	if err := r.Store.save(st); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.report(ctx, d, s, true, "ImageReady"); err != nil {
		return ctrl.Result{}, err
	}
	// Finished Jobs and logs remain for an hour after durable bookkeeping.
	// No Kubernetes TTL may delete their output before collection can recover.
	if st.Trigger == OnDemand {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: max(time.Millisecond, st.Due.Sub(r.now()))}, nil
}
