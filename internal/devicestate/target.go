package devicestate

import (
	"errors"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
)

type profile struct {
	width, height int32
	palette       string
	limit         int32
}

// Only literal aliases reviewed in notes/DESIGN.md are recognized. Model and
// firmware observations never become part of effective delivery identity.
var profiles = map[string]profile{
	"og":               {800, 480, "gray4", 90000},
	"reterminal_e1001": {800, 480, "gray4", 750000},
	"reTerminal E1001": {800, 480, "gray4", 750000},
	"reterminal_e1002": {800, 480, "spectra6", 750000},
	"reTerminal E1002": {800, 480, "spectra6", 750000},
	"reterminal_e1003": {1872, 1404, "gray16", 750000},
	"reTerminal E1003": {1872, 1404, "gray16", 750000},
	"reterminal_e1004": {1200, 1600, "spectra6", 750000},
	"reTerminal E1004": {1200, 1600, "spectra6", 750000},
}

func validDimensions(w, h int32) bool {
	return w > 0 && h > 0 && w <= 4096 && h <= 4096 && int64(w)*int64(h) <= 4194304
}

// Resolve recomputes from spec and raw observations on every gate. Effective
// status (including legacy status without provenance) is never an input.
func Resolve(d *api.ArprtDevice) (render.ResolvedTarget, *api.DisplayStatus, error) {
	var p profile
	if d.Status.Device != nil {
		p = profiles[d.Status.Device.Model]
	}
	v := api.DisplayStatus{Width: p.width, Height: p.height, Palette: p.palette, PNGByteLimit: p.limit,
		Sources: &api.DisplaySources{Dimensions: "profile", Palette: "profile", PNGByteLimit: "profile"}}
	if info := d.Status.Device; info != nil && info.ReportedDisplay != nil {
		r := info.ReportedDisplay
		if r.Width != nil && r.Height != nil && validDimensions(*r.Width, *r.Height) {
			v.Width, v.Height, v.Sources.Dimensions = *r.Width, *r.Height, "reported"
		}
	}
	if explicit := d.Spec.Display; explicit != nil {
		if explicit.Width != nil || explicit.Height != nil {
			if explicit.Width == nil || explicit.Height == nil || !validDimensions(*explicit.Width, *explicit.Height) {
				return render.ResolvedTarget{}, nil, errors.New("explicit display dimensions must be a valid bounded pair")
			}
			v.Width, v.Height, v.Sources.Dimensions = *explicit.Width, *explicit.Height, "explicit"
		}
		if explicit.Palette != "" {
			v.Palette, v.Sources.Palette = explicit.Palette, "explicit"
		}
		if explicit.PNGByteLimit != nil {
			v.PNGByteLimit, v.Sources.PNGByteLimit = *explicit.PNGByteLimit, "explicit"
		}
	}
	t := render.ResolvedTarget{Target: render.Target{Width: v.Width, Height: v.Height, Palette: v.Palette, Rotation: d.Spec.Rotation},
		Output: render.OutputConstraints{PNGByteLimit: int(v.PNGByteLimit), Compatibility: render.OutputCompatibilityVersion}}
	if err := t.Validate(); err != nil {
		return render.ResolvedTarget{}, nil, err
	}
	return t, &v, nil
}

// Resolved returns an invalid zero target when capabilities cannot be resolved.
func Resolved(d *api.ArprtDevice) render.ResolvedTarget {
	t, _, _ := Resolve(d)
	return t
}

// RefreshDisplay updates the effective API projection without changing observations.
func RefreshDisplay(d *api.ArprtDevice) error {
	_, status, err := Resolve(d)
	d.Status.Display = status
	return err
}
