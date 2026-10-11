"""Bounded gray4 pixel checks for controller overlay cluster smoke tests."""
from pathlib import Path
import struct
import zlib

CORNERS = ('top-right', 'bottom-right', 'bottom-left', 'top-left')


def samples(png):
    assert png[:8] == b'\x89PNG\r\n\x1a\n' and len(png) < 90000
    data, pos, palette = bytearray(), 8, None
    while pos < len(png):
        length = struct.unpack('>I', png[pos:pos + 4])[0]
        kind, body = png[pos + 4:pos + 8], png[pos + 8:pos + 8 + length]
        assert zlib.crc32(kind + body) == struct.unpack('>I', png[pos + 8 + length:pos + 12 + length])[0]
        if kind == b'IHDR':
            width, height, depth, color, compression, filtering, interlace = struct.unpack('>IIBBBBB', body)
            assert (width, height, depth, compression, filtering, interlace) == (800, 480, 2, 0, 0, 0)
            assert color in (0, 3)
        elif kind == b'PLTE':
            palette = body
        elif kind == b'IDAT':
            data.extend(body)
        pos += length + 12
    if color == 3:
        assert palette == bytes((0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255))
    packed = zlib.decompress(data)
    stride = width // 4
    assert len(packed) == (stride + 1) * height
    pixels, previous = [], [0] * stride
    for y in range(height):
        offset = y * (stride + 1)
        mode, row = packed[offset], list(packed[offset + 1:offset + 1 + stride])
        for x in range(stride):
            a, b, c = row[x - 1] if x else 0, previous[x], previous[x - 1] if x else 0
            if mode == 1:
                row[x] = (row[x] + a) & 255
            elif mode == 2:
                row[x] = (row[x] + b) & 255
            elif mode == 3:
                row[x] = (row[x] + (a + b) // 2) & 255
            elif mode == 4:
                p = a + b - c
                nearest = min((a, b, c), key=lambda v: abs(p - v))
                row[x] = (row[x] + nearest) & 255
            else:
                assert mode == 0
        pixels.extend((value >> shift) & 3 for value in row for shift in (6, 4, 2, 0))
        previous = row
    return pixels


def physical(x, y, rotation):
    if rotation == 90:
        return 799 - y, x
    if rotation == 180:
        return 799 - x, 479 - y
    if rotation == 270:
        return y, 479 - x
    return x, y


def origin(corner, width, rotation=0, height=18, margin_horizontal=0, margin_vertical=0, scale=1):
    logical_width, logical_height = (480, 800) if rotation in (90, 270) else (800, 480)
    return (logical_width - (width + margin_horizontal) * scale if corner.endswith('right') else margin_horizontal * scale,
            logical_height - (height + margin_vertical) * scale if corner.startswith('bottom') else margin_vertical * scale)


def assert_overlay(base, composite, corner='top-right', width=258, rotation=0, height=18, margin_horizontal=0, margin_vertical=0, scale=1):
    original, drawn = samples(base), samples(composite)
    fx, fy = origin(corner, width, rotation, height, margin_horizontal, margin_vertical, scale)
    x = fx if corner.endswith('right') else 0
    y = fy if corner.startswith('bottom') else 0
    region = {py * 800 + px for ly in range(y, y + (height + margin_vertical) * scale)
              for lx in range(x, x + (width + margin_horizontal) * scale)
              for px, py in [physical(lx, ly, rotation)]}
    for i, (old, new) in enumerate(zip(original, drawn)):
        assert new in (0, 3) if i in region else new == old, 'overlay changed unexpected pixels'
    for ly in range(y, y + (height + margin_vertical) * scale):
        for lx in range(x, x + (width + margin_horizontal) * scale):
            if lx < fx or lx >= fx + width * scale or ly < fy or ly >= fy + height * scale:
                px, py = physical(lx, ly, rotation)
                assert drawn[py * 800 + px] == 3, 'foreground entered the edge margin'
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
