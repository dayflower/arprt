package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"testing"
)

// These are behavioral probes of the pinned standard library, including gaps
// that the future complete-file validator must close. They are not an input
// validator implementation.
func TestPinnedDecoder(t *testing.T) {
	for typ, depths := range map[byte][]byte{0: {1, 2, 4, 8, 16}, 2: {8, 16}, 3: {1, 2, 4, 8}, 4: {8, 16}, 6: {8, 16}} {
		for _, depth := range depths {
			for _, adam := range []bool{false, true} {
				t.Run(fmt.Sprintf("type%d-depth%d-adam%v", typ, depth, adam), func(t *testing.T) {
					b := fixture(17, 13, typ, depth, adam, false)
					if _, err := png.Decode(bytes.NewReader(b)); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
	b := fixture(8, 8, 6, 16, false, false)
	for name, data := range map[string][]byte{
		"trailing-after-IEND":        append(bytes.Clone(b), 'x'),
		"shared-first-frame":         apng(b, true, false),
		"separate-default":           apng(b, false, false),
		"invalid-animation-sequence": apng(b, false, true),
		"duplicate-IHDR":             append(append(bytes.Clone(b[:33]), b[8:33]...), b[33:]...),
		"unknown-critical":           insert(b, "ABCD", nil),
		"ignored-gamma":              insert(b, "gAMA", []byte{0, 0, 177, 143}),
	} {
		t.Run(name, func(t *testing.T) {
			img, err := png.Decode(bytes.NewReader(data))
			// Go 1.26.8 rejects a duplicate IHDR, but accepts the other gaps.
			if name == "duplicate-IHDR" {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _ := png.Decode(bytes.NewReader(b))
			for y := 0; y < 8; y++ {
				for x := 0; x < 8; x++ {
					if img.At(x, y) != want.At(x, y) {
						t.Fatal("default image or samples changed")
					}
				}
			}
		})
	}
	for _, kind := range []string{"IHDR", "IDAT", "IEND", "gAMA", "fdAT"} {
		data := apng(insert(b, "gAMA", []byte{0, 0, 177, 143}), false, false)
		for pos := 8; pos < len(data); {
			n := int(binary.BigEndian.Uint32(data[pos:]))
			if string(data[pos+4:pos+8]) == kind {
				data[pos+8+n] ^= 1
				break
			}
			pos += 12 + n
		}
		if _, err := png.Decode(bytes.NewReader(data)); err == nil {
			t.Fatalf("accepted bad %s CRC", kind)
		}
	}
	if _, err := png.Decode(bytes.NewReader(b[:len(b)-1])); err == nil {
		t.Fatal("accepted truncation")
	}
	// Too much/too little decompressed pixel data must be rejected even when
	// the PNG CRC and zlib checksum are valid.
	for _, delta := range []int{-1, 1} {
		raw := make([]byte, 8*(1+8*8)+delta)
		data := assemble(8, 8, 6, 16, false, raw)
		if _, err := png.Decode(bytes.NewReader(data)); err == nil {
			t.Fatalf("accepted inflated delta %d", delta)
		}
	}
}
