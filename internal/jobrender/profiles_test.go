package jobrender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/render"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProfileJobRecoveryAndNormalization(t *testing.T) {
	for _, model := range []string{"og", "reterminal_e1001", "reterminal_e1002", "reterminal_e1003", "reterminal_e1004", "unknown"} {
		t.Run(model, func(t *testing.T) {
			r, d, s, _ := fixture(t)
			d.Status.Device = &api.DeviceInfoStatus{Model: model, FirmwareVersion: "unrecognized"}
			// Scheduling recomputes observations even with obsolete effective status.
			if err := r.Client.Status().Update(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			if model == "unknown" {
				d.Spec.Display = &api.DisplayOverride{Width: ptr.To(int32(320)), Height: ptr.To(int32(240)), Palette: "spectra6", PNGByteLimit: ptr.To(int32(90000))}
				if err := r.Client.Update(t.Context(), d); err != nil {
					t.Fatal(err)
				}
			}
			s.Spec.ImageProcessing = &api.ImageProcessing{Dithering: "none"}
			if err := r.Client.Update(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			st := load(t, r, d)
			if st == nil || st.Unit.ResolvedTarget() != devicestate.Resolved(d) {
				t.Fatal("incorrect scheduled target", st)
			}
			b, err := os.ReadFile(filepath.Join(r.Store.run(st.Active.ID), "context.json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot render.Context
			if err := json.Unmarshal(b, &snapshot); err != nil || snapshot.RenderTarget != st.Unit.Target || snapshot.Device.Model != model || snapshot.Device.Firmware.Version != "unrecognized" {
				t.Fatal("incorrect snapshot", snapshot, err)
			}
			finish(t, r, d, png(t), false)
			r.Store, err = NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			m, err := r.Repository.Latest(t.Context(), st.Unit)
			if err != nil {
				t.Fatal("profile collection failed", err, load(t, r, d))
			}
			output, err := r.Repository.Read(t.Context(), m.ImageDigest)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Unit.ResolvedTarget().ValidateOutput(t.Context(), output); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBudgetChangesRetirePreparedAndRunningJobs(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "prepared"}[prepared], func(t *testing.T) {
			r, d, _, _ := fixture(t)
			step(t, r, d)
			old := load(t, r, d)
			if prepared {
				// Retain the exact durable snapshot, but model the crash before
				// submission. The recovery path must retire it before replacement.
				if _, err := r.cancel(t.Context(), old); err != nil {
					t.Fatal(err)
				}
				old.Active.Submitted = false
				if err := r.Store.save(old); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			d.Status.Device.Model = "reterminal_e1001" // Same context target; different output budget.
			if err := r.Client.Status().Update(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			deletePods(t, r)
			step(t, r, d)
			current := load(t, r, d)
			if current == nil || current.Active == nil || current.Active.ID == old.Active.ID || current.Unit.Output.PNGByteLimit != 750000 {
				t.Fatal("obsolete attempt survived budget change", current)
			}
		})
	}
}
