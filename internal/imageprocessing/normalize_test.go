package imageprocessing

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

func TestLabReferences(t *testing.T) {
	for _, tc := range []struct {
		rgb  [3]uint16
		want lab
	}{
		{[3]uint16{0, 0, 0}, lab{0, 0, 0}},
		{[3]uint16{65535, 65535, 65535}, lab{100, 0, 0}},
		{[3]uint16{65535, 0, 0}, lab{53.2408, 80.0925, 67.2032}},
		{[3]uint16{0, 65535, 0}, lab{87.7347, -86.1827, 83.1793}},
		{[3]uint16{0, 0, 65535}, lab{32.2970, 79.1875, -107.8602}},
	} {
		got := toLab(tc.rgb)
		for ch := range 3 {
			if math.Abs(got[ch]-tc.want[ch]) > 0.0001 {
				t.Fatalf("%v: %v", tc.rgb, got)
			}
		}
	}
	for _, v := range []uint16{1, 2650, 2651, 32768, 65534} {
		got := toLab([3]uint16{v, v, v})
		if got[1] != 0 || got[2] != 0 {
			t.Fatalf("neutral acquired chroma: %v", got)
		}
	}
	if nearest(lab{50, 1000, -1000}, []lab{{0, 0, 0}, {100, 0, 0}}) != 0 {
		t.Fatal("tie did not choose canonical index")
	}
	// Out-of-range working errors must not be clamped before comparison.
	if nearest(lab{120, 200, -200}, []lab{{100, 0, 0}, {80, 100, -100}}) != 1 {
		t.Fatal("Lab values were clamped")
	}
}

func TestCompositionAndBilinearReferences(t *testing.T) {
	img := image.NewNRGBA64(image.Rect(3, 5, 5, 6))
	img.SetNRGBA64(3, 5, color.NRGBA64{65535, 0, 0, 32896})
	img.SetNRGBA64(4, 5, color.NRGBA64{0, 65535, 12345, 0})
	if got := white(img, 3, 5); got != [3]uint16{65535, 32639, 32639} {
		t.Fatalf("composition: %v", got)
	}
	if got := white(img, 4, 5); got != [3]uint16{65535, 65535, 65535} {
		t.Fatalf("transparent: %v", got)
	}
	for _, tc := range []struct {
		src   []uint16
		width int
		want  []uint16
	}{
		{[]uint16{0, 65535}, 3, []uint16{0, 32768, 65535}},
		{[]uint16{0, 0, 65535, 65535}, 1, []uint16{32768}},
		{[]uint16{12345}, 3, []uint16{12345, 12345, 12345}},
	} {
		src := image.NewGray16(image.Rect(0, 0, len(tc.src), 1))
		for x, v := range tc.src {
			src.SetGray16(x, 0, color.Gray16{Y: v})
		}
		for x, want := range tc.want {
			if got := sample(src, x, 0, tc.width, 1); got != [3]uint16{want, want, want} {
				t.Fatalf("resize %v to %d at %d: %v", tc.src, tc.width, x, got)
			}
		}
	}
	// Four neighbors, horizontal then vertical, rounding only at the end.
	src := image.NewNRGBA64(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			src.SetNRGBA64(x, y, color.NRGBA64{uint16(10000*x + 30000*y), 0, 0, 65535})
		}
	}
	if got := sample(src, 0, 0, 1, 1); got != [3]uint16{20000, 0, 0} {
		t.Fatalf("2D interpolation: %v", got)
	}
}

func TestContainLayout(t *testing.T) {
	for _, tc := range []struct {
		sw, sh, tw, th int
		want           image.Rectangle
	}{
		{2, 1, 5, 5, image.Rect(0, 1, 5, 4)},
		{1, 2, 5, 5, image.Rect(1, 0, 4, 5)},
		{4, 1, 2, 4, image.Rect(0, 1, 2, 2)},
		{1, 4, 4, 2, image.Rect(1, 0, 2, 2)},
		{16384, 1, 1, 4096, image.Rect(0, 2047, 1, 2048)},
	} {
		if got := contentBounds(tc.sw, tc.sh, tc.tw, tc.th, Contain); got != tc.want {
			t.Fatalf("layout %v: %v", tc, got)
		}
		if got := contentBounds(tc.sw, tc.sh, tc.tw, tc.th, Stretch); got != image.Rect(0, 0, tc.tw, tc.th) {
			t.Fatalf("stretch: %v", got)
		}
	}
	for _, pal := range []Palette{Gray4, Gray16, Spectra6} {
		for _, method := range []Dither{Atkinson, FloydSteinberg, Bayer, None} {
			src := image.NewGray16(image.Rect(0, 0, 4, 1))
			d := &Decoded{pixels: src, width: 4, height: 1}
			out, err := d.Quantize(context.Background(), Target{2, 4, pal, 90000}, Policy{Dither: method})
			if err != nil {
				t.Fatal(err)
			}
			white := byte(len(pal.Colors()) - 1)
			if pal == Spectra6 {
				white = 1
			}
			want := []byte{white, white, 0, 0, white, white, white, white}
			if !bytes.Equal(out, want) {
				t.Fatalf("padding %s/%s: %v", pal, method, out)
			}
		}
	}
}

