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
	if err != nil || len(set.Entries) != 2 || set.Entries[0].Screen.Name != "b" {
		t.Fatal(set, err)
	}
	now := time.Now()
	d.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: set.Fingerprint, ScreenUID: "ab", Target: api.CycleTargetStatus{Width: d.Status.Display.Width, Height: d.Status.Display.Height, Palette: d.Status.Display.Palette}, ServedAt: metav1.NewMicroTime(now)}
	if s, replay := set.Select(d, now.Add(time.Second), 5*time.Second); s.Screen.Name != "b" || !replay {
		t.Fatal("retry advanced")
	}
	if s, replay := set.Select(d, now.Add(5*time.Second), 5*time.Second); s.Screen.Name != "z" || replay {
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
	if s, _ := changed.Select(d, now, 5*time.Second); s.Screen.Name != "a" {
		t.Fatal("membership change did not reset")
	}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "list", UID: "list"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "z"}, {Name: "missing"}, {Name: "b"}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
	set, err = r.Eligible(ctx, d)
	if err != nil || set.Missing != 1 || len(set.Entries) != 2 || set.Entries[0].Screen.Name != "z" {
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
	if err != nil || len(set.Entries) != MaxScreens || set.Entries[MaxScreens-1].Screen.Name != "screen-063" {
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
		t.Fatal("Hold changed selection")
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

// countingReader checks that duplicate positions use a single resolution snapshot.
type countingReader struct {
	client.Reader
	gets map[string]int
}

func (r countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*api.ArprtScreen); ok {
		r.gets[key.Name]++
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestRepeatedEntriesCycleAndCandidates(t *testing.T) {
	for _, test := range []struct {
		name string
		refs []string
		want []int32
	}{
		{"repeated", []string{"a", "b", "a", "c"}, []int32{0, 1, 2, 3, 0}},
		{"same", []string{"a", "a"}, []int32{0, 1, 0}},
		{"missing", []string{"a", "missing", "a", "b"}, []int32{0, 2, 3, 0}},
		{"deleting", []string{"a", "deleting", "a", "b"}, []int32{0, 2, 3, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "playlist", UID: "playlist"}}
			for _, name := range test.refs {
				p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: name})
			}
			objects := []client.Object{p}
			for _, name := range []string{"a", "b", "c", "deleting"} {
				sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: name, UID: types.UID(name)}}
				if name == "deleting" {
					now := metav1.Now()
					sc.DeletionTimestamp = &now
					sc.Finalizers = []string{"example.test/hold"}
				}
				objects = append(objects, sc)
			}
			reader := countingReader{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), gets: map[string]int{}}
			d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: api.ArprtDeviceSpec{PlaylistRef: &api.LocalReference{Name: p.Name}}, Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og"}, Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}}}
			set, err := (Explicit{Reader: reader}).Eligible(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			for name, count := range reader.gets {
				if count != 1 {
					t.Fatalf("%s resolved %d times", name, count)
				}
			}
			for _, a := range set.Entries {
				for _, b := range set.Entries {
					if a.Screen.Name == b.Screen.Name && a.Screen != b.Screen {
						t.Fatal("duplicate entries did not share a snapshot")
					}
				}
			}
			now := time.Now()
			for _, want := range test.want {
				entry, replay := set.Select(d, now, 5*time.Second)
				if replay || entry.Index != want {
					t.Fatalf("got index %d replay %v, want %d", entry.Index, replay, want)
				}
				candidates := set.Candidates(entry)
				if len(candidates) != len(set.Entries) || candidates[0].Index != want {
					t.Fatalf("wrong candidate traversal: %+v", candidates)
				}
				for i, candidate := range candidates {
					if candidate.Index != set.Entries[(indexOfEntry(set, entry.Index)+i)%len(set.Entries)].Index {
						t.Fatal("candidate order did not preserve positions")
					}
				}
				d.Status.ScreenCycle = &api.ScreenCycleStatus{EntryIndex: entry.Index, ScreenUID: string(entry.Screen.UID), Assignment: set.Fingerprint, Target: api.CycleTargetStatus{Width: 800, Height: 480, Palette: "gray4"}, ServedAt: metav1.NewMicroTime(now)}
				if held, replay := set.Select(d, now.Add(time.Second), 5*time.Second); !replay || held.Index != want {
					t.Fatal("replay did not preserve the exact position")
				}
				now = now.Add(5 * time.Second)
			}
			// Neither an unknown position nor a mismatched UID may resume a turn.
			for _, last := range []api.ScreenCycleStatus{
				{EntryIndex: 63, ScreenUID: "a"},
				{EntryIndex: 0, ScreenUID: "wrong"},
			} {
				last.Assignment, last.Target = set.Fingerprint, d.Status.ScreenCycle.Target
				last.ServedAt = metav1.NewMicroTime(now)
				d.Status.ScreenCycle = &last
				if entry, replay := set.Select(d, now, 5*time.Second); replay || entry.Index != 0 {
					t.Fatal("invalid turn did not reset to the first entry")
				}
			}
		})
	}
}

