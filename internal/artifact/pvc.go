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

	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// PVC supports one active writer on a POSIX filesystem with rename and fsync.
// Root is operator configuration, never renderer-controlled. Renderers mount
// per-attempt directories in Phase 4, not this repository's directories.
type PVC struct {
	root         string
	mu           sync.Mutex
	MaxBytes     int64
	MinFreeBytes uint64
	pins         map[string]int
}

var _ Repository = (*PVC)(nil)

func NewPVC(root string) (*PVC, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, errors.New("artifact root must be a clean absolute directory")
	}
	for _, dir := range []string{"blobs", "records", "latest", "issued", "identities", "derived", "corners"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	if err := syncDir(root); err != nil {
		return nil, err
	}
	return &PVC{root: root, MaxBytes: 768 << 20, MinFreeBytes: 64 << 20, pins: map[string]int{}}, nil
}

func unitKey(u Unit) (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	var value any = u
	if u.Output.IsZero() {
		value = u.legacy()
	}
	b, err := json.Marshal(value)
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
	if len(png) == 0 || len(png) >= m.Unit.byteLimit() || len(png) > imageprocessing.MaxInputBytes || !digestPattern.MatchString(m.InputRevision) || m.Sequence == 0 || m.AttemptID == "" || m.RenderedAt.IsZero() || (m.ContextDigest != "" && (m.Unit.DeviceUID == "" || !digestPattern.MatchString(m.ContextDigest))) {
		return Metadata{}, errors.New("invalid artifact publication metadata or size")
	}
	if err := s.checkCapacity(ctx, int64(len(png))+16384); err != nil {
		return Metadata{}, err
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
	// New constraints must not reset ordering when their key changes. Validate
	// sibling records using their original keys before considering sequences.
	if !m.Unit.Output.IsZero() {
		previous, err := s.related(ctx, m.Unit)
		if err != nil {
			return Metadata{}, err
		}
		for _, old := range previous {
			if old.Sequence >= m.Sequence {
				return Metadata{}, ErrObsolete
			}
		}
	}
	m.Size, m.ContentType = len(png), "image/png"
	m.RenderedAt = m.RenderedAt.UTC()
	_, m.Filename, err = s.storeBlob(png)
	if err != nil {
		return Metadata{}, err
	}
	// All immutable bytes/records are durable before the success pointer changes.
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return Metadata{}, err
	}
	// Preparing and syncing the pointer may itself consume the remaining render
	// budget. Recheck immediately before the atomic publication transition.
	if err := atomicWriteChecked(filepath.Join(s.root, "latest", key+".json"), encoded, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return nil
	}); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

func (s *PVC) latest(key string) (Metadata, error) {
	var m Metadata
	p := filepath.Join(s.root, "latest", key+".json")
	err := readJSON(p, &m)
	if err == nil {
		actual, valid := unitKey(m.Unit)
		if valid != nil || actual != key || !digestPattern.MatchString(m.ImageDigest) || !digestPattern.MatchString(m.InputRevision) || m.Sequence == 0 || m.Size <= 0 || m.Size >= m.Unit.byteLimit() || m.Size > imageprocessing.MaxInputBytes || m.RenderedAt.IsZero() || !filenamePattern.MatchString(m.Filename) {
			err = ErrCorrupt
		}
	}
	if errors.Is(err, ErrCorrupt) {
		return Metadata{}, s.discardPointer(p)
	}
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
	if errors.Is(err, ErrNotFound) && !u.Output.IsZero() {
		legacy := u
		legacy.Output = render.OutputConstraints{}
		legacyKey, keyErr := unitKey(legacy)
		if keyErr != nil {
			return Metadata{}, keyErr
		}
		m, err = s.latest(legacyKey)
		key = legacyKey
	}
	if err != nil {
		return Metadata{}, err
	}
	if m.Unit.legacy() != u.legacy() || (m.Unit != u && !m.Unit.Output.IsZero()) || !digestPattern.MatchString(m.ImageDigest) {
		return Metadata{}, s.discardPointer(filepath.Join(s.root, "latest", key+".json"))
	}
	b, err := s.Read(ctx, m.ImageDigest)
	if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrNotFound) {
		return Metadata{}, s.discardPointer(filepath.Join(s.root, "latest", key+".json"))
	}
	if err != nil {
		return Metadata{}, err
	}
	if len(b) != m.Size {
		return Metadata{}, s.discardPointer(filepath.Join(s.root, "latest", key+".json"))
	}
	if !u.Output.IsZero() {
		if err := u.ResolvedTarget().ValidateOutput(ctx, b); err != nil {
			if ctx.Err() != nil {
				return Metadata{}, ctx.Err()
			}
			// Incompatible legacy bytes remain retained and immutable, but cannot
			// become a new delivery or be rewritten by a read.
			return Metadata{}, ErrNotFound
		}
	}
	return m, nil
}

