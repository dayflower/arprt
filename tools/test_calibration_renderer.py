"""Check edge distances, pixel steps, rotation and the calibration HTTP contract."""
import importlib.util
from pathlib import Path
import struct
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import urlopen

import zlib

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('calibration_renderer', ROOT / 'examples/renderers/http/calibration/renderer.py')
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class CalibrationTests(unittest.TestCase):
    def pixels(self, width=800, height=480, rotation=0):
        data = renderer.png(width, height, rotation)
        self.assertLess(len(data), 90000)
        self.assertEqual(data[:8], b'\x89PNG\r\n\x1a\n')
        self.assertEqual(struct.unpack('>IIBBBBB', data[16:29]), (width, height, 8, 0, 0, 0, 0))
        compressed = bytearray()
        pos = 8
        while pos < len(data):
            length = struct.unpack_from('>I', data, pos)[0]
            kind, body = data[pos + 4:pos + 8], data[pos + 8:pos + 8 + length]
            self.assertEqual(zlib.crc32(kind + body), struct.unpack_from('>I', data, pos + 8 + length)[0])
            if kind == b'IDAT':
                compressed.extend(body)
            pos += length + 12
        rows = zlib.decompress(compressed)
        self.assertEqual(len(rows), (width + 1) * height)
        self.assertTrue(all(rows[y * (width + 1)] == 0 for y in range(height)))
        return b''.join(rows[y * (width + 1) + 1:(y + 1) * (width + 1)] for y in range(height))

    def test_frames_and_checkerboards_follow_logical_rotation(self):
        for rotation in (0, 90, 180, 270):
            with self.subTest(rotation=rotation):
                pixels = self.pixels(rotation=rotation)
                self.assertEqual(set(pixels), {0, 255})

                def at(x, y):
                    px, py = ((x, y) if rotation == 0 else (799 - y, x) if rotation == 90
                              else (799 - x, 479 - y) if rotation == 180 else (y, 479 - x))
                    return pixels[py * 800 + px]

                lw, lh = (480, 800) if rotation in (90, 270) else (800, 480)
                for distance in (0, 4, 8, 12, 16, 24, 32):
                    self.assertEqual(at(100, distance), 0)
                    self.assertEqual(at(100, lh - 1 - distance), 0)
                    self.assertEqual(at(distance, 100), 0)
                    self.assertEqual(at(lw - 1 - distance, 100), 0)
                self.assertEqual(at(3, 3), 255)  # No renderer-authored overlay.
                self.assertEqual([at(x, 62) for x in range(42, 46)], [0, 255, 0, 255])
                self.assertEqual([at(x, 62) for x in range(58, 62)], [0, 0, 255, 255])
                self.assertEqual(at(lw // 2 - 52, 1), 255)
                self.assertEqual(at(lw // 2 - 52, 2), 0)

    def test_other_target_sizes_and_bounds(self):
        for width, height in ((640, 400), (1872, 1404), (1200, 1600)):
            with self.subTest(size=(width, height)):
                self.assertEqual(set(self.pixels(width, height)), {0, 255})
        for args in ((399, 480), (800, 0), (4097, 480), (4096, 4096), (800, 480, 45), (800, 480, True), (800, 480, False)):
            with self.subTest(args=args), self.assertRaises(ValueError):
                renderer.png(*args)

    def test_http_targets_and_health(self):
        server = renderer.ThreadingHTTPServer(('127.0.0.1', 0), renderer.Renderer)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base = 'http://127.0.0.1:' + str(server.server_port)
        try:
            with urlopen(base + '/healthz', timeout=5) as response:
                self.assertEqual(response.read(), b'ok\n')
            for palette in ('gray4', 'gray16', 'spectra6'):
                with urlopen(base + '/render?width=800&height=480&palette=' + palette, timeout=5) as response:
                    self.assertEqual(response.headers['Content-Type'], 'image/png')
                    self.assertEqual(response.read(), renderer.png(800, 480))
            for query in ('width=800&height=480', 'width=800&height=480&palette=rgb',
                          'width=800&height=480&palette=bw',
                          'width=800&width=480&height=480&palette=gray4',
                          'width=800&height=480&palette=gray4&rotation=45',
                          'width=800&height=480&palette=gray4&rotation=0&rotation=90'):
                with self.subTest(query=query), self.assertRaises(HTTPError) as failure:
                    urlopen(base + '/render?' + query, timeout=5)
                self.assertEqual(failure.exception.code, 400)
        finally:
            server.shutdown()
            thread.join()
            server.server_close()


if __name__ == '__main__':
    unittest.main()
