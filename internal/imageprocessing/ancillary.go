package imageprocessing

import "bytes"

func knownSingleton(kind string) bool {
	switch kind {
	case "PLTE", "tRNS", "cHRM", "gAMA", "iCCP", "sBIT", "sRGB", "cICP", "mDCV", "cLLI", "bKGD", "hIST", "pHYs", "tIME", "eXIf":
		return true
	}
	return false
}

// Enforce standard placement/lengths for declarations we ignore. ICC and text
// payloads are never decompressed. Unknown ancillary chunks remain extensible.
func validAncillary(kind string, b []byte, d *Decoded, seen map[string]bool, idat bool) bool {
	if knownSingleton(kind) && seen[kind] {
		return false
	}
	switch kind {
	case "cHRM", "gAMA", "iCCP", "sBIT", "sRGB", "cICP", "mDCV", "cLLI":
		if idat || seen["PLTE"] {
			return false
		}
	case "bKGD", "hIST", "pHYs", "sPLT", "eXIf":
		if idat {
			return false
		}
	}
	switch kind {
	case "cHRM":
		return len(b) == 32
	case "gAMA":
		return len(b) == 4
	case "sRGB":
		return len(b) == 1 && b[0] <= 3
	case "cICP":
		return len(b) == 4
	case "mDCV":
		return len(b) == 24
	case "cLLI":
		return len(b) == 8
	case "iCCP":
		n := bytes.IndexByte(b, 0)
		return n >= 1 && n <= 79 && len(b) > n+2 && b[n+1] == 0
	case "sBIT":
		channels := map[byte]int{0: 1, 2: 3, 3: 3, 4: 2, 6: 4}[d.kind]
		depth := d.depth
		if d.kind == 3 {
			depth = 8
		}
		if len(b) != channels {
			return false
		}
		for _, v := range b {
			if v == 0 || v > depth {
				return false
			}
		}
		return true
	case "bKGD":
		if d.kind == 3 {
			return len(d.palette) > 0 && len(b) == 1 && int(b[0]) < len(d.palette)/3
		}
		if d.kind == 0 || d.kind == 4 {
			return len(b) == 2
		}
		return len(b) == 6
	case "hIST":
		return len(d.palette) > 0 && len(b) == 2*(len(d.palette)/3)
	case "pHYs":
		return len(b) == 9 && b[8] <= 1
	case "tIME":
		return len(b) == 7
	}
	return true
}
