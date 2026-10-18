#!/usr/bin/env python3
"""Pixel-exact black/white edge calibration; controller overlays stay separate."""
import argparse
from functools import lru_cache
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import struct
from urllib.parse import parse_qs, urlsplit
import zlib

# Original 3x5 glyphs keep ruler labels legible at ten-pixel intervals.
FONT = {
    '0': '111 101 101 101 111', '1': '010 110 010 010 111',
    '2': '111 001 111 100 111', '3': '111 001 111 001 111',
    '4': '101 101 111 001 001', '5': '111 100 111 001 111',
    '6': '111 100 111 101 111', '7': '111 001 010 010 010',
    '8': '111 101 111 101 111', '9': '111 101 111 001 111',
    'A': '010 101 111 101 101', 'B': '110 101 110 101 110',
    'C': '111 100 100 100 111', 'D': '110 101 101 101 110',
    'E': '111 100 110 100 111', 'F': '111 100 110 100 100',
    'G': '111 100 101 101 111', 'H': '101 101 111 101 101',
    'I': '111 010 010 010 111', 'J': '001 001 001 101 111',
    'K': '101 101 110 101 101', 'L': '100 100 100 100 111',
    'M': '101 111 111 101 101', 'N': '101 111 111 111 101',
    'O': '111 101 101 101 111', 'P': '111 101 111 100 100',
    'Q': '111 101 101 111 001', 'R': '110 101 110 101 101',
    'S': '111 100 111 001 111', 'T': '111 010 010 010 010',
    'U': '101 101 101 101 111', 'V': '101 101 101 101 010',
    'W': '101 101 111 111 101', 'X': '101 101 010 101 101',
    'Y': '101 101 010 010 010', 'Z': '111 001 010 100 111',
    '-': '000 000 111 000 000', ' ': '000 000 000 000 000',
}
FRAMES = (0, 4, 8, 12, 16, 24, 32)


def chunk(kind, body):
    return struct.pack('>I', len(body)) + kind + body + struct.pack('>I', zlib.crc32(kind + body))


