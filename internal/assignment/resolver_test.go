package assignment

import (
	"context"
	"fmt"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestOrderingScopeEditsAndRetryWindow(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	for _, s := range []struct{ ns, name string }{{"a", "z"}, {"a", "b"}, {"other", "a"}} {
		if err := c.Create(ctx, &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Namespace: s.ns, Name: s.name, UID: types.UID(s.ns + s.name)}}); err != nil {
			t.Fatal(err)
		}
	}
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "a"}, Status: api.TrmnlDeviceStatus{Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}}}
	r := Explicit{Reader: c}
	set, err := r.Eligible(ctx, d)
	if err != nil || len(set.Screens) != 2 || set.Screens[0].Name != "b" {
		t.Fatal(set, err)
	}
	now := time.Now()
	d.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: set.Fingerprint, ScreenUID: "ab", Target: *d.Status.Display, ServedAt: metav1.NewMicroTime(now)}
	if s, replay := set.Select(d, now.Add(time.Second), 5*time.Second); s.Name != "b" || !replay {
		t.Fatal("retry advanced")
	}
	if s, replay := set.Select(d, now.Add(5*time.Second), 5*time.Second); s.Name != "z" || replay {
		t.Fatal("due request did not advance")
	}
	d.UID = "replacement"
	replaced, _ := r.Eligible(ctx, d)
	if replaced.Fingerprint == set.Fingerprint {
		t.Fatal("replacement inherited screen-cycle ownership")
	}
	d.UID = ""

	if err := c.Create(ctx, &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "a", UID: "aa"}}); err != nil {
		t.Fatal(err)
	}
	changed, _ := r.Eligible(ctx, d)
	if changed.Fingerprint == set.Fingerprint {
		t.Fatal("membership not tracked")
	}
	if s, _ := changed.Select(d, now, 5*time.Second); s.Name != "a" {
		t.Fatal("membership change did not reset")
	}
	p := &api.TrmnlPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "list", UID: "list"}, Spec: api.TrmnlPlaylistSpec{Screens: []api.LocalReference{{Name: "z"}, {Name: "missing"}, {Name: "b"}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
	set, err = r.Eligible(ctx, d)
	if err != nil || set.Missing != 1 || len(set.Screens) != 2 || set.Screens[0].Name != "z" {
		t.Fatal("playlist order or missing reference", set, err)
	}
	p.Spec.Screens = []api.LocalReference{{Name: "b"}, {Name: "z"}}
	if err := c.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	changed, _ = r.Eligible(ctx, d)
	if changed.Fingerprint == set.Fingerprint {
		t.Fatal("edit not tracked")
	}
	d.Spec.PlaylistRef.Name = "absent"
	if _, err := r.Eligible(ctx, d); !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	d.Spec.PlaylistRef = nil
	d.Spec.ScreenRef = &api.LocalReference{Name: "absent"}
	if _, err := r.Eligible(ctx, d); !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func TestDiscoveryFanoutBound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	objects := []client.Object{}
	for i := 0; i < MaxScreens+3; i++ {
		objects = append(objects, &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: fmt.Sprintf("screen-%03d", i)}})
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	set, err := (Explicit{Reader: c}).Eligible(context.Background(), &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "a"}})
	if err != nil || len(set.Screens) != MaxScreens || set.Screens[MaxScreens-1].Name != "screen-063" {
		t.Fatal(set, err)
	}
}
