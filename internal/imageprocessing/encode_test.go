package imageprocessing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"testing"
)

func TestEncodeExactPixels(t *testing.T) {
	for _, p := range []Palette{Gray4, Gray16, Spectra6} {
		for _, size := range [][2]int{{1, 1}, {3, 2}, {17, 13}, {800, 480}} {
			for _, mode := range []string{"black", "white", "black-white", "all-colors", "last-pixel-gray"} {
				if p != Gray4 && mode == "last-pixel-gray" {
					continue
				}
				t.Run(fmt.Sprintf("%s-%dx%d-%s", p, size[0], size[1], mode), func(t *testing.T) {
					target := Target{size[0], size[1], p, 750000}
					colors := p.Colors()
					white := byte(len(colors) - 1)
					if p == Spectra6 {
						white = 1
					}
					indices := make([]byte, target.Width*target.Height)
					for i := range indices {
						switch mode {
						case "white":
							indices[i] = white
						case "black-white":
							indices[i] = byte(i%2) * white
						case "all-colors":
							indices[i] = byte(i % len(colors))
						}
					}
					if mode == "last-pixel-gray" {
						indices[len(indices)-1] = 1
					}
					original := bytes.Clone(indices)
					data, err := Encode(context.Background(), indices, target)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(original, indices) {
						t.Fatal("encoding changed indices")
					}
					repeat, err := Encode(context.Background(), indices, target)
					if err != nil || !bytes.Equal(data, repeat) {
						t.Fatalf("nondeterministic encoding: %v", err)
					}
					img, err := png.Decode(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					if img.Bounds().Dx() != target.Width || img.Bounds().Dy() != target.Height {
						t.Fatal("wrong dimensions")
					}
					blackWhite := true
					for i, index := range indices {
						want := colors[index]
						got := color.NRGBAModel.Convert(img.At(i%target.Width, i/target.Width)).(color.NRGBA)
						if got != want {
							t.Fatalf("pixel %d: got %v, want %v", i, got, want)
						}
						if index != 0 && index != 3 {
							blackWhite = false
						}
					}
					depth, kind := byte(4), byte(0)
					if p == Gray4 && (!Gray4Native4 || blackWhite) {
						depth = 2
					}
					if p == Spectra6 {
						kind = 3
					}
					if data[24] != depth || data[25] != kind {
						t.Fatalf("encoding depth/type %d/%d, want %d/%d", data[24], data[25], depth, kind)
					}
					if data[26] != 0 || data[27] != 0 || data[28] != 0 {
						t.Fatal("invalid compression/filter/interlace")
					}
					chunks := testChunks(data)
					wantKinds := []string{"IHDR", "IDAT", "IEND"}
					if p == Spectra6 {
						wantKinds = []string{"IHDR", "PLTE", "IDAT", "IEND"}
					}
					if len(chunks) != len(wantKinds) {
						t.Fatal("unexpected output metadata")
					}
					for i, kind := range wantKinds {
						if chunks[i].kind != kind {
							t.Fatal("unexpected output chunk")
						}
					}
					if p == Spectra6 {
						want := []byte{0, 0, 0, 255, 255, 255, 255, 243, 56, 191, 0, 0, 100, 64, 255, 67, 138, 28}
						if !bytes.Equal(chunks[1].body, want) {
							t.Fatal("wrong Spectra transmission palette")
						}
					}
					d, err := Decode(context.Background(), data)
					if err != nil {
						t.Fatal(err)
					}
					if ok, err := d.Compatible(context.Background(), target); err != nil || !ok {
						t.Fatalf("encoded output is incompatible: %v, %v", ok, err)
					}
				})
			}
		}
	}
}

func TestEncodeOutputBudget(t *testing.T) {
	for _, p := range []Palette{Gray4, Gray16, Spectra6} {
		target := Target{17, 13, p, 750000}
		indices := make([]byte, 17*13)
		for i := range indices {
			indices[i] = byte(i % len(p.Colors()))
		}
		data, err := Encode(context.Background(), indices, target)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, len(data) - 1, len(data), len(data) + 1} {
			target.PNGByteLimit = limit
			got, err := Encode(context.Background(), indices, target)
			if limit <= len(data) {
				if !errors.Is(err, errOutputSize) || got != nil {
					t.Fatalf("%s limit %d: bytes %d, err %v", p, limit, len(got), err)
				}
			} else if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("boundary success: %v", err)
			}
		}
	}
}

func TestEncodeInvalidAndCanceled(t *testing.T) {
	for _, target := range []Target{
		{0, 1, Gray4, 90000}, {1, 0, Gray4, 90000}, {-1, 1, Gray4, 90000},
		{4097, 1, Gray4, 90000}, {4096, 1025, Gray4, 90000}, {1, 1, "invalid", 90000},
		{1, 1, Gray4, 0}, {1, 1, Gray4, -1}, {int(^uint(0) >> 1), 2, Gray4, 90000},
	} {
		if _, err := Encode(context.Background(), []byte{0}, target); err == nil {
			t.Fatalf("accepted target %v", target)
		}
	}
	for _, p := range []Palette{Gray4, Gray16, Spectra6} {
		if _, err := Encode(context.Background(), []byte{byte(len(p.Colors()))}, Target{1, 1, p, 90000}); err == nil {
			t.Fatal("accepted invalid index")
		}
	}
	if _, err := Encode(context.Background(), nil, Target{1, 1, Gray4, 90000}); err == nil {
		t.Fatal("accepted missing pixels")
	}
	for _, n := range []int{1, 10, 150, 262} {
		ctx := cancelAt(n)
		if _, err := Encode(ctx, make([]byte, 257*129), Target{257, 129, Gray4, 90000}); !errors.Is(err, context.Canceled) {
			t.Fatalf("checkpoint %d: %v", n, err)
		}
		ctx.cancel()
	}
}

func TestPaletteCopies(t *testing.T) {
	colors := Spectra6.Colors()
	colors[0] = color.NRGBA{1, 2, 3, 4}
	if Spectra6.Colors()[0] != (color.NRGBA{0, 0, 0, 255}) {
		t.Fatal("shared palette mutated")
	}
}

func TestOutputDimensionBounds(t *testing.T) {
	for _, target := range []Target{
		{4096, 1, Gray4, 750000}, {1, 4096, Gray4, 750000},
		{4096, 1024, Gray16, 750000}, {1872, 1404, Gray16, 750000},
		{1200, 1600, Spectra6, 750000},
	} {
		t.Run(fmt.Sprintf("%s-%dx%d", target.Palette, target.Width, target.Height), func(t *testing.T) {
			data, err := Encode(context.Background(), make([]byte, target.Width*target.Height), target)
			if err != nil {
				t.Fatal(err)
			}
			d, err := Decode(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := d.Compatible(context.Background(), target); err != nil || !ok {
				t.Fatalf("boundary output: %v, %v", ok, err)
			}
		})
	}
}
