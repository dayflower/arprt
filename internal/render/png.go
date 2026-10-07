package render

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image/png"
	"io"
)

// ValidatePNG enforces the reviewed OG contract before any publication.
// Chunk lengths, CRCs and inflated scanlines are bounded independently of decode.
func ValidatePNG(b []byte, t Target) error {
	bad := errors.New("image must be a complete, bounded, noninterlaced 2-bit gray4 PNG matching the target")
	if t.Validate() != nil || t.Palette != "gray4" || int64(t.Width)*int64(t.Height) > 384000 || len(b) > MaxPNGBytes || len(b) < 33 || !bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return bad
	}
	var idat []byte
	var indexed, palette, data, end bool
	for pos := 8; pos < len(b); {
		if len(b)-pos < 12 {
			return bad
		}
		n := int(binary.BigEndian.Uint32(b[pos : pos+4]))
		if n > len(b)-pos-12 {
			return bad
		}
		kind := string(b[pos+4 : pos+8])
		body := b[pos+8 : pos+8+n]
		if crc32.ChecksumIEEE(b[pos+4:pos+8+n]) != binary.BigEndian.Uint32(b[pos+8+n:pos+12+n]) {
			return bad
		}
		if pos == 8 && kind != "IHDR" {
			return bad
		}
		switch kind {
		case "IHDR":
			if pos != 8 || n != 13 || binary.BigEndian.Uint32(body[:4]) != uint32(t.Width) || binary.BigEndian.Uint32(body[4:8]) != uint32(t.Height) || body[8] != 2 || (body[9] != 0 && body[9] != 3) || body[10] != 0 || body[11] != 0 || body[12] != 0 {
				return bad
			}
			indexed = body[9] == 3
		case "PLTE":
			if !indexed || palette || data || !bytes.Equal(body, []byte{0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255}) {
				return bad
			}
			palette = true
		case "IDAT":
			if indexed && !palette {
				return bad
			}
			data = true
			idat = append(idat, body...)
		case "IEND":
			if n != 0 || !data || pos+12 != len(b) {
				return bad
			}
			end = true
		case "tRNS", "acTL", "fcTL", "fdAT":
			return bad
		default:
			if len(kind) != 4 || kind[0]&32 == 0 {
				return bad
			}
		}
		pos += 12 + n
	}
	if !end {
		return bad
	}
	compressed := bytes.NewReader(idat)
	z, err := zlib.NewReader(compressed)
	if err != nil {
		return bad
	}
	expected := (int(t.Width)+3)/4 + 1
	raw, err := io.ReadAll(io.LimitReader(z, int64(expected)*int64(t.Height)+1))
	closeErr := z.Close()
	if err != nil || closeErr != nil || compressed.Len() != 0 || len(raw) != expected*int(t.Height) {
		return bad
	}
	for row := 0; row < int(t.Height); row++ {
		if raw[row*expected] > 4 {
			return bad
		}
	}
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		return bad
	}
	return nil
}
