#!/usr/bin/env python3
"""Device-status gray4 renderer; Render Context v1 works over HTTP or a file.

Python standard library only. Optional/unknown context fields may be omitted or
added without changing the renderer. Missing measurements display UNKNOWN.
"""
import argparse
import json
from pathlib import Path
import struct
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit
import zlib

# Tiny built-in 5x7 font keeps the example dependency-free and offline-capable.
FONT = {
    'A': '01110 10001 10001 11111 10001 10001 10001',
    'B': '11110 10001 10001 11110 10001 10001 11110',
    'C': '01111 10000 10000 10000 10000 10000 01111',
    'D': '11110 10001 10001 10001 10001 10001 11110',
    'E': '11111 10000 10000 11110 10000 10000 11111',
    'F': '11111 10000 10000 11110 10000 10000 10000',
    'G': '01111 10000 10000 10111 10001 10001 01111',
    'H': '10001 10001 10001 11111 10001 10001 10001',
    'I': '11111 00100 00100 00100 00100 00100 11111',
    'J': '00111 00010 00010 00010 10010 10010 01100',
    'K': '10001 10010 10100 11000 10100 10010 10001',
    'L': '10000 10000 10000 10000 10000 10000 11111',
    'M': '10001 11011 10101 10101 10001 10001 10001',
    'N': '10001 11001 10101 10011 10001 10001 10001',
    'O': '01110 10001 10001 10001 10001 10001 01110',
    'P': '11110 10001 10001 11110 10000 10000 10000',
    'Q': '01110 10001 10001 10001 10101 10010 01101',
    'R': '11110 10001 10001 11110 10100 10010 10001',
    'S': '01111 10000 10000 01110 00001 00001 11110',
    'T': '11111 00100 00100 00100 00100 00100 00100',
    'U': '10001 10001 10001 10001 10001 10001 01110',
    'V': '10001 10001 10001 10001 10001 01010 00100',
    'W': '10001 10001 10001 10101 10101 10101 01010',
    'X': '10001 10001 01010 00100 01010 10001 10001',
    'Y': '10001 10001 01010 00100 00100 00100 00100',
    'Z': '11111 00001 00010 00100 01000 10000 11111',
    '0': '01110 10001 10011 10101 11001 10001 01110',
    '1': '00100 01100 00100 00100 00100 00100 01110',
    '2': '01110 10001 00001 00010 00100 01000 11111',
    '3': '11110 00001 00001 01110 00001 00001 11110',
    '4': '00010 00110 01010 10010 11111 00010 00010',
    '5': '11111 10000 10000 11110 00001 00001 11110',
    '6': '01110 10000 10000 11110 10001 10001 01110',
    '7': '11111 00001 00010 00100 01000 01000 01000',
    '8': '01110 10001 10001 01110 10001 10001 01110',
    '9': '01110 10001 10001 01111 00001 00001 01110',
    '-': '00000 00000 00000 11111 00000 00000 00000',
    '.': '00000 00000 00000 00000 00000 00100 00100',
    ':': '00000 00100 00100 00000 00100 00100 00000',
    '?': '01110 10001 00001 00010 00100 00000 00100',
    ' ': '00000 00000 00000 00000 00000 00000 00000',
}


def render(context):
    target, device = context['renderTarget'], context['device']
    if context['version'] != 'v1' or not device['name'] or any(target.get(key) != value for key, value in {'width': 800, 'height': 480, 'palette': 'gray4'}.items()):
        raise ValueError('Expected Render Context v1 for 800 x 480 / gray4')
    pixels = [bytearray([3]) * 800 for _ in range(480)]

    def text(value, x, y, scale=4, shade=0):
        for letter in str(value).upper():
            if x + 5 * scale > 780:
                break
            for dy, row in enumerate(FONT.get(letter, FONT['?']).split()):
                for dx, bit in enumerate(row):
                    if bit == '1':
                        for yy in range(y + dy * scale, y + (dy + 1) * scale):
                            pixels[yy][x + dx * scale:x + (dx + 1) * scale] = bytes([shade]) * scale
            x += 6 * scale

    text('DEVICE STATUS', 32, 28, 6)
    text(device['name'], 32, 95, 4, 1)
    text('MODEL: ' + device.get('model', 'unknown'), 32, 154)
    text('FW: ' + device.get('firmware', {}).get('version', 'unknown'), 32, 208)
    voltage = device.get('battery', {}).get('voltage')
    rssi = device.get('network', {}).get('rssi')
    text('BATTERY: ' + ('UNKNOWN' if voltage is None else f'{voltage:.2f} V'), 32, 274)
    text('RSSI: ' + ('UNKNOWN' if rssi is None else f'{rssi} DBM'), 32, 340)
    # Calibration strip retains all four shades; voltage is never guessed as %.
    for row in pixels[412:448]:
        for shade in range(4):
            row[32 + shade * 184:32 + (shade + 1) * 184] = bytes([shade]) * 184
    rows = bytearray()
    for row in pixels:
        rows.append(0)  # PNG filter None
        rows.extend((row[x] << 6) | (row[x + 1] << 4) | (row[x + 2] << 2) | row[x + 3] for x in range(0, 800, 4))

    def chunk(name, data):
        return struct.pack('>I', len(data)) + name + data + struct.pack('>I', zlib.crc32(name + data))
    header = struct.pack('>IIBBBBB', 800, 480, 2, 0, 0, 0, 0)
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) + chunk(b'IDAT', zlib.compress(rows)) + chunk(b'IEND', b'')


class Renderer(BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if self.headers.get('Content-Type') != 'application/json' or not 0 < size <= 16384:
                raise ValueError('Invalid request body')
            context = json.loads(self.rfile.read(size))
            query = parse_qs(urlsplit(self.path).query)
            target = context['renderTarget']
            if any(query.get(key) != [str(target[key])] for key in ('width', 'height', 'palette')):
                raise ValueError('Query and context targets differ')
            body = render(context)
        except (ValueError, KeyError, TypeError, AttributeError):
            self.send_error(400, 'Invalid Render Context v1 or target query')
            return
        self.send_response(200)
        self.send_header('Content-Type', 'image/png')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--port', type=int, default=8000)
    parser.add_argument('--context', type=Path, help='Render a context JSON file without running an HTTP server')
    parser.add_argument('--output', type=Path, default=Path('output.png'))
    args = parser.parse_args()
    if args.context:
        args.output.write_bytes(render(json.loads(args.context.read_text())))
    else:
        ThreadingHTTPServer(('0.0.0.0', args.port), Renderer).serve_forever()
