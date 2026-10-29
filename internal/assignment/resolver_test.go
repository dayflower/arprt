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
		if err := c.Create(ctx, &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: s.ns, Name: s.name, UID: types.UID(s.ns + s.name)}}); err != nil {
			t.Fatal(err)
		}
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "a"}, Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og"}, Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}}}
	r := Explicit{Reader: c}
	set, err := r.Eligible(ctx, d)
	if err != nil || len(set.Screens) != 2 || set.Screens[0].Name != "b" {
		t.Fatal(set, err)
	}
	now := time.Now()
	d.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: set.Fingerprint, ScreenUID: "ab", Target: api.CycleTargetStatus{Width: d.Status.Display.Width, Height: d.Status.Display.Height, Palette: d.Status.Display.Palette}, ServedAt: metav1.NewMicroTime(now)}
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

	if err := c.Create(ctx, &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "a", UID: "aa"}}); err != nil {
		t.Fatal(err)
	}
	changed, _ := r.Eligible(ctx, d)
	if changed.Fingerprint == set.Fingerprint {
		t.Fatal("membership not tracked")
	}
	if s, _ := changed.Select(d, now, 5*time.Second); s.Name != "a" {
		t.Fatal("membership change did not reset")
	}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "list", UID: "list"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "z"}, {Name: "missing"}, {Name: "b"}}}}
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
		objects = append(objects, &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: fmt.Sprintf("screen-%03d", i)}})
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	set, err := (Explicit{Reader: c}).Eligible(context.Background(), &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "a"}})
	if err != nil || len(set.Screens) != MaxScreens || set.Screens[MaxScreens-1].Name != "screen-063" {
		t.Fatal(set, err)
	}
}

func TestPlaylistPolicyDefaultsAndCandidateSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "screen", UID: "screen"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer.example.test"}}}}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "playlist", UID: "playlist"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "screen"}, {Name: "missing"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sc, p).Build()
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "a", UID: "device"}, Spec: api.ArprtDeviceSpec{PlaylistRef: &api.LocalReference{Name: "playlist"}}}
	resolver := Explicit{Reader: c}
	read := func() Set {
		t.Helper()
		set, err := resolver.Eligible(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	omitted := read()
	p.Spec.FailurePolicy = "Hold"
	if err := c.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if explicit := read(); explicit.Fingerprint != omitted.Fingerprint || explicit.Skip {
		t.Fatal("Hold changed legacy selection")
	}
	p.Spec.FailurePolicy = "Skip"
	if err := c.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	skip := read()
	if !skip.Skip || skip.RenderTimeout != 10*time.Second || skip.Fingerprint == omitted.Fingerprint {
		t.Fatal(skip)
	}
	p.Spec.RenderTimeout = "10s"
	if err := c.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if explicit := read(); explicit.Fingerprint != skip.Fingerprint {
		t.Fatal("explicit default changed fingerprint")
	}
	before, err := skip.Revision()
	if err != nil {
		t.Fatal(err)
	}
	sc.Spec.Source.HTTP.URL = "http://other.example.test"
	if err := c.Update(t.Context(), sc); err != nil {
		t.Fatal(err)
	}
	changed := read()
	after, err := changed.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if before == after || changed.Fingerprint != skip.Fingerprint {
		t.Fatal("definitions must invalidate replay without resetting order")
	}
	p.Spec.RenderTimeout = "1s"
	if err := c.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if read().Fingerprint == skip.Fingerprint {
		t.Fatal("budget edit did not invalidate replay")
	}
}
