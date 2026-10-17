package imageprocessing

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
)

func TestPassthroughDepthTable(t *testing.T) {
	for _, palette := range []Palette{Gray4, Gray16, Spectra6} {
		for _, kind := range []byte{0, 3} {
			depths := []byte{1, 2, 4, 8}
			if kind == 0 {
				depths = append(depths, 16)
			}
			for _, depth := range depths {
				t.Run(fmt.Sprintf("%s-type%d-depth%d", palette, kind, depth), func(t *testing.T) {
					var plte []byte
					if kind == 3 {
						plte = []byte{255, 255, 255, 0, 0, 0}
					}
					b := testPNG(3, 2, kind, depth, false, plte, func(x, y, c int) uint16 {
						if kind == 3 {
							return uint16(x % 2)
						}
						if x%2 == 0 {
							return 0
						}
						return uint16(uint32(1)<<depth - 1)
					})
					original := bytes.Clone(b)
					d, err := Decode(context.Background(), b)
					if err != nil {
						t.Fatal(err)
					}
					want := false
					if kind == 0 {
						want = palette != Spectra6 && depth <= 4
					} else {
						want = palette == Spectra6 || depth != 1
					}
					got, err := d.Compatible(context.Background(), Target{3, 2, palette, 90000})
					if err != nil || got != want {
						t.Fatalf("compatible=%v, %v; want %v", got, err, want)
					}
					if !bytes.Equal(b, original) {
						t.Fatal("input bytes changed")
					}
				})
			}
		}
	}
}

