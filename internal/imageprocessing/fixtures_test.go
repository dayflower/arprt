package imageprocessing

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
)

type testChunk struct {
	kind string
	body []byte
}

func (c testChunk) bytes() []byte {
	b := make([]byte, len(c.body)+12)
	binary.BigEndian.PutUint32(b, uint32(len(c.body)))
	copy(b[4:], c.kind)
	copy(b[8:], c.body)
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[4:len(b)-4]))
	return b
}

func testChunks(b []byte) []testChunk {
	var chunks []testChunk
	for pos := 8; pos < len(b); {
		n := int(binary.BigEndian.Uint32(b[pos:]))
		chunks = append(chunks, testChunk{string(b[pos+4 : pos+8]), bytes.Clone(b[pos+8 : pos+8+n])})
		pos += n + 12
	}
	return chunks
}

func testFile(chunks ...testChunk) []byte {
	b := []byte(signature)
	for _, c := range chunks {
		b = append(b, c.bytes()...)
	}
	return b
}

func testDeflate(raw []byte) []byte {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	_, _ = z.Write(raw)
	_ = z.Close()
	return b.Bytes()
}

// testPNG constructs native samples independently of image/png's encoder, which
// cannot emit every legal format or Adam7. Sample values are unscaled integers.
func testPNG(w, h int, kind, depth byte, adam bool, plte []byte, sample func(x, y, channel int) uint16) []byte {
	head := make([]byte, 13)
	binary.BigEndian.PutUint32(head, uint32(w))
	binary.BigEndian.PutUint32(head[4:], uint32(h))
	head[8], head[9] = depth, kind
	passes := [][4]int{{0, 0, 1, 1}}
	if adam {
		head[12] = 1
		passes = [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}
	}
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[kind]
	var raw []byte
	for _, p := range passes {
		for y := p[1]; y < h; y += p[3] {
			if p[0] >= w {
				continue
			}
			var row []byte
			var packed byte
			bits := 0
			for x := p[0]; x < w; x += p[2] {
				for c := range channels {
					v := sample(x, y, c)
					if depth == 16 {
						row = append(row, byte(v>>8), byte(v))
						continue
					}
					if depth == 8 {
						row = append(row, byte(v))
						continue
					}
					packed |= byte(v) << (8 - depth - byte(bits))
					bits += int(depth)
					if bits == 8 {
						row = append(row, packed)
						packed, bits = 0, 0
					}
				}
			}
			if bits != 0 {
				row = append(row, packed)
			}
			raw = append(raw, 0)
			raw = append(raw, row...)
		}
	}
	chunks := []testChunk{{"IHDR", head}}
	if plte != nil {
		chunks = append(chunks, testChunk{"PLTE", plte})
	}
	return testFile(append(chunks, testChunk{"IDAT", testDeflate(raw)}, testChunk{"IEND", nil})...)
}

func testAPNG(b []byte, shared bool) []byte {
	chunks := testChunks(b)
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, 1)
	fctl := make([]byte, 26)
	copy(fctl[4:12], chunks[0].body[:8])
	result := []testChunk{chunks[0], {"acTL", actl}}
	if shared {
		result = append(result, testChunk{"fcTL", fctl})
	}
	result = append(result, chunks[1:len(chunks)-1]...)
	if !shared {
		// Deliberately invalid compressed frame bytes: later frames are opaque
		// to this scanner, and must never replace the valid IDAT default image.
		result = append(result, testChunk{"fcTL", fctl}, testChunk{"fdAT", []byte{0, 0, 0, 1, 0xff}})
	}
	return testFile(append(result, testChunk{"IEND", nil})...)
}
