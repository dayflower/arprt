package overlay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/render"
)

func resolvedTarget(p imageprocessing.Palette, w, h int) render.ResolvedTarget {
	return render.ResolvedTarget{Target: render.Target{Width: int32(w), Height: int32(h), Palette: string(p)}, Output: render.OutputConstraints{PNGByteLimit: 750000, Compatibility: render.OutputCompatibilityVersion}}
}

// Reversed indexed palettes exercise sample preservation independently of the
// canonical encoder's index ordering, including all six Spectra colors.
func paletteBase(t *testing.T, rt render.ResolvedTarget, reordered bool) []byte {
	t.Helper()
	it := rt.ImageTarget()
	colors := it.Palette.Colors()
	indices := make([]byte, it.Width*it.Height)
	for y := range it.Height {
		for x := range it.Width {
			indices[y*it.Width+x] = byte((x/17 + y/13) % len(colors))
		}
	}
	if !reordered {
		b, err := imageprocessing.Encode(t.Context(), indices, it)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// Repeated exact entries force Go's encoder to depth 4 without introducing
	// new colors. Normalize just as delivery does before overlay composition.
	palette := make(color.Palette, max(16, len(colors)))
	for i := range palette {
		palette[i] = colors[0]
	}
	for i, c := range colors {
		palette[len(palette)-1-i] = c
	}
	img := image.NewPaletted(image.Rect(0, 0, it.Width, it.Height), palette)
	for i, v := range indices {
		img.Pix[i] = byte(len(palette)-1) - v
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	normalized, err := imageprocessing.Normalize(t.Context(), b.Bytes(), it, imageprocessing.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func checkPaletteComposite(t *testing.T, rt render.ResolvedTarget, base []byte, d Drawing) {
	t.Helper()
	out, err := ComposeResolved(t.Context(), base, rt, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.ValidateOutput(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	before, err := png.Decode(bytes.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	x, y, w, h, err := d.Bounds(rt.Target)
	if err != nil {
		t.Fatal(err)
	}
	black, white := 0, 0
	for ly := 0; ly < int(rt.Target.Height); ly++ {
		for lx := 0; lx < int(rt.Target.Width); lx++ {
			// Independent inverse rotation for physical pixels.
			vx, vy := lx, ly
			switch rt.Target.Rotation {
			case 90:
				vx, vy = ly, int(rt.Target.Width)-1-lx
			case 180:
				vx, vy = int(rt.Target.Width)-1-lx, int(rt.Target.Height)-1-ly
			case 270:
				vx, vy = int(rt.Target.Height)-1-ly, lx
			}
			got := color.NRGBAModel.Convert(after.At(lx, ly)).(color.NRGBA)
			if vx >= x && vx < x+w && vy >= y && vy < y+h {
				switch got {
				case color.NRGBA{0, 0, 0, 255}:
					black++
				case color.NRGBA{255, 255, 255, 255}:
					white++
				default:
					t.Fatalf("non black/white overlay pixel: %v", got)
				}
			} else if want := color.NRGBAModel.Convert(before.At(lx, ly)).(color.NRGBA); got != want {
				t.Fatalf("base color changed at %d,%d: %v != %v", lx, ly, got, want)
			}
		}
	}
	if black == 0 || white == 0 {
		t.Fatal("missing ink/background")
	}
	wantDepth := byte(4)
	if rt.ImageTarget().Palette == imageprocessing.Gray4 && !imageprocessing.Gray4Native4 {
		wantDepth = 2
	}
	if out[24] != wantDepth {
		t.Fatal("wrong generated depth", out[24])
	}
	wantKind := byte(0)
	if rt.ImageTarget().Palette == imageprocessing.Spectra6 {
		wantKind = 3
	}
	if out[25] != wantKind {
		t.Fatal("wrong generated color type", out[25])
	}
	again, err := ComposeResolved(t.Context(), base, rt, d)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatal("nondeterministic composite", err)
	}
}

func TestPalettePixelsRotationsCorners(t *testing.T) {
	for _, p := range []imageprocessing.Palette{imageprocessing.Gray4, imageprocessing.Gray16, imageprocessing.Spectra6} {
		for _, reordered := range []bool{false, true} {
			rt := resolvedTarget(p, 481, 321)
			base := paletteBase(t, rt, reordered)
			for _, rotation := range []int32{0, 90, 180, 270} {
				for _, corner := range []Corner{TopRight, BottomRight, BottomLeft, TopLeft} {
					t.Run(fmt.Sprintf("%s/reordered=%t/%d/%s", p, reordered, rotation, corner), func(t *testing.T) {
						rt := rt
						rt.Target.Rotation = rotation
						d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", Charging: true, WiFi: pointer(4), Scale: 1, Corner: corner, Margin: Margins{Horizontal: 12, Vertical: 4}}
						checkPaletteComposite(t, rt, base, d)
					})
				}
			}
		}
	}
}

func TestPaletteProfileDimensions(t *testing.T) {
	for _, rt := range []render.ResolvedTarget{
		resolvedTarget(imageprocessing.Gray4, 800, 480),
		resolvedTarget(imageprocessing.Spectra6, 800, 480),
		resolvedTarget(imageprocessing.Gray16, 1872, 1404),
		resolvedTarget(imageprocessing.Spectra6, 1200, 1600),
		resolvedTarget(imageprocessing.Gray16, 4096, 1024),
	} {
		t.Run(fmt.Sprintf("%s/%dx%d", rt.Target.Palette, rt.Target.Width, rt.Target.Height), func(t *testing.T) {
			d, err := NormalizeResolved(rt, Settings{Items: []string{"timestamp", "battery", "wifi"}, Margin: Margins{Horizontal: 12, Vertical: 4}}, time.Date(2026, 10, 9, 12, 34, 0, 0, time.UTC), time.UTC, Telemetry{Percent: pointer(50.0), RSSI: pointer(int32(-60))}, BottomLeft)
			if err != nil || d.Version != AppearanceVersion || d.Scale != AutoScale(rt.Target.Width, rt.Target.Height) || d.Timestamp != "2026-10-09 12:34" {
				t.Fatal(d, err)
			}
			checkPaletteComposite(t, rt, paletteBase(t, rt, false), d)
		})
	}
}

func TestFinalGray4Depth(t *testing.T) {
	rt := resolvedTarget(imageprocessing.Gray4, 800, 480)
	d := Drawing{Version: AppearanceVersion, WiFi: pointer(4), Corner: TopLeft, Scale: 1}
	for _, tt := range []struct {
		name  string
		index int
		want  byte
	}{
		{"removed gray", 0, 2},
		{"below firmware counting region", 450*800 + 799, 4},
		{"last pixel", 800*480 - 1, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			indices := make([]byte, 800*480)
			indices[tt.index] = 1
			base, err := imageprocessing.Encode(t.Context(), indices, rt.ImageTarget())
			if err != nil {
				t.Fatal(err)
			}
			out, err := ComposeResolved(t.Context(), base, rt, d)
			wantDepth := tt.want
			if !imageprocessing.Gray4Native4 {
				wantDepth = 2
			}
			if err != nil || out[24] != wantDepth {
				t.Fatal("final depth", err)
			}
		})
	}
}

func TestCompositeExclusiveBudgetAndInvalidInputs(t *testing.T) {
	for _, p := range []imageprocessing.Palette{imageprocessing.Gray4, imageprocessing.Gray16, imageprocessing.Spectra6} {
		rt := resolvedTarget(p, 800, 480)
		base, err := imageprocessing.Encode(t.Context(), make([]byte, 800*480), rt.ImageTarget())
		if err != nil {
			t.Fatal(err)
		}
		d := Drawing{Version: AppearanceVersion, Timestamp: "2026-10-09 12:34", Corner: TopRight, Scale: 1}
		out, err := ComposeResolved(t.Context(), base, rt, d)
		if err != nil {
			t.Fatal(err)
		}
		if len(base) >= len(out)-1 {
			t.Fatal("budget fixture must isolate composite size")
		}
		for _, limit := range []int{len(out) - 1, len(out), len(out) + 1} {
			limited := rt
			limited.Output.PNGByteLimit = limit
			got, err := ComposeResolved(t.Context(), base, limited, d)
			if limit <= len(out) {
				if err == nil || got != nil {
					t.Fatal("oversized composite returned", p, limit)
				}
				if err := limited.ValidateOutput(t.Context(), base); err != nil {
					t.Fatal("base fallback lost", err)
				}
			} else if err != nil || !bytes.Equal(got, out) {
				t.Fatal("budget changed encoding", err)
			}
		}
		if got, err := ComposeResolved(t.Context(), base, rt, Drawing{}); err != nil || got != nil {
			t.Fatal("empty overlay reencoded base", err)
		}
		bad := d
		bad.Scale = 10000
		if _, err := ComposeResolved(t.Context(), base, rt, bad); err == nil {
			t.Fatal("oversized layout accepted")
		}
		bad = d
		bad.Version = LegacyAppearanceVersion
		if _, err := ComposeResolved(t.Context(), base, rt, bad); err == nil {
			t.Fatal("legacy drawing reused")
		}
		if _, err := ComposeResolved(t.Context(), []byte("invalid"), rt, Drawing{}); err == nil {
			t.Fatal("invalid empty base accepted")
		}
		badTarget := rt
		badTarget.Target.Width++
		if _, err := ComposeResolved(t.Context(), base, badTarget, d); err == nil {
			t.Fatal("wrong dimensions accepted")
		}
		badTarget = rt
		badTarget.Output.Compatibility = "unknown"
		if _, err := ComposeResolved(t.Context(), base, badTarget, d); err == nil {
			t.Fatal("unknown contract accepted")
		}
	}
}

// Counting synchronous checkpoints covers decode, index extraction, drawing and
// encoding without racing a wall-clock timer or leaving work in a goroutine.
type checkpointContext struct {
	context.Context
	remaining int
}

func (c *checkpointContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
func TestResolvedComposeCancellation(t *testing.T) {
	rt := resolvedTarget(imageprocessing.Spectra6, 800, 480)
	base := paletteBase(t, rt, true)
	d := Drawing{Version: AppearanceVersion, Charging: true, Corner: TopLeft, Scale: 1}
	for _, n := range []int{1, 10, 100, 500, 700, 1000, 1500} {
		ctx := &checkpointContext{Context: context.Background(), remaining: n}
		if out, err := ComposeResolved(ctx, base, rt, d); !errors.Is(err, context.Canceled) || out != nil {
			t.Fatalf("checkpoint %d: %v", n, err)
		}
	}
}
