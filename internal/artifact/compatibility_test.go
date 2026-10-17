package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func modernUnit(p imageprocessing.Palette, w, h, limit int) Unit {
	return Unit{Namespace: "arprt", ScreenUID: "screen", Target: render.Target{Width: int32(w), Height: int32(h), Palette: string(p)}, Output: render.OutputConstraints{PNGByteLimit: limit, Compatibility: render.OutputCompatibilityVersion}}
}
func publication(u Unit, sequence uint64) Metadata {
	return Metadata{Unit: u, InputRevision: strings.Repeat("a", 64), Sequence: sequence, AttemptID: "attempt", RenderedAt: time.Now().UTC()}
}
func encoded(t *testing.T, u Unit, entropy bool) []byte {
	t.Helper()
	indices := make([]byte, int(u.Target.Width)*int(u.Target.Height))
	colorCount := uint32(len(imageprocessing.Palette(u.Target.Palette).Colors()))
	rng := uint32(1)
	for i := range indices {
		if entropy {
			rng ^= rng << 13
			rng ^= rng >> 17
			rng ^= rng << 5
		}
		indices[i] = byte(rng % colorCount)
	}
	b, err := imageprocessing.Encode(t.Context(), indices, u.ResolvedTarget().ImageTarget())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLegacyKeysAndStaleFallback(t *testing.T) {
	for _, rotation := range []int32{0, 90, 180, 270} {
		u := modernUnit(imageprocessing.Gray4, 800, 480, 90000)
		u.Target.Rotation = rotation
		legacy := u
		legacy.Output = render.OutputConstraints{}
		// This is the original serialized Unit, independent of the new DTO.
		rotationJSON := ""
		if rotation != 0 {
			v, _ := json.Marshal(rotation)
			rotationJSON = `,"rotation":` + string(v)
		}
		original := []byte(`{"namespace":"arprt","screenUID":"screen","target":{"width":800,"height":480,"palette":"gray4"` + rotationJSON + `}}`)
		key, err := unitKey(legacy)
		if err != nil || key != digest(original) {
			t.Fatalf("legacy hash changed: %v", err)
		}
		b, err := json.Marshal(legacy)
		if err != nil || !bytes.Equal(b, original) {
			t.Fatalf("legacy DTO changed: %s: %v", b, err)
		}
		repo, err := NewPVC(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// A legacy black/white encoding satisfies both contracts.
		data, err := imageprocessing.Encode(t.Context(), make([]byte, 800*480), u.ResolvedTarget().ImageTarget())
		if err != nil {
			t.Fatal(err)
		}
		old, err := repo.Publish(t.Context(), publication(legacy, 7), data)
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(repo.root, "latest", key+".json"))
		if err != nil {
			t.Fatal(err)
		}
		repo, err = NewPVC(repo.root)
		if err != nil {
			t.Fatal(err)
		}
		fallback, err := repo.Latest(t.Context(), u)
		if err != nil || fallback != old || fallback.Fresh(u, old.InputRevision) {
			t.Fatalf("legacy fallback/freshness: %v", err)
		}
		after, _ := os.ReadFile(filepath.Join(repo.root, "latest", key+".json"))
		if !bytes.Equal(before, after) {
			t.Fatal("read rewrote old pointer")
		}
		if sequence, err := repo.Sequence(t.Context(), u); err != nil || sequence != 7 {
			t.Fatalf("sequence: %d, %v", sequence, err)
		}
		if _, err := repo.Publish(t.Context(), publication(u, 7), data); !errors.Is(err, ErrObsolete) {
			t.Fatal("migration reset publication ordering", err)
		}
		current, err := repo.Publish(t.Context(), publication(u, 8), data)
		if err != nil {
			t.Fatal(err)
		}
		if !current.Fresh(u, old.InputRevision) || current.Filename != old.Filename {
			t.Fatal("current identity or immutable filename changed")
		}
		if _, err := repo.Publish(t.Context(), publication(u, 8), data); err != nil {
			t.Fatal("current publication retry", err)
		}
		u.Output.PNGByteLimit++
		if _, err := repo.Publish(t.Context(), publication(u, 8), data); !errors.Is(err, ErrObsolete) {
			t.Fatal("budget key reset ordering", err)
		}
		if sequence, err := repo.Sequence(t.Context(), u); err != nil || sequence != 8 {
			t.Fatal(sequence, err)
		}
	}
}

func TestIncompatibleLegacyBytesRemainIssuedAndRetained(t *testing.T) {
	u := modernUnit(imageprocessing.Gray4, 800, 480, 90000)
	legacy := u
	legacy.Output = render.OutputConstraints{}
	repo, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	old, err := repo.Publish(t.Context(), publication(legacy, 11), data)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repo.Issue(t.Context(), "device", old.ImageDigest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Latest(t.Context(), u); !errors.Is(err, ErrNotFound) {
		t.Fatal("legacy intermediate grays acquired current compatibility", err)
	}
	if seq, err := repo.Sequence(t.Context(), u); err != nil || seq != 11 {
		t.Fatal("incompatible old success lost ordering", seq, err)
	}
	key, _ := unitKey(legacy)
	if _, err := os.Stat(filepath.Join(repo.root, "latest", key+".json")); err != nil {
		t.Fatal("incompatibility discarded legacy retention root", err)
	}
	oldTime := now.Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo.root, "blobs", old.ImageDigest+".png"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := repo.Collect(t.Context(), func(Unit) bool { return false }, map[string]bool{"device": true}, now); err != nil {
		t.Fatal(err)
	}
	repo, err = NewPVC(repo.root)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.Authorized(t.Context(), "device", old.ImageDigest); err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, err := repo.Read(t.Context(), old.ImageDigest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("old URL bytes were altered or collected", err)
	}
}

func TestStorageAllPalettesAndExclusiveBudgets(t *testing.T) {
	for _, pal := range []imageprocessing.Palette{imageprocessing.Gray4, imageprocessing.Gray16, imageprocessing.Spectra6} {
		u := modernUnit(pal, 1200, 825, 750000)
		data := encoded(t, u, true)
		if len(data) <= 90000 {
			t.Fatal("test must cross legacy storage bound")
		}
		for _, limit := range []int{len(data) - 1, len(data), len(data) + 1} {
			u.Output.PNGByteLimit = limit
			repo, err := NewPVC(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			base, err := repo.Publish(t.Context(), publication(u, 1), data)
			if limit <= len(data) {
				if err == nil {
					t.Fatal("published at exclusive bound")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			d := overlay.Drawing{Version: "future-encoder-test", Corner: overlay.TopRight, Scale: 1, WiFi: new(int)}
			derived, unpin, err := repo.StoreDerived(t.Context(), base, d, data)
			if err != nil {
				t.Fatal(err)
			}
			unpin()
			if err := repo.Issue(t.Context(), "device", derived.ImageDigest, time.Now()); err != nil {
				t.Fatal(err)
			}
			repo, err = NewPVC(repo.root)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := repo.Latest(t.Context(), u); err != nil || got != base {
				t.Fatal("large latest did not survive restart", err)
			}
			if got, unpin, err := repo.FindDerived(t.Context(), base, d); err != nil || got != derived {
				t.Fatal("large derived did not survive restart", err)
			} else {
				unpin()
			}
			if err := repo.Collect(t.Context(), func(candidate Unit) bool { return candidate == u }, map[string]bool{"device": true}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLegacyPendingCornerSurvivesNewStorage(t *testing.T) {
	repo, base, d, data := derivedFixture(t)
	derived, unpin, err := repo.StoreDerived(t.Context(), base, d, data)
	if err != nil {
		t.Fatal(err)
	}
	unpin()
	r, err := repo.Corner(t.Context(), "device")
	if err != nil {
		t.Fatal(err)
	}
	previous := &api.ScreenCycleStatus{Assignment: digest([]byte("assignment")), ScreenUID: base.Unit.ScreenUID, InputRevision: base.InputRevision, Target: api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}, ImageDigest: base.ImageDigest, Filename: base.Filename, ServedAt: metav1.NewMicroTime(time.Now().UTC().Truncate(time.Microsecond))}
	proposed := previous.DeepCopy()
	proposed.ImageDigest = derived.ImageDigest
	proposed.Filename = derived.Filename
	proposed.ServedAt.Time = proposed.ServedAt.Add(time.Second)
	if err := repo.PrepareCorner(t.Context(), r, CornerPending{Next: r.Current.Next(), Previous: previous, Proposed: proposed}); err != nil {
		t.Fatal(err)
	}
	u := modernUnit(imageprocessing.Spectra6, 1200, 825, 750000)
	if _, err := repo.Publish(t.Context(), publication(u, 1), encoded(t, u, false)); err != nil {
		t.Fatal(err)
	}
	repo, err = NewPVC(repo.root)
	if err != nil {
		t.Fatal(err)
	}
	r, err = repo.Corner(t.Context(), "device")
	if err != nil {
		t.Fatal(err)
	}
	if !EqualCycle(r.Pending.Previous, previous) || !EqualCycle(r.Pending.Proposed, proposed) {
		t.Fatal("legacy complete turns were defaulted or changed")
	}
	if err := repo.Collect(t.Context(), func(Unit) bool { return false }, map[string]bool{"device": true}, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(t.Context(), proposed.ImageDigest); err != nil {
		t.Fatal("pending legacy proposal lost GC root", err)
	}
	if _, err := repo.Read(t.Context(), previous.ImageDigest); err != nil {
		t.Fatal("pending previous turn lost GC root", err)
	}
	if err := repo.ResolveCorner(t.Context(), r, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.ResolveCorner(t.Context(), r, true); err != nil {
		t.Fatal("legacy resolution lost idempotency", err)
	}
}

func TestUnknownCompatibilityFailsClosed(t *testing.T) {
	u := modernUnit(imageprocessing.Gray16, 1200, 825, 750000)
	u.Output.Compatibility = "future-unknown"
	repo, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Publish(context.Background(), publication(u, 1), []byte("data")); err == nil {
		t.Fatal("unknown semantics were defaulted")
	}
}
