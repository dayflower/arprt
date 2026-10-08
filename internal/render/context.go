// Package render contains transport-independent renderer contracts.
package render

import (
	"context"
	"errors"
	"math"
)

type Target struct {
	Width   int32  `json:"width"`
	Height  int32  `json:"height"`
	Palette string `json:"palette"`
}

func (t Target) Validate() error {
	if t.Width <= 0 || t.Height <= 0 {
		return errors.New("render target dimensions must be positive")
	}
	switch t.Palette {
	case "bw", "gray4", "gray16", "bwry":
		return nil
	default:
		return errors.New("unknown semantic palette")
	}
}

type Context struct {
	Version      string `json:"version"`
	RenderTarget Target `json:"renderTarget"`
	Device       Device `json:"device"`
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
	Percent *int32   `json:"percent,omitempty"`
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
	Percent         *int32
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
func validPercent(p *int32) *int32 {
	if p == nil || *p < 0 || *p > 100 {
		return nil
	}
	v := *p
	return &v
}

// Request is normalized by the Kubernetes-facing adapter. GET has no Context.
type Request struct {
	Target  Target
	Context *Context
}

// Output contains validated PNG bytes; executors must validate before returning.
// HTTP execution and future asynchronous Job collection share this contract.
type Output struct{ PNG []byte }
type Executor interface {
	Execute(context.Context, Request) (Output, error)
}
