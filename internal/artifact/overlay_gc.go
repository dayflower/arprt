package artifact

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

func (s *PVC) validateOverlayRetention(ctx context.Context) error {
	for _, dir := range []string{"corners", "derived"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
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
			p := filepath.Join(s.root, dir, e.Name())
			if dir == "corners" {
				var r CornerRecord
				if err := readJSON(p, &r); err != nil {
					return err
				}
				if !validCorner(r, r.DeviceUID) || digest([]byte(r.DeviceUID))+".json" != e.Name() {
					return ErrCorrupt
				}
			} else {
				var r derivedRecord
				if err := readJSON(p, &r); err != nil {
					return err
				}
				if !validDerived(r, e.Name()[:len(e.Name())-5]) {
					return ErrCorrupt
				}
			}
		}
	}
	return nil
}

// Collect cache references by their own use time, independently of base latest
// and shared digest issuance. Otherwise equal bytes from successive successes
// would retain an unbounded number of old provenance/cache records.
func (s *PVC) collectOverlays(ctx context.Context, retained map[string]bool, uids map[string]bool, now time.Time) error {
	for imageDigest := range s.pins {
		retained[imageDigest] = true
	}
	pending := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.root, "corners"))
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
		p := filepath.Join(s.root, "corners", e.Name())
		var r CornerRecord
		if err := readJSON(p, &r); err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		// A snapshot older than a new device record must not collect that record.
		if !uids[r.DeviceUID] && now.Sub(info.ModTime()) >= 24*time.Hour {
			if err := os.Remove(p); err != nil {
				return err
			}
			continue
		}
		if r.Pending != nil {
			pending[r.Pending.Proposed.ImageDigest] = true
			retained[r.Pending.Proposed.ImageDigest] = true
		}
	}
	entries, err = os.ReadDir(filepath.Join(s.root, "derived"))
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
		p := filepath.Join(s.root, "derived", e.Name())
		var r derivedRecord
		if err := readJSON(p, &r); err != nil {
			return err
		}
		if now.Sub(r.LastUsedAt) < 24*time.Hour+time.Minute || pending[r.Output.ImageDigest] || s.pins[r.Output.ImageDigest] > 0 {
			retained[r.Output.ImageDigest] = true
		} else if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
