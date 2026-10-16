package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func TestCollectionKeepsLatestAndIssuedImages(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, err := NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := Unit{Namespace: "arprt", ScreenUID: "screen", DeviceUID: "device", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	m, err := repo.Publish(ctx, Metadata{Unit: u, ScreenGeneration: 1, InputRevision: digest([]byte("revision")), AttemptID: "attempt", Sequence: 1, RenderedAt: now.Add(-48 * time.Hour)}, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(filepath.Join(root, "blobs", m.ImageDigest+".png"), now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = repo.Collect(ctx, func(Unit) bool { return true }, map[string]bool{"device": true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Latest(ctx, u); err != nil {
		t.Fatal("latest success removed", err)
	}
	if err = repo.Issue(ctx, "device", m.ImageDigest, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.Collect(ctx, func(Unit) bool { return false }, map[string]bool{}, now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Latest(ctx, u); !errors.Is(err, ErrNotFound) {
		t.Fatal("obsolete pointer survived")
	}
	if allowed, err := repo.Authorized(ctx, "device", m.ImageDigest); !allowed || err != nil {
		t.Fatal("issued URL lost before 24h", err)
	}
	if _, err = repo.Read(ctx, m.ImageDigest); err != nil {
		t.Fatal("issued bytes removed", err)
	}
	if err = repo.Collect(ctx, func(Unit) bool { return false }, map[string]bool{}, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Read(ctx, m.ImageDigest); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan bytes not removed")
	}
}
