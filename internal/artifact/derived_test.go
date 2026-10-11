package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func derivedFixture(t *testing.T) (*PVC, Metadata, overlay.Drawing, []byte) {
	t.Helper()
	repo, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.Publish(context.Background(), Metadata{Unit: Unit{Namespace: "arprt", ScreenUID: "screen", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}, InputRevision: digest([]byte("revision")), AttemptID: "attempt", Sequence: 1, RenderedAt: time.Now().UTC()}, b)
	if err != nil {
		t.Fatal(err)
	}
	d, err := overlay.Normalize(base.Unit.Target, overlay.Settings{Items: []string{"timestamp"}}, base.RenderedAt, time.UTC, overlay.Telemetry{}, overlay.TopRight)
	if err != nil {
		t.Fatal(err)
	}
	png, err := overlay.Compose(context.Background(), b, base.Unit.Target, d)
	if err != nil {
		t.Fatal(err)
	}
	return repo, base, d, png
}
func TestDerivedSeparationCacheOwnershipAndRestart(t *testing.T) {
	ctx := context.Background()
	repo, base, d, png := derivedFixture(t)
	derived, release, err := repo.StoreDerived(ctx, base, d, png)
	if err != nil {
		t.Fatal(err)
	}
	release()
	latest, err := repo.Latest(ctx, base.Unit)
	if err != nil || !reflect.DeepEqual(latest, base) {
		t.Fatal("derived changed base success", latest, err)
	}
	if allowed, err := repo.Authorized(ctx, "receiving-uid", derived.ImageDigest); err != nil || allowed {
		t.Fatal("storage granted issuance")
	}
	if err := repo.Issue(ctx, "receiving-uid", derived.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if allowed, err := repo.Authorized(ctx, "other-uid", derived.ImageDigest); err != nil || allowed {
		t.Fatal("issuance leaked")
	}
	for _, mutation := range []func(*Metadata){func(m *Metadata) { m.RenderedAt = m.RenderedAt.Add(time.Second) }, func(m *Metadata) { m.Unit.DeviceUID = "another" }, func(m *Metadata) { m.Unit.Target.Rotation = 90 }, func(m *Metadata) { m.Unit.ScreenUID = "another" }} {
		other := base
		mutation(&other)
		if _, _, err := repo.FindDerived(ctx, other, d); !errors.Is(err, ErrNotFound) {
			t.Fatal("cache crossed provenance", err)
		}
		same, unpin, err := repo.StoreDerived(ctx, other, d, png)
		if err != nil {
			t.Fatal(err)
		}
		unpin()
		if same.Filename != derived.Filename {
			t.Fatal("identical bytes changed filename")
		}
	}
	restarted, err := NewPVC(repo.root)
	if err != nil {
		t.Fatal(err)
	}
	cached, unpin, err := restarted.FindDerived(ctx, base, d)
	if err != nil || cached.ImageDigest != derived.ImageDigest {
		t.Fatal("cache lost after restart", err)
	}
	unpin()
	if err := os.WriteFile(filepath.Join(repo.root, "blobs", derived.ImageDigest+".png"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.FindDerived(ctx, base, d); !errors.Is(err, ErrNotFound) {
		t.Fatal("corrupt cache reused", err)
	}
	repo.MaxBytes = 1
	if _, _, err := repo.StoreDerived(ctx, base, d, png); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity ignored", err)
	}
	latest, err = repo.Latest(ctx, base.Unit)
	if err != nil || latest.ImageDigest != base.ImageDigest {
		t.Fatal("failed derived persistence lost base")
	}
}
func TestDerivedPinAndIssuedRetention(t *testing.T) {
	ctx := context.Background()
	repo, base, d, png := derivedFixture(t)
	derived, release, err := repo.StoreDerived(ctx, base, d, png)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo.root, "blobs", derived.ImageDigest+".png"), old, old); err != nil {
		t.Fatal(err)
	}
	future := now.Add(48 * time.Hour)
	if err := repo.Collect(ctx, func(Unit) bool { return true }, nil, future); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(ctx, derived.ImageDigest); err != nil {
		t.Fatal("GC removed pinned candidate", err)
	}
	if err := repo.Issue(ctx, "device", derived.ImageDigest, future); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if err := repo.Collect(ctx, func(Unit) bool { return true }, nil, future.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(ctx, derived.ImageDigest); err != nil {
		t.Fatal("issued data removed early", err)
	}
	refs, err := os.ReadDir(filepath.Join(repo.root, "derived"))
	if err != nil || len(refs) != 0 {
		t.Fatal("cache history retained", err)
	}
	if _, err := repo.Latest(ctx, base.Unit); err != nil {
		t.Fatal("cache GC removed base", err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return true }, nil, future.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(ctx, derived.ImageDigest); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired derived bytes retained", err)
	}
}
func TestCornerPersistenceAndFailClosedGC(t *testing.T) {
	ctx := context.Background()
	repo, base, d, png := derivedFixture(t)
	image, release, err := repo.StoreDerived(ctx, base, d, png)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r, err := repo.Corner(ctx, "device")
	if err != nil || r.Current != overlay.TopRight {
		t.Fatal(r, err)
	}
	proposal := &api.ScreenCycleStatus{Assignment: digest([]byte("assignment")), ScreenUID: base.Unit.ScreenUID, InputRevision: base.InputRevision, Target: api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}, ImageDigest: image.ImageDigest, Filename: image.Filename, ServedAt: metav1.NewMicroTime(time.Now().UTC().Truncate(time.Microsecond))}
	pending := CornerPending{Next: r.Current.Next(), Proposed: proposal}
	if err := repo.PrepareCorner(ctx, r, pending); err != nil {
		t.Fatal(err)
	}
	if err := repo.PrepareCorner(ctx, r, pending); err != nil {
		t.Fatal("prepare retry not idempotent", err)
	}
	restarted, err := NewPVC(repo.root)
	if err != nil {
		t.Fatal(err)
	}
	r, err = restarted.Corner(ctx, "device")
	if err != nil || r.Pending == nil || !EqualCycle(r.Pending.Proposed, proposal) {
		t.Fatal("pending proposal lost", r, err)
	}
	if err := restarted.ResolveCorner(ctx, r, true); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ResolveCorner(ctx, r, true); err != nil {
		t.Fatal("commit retry not idempotent", err)
	}
	r, err = restarted.Corner(ctx, "device")
	if err != nil || r.Pending != nil || r.Current != overlay.BottomRight || r.Revision != 1 {
		t.Fatal(r, err)
	}
	path := filepath.Join(repo.root, "corners", digest([]byte("device"))+".json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Corner(ctx, "device"); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corruption reset corner", err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return false }, nil, time.Now().Add(72*time.Hour)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corrupt root allowed destructive GC", err)
	}
	if _, err := repo.Read(ctx, base.ImageDigest); err != nil {
		t.Fatal("GC deleted before root validation", err)
	}
}

