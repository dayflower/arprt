package imageprocessing

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"testing"
)

func TestLegalPNGFormats(t *testing.T) {
	for kind, depths := range map[byte][]byte{0: {1, 2, 4, 8, 16}, 2: {8, 16}, 3: {1, 2, 4, 8}, 4: {8, 16}, 6: {8, 16}} {
		for _, depth := range depths {
			for _, adam := range []bool{false, true} {
				for _, size := range [][2]int{{1, 1}, {3, 2}, {17, 13}} {
					t.Run(fmt.Sprintf("type%d-depth%d-adam%v-%dx%d", kind, depth, adam, size[0], size[1]), func(t *testing.T) {
						var plte []byte
						if kind == 3 {
							for i := range 1 << depth {
								plte = append(plte, byte(i), byte(255-i), byte(i/2))
							}
						}
						maxSample := uint32(1)<<depth - 1
						sample := func(x, y, c int) uint16 { return uint16(uint32(x*997+y*137+c*1271+1) & maxSample) }
						b := testPNG(size[0], size[1], kind, depth, adam, plte, sample)
						d, err := Decode(context.Background(), b)
						if err != nil {
							t.Fatal(err)
						}
						for y := range size[1] {
							for x := range size[0] {
								expand := func(c int) uint16 { return uint16(uint32(sample(x, y, c)) * 65535 / maxSample) }
								want := color.NRGBA64{A: 65535}
								switch kind {
								case 0:
									want.R, want.G, want.B = expand(0), expand(0), expand(0)
								case 2, 6:
									want.R, want.G, want.B = expand(0), expand(1), expand(2)
									if kind == 6 {
										want.A = expand(3)
									}
								case 3:
									i := int(sample(x, y, 0)) * 3
									want.R, want.G, want.B = uint16(plte[i])*257, uint16(plte[i+1])*257, uint16(plte[i+2])*257
								case 4:
									want.R, want.G, want.B, want.A = expand(0), expand(0), expand(0), expand(1)
								}
								got := d.Pixels().At(x, y)
								// Compare premultiplied values without discarding native 16-bit samples.
								gr, gg, gb, ga := got.RGBA()
								wr, wg, wb, wa := want.RGBA()
								if gr != wr || gg != wg || gb != wb || ga != wa {
									t.Fatalf("pixel %d,%d: got %v, want %v", x, y, got, want)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestPNGTransparency(t *testing.T) {
	for _, tc := range []struct {
		kind, depth byte
		plte, trns  []byte
	}{
		{0, 4, nil, []byte{0, 5}}, {0, 16, nil, []byte{0x12, 0x34}},
		{2, 8, nil, []byte{0, 5, 0, 5, 0, 5}}, {2, 16, nil, []byte{0x12, 0x34, 0x12, 0x34, 0x12, 0x34}},
		{3, 2, []byte{0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255}, []byte{0, 128, 255}},
	} {
		t.Run(fmt.Sprintf("type%d-depth%d", tc.kind, tc.depth), func(t *testing.T) {
			v := uint16(5)
			if tc.depth == 16 {
				v = 0x1234
			}
			if tc.kind == 3 {
				v = 0
			}
			b := testPNG(2, 1, tc.kind, tc.depth, false, tc.plte, func(x, y, c int) uint16 { return v + uint16(x) })
			chunks := testChunks(b)
			at := 1
			if tc.plte != nil {
				at++
			}
			chunks = append(chunks[:at:at], append([]testChunk{{"tRNS", tc.trns}}, chunks[at:]...)...)
			d, err := Decode(context.Background(), testFile(chunks...))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, alpha := d.Pixels().At(0, 0).RGBA()
			if alpha != 0 {
				t.Fatal("transparent sample became opaque")
			}
			_, _, _, alpha = d.Pixels().At(1, 0).RGBA()
			if tc.kind == 3 {
				if alpha != 128*257 {
					t.Fatal("partial indexed alpha changed")
				}
			} else if alpha != 65535 {
				t.Fatal("opaque sample became transparent")
			}
		})
	}
}

func TestRejectMalformedPNG(t *testing.T) {
	b := testPNG(3, 2, 0, 2, false, nil, func(x, y, c int) uint16 { return 3 })
	base := testChunks(b)
	tests := map[string][]byte{
		"signature": append([]byte("badbytes"), b[8:]...),
		"truncated": b[:len(b)-1], "trailing": append(bytes.Clone(b), 0),
		"missing-IEND": b[:len(b)-12], "missing-IDAT": testFile(base[0], base[2]),
		"duplicate-IHDR":   testFile(base[0], base[0], base[1], base[2]),
		"non-first-IHDR":   testFile(testChunk{"tEXt", nil}, base[0], base[1], base[2]),
		"nonempty-IEND":    testFile(base[0], base[1], testChunk{"IEND", []byte{0}}),
		"unknown-critical": testFile(base[0], testChunk{"ABCD", nil}, base[1], base[2]),
		"reserved-bit":     testFile(base[0], testChunk{"abct", nil}, base[1], base[2]),
		"nonletter-type":   testFile(base[0], testChunk{"a1Cd", nil}, base[1], base[2]),
		"split-IDAT":       testFile(base[0], base[1], testChunk{"tEXt", nil}, base[1], base[2]),
		"duplicate-PLTE":   testFile(testChunks(testPNG(1, 1, 3, 2, false, []byte{0, 0, 0}, func(x, y, c int) uint16 { return 0 }))[0], testChunk{"PLTE", []byte{0, 0, 0}}, testChunk{"PLTE", []byte{0, 0, 0}}, base[1], base[2]),
		"gray-PLTE":        testFile(base[0], testChunk{"PLTE", []byte{0, 0, 0}}, base[1], base[2]),
		"late-PLTE":        testFile(base[0], base[1], testChunk{"PLTE", []byte{0, 0, 0}}, base[2]),
		"late-tRNS":        testFile(base[0], base[1], testChunk{"tRNS", []byte{0, 0}}, base[2]),
		"duplicate-tRNS":   testFile(base[0], testChunk{"tRNS", []byte{0, 0}}, testChunk{"tRNS", []byte{0, 0}}, base[1], base[2]),
		"bad-tRNS-length":  testFile(base[0], testChunk{"tRNS", []byte{0}}, base[1], base[2]),
		"bad-tRNS-sample":  testFile(base[0], testChunk{"tRNS", []byte{0, 4}}, base[1], base[2]),
		"short-inflated":   testFile(base[0], testChunk{"IDAT", testDeflate([]byte{0, 0, 0})}, base[2]),
		"long-inflated":    testFile(base[0], testChunk{"IDAT", testDeflate([]byte{0, 0, 0, 0, 0})}, base[2]),
		"invalid-filter":   testFile(base[0], testChunk{"IDAT", testDeflate([]byte{5, 0, 0, 0})}, base[2]),
		"compressed-tail":  testFile(base[0], testChunk{"IDAT", append(bytes.Clone(base[1].body), 0)}, base[2]),
		"second-zlib":      testFile(base[0], testChunk{"IDAT", append(bytes.Clone(base[1].body), testDeflate(nil)...)}, base[2]),
		"late-gamma":       testFile(base[0], base[1], testChunk{"gAMA", []byte{0, 0, 0, 1}}, base[2]),
		"duplicate-gamma":  testFile(base[0], testChunk{"gAMA", []byte{0, 0, 0, 1}}, testChunk{"gAMA", []byte{0, 0, 0, 1}}, base[1], base[2]),
		"bad-gamma-length": testFile(base[0], testChunk{"gAMA", nil}, base[1], base[2]),
	}
	for _, offset := range []int{8, 9, 10, 11, 12} {
		head := bytes.Clone(base[0].body)
		head[offset] = 7
		tests[fmt.Sprintf("bad-IHDR-field%d", offset)] = testFile(testChunk{"IHDR", head}, base[1], base[2])
	}
	length := bytes.Clone(b)
	binary.BigEndian.PutUint32(length[33:], ^uint32(0))
	tests["chunk-overflow"] = length
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(context.Background(), b); err == nil {
				t.Fatal("accepted malformed PNG")
			}
		})
	}
	// Check every byte for truncation, and every chunk CRC including ignored chunks.
	for i := 0; i < len(b); i++ {
		if _, err := Decode(context.Background(), b[:i]); err == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	withMetadata := testFile(base[0], testChunk{"raNd", []byte{1, 2, 3}}, base[1], base[2])
	for pos := 8; pos < len(withMetadata); {
		n := int(binary.BigEndian.Uint32(withMetadata[pos:]))
		bad := bytes.Clone(withMetadata)
		bad[pos+8+n] ^= 1
		if _, err := Decode(context.Background(), bad); err == nil {
			t.Fatalf("accepted bad %s CRC", bad[pos+4:pos+8])
		}
		pos += n + 12
	}
}

func TestInputLimits(t *testing.T) {
	b := testPNG(1, 1, 0, 1, false, nil, func(x, y, c int) uint16 { return 0 })
	for _, size := range [][2]uint32{{0, 1}, {1, 0}, {16385, 1}, {1, 16385}, {4096, 4097}, {^uint32(0), ^uint32(0)}} {
		chunks := testChunks(b)
		binary.BigEndian.PutUint32(chunks[0].body, size[0])
		binary.BigEndian.PutUint32(chunks[0].body[4:], size[1])
		if _, err := Decode(context.Background(), testFile(chunks...)); err == nil {
			t.Fatalf("accepted %v", size)
		}
	}
	for _, size := range [][2]int{{16384, 1}, {1, 16384}, {4096, 4096}} {
		data := testPNG(size[0], size[1], 0, 1, false, nil, func(x, y, c int) uint16 { return 0 })
		if _, err := Decode(context.Background(), data); err != nil {
			t.Fatalf("boundary %v: %v", size, err)
		}
	}
	chunks := testChunks(b)
	padding := make([]byte, MaxInputBytes-len(b)-12)
	boundary := testFile(chunks[0], testChunk{"raNd", padding}, chunks[1], chunks[2])
	if len(boundary) != MaxInputBytes {
		t.Fatal("incorrect boundary fixture")
	}
	if _, err := Decode(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), append(boundary, 0)); err == nil {
		t.Fatal("accepted oversized complete file")
	}
}

func TestConsecutiveIDAT(t *testing.T) {
	b := testPNG(17, 13, 6, 16, true, nil, func(x, y, c int) uint16 { return uint16(x*997 + y*137 + c*1271) })
	chunks := testChunks(b)
	parts := []testChunk{chunks[0], {"IDAT", nil}}
	for _, v := range chunks[1].body {
		parts = append(parts, testChunk{"IDAT", []byte{v}}, testChunk{"IDAT", nil})
	}
	parts = append(parts, chunks[2])
	if _, err := Decode(context.Background(), testFile(parts...)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidIndexedPNG(t *testing.T) {
	for _, depth := range []byte{1, 2, 4, 8} {
		for _, adam := range []bool{false, true} {
			b := testPNG(17, 13, 3, depth, adam, []byte{0, 0, 0}, func(x, y, c int) uint16 {
				if x == 16 && y == 12 {
					return 1
				}
				return 0
			})
			if _, err := Decode(context.Background(), b); err == nil {
				t.Fatalf("accepted undeclared index at depth %d, Adam7 %v", depth, adam)
			}
		}
	}
	base := testChunks(testPNG(1, 1, 3, 1, false, []byte{0, 0, 0}, func(x, y, c int) uint16 { return 0 }))
	for name, chunks := range map[string][]testChunk{
		"missing-PLTE":     {base[0], base[2], base[3]},
		"empty-PLTE":       {base[0], {"PLTE", nil}, base[2], base[3]},
		"unaligned-PLTE":   {base[0], {"PLTE", []byte{0, 0}}, base[2], base[3]},
		"too-many-entries": {base[0], {"PLTE", make([]byte, 9)}, base[2], base[3]},
		"tRNS-before-PLTE": {base[0], {"tRNS", []byte{0}}, base[1], base[2], base[3]},
		"tRNS-too-long":    {base[0], base[1], {"tRNS", []byte{0, 128}}, base[2], base[3]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(context.Background(), testFile(chunks...)); err == nil {
				t.Fatal("accepted malformed indexed input")
			}
		})
	}
}

type checkpointContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *checkpointContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func cancelAt(n int) *checkpointContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &checkpointContext{ctx, cancel, n}
}

func TestDecodeCancellation(t *testing.T) {
	b := testPNG(257, 129, 6, 16, true, nil, func(x, y, c int) uint16 { return uint16(x*997 + y*137 + c*1271) })
	for _, n := range []int{1, 3, 5, 10, 25, 50, 100} {
		ctx := cancelAt(n)
		_, err := Decode(ctx, b)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("checkpoint %d: %v", n, err)
		}
		ctx.cancel()
	}
}

func FuzzDecode(f *testing.F) {
	for _, kind := range []byte{0, 2, 3, 4, 6} {
		var plte []byte
		if kind == 3 {
			plte = []byte{0, 0, 0}
		}
		b := testPNG(3, 2, kind, 8, true, plte, func(x, y, c int) uint16 { return 0 })
		f.Add(b)
		f.Add(testAPNG(b, true))
		f.Add(testAPNG(b, false))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxInputBytes {
			return
		}
		d, err := Decode(context.Background(), b)
		if err == nil && (d.Pixels() == nil || d.Pixels().Bounds().Dx() > MaxInputAxis || d.Pixels().Bounds().Dy() > MaxInputAxis) {
			t.Fatal("invalid successful decode")
		}
	})
}
