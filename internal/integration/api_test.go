//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

func TestAPIAndController(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("run make integration to provision API server binaries")
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
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"trmnl", "other"} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	validScreen := func(name string) *api.TrmnlScreen {
		return &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "trmnl"}, Spec: api.TrmnlScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render?key=a&key=b"}}}}
	}
	validDevice := func(name string) *api.TrmnlDevice {
		return &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "trmnl"}, Spec: api.TrmnlDeviceSpec{HardwareID: "02:00:00:00:00:01", AuthSecretRef: api.LocalReference{Name: "device-auth"}, ScreenRef: &api.LocalReference{Name: "photo"}}}
	}
	for i, mutation := range []struct {
		name   string
		mutate func(*api.TrmnlScreen)
	}{
		{"no-source", func(s *api.TrmnlScreen) { s.Spec.Source = api.ScreenSource{} }},
		{"two-sources", func(s *api.TrmnlScreen) {
			s.Spec.Source.Job = &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}}
		}},
		{"method", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.Method = "PUT" }},
		{"post-interval", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.Method = "POST"; s.Spec.RefreshInterval = "5m" }},
		{"interval-zero", func(s *api.TrmnlScreen) { s.Spec.RefreshInterval = "0s" }},
		{"interval-big", func(s *api.TrmnlScreen) { s.Spec.RefreshInterval = "24h1s" }},
		{"interval-fraction", func(s *api.TrmnlScreen) { s.Spec.RefreshInterval = "1.5s" }},
		{"interval-no-unit", func(s *api.TrmnlScreen) { s.Spec.RefreshInterval = "30" }},
		{"url-relative", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "/render" }},
		{"url-scheme", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "ftp://host/render" }},
		{"url-no-host", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "http:///render" }},
		{"url-userinfo", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "https://user:secret@host/render" }},
		{"url-fragment", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "https://host/render#fragment" }},
		{"url-escaping", func(s *api.TrmnlScreen) { s.Spec.Source.HTTP.URL = "https://host/render?x=%XX" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			s := validScreen(fmt.Sprintf("invalid-%d", i))
			mutation.mutate(s)
			if err := c.Create(ctx, s); !apierrors.IsInvalid(err) {
				t.Fatalf("expected schema rejection, got %v", err)
			}
		})
	}
	for i, paths := range []struct{ context, image string }{
		{"relative", "/work/out.png"}, {"/work/../context.json", "/work/out.png"}, {"/work/context.json", "/work//out.png"}, {"/work/context.json", "/work/out.png/"},
		{"/work/context.json", "/work/context.json"}, {"/work/context", "/work/context/out.png"}, {"/work/context/in.json", "/work/context"},
		{"/work/context.json", "/output.png"}, {"/work/context.json", "/usr/share/out.png"}, {"/work/context.json", "/etc/out.png"},
		{"/work/context.json", "/var/run/secrets/kubernetes.io/serviceaccount/out.png"}, {"/var/lib/arprt/context.json", "/work/out.png"}, {"/work/context.json", "/var/lib/arprt/out.png"},
		{"/work/context.json", "/var/lib/file"}, {"/work/context.json", "/"}, {"/work/./context.json", "/work/out.png"}, {"/work/\x00context.json", "/work/out.png"},
	} {
		t.Run(fmt.Sprintf("path-%d", i), func(t *testing.T) {
			s := validScreen(fmt.Sprintf("bad-path-%d", i))
			s.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}, ContextPath: paths.context, ImagePath: paths.image}}
			if err := c.Create(ctx, s); !apierrors.IsInvalid(err) {
				t.Fatalf("expected path rejection, got %v", err)
			}
		})
	}
	for i, mutation := range []struct {
		name   string
		mutate func(*api.TrmnlDevice)
	}{
		{"assignment-conflict", func(d *api.TrmnlDevice) { d.Spec.PlaylistRef = &api.LocalReference{Name: "playlist"} }},
		{"invalid-mac", func(d *api.TrmnlDevice) { d.Spec.HardwareID = "02:aa:00:00:00:01" }},
		{"missing-secret", func(d *api.TrmnlDevice) { d.Spec.AuthSecretRef.Name = "" }},
		{"bad-reference", func(d *api.TrmnlDevice) { d.Spec.ScreenRef.Name = "../other" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			d := validDevice(fmt.Sprintf("bad-device-%d", i))
			mutation.mutate(d)
			if err := c.Create(ctx, d); !apierrors.IsInvalid(err) {
				t.Fatalf("expected device rejection, got %v", err)
			}
		})
	}
	// Unknown source keys cannot survive strict API writes. CRDs normally prune
	// unknown fields, so every installation/example command uses validation=strict.
	for _, raw := range []string{
		`{"apiVersion":"arprt.dayflower.github.io/v1alpha1","kind":"TrmnlScreen","metadata":{"name":"unknown-source","namespace":"trmnl"},"spec":{"source":{"http":{"url":"http://host/render"},"unknown":{}}}}`,
		`{"apiVersion":"arprt.dayflower.github.io/v1alpha1","kind":"TrmnlDevice","metadata":{"name":"cross-namespace","namespace":"trmnl"},"spec":{"hardwareID":"02:00:00:00:00:01","authSecretRef":{"name":"key","namespace":"other"}}}`,
	} {
		var obj unstructured.Unstructured
		if err := json.Unmarshal([]byte(raw), &obj.Object); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, &obj, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
			t.Fatal("unknown fields accepted in strict mode")
		}
	}
	photo := validScreen("photo")
	if err := c.Create(ctx, photo); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(photo), photo); err != nil {
		t.Fatal(err)
	}
	if photo.Spec.Source.HTTP.Method != "GET" || photo.Spec.RefreshInterval != "" {
		t.Fatal("GET default or conditional interval default incorrect")
	}
	job := validScreen("default-job")
	job.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}}}
	if err := c.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
		t.Fatal(err)
	}
	if job.Spec.Source.Job.ContextPath != "/trmnl/context.json" || job.Spec.Source.Job.ImagePath != "/trmnl/output.png" {
		t.Fatal("job path defaults missing")
	}
	for i, interval := range []string{"1s", "24h", "1h30m", "0h0m1s"} {
		s := validScreen(fmt.Sprintf("interval-%d", i))
		s.Spec.RefreshInterval = interval
		if err := c.Create(ctx, s); err != nil {
			t.Fatal("valid duration rejected", err)
		}
	}
	// Apply every checked-in sample, preserving native Kubernetes container fields.
	for _, file := range []string{"screens.yaml", "device.yaml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "config", "samples", file))
		if err != nil {
			t.Fatal(err)
		}
		for i, doc := range splitDocuments(b) {
			var obj unstructured.Unstructured
			if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
				t.Fatal(err)
			}
			obj.SetNamespace("trmnl")
			obj.SetName(fmt.Sprintf("sample-%s-%d", file[:len(file)-5], i))
			if err := c.Create(ctx, &obj, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
				t.Fatal("sample rejected", err)
			}
			if err := c.Delete(ctx, &obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	d := validDevice("living-room")
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Spec.HardwareID = "02:00:00:00:00:02"
	if err := c.Update(ctx, d); !apierrors.IsInvalid(err) {
		t.Fatal("immutable MAC changed", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Spec.AuthSecretRef.Name = "rotated-key"
	if err := c.Update(ctx, d); err != nil {
		t.Fatal("credential rotation rejected", err)
	}
	// Start the real reconcilers with the same namespace-only cache as production.
	shutdown := 5 * time.Second
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"trmnl": {}}}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", GracefulShutdownTimeout: &shutdown})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&controller.ScreenReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := (&controller.DeviceReconciler{Client: mgr.GetClient(), Resolver: assignment.Explicit{Reader: mgr.GetClient()}}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager did not shut down gracefully")
		}
	})
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return reason(d.Status.Conditions, controller.ConditionAssignmentReady) == controller.ReasonAssignmentResolved && d.Status.ObservedGeneration == d.Generation
	})
	if d.Status.Display != nil {
		t.Fatal("controller invented device capabilities")
	}
	// Status subresource writes must not mutate spec; concurrent status survives.
	voltage := 3.91
	d.Status.Battery = &api.BatteryStatus{Voltage: &voltage}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	duplicate := validDevice("duplicate")
	if err := c.Create(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return reason(d.Status.Conditions, controller.ConditionIdentityUnique) == controller.ReasonDuplicateHardwareID
	})
	if d.Spec.AuthSecretRef.Name != "rotated-key" || d.Status.Battery == nil || *d.Status.Battery.Voltage != 3.91 {
		t.Fatal("reconcile overwrote spec or concurrent telemetry")
	}
	if err := c.Delete(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return reason(d.Status.Conditions, controller.ConditionIdentityUnique) == controller.ReasonIdentityUnique
	})
	if err := c.Delete(ctx, photo); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return reason(d.Status.Conditions, controller.ConditionAssignmentReady) == controller.ReasonReferenceNotFound
	})
	photo = validScreen("photo")
	photo.Namespace = "other"
	if err := c.Create(ctx, photo); err != nil {
		t.Fatal(err)
	}
	// An identically named resource in another namespace cannot resolve it.
	r := controller.DeviceReconciler{Client: c, Resolver: assignment.Explicit{Reader: c}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	if reason(d.Status.Conditions, controller.ConditionAssignmentReady) != controller.ReasonReferenceNotFound {
		t.Fatal("reference crossed namespace")
	}
	for _, playlist := range []bool{false, true} {
		x := validDevice(fmt.Sprintf("deferred-%t", playlist))
		x.Spec.HardwareID = "02:00:00:00:00:03"
		x.Spec.ScreenRef = nil
		if playlist {
			x.Spec.PlaylistRef = &api.LocalReference{Name: "future"}
		}
		if err := c.Create(ctx, x); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			if c.Get(ctx, client.ObjectKeyFromObject(x), x) != nil {
				return false
			}
			return reason(x.Status.Conditions, controller.ConditionAssignmentReady) == controller.ReasonUnsupportedFeature
		})
	}
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(job), job) != nil {
			return false
		}
		return reason(job.Status.Conditions, controller.ConditionReady) == controller.ReasonUnsupportedFeature
	})
}

func reason(conditions []metav1.Condition, kind string) string {
	c := meta.FindStatusCondition(conditions, kind)
	if c == nil {
		return ""
	}
	return c.Reason
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition did not converge")
}

func splitDocuments(b []byte) [][]byte { return bytes.Split(b, []byte("\n---\n")) }
