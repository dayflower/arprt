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

type Set struct {
	Screens     []*api.ArprtScreen
	Fingerprint string
	Cycling     bool
	Missing     int
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
			set.Screens = append(set.Screens, list.Items[i].DeepCopy())
			if len(set.Screens) == MaxScreens {
				break
			}
		}
	}
	if len(refs) > MaxScreens {
		return set, errors.New("assignment exceeds 64 screens")
	}
	for _, ref := range refs {
		var s api.ArprtScreen
		err := r.Reader.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: ref.Name}, &s)
		if apierrors.IsNotFound(err) {
			if d.Spec.ScreenRef != nil {
				return set, err
			}
			set.Missing++
			continue
		}
		if err != nil {
			return set, err
		}
		if !s.DeletionTimestamp.IsZero() {
			set.Missing++
			continue
		}
		set.Screens = append(set.Screens, &s)
	}
	if len(set.Screens) == 0 {
		return set, ErrEmpty
	}
	ids := []string{identity, string(d.UID), strconv.FormatInt(d.Generation, 10)}
	for _, s := range set.Screens {
		ids = append(ids, s.Name+"/"+string(s.UID))
	}
	b, _ := json.Marshal(ids)
	h := sha256.Sum256(b)
	set.Fingerprint = hex.EncodeToString(h[:])
	return set, nil
}

// Select holds the last successful turn during the retry window, then advances.
func (set Set) Select(d *api.ArprtDevice, now time.Time, interval time.Duration) (*api.ArprtScreen, bool) {
	last := d.Status.ScreenCycle
	if set.Cycling && last != nil && last.Assignment == set.Fingerprint && d.Status.Display != nil && last.Target == *d.Status.Display {
		for i, s := range set.Screens {
			if string(s.UID) == last.ScreenUID {
				if now.Before(last.ServedAt.Add(interval)) {
					return s, true
				}
				return set.Screens[(i+1)%len(set.Screens)], false
			}
		}
	}
	return set.Screens[0], false
}
func (r Explicit) Resolve(ctx context.Context, d *api.ArprtDevice) (*api.ArprtScreen, error) {
	set, err := r.Eligible(ctx, d)
	if err != nil {
		return nil, err
	}
	s, _ := set.Select(d, time.Now(), 0)
	return s, nil
}
func (r Explicit) Contains(ctx context.Context, d *api.ArprtDevice, s *api.ArprtScreen) (bool, error) {
	set, err := r.Eligible(ctx, d)
	if err != nil {
		return false, err
	}
	for _, current := range set.Screens {
		if current.UID == s.UID {
			return true, nil
		}
	}
	return false, nil
}
