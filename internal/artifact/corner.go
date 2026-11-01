package artifact

import (
	"context"
	"encoding/json"
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
	TransactionID  string                 `json:"transactionID,omitempty"`
	RecordRevision uint64                 `json:"recordRevision,omitempty"`
	PairKey        string                 `json:"pairKey,omitempty"`
	CandidateRef   string                 `json:"candidateRef,omitempty"`
	Epoch          uint64                 `json:"epoch,omitempty"`
	Generation     uint64                 `json:"generation,omitempty"`
	ImageDigest    string                 `json:"imageDigest,omitempty"`
	Filename       string                 `json:"filename,omitempty"`
	Next           overlay.Corner         `json:"next"`
	Previous       *api.ScreenCycleStatus `json:"previous,omitempty"`
	Proposed       *api.ScreenCycleStatus `json:"proposed"`
}

// CornerRecord is the common authoritative device record. Its original API is
// retained for callers that advance only overlay corners.
type CornerRecord struct {
	Pairs           map[string]RendererPair `json:"pairs,omitempty"`
	LastTransaction string                  `json:"lastTransaction,omitempty"`
	LastCommitted   bool                    `json:"lastCommitted,omitempty"`
	Version         int                     `json:"version"`
	DeviceUID       string                  `json:"deviceUID"`
	Revision        uint64                  `json:"revision"`
	Current         overlay.Corner          `json:"current"`
	Pending         *CornerPending          `json:"pending,omitempty"`
}
type CornerStore interface {
	Corner(context.Context, string) (CornerRecord, error)
	PrepareCorner(context.Context, CornerRecord, CornerPending) error
	ResolveCorner(context.Context, CornerRecord, bool) error
}

func validTurn(t *api.ScreenCycleStatus) bool {
	return t != nil && t.EntryIndex >= 0 && t.EntryIndex <= 63 && (t.CompatibilityFingerprint == "" || digestPattern.MatchString(t.CompatibilityFingerprint)) && digestPattern.MatchString(t.Assignment) && t.ScreenUID != "" && digestPattern.MatchString(t.InputRevision) && digestPattern.MatchString(t.ImageDigest) && filenamePattern.MatchString(t.Filename) && !t.ServedAt.IsZero() && (imageprocessing.Target{Width: int(t.Target.Width), Height: int(t.Target.Height), Palette: imageprocessing.Palette(t.Target.Palette), PNGByteLimit: 1}).Validate() == nil
}
func validCorner(r CornerRecord, uid string) bool {
	if r.Version != 2 || uid == "" || r.DeviceUID != uid || !r.Current.Valid() || r.Revision == math.MaxUint64 || len(r.Pairs) > MaxDevicePairs {
		return false
	}
	for key, pair := range r.Pairs {
		if !validPair(pair, uid) || key != PairKey(pair.Definition.Unit.Namespace, pair.Definition.Unit.ScreenUID) {
			return false
		}
	}
	if r.Pending != nil {
		p := r.Pending
		if p.TransactionID != "" {
			if !validPending(r, *p) {
				return false
			}
		} else if p.Next != r.Current.Next() || !validTurn(p.Proposed) || p.Previous != nil && !validTurn(p.Previous) || EqualCycle(p.Previous, p.Proposed) || p.PairKey != "" || p.CandidateRef != "" {
			return false
		}
	}
	return true
}
func (s *PVC) corner(uid string) (CornerRecord, error) {
	if uid == "" {
		return CornerRecord{}, errors.New("device UID required")
	}
	var r CornerRecord
	err := strictRead(filepath.Join(s.root, "corners", digest([]byte(uid))+".json"), MaxDeviceRecordBytes, &r)
	if errors.Is(err, ErrNotFound) {
		return CornerRecord{Version: 2, DeviceUID: uid, Current: overlay.TopRight}, nil
	}
	if err != nil {
		return CornerRecord{}, err
	}
	if !validCorner(r, uid) {
		return CornerRecord{}, ErrCorrupt
	}
	if err := s.validateDeviceReferences(r); err != nil {
		return CornerRecord{}, err
	}
	return r, nil
}
func (s *PVC) Corner(ctx context.Context, uid string) (CornerRecord, error) {
	if err := ctx.Err(); err != nil {
		return CornerRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.corner(uid)
	if err != nil {
		return r, err
	}
	// Complete an ambiguous directory fsync before acknowledging an observed
	// committed record, including a transaction whose pending field is gone.
	if err := s.confirmDurable("corners"); err != nil {
		return CornerRecord{}, err
	}
	return r, nil
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
	if pending.TransactionID != "" {
		return ErrObsolete
	}
	proposed := r
	proposed.Pending = &pending
	if !validCorner(proposed, r.DeviceUID) {
		return errors.New("invalid corner proposal")
	}
	if equalCorner(current, proposed) {
		return s.confirmDurable("corners")
	}
	if !equalCorner(current, r) || r.Pending != nil {
		return ErrObsolete
	}
	r = proposed
	return s.writeDevice(ctx, r, false)
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
	if r.Pending != nil && r.Pending.TransactionID != "" {
		return ErrObsolete
	}
	next := r
	next.Pending = nil
	if advance {
		next.Current = r.Current.Next()
		next.Revision++
	}
	if equalCorner(current, next) {
		return s.confirmDurable("corners")
	}
	if !equalCorner(current, r) || !validCorner(next, r.DeviceUID) {
		return ErrObsolete
	}
	// Existing authoritative records can use the reserve to finalize delivery.
	return s.writeDevice(ctx, next, r.Pending != nil || r.Revision > 0)
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
	ac, errA := json.Marshal(a)
	bc, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ac) == string(bc)
}
