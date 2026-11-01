package artifact

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Referenced manifests are validated before any destructive collection by the
// device-root scan. Unreferenced objects have a full diagnostic-day grace period.
func (s *PVC) collectManifests(ctx context.Context, now time.Time) error {
	refs := map[string]bool{}
	devices, err := os.ReadDir(filepath.Join(s.root, "corners"))
	if err != nil {
		return err
	}
	for _, e := range devices {
		if err := ctx.Err(); err != nil {
			return err
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var r CornerRecord
		if err := strictRead(filepath.Join(s.root, "corners", e.Name()), MaxDeviceRecordBytes, &r); err != nil {
			return err
		}
		for _, p := range r.Pairs {
			if p.StateRef != "" {
				refs[p.StateRef] = true
			}
			if p.CandidateRef != "" {
				refs[p.CandidateRef] = true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "manifests"))
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
		ref := e.Name()[:len(e.Name())-5]
		if !digestPattern.MatchString(ref) || refs[ref] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) >= 24*time.Hour {
			if err := os.Remove(filepath.Join(s.root, "manifests", e.Name())); err != nil {
				return err
			}
		}
	}
	return syncDir(filepath.Join(s.root, "manifests"))
}

// Expire detached payloads while retaining an epoch tombstone. Recreating a pair
// must not reuse an old epoch, and capacity never silently evicts retained pairs.
func (s *PVC) expireDetached(ctx context.Context, now time.Time) error {
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
		var r CornerRecord
		if err := strictRead(filepath.Join(s.root, "corners", e.Name()), MaxDeviceRecordBytes, &r); err != nil {
			return err
		}
		if r.Pending != nil {
			continue
		}
		changed := false
		for key, p := range r.Pairs {
			if p.DetachedAt != nil && now.Sub(*p.DetachedAt) >= PairRetention && (p.StateRef != "" || p.Generation != 0 || p.LastConsumed != "") {
				p.Epoch++
				p.Generation = 0
				p.StateRef = ""
				p.CandidateRef = ""
				p.LastConsumed = ""
				r.Pairs[key] = p
				changed = true
			}
		}
		if changed {
			r.Revision++
			if err := s.writeDevice(ctx, r, true); err != nil {
				return err
			}
		}
	}
	return nil
}
