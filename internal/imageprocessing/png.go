package imageprocessing

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
)

const signature = "\x89PNG\r\n\x1a\n"
const checkBlock = 32 << 10

var errPNG = errors.New("invalid or unbounded PNG")

// Decoded retains one decoded default image and validated encoding metadata.
// Neither the source buffer nor a second inflated canvas is retained.
type Decoded struct {
	pixels                 image.Image
	width, height, size    int
	depth, kind, interlace byte
	palette                []byte
	animated, transparency bool
}

// Pixels exposes the decoded samples for subsequent normalization. Treat it as
// read-only while using Compatible; embedded color-space metadata is ignored.
func (d *Decoded) Pixels() image.Image { return d.pixels }

// Decode validates the entire file before allocating pixels, then decodes IDAT
// exactly once. APNG frame streams are structurally checked but never inflated.
// Work runs synchronously and stops before returning a cancellation error.
func Decode(ctx context.Context, data []byte) (*Decoded, error) {
	d, start, end, err := scan(ctx, data)
	if err != nil {
		return nil, err
	}
	// The standard decoder checks pixels and filters, but its buffering can hide
	// compressed tails. Independently stream-count scanlines without retaining
	// them; the IDAT reader supports ReadByte so zlib cannot read past its member.
	if err := validateStream(ctx, data, start, end, d); err != nil {
		return nil, err
	}
	d.pixels, err = png.Decode(contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return nil, fmt.Errorf("decode PNG: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Go's decoder extends an indexed palette with implicit black for illegal
	// pixel indices, including while merging Adam7 passes. Do not accept those
	// implicit entries as declared colors or silently alter malformed samples.
	if p, ok := d.pixels.(*image.Paletted); ok && len(p.Palette) > len(d.palette)/3 {
		return nil, fmt.Errorf("%w: undeclared palette index", errPNG)
	}
	return d, nil
}

func scan(ctx context.Context, b []byte) (*Decoded, int, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	if len(b) < 33 || len(b) > MaxInputBytes || string(b[:8]) != signature {
		return nil, 0, 0, errPNG
	}
	d := &Decoded{size: len(b)}
	seen := make(map[string]bool)
	start, end := 0, 0
	closedIDAT := false
	var animation animationState
	for pos := 8; pos < len(b); {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		if len(b)-pos < 12 {
			return nil, 0, 0, errPNG
		}
		n64 := uint64(binary.BigEndian.Uint32(b[pos:]))
		if n64 > uint64(len(b)-pos-12) {
			return nil, 0, 0, errPNG
		}
		n := int(n64)
		kind := string(b[pos+4 : pos+8])
		body := b[pos+8 : pos+8+n]
		if !validChunkType(kind) || (pos == 8 && kind != "IHDR") {
			return nil, 0, 0, errPNG
		}
		crc := crc32.NewIEEE()
		for payload := b[pos+4 : pos+8+n]; len(payload) > 0; {
			if err := ctx.Err(); err != nil {
				return nil, 0, 0, err
			}
			part := min(len(payload), checkBlock)
			_, _ = crc.Write(payload[:part])
			payload = payload[part:]
		}
		if crc.Sum32() != binary.BigEndian.Uint32(b[pos+8+n:]) {
			return nil, 0, 0, fmt.Errorf("%w: %s CRC", errPNG, kind)
		}
		if start != 0 && kind != "IDAT" {
			closedIDAT = true
		}
		switch kind {
		case "IHDR":
			if pos != 8 || n != 13 {
				return nil, 0, 0, errPNG
			}
			w, h := binary.BigEndian.Uint32(body), binary.BigEndian.Uint32(body[4:])
			if w > MaxInputAxis || h > MaxInputAxis || !dimensionsValid(int(w), int(h), MaxInputAxis, MaxInputPixels) || !legalDepth(body[9], body[8]) || body[10] != 0 || body[11] != 0 || body[12] > 1 {
				return nil, 0, 0, errPNG
			}
			d.width, d.height = int(w), int(h)
			d.depth, d.kind, d.interlace = body[8], body[9], body[12]
		case "PLTE":
			if seen[kind] || start != 0 || seen["tRNS"] || seen["bKGD"] || d.kind == 0 || d.kind == 4 || n == 0 || n%3 != 0 || n > 768 || (d.kind == 3 && n/3 > 1<<d.depth) {
				return nil, 0, 0, errPNG
			}
			d.palette = bytes.Clone(body)
		case "tRNS":
			if seen[kind] || start != 0 || !validTransparency(d, body) {
				return nil, 0, 0, errPNG
			}
			d.transparency = true
		case "IDAT":
			if closedIDAT || (d.kind == 3 && !seen["PLTE"]) {
				return nil, 0, 0, errPNG
			}
			if start == 0 {
				start = pos
			}
			end = pos + 12 + n
			if animation.frames != 0 {
				animation.hasData = animation.hasData || n > 0
			}
		case "IEND":
			if n != 0 || start == 0 || pos+12 != len(b) || !animation.complete() {
				return nil, 0, 0, errPNG
			}
			return d, start, end, nil
		case "acTL", "fcTL", "fdAT":
			if !animation.chunk(kind, body, start != 0, d.width, d.height) {
				return nil, 0, 0, errPNG
			}
			d.animated = true
		default:
			if kind[0]&32 == 0 || !validAncillary(kind, body, d, seen, start != 0) {
				return nil, 0, 0, errPNG
			}
		}
		// Only fixed known names need duplicate bookkeeping; unknown ancillary
		// names must not create input-proportional map allocations.
		if knownSingleton(kind) {
			seen[kind] = true
		}
		pos += 12 + n
	}
	return nil, 0, 0, errPNG
}

func validChunkType(s string) bool {
	for i := range 4 {
		if (s[i] < 'A' || s[i] > 'Z') && (s[i] < 'a' || s[i] > 'z') {
			return false
		}
	}
	return s[2]&32 == 0
}

func legalDepth(kind, depth byte) bool {
	switch kind {
	case 0:
		return depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16
	case 3:
		return depth == 1 || depth == 2 || depth == 4 || depth == 8
	case 2, 4, 6:
		return depth == 8 || depth == 16
	}
	return false
}

func validTransparency(d *Decoded, b []byte) bool {
	switch d.kind {
	case 0:
		return len(b) == 2 && uint32(binary.BigEndian.Uint16(b)) < uint32(1)<<d.depth
	case 2:
		if len(b) != 6 {
			return false
		}
		for i := 0; i < 6; i += 2 {
			if uint32(binary.BigEndian.Uint16(b[i:])) >= uint32(1)<<d.depth {
				return false
			}
		}
		return true
	case 3:
		return len(d.palette) != 0 && len(b) > 0 && len(b) <= len(d.palette)/3
	}
	return false
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p[:min(len(p), checkBlock)])
}

// idatReader walks the validated consecutive IDAT region without copying data
// or allocating a span for every potentially tiny or empty chunk.
type idatReader struct {
	ctx      context.Context
	data     []byte
	pos, end int
	body     []byte
}

func (r *idatReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.body) == 0 {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.pos == r.end {
			return 0, io.EOF
		}
		n := int(binary.BigEndian.Uint32(r.data[r.pos:]))
		r.body = r.data[r.pos+8 : r.pos+8+n]
		r.pos += n + 12
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n := copy(p[:min(len(p), checkBlock)], r.body)
	r.body = r.body[n:]
	return n, nil
}

func (r *idatReader) ReadByte() (byte, error) {
	var one [1]byte
	_, err := r.Read(one[:])
	return one[0], err
}

func validateStream(ctx context.Context, data []byte, start, end int, d *Decoded) error {
	r := &idatReader{ctx: ctx, data: data, pos: start, end: end}
	z, err := zlib.NewReader(r)
	if err != nil {
		return fmt.Errorf("PNG compression: %w", err)
	}
	expected := scanlineBytes(d.width, d.height, d.kind, d.depth, d.interlace)
	buf := make([]byte, checkBlock)
	n, readErr := io.CopyBuffer(io.Discard, io.LimitReader(contextReader{ctx, z}, expected+1), buf)
	closeErr := z.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if readErr != nil || closeErr != nil || n != expected {
		return errPNG
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return errPNG
	}
	return nil
}

func scanlineBytes(w, h int, kind, depth, interlace byte) int64 {
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[kind]
	passes := [][4]int{{0, 0, 1, 1}}
	if interlace == 1 {
		passes = [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}
	}
	var n int64
	for _, p := range passes {
		pw, ph := (w-p[0]+p[2]-1)/p[2], (h-p[1]+p[3]-1)/p[3]
		if pw > 0 && ph > 0 {
			n += int64(1+(pw*channels*int(depth)+7)/8) * int64(ph)
		}
	}
	return n
}
