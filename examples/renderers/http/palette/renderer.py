#!/usr/bin/env python3
"""Dependency-free RGB normalization and indexed Spectra 6 renderer examples."""
import argparse
from functools import lru_cache
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import struct
from urllib.parse import parse_qs, urlsplit
import zlib

SPECTRA6 = ((0, 0, 0), (255, 255, 255), (255, 243, 56),
            (191, 0, 0), (100, 64, 255), (67, 138, 28))


def chunk(kind, body):
    return struct.pack('>I', len(body)) + kind + body + struct.pack('>I', zlib.crc32(kind + body))


def assemble(width, height, depth, color, compressed, palette=b'', interlace=0):
    header = struct.pack('>IIBBBBB', width, height, depth, color, 0, 0, interlace)
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', header) +
            (chunk(b'PLTE', palette) if palette else b'') +
            chunk(b'IDAT', compressed) + chunk(b'IEND', b''))


@lru_cache(maxsize=16)
def png(width, height, palette, rotation=0, encoding='rgb'):
    assert 0 < width <= 4096 and 0 < height <= 4096 and width * height <= 4194304
    assert rotation in (0, 90, 180, 270)
    assert encoding in ('rgb', 'indexed')
    if encoding == 'indexed' and palette != 'spectra6':
        raise ValueError('Indexed example requires a Spectra 6 target')
    # RGB intentionally uses a small, differently shaped canvas. The controller
    # composes/resizes/quantizes it; indexed output already matches the target.
    w, h = (257, 129) if encoding == 'rgb' else (width, height)
    if encoding == 'rgb' and rotation in (90, 270):
        w, h = h, w
    rows = bytearray()
    for y in range(h):
        rows.append(0)
        indices = []
        for x in range(w):
            lx, span = ((x, w) if rotation == 0 else (y, h) if rotation == 90
                        else (w - 1 - x, w) if rotation == 180 else (h - 1 - y, h))
            if encoding == 'rgb':
                # Smooth RGB samples deliberately include off-palette colors.
                rows.extend((lx * 255 // max(1, span - 1), 96, 255 - lx * 255 // max(1, span - 1)))
            else:
                indices.append(min(5, lx * 6 // span))
        if encoding == 'indexed':
            rows.extend((indices[x] << 4) | (indices[x + 1] if x + 1 < w else 0)
                        for x in range(0, w, 2))
    return assemble(w, h, 8 if encoding == 'rgb' else 4, 2 if encoding == 'rgb' else 3,
                    zlib.compress(rows), bytes(v for c in SPECTRA6 for v in c) if encoding == 'indexed' else b'')


class Renderer(BaseHTTPRequestHandler):
    def render(self, context=None):
        query = parse_qs(urlsplit(self.path).query)
        target = {key: query[key][0] for key in ('width', 'height', 'palette')}
        target['rotation'] = int(query.get('rotation', ['0'])[0])
        if context is not None:
            observed = context['renderTarget']
            assert all(str(observed[key]) == target[key] for key in ('width', 'height', 'palette'))
            assert observed.get('rotation', 0) == target['rotation']
        body = png(int(target['width']), int(target['height']), target['palette'], target['rotation'],
                   query.get('encoding', ['rgb'])[0])
        self.send_response(200)
        self.send_header('Content-Type', 'image/png')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.render()

    def do_POST(self):
        length = int(self.headers['Content-Length'])
        assert 0 < length <= 65536
        self.render(json.loads(self.rfile.read(length)))

    def log_message(self, *_):
        pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port', type=int, default=8000)
    parser.add_argument('--context', type=Path)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--encoding', choices=('rgb', 'indexed'), default='rgb')
    args = parser.parse_args()
    if args.context and args.output:
        target = json.loads(args.context.read_text())['renderTarget']
        args.output.write_bytes(png(target['width'], target['height'], target['palette'],
                                    target.get('rotation', 0), args.encoding))
    else:
        ThreadingHTTPServer(('0.0.0.0', args.port), Renderer).serve_forever()
