//go:build integration

package integration

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/jobrender"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This uses real API watches and durable Job/turn state; envtest has no kubelet.
func verifySkipDisplay(t *testing.T, c client.Client, jobs *jobrender.Reconciler, root string) {
	t.Helper()
	ctx := t.Context()
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "execution-test", Name: "skip-list"}, Spec: api.ArprtPlaylistSpec{FailurePolicy: "Skip", Screens: []api.LocalReference{{Name: "skip-a"}, {Name: "skip-b"}}}}
	for _, ref := range p.Spec.Screens {
		sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: ref.Name}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{ExecutionMode: "OnDemand", Renderer: api.RendererContainer{Image: "busybox:1.37.0"}}}}}
		if err := c.Create(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: "skip-device"}, Spec: api.ArprtDeviceSpec{PlaylistRef: &api.LocalReference{Name: p.Name}, HardwareID: "02:00:00:00:00:43", AuthSecretRef: api.LocalReference{Name: "skip-auth"}}}
	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: "skip-auth"}, Data: map[string][]byte{"apiKey": []byte(key)}}
	for _, obj := range []client.Object{p, secret, d} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	newServer := func() *byos.Server {
		t.Helper()
		s, err := byos.NewServer(byos.Config{Namespace: p.Namespace, PublicBaseURL: "https://byos.example.test"}, c, c, jobs.Repository)
		if err != nil {
			t.Fatal(err)
		}
		s.Jobs = jobs
		return s
	}
	s := newServer()
	fetch := func(server *byos.Server) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/devices/execution-test/skip-device/api/display", nil).WithContext(ctx)
		r.Header.Set("ID", d.Spec.HardwareID)
		r.Header.Set("Access-Token", key)
		r.Header.Set("Model", "og")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	owned := func() []batchv1.Job {
		var list batchv1.JobList
		if err := c.List(ctx, &list, client.InNamespace(p.Namespace)); err != nil {
			t.Fatal(err)
		}
		var result []batchv1.Job
		for _, job := range list.Items {
			if owner := metav1.GetControllerOf(&job); owner != nil && owner.UID == d.UID {
				result = append(result, job)
			}
		}
		return result
	}
	first, second := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- fetch(s) }()
	eventually(t, func() bool {
		select {
		case w := <-first:
			t.Fatalf("Skip stopped before first Job: %d %s", w.Code, w.Body.String())
		default:
		}
		return len(owned()) == 1
	})
	go func() { second <- fetch(s) }()
	failed := owned()[0]
	failed.Status.StartTime = &metav1.Time{Time: time.Now()}
	failed.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: metav1.Now()}, {Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, &failed); err != nil {
		t.Fatal(err)
	}
	var success batchv1.Job
	eventually(t, func() bool {
		select {
		case w := <-first:
			t.Fatalf("Skip stopped before second Job: %d %s", w.Code, w.Body.String())
		default:
		}
		for _, job := range owned() {
			if job.UID != failed.UID && job.Status.CompletionTime == nil {
				success = job
				return true
			}
		}
		return false
	})
	completeDisplayJob(t, c, root, &success)
	var previous byos.DisplayResponse
	for _, done := range []<-chan *httptest.ResponseRecorder{first, second} {
		select {
		case w := <-done:
			var response byos.DisplayResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.Status != 0 {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			if previous.ImageURL != "" && previous != response {
				t.Fatal("concurrent requests advanced twice", previous, response)
			}
			previous = response
		case <-time.After(6 * time.Second):
			t.Fatal("Skip display did not finish")
		}
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	var b api.ArprtScreen
	if err := c.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: "skip-b"}, &b); err != nil {
		t.Fatal(err)
	}
	if d.Status.ScreenCycle == nil || d.Status.ScreenCycle.ScreenUID != string(b.UID) {
		t.Fatal("failed position recorded", d.Status.ScreenCycle)
	}
	count := len(owned())
	replay := fetch(newServer())
	var response byos.DisplayResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &response); err != nil || replay.Code != 200 || response != previous || len(owned()) != count {
		t.Fatal("restart duplicated the turn", replay.Body.String(), err)
	}
}
