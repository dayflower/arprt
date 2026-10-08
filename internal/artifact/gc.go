package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Collect removes obsolete selection pointers and unreferenced immutable files.
// Latest compatible successes and URLs issued within 24h are always retained.
// keep is computed from uncached current assignments, UIDs and targets.
func (s *PVC) Collect(ctx context.Context, keep func(Unit) bool, deviceUIDs map[string]bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	retained := map[string]bool{}
	latest, err := os.ReadDir(filepath.Join(s.root, "latest"))
	if err != nil {
		return err
	}
	for _, e := range latest {
		if err = ctx.Err(); err != nil {
			return err
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(s.root, "latest", e.Name())
		var m Metadata
		if err = readJSON(p, &m); err != nil {
			return err
		}
		if keep(m.Unit) {
			retained[m.ImageDigest] = true
		} else if err = os.Remove(p); err != nil {
			return err
		}
	}
	issued, err := os.ReadDir(filepath.Join(s.root, "issued"))
	if err != nil {
		return err
	}
	for _, e := range issued {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(s.root, "issued", e.Name())
		var i issuance
		if err = readJSON(p, &i); err != nil {
			return err
		}
		if now.Sub(i.LastIssuedAt) < 24*time.Hour {
			retained[i.ImageDigest] = true
		} else if err = os.Remove(p); err != nil {
			return err
		}
	}
	blobs, err := os.ReadDir(filepath.Join(s.root, "blobs"))
	if err != nil {
		return err
	}
	for _, e := range blobs {
		if err = ctx.Err(); err != nil {
			return err
		}
		if filepath.Ext(e.Name()) != ".png" {
			continue
		}
		d := e.Name()[:len(e.Name())-4]
		if !digestPattern.MatchString(d) || retained[d] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < 24*time.Hour {
			continue
		}
		p := filepath.Join(s.root, "records", d+".json")
		var record struct {
			Filename string `json:"filename"`
		}
		if err = readJSON(p, &record); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err = os.Remove(filepath.Join(s.root, "blobs", e.Name())); err != nil {
			return err
		}
		if err = os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Base(record.Filename) == record.Filename && record.Filename != "" {
			if err = os.Remove(filepath.Join(s.root, "records", record.Filename)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	identities, err := os.ReadDir(filepath.Join(s.root, "identities"))
	if err != nil {
		return err
	}
	for _, e := range identities {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(s.root, "identities", e.Name())
		var b binding
		if err = readJSON(p, &b); err != nil {
			return err
		}
		if !deviceUIDs[b.DeviceUID] {
			if err = os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}
