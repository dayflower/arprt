// Package jobrender schedules and collects PVC-backed Kubernetes render Jobs.
package jobrender

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"golang.org/x/sys/unix"
)

type attempt struct {
	ID             string        `json:"id"`
	Sequence       uint64        `json:"sequence"`
	StartedAt      time.Time     `json:"startedAt"`
	ContextDigest  string        `json:"contextDigest"`
	Source         api.JobSource `json:"source"`
	TerminalReason string        `json:"terminalReason,omitempty"`
	Submitted      bool          `json:"submitted,omitempty"`
}

type state struct {
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
	b, err := os.ReadFile(s.path(namespace, name, screenName...))
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
	return &st, nil
}
func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == hex.EncodeToString(b)
}
func (s *Store) save(st *state) error {
	return artifact.WriteJSONAtomic(s.path(st.Namespace, st.DeviceName, st.ScreenName), st)
}
func (s *Store) run(id string) string { return filepath.Join(s.Root, "runs", id) }
func (s *Store) prepare(a *attempt, snapshot []byte) error {
	if !validID(a.ID) {
		return errors.New("invalid run identity")
	}
	if err := os.MkdirAll(filepath.Join(s.run(a.ID), "output"), 0750); err != nil {
		return err
	}
	// Runs are traversable by renderer UID 1000; only output is mounted writable.
	if err := os.Chmod(s.run(a.ID), 0750); err != nil {
		return err
	}
	if err := artifact.WriteAtomic(filepath.Join(s.run(a.ID), "context.json"), snapshot); err != nil {
		return err
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

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Store) collect(a *attempt, target render.Target) ([]byte, error) {
	// Root-relative opening rejects links outside the writable output subtree.
	// O_NONBLOCK prevents a FIFO from stalling the controller's work queue.
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
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("renderer output must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 90000))
	if err != nil {
		return nil, err
	}
	if err = render.ValidatePNG(b, target); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) records() ([]state, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "jobs"))
	if err != nil {
		return nil, err
	}
	var records []state
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Root, "jobs", e.Name()))
		if err != nil {
			return nil, err
		}
		var st state
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, err
		}
		records = append(records, st)
	}
	return records, nil
}
