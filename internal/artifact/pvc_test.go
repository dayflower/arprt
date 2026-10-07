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

func TestPublicationRecoveryOwnershipAndOrdering(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	png, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	u := Unit{Namespace: "trmnl", ScreenUID: "screen-1", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	m := Metadata{Unit: u, ScreenGeneration: 1, InputRevision: strings.Repeat("a", 64), AttemptID: "attempt-1", Sequence: 1, RenderedAt: time.Now()}
	published, err := s.Publish(ctx, m, png)
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Filename) > 31 || published.ContentType != "image/png" || published.Size != len(png) {
		t.Fatal("invalid device-facing artifact metadata")
	}
	if _, err := s.Publish(ctx, m, png); err != nil {
		t.Fatal("idempotent publication failed", err)
	}
	if err := s.Issue(ctx, "device-1", published.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Reopen the PVC as after a controller restart.
	s, err = NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.Latest(ctx, u)
	if err != nil || latest.ImageDigest != published.ImageDigest {
		t.Fatal("success pointer did not survive restart", err)
	}
	for _, uid := range []string{"device-1", "device-recreated"} {
		allowed, err := s.Authorized(ctx, uid, published.ImageDigest)
		if err != nil || allowed != (uid == "device-1") {
			t.Fatal("issuance crossed device UID boundary", err)
		}
	}
	m.Sequence, m.AttemptID, m.InputRevision = 2, "attempt-2", strings.Repeat("b", 64)
	if _, err := s.Publish(ctx, m, nil); err == nil {
		t.Fatal("empty failed output was published")
	}
	withContext := m
	withContext.ContextDigest = strings.Repeat("c", 64)
	if _, err := s.Publish(ctx, withContext, png); err == nil {
		t.Fatal("GET unit accepted device context metadata")
	}
	latest, err = s.Latest(ctx, u)
	if err != nil || latest.InputRevision != published.InputRevision {
		t.Fatal("failed publication replaced prior success")
	}
	newer, err := s.Publish(ctx, m, png)
	if err != nil {
		t.Fatal(err)
	}
	if newer.Filename != published.Filename {
		t.Fatal("same bytes changed firmware cache filename")
	}
	m.Sequence, m.AttemptID = 1, "late-attempt"
	if _, err := s.Publish(ctx, m, png); !errors.Is(err, ErrObsolete) {
		t.Fatal("late result replaced newer success", err)
	}
	for _, incompatible := range []Unit{
		{Namespace: "other", ScreenUID: u.ScreenUID, Target: u.Target},
		{Namespace: u.Namespace, ScreenUID: "screen-recreated", Target: u.Target},
		{Namespace: u.Namespace, ScreenUID: u.ScreenUID, DeviceUID: "private-device", Target: u.Target},
		{Namespace: u.Namespace, ScreenUID: u.ScreenUID, Target: render.Target{Width: 1200, Height: 825, Palette: "gray16"}},
	} {
		if _, err := s.Latest(ctx, incompatible); !errors.Is(err, ErrNotFound) {
			t.Fatal("incompatible unit reused artifact", err)
		}
	}
	// Unreferenced immutable bytes after interrupted publication are harmless.
	if err := os.WriteFile(filepath.Join(root, "blobs", strings.Repeat("c", 64)+".png"), png, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	latest, err = s.Latest(ctx, u)
	if err != nil || latest.Sequence != 2 {
		t.Fatal("orphan image changed latest selection", err)
	}
	if err := os.WriteFile(filepath.Join(root, "blobs", published.ImageDigest+".png"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Latest(ctx, u); err == nil {
		t.Fatal("corrupted image was accepted")
	}
}

func TestDeviceAwareIsolation(t *testing.T) {
	s, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	png, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	u := Unit{Namespace: "trmnl", ScreenUID: "screen-1", DeviceUID: "device-1", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	m := Metadata{Unit: u, InputRevision: strings.Repeat("a", 64), ContextDigest: strings.Repeat("b", 64), Sequence: 1, AttemptID: "attempt-1", RenderedAt: time.Now()}
	if _, err := s.Publish(context.Background(), m, png); err != nil {
		t.Fatal(err)
	}
	u.DeviceUID = "device-2"
	if _, err := s.Latest(context.Background(), u); !errors.Is(err, ErrNotFound) {
		t.Fatal("private artifact crossed devices")
	}
	if _, err := s.Read(context.Background(), "../../file"); err == nil {
		t.Fatal("invalid digest accepted")
	}
}
