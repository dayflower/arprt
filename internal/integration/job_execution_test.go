//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestJobExecutionWatchCancellationAndRecovery(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("run make integration")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "execution-test"}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*jobrender.Reconciler, func()) {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"execution-test": {}}}, Controller: config.Controller{SkipNameValidation: ptr.To(true)}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		if err != nil {
			t.Fatal(err)
		}
		store, err := jobrender.NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		r := &jobrender.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Store: store, Repository: repo, Config: jobrender.Config{Namespace: "execution-test", PVC: "data", DeadlineSeconds: 120, MaxActive: 2}, TriggerFor: func(*api.ArprtScreen) jobrender.Trigger { return jobrender.OnDemand }}
		if err := r.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Add(r); err != nil {
			t.Fatal(err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(runCtx) }()
		if !mgr.GetCache().WaitForCacheSync(runCtx) {
			t.Fatal("cache did not start")
		}
		return r, func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("manager shutdown timed out")
			}
		}
	}
	r, stop := start()
	defer func() { stop() }()
	s := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "execution-test", Name: "screen"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "busybox:1.37.0"}}}}}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: "device"}, Spec: api.ArprtDeviceSpec{ScreenRef: &api.LocalReference{Name: s.Name}, AuthSecretRef: api.LocalReference{Name: "auth"}}}
	if err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status.Device = &api.DeviceInfoStatus{Model: "og"}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	// The policy hook enables only the internal execution path. No new CRD
	// fields or normal request routing are exposed by this phase.
	time.Sleep(100 * time.Millisecond)
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs, client.InNamespace(s.Namespace)); err != nil || len(jobs.Items) != 0 {
		t.Fatal("autonomous on-demand execution", err)
	}
	target := render.CurrentTarget(render.Target{Width: 800, Height: 480, Palette: "gray4"})
	u := artifact.Unit{Namespace: d.Namespace, DeviceUID: string(d.UID), ScreenUID: string(s.UID), Target: target.Target, Output: target.Output}
	revision, err := screen.InputRevision(s.Spec)
	if err != nil {
		t.Fatal(err)
	}
	request := func() jobrender.ExecutionRequest {
		started := time.Now()
		return jobrender.ExecutionRequest{Device: client.ObjectKeyFromObject(d), Screen: s.Name, Unit: u, Revision: revision, StartedAt: started, RenderDeadline: started.Add(10 * time.Second)}
	}
	type result struct {
		m   artifact.Metadata
		err error
	}
	execute := func(callCtx context.Context) <-chan result {
		done := make(chan result, 1)
		req := request()
		go func() { m, err := r.Execute(callCtx, req); done <- result{m, err} }()
		return done
	}
	first := execute(ctx)
	eventually(t, func() bool { return c.List(ctx, &jobs, client.InNamespace(s.Namespace)) == nil && len(jobs.Items) == 1 })
	job := jobs.Items[0]
	id := strings.TrimPrefix(job.Name, "arprt-render-")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: job.Name, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name, "arprt.dayflower.github.io/renderer": "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}}, Spec: *job.Spec.Template.Spec.DeepCopy()}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "renderer", Image: "busybox:1.37.0", ImageID: "fixture", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs", id, "output", "output.png"), b, 0600); err != nil {
		t.Fatal(err)
	}
	job.Status.StartTime = ptr.To(metav1.Now())
	job.Status.CompletionTime = ptr.To(metav1.Now())
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}, {Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-first:
		if result.err != nil || result.m.AttemptID != id {
			t.Fatal(result)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("completion watch did not publish")
	}
	// A second request is cancelled while a Pod still owns its output mount.
	callCtx, cancel := context.WithCancel(ctx)
	second := execute(callCtx)
	eventually(t, func() bool { return c.List(ctx, &jobs, client.InNamespace(s.Namespace)) == nil && len(jobs.Items) == 2 })
	var stopping batchv1.Job
	for _, candidate := range jobs.Items {
		if candidate.Name != job.Name {
			stopping = candidate
		}
	}
	stoppingPod := pod.DeepCopy()
	stoppingPod.ResourceVersion = ""
	stoppingPod.UID = ""
	stoppingPod.Name = stopping.Name
	stoppingPod.OwnerReferences[0].Name = stopping.Name
	stoppingPod.OwnerReferences[0].UID = stopping.UID
	stoppingPod.Labels["batch.kubernetes.io/job-name"] = stopping.Name
	stoppingPod.Status = corev1.PodStatus{}
	if err := c.Create(ctx, stoppingPod); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case result := <-second:
		if result.err != context.Canceled {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter waited for Pod deletion")
	}
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(&stopping), &stopping) == nil && !stopping.DeletionTimestamp.IsZero()
	})
	// Envtest supplies no kubelet or garbage collector. Restart must retain the
	// run while this real API Pod remains, and must not publish its late bytes.
	stop()
	r, stop = start()
	stoppingID := strings.TrimPrefix(stopping.Name, "arprt-render-")
	if err := os.WriteFile(filepath.Join(root, "runs", stoppingID, "output", "output.png"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.Latest(ctx, u)
	if err != nil || latest.AttemptID != id {
		t.Fatal("restart published cancelled output", latest, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", stoppingID)); err != nil {
		t.Fatal("GC removed mounted run", err)
	}
	if err := c.List(ctx, &jobs, client.InNamespace(s.Namespace)); err != nil || len(jobs.Items) != 2 {
		t.Fatal("restart created replacement", err)
	}
	// Emulate GC explicitly, then observe the deletion watch clear the attempt.
	if err := c.Delete(ctx, stoppingPod); err != nil {
		t.Fatal(err)
	}
	stopping.Finalizers = nil
	if err := c.Update(ctx, &stopping); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return c.List(ctx, &jobs, client.InNamespace(s.Namespace)) == nil && len(jobs.Items) == 1 })
}
