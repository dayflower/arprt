package overlay

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"github.com/dayflower/arprt/internal/render"
)

// cappedBuffer bounds compressed output even for incompressible inputs.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > render.MaxPNGBytes-57 {
		return 0, errors.New("composite exceeds PNG limit")
	}
	return b.Buffer.Write(p)
}

// encode explicitly packs four 2-bit gray samples per byte. Fixed filter and
// compression settings make identical inputs produce identical PNG bytes.
func encode(ctx context.Context, pixels []byte, width, height int) ([]byte, error) {
	if width < 1 || height < 1 || int64(width)*int64(height) > 384000 || len(pixels) != width*height {
		return nil, errors.New("invalid pixel dimensions")
	}
	var compressed cappedBuffer
	z, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	row := make([]byte, 1+(width+3)/4)
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			z.Close()
			return nil, err
		}
		clear(row)
		for x := 0; x < width; x++ {
			value := pixels[y*width+x]
			if value > 3 {
				z.Close()
				return nil, errors.New("invalid gray sample")
			}
			row[1+x/4] |= value << uint(6-2*(x%4))
		}
		if _, err := z.Write(row); err != nil {
			z.Close()
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, b []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(b)))
		output.Write(n[:])
		output.WriteString(kind)
		output.Write(b)
		crc := crc32.NewIEEE()
		crc.Write([]byte(kind))
		crc.Write(b)
		binary.BigEndian.PutUint32(n[:], crc.Sum32())
		output.Write(n[:])
	}
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[:4], uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(height))
	ihdr[8] = 2
	chunk("IHDR", ihdr[:])
	chunk("IDAT", compressed.Bytes())
	chunk("IEND", nil)
	return output.Bytes(), nil
}
