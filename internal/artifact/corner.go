package artifact

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/overlay"
)

// Corner records are authoritative and never inferred from regenerable caches.
// Pending stores both complete turns to distinguish status commitment from a
// retry or unrelated status edit. Public status carries no overlay counters.
type CornerPending struct {
	Next     overlay.Corner         `json:"next"`
	Previous *api.ScreenCycleStatus `json:"previous,omitempty"`
	Proposed *api.ScreenCycleStatus `json:"proposed"`
}
type CornerRecord struct {
	Version   int            `json:"version"`
	DeviceUID string         `json:"deviceUID"`
	Revision  uint64         `json:"revision"`
	Current   overlay.Corner `json:"current"`
	Pending   *CornerPending `json:"pending,omitempty"`
}
type CornerStore interface {
	Corner(context.Context, string) (CornerRecord, error)
	PrepareCorner(context.Context, CornerRecord, CornerPending) error
	ResolveCorner(context.Context, CornerRecord, bool) error
}

func validTurn(t *api.ScreenCycleStatus) bool {
	return t != nil && (t.CompatibilityFingerprint == "" || digestPattern.MatchString(t.CompatibilityFingerprint)) && digestPattern.MatchString(t.Assignment) && t.ScreenUID != "" && digestPattern.MatchString(t.InputRevision) && digestPattern.MatchString(t.ImageDigest) && filenamePattern.MatchString(t.Filename) && !t.ServedAt.IsZero() && (imageprocessing.Target{Width: int(t.Target.Width), Height: int(t.Target.Height), Palette: imageprocessing.Palette(t.Target.Palette), PNGByteLimit: 1}).Validate() == nil
}
func validCorner(r CornerRecord, uid string) bool {
	return r.Version == 1 && uid != "" && r.DeviceUID == uid && r.Current.Valid() && r.Revision < math.MaxUint64 && (r.Pending == nil || (r.Pending.Next == r.Current.Next() && validTurn(r.Pending.Proposed) && (r.Pending.Previous == nil || validTurn(r.Pending.Previous)) && !EqualCycle(r.Pending.Previous, r.Pending.Proposed)))
}
func (s *PVC) corner(uid string) (CornerRecord, error) {
	if uid == "" {
		return CornerRecord{}, errors.New("device UID required")
	}
	var r CornerRecord
	err := readJSON(filepath.Join(s.root, "corners", digest([]byte(uid))+".json"), &r)
	if errors.Is(err, ErrNotFound) {
		return CornerRecord{Version: 1, DeviceUID: uid, Current: overlay.TopRight}, nil
	}
	if err != nil {
		return CornerRecord{}, err
	}
	if !validCorner(r, uid) {
		return CornerRecord{}, ErrCorrupt
	}
	return r, nil
}
func (s *PVC) Corner(ctx context.Context, uid string) (CornerRecord, error) {
	if err := ctx.Err(); err != nil {
		return CornerRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.corner(uid)
}
func (s *PVC) PrepareCorner(ctx context.Context, r CornerRecord, pending CornerPending) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return err
	}
	proposed := r
	proposed.Pending = &pending
	if !validCorner(proposed, r.DeviceUID) {
		return errors.New("invalid corner proposal")
	}
	if equalCorner(current, proposed) {
		return nil
	}
	if !equalCorner(current, r) || r.Pending != nil {
		return ErrObsolete
	}
	r = proposed
	if err := s.checkCapacity(ctx, 8192); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.root, "corners", digest([]byte(r.DeviceUID))+".json"), r)
}

// ResolveCorner commits the next corner, or cancels an uncommitted proposal.
// Atomic retries are idempotent against the complete previous record.
func (s *PVC) ResolveCorner(ctx context.Context, r CornerRecord, advance bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return err
	}
	next := r
	next.Pending = nil
	if advance {
		next.Current = r.Current.Next()
		next.Revision++
	}
	if equalCorner(current, next) {
		return nil
	}
	if !equalCorner(current, r) || !validCorner(next, r.DeviceUID) {
		return ErrObsolete
	}
	// Existing authoritative records can use the reserve to finalize delivery.
	if r.Pending != nil || r.Revision > 0 {
		_, free, err := s.Usage(ctx)
		if err != nil {
			return err
		}
		if free < 8192 {
			return ErrCapacity
		}
	} else if err := s.checkCapacity(ctx, 8192); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.root, "corners", digest([]byte(r.DeviceUID))+".json"), next)
}

// EqualCycle compares every persisted turn identity field while treating equal
// instants identically across JSON/API time-location normalization.
func EqualCycle(a, b *api.ScreenCycleStatus) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ac, bc := a.DeepCopy(), b.DeepCopy()
	ac.ServedAt.Time = ac.ServedAt.Time.UTC()
	bc.ServedAt.Time = bc.ServedAt.Time.UTC()
	return reflect.DeepEqual(ac, bc)
}
func equalCorner(a, b CornerRecord) bool {
	if a.Version != b.Version || a.DeviceUID != b.DeviceUID || a.Revision != b.Revision || a.Current != b.Current {
		return false
	}
	if a.Pending == nil || b.Pending == nil {
		return a.Pending == nil && b.Pending == nil
	}
	return a.Pending.Next == b.Pending.Next && EqualCycle(a.Pending.Previous, b.Pending.Previous) && EqualCycle(a.Pending.Proposed, b.Pending.Proposed)
}
