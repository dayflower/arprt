// Package render contains transport-independent renderer contracts.
package render

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/dayflower/arprt/internal/imageprocessing"
)

// Insets are unscaled logical viewing pixels, independent of bitmap scale.
type Insets struct {
	Top    int32 `json:"top,omitempty"`
	Bottom int32 `json:"bottom,omitempty"`
	Left   int32 `json:"left,omitempty"`
	Right  int32 `json:"right,omitempty"`
}

type Target struct {
	SafeAreaInsets Insets `json:"safeAreaInsets,omitzero"`
	Width          int32  `json:"width"`
	Height         int32  `json:"height"`
	Palette        string `json:"palette"`
	// Rotation is clockwise content rotation; zero is omitted for compatibility
	// with existing v1 contexts and persisted artifact keys.
	Rotation int32 `json:"rotation,omitempty"`
}

func (t Target) Validate() error {
	if t.Width <= 0 || t.Height <= 0 {
		return errors.New("render target dimensions must be positive")
	}
	switch t.Rotation {
	case 0, 90, 180, 270:
	default:
		return errors.New("render target rotation must be 0, 90, 180 or 270")
	}
	w, h := t.LogicalSize()
	i := t.SafeAreaInsets
	if i.Top < 0 || i.Bottom < 0 || i.Left < 0 || i.Right < 0 ||
		i.Top > 4095 || i.Bottom > 4095 || i.Left > 4095 || i.Right > 4095 ||
		int64(i.Left)+int64(i.Right) >= int64(w) || int64(i.Top)+int64(i.Bottom) >= int64(h) {
		return errors.New("safe area must have nonnegative insets and positive dimensions")
	}
	switch t.Palette {
	case "bw", "gray4", "gray16", "bwry", "spectra6":
		return nil
	default:
		return errors.New("unknown semantic palette")
	}
}

// LogicalSize returns the upright canvas dimensions before clockwise rotation.
func (t Target) LogicalSize() (int32, int32) {
	if t.Rotation == 90 || t.Rotation == 270 {
		return t.Height, t.Width
	}
	return t.Width, t.Height
}

type Context struct {
	RendererState json.RawMessage `json:"rendererState,omitempty"`
	Version       string          `json:"version"`
	RenderTarget  Target          `json:"renderTarget"`
	Device        Device          `json:"device"`
}

type Device struct {
	Name     string    `json:"name"`
	Model    string    `json:"model,omitempty"`
	Firmware *Firmware `json:"firmware,omitempty"`
	Battery  *Battery  `json:"battery,omitempty"`
	Network  *Network  `json:"network,omitempty"`
}

type Firmware struct {
	Version string `json:"version"`
}
type Battery struct {
	Voltage *float64 `json:"voltage,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
}
type Network struct {
	RSSI *int32 `json:"rssi,omitempty"`
}

// Observation is an allowlist, not an HTTP header bag or Kubernetes object.
// A caller authenticates and resolves capabilities before building context.
type Observation struct {
	Model           string
	FirmwareVersion string
	Voltage         *float64
	Percent         *float64
	RSSI            *int32
}

// Build overlays current valid observations on known state without an informer
// round trip. Pointer fields distinguish an absent measurement from actual zero.
// Returned values are copied so a scheduled snapshot cannot change later.
func Build(name string, target Target, known, current Observation) (Context, error) {
	if name == "" {
		return Context{}, errors.New("device name is required")
	}
	if err := target.Validate(); err != nil {
		return Context{}, err
	}
	d := Device{Name: name, Model: known.Model}
	if current.Model != "" {
		d.Model = current.Model
	}
	fw := known.FirmwareVersion
	if current.FirmwareVersion != "" {
		fw = current.FirmwareVersion
	}
	if fw != "" {
		d.Firmware = &Firmware{Version: fw}
	}
	voltage := validVoltage(known.Voltage)
	if v := validVoltage(current.Voltage); v != nil {
		voltage = v
	}
	percent := validPercent(known.Percent)
	if p := validPercent(current.Percent); p != nil {
		percent = p
	}
	if voltage != nil || percent != nil {
		d.Battery = &Battery{Voltage: voltage, Percent: percent}
	}
	rssi := known.RSSI
	if current.RSSI != nil {
		rssi = current.RSSI
	}
	if rssi != nil {
		v := *rssi
		d.Network = &Network{RSSI: &v}
	}
	return Context{Version: "v1", RenderTarget: target, Device: d}, nil
}

func validVoltage(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 {
		return nil
	}
	v := *p
	return &v
}
func validPercent(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100 {
		return nil
	}
	v := *p
	return &v
}

// Request is normalized by the Kubernetes-facing adapter. GET has no Context.
type Request struct {
	Output  OutputConstraints
	Policy  imageprocessing.Policy
	Target  Target
	Context *Context
}

// ResolvedTarget includes internal constraints without extending context v1.
func (r Request) ResolvedTarget() ResolvedTarget {
	return ResolvedTarget{Target: r.Target, Output: r.Output}
}

// Output contains validated PNG bytes; executors must validate before returning.
// HTTP execution and asynchronous Job collection share ProcessPNG.
type Output struct {
	PNG        []byte
	RenderedAt time.Time
}
type Executor interface {
	Execute(context.Context, Request) (Output, error)
}
