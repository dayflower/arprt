// Package assignment resolves desired assignments within one namespace.
package assignment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MaxScreens = 64

var ErrEmpty = errors.New("assignment has no existing screens")

type Resolver interface {
	Resolve(context.Context, *api.ArprtDevice) (*api.ArprtScreen, error)
}

// Explicit resolves all three assignment modes. The name is retained for callers.
type Explicit struct{ Reader client.Reader }

// Entry identifies an occurrence in the original ordered assignment.
type Entry struct {
	Index  int32
	Screen *api.ArprtScreen
}

type Set struct {
	Entries       []Entry
	Fingerprint   string
	Cycling       bool
	Missing       int
	Skip          bool
	RenderTimeout time.Duration
}

func (r Explicit) Eligible(ctx context.Context, d *api.ArprtDevice) (Set, error) {
	set := Set{Cycling: d.Spec.ScreenRef == nil}
	identity := "discovery"
	var refs []api.LocalReference
	switch {
	case d.Spec.ScreenRef != nil:
		refs = []api.LocalReference{*d.Spec.ScreenRef}
		identity = "screen/" + d.Spec.ScreenRef.Name
	case d.Spec.PlaylistRef != nil:
		var p api.ArprtPlaylist
		if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.PlaylistRef.Name}, &p); err != nil {
			return set, err
		}
		if !p.DeletionTimestamp.IsZero() {
			return set, ErrEmpty
		}
		identity = "playlist/" + string(p.UID)
		refs = p.Spec.Screens
		set.Skip = p.Spec.FailurePolicy == "Skip"
		if set.Skip {
			set.RenderTimeout = 10 * time.Second
			if p.Spec.RenderTimeout != "" {
				var err error
				set.RenderTimeout, err = time.ParseDuration(p.Spec.RenderTimeout)
				if err != nil || set.RenderTimeout < time.Second || set.RenderTimeout > 10*time.Second || set.RenderTimeout%time.Second != 0 {
					return set, errors.New("invalid playlist render timeout")
				}
			}
		}
	default:
		var list api.ArprtScreenList
		if err := r.Reader.List(ctx, &list, client.InNamespace(d.Namespace)); err != nil {
			return set, err
		}
		sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
		for i := range list.Items {
			if !list.Items[i].DeletionTimestamp.IsZero() {
				continue
			}
			set.Entries = append(set.Entries, Entry{Index: int32(len(set.Entries)), Screen: list.Items[i].DeepCopy()})
			if len(set.Entries) == MaxScreens {
				break
			}
		}
	}
	if len(refs) > MaxScreens {
		return set, errors.New("assignment exceeds 64 screens")
	}
	// Resolve each name once so repeated entries share one immutable snapshot.
	resolved := map[string]*api.ArprtScreen{}
	ids := []string{identity, string(d.UID), strconv.FormatInt(d.Generation, 10)}
	if set.Skip {
		ids = append(ids, "Skip", set.RenderTimeout.String())
	}
	for i, ref := range refs {
		s, known := resolved[ref.Name]
		if !known {
			var found api.ArprtScreen
			err := r.Reader.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: ref.Name}, &found)
			if apierrors.IsNotFound(err) {
				if d.Spec.ScreenRef != nil {
					return set, err
				}
			} else if err != nil {
				return set, err
			} else {
				s = &found
			}
			resolved[ref.Name] = s
		}
		// Include every original reference and its resolution, even unavailable ones.
		ids = append(ids, strconv.Itoa(i), ref.Name)
		if s == nil {
			ids = append(ids, "missing")
			set.Missing++
			continue
		}
		ids = append(ids, string(s.UID))
		if !s.DeletionTimestamp.IsZero() {
			ids = append(ids, "deleting")
			set.Missing++
			continue
		}
		ids = append(ids, "available")
		set.Entries = append(set.Entries, Entry{Index: int32(i), Screen: s})
	}
	if len(set.Entries) == 0 {
		return set, ErrEmpty
	}
	if refs == nil {
		for _, entry := range set.Entries {
			ids = append(ids, strconv.Itoa(int(entry.Index)), entry.Screen.Name, string(entry.Screen.UID))
		}
	}
	b, _ := json.Marshal(ids)
	h := sha256.Sum256(b)
	set.Fingerprint = hex.EncodeToString(h[:])
	return set, nil
}

// Select holds the last successful turn during the retry window, then advances.
func (set Set) Select(d *api.ArprtDevice, now time.Time, interval time.Duration) (Entry, bool) {
	last := d.Status.ScreenCycle
	target := devicestate.Target(d)
	if set.Cycling && last != nil && last.Assignment == set.Fingerprint && target.Validate() == nil && last.Target == (api.CycleTargetStatus{Width: target.Width, Height: target.Height, Palette: target.Palette}) {
		for i, entry := range set.Entries {
			if entry.Index == last.EntryIndex && string(entry.Screen.UID) == last.ScreenUID {
				if now.Before(last.ServedAt.Add(interval)) {
					return entry, true
				}
				return set.Entries[(i+1)%len(set.Entries)], false
			}
		}
	}
	return set.Entries[0], false
}
func (r Explicit) Resolve(ctx context.Context, d *api.ArprtDevice) (*api.ArprtScreen, error) {
	set, err := r.Eligible(ctx, d)
	if err != nil {
		return nil, err
	}
	s, _ := set.Select(d, time.Now(), 0)
	return s.Screen, nil
}
func (r Explicit) Contains(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen) (bool, error) {
	set, err := r.Eligible(ctx, d)
	if err != nil {
		return false, err
	}
	for _, current := range set.Entries {
		if current.Screen.UID == s.UID {
			return true, nil
		}
	}
	return false, nil
}

// Candidates returns a finite traversal beginning at the selected turn.
func (set Set) Candidates(first Entry) []Entry {
	for i, entry := range set.Entries {
		if entry.Index == first.Index && entry.Screen.UID == first.Screen.UID {
			return append(append([]Entry{}, set.Entries[i:]...), set.Entries[:i]...)
		}
	}
	return nil
}

// Revision binds the complete candidate definitions without changing the
// membership fingerprint used to choose the next turn.
func (set Set) Revision() (string, error) {
	ids := []string{set.Fingerprint}
	for _, entry := range set.Entries {
		s := entry.Screen
		r, err := screen.InputRevision(s.Spec)
		if err != nil {
			return "", err
		}
		ids = append(ids, string(s.UID), strconv.FormatInt(s.Generation, 10), r)
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
