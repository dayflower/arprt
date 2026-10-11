package artifact

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/dayflower/arprt/internal/observe"
	"golang.org/x/sys/unix"
)

var filenamePattern = regexp.MustCompile(`^[A-Z2-7]{26}\.png$`)

// Usage does not follow symlinks, including renderer-created output links.
func (s *PVC) Usage(ctx context.Context) (int64, uint64, error) {
	var used int64
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if path != s.root && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if errors.Is(err, os.ErrNotExist) {
				return nil
			} // A concurrent atomic rename.
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(s.root, &stat); err != nil {
		return 0, 0, err
	}
	free := stat.Bavail * uint64(stat.Bsize)
	observe.StorageBytes.Set(float64(used))
	observe.StorageFree.Set(float64(free))
	observe.StorageLimit.Set(float64(s.MaxBytes))
	return used, free, nil
}

func (s *PVC) CheckCapacity(ctx context.Context, additional int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkCapacity(ctx, additional)
}
func (s *PVC) checkCapacity(ctx context.Context, additional int64) error {
	used, free, err := s.Usage(ctx)
	if err != nil {
		return err
	}
	if additional < 0 || used > s.MaxBytes-additional || free < s.MinFreeBytes || uint64(additional) > free-s.MinFreeBytes {
		observe.StorageErrors.WithLabelValues("capacity").Inc()
		return ErrCapacity
	}
	return nil
}

// Only selection pointers are regenerable. Never discard identity/issuance records.
func (s *PVC) discardPointer(path string) error {
	observe.StorageErrors.WithLabelValues("pointer").Inc()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return ErrNotFound
}

// Validate retention roots before deleting anything. Malformed metadata cannot
// silently remove the only references protecting an issued image.
func (s *PVC) validateRetention(ctx context.Context, now time.Time) error {
	maxAge := 0.0
	for _, dir := range []string{"latest", "issued", "identities"} {
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
			switch dir {
			case "latest":
				var m Metadata
				if err := readJSON(p, &m); err != nil {
					return err
				}
				key, err := unitKey(m.Unit)
				if err != nil || key+".json" != e.Name() || !digestPattern.MatchString(m.ImageDigest) || m.RenderedAt.IsZero() {
					return ErrCorrupt
				}
				maxAge = max(maxAge, now.Sub(m.RenderedAt).Seconds())
			case "issued":
				var i issuance
				if err := readJSON(p, &i); err != nil {
					return err
				}
				key, err := issuanceKey(i.DeviceUID, i.ImageDigest)
				if err != nil || key+".json" != e.Name() || i.LastIssuedAt.IsZero() {
					return ErrCorrupt
				}
			case "identities":
				var b binding
				if err := readJSON(p, &b); err != nil {
					return err
				}
				if b.DeviceUID == "" || !hardwareIDPattern.MatchString(b.HardwareID) || digest([]byte(b.DeviceUID))+".json" != e.Name() {
					return ErrCorrupt
				}
			}
		}
	}
	if err := s.validateOverlayRetention(ctx); err != nil {
		return err
	}
	observe.ArtifactAge.Set(maxAge)
	return nil
}

// Lock prevents overlapping controllers from writing the same PVC, including
// accidental scale-up during a rollout. It is advisory, for arprt processes.
func (s *PVC) Lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.root, ".controller-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("artifact root already has an active controller")
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
