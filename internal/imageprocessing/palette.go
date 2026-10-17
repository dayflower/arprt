// Package imageprocessing provides bounded PNG input, device compatibility and
// deterministic encoding independently of renderer transports and persistence.
// Runtime integration and normalization policies are owned by later phases.
package imageprocessing

import (
	"errors"
	"image/color"
)

type Palette string

const (
	Gray4    Palette = "gray4"
	Gray16   Palette = "gray16"
	Spectra6 Palette = "spectra6"

	MaxInputBytes   = 32 << 20
	MaxInputAxis    = 16384
	MaxInputPixels  = 16777216
	MaxOutputAxis   = 4096
	MaxOutputPixels = 4194304
)

// Colors returns a private copy in canonical encoding and quantization order.
// Spectra values are the EPF reference/transmission colors, not pure RGB primaries:
// https://github.com/jwchen119/EPF/blob/65a5f83f21be15ef4525f7ae3069a58b7187e4d2/epf/imaging.py#L21-L30
func (p Palette) Colors() []color.NRGBA {
	switch p {
	case Gray4:
		return []color.NRGBA{{0, 0, 0, 255}, {85, 85, 85, 255}, {170, 170, 170, 255}, {255, 255, 255, 255}}
	case Gray16:
		colors := make([]color.NRGBA, 16)
		for i := range colors {
			v := byte(i * 17)
			colors[i] = color.NRGBA{v, v, v, 255}
		}
		return colors
	case Spectra6:
		return []color.NRGBA{{0, 0, 0, 255}, {255, 255, 255, 255}, {255, 243, 56, 255}, {191, 0, 0, 255}, {100, 64, 255, 255}, {67, 138, 28, 255}}
	default:
		return nil
	}
}

// Target contains effective output constraints, with an exclusive byte limit.
// It deliberately carries no model, firmware, provenance or render rotation.
type Target struct {
	Width, Height int
	Palette       Palette
	PNGByteLimit  int
}

func (t Target) Validate() error {
	if !dimensionsValid(t.Width, t.Height, MaxOutputAxis, MaxOutputPixels) || len(t.Palette.Colors()) == 0 || t.PNGByteLimit <= 0 {
		return errors.New("invalid image output target")
	}
	return nil
}

func dimensionsValid(w, h, axis, pixels int) bool {
	return w > 0 && h > 0 && w <= axis && h <= axis && int64(w)*int64(h) <= int64(pixels)
}
