package jobrender

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/render"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type executionResult struct {
	m   artifact.Metadata
	err error
}

func demandFixture(t *testing.T) (*Reconciler, *api.ArprtDevice, ExecutionRequest) {
	t.Helper()
	r, d, _, _ := fixture(t)
	r.Now = nil
	r.TriggerFor = func(*api.ArprtScreen) Trigger { return OnDemand }
	t.Cleanup(r.stopExecutions)
	_, _, wanted, err := r.desired(t.Context(), client.ObjectKeyFromObject(d), "screen")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	req := ExecutionRequest{Device: client.ObjectKeyFromObject(d), Screen: "screen", Unit: wanted.Unit, Revision: wanted.Revision, StartedAt: started, RenderDeadline: started.Add(10 * time.Second)}
	return r, d, req
}

func executeAsync(ctx context.Context, r *Reconciler, req ExecutionRequest) <-chan executionResult {
	done := make(chan executionResult, 1)
	go func() { m, err := r.Execute(ctx, req); done <- executionResult{m, err} }()
	return done
}

func awaitExecution(t *testing.T, r *Reconciler, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ok := condition()
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("execution state did not converge")
}

func awaitAttempt(t *testing.T, r *Reconciler, d *api.ArprtDevice) *state {
	t.Helper()
	var st *state
	awaitExecution(t, r, func() bool { st = load(t, r, d); return st != nil && st.Active != nil && st.Active.Submitted })
	return st
}

func executionDone(t *testing.T, done <-chan executionResult) executionResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(4 * time.Second):
		t.Fatal("execution did not stop")
		return executionResult{}
	}
}

func TestExecutionSharedWaitersAndIndependentRequests(t *testing.T) {
	r, d, req := demandFixture(t)
	counter := &countPublication{Repository: r.Repository}
	r.Repository = counter
	output := png(t)
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	first := executeAsync(firstCtx, r, req)
	initial := awaitAttempt(t, r, d)
	second := executeAsync(t.Context(), r, req)
	awaitExecution(t, r, func() bool { return r.flight(initial).waiters.Load() == 2 })
	shortCtx, cancelShort := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancelShort()
	short := executeAsync(shortCtx, r, req)
	if result := executionDone(t, short); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	cancelFirst()
	if result := executionDone(t, first); !errors.Is(result.err, context.Canceled) {
		t.Fatal(result)
	}
	r.mu.Lock()
	current := load(t, r, d)
	if current.Active.ID != initial.Active.ID || !current.Active.Deadline.Equal(initial.Active.Deadline) {
		t.Fatal("join replaced or extended the attempt")
	}
	finish(t, r, d, output, false)
	r.mu.Unlock()
	step(t, r, d) // A watch reconciliation and the waiter share one collector.
	result := executionDone(t, second)
	if result.err != nil || result.m.AttemptID != initial.Active.ID || counter.calls.Load() != 1 {
		t.Fatal(result)
	}
	// Reconciliation alone neither refreshes nor retries on-demand work.
	step(t, r, d)
	r.mu.Lock()
	st := load(t, r, d)
	if st.Active != nil || !st.Due.IsZero() || st.Failures != 0 {
		t.Fatal(st)
	}
	deletePods(t, r)
	r.mu.Unlock()
	third := executeAsync(t.Context(), r, req)
	next := awaitAttempt(t, r, d)
	if next.Active.ID == initial.Active.ID || next.Active.Sequence != initial.Active.Sequence+1 {
		t.Fatal("independent request reused work")
	}
	r.mu.Lock()
	finish(t, r, d, output, false)
	r.mu.Unlock()
	if thirdResult := executionDone(t, third); thirdResult.err != nil || thirdResult.m.Filename != result.m.Filename {
		t.Fatal(thirdResult)
	}
	if counter.calls.Load() != 2 {
		t.Fatal("duplicate publication")
	}
}

type countPublication struct {
	artifact.Repository
	calls atomic.Int32
}

func (c *countPublication) Publish(ctx context.Context, m artifact.Metadata, b []byte) (artifact.Metadata, error) {
	c.calls.Add(1)
	return c.Repository.Publish(ctx, m, b)
}

