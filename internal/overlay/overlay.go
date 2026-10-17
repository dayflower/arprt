// Package overlay draws bounded bitmap assets on validated palette PNGs.
// It has no HTTP or Kubernetes dependencies and never estimates a percentage.
package overlay

import (
	"context"
	"errors"
	"image"
	"math"
	"time"

	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/overlay/homevideo"
	"github.com/dayflower/arprt/internal/render"
)

const (
	LegacyAppearanceVersion = "bitmap-v1-gray2-v1"
	AppearanceVersion       = "bitmap-v1-" + imageprocessing.EncoderVersion + "-" + imageprocessing.PaletteVersion
)

type Corner string

const (
	TopRight    Corner = "top-right"
	BottomRight Corner = "bottom-right"
	BottomLeft  Corner = "bottom-left"
	TopLeft     Corner = "top-left"
)

func (c Corner) Valid() bool {
	return c == TopRight || c == BottomRight || c == BottomLeft || c == TopLeft
}
func (c Corner) Next() Corner {
	switch c {
	case TopRight:
		return BottomRight
	case BottomRight:
		return BottomLeft
	case BottomLeft:
		return TopLeft
	default:
		return TopRight
	}
}

// Margins are unscaled pixels in the logical viewing orientation.
type Margins struct {
	Horizontal int `json:"horizontal,omitempty"`
	Vertical   int `json:"vertical,omitempty"`
}

func (m Margins) valid() bool { return m.Horizontal >= 0 && m.Vertical >= 0 }

type Settings struct {
	Items  []string
	Scale  int32
	Margin Margins
}
type Telemetry struct {
	Model            string
	Percent, Voltage *float64
	RSSI             *int32
	Charging         *bool
}

// Drawing contains only visible inputs. Raw observations and hidden items never
// enter cache keys. Render timestamp provenance is recorded separately.
type Drawing struct {
	Version   string  `json:"version"`
	Timestamp string  `json:"timestamp,omitempty"`
	Battery   *int    `json:"battery,omitempty"`
	Charging  bool    `json:"charging,omitempty"`
	WiFi      *int    `json:"wifi,omitempty"`
	Corner    Corner  `json:"corner"`
	Scale     int     `json:"scale"`
	Margin    Margins `json:"margin"`
}

func valid(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 }
func BatteryLevel(t Telemetry) *int {
	var level int
	if valid(t.Percent) && *t.Percent <= 100 {
		switch {
		case *t.Percent < 20:
			level = 0
		case *t.Percent < 60:
			level = 1
		default:
			level = 2
		}
	} else if t.Model == "og" && valid(t.Voltage) {
		switch {
		case *t.Voltage < 3.6:
			level = 0
		case *t.Voltage < 3.8:
			level = 1
		default:
			level = 2
		}
	} else {
		return nil
	}
	return &level
}

// Charging requires a known current charging observation. USB presence and
// stored observations do not establish active charging.
func Charging(t Telemetry) bool {
	return t.Charging != nil && *t.Charging
}
func WiFiLevel(rssi *int32) *int {
	if rssi == nil {
		return nil
	}
	level := 0
	for _, threshold := range []int32{-88, -77, -66, -55} {
		if *rssi >= threshold {
			level++
		}
	}
	return &level
}
func AutoScale(width, height int32) int { return max(1, int(min(width, height)/480)) }

// Normalize retains the legacy drawing identity until runtime integration.
func Normalize(t render.Target, s Settings, renderedAt time.Time, location *time.Location, telemetry Telemetry, corner Corner) (Drawing, error) {
	if t.Validate() != nil || !corner.Valid() || location == nil || renderedAt.IsZero() || s.Scale < 0 || !s.Margin.valid() {
		return Drawing{}, errors.New("invalid overlay inputs")
	}
	d := Drawing{Version: LegacyAppearanceVersion, Corner: corner, Scale: AutoScale(t.Width, t.Height), Margin: s.Margin}
	if s.Scale != 0 {
		d.Scale = int(s.Scale)
	}
	for _, item := range s.Items {
		switch item {
		case "timestamp":
			d.Timestamp = renderedAt.In(location).Format("2006-01-02 15:04")
		case "battery":
			d.Charging = Charging(telemetry)
			if !d.Charging {
				d.Battery = BatteryLevel(telemetry)
			}
		case "wifi":
			d.WiFi = WiFiLevel(telemetry.RSSI)
		default:
			return Drawing{}, errors.New("invalid overlay item")
		}
	}
	return d, nil
}

