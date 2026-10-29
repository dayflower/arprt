// Package jobrender schedules and collects PVC-backed Kubernetes render Jobs.
package jobrender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/validation"
)

type attempt struct {
	ID             string        `json:"id"`
	Sequence       uint64        `json:"sequence"`
	StartedAt      time.Time     `json:"startedAt"`
	ContextDigest  string        `json:"contextDigest"`
	Source         api.JobSource `json:"source"`
	TerminalReason string        `json:"terminalReason,omitempty"`
	Submitted      bool          `json:"submitted,omitempty"`
	Trigger        Trigger       `json:"trigger,omitempty"`
	Deadline       time.Time     `json:"deadline,omitzero"`
}

type state struct {
	// Zero denotes the original scheduler DTO. Version 2 carries explicit
	// output constraints; active IDs and immutable contexts are never defaulted.
	Version          int           `json:"version,omitempty"`
	Namespace        string        `json:"namespace"`
	DeviceName       string        `json:"deviceName"`
	DeviceUID        string        `json:"deviceUID"`
	ScreenName       string        `json:"screenName"`
	ScreenGeneration int64         `json:"screenGeneration"`
	Unit             artifact.Unit `json:"unit"`
	Revision         string        `json:"revision"`
	Sequence         uint64        `json:"sequence"`
	Due              time.Time     `json:"due"`
	Failures         int           `json:"failures"`
	Reason           string        `json:"reason,omitempty"`
	Active           *attempt      `json:"active,omitempty"`
	Trigger          Trigger       `json:"trigger,omitempty"`
	DeviceGeneration int64         `json:"deviceGeneration,omitempty"`
	Assignment       string        `json:"assignment,omitempty"`
}

type Store struct{ Root string }

func NewStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, errors.New("invalid scheduler root")
	}
	for _, dir := range []string{"jobs", "runs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	if err := syncDirectory(root); err != nil {
		return nil, err
	}
	store := &Store{Root: root}
	// Migrate Phase 4's one-record-per-device layout without losing active work.
	records, err := store.records()
	if err != nil {
		return nil, err
	}
	for _, st := range records {
		old := store.path(st.Namespace, st.DeviceName)
		if _, err := os.Stat(old); err == nil {
			dest := store.path(st.Namespace, st.DeviceName, st.ScreenName)
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("conflicting scheduler migration records")
			}
			if err := os.Rename(old, dest); err != nil {
				return nil, err
			}
		}
	}
	if err := syncDirectory(filepath.Join(root, "jobs")); err != nil {
		return nil, err
	}
	return store, nil
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Store) path(namespace, name string, screenName ...string) string {
	key := namespace + "/" + name
	if len(screenName) > 0 {
		key += "/" + screenName[0]
	}
	return filepath.Join(s.Root, "jobs", hash([]byte(key))+".json")
}
func (s *Store) load(namespace, name string, screenName ...string) (*state, error) {
	b, err := readStateFile(s.path(namespace, name, screenName...))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err = json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Namespace != namespace || st.DeviceName != name || (len(screenName) > 0 && st.ScreenName != screenName[0]) || (st.Active != nil && !validID(st.Active.ID)) {
		return nil, errors.New("invalid scheduler record")
	}
	if err := validateState(&st); err != nil {
		return nil, err
	}
	return &st, nil
}
func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == hex.EncodeToString(b)
}
func (s *Store) save(st *state) error {
	if !st.Unit.Output.IsZero() {
		st.Version = 2
	}
	if err := validateState(st); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("scheduler record exceeds size limit")
	}
	return artifact.WriteAtomic(s.path(st.Namespace, st.DeviceName, st.ScreenName), b)
}
func (s *Store) run(id string) string { return filepath.Join(s.Root, "runs", id) }
func (s *Store) prepare(a *attempt, snapshot []byte) error {
	if !validID(a.ID) {
		return errors.New("invalid run identity")
	}
	if len(snapshot) > 65536 || hash(snapshot) != a.ContextDigest {
		return errors.New("immutable context digest mismatch")
	}
	contextPath := filepath.Join(s.run(a.ID), "context.json")
	existing := false
	if old, err := readSnapshot(contextPath); err == nil {
		if hash(old) != a.ContextDigest {
			return errors.New("attempt context already exists with different bytes")
		}
		existing = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.run(a.ID), "output"), 0750); err != nil {
		return err
	}
	// Runs are traversable by renderer UID 1000; only output is mounted writable.
	if err := os.Chmod(s.run(a.ID), 0750); err != nil {
		return err
	}
	if !existing {
		if err := artifact.WriteAtomic(contextPath, snapshot); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Join(s.run(a.ID), "context.json"), 0444); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(s.run(a.ID), "context.json"))
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(s.Root, "runs"))
}

