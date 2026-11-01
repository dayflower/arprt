package jobrender

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/render"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Trigger is internal execution policy; the empty value reads old records as
// scheduled work without rewriting their IDs, snapshots or retry state.
type Trigger string

const (
	Scheduled Trigger = ""
	OnDemand  Trigger = "OnDemand"
)

func (t Trigger) valid() bool { return t == Scheduled || t == OnDemand }

// ExecutionRequest carries the already-admitted request's absolute render
// cutoff and exact expected identity. It does not authorize image issuance.
// Callers retain the separate request context for fallback and finalization.
type ExecutionRequest struct {
	Device         client.ObjectKey
	Screen         string
	Unit           artifact.Unit
	Revision       string
	StartedAt      time.Time
	RenderDeadline time.Time
	Timeout        time.Duration
	Observation    render.Observation
	// Snapshot is captured before display waiters release their device gate.
	Snapshot         *render.Context
	DeviceGeneration int64
	Assignment       string
}

// ExecutionError describes candidate failure; storage/API errors remain errors
// from their original operation so a caller cannot hide them by skipping.
type ExecutionError struct {
	Reason string
	cause  error
}

func (e *ExecutionError) Error() string { return e.Reason }
func (e *ExecutionError) Unwrap() error { return e.cause }

// operationMutex keeps the existing operation-wide GC exclusion while allowing
// request admission to stop waiting at its own absolute cutoff.
type operationMutex struct {
	once sync.Once
	held chan struct{}
}

func (m *operationMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *operationMutex) Unlock() { <-m.held }
func (m *operationMutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.held <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		m.Unlock()
		return err
	}
	return nil
}

type execution struct {
	wanted    *state
	ctx       context.Context
	cancel    context.CancelFunc
	waiters   atomic.Int64
	done      chan struct{}
	id        string
	attempted bool
	metadata  artifact.Metadata
	err       error
	snapshot  []byte
	started   time.Time
	stop      func() bool
}

func pairKey(namespace, device, screen string) string { return namespace + "/" + device + "/" + screen }
func clientRequest(st *state) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKey{Namespace: st.Namespace, Name: st.DeviceName}}
}

// flight is called under mu. Identity changes never join an older execution.
func (r *Reconciler) flight(st *state) *execution {
	f := r.flights[pairKey(st.Namespace, st.DeviceName, st.ScreenName)]
	if f != nil && same(f.wanted, st) {
		return f
	}
	return nil
}

