package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/render"
)

// DeliveryRecord is an observation of a completed delivery transaction, never
// an authorization or retention root. It may lag successful issuance after a
// failed write or crash; callers must describe it as the last recorded delivery.
// RenderedAt belongs to the original success, not the digest or issuance time.
type DeliveryRecord struct {
	Version          int                    `json:"version"`
	DeviceUID        string                 `json:"deviceUID"`
	DeviceGeneration int64                  `json:"deviceGeneration"`
	ScreenName       string                 `json:"screenName"`
	ScreenUID        string                 `json:"screenUID"`
	ScreenGeneration int64                  `json:"screenGeneration"`
	Assignment       string                 `json:"assignment"`
	InputRevision    string                 `json:"inputRevision"`
	Target           render.ResolvedTarget  `json:"target"`
	ImageDigest      string                 `json:"imageDigest"`
	RenderedAt       *time.Time             `json:"renderedAt,omitempty"`
	IssuedAt         time.Time              `json:"issuedAt"`
	Result           string                 `json:"result"`    // ready, stale or unknown; scoped to this delivery.
	Rendering        string                 `json:"rendering"` // ready, failed, pending or unknown; never inferred from age.
	Cycle            *api.ScreenCycleStatus `json:"cycle,omitempty"`
}

// DeliveryReader deliberately exposes neither issuance nor mutable cache reads.
type DeliveryReader interface {
	Delivery(context.Context, string) (DeliveryRecord, error)
	DeliveryPreviewAvailable(context.Context, string, string) (bool, error)
	ReadDeliveryPreview(context.Context, string, string) ([]byte, error)
}

type DeliveryStore interface {
	DeliveryReader
	RecordDelivery(context.Context, DeliveryRecord) error
}

func validDelivery(r DeliveryRecord, uid string) bool {
	return r.Version == 1 && uid != "" && r.DeviceUID == uid && r.DeviceGeneration > 0 &&
		r.ScreenName != "" && r.ScreenUID != "" && r.ScreenGeneration > 0 &&
		digestPattern.MatchString(r.Assignment) && digestPattern.MatchString(r.InputRevision) &&
		r.Target.Validate() == nil && digestPattern.MatchString(r.ImageDigest) && !r.IssuedAt.IsZero() &&
		(r.RenderedAt == nil || !r.RenderedAt.IsZero()) &&
		((r.Result == "ready" && r.Rendering == "ready") || (r.Result == "unknown" && r.Rendering == "unknown") ||
			(r.Result == "stale" && (r.Rendering == "failed" || r.Rendering == "pending" || r.Rendering == "unknown"))) &&
		(r.Cycle == nil || (validTurn(r.Cycle) && r.Cycle.ImageDigest == r.ImageDigest && r.Cycle.ScreenUID == r.ScreenUID && r.Cycle.Assignment == r.Assignment && r.Cycle.InputRevision == r.InputRevision &&
			r.Cycle.Target == (api.CycleTargetStatus{Width: r.Target.Target.Width, Height: r.Target.Target.Height, Palette: r.Target.Target.Palette})))
}

func (s *PVC) delivery(uid string) (DeliveryRecord, error) {
	if uid == "" {
		return DeliveryRecord{}, errors.New("device UID required")
	}
	var r DeliveryRecord
	if err := readJSON(filepath.Join(s.root, "deliveries", digest([]byte(uid))+".json"), &r); err != nil {
		return DeliveryRecord{}, err
	}
	if !validDelivery(r, uid) {
		return DeliveryRecord{}, ErrCorrupt
	}
	return r, nil
}

// Delivery does not repair or discard invalid data and never refreshes retention.
func (s *PVC) Delivery(ctx context.Context, uid string) (DeliveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeliveryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivery(uid)
}

// DeliveryPreviewAvailable checks presence without loading any image bytes.
func (s *PVC) DeliveryPreviewAvailable(ctx context.Context, uid, imageDigest string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.delivery(uid)
	if err != nil {
		return false, err
	}
	if r.ImageDigest != imageDigest {
		return false, ErrNotFound
	}
	info, err := os.Stat(filepath.Join(s.root, "blobs", r.ImageDigest+".png"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= imageprocessing.MaxInputBytes, nil
}

// ReadDeliveryPreview binds reads to a record under the GC/writer lock. It does
// not issue access, pin output, repair data or update any last-used timestamp.
func (s *PVC) ReadDeliveryPreview(ctx context.Context, uid, imageDigest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.delivery(uid)
	if err != nil {
		return nil, err
	}
	if r.ImageDigest != imageDigest {
		return nil, ErrNotFound
	}
	return s.Read(ctx, r.ImageDigest)
}

// RecordDelivery is called under the BYOS device gate after required commitment.
// The repository lock plus timestamp guard also reject delayed older writers.
// Equal timestamps fail conservatively rather than guessing transaction order.
func (s *PVC) RecordDelivery(ctx context.Context, r DeliveryRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validDelivery(r, r.DeviceUID) {
		return errors.New("invalid delivery observation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.delivery(r.DeviceUID)
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrCorrupt) {
		return err
	}
	if err == nil && !r.IssuedAt.After(previous.IssuedAt) {
		return ErrObsolete
	}
	if err := s.checkCapacity(ctx, 8192); err != nil {
		return err
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicWriteChecked(filepath.Join(s.root, "deliveries", digest([]byte(r.DeviceUID))+".json"), encoded, ctx.Err)
}

// Observations never keep blobs alive. Deleted UIDs have a grace period because
// the maintenance API snapshot may precede a concurrently recorded delivery.
// Malformed/unknown versions are disposable observations, unlike retention roots.
func (s *PVC) collectDeliveries(ctx context.Context, uids map[string]bool, now time.Time) error {
	entries, err := os.ReadDir(filepath.Join(s.root, "deliveries"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.root, "deliveries", e.Name())
		var r DeliveryRecord
		err := readJSON(path, &r)
		if err != nil && !errors.Is(err, ErrCorrupt) {
			return err
		}
		if err == nil && validDelivery(r, r.DeviceUID) && digest([]byte(r.DeviceUID))+".json" == e.Name() && uids[r.DeviceUID] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) >= 24*time.Hour {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
