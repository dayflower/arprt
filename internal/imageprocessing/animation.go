package imageprocessing

import "encoding/binary"

// animationState validates APNG structure only. Later frames' compressed pixels
// and timing are intentionally opaque; no frame canvas is ever allocated.
type animationState struct {
	declared, frames         uint32
	sequence                 uint64
	control, shared, hasData bool
}

func (a *animationState) next(b []byte) bool {
	if a.sequence > uint64(^uint32(0)) || binary.BigEndian.Uint32(b) != uint32(a.sequence) {
		return false
	}
	a.sequence++
	return true
}

func (a *animationState) chunk(kind string, b []byte, idat bool, width, height int) bool {
	switch kind {
	case "acTL":
		if a.control || idat || len(b) != 8 || binary.BigEndian.Uint32(b) == 0 {
			return false
		}
		a.control = true
		a.declared = binary.BigEndian.Uint32(b)
	case "fcTL":
		if !a.control || len(b) != 26 || !a.next(b) || (a.frames > 0 && !a.hasData) || a.frames >= a.declared {
			return false
		}
		w, h := binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:])
		x, y := binary.BigEndian.Uint32(b[12:]), binary.BigEndian.Uint32(b[16:])
		if w == 0 || h == 0 || w > uint32(width) || h > uint32(height) || x > uint32(width)-w || y > uint32(height)-h || b[24] > 2 || b[25] > 1 {
			return false
		}
		if !idat {
			if a.frames != 0 || x != 0 || y != 0 || w != uint32(width) || h != uint32(height) {
				return false
			}
			a.shared = true
		}
		a.frames++
		a.hasData = false
	case "fdAT":
		if !a.control || !idat || a.frames == 0 || (a.shared && a.frames == 1) || len(b) < 4 || !a.next(b) {
			return false
		}
		a.hasData = a.hasData || len(b) > 4
	}
	return true
}

func (a *animationState) complete() bool {
	return !a.control || (a.frames == a.declared && a.hasData)
}
