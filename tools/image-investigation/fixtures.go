package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
)

func chunk(kind string, p []byte) []byte {
	b := make([]byte, len(p)+12)
	binary.BigEndian.PutUint32(b, uint32(len(p)))
	copy(b[4:], kind)
	copy(b[8:], p)
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[4:len(b)-4]))
	return b
}
func insert(b []byte, kind string, p []byte) []byte {
	return append(append(append([]byte{}, b[:33]...), chunk(kind, p)...), b[33:]...)
}
func assemble(w, h int, typ, depth byte, adam bool, raw []byte) []byte {
	head := make([]byte, 13)
	binary.BigEndian.PutUint32(head, uint32(w))
	binary.BigEndian.PutUint32(head[4:], uint32(h))
	head[8], head[9] = depth, typ
	if adam {
		head[12] = 1
	}
	b := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", head)...)
	if typ == 3 {
		p := make([]byte, 3*(1<<depth))
		for i := range 1 << depth {
			p[i*3], p[i*3+1], p[i*3+2] = byte(i), byte(255-i), byte(i)
		}
		b = append(b, chunk("PLTE", p)...)
		b = append(b, chunk("tRNS", []byte{0, 128})...)
	}
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	_, _ = z.Write(raw)
	_ = z.Close()
	b = append(b, chunk("IDAT", compressed.Bytes())...)
	return append(b, chunk("IEND", nil)...)
}
func fixture(w, h int, typ, depth byte, adam, entropy bool) []byte {
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[typ]
	passes := [][4]int{{0, 0, 1, 1}}
	if adam {
		passes = [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}
	}
	var raw bytes.Buffer
	state := uint32(42)
	for _, p := range passes {
		pw := (w - p[0] + p[2] - 1) / p[2]
		ph := (h - p[1] + p[3] - 1) / p[3]
		if pw <= 0 || ph <= 0 {
			continue
		}
		row := make([]byte, 1+(pw*channels*int(depth)+7)/8)
		for y := 0; y < ph; y++ {
			for i := 1; i < len(row); i++ {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				if entropy {
					row[i] = byte(state)
				} else {
					row[i] = byte(i % 251)
				}
			}
			raw.Write(row)
		}
	}
	return assemble(w, h, typ, depth, adam, raw.Bytes())
}
func apng(b []byte, shared, badSequence bool) []byte {
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, 1)
	fctl := make([]byte, 26)
	copy(fctl[4:12], b[16:24])
	fctl[21] = 1
	out := insert(b, "acTL", actl)
	if shared {
		out = append(append(append(bytes.Clone(b[:33]), chunk("acTL", actl)...), chunk("fcTL", fctl)...), b[33:]...)
	} else {
		// The separate fallback stays in IDAT; animation data deliberately uses
		// a different RGBA16 image so default selection can be observed.
		frame := fixture(8, 8, 6, 16, false, true)
		n := int(binary.BigEndian.Uint32(frame[33:]))
		fd := make([]byte, 4+n)
		binary.BigEndian.PutUint32(fd, 1)
		if badSequence {
			binary.BigEndian.PutUint32(fd, 7)
		}
		copy(fd[4:], frame[41:41+n])
		out = append(append(append(bytes.Clone(out[:len(out)-12]), chunk("fcTL", fctl)...), chunk("fdAT", fd)...), chunk("IEND", nil)...)
	}
	return out
}
