"""Synthetic allocation-bound PNGs for deployment tests; no device data."""
import argparse
from functools import lru_cache
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import struct
import zlib


def chunk(kind, body):
    return struct.pack('>I', len(body)) + kind + body + struct.pack('>I', zlib.crc32(kind + body))


@lru_cache(maxsize=2)
def maximum_png(interlace=True):
    """Maximum-area RGBA16, maximum complete-file bytes, bounded generator RAM.

    A valid private ancillary chunk fills the compressed input budget. Repeated
    semi-transparent samples force composition while keeping generation cheap.
    """
    compressor = zlib.compressobj()
    compressed = bytearray()
    passes = ((0, 0, 8, 8), (4, 0, 8, 8), (0, 4, 4, 8), (2, 0, 4, 4),
              (0, 2, 2, 4), (1, 0, 2, 2), (0, 1, 1, 2)) if interlace else ((0, 0, 1, 1),)
    for x, y, dx, dy in passes:
        width = (4096 - x + dx - 1) // dx
        row = b'\0' + struct.pack('>HHHH', 65535, 0, 0, 32896) * width
        for _ in range(y, 4096, dy):
            compressed.extend(compressor.compress(row))
    compressed.extend(compressor.flush())
    header = struct.pack('>IIBBBBB', 4096, 4096, 16, 6, 0, 0, int(interlace))
    body = b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) + chunk(b'IDAT', compressed)
    padding = (32 << 20) - len(body) - 24
    return body + chunk(b'arPt', bytes(padding)) + chunk(b'IEND', b'')


class Renderer(BaseHTTPRequestHandler):
    def do_GET(self):
        body = maximum_png('plain' not in self.path)
        self.send_response(200)
        self.send_header('Content-Type', 'image/png')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_POST(self):
        self.rfile.read(int(self.headers['Content-Length']))
        self.do_GET()

    def log_message(self, *_):
        pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    if args.output:
        args.output.write_bytes(maximum_png())
    else:
        # Prepare both files before readiness is reported by the listening socket.
        maximum_png()
        maximum_png(False)
        ThreadingHTTPServer(('0.0.0.0', 8001), Renderer).serve_forever()
