package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func operationalFixture(t *testing.T) (*PVC, Metadata, []byte) {
	t.Helper()
	repo, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	m, err := repo.Publish(context.Background(), Metadata{Unit: Unit{Namespace: "arprt", ScreenUID: "screen", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}, InputRevision: strings.Repeat("a", 64), Sequence: 1, AttemptID: "first", RenderedAt: time.Now().UTC()}, b)
	if err != nil {
		t.Fatal(err)
	}
	return repo, m, b
}

func TestCapacityRejectsWritesWithoutEvictingIssuedSuccess(t *testing.T) {
	repo, m, b := operationalFixture(t)
	ctx := context.Background()
	if err := repo.Issue(ctx, "device", m.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	used, _, err := repo.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repo.MaxBytes = used
	m.Sequence++
	m.AttemptID = "second"
	if _, err := repo.Publish(ctx, m, b); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity did not stop publication", err)
	}
	if got, err := repo.Latest(ctx, m.Unit); err != nil || got.Sequence != 1 {
		t.Fatal("capacity evicted success", err)
	}
	if ok, err := repo.Authorized(ctx, "device", m.ImageDigest); !ok || err != nil {
		t.Fatal("capacity revoked URL", err)
	}
	if _, err := repo.Read(ctx, m.ImageDigest); err != nil {
		t.Fatal(err)
	}
	repo.MaxBytes = 1 << 30
	repo.MinFreeBytes = ^uint64(0)
	if err := repo.CheckCapacity(ctx, 1024); !errors.Is(err, ErrCapacity) {
		t.Fatal("free-space reserve ignored", err)
	}
}

func TestCorruptBlobAndPointerCanBeRegenerated(t *testing.T) {
	for _, kind := range []string{"blob", "pointer"} {
		t.Run(kind, func(t *testing.T) {
			repo, m, b := operationalFixture(t)
			key, _ := unitKey(m.Unit)
			path := filepath.Join(repo.root, "latest", key+".json")
			if kind == "blob" {
				path = filepath.Join(repo.root, "blobs", m.ImageDigest+".png")
			}
			if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Latest(context.Background(), m.Unit); !errors.Is(err, ErrNotFound) {
				t.Fatal("corruption was not isolated", err)
			}
			m.Sequence++
			m.AttemptID = "recovery"
			if _, err := repo.Publish(context.Background(), m, b); err != nil {
				t.Fatal("valid replacement rejected", err)
			}
			if _, err := repo.Latest(context.Background(), m.Unit); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCorruptRetentionFailsBeforeDeletingPointersOrImages(t *testing.T) {
	repo, m, _ := operationalFixture(t)
	ctx := context.Background()
	if err := repo.Issue(ctx, "device", m.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	key, _ := issuanceKey("device", m.ImageDigest)
	if err := os.WriteFile(filepath.Join(repo.root, "issued", key+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return false }, nil, time.Now().Add(48*time.Hour)); err == nil {
		t.Fatal("corrupt retention accepted")
	}
	if _, err := repo.Latest(ctx, m.Unit); err != nil {
		t.Fatal("destructive collection preceded validation", err)
	}
	if _, err := repo.Read(ctx, m.ImageDigest); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveWriterAndInterruptedWriteCleanup(t *testing.T) {
	repo, m, _ := operationalFixture(t)
	unlock, err := repo.Lock()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPVC(repo.root)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := second.Lock(); err == nil {
		release()
		t.Fatal("two writers admitted")
	}
	unlock()
	release, err := second.Lock()
	if err != nil {
		t.Fatal("lock not released", err)
	}
	defer release()
	for _, name := range []string{".pending-interrupted", strings.Repeat("A", 26) + ".png"} {
		p := filepath.Join(repo.root, "records", name)
		if err := os.WriteFile(p, []byte("orphan"), 0600); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Collect(context.Background(), func(Unit) bool { return true }, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(repo.root, "records"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("orphan cleanup failed: %d %v", len(entries), err)
	}
	if _, err := repo.Latest(context.Background(), m.Unit); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionDoesNotEraseBindingNewerThanDeviceSnapshot(t *testing.T) {
	repo, _, _ := operationalFixture(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Second)
	if err := repo.Bind(ctx, "new-device", "02:00:00:00:00:01"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return true }, map[string]bool{}, before); err != nil {
		t.Fatal(err)
	}
	if id, err := repo.Binding(ctx, "new-device"); err != nil || id != "02:00:00:00:00:01" {
		t.Fatal("stale API snapshot erased new registration", err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return true }, map[string]bool{}, before.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Binding(ctx, "new-device"); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan binding not collected", err)
	}
}

func TestCorruptFilenameRecordCannotReplaceValidDelivery(t *testing.T) {
	repo, m, b := operationalFixture(t)
	if err := os.WriteFile(filepath.Join(repo.root, "records", m.ImageDigest+".json"), []byte(`{"filename":"../invalid.png"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.Sequence++
	m.AttemptID = "after-damage"
	if _, err := repo.Publish(context.Background(), m, b); !errors.Is(err, ErrCorrupt) {
		t.Fatal("unsafe firmware filename published", err)
	}
	if got, err := repo.Latest(context.Background(), m.Unit); err != nil || got.Sequence != 1 {
		t.Fatal("corrupt filename replaced valid pointer", err)
	}
}
