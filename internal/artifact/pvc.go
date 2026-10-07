package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// PVC supports one active writer on a POSIX filesystem with rename and fsync.
// Root is operator configuration, never renderer-controlled. Renderers mount
// per-attempt directories in Phase 4, not this repository's directories.
type PVC struct {
	root string
	mu   sync.Mutex
}

var _ Repository = (*PVC)(nil)

func NewPVC(root string) (*PVC, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, errors.New("artifact root must be a clean absolute directory")
	}
	for _, dir := range []string{"blobs", "records", "latest", "issued"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	if err := syncDir(root); err != nil {
		return nil, err
	}
	return &PVC{root: root}, nil
}

func unitKey(u Unit) (string, error) {
	if u.Namespace == "" || u.ScreenUID == "" {
		return "", errors.New("artifact unit requires namespace and screen UID")
	}
	if err := u.Target.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(u)
	if err != nil {
		return "", err
	}
	return digest(b), nil
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *PVC) Publish(ctx context.Context, m Metadata, png []byte) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := unitKey(m.Unit)
	if err != nil {
		return Metadata{}, err
	}
	if len(png) == 0 || len(png) >= 90000 || !digestPattern.MatchString(m.InputRevision) || m.Sequence == 0 || m.AttemptID == "" || m.RenderedAt.IsZero() || (m.ContextDigest != "" && (m.Unit.DeviceUID == "" || !digestPattern.MatchString(m.ContextDigest))) {
		return Metadata{}, errors.New("invalid artifact publication metadata or size")
	}
	m.ImageDigest = digest(png)
	previous, err := s.latest(key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Metadata{}, err
	}
	if err == nil && previous.Sequence >= m.Sequence {
		if previous.Sequence == m.Sequence && previous.AttemptID == m.AttemptID && previous.ImageDigest == m.ImageDigest && previous.InputRevision == m.InputRevision {
			return previous, nil
		}
		return Metadata{}, ErrObsolete
	}
	m.Size, m.ContentType = len(png), "image/png"
	m.RenderedAt = m.RenderedAt.UTC()
	blob := filepath.Join(s.root, "blobs", m.ImageDigest+".png")
	if existing, err := readImage(blob); err == nil {
		if digest(existing) != m.ImageDigest {
			return Metadata{}, errors.New("stored image integrity failure")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, err
	} else if err := atomicWrite(blob, png); err != nil {
		return Metadata{}, err
	}
	// Filename is stable for identical bytes, independent of target/unit. Random
	// 128-bit identifiers are collision checked; the firmware limit is 31 ASCII.
	recordPath := filepath.Join(s.root, "records", m.ImageDigest+".json")
	var record struct {
		Filename string `json:"filename"`
	}
	if err := readJSON(recordPath, &record); errors.Is(err, ErrNotFound) {
		for {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return Metadata{}, err
			}
			record.Filename = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(id[:]) + ".png"
			// Reserve filename with O_EXCL; an interrupted reservation is harmless.
			f, err := os.OpenFile(filepath.Join(s.root, "records", record.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return Metadata{}, err
			}
			if err := f.Close(); err != nil {
				return Metadata{}, err
			}
			break
		}
		if err := writeJSON(recordPath, record); err != nil {
			return Metadata{}, err
		}
	} else if err != nil {
		return Metadata{}, err
	}
	m.Filename = record.Filename
	// All immutable bytes/records are durable before the success pointer changes.
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	if err := writeJSON(filepath.Join(s.root, "latest", key+".json"), m); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

func (s *PVC) latest(key string) (Metadata, error) {
	var m Metadata
	err := readJSON(filepath.Join(s.root, "latest", key+".json"), &m)
	return m, err
}

func (s *PVC) Latest(ctx context.Context, u Unit) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	key, err := unitKey(u)
	if err != nil {
		return Metadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.latest(key)
	if err != nil {
		return Metadata{}, err
	}
	if m.Unit != u || !digestPattern.MatchString(m.ImageDigest) {
		return Metadata{}, errors.New("invalid artifact pointer")
	}
	b, err := s.Read(ctx, m.ImageDigest)
	if err != nil {
		return Metadata{}, err
	}
	if len(b) != m.Size {
		return Metadata{}, errors.New("stored image size mismatch")
	}
	return m, nil
}

func (s *PVC) Read(ctx context.Context, imageDigest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(imageDigest) {
		return nil, errors.New("invalid image digest")
	}
	b, err := readImage(filepath.Join(s.root, "blobs", imageDigest+".png"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if digest(b) != imageDigest {
		return nil, errors.New("stored image integrity failure")
	}
	return b, nil
}

type issuance struct {
	DeviceUID    string    `json:"deviceUID"`
	ImageDigest  string    `json:"imageDigest"`
	LastIssuedAt time.Time `json:"lastIssuedAt"`
}

func issuanceKey(deviceUID, imageDigest string) (string, error) {
	if deviceUID == "" || !digestPattern.MatchString(imageDigest) {
		return "", errors.New("invalid issuance identity")
	}
	return digest([]byte(deviceUID + "\x00" + imageDigest)), nil
}

func (s *PVC) Issue(ctx context.Context, uid, imageDigest string, now time.Time) error {
	key, err := issuanceKey(uid, imageDigest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Read(ctx, imageDigest); err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("issuance time is required")
	}
	var previous issuance
	if err := readJSON(filepath.Join(s.root, "issued", key+".json"), &previous); err == nil && now.Before(previous.LastIssuedAt) {
		now = previous.LastIssuedAt
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return writeJSON(filepath.Join(s.root, "issued", key+".json"), issuance{uid, imageDigest, now.UTC()})
}

func (s *PVC) Authorized(ctx context.Context, uid, imageDigest string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := issuanceKey(uid, imageDigest)
	if err != nil {
		return false, err
	}
	var i issuance
	err = readJSON(filepath.Join(s.root, "issued", key+".json"), &i)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return i.DeviceUID == uid && i.ImageDigest == imageDigest, err
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("invalid artifact metadata: %w", err)
	}
	return nil
}

func readImage(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 90000))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) >= 90000 {
		return nil, errors.New("stored image size outside device limits")
	}
	return b, nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
}

func atomicWrite(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