// NormalizeResolved binds drawing identity to the selected encoding semantics.
// RenderedAt is the original base success time, including for stale delivery.
func NormalizeResolved(t render.ResolvedTarget, s Settings, renderedAt time.Time, location *time.Location, telemetry Telemetry, corner Corner) (Drawing, error) {
	if !t.Output.IsZero() {
		if err := t.Validate(); err != nil {
			return Drawing{}, err
		}
	}
	d, err := Normalize(t.Target, s, renderedAt, location, telemetry, corner)
	if !t.Output.IsZero() {
		d.Version = AppearanceVersion
	}
	return d, err
}

func (d Drawing) Empty() bool {
	return d.Timestamp == "" && d.Battery == nil && !d.Charging && d.WiFi == nil
}

// Bounds checks scale before multiplication, including on 32-bit platforms.
func (d Drawing) Bounds(t render.Target) (x, y, width, height int, err error) {
	_, background, err := d.layout(t)
	return background.Min.X, background.Min.Y, background.Dx(), background.Dy(), err
}

// Foreground is inset from both corner edges. The background includes that
// inset and bleeds to those edges, without padding on the content-facing sides.
func (d Drawing) layout(t render.Target) (foreground, background image.Rectangle, err error) {
	if t.Validate() != nil || !d.Corner.Valid() || d.Scale < 1 || !d.Margin.valid() || (d.Charging && d.Battery != nil) ||
		(d.Timestamp != "" && len(d.Timestamp) != 16) || (d.Battery != nil && (*d.Battery < 0 || *d.Battery > 2)) || (d.WiFi != nil && (*d.WiFi < 0 || *d.WiFi > 4)) {
		return foreground, background, errors.New("invalid drawing")
	}
	lw, lh := int(t.Width), int(t.Height)
	if t.Rotation == 90 || t.Rotation == 270 {
		lw, lh = lh, lw
	}
	width, height, items := 0, iconSize, 0
	if d.Timestamp != "" {
		for _, ch := range d.Timestamp {
			if _, ok := homevideo.Lookup(ch); !ok {
				return foreground, background, errors.New("unknown timestamp glyph")
			}
		}
		width += iconSize + 4 + len(d.Timestamp)*homevideo.CellWidth
		height = homevideo.CellHeight
		items++
	}
	if d.Battery != nil || d.Charging {
		width += iconSize
		items++
	}
	if d.WiFi != nil {
		width += iconSize
		items++
	}
	if items == 0 {
		return foreground, background, nil
	}
	width += (items - 1) * 4
	// Subtract before adding or scaling to avoid overflow, even with MaxInt
	// margin/scale on 32-bit platforms. Only fitting values are multiplied.
	if width > lw/d.Scale-d.Margin.Horizontal || height > lh/d.Scale-d.Margin.Vertical {
		return foreground, background, errors.New("overlay does not fit")
	}
	width, height = width*d.Scale, height*d.Scale
	mx, my := d.Margin.Horizontal*d.Scale, d.Margin.Vertical*d.Scale
	x, y := mx, my
	if d.Corner == TopRight || d.Corner == BottomRight {
		x = lw - mx - width
	}
	if d.Corner == BottomRight || d.Corner == BottomLeft {
		y = lh - my - height
	}
	foreground = image.Rect(x, y, x+width, y+height)
	background = foreground
	if d.Corner == TopRight || d.Corner == BottomRight {
		background.Max.X = lw
	} else {
		background.Min.X = 0
	}
	if d.Corner == BottomRight || d.Corner == BottomLeft {
		background.Max.Y = lh
	} else {
		background.Min.Y = 0
	}
	return foreground, background, nil
}

// Physical maps logical viewing coordinates onto already rotated renderer output.
func Physical(t render.Target, x, y int) (int, int) {
	switch t.Rotation {
	case 90:
		return int(t.Width) - 1 - y, x
	case 180:
		return int(t.Width) - 1 - x, int(t.Height) - 1 - y
	case 270:
		return y, int(t.Height) - 1 - x
	default:
		return x, y
	}
}

// Compose preserves legacy OG delivery until callers resolve output constraints.
func Compose(ctx context.Context, base []byte, t render.Target, d Drawing) ([]byte, error) {
	return ComposeResolved(ctx, base, render.ResolvedTarget{Target: t}, d)
}

