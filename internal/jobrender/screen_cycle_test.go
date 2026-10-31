package jobrender

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAllEligiblePairsBoundRemovalAndRetention(t *testing.T) {
	for _, mode := range []string{"playlist", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, d, s, now := fixture(t)
			r.Config.MaxActive = 1
			other := s.DeepCopy()
			other.Name = "second"
			other.UID = "second-uid"
			other.ResourceVersion = ""
			if err := r.Client.Create(ctx, other); err != nil {
				t.Fatal(err)
			}
			d.Spec.ScreenRef = nil
			var p *api.ArprtPlaylist
			if mode == "playlist" {
				p = &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "list", UID: "list-uid"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: other.Name}, {Name: s.Name}}}}
				if err := r.Client.Create(ctx, p); err != nil {
					t.Fatal(err)
				}
				d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
			}
			if err := r.Client.Update(ctx, d); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			var jobs batchv1.JobList
			if err := r.Client.List(ctx, &jobs); err != nil || len(jobs.Items) != 1 {
				t.Fatal("concurrency exceeded", len(jobs.Items), err)
			}
			queued, err := r.Store.load(d.Namespace, d.Name, other.Name)
			if err != nil || queued == nil || queued.Active != nil {
				t.Fatal("queued pair missing or attempt allocated while full", queued, err)
			}
			first := load(t, r, d)
			finish(t, r, d, png(t), false)
			step(t, r, d)
			queued, err = r.Store.load(d.Namespace, d.Name, other.Name)
			if err != nil || queued.Active == nil {
				t.Fatal("next eligible pair not prepared", err)
			}
			// Restart recovers both completed and active pairs without duplicating work.
			r.Store, err = NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			recovered, _ := r.Store.load(d.Namespace, d.Name, other.Name)
			if recovered.Active.ID != queued.Active.ID {
				t.Fatal("restart duplicated pair")
			}
			if err := r.maintenance(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Repository.Latest(ctx, first.Unit); err != nil {
				t.Fatal("nonselected eligible success collected", err)
			}
			if mode == "playlist" {
				p.Spec.Screens = []api.LocalReference{{Name: s.Name}}
				if err := r.Client.Update(ctx, p); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.Client.Delete(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			deletePods(t, r)
			step(t, r, d)
			removed, _ := r.Store.load(d.Namespace, d.Name, other.Name)
			if removed != nil {
				t.Fatal("removed pair retained")
			}
			if _, err := r.Repository.Latest(ctx, queued.Unit); !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("removed pair published", err)
			}
			*now = now.Add(time.Hour)
			step(t, r, d)
			removed, _ = r.Store.load(d.Namespace, d.Name, other.Name)
			if removed != nil {
				t.Fatal("removed pair rescheduled")
			}
		})
	}
}

func TestPhase4StateMigrationPreservesAttempt(t *testing.T) {
	r, d, _, _ := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	old := r.Store.path(d.Namespace, d.Name)
	current := r.Store.path(d.Namespace, d.Name, st.ScreenName)
	if err := os.Rename(current, old); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := store.load(d.Namespace, d.Name, st.ScreenName)
	if err != nil || migrated.Active.ID != st.Active.ID {
		t.Fatal("migration lost active attempt", err)
	}
	b, err := os.ReadFile(store.run(st.Active.ID) + "/context.json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) || hash(b) != st.Active.ContextDigest {
		t.Fatal("migration changed context")
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy record remains")
	}
	r.Store = store
	step(t, r, d)
	if load(t, r, d).Active.ID != st.Active.ID {
		t.Fatal("migration duplicated Job")
	}
}

func TestRepeatedEntriesShareScheduledPair(t *testing.T) {
	r, d, s, now := fixture(t)
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "repeated", UID: "playlist-uid"},
		Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: s.Name}, {Name: s.Name}, {Name: s.Name}}}}
	if err := r.Client.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	d.Spec.ScreenRef, d.Spec.PlaylistRef = nil, &api.LocalReference{Name: p.Name}
	if err := r.Client.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	first := load(t, r, d)
	var jobs batchv1.JobList
	if err := r.Client.List(t.Context(), &jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatal("duplicate Scheduled Jobs", jobs.Items, err)
	}
	// Removing some occurrences does not obsolete an active Scheduled attempt.
	p.Spec.Screens = p.Spec.Screens[:1]
	if err := r.Client.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if st := load(t, r, d); st.Active == nil || st.Active.ID != first.Active.ID || st.Sequence != first.Sequence {
		t.Fatal("partial removal cancelled or replaced active pair", st)
	}
	finish(t, r, d, png(t), false)
	step(t, r, d)
	completed := load(t, r, d)
	if completed.Active != nil || completed.Sequence != first.Sequence {
		t.Fatal("pair completion duplicated work", completed)
	}
	// Reintroducing duplicates and reopening the scheduler preserves the due
	// time and success, then allocates exactly one next attempt when due.
	p.Spec.Screens = append(p.Spec.Screens, p.Spec.Screens[0], p.Spec.Screens[0])
	if err := r.Client.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	var err error
	r.Store, err = NewStore(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if st := load(t, r, d); st.Active != nil || !st.Due.Equal(completed.Due) {
		t.Fatal("duplicates reset scheduling", st)
	}
	*now = completed.Due.Add(time.Second)
	step(t, r, d)
	if st := load(t, r, d); st.Active == nil || st.Sequence != first.Sequence+1 {
		t.Fatal("wrong next scheduled attempt", st)
	}
	records, err := r.Store.records()
	if err != nil || len(records) != 1 {
		t.Fatal("duplicate pair records", records, err)
	}
}
