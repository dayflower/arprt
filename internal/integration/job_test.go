//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestJobWatchAndRestart(t *testing.T) {
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
	ctx := context.Background()
	if err = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "job-test"}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	start := func() func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"job-test": {}}}, Controller: config.Controller{SkipNameValidation: ptr.To(true)}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		if err != nil {
			t.Fatal(err)
		}
		store, err := jobrender.NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := artifact.NewPVC(root)
		if err != nil {
			t.Fatal(err)
		}
		r := &jobrender.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Repository: recovered, Store: store, Config: jobrender.Config{Namespace: "job-test", PVC: "data", DeadlineSeconds: 120, MaxActive: 2}}
		if err = r.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(runCtx) }()
		return func() {
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
	stop := start()
	s := &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Name: "screen", Namespace: "job-test"}, Spec: api.TrmnlScreenSpec{RefreshInterval: "5m", Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "busybox:1.37.0", Command: []string{"sh", "-c"}, Args: []string{"exit 0"}}}}}}
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Name: "device", Namespace: "job-test"}, Spec: api.TrmnlDeviceSpec{ScreenRef: &api.LocalReference{Name: "screen"}, AuthSecretRef: api.LocalReference{Name: "auth"}}}
	if err = c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	var jobs batchv1.JobList
	if err = c.List(ctx, &jobs, client.InNamespace(d.Namespace)); err != nil || len(jobs.Items) != 0 {
		t.Fatal("rendered without target", err)
	}
	d.Status.Display = &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}
	if err = c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return c.List(ctx, &jobs, client.InNamespace(d.Namespace)) == nil && len(jobs.Items) == 1 })
	job := jobs.Items[0]
	originalUID := job.UID
	id := strings.TrimPrefix(job.Name, "arprt-render-")
	if _, err = os.Stat(filepath.Join(root, "runs", id, "context.json")); err != nil {
		t.Fatal("context not persisted before Job", err)
	}
	stop()
	stop = start()
	defer stop()
	eventually(t, func() bool {
		return c.List(ctx, &jobs, client.InNamespace(d.Namespace)) == nil && len(jobs.Items) == 1 && jobs.Items[0].UID == originalUID
	})
	// API integration emulates kubelet termination; cluster smoke exercises the
	// actual scheduler, mounts, container and garbage collector separately.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name, Namespace: d.Namespace, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}}, Spec: *job.Spec.Template.Spec.DeepCopy()}
	if err = c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "renderer", Image: "busybox:1.37.0", ImageID: "fixture", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}
	if err = c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "runs", id, "output", "output.png"), b, 0600); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}, {Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}}
	job.Status.StartTime = ptr.To(metav1.Now())
	job.Status.CompletionTime = ptr.To(metav1.Now())
	if err = c.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), DeviceUID: string(d.UID), Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	var m artifact.Metadata
	eventually(t, func() bool { m, err = repo.Latest(ctx, u); return err == nil })
	contextBytes, err := os.ReadFile(filepath.Join(root, "runs", id, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot render.Context
	if err = json.Unmarshal(contextBytes, &snapshot); err != nil || snapshot.Device.Name != d.Name || snapshot.RenderTarget != u.Target || m.AttemptID != id || m.Sequence != 1 {
		t.Fatal("Job context/artifact contract", err)
	}
	if _, err = repo.Read(ctx, m.ImageDigest); err != nil {
		t.Fatal(err)
	}
	// Screen creation must wake discovery devices without another display poll.
	editAssignment := func(fn func(*api.TrmnlDevice)) {
		t.Helper()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				return err
			}
			fn(d)
			return c.Update(ctx, d)
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Changing desired rotation wakes Job scheduling without another display poll
	// or waiting for the old 5-minute freshness interval.
	editAssignment(func(current *api.TrmnlDevice) { current.Spec.Rotation = 90 })
	eventually(t, func() bool {
		if c.List(ctx, &jobs, client.InNamespace(d.Namespace)) != nil {
			return false
		}
		for _, candidate := range jobs.Items {
			if candidate.UID == originalUID || !candidate.DeletionTimestamp.IsZero() {
				continue
			}
			runID := strings.TrimPrefix(candidate.Name, "arprt-render-")
			b, err := os.ReadFile(filepath.Join(root, "runs", runID, "context.json"))
			var rotated render.Context
			if err == nil && json.Unmarshal(b, &rotated) == nil && rotated.RenderTarget.Rotation == 90 {
				return rotated.RenderTarget.Width == 800 && rotated.RenderTarget.Height == 480
			}
		}
		return false
	})
	// Emulate cancellation of the prepared render before discovery coverage;
	// envtest has no garbage collector for foreground Job deletion.
	editAssignment(func(current *api.TrmnlDevice) { current.Spec.Rotation = 0 })
	eventually(t, func() bool {
		if c.List(ctx, &jobs, client.InNamespace(d.Namespace)) != nil {
			return false
		}
		for i := range jobs.Items {
			candidate := &jobs.Items[i]
			if candidate.UID != originalUID {
				if candidate.DeletionTimestamp.IsZero() {
					return false
				}
				candidate.Finalizers = nil
				if c.Update(ctx, candidate) != nil {
					return false
				}
			}
		}
		return true
	})

	editAssignment(func(current *api.TrmnlDevice) { current.Spec.ScreenRef = nil })
	second := s.DeepCopy()
	second.Name = "second"
	second.ResourceVersion = ""
	second.UID = ""
	second.Spec.Source.Job.Renderer.Args = []string{"exit 0 # discovered"}
	if err := c.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	hasAttempt := func(marker string) bool {
		if c.List(ctx, &jobs, client.InNamespace(d.Namespace)) != nil {
			return false
		}
		for _, candidate := range jobs.Items {
			if candidate.Spec.Template.Spec.Containers[0].Args[0] == marker && candidate.DeletionTimestamp.IsZero() {
				return true
			}
		}
		return false
	}
	eventually(t, func() bool { return hasAttempt("exit 0 # discovered") })
	p := &api.TrmnlPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "cycle"}, Spec: api.TrmnlPlaylistSpec{Screens: []api.LocalReference{{Name: s.Name}, {Name: second.Name}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	editAssignment(func(current *api.TrmnlDevice) { current.Spec.PlaylistRef = &api.LocalReference{Name: p.Name} })
	third := s.DeepCopy()
	third.Name = "third"
	third.ResourceVersion = ""
	third.UID = ""
	third.Spec.Source.Job.Renderer.Args = []string{"exit 0 # playlist"}
	if err := c.Create(ctx, third); err != nil {
		t.Fatal(err)
	}
	p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: third.Name})
	if err := c.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return hasAttempt("exit 0 # playlist") })
}
