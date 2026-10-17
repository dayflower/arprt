package jobrender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dayflower/arprt/internal/render"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSchedulerCompatibilityRoundTripPreservesActiveAttempt(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, submitted := range []bool{false, true} {
			r, d, _, _ := fixture(t)
			step(t, r, d)
			st := load(t, r, d)
			st.Active.Submitted = submitted
			if current {
				st.Unit.Output = render.OutputConstraints{PNGByteLimit: 90000, Compatibility: render.OutputCompatibilityVersion}
			}
			if err := r.Store.save(st); err != nil {
				t.Fatal(err)
			}
			path := r.Store.path(st.Namespace, st.DeviceName, st.ScreenName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshotPath := filepath.Join(r.Store.run(st.Active.ID), "context.json")
			snapshot, err := os.ReadFile(snapshotPath)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := restarted.load(st.Namespace, st.DeviceName, st.ScreenName)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Unit != st.Unit || !reflect.DeepEqual(recovered.Active, st.Active) || recovered.Sequence != st.Sequence {
				t.Fatal("startup changed attempt compatibility or identity")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("startup rewrote scheduler record")
			}
			if err := restarted.prepare(recovered.Active, snapshot); err != nil {
				t.Fatal("idempotent prepare failed", err)
			}
			modified := append(bytes.Clone(snapshot), ' ')
			attemptCopy := *recovered.Active
			attemptCopy.ContextDigest = hash(modified)
			if err := restarted.prepare(&attemptCopy, modified); err == nil {
				t.Fatal("existing run context was overwritten")
			}
			after, _ = os.ReadFile(snapshotPath)
			if !bytes.Equal(snapshot, after) {
				t.Fatal("immutable context changed")
			}
			if current && recovered.Version != 2 {
				t.Fatal("current scheduler version absent")
			}
			if !current && recovered.Version != 0 {
				t.Fatal("legacy record acquired new semantics")
			}
			var raw map[string]any
			if err := json.Unmarshal(before, &raw); err != nil {
				t.Fatal(err)
			}
			raw["version"] = 99
			bad, _ := json.Marshal(raw)
			if err := os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(r.Store.Root); err == nil {
				t.Fatal("unknown scheduler version was defaulted")
			}
		}
	}
}

func TestObsoleteAttemptCannotReviveAfterInterruptedCancellation(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		ctx := context.Background()
		r, d, _, now := fixture(t)
		step(t, r, d)
		st := load(t, r, d)
		st.Active.Submitted = submitted
		if !submitted {
			if err := r.Client.Delete(ctx, r.Config.job(st)); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Store.save(st); err != nil {
			t.Fatal(err)
		}
		snapshotPath := filepath.Join(r.Store.run(st.Active.ID), "context.json")
		snapshot, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			t.Fatal(err)
		}
		d.Spec.Rotation = 90
		if err := r.Client.Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		c := r.Client
		injected := errors.New("interrupted cancellation")
		r.Client = deleteErrorClient{Client: c, err: injected}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); !errors.Is(err, injected) {
			t.Fatal("cancellation not reached", err)
		}
		interrupted := load(t, r, d)
		if interrupted.Active.ID != st.Active.ID || interrupted.Active.TerminalReason != "ObsoleteAttempt" {
			t.Fatal("obsolescence was not durable before cancellation")
		}
		// Reverting the target after a restart must not re-submit the old ID.
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			t.Fatal(err)
		}
		d.Spec.Rotation = 0
		if err := c.Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		r.Store, err = NewStore(r.Store.Root)
		if err != nil {
			t.Fatal(err)
		}
		r.Client = c
		step(t, r, d)
		recovered := load(t, r, d)
		if recovered.Active != nil || recovered.Reason != "ObsoleteAttempt" || recovered.Sequence != st.Sequence {
			t.Fatal("terminal attempt revived")
		}
		after, _ := os.ReadFile(snapshotPath)
		if !bytes.Equal(snapshot, after) {
			t.Fatal("obsolete attempt snapshot changed")
		}
		*now = recovered.Due
		step(t, r, d)
		next := load(t, r, d)
		if next.Active == nil || next.Active.ID == st.Active.ID || next.Active.Sequence != st.Sequence+1 {
			t.Fatal("new attempt did not preserve ordering")
		}
	}
}

func TestPrepareRejectsUnboundedOrSpecialSnapshots(t *testing.T) {
	for _, kind := range []string{"oversized", "fifo"} {
		r, d, _, _ := fixture(t)
		step(t, r, d)
		st := load(t, r, d)
		path := filepath.Join(r.Store.run(st.Active.ID), "context.json")
		snapshot, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if kind == "oversized" {
			if err := os.WriteFile(path, make([]byte, 65537), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Store.prepare(st.Active, snapshot); err == nil {
			t.Fatalf("accepted %s immutable snapshot", kind)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "oversized" && info.Size() != 65537 {
			t.Fatal("replaced invalid snapshot")
		}
		if kind == "fifo" && info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("replaced special snapshot")
		}
	}
}