// Exhaust every discrete mixture in specification order, independently of the
// accelerated tree and neutral lookup. Keep the lexicographically first tie.
func exhaustive(v lab, p []lab) mixture {
	best := mixture{}
	distanceBest := math.Inf(1)
	for i := range p {
		for j := i; j < len(p); j++ {
			for k := range 65 {
				var c lab
				for ch := range 3 {
					c[ch] = p[i][ch] + float64(k)/64*(p[j][ch]-p[i][ch])
				}
				d := distance(v, c)
				if d < distanceBest {
					best = mixture{a: i, b: j, k: k}
					distanceBest = d
				}
			}
		}
	}
	return best
}

func TestBayerExactSearch(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	for _, pal := range []Palette{Gray4, Gray16, Spectra6} {
		var p []lab
		for _, c := range pal.Colors() {
			p = append(p, toLab([3]uint16{uint16(c.R) * 257, uint16(c.G) * 257, uint16(c.B) * 257}))
		}
		tree := mixtureTree(p)
		values := append([]lab{{0, 0, 0}, {100, 0, 0}, {50, 0, 0}, {-10, 400, -400}}, p...)
		for range 100 {
			values = append(values, lab{rng.Float64()*140 - 20, rng.Float64()*300 - 150, rng.Float64()*300 - 150})
		}
		for _, v := range values {
			want := exhaustive(v, p)
			for y := range 8 {
				for x := range 8 {
					index := byte(want.a)
					if bayer[y][x] < want.k {
						index = byte(want.b)
					}
					if got := tree.pixel(v, x, y); got != index {
						t.Fatalf("%s %v at %d,%d: %d != %d (%v)", pal, v, x, y, got, index, want)
					}
				}
			}
		}
	}
	tree := mixtureTree([]lab{{0, 0, 0}, {100, 0, 0}})
	for _, allocation := range []int{0, 1, 16, 32, 48, 63, 64} {
		count := 0
		for y := range 8 {
			for x := range 8 {
				count += int(tree.pixel(lab{float64(allocation) * 100 / 64, 0, 0}, x, y))
			}
		}
		if count != allocation {
			t.Fatalf("Bayer allocation %d: %d", allocation, count)
		}
	}
}

func TestNormalizePreservesExactPixels(t *testing.T) {
	ctx := context.Background()
	for _, pal := range []Palette{Gray4, Gray16, Spectra6} {
		colors := pal.Colors()
		var plte []byte
		for _, c := range colors {
			plte = append(plte, c.R, c.G, c.B)
		}
		// Interlacing forces normalization even though every color is exact.
		source := testPNG(17, 13, 3, 8, true, plte, func(x, y, c int) uint16 { return uint16((x + y) % len(colors)) })
		for _, method := range []Dither{Atkinson, FloydSteinberg, Bayer, None} {
			target := Target{17, 13, pal, 90000}
			out, err := Normalize(ctx, source, target, Policy{Dither: method})
			if err != nil {
				t.Fatal(err)
			}
			d, err := Decode(ctx, out)
			if err != nil {
				t.Fatal(err)
			}
			for y := range 13 {
				for x := range 17 {
					if got := color.NRGBAModel.Convert(d.pixels.At(x, y)); got != colors[(x+y)%len(colors)] {
						t.Fatalf("changed exact %s/%s pixel %d,%d: %v", pal, method, x, y, got)
					}
				}
			}
			passthrough, err := Normalize(ctx, out, target, Policy{Dither: method})
			if err != nil || &out[0] != &passthrough[0] {
				t.Fatalf("compatible bytes were not retained: %v", err)
			}
			again, err := Normalize(ctx, source, target, Policy{Dither: method})
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("nondeterministic output: %v", err)
			}
		}
	}
	// Legacy native 2-bit intermediate grays are re-encoded exactly at depth 4.
	source := testPNG(4, 1, 0, 2, false, nil, func(x, y, c int) uint16 { return uint16(x) })
	out, err := Normalize(ctx, source, Target{4, 1, Gray4, 90000}, Policy{})
	if err != nil || out[24] != 4 {
		t.Fatalf("legacy gray conversion: %v", err)
	}
	d, _ := Decode(ctx, out)
	for x := range 4 {
		r, _, _, _ := d.pixels.At(x, 0).RGBA()
		if r != uint32(x)*21845 {
			t.Fatal("legacy gray re-dithered")
		}
	}
}

