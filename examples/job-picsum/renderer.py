#!/usr/bin/env python3
"""Fetch a random Picsum photo and write an arprt-compatible gray4 PNG."""

import argparse
import io
import json
from pathlib import Path
import secrets
import struct
import time
import urllib.request
import zlib

from PIL import Image, ImageOps


def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))


def encode(image):
    """Pack exact grayscale samples, avoiding generic encoders' 8-bit defaults."""
    width, height = image.size
    pixels = image.tobytes()
    rows = bytearray()
    for y in range(height):
        rows.append(0)
        offset = y * width
        for x in range(0, width, 4):
            a, b, c, d = pixels[offset + x:offset + x + 4]
            rows.append(((a // 85) << 6) | ((b // 85) << 4) | ((c // 85) << 2) | (d // 85))
    header = struct.pack(">IIBBBBB", width, height, 2, 0, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(rows, 9)) + chunk(b"IEND", b"")


def render(context, source=None):
    target = context["renderTarget"]
    if context["version"] != "v1" or any(
        target.get(key) != value
        for key, value in {"width": 800, "height": 480, "palette": "gray4"}.items()
    ):
        raise ValueError("Expected Render Context v1 for 800 x 480 / gray4")
    rotation = target.get("rotation", 0)
    if type(rotation) is not int or rotation not in (0, 90, 180, 270):
        raise ValueError("Expected rotation 0, 90, 180 or 270")
    size = (480, 800) if rotation in (90, 270) else (800, 480)
    photo_id = "local"
    if source is None:
        # A fresh random query bypasses cached redirects without sending device data.
        url = f"https://picsum.photos/{size[0]}/{size[1]}.jpg?random={secrets.token_hex(16)}"
        request = urllib.request.Request(url, headers={"User-Agent": "arprt-picsum/1.0"})
        for attempt in range(3):
            try:
                with urllib.request.urlopen(request, timeout=20) as response:
                    source = response.read(8_000_001)
                    photo_id = response.headers.get("Picsum-ID", "unknown")
                if len(source) > 8_000_000:
                    raise ValueError("Downloaded photo exceeds size limit")
                break
            except OSError:
                if attempt == 2:
                    raise
                time.sleep(2)
    with Image.open(io.BytesIO(source)) as photo:
        gray = ImageOps.fit(ImageOps.exif_transpose(photo).convert("L"), size, method=Image.Resampling.LANCZOS)
    gray = ImageOps.autocontrast(gray, cutoff=0.5)
    palette = Image.new("P", (1, 1))
    palette.putpalette([v for shade in (0, 85, 170, 255) for v in (shade,) * 3] + [255] * (252 * 3))
    # Floyd-Steinberg keeps gradients visible with just four physical shades.
    quantized = gray.convert("RGB").quantize(palette=palette, dither=Image.Dither.FLOYDSTEINBERG).convert("L")
    if rotation:
        quantized = quantized.rotate(-rotation, expand=True)
    png = encode(quantized)
    if len(png) >= 90_000:
        # High-frequency photos can exceed the device's compressed-image budget.
        quantized = gray.point(lambda value: min(3, (value + 42) // 85) * 85)
        if rotation:
            quantized = quantized.rotate(-rotation, expand=True)
        png = encode(quantized)
    if len(png) >= 90_000:
        raise ValueError("PNG exceeds device size limit")
    print(f"Rendered Picsum photo {photo_id}: 800x480 gray4, {len(png)} bytes", flush=True)
    return png


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", default="/arprt/context.json")
    parser.add_argument("--output", default="/arprt/output.png")
    parser.add_argument("--input", help="Use a local JPEG for offline checks")
    args = parser.parse_args()
    context = json.loads(Path(args.context).read_text())
    source = Path(args.input).read_bytes() if args.input else None
    png = render(context, source)
    output = Path(args.output)
    temporary = output.with_suffix(".tmp")
    temporary.write_bytes(png)
    temporary.replace(output)


if __name__ == "__main__":
    main()