func readSnapshot(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("invalid immutable context file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, err
	}
	if len(b) > 65536 {
		return nil, errors.New("immutable context exceeds size limit")
	}
	return b, nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Store) collectInput(ctx context.Context, a *attempt) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshotRoot, err := os.OpenRoot(s.run(a.ID))
	if err != nil {
		return nil, err
	}
	defer snapshotRoot.Close()
	snapshot, err := snapshotRoot.OpenFile("context.json", os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := snapshot.Stat()
	if err != nil || !info.Mode().IsRegular() {
		snapshot.Close()
		return nil, errors.New("context must be a regular file")
	}
	contextBytes, readErr := io.ReadAll(io.LimitReader(snapshot, 65537))
	closeErr := snapshot.Close()
	if readErr != nil || closeErr != nil || len(contextBytes) > 65536 || hash(contextBytes) != a.ContextDigest {
		return nil, errors.New("immutable context integrity failure")
	}

	// Root-relative opening rejects links outside the writable output subtree.
	// O_NONBLOCK prevents a FIFO from stalling the controller's work queue.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Join(s.run(a.ID), "output"))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(a.Source.ImagePath), os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("renderer output must be a regular file")
	}
	if info.Size() > imageprocessing.MaxInputBytes {
		return nil, errors.New("renderer input exceeds byte limit")
	}
	b, err := imageprocessing.ReadInput(ctx, f)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// recordPolicy preserves the callers' existing key and decoding-error contracts.
// Startup migration and reconciliation accept both Phase 4 device and pair keys;
// maintenance accepts only pair keys and reports malformed JSON generically.
type recordPolicy uint8

const (
	migrationCompatibleRecords recordPolicy = iota
	currentPairRecords
)

func (s *Store) records() ([]state, error) {
	var records []state
	err := s.walkRecords(migrationCompatibleRecords, func(_ os.DirEntry, st *state) error {
		records = append(records, *st)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// walkRecords validates one entry immediately before visiting it, in ReadDir
// order. Maintenance receives pending entries with nil state so cleanup stays
// interleaved with record effects and errors under the reconciler's mutex.
func (s *Store) walkRecords(policy recordPolicy, visit func(os.DirEntry, *state) error) error {
	entries, err := os.ReadDir(filepath.Join(s.Root, "jobs"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if policy == currentPairRecords && strings.HasPrefix(entry.Name(), ".pending-") {
			if err := visit(entry, nil); err != nil {
				return err
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readStateFile(filepath.Join(s.Root, "jobs", entry.Name()))
		if err != nil {
			return err
		}
		var st state
		if err := json.Unmarshal(b, &st); err != nil {
			if policy == currentPairRecords {
				return errors.New("corrupt scheduler metadata")
			}
			return err
		}
		if err := validateState(&st); err != nil {
			return err
		}
		if entry.Name() != filepath.Base(s.path(st.Namespace, st.DeviceName, st.ScreenName)) &&
			(policy != migrationCompatibleRecords || entry.Name() != filepath.Base(s.path(st.Namespace, st.DeviceName))) {
			return errors.New("invalid scheduler key")
		}
		if err := visit(entry, &st); err != nil {
			return err
		}
	}
	return nil
}

func readStateFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("scheduler record exceeds size limit")
	}
	return b, nil
}
func validateState(st *state) error {
	if !st.Trigger.valid() {
		return errors.New("invalid scheduler trigger")
	}
	if (st.Version != 0 && st.Version != 2) || (st.Version == 0 && !st.Unit.Output.IsZero()) || (st.Version == 2 && st.Unit.Output.IsZero()) {
		return errors.New("unknown scheduler compatibility version")
	}
	if st.Namespace == "" || len(validation.IsDNS1123Label(st.Namespace)) > 0 || st.DeviceName == "" || len(validation.IsDNS1123Subdomain(st.DeviceName)) > 0 || st.ScreenName == "" || len(validation.IsDNS1123Subdomain(st.ScreenName)) > 0 || st.Unit.Namespace != st.Namespace || st.Unit.DeviceUID != st.DeviceUID || st.DeviceUID == "" || st.Unit.ScreenUID == "" || st.Unit.Validate() != nil || len(st.Revision) != 64 {
		return errors.New("invalid scheduler record")
	}
	if st.Active != nil {
		if !st.Active.Trigger.valid() || st.Active.Trigger != st.Trigger || (st.Trigger == OnDemand && (st.Active.Deadline.IsZero() || !st.Active.Deadline.After(st.Active.StartedAt))) {
			return errors.New("invalid scheduler execution deadline or trigger")
		}
		if !validID(st.Active.ID) || st.Active.Sequence == 0 || st.Active.StartedAt.IsZero() {
			return errors.New("invalid scheduler attempt")
		}
		if _, err := normalize(st.Active.Source); err != nil {
			return errors.New("invalid scheduler source")
		}
	}
	return nil
}
