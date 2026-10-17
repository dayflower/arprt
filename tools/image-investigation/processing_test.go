package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

func TestNumericalReferences(t *testing.T) {
	for _, c := range []struct {
		rgb  [3]float64
		want lab
	}{
		{[3]float64{0, 0, 0}, lab{0, 0, 0}},
		{[3]float64{65535, 65535, 65535}, lab{100, 0, 0}},
		{[3]float64{65535, 0, 0}, lab{53.2408, 80.0925, 67.2032}},
		{[3]float64{0, 65535, 0}, lab{87.7347, -86.1827, 83.1793}},
		{[3]float64{0, 0, 65535}, lab{32.2970, 79.1875, -107.8602}},
	} {
		got := toLab(c.rgb)
		for i := range 3 {
			if math.Abs(got[i]-c.want[i]) > 0.0001 {
				t.Fatalf("Lab %v: %v", c.rgb, got)
			}
		}
	}
	img := image.NewNRGBA64(image.Rect(0, 0, 1, 1))
	img.SetNRGBA64(0, 0, color.NRGBA64{65535, 0, 0, 32896})
	if got := white(img, 0, 0); got != [3]float64{65535, 32639, 32639} {
		t.Fatalf("white composition: %v", got)
	}
	// An exact equal-distance tie chooses the lower palette index, including
	// out-of-range Lab errors; no clamping occurs before selection.
	if nearest(lab{50, 1000, -1000}, []lab{{0, 0, 0}, {100, 0, 0}}) != 0 {
		t.Fatal("nearest tie")
	}
	for _, pl := range [][]lab{{{0, 0, 0}, {100, 0, 0}}, {{0, 0, 0}, {100, 0, 0}, {50, 50, 0}, {50, -50, 0}}} {
		tree := mixtureTree(pl)
		for _, v := range []lab{{25, 0, 0}, {75, 0, 0}, {-20, 40, -100}, {150, -40, 100}, {50, 20, 0}} {
			for y := range 8 {
				for x := range 8 {
					if tree.pixel(v, x, y) != ordered(v, pl, x, y) {
						t.Fatalf("Bayer search disagrees at %v/%d,%d", v, x, y)
					}
				}
			}
		}
	}
	tree := mixtureTree([]lab{{0, 0, 0}, {100, 0, 0}})
	count := 0
	for y := range 8 {
		for x := range 8 {
			if tree.pixel(lab{75, 0, 0}, x, y) == 1 {
				count++
			}
		}
	}
	if count != 48 {
		t.Fatalf("25/75 Bayer allocation: %d", count)
	}
	// Half-pixel bilinear reduction: the two center pixels contribute equally.
	img2 := image.NewNRGBA64(image.Rect(0, 0, 4, 1))
	for x := range 4 {
		v := uint16(0)
		if x >= 2 {
			v = 65535
		}
		img2.SetNRGBA64(x, 0, color.NRGBA64{v, v, v, 65535})
	}
	p, pl := palette("gray16")
	out, err := process(context.Background(), img2, 1, 1, p, pl, "none")
	if err != nil {
		t.Fatal(err)
	}
	if out.Pix[0] != 8 {
		t.Fatalf("bilinear center pixel: index %d", out.Pix[0])
	}
	b, err := encode(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r, g, bv, a := decoded.At(0, 0).RGBA()
	wr, wg, wb, wa := out.At(0, 0).RGBA()
	if r != wr || g != wg || bv != wb || a != wa {
		t.Fatal("packed encoder changed sample")
	}
}
