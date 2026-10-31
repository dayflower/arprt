package artifact

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCyclePositionPersistenceAndValidation(t *testing.T) {
	s, r := deliveryFixture(t)
	r.Cycle = &api.ScreenCycleStatus{EntryIndex: 2, Assignment: r.Assignment, ScreenUID: r.ScreenUID,
		InputRevision: r.InputRevision, Target: api.CycleTargetStatus{Width: 800, Height: 480, Palette: "gray4"},
		ImageDigest: r.ImageDigest, Filename: strings.Repeat("A", 26) + ".png", ServedAt: metav1.NewMicroTime(r.IssuedAt.Truncate(time.Microsecond))}
	previous := r.Cycle.DeepCopy()
	previous.EntryIndex = 0
	if EqualCycle(previous, r.Cycle) {
		t.Fatal("different positions compared equal")
	}
	if err := s.RecordDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	corner, err := s.Corner(t.Context(), r.DeviceUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareCorner(t.Context(), corner, CornerPending{Next: corner.Current.Next(), Previous: previous, Proposed: r.Cycle}); err != nil {
		t.Fatal("position-only turn change rejected", err)
	}
	restarted, err := NewPVC(s.root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Delivery(t.Context(), r.DeviceUID)
	if err != nil || !EqualCycle(got.Cycle, r.Cycle) {
		t.Fatal("delivery lost position after restart", got, err)
	}
	pending, err := restarted.Corner(t.Context(), r.DeviceUID)
	if err != nil || pending.Pending == nil || !EqualCycle(pending.Pending.Previous, previous) || !EqualCycle(pending.Pending.Proposed, r.Cycle) {
		t.Fatal("pending turns lost positions after restart", pending, err)
	}
	for _, index := range []int32{-1, 64} {
		invalid := r
		invalid.Cycle = r.Cycle.DeepCopy()
		invalid.Cycle.EntryIndex = index
		if err := restarted.RecordDelivery(t.Context(), invalid); err == nil {
			t.Fatal("invalid position accepted", index)
		}
	}
	// Every persisted turn requires the new position field; omission and null
	// must never be silently decoded as position zero, including during GC.
	for _, field := range []string{"cycle", "previous", "proposed"} {
		for _, value := range []string{"missing", "null", "-1", "64"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				var record any = r
				dir := "deliveries"
				if field != "cycle" {
					record, dir = pending, "corners"
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				var raw map[string]any
				if err := json.Unmarshal(encoded, &raw); err != nil {
					t.Fatal(err)
				}
				container := raw
				if dir == "corners" {
					container = raw["pending"].(map[string]any)
				}
				turn := container[field].(map[string]any)
				switch value {
				case "missing":
					delete(turn, "entryIndex")
				case "null":
					turn["entryIndex"] = nil
				case "-1":
					turn["entryIndex"] = -1
				case "64":
					turn["entryIndex"] = 64
				}
				encoded, err = json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.root, dir, digest([]byte(r.DeviceUID))+".json")
				if err := os.WriteFile(path, encoded, 0600); err != nil {
					t.Fatal(err)
				}
				if dir == "deliveries" {
					_, err = restarted.Delivery(t.Context(), r.DeviceUID)
				} else {
					_, err = restarted.Corner(t.Context(), r.DeviceUID)
				}
				if !errors.Is(err, ErrCorrupt) {
					t.Fatal("invalid persisted position accepted", err)
				}
				if dir == "corners" {
					if err := restarted.Collect(t.Context(), func(Unit) bool { return false }, nil, r.IssuedAt.Add(48*time.Hour)); !errors.Is(err, ErrCorrupt) {
						t.Fatal("GC ignored corrupt authoritative turn", err)
					}
				}
			})
		}
	}
}