// Execute shares one candidate, including slot waiting, collection and storage.
// No mutex or image admission lease is held while waiting for Kubernetes work.
// Joining and candidate changes cannot extend the supplied absolute deadline.
func (r *Reconciler) Execute(ctx context.Context, req ExecutionRequest) (metadata artifact.Metadata, err error) {
	if err := ctx.Err(); err != nil {
		return artifact.Metadata{}, err
	}
	if req.Timeout == 0 {
		req.Timeout = 5 * time.Second
	}
	if req.StartedAt.IsZero() || req.RenderDeadline.IsZero() || req.Timeout < time.Second || req.Timeout > 10*time.Second || req.Timeout%time.Second != 0 {
		return artifact.Metadata{}, errors.New("invalid execution budget")
	}
	started := r.now() // Includes slot waiting and immutable-context preparation.
	deadline := minTime(req.RenderDeadline, req.StartedAt.Add(10*time.Second), started.Add(req.Timeout), started.Add(time.Duration(r.Config.DeadlineSeconds)*time.Second))
	if parent, ok := ctx.Deadline(); ok {
		deadline = minTime(deadline, parent)
	}
	if !started.Before(deadline) {
		return artifact.Metadata{}, &ExecutionError{Reason: "JobTimedOut", cause: context.DeadlineExceeded}
	}
	waitCtx, stopWaiting := context.WithDeadline(ctx, deadline)
	defer stopWaiting()
	var shared *execution
	defer func() {
		// Only our own expired budget is a candidate timeout. An API/storage
		// operation's independent timeout must remain an infrastructure error.
		var candidate *ExecutionError
		if errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &candidate) && (errors.Is(waitCtx.Err(), context.DeadlineExceeded) || shared != nil && errors.Is(shared.ctx.Err(), context.DeadlineExceeded)) {
			err = &ExecutionError{Reason: "JobTimedOut", cause: err}
		}
	}()
	if err := r.mu.LockContext(waitCtx); err != nil {
		return artifact.Metadata{}, err
	}
	if r.stopping || r.lifecycle != nil && r.lifecycle.Err() != nil {
		r.mu.Unlock()
		return artifact.Metadata{}, context.Canceled
	}
	d, _, wanted, err := r.desired(waitCtx, req.Device, req.Screen)
	if err == nil && (wanted == nil || wanted.Trigger != OnDemand || wanted.Unit != req.Unit || wanted.Revision != req.Revision) {
		err = &ExecutionError{Reason: "ObsoleteAttempt"}
	}
	if err == nil && req.Assignment != "" && (wanted.Assignment != req.Assignment || wanted.DeviceGeneration != req.DeviceGeneration) {
		err = &ExecutionError{Reason: "ObsoleteAttempt"}
	}
	if err != nil {
		r.mu.Unlock()
		return artifact.Metadata{}, err
	}
	key := pairKey(req.Device.Namespace, req.Device.Name, req.Screen)
	f := r.flights[key]
	if f != nil && !same(f.wanted, wanted) {
		r.mu.Unlock()
		return artifact.Metadata{}, &ExecutionError{Reason: "ObsoleteAttempt"}
	}
	if f == nil {
		snapshot, err := render.Build(d.Name, wanted.Unit.Target, devicestate.Observation(d.Status), req.Observation)
		if err != nil {
			r.mu.Unlock()
			return artifact.Metadata{}, err
		}
		if req.Snapshot != nil {
			if req.Snapshot.Version != "v1" || req.Snapshot.Device.Name != d.Name || req.Snapshot.RenderTarget != wanted.Unit.Target {
				r.mu.Unlock()
				return artifact.Metadata{}, errors.New("invalid execution snapshot")
			}
			snapshot = *req.Snapshot
		}
		b, err := render.MarshalContext(snapshot)
		if err != nil {
			r.mu.Unlock()
			return artifact.Metadata{}, err
		}
		st, loadErr := r.Store.load(req.Device.Namespace, req.Device.Name, req.Screen)
		if loadErr == nil && same(st, wanted) && st.Active == nil {
			st.Reason = ""
			loadErr = r.Store.save(st)
		}
		if loadErr != nil {
			r.mu.Unlock()
			return artifact.Metadata{}, loadErr
		}
		work, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		f = &execution{wanted: wanted, ctx: work, cancel: cancel, done: make(chan struct{}), snapshot: b, started: started}
		if r.lifecycle != nil {
			f.stop = context.AfterFunc(r.lifecycle, cancel)
		}
		if r.flights == nil {
			r.flights = map[string]*execution{}
		}
		r.flights[key] = f
		f.waiters.Store(1)
		r.workers.Add(1)
		go r.runExecution(key, f)
	} else {
		f.waiters.Add(1)
	}
	r.mu.Unlock()
	shared = f
	defer func() {
		if f.waiters.Add(-1) == 0 {
			f.cancel()
		}
	}()
	select {
	case <-waitCtx.Done():
		return artifact.Metadata{}, waitCtx.Err()
	case <-f.done:
		if !r.now().Before(deadline) {
			return artifact.Metadata{}, context.DeadlineExceeded
		}
		if err := waitCtx.Err(); err != nil {
			return artifact.Metadata{}, err
		}
		return f.metadata, f.err
	}
}

func minTime(values ...time.Time) time.Time {
	result := values[0]
	for _, value := range values[1:] {
		if value.Before(result) {
			result = value
		}
	}
	return result
}

func (r *Reconciler) executionFailureReason(ctx context.Context, deadline time.Time) string {
	// The last waiter can cancel at the same instant as the deadline timer.
	// Match reconciliation's absolute-deadline precedence in either ordering.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || !r.now().Before(deadline):
		return "JobTimedOut"
	case ctx.Err() != nil:
		return "RequestCancelled"
	default:
		return "ExecutionFailed"
	}
}

