package artifact

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
)

var hardwareIDPattern = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

type binding struct {
	DeviceUID  string `json:"deviceUID"`
	HardwareID string `json:"hardwareID"`
}

func (s *PVC) binding(uid string) (string, error) {
	if uid == "" {
		return "", errors.New("device UID is required")
	}
	var b binding
	if err := readJSON(filepath.Join(s.root, "identities", digest([]byte(uid))+".json"), &b); err != nil {
		return "", err
	}
	if b.DeviceUID != uid || !hardwareIDPattern.MatchString(b.HardwareID) {
		return "", errors.New("invalid identity registration record")
	}
	return b.HardwareID, nil
}

func (s *PVC) Binding(ctx context.Context, uid string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binding(uid)
}

func (s *PVC) Bind(ctx context.Context, uid, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if uid == "" || !hardwareIDPattern.MatchString(id) {
		return errors.New("invalid device identity registration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.binding(uid)
	if err == nil {
		if previous != id {
			return ErrIdentityConflict
		}
		// Retry a directory fsync that may have failed after an earlier rename.
		return syncDir(filepath.Join(s.root, "identities"))
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return writeJSON(filepath.Join(s.root, "identities", digest([]byte(uid))+".json"), binding{uid, id})
}