func TestCorruptIssuanceCannotBeOverwritten(t *testing.T) {
	repo, base, _, _ := derivedFixture(t)
	ctx := context.Background()
	if err := repo.Issue(ctx, "device", base.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	key, err := issuanceKey("device", base.ImageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(repo.root, "issued", key+".json"), issuance{DeviceUID: "other", ImageDigest: base.ImageDigest, LastIssuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Issue(ctx, "device", base.ImageDigest, time.Now()); !errors.Is(err, ErrCorrupt) {
		t.Fatal("authoritative issuance corruption overwritten", err)
	}
}
func TestNewCornerSurvivesOlderDeviceSnapshot(t *testing.T) {
	repo, _, _, _ := derivedFixture(t)
	ctx := context.Background()
	before := time.Now().UTC()
	r, err := repo.Corner(ctx, "new-device")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ResolveCorner(ctx, r, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return true }, nil, before); err != nil {
		t.Fatal(err)
	}
	r, err = repo.Corner(ctx, "new-device")
	if err != nil || r.Revision != 1 {
		t.Fatal("old API snapshot deleted new record", r, err)
	}
	if err := repo.Collect(ctx, func(Unit) bool { return true }, nil, before.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, err = repo.Corner(ctx, "new-device")
	if err != nil || r.Revision != 0 {
		t.Fatal("deleted UID record did not expire", r, err)
	}
}

func TestDerivedMarginAndChargingIdentity(t *testing.T) {
	repo, base, d, _ := derivedFixture(t)
	b, err := repo.Read(context.Background(), base.ImageDigest)
	if err != nil {
		t.Fatal(err)
	}
	d.Timestamp = ""
	d.Battery = new(int)
	var previous Metadata
	for _, margin := range []overlay.Margins{{}, {Horizontal: 4}, {Vertical: 4}, {Horizontal: 8, Vertical: 4}} {
		d.Margin = margin
		png, err := overlay.Compose(context.Background(), b, base.Unit.Target, d)
		if err != nil {
			t.Fatal(err)
		}
		current, release, err := repo.StoreDerived(context.Background(), base, d, png)
		if err != nil {
			t.Fatal(err)
		}
		release()
		if current.ImageDigest == previous.ImageDigest {
			t.Fatal("margin reused another drawing")
		}
		cached, release, err := repo.FindDerived(context.Background(), base, d)
		if err != nil || cached != current {
			t.Fatal("margin cache miss", err)
		}
		release()
		previous = current
	}
	d.Battery = nil
	d.Charging = true
	png, err := overlay.Compose(context.Background(), b, base.Unit.Target, d)
	if err != nil {
		t.Fatal(err)
	}
	image, release, err := repo.StoreDerived(context.Background(), base, d, png)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if image.ImageDigest == previous.ImageDigest {
		t.Fatal("charging mark reused battery level")
	}
	if err := repo.Issue(context.Background(), "device", image.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Collect(context.Background(), func(Unit) bool { return true }, map[string]bool{"device": true}, time.Now().Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(context.Background(), image.ImageDigest); err != nil {
		t.Fatal("charging output collected early", err)
	}
}
