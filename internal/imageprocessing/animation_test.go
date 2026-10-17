package imageprocessing

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestAPNGDefaultImage(t *testing.T) {
	b := testPNG(17, 13, 0, 4, false, nil, func(x, y, c int) uint16 { return uint16((x + y) % 4 * 5) })
	want, err := Decode(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared%v", shared), func(t *testing.T) {
			data := testAPNG(b, shared)
			d, err := Decode(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			for y := range 13 {
				for x := range 17 {
					if d.Pixels().At(x, y) != want.Pixels().At(x, y) {
						t.Fatal("default image changed")
					}
				}
			}
			ok, err := d.Compatible(context.Background(), Target{17, 13, Gray4, 90000})
			if err != nil || ok {
				t.Fatalf("APNG passthrough: %v, %v", ok, err)
			}
		})
	}
	// A shared first frame followed by two fdAT chunks with ancillary data
	// between them exercises contiguous sequence numbers across frame boundaries.
	chunks := testChunks(testAPNG(b, true))
	binary.BigEndian.PutUint32(chunks[1].body, 2)
	fctl := bytes.Clone(chunks[2].body)
	binary.BigEndian.PutUint32(fctl, 1)
	more := []testChunk{{"fcTL", fctl}, {"fdAT", []byte{0, 0, 0, 2, 1}}, {"tEXt", []byte("Note\x00example")}, {"fdAT", []byte{0, 0, 0, 3, 2}}}
	chunks = append(chunks[:len(chunks)-1], append(more, testChunk{"IEND", nil})...)
	if _, err := Decode(context.Background(), testFile(chunks...)); err != nil {
		t.Fatal(err)
	}
}

func TestAPNGRejectStructure(t *testing.T) {
	b := testPNG(8, 8, 0, 2, false, nil, func(x, y, c int) uint16 { return 3 })
	for _, shared := range []bool{false, true} {
		base := testAPNG(b, shared)
		cases := map[string]func([]testChunk) []testChunk{
			"duplicate-control": func(c []testChunk) []testChunk { return append(c[:2:2], c[1:]...) },
			"orphan-frame":      func(c []testChunk) []testChunk { return append(c[:1:1], c[2:]...) },
			"zero-frames":       func(c []testChunk) []testChunk { binary.BigEndian.PutUint32(c[1].body, 0); return c },
			"frame-count":       func(c []testChunk) []testChunk { binary.BigEndian.PutUint32(c[1].body, 2); return c },
			"short-control":     func(c []testChunk) []testChunk { c[1].body = c[1].body[:7]; return c },
			"late-control": func(c []testChunk) []testChunk {
				a := c[1]
				c = append(c[:1:1], c[2:]...)
				return append(c[:len(c)-1:len(c)-1], a, c[len(c)-1])
			},
			"missing-frame-data": func(c []testChunk) []testChunk {
				for i := range c {
					if c[i].kind == "IDAT" || c[i].kind == "fdAT" {
						c[i].body = nil
					}
				}
				return c
			},
		}
		for _, field := range []struct {
			name   string
			offset int
			value  uint32
		}{
			{"sequence", 0, 1}, {"width-zero", 4, 0}, {"height-zero", 8, 0}, {"width-large", 4, 9}, {"height-large", 8, 9},
			{"x-offset", 12, 1}, {"y-offset", 16, 1}, {"x-overflow", 12, ^uint32(0)}, {"y-overflow", 16, ^uint32(0)},
		} {
			cases[field.name] = func(c []testChunk) []testChunk {
				for i := range c {
					if c[i].kind == "fcTL" {
						binary.BigEndian.PutUint32(c[i].body[field.offset:], field.value)
					}
				}
				return c
			}
		}
		cases["short-frame"] = func(c []testChunk) []testChunk {
			for i := range c {
				if c[i].kind == "fcTL" {
					c[i].body = c[i].body[:25]
				}
			}
			return c
		}
		cases["dispose"] = func(c []testChunk) []testChunk {
			for i := range c {
				if c[i].kind == "fcTL" {
					c[i].body[24] = 3
				}
			}
			return c
		}
		cases["blend"] = func(c []testChunk) []testChunk {
			for i := range c {
				if c[i].kind == "fcTL" {
					c[i].body[25] = 2
				}
			}
			return c
		}
		if shared {
			cases["fdAT-for-shared-first"] = func(c []testChunk) []testChunk {
				return append(c[:len(c)-1:len(c)-1], testChunk{"fdAT", []byte{0, 0, 0, 1, 1}}, c[len(c)-1])
			}
			cases["partial-shared-frame"] = func(c []testChunk) []testChunk { binary.BigEndian.PutUint32(c[2].body[4:], 7); return c }
		} else {
			cases["short-fdAT"] = func(c []testChunk) []testChunk { c[len(c)-2].body = []byte{0, 0, 0}; return c }
			cases["fdAT-sequence"] = func(c []testChunk) []testChunk { c[len(c)-2].body[3] = 2; return c }
			cases["fdAT-before-frame"] = func(c []testChunk) []testChunk { c[len(c)-2], c[len(c)-3] = c[len(c)-3], c[len(c)-2]; return c }
			cases["empty-frame-data"] = func(c []testChunk) []testChunk { c[len(c)-2].body = c[len(c)-2].body[:4]; return c }
		}
		for name, mutate := range cases {
			t.Run(fmt.Sprintf("shared%v-%s", shared, name), func(t *testing.T) {
				if _, err := Decode(context.Background(), testFile(mutate(testChunks(base))...)); err == nil {
					t.Fatal("accepted malformed APNG")
				}
			})
		}
		// CRC failures after IDAT cannot be hidden by decoding only a prefix.
		bad := bytes.Clone(base)
		for pos := 8; pos < len(bad); {
			n := int(binary.BigEndian.Uint32(bad[pos:]))
			if string(bad[pos+4:pos+8]) == "fcTL" || string(bad[pos+4:pos+8]) == "fdAT" {
				copyBad := bytes.Clone(bad)
				copyBad[pos+8+n] ^= 1
				if _, err := Decode(context.Background(), copyBad); err == nil {
					t.Fatal("accepted animation CRC failure")
				}
			}
			pos += n + 12
		}
	}
}