// ComposeResolved draws exact black/white palette indices after normalization.
// It never resizes, quantizes, dithers or rotates the renderer's base content.
// The caller owns shared admission and the enclosing processing deadline.
func ComposeResolved(ctx context.Context, base []byte, resolved render.ResolvedTarget, d Drawing) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t := resolved.Target
	legacy := resolved.Output.IsZero()
	version := AppearanceVersion
	it := resolved.ImageTarget()
	if legacy {
		version = LegacyAppearanceVersion
		it.PNGByteLimit = render.MaxPNGBytes + 1
		if err := render.ValidatePNG(base, t); err != nil {
			return nil, err
		}
	} else if err := resolved.Validate(); err != nil {
		return nil, err
	}
	decoded, err := imageprocessing.Decode(ctx, base)
	if err != nil {
		return nil, err
	}
	if !legacy {
		ok, err := decoded.Compatible(ctx, it)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("incompatible overlay base")
		}
	}
	if d.Empty() {
		return nil, nil
	}
	if d.Version != version {
		return nil, errors.New("invalid drawing version")
	}
	foreground, background, err := d.layout(t)
	if err != nil {
		return nil, err
	}
	// Convert exact samples to canonical indices, preserving reordered indexed
	// palettes and every color outside the overlay. Compatibility has already
	// excluded alpha and off-palette samples; no nearest-color matching is used.
	colors := it.Palette.Colors()
	indices := make(map[uint64]byte, len(colors))
	var white byte
	key := func(r, g, b uint32) uint64 { return uint64(r)<<32 | uint64(g)<<16 | uint64(b) }
	for i, c := range colors {
		indices[key(uint32(c.R)*257, uint32(c.G)*257, uint32(c.B)*257)] = byte(i)
		if c.R == 255 && c.G == 255 && c.B == 255 {
			white = byte(i)
		}
	}
	pixels := make([]byte, it.Width*it.Height)
	for py := 0; py < it.Height; py++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for px := 0; px < it.Width; px++ {
			r, g, b, a := decoded.Pixels().At(px, py).RGBA()
			index, ok := indices[key(r, g, b)]
			if !ok || a != 65535 {
				return nil, errors.New("invalid overlay palette sample")
			}
			pixels[py*it.Width+px] = index
		}
	}
	put := func(lx, ly int, value byte) { px, py := Physical(t, lx, ly); pixels[py*int(t.Width)+px] = value }
	for ly := background.Min.Y; ly < background.Max.Y; ly++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for lx := background.Min.X; lx < background.Max.X; lx++ {
			put(lx, ly, white)
		}
	}
	cursor, count := foreground.Min.X, 0
	draw := func(width, height int, pixel func(int, int) bool) {
		if count > 0 {
			cursor += 4 * d.Scale
		}
		count++
		oy := foreground.Min.Y + (foreground.Dy()-height*d.Scale)/2
		for row := 0; row < height; row++ {
			if ctx.Err() != nil {
				return
			}
			for col := 0; col < width; col++ {
				if pixel(col, row) {
					for sy := 0; sy < d.Scale; sy++ {
						for sx := 0; sx < d.Scale; sx++ {
							put(cursor+col*d.Scale+sx, oy+row*d.Scale+sy, 0)
						}
					}
				}
			}
		}
		cursor += width * d.Scale
	}
	drawIcon := func(rows [iconSize]uint32) {
		draw(iconSize, iconSize, func(x, y int) bool { return rows[y]&(1<<uint(iconSize-1-x)) != 0 })
	}
	if d.Timestamp != "" {
		drawIcon(update)
		glyphs := make([]*homevideo.Glyph, 0, len(d.Timestamp))
		for _, ch := range d.Timestamp {
			glyph, _ := homevideo.Lookup(ch) // Validated by layout.
			glyphs = append(glyphs, glyph)
		}
		draw(len(glyphs)*homevideo.CellWidth, homevideo.CellHeight, func(x, y int) bool {
			return glyphs[x/homevideo.CellWidth].Pixel(x%homevideo.CellWidth, y)
		})
	}
	if d.Charging {
		drawIcon(charging)
	} else if d.Battery != nil {
		drawIcon(batteries[*d.Battery])
	}
	if d.WiFi != nil {
		drawIcon(wifi[*d.WiFi])
	}
	encoder := imageprocessing.Encode
	if legacy {
		encoder = imageprocessing.EncodeLegacyGray4
	}
	output, err := encoder(ctx, pixels, it)
	if err != nil {
		return nil, err
	}
	if err := resolved.ValidateOutput(ctx, output); err != nil {
		return nil, err
	}
	return output, nil
}