func TestExecutionSnapshotPrecedesSlotWaitingAndJoinKeepsOwnCutoff(t *testing.T) {
	r, d, req := demandFixture(t)
	r.Config.MaxActive = 1
	blocker := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "blocker", Namespace: d.Namespace, Labels: map[string]string{managedLabel: "true", "batch.kubernetes.io/job-name": "unrelated"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Client.Create(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	req.Observation.Voltage = ptr.To(3.8)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := executeAsync(ctx, r, req)
	awaitExecution(t, r, func() bool { return len(r.flights) == 1 })
	joined := req
	joined.RenderDeadline = time.Now().Add(50 * time.Millisecond)
	joined.Observation.Voltage = ptr.To(3.1)
	if result := executionDone(t, executeAsync(t.Context(), r, joined)); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Status.Battery.Voltage = ptr.To(3.2)
	if err := r.Client.Status().Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := r.Client.Delete(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	st := awaitAttempt(t, r, d)
	b, err := os.ReadFile(filepath.Join(r.Store.run(st.Active.ID), "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot render.Context
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	if *snapshot.Device.Battery.Voltage != 3.8 {
		t.Fatal("waiting or joined request changed immutable snapshot")
	}
	cancel()
	if result := executionDone(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatal(result)
	}
}

func TestExecutionAbsoluteCutoffIncludesPriorWorkAndSlotWaiting(t *testing.T) {
	r, d, req := demandFixture(t)
	r.Config.MaxActive = 1
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: d.Namespace, Labels: map[string]string{managedLabel: "true"}}}
	if err := r.Client.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: d.Namespace, Labels: map[string]string{managedLabel: "true", "batch.kubernetes.io/job-name": job.Name}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Client.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	req.StartedAt = time.Now().Add(-9800 * time.Millisecond)
	start := time.Now()
	done := executeAsync(t.Context(), r, req)
	awaitExecution(t, r, func() bool { return len(r.flights) == 1 })
	// Maintenance and image admission must progress while the execution waits.
	if err := r.maintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := imageprocessing.RuntimeAdmission.Do(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	result := executionDone(t, done)
	var candidate *ExecutionError
	if !errors.Is(result.err, context.DeadlineExceeded) || !errors.As(result.err, &candidate) || candidate.Reason != "JobTimedOut" || time.Since(start) > time.Second {
		t.Fatal("slot wait extended the cutoff", result)
	}
	r.mu.Lock()
	st := load(t, r, d)
	runs, err := os.ReadDir(filepath.Join(r.Store.Root, "runs"))
	r.mu.Unlock()
	if err != nil || len(runs) != 0 || st.Active != nil {
		t.Fatal("slot wait allocated a run", st, err)
	}
	req.StartedAt = time.Now().Add(-11 * time.Second)
	if _, err := r.Execute(t.Context(), req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("spent authentication budget launched work", err)
	}
}

type deadlineAPIReader struct{ client.Reader }

type interruptedJobReader struct {
	client.Reader
	armed  atomic.Bool
	cancel context.CancelFunc
}

func (r *interruptedJobReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, job := obj.(*batchv1.Job); job && r.armed.CompareAndSwap(true, false) {
		if r.cancel != nil {
			r.cancel()
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestExecutionInterruptionDuringReconciliationRetainsReason(t *testing.T) {
	for _, boundary := range []string{"cancel", "deadline", "cancel-at-deadline"} {
		t.Run(boundary, func(t *testing.T) {
			reason := "JobTimedOut"
			if boundary == "cancel" {
				reason = "RequestCancelled"
			}
			r, d, req := demandFixture(t)
			reader := &interruptedJobReader{Reader: r.Reader}
			r.Reader = reader
			if boundary == "deadline" {
				req.Timeout = time.Second
			}
			done := executeAsync(t.Context(), r, req)
			initial := awaitAttempt(t, r, d)
			r.mu.Lock()
			if boundary == "cancel" {
				reader.cancel = r.flight(initial).cancel
			} else if boundary == "cancel-at-deadline" {
				cancel := r.flight(initial).cancel
				reader.cancel = func() {
					r.Now = func() time.Time { return initial.Active.Deadline }
					cancel()
				}
			}
			reader.armed.Store(true)
			r.mu.Unlock()
			result := executionDone(t, done)
			if !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
				t.Fatal(result)
			}
			r.mu.Lock()
			st := load(t, r, d)
			actual := st.Reason
			if st.Active != nil {
				actual = st.Active.TerminalReason
			}
			r.mu.Unlock()
			if actual != reason {
				t.Fatalf("durable reason = %q, want %q", actual, reason)
			}
		})
	}
}

func (r deadlineAPIReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return context.DeadlineExceeded
}

func TestExecutionInfrastructureTimeoutIsNotCandidateFailure(t *testing.T) {
	r, _, req := demandFixture(t)
	r.Reader = deadlineAPIReader{r.Reader}
	_, err := r.Execute(t.Context(), req)
	var candidate *ExecutionError
	if err != context.DeadlineExceeded || errors.As(err, &candidate) {
		t.Fatal("API timeout became a skippable rendering failure", err)
	}
}

func TestExecutionLockWaitingCannotSpendFinalizationReserve(t *testing.T) {
	r, _, req := demandFixture(t)
	req.StartedAt = time.Now().Add(-9900 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	started := time.Now()
	result := executionDone(t, executeAsync(t.Context(), r, req))
	if !errors.Is(result.err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("operation lock extended render waiting", result)
	}
}

func TestExecutionCandidateFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		output []byte
		failed bool
		reason string
	}{
		{"missing", nil, false, "CollectionFailed"},
		{"invalid", []byte("invalid PNG"), false, "CollectionFailed"},
		{"exit", nil, true, "JobFailed"},
		{"renderer", nil, false, "RendererNotSuccessful"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, d, req := demandFixture(t)
			done := executeAsync(t.Context(), r, req)
			st := awaitAttempt(t, r, d)
			r.mu.Lock()
			finish(t, r, d, test.output, test.failed)
			if test.name == "renderer" {
				var pod corev1.Pod
				if err := r.Client.Get(t.Context(), client.ObjectKey{Namespace: d.Namespace, Name: "arprt-render-" + st.Active.ID}, &pod); err != nil {
					t.Fatal(err)
				}
				pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
				if err := r.Client.Status().Update(t.Context(), &pod); err != nil {
					t.Fatal(err)
				}
			}
			r.mu.Unlock()
			result := executionDone(t, done)
			var candidate *ExecutionError
			if !errors.As(result.err, &candidate) || candidate.Reason != test.reason {
				t.Fatal(result)
			}
			if _, err := r.Repository.Latest(t.Context(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("failed output published", err)
			}
		})
	}
}

func TestExecutionLastWaiterStopsWithoutWaitingForPodDeletion(t *testing.T) {
	r, d, req := demandFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := executeAsync(ctx, r, req)
	initial := awaitAttempt(t, r, d)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "lingering", Namespace: d.Namespace, Labels: map[string]string{managedLabel: "true", "batch.kubernetes.io/job-name": "arprt-render-" + initial.Active.ID}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Client.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	cancel()
	if result := executionDone(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatal(result)
	}
	awaitExecution(t, r, func() bool {
		st := load(t, r, d)
		return st.Active != nil && st.Active.TerminalReason != "" && len(r.flights) == 0
	})
	r.mu.Lock()
	st := load(t, r, d)
	if slots, err := r.activeSlots(t.Context()); err != nil || slots != 1 {
		t.Fatal("stopping Pod released its slot", slots, err)
	}
	if err := os.Chtimes(r.Store.run(st.Active.ID), time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r.mu.Unlock()
	if err := r.maintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Store.run(st.Active.ID)); err != nil {
		t.Fatal("GC removed mounted output", err)
	}
	// A replacement request spends its own budget waiting, with no new Job.
	req.StartedAt = time.Now().Add(-9800 * time.Millisecond)
	next := executeAsync(t.Context(), r, req)
	if result := executionDone(t, next); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	r.mu.Lock()
	current := load(t, r, d)
	if current.Active.ID != initial.Active.ID {
		t.Fatal("lingering Pod allowed replacement")
	}
	r.mu.Unlock()
	if _, err := r.Repository.Latest(t.Context(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestExecutionLateCompletionCannotPublish(t *testing.T) {
	r, d, req := demandFixture(t)
	req.StartedAt = time.Now().Add(-9700 * time.Millisecond)
	done := executeAsync(t.Context(), r, req)
	st := awaitAttempt(t, r, d)
	result := executionDone(t, done)
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	r.mu.Lock()
	// Model a delayed API completion before asynchronous deletion can finish.
	job := r.Config.job(st)
	job.Finalizers = []string{"example.test/hold"}
	_ = r.Client.Create(t.Context(), job)
	current := load(t, r, d)
	if current.Active == nil {
		current.Active = st.Active
		current.Active.TerminalReason = "JobTimedOut"
		if err := r.Store.save(current); err != nil {
			t.Fatal(err)
		}
	}
	finish(t, r, d, png(t), false)
	r.mu.Unlock()
	step(t, r, d)
	if _, err := r.Repository.Latest(t.Context(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("late completion published", err)
	}
}

type slowPublication struct{ artifact.Repository }

func (s slowPublication) Publish(ctx context.Context, _ artifact.Metadata, _ []byte) (artifact.Metadata, error) {
	<-ctx.Done()
	return artifact.Metadata{}, ctx.Err()
}

type failedPublication struct {
	artifact.Repository
	failure error
}

func (s failedPublication) Publish(context.Context, artifact.Metadata, []byte) (artifact.Metadata, error) {
	return artifact.Metadata{}, s.failure
}

func TestExecutionStorageFailureRemainsInfrastructureError(t *testing.T) {
	r, d, req := demandFixture(t)
	injected := errors.New("injected storage failure")
	r.Repository = failedPublication{r.Repository, injected}
	done := executeAsync(t.Context(), r, req)
	awaitAttempt(t, r, d)
	r.mu.Lock()
	finish(t, r, d, png(t), false)
	r.mu.Unlock()
	if result := executionDone(t, done); !errors.Is(result.err, injected) {
		t.Fatal("storage failure became a candidate failure", result)
	}
}

func TestExecutionCapacityFailureDoesNotBecomeTimeoutOrRetry(t *testing.T) {
	r, d, req := demandFixture(t)
	r.Repository.(*artifact.PVC).MaxBytes = 1
	if result := executionDone(t, executeAsync(t.Context(), r, req)); !errors.Is(result.err, artifact.ErrCapacity) {
		t.Fatal(result)
	}
	r.mu.Lock()
	st := load(t, r, d)
	r.mu.Unlock()
	if st != nil && (st.Active != nil || st.Failures != 0 || !st.Due.IsZero()) {
		t.Fatal("capacity failure reserved a retry", st)
	}
}

func TestExecutionStorageIsInsideCandidateDeadline(t *testing.T) {
	r, d, req := demandFixture(t)
	r.Repository = slowPublication{r.Repository}
	req.StartedAt = time.Now().Add(-9 * time.Second)
	done := executeAsync(t.Context(), r, req)
	st := awaitAttempt(t, r, d)
	r.mu.Lock()
	finish(t, r, d, png(t), false)
	r.mu.Unlock()
	if result := executionDone(t, done); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	awaitExecution(t, r, func() bool { st := load(t, r, d); return st.Active == nil || st.Active.TerminalReason != "" })
	if _, err := r.Repository.Latest(t.Context(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("late storage accepted", err)
	}
}

func TestExecutionRestartCancelsUncommittedAndKeepsDurableSuccess(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "published"}[published], func(t *testing.T) {
			r, d, req := demandFixture(t)
			// Prepare through the common scheduler, then turn its original record
			// into a persisted on-demand attempt with no in-memory waiters.
			r.TriggerFor = nil
			step(t, r, d)
			st := load(t, r, d)
			st.Trigger = OnDemand
			r.TriggerFor = func(*api.ArprtScreen) Trigger { return OnDemand }
			_, _, wanted, err := r.desired(t.Context(), client.ObjectKeyFromObject(d), st.ScreenName)
			if err != nil {
				t.Fatal(err)
			}
			st.DeviceGeneration, st.Assignment = wanted.DeviceGeneration, wanted.Assignment
			st.Active.Trigger = OnDemand
			st.Active.Deadline = time.Now().Add(time.Second)
			if err := r.Store.save(st); err != nil {
				t.Fatal(err)
			}
			if published {
				_, err := r.Repository.Publish(t.Context(), artifact.Metadata{Unit: st.Unit, ScreenGeneration: st.ScreenGeneration, InputRevision: st.Revision, AttemptID: st.Active.ID, Sequence: st.Active.Sequence, ContextDigest: st.Active.ContextDigest, RenderedAt: time.Now()}, png(t))
				if err != nil {
					t.Fatal(err)
				}
			}
			r.TriggerFor = func(*api.ArprtScreen) Trigger { return OnDemand }
			if err := r.maintenance(t.Context()); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			current := load(t, r, d)
			if current.Active != nil || !current.Due.IsZero() || current.Failures != 0 {
				t.Fatal(current)
			}
			m, err := r.Repository.Latest(t.Context(), req.Unit)
			if published && (err != nil || m.AttemptID != st.Active.ID) {
				t.Fatal("durable success lost", err)
			}
			if !published && (!errors.Is(err, artifact.ErrNotFound) || current.Reason != "RequestLost") {
				t.Fatal("orphan resumed", current, err)
			}
		})
	}
}

func TestExecutionModeSwitchWaitsForOldPod(t *testing.T) {
	r, d, req := demandFixture(t)
	r.TriggerFor = nil
	step(t, r, d)
	old := load(t, r, d)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: d.Namespace, Labels: map[string]string{"batch.kubernetes.io/job-name": "arprt-render-" + old.Active.ID}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Client.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	r.TriggerFor = func(*api.ArprtScreen) Trigger { return OnDemand }
	req.StartedAt = time.Now().Add(-9800 * time.Millisecond)
	if result := executionDone(t, executeAsync(t.Context(), r, req)); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
	st := load(t, r, d)
	if st.Active == nil || st.Active.ID != old.Active.ID || st.Active.TerminalReason != "ObsoleteAttempt" {
		t.Fatal("switch replaced old Pod", st)
	}
	if err := r.Client.Delete(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	st = load(t, r, d)
	if st.Trigger != OnDemand || st.Active != nil {
		t.Fatal("switch started autonomous work", st)
	}
}

type cancelOnSlotReader struct {
	client.Reader
	cancel context.CancelFunc
}

func (r cancelOnSlotReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*batchv1.JobList); ok {
		r.cancel()
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestReconcileRetiredExecutionAfterOldPodDisappears(t *testing.T) {
	for _, boundary := range []string{"cancelled", "no-waiters", "slot-observation"} {
		t.Run(boundary, func(t *testing.T) {
			r, d, _ := demandFixture(t)
			r.TriggerFor = nil
			step(t, r, d)
			old := load(t, r, d)
			r.TriggerFor = func(*api.ArprtScreen) Trigger { return OnDemand }
			_, _, wanted, err := r.desired(t.Context(), client.ObjectKeyFromObject(d), old.ScreenName)
			if err != nil {
				t.Fatal(err)
			}
			work, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			f := &execution{wanted: wanted, ctx: work, cancel: cancel, started: time.Now()}
			f.waiters.Store(1)
			r.flights = map[string]*execution{pairKey(d.Namespace, d.Name, old.ScreenName): f}
			switch boundary {
			case "cancelled":
				cancel()
			case "no-waiters":
				f.waiters.Store(0)
			case "slot-observation":
				r.Reader = cancelOnSlotReader{Reader: r.Reader, cancel: cancel}
			}
			// The waiter has returned, but its worker has not removed the flight
			// yet. A watch can now observe that the old Pod is gone and finish the
			// mode transition. It must neither start new work nor report the old
			// request's cancellation as an infrastructure failure.
			step(t, r, d)
			current := load(t, r, d)
			if current.Trigger != OnDemand || current.Active != nil || current.Sequence != old.Sequence {
				t.Fatal("retired execution allocated an attempt", current)
			}
			var jobs batchv1.JobList
			if err := r.Client.List(t.Context(), &jobs); err != nil || len(jobs.Items) != 0 {
				t.Fatal("retired execution created a Job", jobs, err)
			}
		})
	}
}

func TestExecutionShortSafetyLimitWins(t *testing.T) {
	r, d, req := demandFixture(t)
	r.Config.DeadlineSeconds = 1
	done := executeAsync(t.Context(), r, req)
	st := awaitAttempt(t, r, d)
	if delta := st.Active.Deadline.Sub(st.Active.StartedAt); delta != time.Second {
		t.Fatal(delta)
	}
	job := r.Config.job(st)
	if *job.Spec.ActiveDeadlineSeconds != 1 || *job.Spec.BackoffLimit != 0 || ptr.Deref(job.Spec.Template.Spec.AutomountServiceAccountToken, true) {
		t.Fatal("Job safety contract")
	}
	if result := executionDone(t, done); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatal(result)
	}
}
