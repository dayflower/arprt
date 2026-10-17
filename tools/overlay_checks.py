"""Palette-aware pixel checks for controller overlay cluster smoke tests."""
from pathlib import Path
from image_checks import PALETTES, inspect

CORNERS = ('top-right', 'bottom-right', 'bottom-left', 'top-left')


def samples(png, image_width=800, image_height=480, palette='gray4', png_byte_limit=90000, legacy=False):
    return inspect(png, image_width, image_height, palette, png_byte_limit, allow_legacy_gray4=legacy)


def physical(x, y, rotation, image_width=800, image_height=480):
    if rotation == 90:
        return image_width - 1 - y, x
    if rotation == 180:
        return image_width - 1 - x, image_height - 1 - y
    if rotation == 270:
        return y, image_height - 1 - x
    return x, y


def origin(corner, width, rotation=0, height=18, margin_horizontal=0, margin_vertical=0, scale=1, image_width=800, image_height=480):
    logical_width, logical_height = (image_height, image_width) if rotation in (90, 270) else (image_width, image_height)
    return (logical_width - (width + margin_horizontal) * scale if corner.endswith('right') else margin_horizontal * scale,
            logical_height - (height + margin_vertical) * scale if corner.startswith('bottom') else margin_vertical * scale)


def assert_overlay(base, composite, corner='top-right', width=258, rotation=0, height=18, margin_horizontal=0, margin_vertical=0, scale=1, image_width=800, image_height=480, palette='gray4', png_byte_limit=90000):
    original = samples(base, image_width, image_height, palette, png_byte_limit, legacy=True)
    drawn = samples(composite, image_width, image_height, palette, png_byte_limit, legacy=False)
    white = PALETTES[palette].index((255, 255, 255))
    fx, fy = origin(corner, width, rotation, height, margin_horizontal, margin_vertical, scale, image_width, image_height)
    x = fx if corner.endswith('right') else 0
    y = fy if corner.startswith('bottom') else 0
    region = {py * image_width + px for ly in range(y, y + (height + margin_vertical) * scale)
              for lx in range(x, x + (width + margin_horizontal) * scale)
              for px, py in [physical(lx, ly, rotation, image_width, image_height)]}
    for i, (old, new) in enumerate(zip(original, drawn)):
        assert new in (0, white) if i in region else new == old, 'overlay changed unexpected pixels'
    for ly in range(y, y + (height + margin_vertical) * scale):
        for lx in range(x, x + (width + margin_horizontal) * scale):
            if lx < fx or lx >= fx + width * scale or ly < fy or ly >= fy + height * scale:
                px, py = physical(lx, ly, rotation, image_width, image_height)
                assert drawn[py * image_width + px] == white, 'foreground entered the edge margin'
    assert any(original[i] != drawn[i] for i in region), 'overlay was not drawn'
    return drawn


def font_glyphs():
    source = Path(__file__).resolve().parents[1] / 'internal/overlay/homevideo/testdata/homevideo-12x18.txt'
    result = {}
    for line in source.read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        code, bitmap = line.split(':')
        result[tuple(int(bitmap[y * 3:y * 3 + 3], 16) for y in range(18))] = chr(int(code, 16))
    assert len(result) == 13
    return result


def timestamp(png, corner='top-right', width=258, rotation=0, margin_horizontal=0, margin_vertical=0, scale=1):
    glyphs = font_glyphs()
    pixels = samples(png)
    x, y = origin(corner, width, rotation, 18, margin_horizontal, margin_vertical, scale)
    # The render-time marker occupies the first 18 pixels, followed by a
    # four-pixel white gap. Decode only the unchanged sixteen numeric glyphs.
    assert any(pixels[py * 800 + px] == 0
               for row in range(18) for col in range(18)
               for px, py in [physical(x + col * scale, y + row * scale, rotation)]), 'render-time marker missing'
    assert all(pixels[py * 800 + px] == 3
               for row in range(18) for col in range(18, 22)
               for px, py in [physical(x + col * scale, y + row * scale, rotation)]), 'render-time marker gap lost'
    x += 22 * scale
    text = ''
    for cell in range(16):
        rows = []
        for row in range(18):
            bits = 0
            for col in range(12):
                px, py = physical(x + (cell * 12 + col) * scale, y + row * scale, rotation)
                bits = (bits << 1) | (pixels[py * 800 + px] == 0)
            rows.append(bits)
        text += glyphs[tuple(rows)]
    return text
