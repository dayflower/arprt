package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/dayflower/arprt/internal/overlay/homevideo"
	"hash/crc32"
	"image/png"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func pointer[T any](v T) *T { return &v }

var target = render.Target{Width: 800, Height: 480, Palette: "gray4"}

func patternedPNG(t *testing.T, indexed bool) []byte {
	t.Helper()
	pixels := make([]byte, 800*480)
	for y := 0; y < 480; y++ {
		for x := 0; x < 800; x++ {
			pixels[y*800+x] = byte((x/3 + y/7) % 4)
		}
	}
	b, err := encode(context.Background(), pixels, 800, 480)
	if err != nil {
		t.Fatal(err)
	}
	if indexed {
		b[25] = 3
		binary.BigEndian.PutUint32(b[29:33], crc32.ChecksumIEEE(b[12:29]))
		palette := []byte{0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255}
		chunk := make([]byte, 24)
		binary.BigEndian.PutUint32(chunk[:4], 12)
		copy(chunk[4:8], "PLTE")
		copy(chunk[8:20], palette)
		binary.BigEndian.PutUint32(chunk[20:], crc32.ChecksumIEEE(chunk[4:20]))
		b = append(append(append([]byte{}, b[:33]...), chunk...), b[33:]...)
	}
	if err := render.ValidatePNG(b, target); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestPixelsRotationsCornersItemsAndScaling(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		base := patternedPNG(t, indexed)
		original, err := png.Decode(bytes.NewReader(base))
		if err != nil {
			t.Fatal(err)
		}
		for _, rotation := range []int32{0, 90, 180, 270} {
			for _, corner := range []Corner{TopRight, BottomRight, BottomLeft, TopLeft} {
				for selection := 0; selection < 8; selection++ {
					for _, scale := range []int{1, 2} {
						t.Run(string(corner)+"/"+time.Duration(rotation).String()+"/"+string(rune('0'+selection))+"/"+string(rune('0'+scale))+"/"+map[bool]string{true: "indexed", false: "gray"}[indexed], func(t *testing.T) {
							// Cases share only immutable fixtures; retain the complete pixel matrix.
							t.Parallel()
							rt := target
							rt.Rotation = rotation
							d := Drawing{Version: AppearanceVersion, Corner: corner, Scale: scale, Margin: []Margins{{}, {Horizontal: 12, Vertical: 4}, {Horizontal: 4}, {Vertical: 8}}[(selection+scale)%4]}
							if selection&1 != 0 {
								d.Timestamp = "2026-10-09 12:34"
							}
							if selection&2 != 0 {
								if scale == 2 {
									d.Charging = true
								} else {
									d.Battery = pointer(1)
								}
							}
							if selection&4 != 0 {
								d.WiFi = pointer(3)
							}
							out, err := Compose(context.Background(), base, rt, d)
							// At scale 2 a quarter-turn canvas is 240 cells wide.
							// Timestamp plus both icons spans 258; with one icon
							// it spans 236, leaving at most four cells for inset.
							if scale == 2 && (rotation == 90 || rotation == 270) &&
								(selection == 7 || ((selection == 3 || selection == 5) && d.Margin.Horizontal > 4)) {
								if err == nil {
									t.Fatal("oversized rotated overlay accepted")
								}
								return
							}
							if err != nil {
								t.Fatal(err)
							}
							if selection == 0 {
								if out != nil {
									t.Fatal("empty overlay reencoded base")
								}
								return
							}
							if err := render.ValidatePNG(out, rt); err != nil {
								t.Fatal(err)
							}
							decoded, err := png.Decode(bytes.NewReader(out))
							if err != nil {
								t.Fatal(err)
							}
							// Expected bounds are derived from the public layout, independently
							// of the compositing/layout implementation.
							cw, ch, items := 0, 18, 0
							if selection&1 != 0 {
								cw += 214
								ch = 18
								items++
							}
							if selection&2 != 0 {
								cw += 18
								items++
							}
							if selection&4 != 0 {
								cw += 18
								items++
							}
							cw += (items - 1) * 4
							cw, ch = cw*scale, ch*scale
							mx, my := d.Margin.Horizontal*scale, d.Margin.Vertical*scale
							w, h := cw+mx, ch+my
							lw, lh := 800, 480
							if rotation == 90 || rotation == 270 {
								lw, lh = lh, lw
							}
							x, y, fx, fy := 0, 0, mx, my
							if strings.HasSuffix(string(corner), "right") {
								x = lw - w
								fx = x
							}
							if strings.HasPrefix(string(corner), "bottom") {
								y = lh - h
								fy = y
							}
							gx, gy, gw, gh, err := d.Bounds(rt)
							if err != nil || gx != x || gy != y || gw != w || gh != h {
								t.Fatal("wrong bleed bounds", gx, gy, gw, gh, err)
							}
							black, white := 0, 0
							for py := 0; py < 480; py++ {
								for px := 0; px < 800; px++ {
									// Independent inverse rotation expresses membership in logical bounds.
									lx, ly := px, py
									switch rotation {
									case 90:
										lx, ly = py, 799-px
									case 180:
										lx, ly = 799-px, 479-py
									case 270:
										lx, ly = 479-py, px
									}
									value, _, _, _ := decoded.At(px, py).RGBA()
									if lx >= x && lx < x+w && ly >= y && ly < y+h {
										if (lx < fx || lx >= fx+cw || ly < fy || ly >= fy+ch) && value != 65535 {
											t.Fatal("foreground entered the edge margin")
										}
										if value == 0 {
											black++
										} else if value == 65535 {
											white++
										} else {
											t.Fatal("overlay not black/white")
										}
									} else {
										want, _, _, _ := original.At(px, py).RGBA()
										if value != want {
											t.Fatalf("base pixel changed at %d,%d", px, py)
										}
									}
								}
							}
							if black == 0 || white == 0 {
								t.Fatal("bitmap/background missing")
							}
							// Every scale-sized block in logical overlay has identical samples.
							for ly := y; ly < y+h; ly += scale {
								for lx := x; lx < x+w; lx += scale {
									px, py := Physical(rt, lx, ly)
									v, _, _, _ := decoded.At(px, py).RGBA()
									for dy := 0; dy < scale; dy++ {
										for dx := 0; dx < scale; dx++ {
											px, py := Physical(rt, lx+dx, ly+dy)
											got, _, _, _ := decoded.At(px, py).RGBA()
											if v != got {
												t.Fatal("scale is not pixel replication")
											}
										}
									}
								}
							}
							again, err := Compose(context.Background(), base, rt, d)
							if err != nil || !bytes.Equal(again, out) {
								t.Fatal("output is nondeterministic")
							}
						})
					}
				}
			}
		}
	}
}
func TestThresholdsAndMissingInputs(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want int
	}{{0, 0}, {19.999, 0}, {20, 1}, {59.999, 1}, {60, 2}, {100, 2}} {
		got := BatteryLevel(Telemetry{Percent: &tt.v, Voltage: pointer(4.0)})
		if got == nil || *got != tt.want {
			t.Fatal(tt, got)
		}
	}
	for _, tt := range []struct {
		v    float64
		want int
	}{{0, 0}, {3.599, 0}, {3.6, 1}, {3.601, 1}, {3.799, 1}, {3.8, 2}, {3.801, 2}} {
		got := BatteryLevel(Telemetry{Model: "og", Voltage: &tt.v, Percent: pointer(math.NaN())})
		if got == nil || *got != tt.want {
			t.Fatal(tt, got)
		}
	}
	if BatteryLevel(Telemetry{Model: "other", Voltage: pointer(4.0)}) != nil || BatteryLevel(Telemetry{}) != nil || WiFiLevel(nil) != nil {
		t.Fatal("missing telemetry fabricated")
	}
	for _, tt := range []struct {
		r    int32
		want int
	}{{-100, 0}, {-89, 0}, {-88, 1}, {-78, 1}, {-77, 2}, {-67, 2}, {-66, 3}, {-56, 3}, {-55, 4}, {0, 4}} {
		if got := WiFiLevel(&tt.r); got == nil || *got != tt.want {
			t.Fatal(tt, got)
		}
	}
}
func TestTimeZonesLayoutAndLimits(t *testing.T) {
	at := time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC)
	for _, tt := range []struct{ name, want string }{{"UTC", "2026-03-08 06:59"}, {"Asia/Tokyo", "2026-03-08 15:59"}, {"America/New_York", "2026-03-08 01:59"}} {
		loc, err := time.LoadLocation(tt.name)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Normalize(target, Settings{Items: []string{"timestamp"}}, at, loc, Telemetry{}, TopRight)
		if err != nil || d.Timestamp != tt.want {
			t.Fatal(d, err)
		}
		if tt.name == "America/New_York" {
			d, _ = Normalize(target, Settings{Items: []string{"timestamp"}}, at.Add(time.Minute), loc, Telemetry{}, TopRight)
			if d.Timestamp != "2026-03-08 03:00" {
				t.Fatal("DST ignored")
			}
		}
	}
	for _, tt := range []struct {
		w, h  int32
		scale int
	}{{800, 480, 1}, {1600, 960, 2}, {2400, 1440, 3}, {480, 800, 1}} {
		if AutoScale(tt.w, tt.h) != tt.scale {
			t.Fatal(tt)
		}
	}
	d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", Battery: pointer(0), WiFi: pointer(0), Corner: TopRight, Scale: 1}
	_, _, w, h, err := d.Bounds(target)
	if err != nil || w != 258 || h != 18 {
		t.Fatal(w, h, err)
	}
	d.Battery = nil
	_, _, w, _, _ = d.Bounds(target)
	if w != 236 {
		t.Fatal("hidden gap retained", w)
	}
	d.Scale = math.MaxInt32
	if _, err := Compose(context.Background(), patternedPNG(t, false), target, d); err == nil {
		t.Fatal("oversized layout accepted")
	}
	d.Scale = 1
	if _, err := Compose(context.Background(), []byte("bad"), target, d); err == nil {
		t.Fatal("invalid base acceptable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compose(ctx, patternedPNG(t, false), target, d); err == nil {
		t.Fatal("cancellation ignored")
	}
	pixels := make([]byte, 800*480)
	r := rand.New(rand.NewSource(1))
	for i := range pixels {
		pixels[i] = byte(r.Intn(4))
	}
	if _, err := encode(context.Background(), pixels, 800, 480); err == nil {
		t.Fatal("oversized compressed output accepted")
	}
	for _, level := range []int{0, 1, 2} {
		d.Battery = &level
		if _, err := Compose(context.Background(), patternedPNG(t, false), target, d); err != nil {
			t.Fatal(err)
		}
	}
	for level := 0; level <= 4; level++ {
		d.WiFi = &level
		if _, err := Compose(context.Background(), patternedPNG(t, true), target, d); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChargingObservationAndVisibility(t *testing.T) {
	for _, charging := range []*bool{nil, pointer(false), pointer(true)} {
		want := charging != nil && *charging
		for _, voltage := range []*float64{nil, pointer(3.7)} {
			tele := Telemetry{Model: "og", Charging: charging, Voltage: voltage}
			d, err := Normalize(target, Settings{Items: []string{"battery"}}, time.Now(), time.UTC, tele, TopRight)
			if err != nil || d.Charging != want || (d.Battery != nil) != (voltage != nil && !want) || d.Empty() != (voltage == nil && !want) {
				t.Fatal("charging observation/level visibility", d, err)
			}
			hidden, err := Normalize(target, Settings{Items: []string{}}, time.Now(), time.UTC, tele, TopRight)
			if err != nil || !hidden.Empty() {
				t.Fatal("deselected battery drawn", hidden, err)
			}
		}
	}
	direct, _ := Normalize(target, Settings{Items: []string{"battery"}}, time.Now(), time.UTC, Telemetry{Charging: pointer(true)}, TopRight)
	measured, _ := Normalize(target, Settings{Items: []string{"battery"}}, time.Now(), time.UTC, Telemetry{Charging: pointer(true), Voltage: pointer(3.5)}, TopRight)
	if direct != measured {
		t.Fatal("hidden level changed charging mark identity", direct, measured)
	}
}

func TestHomeVideoPixelsAndIconAlignment(t *testing.T) {
	b, err := os.ReadFile("homevideo/testdata/homevideo-12x18.txt")
	if err != nil {
		t.Fatal(err)
	}
	glyphs := map[rune][]uint16{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cp, hex, _ := strings.Cut(line, ":")
		code, err := strconv.ParseInt(cp, 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		rows := []uint16{}
		for y := 0; y < 18; y++ {
			v, err := strconv.ParseUint(hex[y*3:y*3+3], 16, 16)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, uint16(v))
		}
		glyphs[rune(code)] = rows
		g, ok := homevideo.Lookup(rune(code))
		if !ok {
			t.Fatal("missing glyph", cp)
		}
		for y, row := range rows {
			for x := 0; x < 12; x++ {
				if g.Pixel(x, y) != (row&(1<<uint(11-x)) != 0) {
					t.Fatal("glyph differs from upstream", cp, x, y)
				}
			}
		}
	}
	if len(glyphs) != 13 {
		t.Fatal("incomplete upstream subset")
	}
	base := patternedPNG(t, false)
	for _, scale := range []int{1, 2} {
		d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", Charging: true, WiFi: pointer(4), Corner: TopRight, Scale: scale, Margin: Margins{Horizontal: 12, Vertical: 4}}
		out, err := Compose(context.Background(), base, target, d)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		// The arrow starts at 800-(258+12)*scale; text follows 22 cells later.
		ox, oy := 800-270*scale, 4*scale
		for cell, ch := range d.Timestamp {
			for y, row := range glyphs[ch] {
				for x := 0; x < 12; x++ {
					for sy := 0; sy < scale; sy++ {
						for sx := 0; sx < scale; sx++ {
							got, _, _, _ := img.At(ox+(22+cell*12+x)*scale+sx, oy+y*scale+sy).RGBA()
							want := uint32(65535)
							if row&(1<<uint(11-x)) != 0 {
								want = 0
							}
							if got != want {
								t.Fatal("wrong rendered font pixel", cell, x, y, scale)
							}
						}
					}
				}
			}
		}
		for i, rows := range [][18]uint32{update, charging, wifi[4]} {
			ix, iy := ox+[]int{0, 218, 240}[i]*scale, oy
			for y, row := range rows {
				for x := 0; x < 18; x++ {
					got, _, _, _ := img.At(ix+x*scale, iy+y*scale).RGBA()
					want := uint32(65535)
					if row&(1<<uint(17-x)) != 0 {
						want = 0
					}
					if got != want {
						t.Fatal("icon not centered", i, x, y, scale)
					}
				}
			}
		}
	}
}

func TestMarginLimitsAndEmptyLayout(t *testing.T) {
	d := Drawing{Version: AppearanceVersion, Charging: true, Corner: BottomLeft, Scale: 1, Margin: Margins{Horizontal: 782, Vertical: 462}}
	// Independent margins plus an 18-pixel icon exactly fill the 800x480 canvas.
	if _, _, w, h, err := d.Bounds(target); err != nil || w != 800 || h != 480 {
		t.Fatal("exact fit rejected", w, h, err)
	}
	for _, mutation := range []func(*Drawing){func(d *Drawing) { d.Margin.Horizontal++ }, func(d *Drawing) { d.Margin.Vertical++ }, func(d *Drawing) { d.Margin.Horizontal = -1 }, func(d *Drawing) { d.Margin.Vertical = -1 }, func(d *Drawing) { d.Margin.Horizontal = math.MaxInt32 }, func(d *Drawing) { d.Margin.Vertical = math.MaxInt32 }, func(d *Drawing) { d.Scale = math.MaxInt32 }, func(d *Drawing) { d.Battery = pointer(1) }} {
		bad := d
		mutation(&bad)
		if _, _, _, _, err := bad.Bounds(target); err == nil {
			t.Fatal("invalid/overflow drawing accepted", bad)
		}
	}
	d.Charging = false
	if x, y, w, h, err := d.Bounds(target); err != nil || x != 0 || y != 0 || w != 0 || h != 0 {
		t.Fatal("empty margin draws background", x, y, w, h, err)
	}
	for _, margin := range []Margins{{Horizontal: -1}, {Vertical: -1}} {
		if _, err := Normalize(target, Settings{Margin: margin}, time.Now(), time.UTC, Telemetry{}, TopRight); err == nil {
			t.Fatal("negative setting accepted", margin)
		}
	}
}

func TestAllMarginBounds(t *testing.T) {
	for _, rotation := range []int32{0, 90, 180, 270} {
		for _, corner := range []Corner{TopRight, BottomRight, BottomLeft, TopLeft} {
			for _, scale := range []int{1, 2} {
				for _, margin := range []Margins{{}, {Horizontal: 4}, {Vertical: 4}, {Horizontal: 12, Vertical: 4}, {Horizontal: 4, Vertical: 8}} {
					rt := target
					rt.Rotation = rotation
					d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", Battery: pointer(2), WiFi: pointer(4), Corner: corner, Scale: scale, Margin: margin}
					x, y, w, h, err := d.Bounds(rt)
					if scale == 2 && (rotation == 90 || rotation == 270) {
						if err == nil {
							t.Fatal("oversized rotated overlay accepted", d, rotation)
						}
						continue
					}
					if err != nil {
						t.Fatal("fitting margin rejected", d, rotation, err)
					}
					lw, lh := 800, 480
					if rotation == 90 || rotation == 270 {
						lw, lh = lh, lw
					}
					wx, wy := 0, 0
					if strings.HasSuffix(string(corner), "right") {
						wx = lw - (258+margin.Horizontal)*scale
					}
					if strings.HasPrefix(string(corner), "bottom") {
						wy = lh - (18+margin.Vertical)*scale
					}
					if x != wx || y != wy || w != (258+margin.Horizontal)*scale || h != (18+margin.Vertical)*scale {
						t.Fatal("wrong margin bounds", d, x, y, w, h)
					}
				}
			}
		}
	}
	d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", WiFi: pointer(4), Corner: TopRight, Scale: 2, Margin: Margins{Horizontal: 4}}
	rt := target
	rt.Rotation = 90
	if _, _, w, _, err := d.Bounds(rt); err != nil || w != 480 {
		t.Fatal("exact rotated fit rejected", w, err)
	}
	d.Margin.Horizontal++
	if _, _, _, _, err := d.Bounds(rt); err == nil {
		t.Fatal("one-pixel overflow margin accepted")
	}
}

// Cell size alone does not ensure icons visually match the font. Their visible
// ink must span the same rows as numeric text, including icons-only layouts.
func TestIconInkMatchesFontAndLevels(t *testing.T) {
	glyph, ok := homevideo.Lookup('0')
	if !ok {
		t.Fatal("missing numeric glyph")
	}
	fontTop, fontBottom := 18, -1
	for y := 0; y < 18; y++ {
		for x := 0; x < 12; x++ {
			if glyph.Pixel(x, y) {
				fontTop = min(fontTop, y)
				fontBottom = max(fontBottom, y)
			}
		}
	}
	icons := append(append([][18]uint32{}, batteries[:]...), wifi[:]...)
	icons = append(icons, charging, update)
	for i, rows := range icons {
		top, bottom := 18, -1
		for y, row := range rows {
			if row >= 1<<18 {
				t.Fatal("icon extends outside its cell", i)
			}
			if row != 0 {
				top = min(top, y)
				bottom = max(bottom, y)
			}
		}
		if top != fontTop || bottom != fontBottom || bottom-top+1 != 14 {
			t.Fatal("icon ink does not match numeric font height", i, top, bottom, fontTop, fontBottom)
		}
	}
	for _, levels := range [][][18]uint32{batteries[:], wifi[:]} {
		for i := 1; i < len(levels); i++ {
			changed := false
			for y, row := range levels[i-1] {
				if row&levels[i][y] != row {
					t.Fatal("stronger level erases weaker level", i, y)
				}
				changed = changed || row != levels[i][y]
			}
			if !changed {
				t.Fatal("levels are visually indistinguishable", i)
			}
		}
	}
	for i, rows := range batteries {
		if rows == charging {
			t.Fatal("charging indistinguishable from level", i)
		}
	}
}