@lru_cache(maxsize=16, typed=True)
def png(width, height, rotation=0):
    if (type(width) is not int or type(height) is not int or
            not 400 <= width <= 4096 or not 400 <= height <= 4096 or
            width * height > 4194304 or type(rotation) is not int or rotation not in (0, 90, 180, 270)):
        raise ValueError('Expected 400..4096 pixel dimensions, at most 4194304 pixels, and a quarter-turn rotation')
    lw, lh = (height, width) if rotation in (90, 270) else (width, height)
    pixels = [bytearray(b'\xff') * width for _ in range(height)]

    def put(x, y, value=0):
        if not (0 <= x < lw and 0 <= y < lh):
            return
        px, py = ((x, y) if rotation == 0 else (width - 1 - y, x) if rotation == 90
                  else (width - 1 - x, height - 1 - y) if rotation == 180 else (y, height - 1 - x))
        pixels[py][px] = value

    def horizontal(x, y, length):
        for xx in range(x, x + length):
            put(xx, y)

    def vertical(x, y, length):
        for yy in range(y, y + length):
            put(x, yy)

    def text(value, x, y, scale=1, badge=False):
        if badge:
            for yy in range(y - 1, y + 5 * scale + 1):
                for xx in range(x - 1, x + (4 * len(value) - 1) * scale + 1):
                    put(xx, yy, 255)
        for letter in value:
            for row, bits in enumerate(FONT[letter].split()):
                for col, bit in enumerate(bits):
                    if bit == '1':
                        for sy in range(scale):
                            for sx in range(scale):
                                put(x + col * scale + sx, y + row * scale + sy)
            x += 4 * scale

    # Distances are zero-based pixel coordinates: frame 0 is the outermost row/column.
    for distance in FRAMES:
        horizontal(distance, distance, lw - 2 * distance)
        horizontal(distance, lh - 1 - distance, lw - 2 * distance)
        vertical(distance, distance, lh - 2 * distance)
        vertical(lw - 1 - distance, distance, lh - 2 * distance)

    # Alternating minor tick lengths expose single-pixel steps without merging
    # into a solid block. Major ticks are ten pixels apart, with five-pixel marks.
    cx, cy = lw // 2, lh // 2
    for distance in range(41):
        length = 18 if distance % 10 == 0 else 12 if distance % 5 == 0 else 6 if distance % 2 == 0 else 3
        horizontal(cx - 56, distance, length)
        horizontal(cx - 56, lh - 1 - distance, length)
        vertical(distance, cy - 56, length)
        vertical(lw - 1 - distance, cy - 56, length)
        if distance and distance % 10 == 0:
            value = str(distance)
            text(value, cx - 30, distance - 2, badge=True)
            text(value, cx - 30, lh - 3 - distance, badge=True)
            text(value, distance - 3, cy - 30, badge=True)
            text(value, lw - 4 - distance, cy - 30, badge=True)

    text('TOP', cx - 11, 58, 2)
    text('BOTTOM', cx - 23, lh - 68, 2)
    text('LEFT', 58, cy - 5, 2)
    text('RIGHT', lw - 96, cy - 5, 2)
    for name, x, y in (('TL', 42, 44), ('TR', lw - 56, 44),
                       ('BL', 42, lh - 54), ('BR', lw - 56, lh - 54)):
        text(name, x, y, 2)
    # One- and two-pixel checkerboards reveal unintended scaling or dithering.
    for x, y in ((42, 62), (lw - 74, 62), (42, lh - 78), (lw - 74, lh - 78)):
        for yy in range(16):
            for xx in range(32):
                step = 1 if xx < 16 else 2
                put(x + xx, y + yy, 0 if ((xx // step + yy // step) % 2 == 0) else 255)

    lines = (
        ('EDGE CALIBRATION', 3),
        (f'{lw} X {lh} LOGICAL PX', 2),
        (f'ROTATION {rotation}', 2),
        ('FRAMES 0 4 8 12 16 24 32', 2),
        ('1 PX STEPS - 5 PX MARKS', 2),
        ('0 IS OUTERMOST PIXEL', 2),
        ('OVERLAY BY CONTROLLER', 2),
    )
    y = cy - 56
    for value, scale in lines:
        text(value, (lw - (4 * len(value) - 1) * scale) // 2, y, scale)
        y += 22 if scale == 3 else 16

    # Exact target dimensions and pure black/white samples let controller
    # normalization preserve every ruler pixel without resizing or dithering.
    rows = b''.join(b'\0' + row for row in pixels)
    header = struct.pack('>IIBBBBB', width, height, 8, 0, 0, 0, 0)
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) + chunk(b'IDAT', zlib.compress(rows, 9)) + chunk(b'IEND', b'')


class Renderer(BaseHTTPRequestHandler):
    def do_GET(self):
        path = urlsplit(self.path)
        if path.path == '/healthz':
            body = b'ok\n'
            content_type = 'text/plain'
        elif path.path == '/render':
            try:
                query = parse_qs(path.query, keep_blank_values=True)
                if any(len(query.get(key, [])) != 1 for key in ('width', 'height', 'palette')):
                    raise ValueError('Missing or repeated target')
                if query['palette'][0] not in ('gray4', 'gray16', 'spectra6'):
                    raise ValueError('Unknown palette')
                rotation = query.get('rotation', ['0'])
                if len(rotation) != 1:
                    raise ValueError('Repeated rotation')
                body = png(int(query['width'][0]), int(query['height'][0]), int(rotation[0]))
            except ValueError:
                self.send_error(400, 'Invalid calibration target')
                return
            content_type = 'image/png'
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port', type=int, default=8000)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--width', type=int, default=800)
    parser.add_argument('--height', type=int, default=480)
    parser.add_argument('--rotation', type=int, choices=(0, 90, 180, 270), default=0)
    args = parser.parse_args()
    if args.output:
        args.output.write_bytes(png(args.width, args.height, args.rotation))
    else:
        ThreadingHTTPServer(('0.0.0.0', args.port), Renderer).serve_forever()
