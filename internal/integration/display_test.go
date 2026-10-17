//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyDisplayAPI(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	newDevice := func(name string, display *api.DisplayOverride) *api.ArprtDevice {
		return &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: name}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: "display-auth"}, Display: display}}
	}
	for i, display := range []*api.DisplayOverride{
		{Width: ptr.To(int32(800))}, {Height: ptr.To(int32(480))},
		{Width: ptr.To(int32(0)), Height: ptr.To(int32(480))},
		{Width: ptr.To(int32(4097)), Height: ptr.To(int32(1))},
		{Width: ptr.To(int32(2049)), Height: ptr.To(int32(2049))},
		{Palette: "bw"}, {Palette: "bwry"}, {Palette: "unknown"},
		{PNGByteLimit: ptr.To(int32(0))}, {PNGByteLimit: ptr.To(int32(-1))},
	} {
		if err := c.Create(ctx, newDevice(fmt.Sprintf("bad-display-%d", i), display)); !apierrors.IsInvalid(err) {
			t.Fatalf("display override admitted: %+v %v", display, err)
		}
	}
	for mask := 0; mask < 8; mask++ {
		display := &api.DisplayOverride{}
		if mask&1 != 0 {
			display.Width, display.Height = ptr.To(int32(2048)), ptr.To(int32(2048))
		}
		if mask&2 != 0 {
			display.Palette = "spectra6"
		}
		if mask&4 != 0 {
			display.PNGByteLimit = ptr.To(int32(750000))
		}
		d := newDevice(fmt.Sprintf("good-display-%d", mask), display)
		if err := c.Create(ctx, d); err != nil {
			t.Fatal("valid independent override rejected", err)
		}
		if err := c.Delete(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	d := newDevice("display-reconciliation", &api.DisplayOverride{Width: ptr.To(int32(320)), Height: ptr.To(int32(240)), Palette: "gray16", PNGByteLimit: ptr.To(int32(90000))})
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Delete(ctx, d); err != nil {
			t.Error(err)
		}
	})
	// The real running reconciler resolves overrides even before observations.
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(d), d) == nil && d.Status.Display != nil && d.Status.Display.Width == 320 && d.Status.Display.Sources != nil && d.Status.Display.Sources.Dimensions == "explicit"
	})
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Status.Device = &api.DeviceInfoStatus{Model: "reterminal_e1002", FirmwareVersion: "unknown", ReportedDisplay: &api.ReportedDisplayStatus{Width: ptr.To(int32(600)), Height: ptr.To(int32(400))}}
		return c.Status().Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Spec.Display = nil
		return c.Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(d), d) == nil && d.Status.Display != nil && d.Status.Display.Width == 600 && d.Status.Display.Height == 400 && d.Status.Display.Palette == "spectra6" && d.Status.Display.PNGByteLimit == 750000 && d.Status.Display.Sources.Dimensions == "reported" && meta.IsStatusConditionTrue(d.Status.Conditions, controller.ConditionTargetKnown)
	})
	// Unsafe raw observations survive API validation but fall back to the profile.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Status.Device.ReportedDisplay.Width = ptr.To(int32(2147483647))
		return c.Status().Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(d), d) == nil && d.Status.Display != nil && d.Status.Display.Width == 800 && d.Status.Display.Sources.Dimensions == "profile" && *d.Status.Device.ReportedDisplay.Width == 2147483647
	})
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Status.Device.Model = "unreviewed"
		return c.Status().Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(d), d) == nil && d.Status.Display == nil && meta.IsStatusConditionFalse(d.Status.Conditions, controller.ConditionTargetKnown)
	})
}