func (r *Reconciler) runExecution(key string, f *execution) {
	defer r.workers.Done()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	defer f.cancel()
	defer func() {
		if f.stop != nil {
			f.stop()
		}
	}()
	for {
		r.mu.Lock()
		st, err := r.Store.load(f.wanted.Namespace, f.wanted.DeviceName, f.wanted.ScreenName)
		if err == nil && f.ctx.Err() != nil {
			if f.metadata.AttemptID == "" && st != nil && st.Active != nil && st.Active.ID == f.id && st.Active.TerminalReason == "" {
				st.Active.TerminalReason = r.executionFailureReason(f.ctx, st.Active.Deadline)
				err = r.Store.save(st)
			}
			if err == nil {
				err = f.ctx.Err()
			}
		}
		if err == nil {
			// Reconciliation owns all transitions, including collection. The
			// waiter never races it to read or publish output.
			_, err = r.reconcilePair(f.ctx, clientRequest(f.wanted), f.wanted.ScreenName)
		}
		// A transition may save successfully and then fail during status update.
		// Never overwrite that durable state with the pre-transition snapshot.
		current, loadErr := r.Store.load(f.wanted.Namespace, f.wanted.DeviceName, f.wanted.ScreenName)
		if loadErr != nil {
			err = loadErr
		} else {
			st = current
		}
		if err == nil {
			if err == nil && !same(st, f.wanted) {
				_, _, current, desiredErr := r.desired(f.ctx, clientRequest(f.wanted).NamespacedName, f.wanted.ScreenName)
				err = desiredErr
				if err == nil && !same(current, f.wanted) {
					err = &ExecutionError{Reason: "ObsoleteAttempt"}
				}
			}
			if err == nil && f.attempted && st != nil {
				reason := st.Reason
				if st.Active != nil && st.Active.ID == f.id {
					reason = st.Active.TerminalReason
				}
				if reason != "" {
					err = &ExecutionError{Reason: reason}
				}
			}
		}
		if err != nil || f.metadata.AttemptID != "" {
			// Persist cancellation/error before allowing a later request to
			// replace this flight. Cleanup runs independently of its waiters.
			if err != nil && f.metadata.AttemptID == "" && st != nil && st.Active != nil && st.Active.ID == f.id && st.Active.TerminalReason == "" {
				// Cancellation can arrive inside reconciliation's API/storage
				// work, after the check at the start of this iteration.
				st.Active.TerminalReason = r.executionFailureReason(f.ctx, st.Active.Deadline)
				if saveErr := r.Store.save(st); saveErr != nil {
					err = saveErr
				}
			}
			f.err = err
			delete(r.flights, key)
			close(f.done)
			r.mu.Unlock()
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = r.Reconcile(cleanup, clientRequest(f.wanted))
			cancel()
			return
		}
		r.mu.Unlock()
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
		}
	}
}

func (r *Reconciler) stopExecutions() {
	r.mu.Lock()
	r.stopping = true
	for _, f := range r.flights {
		f.cancel()
	}
	r.mu.Unlock()
	r.workers.Wait()
}

func (r *Reconciler) published(ctx context.Context, st *state) (artifact.Metadata, error) {
	var m artifact.Metadata
	err := imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		var err error
		m, err = r.Repository.Latest(ctx, st.Unit)
		return err
	})
	if errors.Is(err, artifact.ErrNotFound) {
		err = nil
	}
	return m, err
}

// activeSlots includes stopping Jobs and orphaned Pods until they disappear.
// A terminal Job with a live Pod still consumes execution capacity.
func (r *Reconciler) activeSlots(ctx context.Context) (int, error) {
	var jobs batchv1.JobList
	if err := r.Reader.List(ctx, &jobs, client.InNamespace(r.Config.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
		return 0, err
	}
	ids := map[string]bool{}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		complete, failed := terminal(job)
		if !job.DeletionTimestamp.IsZero() || (!complete && !failed) {
			ids[job.Name] = true
		}
	}
	var pods corev1.PodList
	if err := r.Reader.List(ctx, &pods, client.InNamespace(r.Config.Namespace), client.MatchingLabels{managedLabel: "true"}); err != nil {
		return 0, err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed || !pod.DeletionTimestamp.IsZero() {
			ids[pod.Labels["batch.kubernetes.io/job-name"]] = true
		}
	}
	records, err := r.Store.records()
	if err != nil {
		return 0, err
	}
	for _, st := range records {
		if st.Active != nil && st.Active.TerminalReason != "" {
			remaining, err := r.pods(ctx, &st)
			if err != nil {
				return 0, err
			}
			if len(remaining) > 0 {
				ids["arprt-render-"+st.Active.ID] = true
			}
		}
	}
	return len(ids), nil
}