func TestDiffusionReference(t *testing.T) {
	// A complete Lab error canvas is an intentionally simple reference. Production
	// must instead reuse three rows and discard errors outside the content.
	src := image.NewNRGBA64(image.Rect(0, 0, 7, 5))
	for y := range 5 {
		for x := range 7 {
			src.SetNRGBA64(x, y, color.NRGBA64{uint16(5000 + x*6000), uint16(8000 + y*8000), uint16(40000 - x*3000), 65535})
		}
	}
	d := &Decoded{pixels: src, width: 7, height: 5}
	for _, pal := range []Palette{Gray4, Gray16, Spectra6} {
		var p []lab
		for _, c := range pal.Colors() {
			p = append(p, toLab([3]uint16{uint16(c.R) * 257, uint16(c.G) * 257, uint16(c.B) * 257}))
		}
		for _, method := range []Dither{Atkinson, FloydSteinberg, None} {
			errorsCanvas := make([]lab, 35)
			var offsets [][3]int
			den := 1.0
			switch method {
			case Atkinson:
				offsets = [][3]int{{1, 0, 1}, {2, 0, 1}, {-1, 1, 1}, {0, 1, 1}, {1, 1, 1}, {0, 2, 1}}
				den = 8
			case FloydSteinberg:
				offsets = [][3]int{{1, 0, 7}, {-1, 1, 3}, {0, 1, 5}, {1, 1, 1}}
				den = 16
			}
			want := make([]byte, 35)
			for y := range 5 {
				for x := range 7 {
					i := y*7 + x
					v := toLab(white(src, x, y))
					for ch := range 3 {
						v[ch] += errorsCanvas[i][ch]
					}
					index := nearest(v, p)
					want[i] = index
					for _, o := range offsets {
						xx, yy := x+o[0], y+o[1]
						if xx < 0 || xx >= 7 || yy >= 5 {
							continue
						}
						for ch := range 3 {
							errorsCanvas[yy*7+xx][ch] += (v[ch] - p[index][ch]) * float64(o[2]) / den
						}
					}
				}
			}
			got, err := d.Quantize(context.Background(), Target{7, 5, pal, 90000}, Policy{Dither: method})
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s/%s: %v != %v: %v", pal, method, got, want, err)
			}
		}
	}
}

func TestNormalizationCancellationAndBudget(t *testing.T) {
	source := testPNG(3, 2, 2, 8, false, nil, func(x, y, c int) uint16 { return uint16(x*70 + y*20 + c*15) })
	target := Target{31, 23, Gray4, 90000}
	out, err := Normalize(context.Background(), source, target, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{len(out) - 1, len(out), len(out) + 1} {
		target.PNGByteLimit = limit
		_, err := Normalize(context.Background(), source, target, Policy{})
		if (err == nil) != (limit > len(out)) {
			t.Fatalf("budget %d: %v", limit, err)
		}
	}
	d, err := Decode(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []Dither{Atkinson, FloydSteinberg, Bayer, None} {
		for _, checkpoint := range []int{1, 5, 30} {
			ctx := cancelAt(checkpoint)
			_, err := d.Quantize(ctx, Target{1024, 1024, Gray16, 750000}, Policy{Dither: method})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel %s/%d: %v", method, checkpoint, err)
			}
			ctx.cancel()
		}
	}
	a, _ := (Policy{}).Revision()
	b, _ := (Policy{Contain, Atkinson}).Revision()
	c, _ := (Policy{Stretch, None}).Revision()
	if a != b || a == c {
		t.Fatal("processing revision defaults or settings")
	}
	for _, p := range []Policy{{Fit: "invalid"}, {Dither: "invalid"}} {
		if _, err := Normalize(context.Background(), source, target, p); err == nil {
			t.Fatal("accepted invalid policy")
		}
	}
}
