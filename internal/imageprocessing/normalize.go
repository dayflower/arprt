package imageprocessing

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
)

// Normalize preserves compatible renderer bytes. Other inputs are composited,
// resized and quantized once. Exact palette samples survive format-only
// conversions without re-dithering. The caller owns admission and the deadline.
func Normalize(ctx context.Context, source []byte, t Target, policy Policy) ([]byte, error) {
	policy, err := policy.Normalize()
	if err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	d, err := Decode(ctx, source)
	if err != nil {
		return nil, err
	}
	ok, err := d.Compatible(ctx, t)
	if err != nil {
		return nil, err
	}
	if ok {
		return source, nil
	}
	indices, err := d.Quantize(ctx, t, policy)
	if err != nil {
		return nil, err
	}
	return Encode(ctx, indices, t)
}

// Quantize uses output indices and three error rows, with no full-size RGB or
// Lab working image. Padding is white and neither sends nor receives diffusion.
// Content rotation remains the renderer's responsibility.
func (d *Decoded) Quantize(ctx context.Context, t Target, policy Policy) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	policy, err := policy.Normalize()
	if err != nil {
		return nil, err
	}
	if d == nil || d.pixels == nil {
		return nil, errors.New("missing decoded image")
	}
	content := contentBounds(d.width, d.height, t.Width, t.Height, policy.Fit)
	colors := t.Palette.Colors()
	pl := make([]lab, len(colors))
	whiteIndex := byte(0)
	for i, c := range colors {
		pl[i] = toLab([3]uint16{uint16(c.R) * 257, uint16(c.G) * 257, uint16(c.B) * 257})
		if c.R == 255 && c.G == 255 && c.B == 255 {
			whiteIndex = byte(i)
		}
	}
	out := make([]byte, t.Width*t.Height)
	for i := range out {
		out[i] = whiteIndex
	}
	var tree *mixtureNode
	if policy.Dither == Bayer {
		tree = mixtureTree(pl)
	}
	w, h := content.Dx(), content.Dy()
	rows := [3][]lab{make([]lab, w), make([]lab, w), make([]lab, w)}
	weights, denominator := diffusion(policy.Dither)
	var lastRGB [3]uint16
	var lastLab lab
	haveLast := false
	for y := range h {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := range w {
			if x%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			rgb := sample(d.pixels, x, y, w, h)
			// Reuse exact repeated input samples before adding diffusion errors.
			// Uniform regions need no repeated cube roots; no precision is lost
			// and working errors still remain independent for every pixel.
			if !haveLast || rgb != lastRGB {
				lastRGB, lastLab, haveLast = rgb, toLab(rgb), true
			}
			v := lastLab
			for ch := range 3 {
				v[ch] += rows[0][x][ch]
			}
			idx := nearest(v, pl)
			if tree != nil {
				idx = tree.pixel(v, x+content.Min.X, y+content.Min.Y)
			}
			out[(y+content.Min.Y)*t.Width+x+content.Min.X] = idx
			for _, weight := range weights {
				dx, dy := x+weight[0], weight[1]
				if dx < 0 || dx >= w || y+dy >= h {
					continue
				}
				for ch := range 3 {
					rows[dy][dx][ch] += (v[ch] - pl[idx][ch]) * float64(weight[2]) / denominator
				}
			}
		}
		rows[0], rows[1], rows[2] = rows[1], rows[2], rows[0]
		clear(rows[2])
	}
	return out, ctx.Err()
}

func diffusion(d Dither) ([][3]int, float64) {
	switch d {
	case Atkinson:
		return [][3]int{{1, 0, 1}, {2, 0, 1}, {-1, 1, 1}, {0, 1, 1}, {1, 1, 1}, {0, 2, 1}}, 8
	case FloydSteinberg:
		return [][3]int{{1, 0, 7}, {-1, 1, 3}, {0, 1, 5}, {1, 1, 1}}, 16
	default:
		return nil, 1
	}
}

func contentBounds(sw, sh, tw, th int, fit Fit) image.Rectangle {
	w, h := tw, th
	if fit == Contain {
		if int64(tw)*int64(sh) <= int64(th)*int64(sw) {
			h = min(th, max(1, int((int64(sh)*int64(tw)*2+int64(sw))/(2*int64(sw)))))
		} else {
			w = min(tw, max(1, int((int64(sw)*int64(th)*2+int64(sh))/(2*int64(sh)))))
		}
	}
	x, y := (tw-w)/2, (th-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// white composites straight 16-bit sRGB before interpolation, with integer
// halves-up rounding. It never interpolates hidden transparent colors.
func white(img image.Image, x, y int) [3]uint16 {
	var c color.NRGBA64
	// PNG decoding returns these concrete image types. Avoid allocating an
	// interface-backed color four times per output pixel on the hot path.
	switch m := img.(type) {
	case *image.NRGBA64:
		c = m.NRGBA64At(x, y)
	case *image.NRGBA:
		v := m.NRGBAAt(x, y)
		c = color.NRGBA64{uint16(v.R) * 257, uint16(v.G) * 257, uint16(v.B) * 257, uint16(v.A) * 257}
	case *image.Gray:
		v := uint16(m.GrayAt(x, y).Y) * 257
		return [3]uint16{v, v, v}
	case *image.Gray16:
		v := m.Gray16At(x, y).Y
		return [3]uint16{v, v, v}
	case *image.RGBA:
		v := m.RGBAAt(x, y)
		if v.A == 255 {
			return [3]uint16{uint16(v.R) * 257, uint16(v.G) * 257, uint16(v.B) * 257}
		}
		c = color.NRGBA64Model.Convert(v).(color.NRGBA64)
	case *image.RGBA64:
		v := m.RGBA64At(x, y)
		if v.A == 65535 {
			return [3]uint16{v.R, v.G, v.B}
		}
		c = color.NRGBA64Model.Convert(v).(color.NRGBA64)
	default:
		c = color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
	}
	var out [3]uint16
	for i, v := range [3]uint16{c.R, c.G, c.B} {
		out[i] = uint16((uint64(v)*uint64(c.A) + 65535*uint64(65535-c.A) + 32767) / 65535)
	}
	return out
}

func coordinate(d, source, dest int) (int, int, float64) {
	s := math.Max(0, math.Min(float64(source-1), (float64(d)+0.5)*float64(source)/float64(dest)-0.5))
	i := int(s)
	return i, min(i+1, source-1), s - float64(i)
}

func sample(img image.Image, x, y, w, h int) [3]uint16 {
	b := img.Bounds()
	x0, x1, fx := coordinate(x, b.Dx(), w)
	y0, y1, fy := coordinate(y, b.Dy(), h)
	a, c := white(img, x0+b.Min.X, y0+b.Min.Y), white(img, x0+b.Min.X, y1+b.Min.Y)
	bv, d := white(img, x1+b.Min.X, y0+b.Min.Y), white(img, x1+b.Min.X, y1+b.Min.Y)
	var rgb [3]uint16
	for ch := range 3 {
		top := float64(a[ch])*(1-fx) + float64(bv[ch])*fx
		bottom := float64(c[ch])*(1-fx) + float64(d[ch])*fx
		rgb[ch] = uint16(math.Floor(top*(1-fy) + bottom*fy + 0.5))
	}
	return rgb
}