func TestWholeImageGray4(t *testing.T) {
	// Two intermediate grays are not a black/white image, regardless of the
	// firmware's color-count heuristic or whether black and white are absent.
	for _, kind := range []byte{0, 3} {
		var plte []byte
		if kind == 3 {
			plte = []byte{0, 0, 0, 85, 85, 85, 170, 170, 170}
		}
		b := testPNG(2, 1, kind, 2, false, plte, func(x, y, c int) uint16 { return uint16(x + 1) })
		d, err := Decode(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := d.Compatible(context.Background(), Target{2, 1, Gray4, 90000}); err != nil || ok {
			t.Fatalf("two intermediate grays: %v, %v", ok, err)
		}
	}
	for _, kind := range []byte{0, 3} {
		for _, depth := range []byte{2, 4} {
			for _, row := range []int{0, 431, 479} {
				for _, count := range []int{2, 3, 4} {
					t.Run(fmt.Sprintf("type%d-depth%d-row%d-colors%d", kind, depth, row, count), func(t *testing.T) {
						var plte []byte
						if kind == 3 {
							plte = []byte{255, 255, 255, 170, 170, 170, 85, 85, 85, 0, 0, 0}
						}
						b := testPNG(800, 480, kind, depth, false, plte, func(x, y, c int) uint16 {
							v := uint16(0)
							if y == row && x >= 800-count+1 {
								v = uint16(x - (800 - count))
							}
							if kind == 0 && depth == 4 {
								v *= 5
							}
							return v
						})
						d, err := Decode(context.Background(), b)
						if err != nil {
							t.Fatal(err)
						}
						ok, err := d.Compatible(context.Background(), Target{800, 480, Gray4, 90000})
						if err != nil || ok != (depth == 4) {
							t.Fatalf("compatible=%v, %v", ok, err)
						}
					})
				}
			}
		}
	}
	// A native 4-bit gray outside 0/5/10/15 requires quantization.
	b := testPNG(1, 1, 0, 4, false, nil, func(x, y, c int) uint16 { return 1 })
	d, err := Decode(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Compatible(context.Background(), Target{1, 1, Gray4, 90000}); err != nil || ok {
		t.Fatalf("off-gray native sample: %v, %v", ok, err)
	}
}

func TestIndexedPaletteMembership(t *testing.T) {
	for _, p := range []Palette{Gray4, Gray16, Spectra6} {
		colors := p.Colors()
		var reversed []byte
		for i := len(colors) - 1; i >= 0; i-- {
			c := colors[i]
			reversed = append(reversed, c.R, c.G, c.B)
		}
		for name, plte := range map[string][]byte{
			"all-reversed":       reversed,
			"subset":             reversed[:6],
			"duplicate":          append(bytes.Clone(reversed[:3]), reversed[:3]...),
			"unused-off-palette": append(bytes.Clone(reversed), 1, 2, 3),
		} {
			t.Run(fmt.Sprintf("%s-%s", p, name), func(t *testing.T) {
				b := testPNG(3, 2, 3, 8, false, plte, func(x, y, c int) uint16 { return 0 })
				d, err := Decode(context.Background(), b)
				if err != nil {
					t.Fatal(err)
				}
				ok, err := d.Compatible(context.Background(), Target{3, 2, p, 90000})
				if err != nil || ok != (name != "unused-off-palette") {
					t.Fatalf("compatible=%v, %v", ok, err)
				}
			})
		}
	}
	for _, plte := range [][]byte{{0, 0, 0, 255, 0, 0}, {0, 0, 0, 191, 0, 0}} {
		b := testPNG(1, 1, 3, 1, false, plte, func(x, y, c int) uint16 { return 0 })
		d, err := Decode(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := d.Compatible(context.Background(), Target{1, 1, Spectra6, 90000})
		if err != nil || ok != (plte[3] == 191) {
			t.Fatalf("pure/reference red: %v, %v", ok, err)
		}
	}
}

func TestCompatibilityGates(t *testing.T) {
	b := testPNG(3, 2, 0, 2, false, nil, func(x, y, c int) uint16 { return 3 })
	d, err := Decode(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Target{{3, 2, Gray4, len(b) - 1}, {3, 2, Gray4, len(b)}, {2, 2, Gray4, 90000}, {3, 1, Gray4, 90000}} {
		if ok, err := d.Compatible(context.Background(), target); err != nil || ok {
			t.Fatalf("target %v: %v, %v", target, ok, err)
		}
	}
	if ok, err := d.Compatible(context.Background(), Target{3, 2, Gray4, len(b) + 1}); err != nil || !ok {
		t.Fatalf("exclusive size boundary: %v, %v", ok, err)
	}
	ctx := cancelAt(3)
	if _, err := d.Compatible(ctx, Target{3, 2, Gray4, 90000}); err == nil {
		t.Fatal("classification ignored cancellation")
	}
	ctx.cancel()
	for name, data := range map[string][]byte{
		"Adam7":           testPNG(3, 2, 0, 2, true, nil, func(x, y, c int) uint16 { return 3 }),
		"RGB-black-white": testPNG(3, 2, 2, 8, false, nil, func(x, y, c int) uint16 { return 255 }),
		"opaque-RGBA":     testPNG(3, 2, 6, 8, false, nil, func(x, y, c int) uint16 { return 255 }),
	} {
		d, err := Decode(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := d.Compatible(context.Background(), Target{3, 2, Gray4, 90000}); err != nil || ok {
			t.Fatalf("%s: %v, %v", name, ok, err)
		}
	}
}

func TestIgnoredColorMetadata(t *testing.T) {
	b := testPNG(3, 2, 0, 4, false, nil, func(x, y, c int) uint16 { return 5 })
	chunks := testChunks(b)
	for _, metadata := range []testChunk{
		{"gAMA", []byte{0, 0, 177, 143}}, {"cHRM", make([]byte, 32)},
		{"iCCP", append([]byte("Profile\x00\x00"), testDeflate([]byte("not interpreted"))...)},
		{"sRGB", []byte{0}},
	} {
		for _, animated := range []bool{false, true} {
			data := testFile(chunks[0], metadata, chunks[1], chunks[2])
			if animated {
				data = testAPNG(data, false)
			}
			d, err := Decode(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			for y := range 2 {
				for x := range 3 {
					r, g, b, a := d.Pixels().At(x, y).RGBA()
					if r != 21845 || g != r || b != r || a != 65535 {
						t.Fatalf("%s changed sample", metadata.kind)
					}
				}
			}
			ok, err := d.Compatible(context.Background(), Target{3, 2, Gray4, 90000})
			if err != nil || ok == animated {
				t.Fatalf("%s metadata compatibility: %v, %v", metadata.kind, ok, err)
			}
		}
	}
}

func TestLegacyGray4Fixtures(t *testing.T) {
	for _, name := range []string{"target-gray4.png", "target-indexed-gray4.png"} {
		data, err := os.ReadFile("../../testdata/png/" + name)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Decode(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := d.Compatible(context.Background(), Target{800, 480, Gray4, 90000}); err != nil || ok {
			t.Fatalf("legacy intermediate gray requires conversion: %v, %v", ok, err)
		}
	}
}
