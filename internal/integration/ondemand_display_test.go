//go:build integration

package integration

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyOnDemandDisplay(t *testing.T, c client.Client, jobs *jobrender.Reconciler, root string) {
	t.Helper()
	ctx := t.Context()
	screen := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "execution-test", Name: "display-screen"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{ExecutionMode: "OnDemand", Renderer: api.RendererContainer{Image: "busybox:1.37.0"}}}}}
	device := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: screen.Namespace, Name: "display-device"}, Spec: api.ArprtDeviceSpec{ScreenRef: &api.LocalReference{Name: screen.Name}, HardwareID: "02:00:00:00:00:42", AuthSecretRef: api.LocalReference{Name: "display-auth"}}}
	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: screen.Namespace, Name: "display-auth"}, Data: map[string][]byte{"apiKey": []byte(key)}}
	for _, obj := range []client.Object{screen, secret, device} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	server, err := byos.NewServer(byos.Config{Namespace: screen.Namespace, PublicBaseURL: "https://byos.example.test"}, c, c, jobs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	server.Jobs = jobs
	path := "/devices/" + device.Namespace + "/" + device.Name
	fetch := func(endpoint string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", endpoint, nil).WithContext(ctx)
		r.Header.Set("ID", device.Spec.HardwareID)
		r.Header.Set("Model", "og")
		r.Header.Set("Battery-Voltage", "3.87")
		if authenticated {
			r.Header.Set("Access-Token", key)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	owned := func() []batchv1.Job {
		var list batchv1.JobList
		if err := c.List(ctx, &list, client.InNamespace(device.Namespace)); err != nil {
			t.Fatal(err)
		}
		var result []batchv1.Job
		for _, job := range list.Items {
			if owner := metav1.GetControllerOf(&job); owner != nil && owner.UID == device.UID {
				result = append(result, job)
			}
		}
		return result
	}
	if w := fetch(path+"/api/display", false); w.Code != 401 || len(owned()) != 0 {
		t.Fatal("unauthenticated display started work", w.Code)
	}
	var previous byos.DisplayResponse
	for turn := 0; turn < 2; turn++ {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- fetch(path+"/api/display", true) }()
		eventually(t, func() bool { return len(owned()) == turn+1 })
		var current batchv1.Job
		for _, job := range owned() {
			if job.Status.CompletionTime == nil {
				current = job
			}
		}
		id := strings.TrimPrefix(current.Name, "arprt-render-")
		contextBytes, err := os.ReadFile(filepath.Join(root, "runs", id, "context.json"))
		if err != nil {
			t.Fatal(err)
		}
		var snapshot render.Context
		if err := json.Unmarshal(contextBytes, &snapshot); err != nil || snapshot.Device.Battery.Voltage == nil || *snapshot.Device.Battery.Voltage != 3.87 {
			t.Fatal("current observation missing from immutable context", err)
		}
		completeDisplayJob(t, c, root, &current)
		select {
		case w := <-done:
			var response byos.DisplayResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.Status != 0 || response.ImageURL == "" {
				t.Fatal("display did not issue completed output", w.Code, w.Body.String(), err)
			}
			if turn == 1 && (response.ImageURL != previous.ImageURL || response.Filename != previous.Filename) {
				t.Fatal("identical-byte regeneration changed filename")
			}
			previous = response
			if image := fetch(strings.TrimPrefix(response.ImageURL, "https://byos.example.test"), true); image.Code != 200 {
				t.Fatal("issued image unavailable", image.Code)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("display did not finish")
		}
		if len(owned()) != turn+1 {
			t.Fatal("image fetch created a Job")
		}
	}
	// Exercise public mode changes while a scheduled Pod still owns the mount.
	setMode := func(mode, timeout string) {
		t.Helper()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := c.Get(ctx, client.ObjectKeyFromObject(screen), screen); err != nil {
				return err
			}
			screen.Spec.Source.Job.ExecutionMode, screen.Spec.Source.Job.Timeout = mode, timeout
			return c.Update(ctx, screen)
		}); err != nil {
			t.Fatal(err)
		}
	}
	setMode("Scheduled", "")
	eventually(t, func() bool { return len(owned()) == 3 })
	var stopping batchv1.Job
	for _, job := range owned() {
		if job.Status.CompletionTime == nil {
			stopping = job
		}
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: stopping.Namespace, Name: stopping.Name, Labels: map[string]string{"batch.kubernetes.io/job-name": stopping.Name, "arprt.dayflower.github.io/renderer": "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: stopping.Name, UID: stopping.UID, Controller: ptr.To(true)}}}, Spec: *stopping.Spec.Template.Spec.DeepCopy()}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	setMode("OnDemand", "1s")
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(&stopping), &stopping) == nil && !stopping.DeletionTimestamp.IsZero()
	})
	if len(owned()) != 3 {
		t.Fatal("mode switch created a replacement while the old Pod remained")
	}
	// Envtest has no GC/kubelet. Clear owned resources explicitly after proving
	// the old mount prevents replacement; no display has been requested yet.
	if err := c.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	stopping.Finalizers = nil
	if err := c.Update(ctx, &stopping); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(owned()) == 2 })
	w := fetch(path+"/api/display", true)
	var fallback byos.DisplayResponse
	if err := json.Unmarshal(w.Body.Bytes(), &fallback); err != nil || w.Code != 200 || fallback.ImageURL != previous.ImageURL || fallback.Filename != previous.Filename {
		t.Fatal("first on-demand timeout lost compatible fallback", w.Code, w.Body.String(), err)
	}
	if len(owned()) != 3 {
		t.Fatal("new display did not trigger an on-demand attempt")
	}
}

func completeDisplayJob(t *testing.T, c client.Client, root string, job *batchv1.Job) {
	t.Helper()
	ctx := t.Context()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: job.Namespace, Name: job.Name, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name, "arprt.dayflower.github.io/renderer": "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}}, Spec: *job.Spec.Template.Spec.DeepCopy()}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "renderer", Image: "busybox:1.37.0", ImageID: "fixture", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(job.Name, "arprt-render-")
	if err := os.WriteFile(filepath.Join(root, "runs", id, "output", "output.png"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	job.Status.StartTime = ptr.To(metav1.Now())
	job.Status.CompletionTime = ptr.To(metav1.Now())
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}, {Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "CompletionsReached", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
}
