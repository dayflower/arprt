#!/usr/bin/env python3
"""Minimal dependency-free, noninterlaced 2-bit gray4 PNG HTTP GET renderer."""
import argparse
import struct
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit


def png(indexed=False):
    def chunk(name, data):
        return struct.pack('>I', len(data)) + name + data + struct.pack('>I', zlib.crc32(name + data))
    header = struct.pack('>IIBBBBB', 800, 480, 2, 3 if indexed else 0, 0, 0, 0)
    # Four vertical bands: black, dark gray, light gray, white.
    row = b'\x00' + b'\x00' * 50 + b'\x55' * 50 + b'\xaa' * 50 + b'\xff' * 50
    palette = chunk(b'PLTE', bytes([0, 0, 0, 85, 85, 85, 170, 170, 170, 255, 255, 255])) if indexed else b''
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) + palette + chunk(b'IDAT', zlib.compress(row * 480)) + chunk(b'IEND', b'')


class Renderer(BaseHTTPRequestHandler):
    def do_GET(self):
        query = parse_qs(urlsplit(self.path).query)
        if (query.get('width'), query.get('height'), query.get('palette')) != (['800'], ['480'], ['gray4']):
            self.send_error(400, 'Expected 800 x 480 / gray4')
            return
        body = png(query.get('encoding') == ['indexed'])
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