// related enumerates every output-key generation of the same owned target.
// Callers hold mu; GC uses the same latest directory for old and new keys.
func (s *PVC) related(ctx context.Context, u Unit) ([]Metadata, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "latest"))
	if err != nil {
		return nil, err
	}
	var records []Metadata
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var m Metadata
		if err := readJSON(filepath.Join(s.root, "latest", e.Name()), &m); err != nil {
			return nil, err
		}
		if m.Unit.legacy() != u.legacy() {
			continue
		}
		m, err := s.latest(e.Name()[:len(e.Name())-5])
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, m)
	}
	return records, nil
}

// Sequence returns the highest validated sequence even if an old image cannot
// be delivered under current constraints. It does not reinterpret old hashes.
func (s *PVC) Sequence(ctx context.Context, u Unit) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := u.Validate(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.related(ctx, u)
	if err != nil {
		return 0, err
	}
	var sequence uint64
	for _, m := range records {
		sequence = max(sequence, m.Sequence)
	}
	return sequence, nil
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
		observe.StorageErrors.WithLabelValues("integrity").Inc()
		return nil, ErrCorrupt
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
	if err := readJSON(filepath.Join(s.root, "issued", key+".json"), &previous); err == nil {
		if previous.DeviceUID != uid || previous.ImageDigest != imageDigest || previous.LastIssuedAt.IsZero() {
			return ErrCorrupt
		}
		if now.Before(previous.LastIssuedAt) {
			now = previous.LastIssuedAt
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if previous.DeviceUID == uid && previous.ImageDigest == imageDigest && now.Sub(previous.LastIssuedAt) < time.Minute {
		return nil
	}
	if previous.DeviceUID == uid && previous.ImageDigest == imageDigest {
		// Existing issuance bookkeeping may consume the reserve so capacity pressure
		// does not prevent delivery of already stored compatible output.
		_, free, err := s.Usage(ctx)
		if err != nil {
			return err
		}
		if free < 8192 {
			return ErrCapacity
		}
	} else if err := s.checkCapacity(ctx, 1024); err != nil {
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
	if err == nil && (i.DeviceUID != uid || i.ImageDigest != imageDigest || i.LastIssuedAt.IsZero()) {
		return false, ErrCorrupt
	}
	return i.DeviceUID == uid && i.ImageDigest == imageDigest, err
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if len(b) > 65536 {
		return ErrCorrupt
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: invalid metadata JSON", ErrCorrupt)
	}
	return nil
}

func readImage(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > imageprocessing.MaxInputBytes {
		return nil, ErrCorrupt
	}
	b := make([]byte, int(info.Size()))
	if _, err := io.ReadFull(f, b); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, ErrCorrupt
		}
		return nil, err
	}
	var extra [1]byte
	if n, err := f.Read(extra[:]); n != 0 || err != io.EOF {
		if n != 0 || err == nil {
			return nil, ErrCorrupt
		}
		return nil, err
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

// WriteJSONAtomic persists internal scheduler records with the same durability
// guarantees as artifact pointers. The caller owns the parent directory.
func WriteJSONAtomic(path string, v any) error { return writeJSON(path, v) }

// WriteAtomic persists an immutable context before its Job is submitted.
func WriteAtomic(path string, b []byte) error { return atomicWrite(path, b) }

func atomicWrite(path string, b []byte) error {
	return atomicWriteChecked(path, b, nil)
}

func atomicWriteChecked(path string, b []byte, check func() error) error {
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
	if check != nil {
		if err := check(); err != nil {
			return err
		}
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

// storeBlob shares byte-addressed persistence and filename reservations between
// base publication and derived output. The caller holds the repository lock.
func (s *PVC) storeBlob(png []byte) (imageDigest, filename string, err error) {
	imageDigest = digest(png)
	blob := filepath.Join(s.root, "blobs", imageDigest+".png")
	if existing, err := readImage(blob); err == nil {
		if digest(existing) != imageDigest {
			observe.StorageErrors.WithLabelValues("integrity").Inc()
			if err := atomicWrite(blob, png); err != nil {
				return "", "", err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrCorrupt) {
		return "", "", err
	} else if err := atomicWrite(blob, png); err != nil {
		return "", "", err
	}
	// Filename is stable for identical bytes, independent of target/unit. Random
	// 128-bit identifiers are collision checked; the firmware limit is 31 ASCII.
	recordPath := filepath.Join(s.root, "records", imageDigest+".json")
	var record struct {
		Filename string `json:"filename"`
	}
	if err := readJSON(recordPath, &record); errors.Is(err, ErrNotFound) {
		for {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return "", "", err
			}
			record.Filename = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(id[:]) + ".png"
			// Reserve filename with O_EXCL; an interrupted reservation is harmless.
			f, err := os.OpenFile(filepath.Join(s.root, "records", record.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return "", "", err
			}
			if err := f.Close(); err != nil {
				return "", "", err
			}
			break
		}
		if err := writeJSON(recordPath, record); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	if !filenamePattern.MatchString(record.Filename) {
		return "", "", ErrCorrupt
	}
	return imageDigest, record.Filename, nil
}