func indexOfEntry(set Set, index int32) int {
	for i, entry := range set.Entries {
		if entry.Index == index {
			return i
		}
	}
	return -1
}

func TestPlaylistFingerprintTracksAllReferences(t *testing.T) {
	for _, change := range []string{"order", "multiplicity", "missing-name", "missing-count", "created", "deleted", "deleting", "uid", "definition"} {
		t.Run(change, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			a := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "a", UID: "a"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer.example.test"}}}}
			b := a.DeepCopy()
			b.Name, b.UID = "b", "b"
			p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "playlist", UID: "playlist"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "a"}, {Name: "missing"}, {Name: "a"}, {Name: "b"}}}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, b, p).Build()
			d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: api.ArprtDeviceSpec{PlaylistRef: &api.LocalReference{Name: p.Name}}, Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og"}, Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}}}
			r := Explicit{Reader: c}
			before, err := r.Eligible(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			d.Status.ScreenCycle = &api.ScreenCycleStatus{EntryIndex: 2, ScreenUID: "a", Assignment: before.Fingerprint, Target: api.CycleTargetStatus{Width: 800, Height: 480, Palette: "gray4"}, ServedAt: metav1.NewMicroTime(now)}
			switch change {
			case "order":
				p.Spec.Screens[0], p.Spec.Screens[3] = p.Spec.Screens[3], p.Spec.Screens[0]
			case "multiplicity":
				p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: "a"})
			case "missing-name":
				p.Spec.Screens[1].Name = "other-missing"
			case "missing-count":
				p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: "missing"})
			case "created":
				sc := a.DeepCopy()
				sc.Name, sc.UID, sc.ResourceVersion = "missing", "new", ""
				if err := c.Create(t.Context(), sc); err != nil {
					t.Fatal(err)
				}
			case "deleted", "uid":
				if err := c.Delete(t.Context(), b); err != nil {
					t.Fatal(err)
				}
				if change == "uid" {
					b.UID, b.ResourceVersion = "replacement", ""
					if err := c.Create(t.Context(), b); err != nil {
						t.Fatal(err)
					}
				}
			case "deleting":
				b.Finalizers = []string{"example.test/hold"}
				if err := c.Update(t.Context(), b); err != nil {
					t.Fatal(err)
				}
				if err := c.Delete(t.Context(), b); err != nil {
					t.Fatal(err)
				}
			case "definition":
				a.Spec.Source.HTTP.URL = "http://changed.example.test"
				a.Generation++
				if err := c.Update(t.Context(), a); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Update(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			after, err := r.Eligible(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			selected, replay := after.Select(d, now, 5*time.Second)
			if change == "definition" {
				beforeRevision, err := before.Revision()
				if err != nil {
					t.Fatal(err)
				}
				afterRevision, err := after.Revision()
				if err != nil {
					t.Fatal(err)
				}
				if before.Fingerprint != after.Fingerprint || beforeRevision == afterRevision || selected.Index != 2 || !replay {
					t.Fatal("definition change reset the order or retained the revision")
				}
				if next, _ := after.Select(d, now.Add(5*time.Second), 5*time.Second); next.Index != 3 {
					t.Fatal("definition change lost the next position")
				}
			} else if before.Fingerprint == after.Fingerprint || selected.Index != 0 || replay {
				t.Fatal("reference change did not reset the cycle")
			}
		})
	}
}

func TestSingleScreenEntryDoesNotCycle(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "a", UID: "a"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sc).Build()
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: api.ArprtDeviceSpec{ScreenRef: &api.LocalReference{Name: "a"}}}
	set, err := (Explicit{Reader: c}).Eligible(t.Context(), d)
	if err != nil || set.Cycling || len(set.Entries) != 1 || set.Entries[0].Index != 0 {
		t.Fatal(set, err)
	}
	d.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: set.Fingerprint, ScreenUID: "a", ServedAt: metav1.NewMicroTime(time.Now())}
	if entry, replay := set.Select(d, time.Now(), time.Hour); entry.Index != 0 || replay {
		t.Fatal("single screen inherited a cycle")
	}
}
