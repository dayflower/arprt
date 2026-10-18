"""Independent, bounded output PNG inspection for deployment acceptance."""

import os
import struct
import zlib

PALETTES = {
    "gray4": tuple((v, v, v) for v in (0, 85, 170, 255)),
    "gray16": tuple((v, v, v) for v in range(0, 256, 17)),
    "spectra6": (
        (0, 0, 0),
        (255, 255, 255),
        (255, 243, 56),
        (191, 0, 0),
        (100, 64, 255),
        (67, 138, 28),
    ),
}

# Match the controller build under test; changing this never changes a running
# controller. Normal builds and the default smoke matrix use two-bit gray4.
GRAY4_NATIVE4 = os.environ.get("ARPRT_GRAY4_NATIVE4", "false") == "true"
GRAY4_FIXTURE = "normalized-gray4.png" if GRAY4_NATIVE4 else "target-gray4.png"


def inspect(
    png,
    width,
    height,
    palette,
    png_byte_limit,
    generated=False,
    allow_legacy_gray4=False,
    gray4_native4=GRAY4_NATIVE4,
):
    """Return canonical palette indices, checking every declared entry/pixel.

    Only static opaque device output is accepted. Inflation is capped at the
    exact expected canvas size; this is independent of the controller decoder.
    """
    assert 0 < width <= 4096 and 0 < height <= 4096 and width * height <= 4194304
    assert png[:8] == b"\x89PNG\r\n\x1a\n" and len(png) < png_byte_limit
    colors = PALETTES[palette]
    data, pos, table, kinds = bytearray(), 8, None, []
    while pos < len(png):
        assert pos + 12 <= len(png)
        length = struct.unpack_from(">I", png, pos)[0]
        assert pos + length + 12 <= len(png)
        kind, body = png[pos + 4 : pos + 8], png[pos + 8 : pos + 8 + length]
        assert (
            zlib.crc32(kind + body)
            == struct.unpack_from(">I", png, pos + 8 + length)[0]
        )
        assert kinds or kind == b"IHDR"
        if kind == b"IHDR":
            assert not kinds and len(body) == 13
            w, h, depth, color, compression, filtering, interlace = struct.unpack(
                ">IIBBBBB", body
            )
            assert (w, h, compression, filtering, interlace) == (width, height, 0, 0, 0)
            assert depth in (1, 2, 4, 8) and color in (0, 3)
            if color == 0:
                assert palette != "spectra6" and depth in (1, 2, 4)
            elif palette == "gray4":
                assert depth in (2, 4, 8)
            elif palette == "gray16":
                assert depth in (2, 4, 8)
        elif kind == b"PLTE":
            assert table is None and color == 3 and b"IDAT" not in kinds
            assert 0 < len(body) <= 3 * (1 << depth) and len(body) % 3 == 0
            table = [tuple(body[i : i + 3]) for i in range(0, len(body), 3)]
            assert all(c in colors for c in table), "off-palette declared entry"
        elif kind == b"IDAT":
            assert kinds[-1] in (b"IHDR", b"PLTE", b"IDAT") or b"IDAT" not in kinds
            data.extend(body)
        elif kind == b"IEND":
            assert not body and pos + 12 == len(png)
        else:
            assert kind not in (b"tRNS", b"acTL", b"fcTL", b"fdAT")
            assert kind[0] & 32, "unknown critical chunk"
        kinds.append(kind)
        pos += length + 12
    assert kinds[-1] == b"IEND" and data and (color == 0 or table)
    stride = (width * depth + 7) // 8
    expected = (stride + 1) * height
    decoder = zlib.decompressobj()
    packed = decoder.decompress(data, expected + 1)
    assert len(packed) == expected and decoder.eof and not decoder.unused_data
    pixels, previous = bytearray(), bytearray(stride)
    lookup = [colors.index(c) for c in table] if table else None
    for y in range(height):
        offset = y * (stride + 1)
        mode, row = packed[offset], bytearray(packed[offset + 1 : offset + 1 + stride])
        assert mode in range(5)
        for x in range(stride):
            a, b, c = row[x - 1] if x else 0, previous[x], previous[x - 1] if x else 0
            if mode == 1:
                prediction = a
            elif mode == 2:
                prediction = b
            elif mode == 3:
                prediction = (a + b) // 2
            elif mode == 4:
                p = a + b - c
                prediction = min((a, b, c), key=lambda v: abs(p - v))
            else:
                prediction = 0
            row[x] = (row[x] + prediction) & 255
        for x in range(width):
            sample = (row[x * depth // 8] >> (8 - depth - x * depth % 8)) & (
                (1 << depth) - 1
            )
            if lookup is not None:
                assert sample < len(lookup)
                pixels.append(lookup[sample])
            else:
                value = sample * 255 // ((1 << depth) - 1)
                assert (value, value, value) in colors
                pixels.append(colors.index((value, value, value)))
        previous = row
    if palette == "gray4" and not allow_legacy_gray4:
        if gray4_native4:
            if depth == 2:
                assert set(pixels) <= {0, 3}, (
                    "intermediate gray requires four-bit output"
                )
        else:
            assert depth <= 2, "temporary gray4 mode requires at most two-bit output"
    if generated:
        expected_depth = (
            2
            if palette == "gray4" and (not gray4_native4 or set(pixels) <= {0, 3})
            else 4
        )
        assert (depth, color) == (expected_depth, 3 if palette == "spectra6" else 0)
        if color == 3:
            assert tuple(table) == colors
    return pixels
