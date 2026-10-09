#!/usr/bin/env python3
"""Minimal dependency-free, noninterlaced 2-bit gray4 PNG HTTP GET renderer."""
import argparse
import struct
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit


def png(indexed=False, rotation=0):
    def chunk(name, data):
        return struct.pack('>I', len(data)) + name + data + struct.pack('>I', zlib.crc32(name + data))
    header = struct.pack('>IIBBBBB', 800, 480, 2, 3 if indexed else 0, 0, 0, 0)
    # Lay out four bands in logical orientation, then rotate into 800 x 480.
    if rotation not in (0, 90, 180, 270):
        raise ValueError('Expected rotation 0, 90, 180 or 270')
    rows = bytearray()
    for y in range(480):
        rows.append(0)
        for x in range(0, 800, 4):
            shades = []
            for xx in range(x, x + 4):
                shade = (xx // 200 if rotation == 0 else y // 120 if rotation == 90
                         else (799 - xx) // 200 if rotation == 180 else (479 - y) // 120)
                shades.append(shade)
            rows.append((shades[0] << 6) | (shades[1] << 4) | (shades[2] << 2) | shades[3])
    palette = chunk(b'PLTE', bytes([0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255])) if indexed else b''
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) + palette + chunk(b'IDAT', zlib.compress(rows)) + chunk(b'IEND', b'')


class Renderer(BaseHTTPRequestHandler):
    def do_GET(self):
        query = parse_qs(urlsplit(self.path).query)
        if (query.get('width'), query.get('height'), query.get('palette')) != (['800'], ['480'], ['gray4']):
            self.send_error(400, 'Expected 800 x 480 / gray4')
            return
        rotation = query.get('rotation', ['0'])
        if len(rotation) != 1 or rotation[0] not in ('0', '90', '180', '270'):
            self.send_error(400, 'Expected rotation 0, 90, 180 or 270')
            return
        body = png(query.get('encoding') == ['indexed'], int(rotation[0]))
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
    args = parser.parse_args()
    ThreadingHTTPServer(('0.0.0.0', args.port), Renderer).serve_forever()
