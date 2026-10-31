package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func deliveryFixture(t *testing.T) (*PVC, DeliveryRecord) {
	t.Helper()
	s, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	target := render.CurrentTarget(render.Target{Width: 800, Height: 480, Palette: "gray4"})
	generated := time.Now().UTC().Add(-time.Hour)
	m, err := s.Publish(t.Context(), Metadata{Unit: Unit{Namespace: "arprt", ScreenUID: "screen-1", Target: target.Target},
		ScreenGeneration: 1, InputRevision: strings.Repeat("a", 64), AttemptID: "one", Sequence: 1, RenderedAt: generated}, b)
	if err != nil {
		t.Fatal(err)
	}
	r := DeliveryRecord{Version: 1, DeviceUID: "device-1", DeviceGeneration: 1, ScreenName: "screen", ScreenUID: "screen-1", ScreenGeneration: 1,
		Assignment: strings.Repeat("b", 64), InputRevision: m.InputRevision, Target: target, ImageDigest: m.ImageDigest,
		RenderedAt: &generated, IssuedAt: time.Now().UTC(), Result: "ready", Rendering: "ready"}
	return s, r
}

func TestDeliveryRestartOrderingAndReadOnlyPreview(t *testing.T) {
	s, r := deliveryFixture(t)
	ctx := t.Context()
	used, _, err := s.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDelivery(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Atomic replacement cannot mix fields or let a delayed writer win. Reads
	// run concurrently with writes and must always see a complete valid record.
	var wg sync.WaitGroup
	for i := 1; i <= 16; i++ {
		wg.Go(func() {
			next := r
			next.IssuedAt = r.IssuedAt.Add(time.Duration(i) * time.Second)
			next.Result, next.Rendering = "stale", "failed"
			if err := s.RecordDelivery(ctx, next); err != nil && !errors.Is(err, ErrObsolete) {
				t.Error(err)
			}
			got, err := s.Delivery(ctx, r.DeviceUID)
			if err != nil || !validDelivery(got, r.DeviceUID) || !got.RenderedAt.Equal(*r.RenderedAt) {
				t.Error("torn record or changed generation time", got, err)
			}
		})
	}
	wg.Wait()
	restarted, err := NewPVC(s.root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Delivery(ctx, r.DeviceUID)
	if err != nil || !got.IssuedAt.Equal(r.IssuedAt.Add(16*time.Second)) || got.Result != "stale" {
		t.Fatal("wrong record after restart", got, err)
	}
	if err := restarted.RecordDelivery(ctx, r); !errors.Is(err, ErrObsolete) {
		t.Fatal("older writer accepted", err)
	}
	for _, uid := range []string{"device-recreated", ""} {
		if _, err := restarted.Delivery(ctx, uid); err == nil {
			t.Fatal("record crossed UID boundary")
		}
	}
	if ok, err := restarted.DeliveryPreviewAvailable(ctx, r.DeviceUID, r.ImageDigest); err != nil || !ok {
		t.Fatal("preview unavailable", ok, err)
	}
	if _, err := restarted.ReadDeliveryPreview(ctx, r.DeviceUID, r.ImageDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadDeliveryPreview(ctx, r.DeviceUID, strings.Repeat("c", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatal("arbitrary digest accepted", err)
	}
	if ok, err := restarted.Authorized(ctx, r.DeviceUID, r.ImageDigest); err != nil || ok {
		t.Fatal("observation or preview issued access", err)
	}
	after, _, err := restarted.Usage(ctx)
	if err != nil || after <= used {
		t.Fatal("delivery bytes missing from capacity accounting", err)
	}
}

func TestDeliveryCapacityCorruptionAndCollection(t *testing.T) {
	s, r := deliveryFixture(t)
	ctx := t.Context()
	if err := s.RecordDelivery(ctx, r); err != nil {
		t.Fatal(err)
	}
	s.MaxBytes = 1
	newer := r
	newer.IssuedAt = r.IssuedAt.Add(time.Minute)
	if err := s.RecordDelivery(ctx, newer); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity failure missing", err)
	}
	got, err := s.Delivery(ctx, r.DeviceUID)
	if err != nil || !got.IssuedAt.Equal(r.IssuedAt) {
		t.Fatal("failed write damaged previous observation", err)
	}
	// A live device keeps its observation after the image is collected. Preview
	// reads must neither extend existing issuance nor protect blobs from GC.
	if err := s.Issue(ctx, r.DeviceUID, r.ImageDigest, r.IssuedAt); !errors.Is(err, ErrCapacity) {
		t.Fatal("new issuance unexpectedly admitted", err)
	}
	s.MaxBytes = 768 << 20
	if err := s.Issue(ctx, r.DeviceUID, r.ImageDigest, r.IssuedAt); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(s.root, "blobs", r.ImageDigest+".png"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDeliveryPreview(ctx, r.DeviceUID, r.ImageDigest); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(ctx, func(Unit) bool { return false }, map[string]bool{r.DeviceUID: true}, r.IssuedAt.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delivery(ctx, r.DeviceUID); err != nil {
		t.Fatal("live observation lost with preview", err)
	}
	if available, err := s.DeliveryPreviewAvailable(ctx, r.DeviceUID, r.ImageDigest); err != nil || available {
		t.Fatal("observation extended image retention", err)
	}
	path := filepath.Join(s.root, "deliveries", digest([]byte(r.DeviceUID))+".json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delivery(ctx, r.DeviceUID); !errors.Is(err, ErrCorrupt) {
		t.Fatal("unknown version accepted", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("read repaired corrupt data")
	}
	// Unlike authoritative retention roots, invalid observation data cannot
	// block image GC; it is removed after the same deleted-record grace period.
	if err := s.Collect(ctx, func(Unit) bool { return false }, nil, r.IssuedAt.Add(26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("obsolete observation not collected", err)
	}
}
