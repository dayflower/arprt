package imageprocessing

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

var errOutputSize = errors.New("PNG exceeds exclusive output byte limit")

// Encode writes canonical palette indices as a static, opaque PNG. It does not
// resize, quantize, dither or copy source metadata. Gray4 indices 0/1/2/3 map
// exactly to native samples 0/5/10/15 at depth 4, or 0/3 at depth 2 for black/white.
func Encode(ctx context.Context, indices []byte, t Target) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if len(indices) != t.Width*t.Height {
		return nil, errors.New("invalid palette index dimensions")
	}
	colors := t.Palette.Colors()
	blackWhite := true
	for y := range t.Height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, index := range indices[y*t.Width : (y+1)*t.Width] {
			if int(index) >= len(colors) {
				return nil, errors.New("invalid palette index")
			}
			if index != 0 && index != 3 {
				blackWhite = false
			}
		}
	}
	depth, kind := byte(4), byte(0)
	if t.Palette == Gray4 {
		depth = gray4Depth(blackWhite)
	}
	overhead := 57 // Signature, IHDR, IDAT framing and IEND.
	if t.Palette == Spectra6 {
		kind = 3
		overhead += 12 + 3*len(colors)
	}
	if t.PNGByteLimit <= overhead {
		return nil, errOutputSize
	}
	compressed := &boundedBuffer{ctx: ctx, limit: t.PNGByteLimit - overhead - 1}
	// Fix the compression level independently of content and device budget.
	// BestCompression's long match search on diffusion patterns can consume
	// the processing deadline at the controller's 500m CPU limit. Default
	// compression preserves every sample while bounding that extra search.
	z, err := zlib.NewWriterLevel(compressed, zlib.DefaultCompression)
	if err != nil {
		return nil, err
	}
	row := make([]byte, 1+(t.Width*int(depth)+7)/8)
	for y := range t.Height {
		if err := ctx.Err(); err != nil {
			_ = z.Close()
			return nil, err
		}
		clear(row)
		for x, v := range indices[y*t.Width : (y+1)*t.Width] {
			if t.Palette == Gray4 && depth == 4 {
				v *= 5
			}
			perByte := 8 / int(depth)
			row[1+x/perByte] |= v << uint(8-int(depth)*(x%perByte+1))
		}
		if _, err := z.Write(row); err != nil {
			_ = z.Close()
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Grow(overhead + compressed.Len())
	output.WriteString(signature)
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[:4], uint32(t.Width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(t.Height))
	ihdr[8], ihdr[9] = depth, kind
	writeChunk(&output, "IHDR", ihdr[:])
	if kind == 3 {
		var plte []byte
		for _, c := range colors {
			plte = append(plte, c.R, c.G, c.B)
		}
		writeChunk(&output, "PLTE", plte)
	}
	writeChunk(&output, "IDAT", compressed.Bytes())
	writeChunk(&output, "IEND", nil)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	ctx   context.Context
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.limit-b.Len() {
		return 0, errOutputSize
	}
	return b.Buffer.Write(p)
}

func writeChunk(out *bytes.Buffer, kind string, body []byte) {
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(len(body)))
	out.Write(word[:])
	start := out.Len()
	out.WriteString(kind)
	out.Write(body)
	binary.BigEndian.PutUint32(word[:], crc32.ChecksumIEEE(out.Bytes()[start:]))
	out.Write(word[:])
}
