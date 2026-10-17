package imageprocessing

import "context"

// Compatible reports whether the original validated bytes can pass through
// unchanged. A false result is a conversion requirement, not an invalid input.
// Source color metadata does not affect sample or palette comparisons.
func (d *Decoded) Compatible(ctx context.Context, t Target) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := t.Validate(); err != nil {
		return false, err
	}
	if d == nil || d.pixels == nil || d.width != t.Width || d.height != t.Height || d.size >= t.PNGByteLimit || d.animated || d.interlace != 0 || d.transparency {
		return false, nil
	}
	// The temporary 2-bit mode also converts higher-depth input, rather than
	// allowing passthrough to reach the affected native 4-bit firmware decoder.
	if t.Palette == Gray4 && !Gray4Native4 && d.depth > 2 {
		return false, nil
	}
	switch d.kind {
	case 0:
		switch t.Palette {
		case Gray4:
			switch d.depth {
			case 1:
				return true, nil
			case 2:
				bw, _, err := d.gray4Usage(ctx)
				return gray4Depth(bw) == 2, err
			case 4:
				_, exact, err := d.gray4Usage(ctx)
				return exact, err
			}
		case Gray16:
			return d.depth == 1 || d.depth == 2 || d.depth == 4, nil
		}
	case 3:
		colors := t.Palette.Colors()
		for i := 0; i < len(d.palette); i += 3 {
			match := false
			for _, c := range colors {
				if d.palette[i] == c.R && d.palette[i+1] == c.G && d.palette[i+2] == c.B {
					match = true
					break
				}
			}
			if !match {
				return false, nil
			}
		}
		switch t.Palette {
		case Gray4:
			if d.depth == 1 {
				return false, nil
			}
			if d.depth == 2 {
				bw, _, err := d.gray4Usage(ctx)
				return gray4Depth(bw) == 2, err
			}
		case Gray16:
			if d.depth == 1 {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// gray4Usage reuses the validated pixels, including every row and the last
// pixel. It never repeats PNG decoding to classify a two-color image.
func (d *Decoded) gray4Usage(ctx context.Context) (blackWhite, exact bool, err error) {
	blackWhite = true
	for y := range d.height {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		for x := range d.width {
			r, g, b, a := d.pixels.At(x, y).RGBA()
			if a != 65535 || r != g || r != b || r%21845 != 0 {
				return false, false, nil
			}
			if r != 0 && r != 65535 {
				blackWhite = false
			}
		}
	}
	return blackWhite, true, ctx.Err()
}

func gray4Depth(blackWhite bool) byte {
	if !Gray4Native4 {
		// Accept the historical counting heuristic until the firmware migration;
		// retain all four sample values instead of reducing the image to B/W.
		return 2
	}
	// The reviewed firmware's png_draw_count stops at y > 430 and can select
	// black/white updates even with gray pixels elsewhere. Scan the whole image
	// and use 4-bit output whenever intermediate gray is present, regardless of
	// how many colors occur. Native 4-bit assumes corrected nibble expansion.
	// https://github.com/usetrmnl/trmnl-firmware/blob/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa/src/display.cpp#L1574
	if blackWhite {
		return 2
	}
	return 4
}
