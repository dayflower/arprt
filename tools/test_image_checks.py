"""Guard acceptance checks against false passes and retain example contracts."""

import importlib.util
import struct
import unittest
import zlib
from pathlib import Path

import overlay_checks
from image_checks import PALETTES, inspect
from image_inputs import maximum_png

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "palette_renderer", ROOT / "examples/renderers/http/palette/renderer.py"
)
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


def image(width, height, depth, color, rows, palette=b""):
    return renderer.assemble(width, height, depth, color, zlib.compress(rows), palette)


class OutputChecks(unittest.TestCase):
    def test_maximum_load_inputs_are_valid_complete_png_streams(self):
        for interlace in (False, True):
            png = maximum_png(interlace)
            self.assertEqual(len(png), 32 << 20)
            self.assertEqual(
                struct.unpack_from(">IIBBBBB", png, 16),
                (4096, 4096, 16, 6, 0, 0, int(interlace)),
            )
            pos, inflated, decoder = 8, 0, zlib.decompressobj()
            while pos < len(png):
                size = struct.unpack_from(">I", png, pos)[0]
                kind, body = png[pos + 4 : pos + 8], png[pos + 8 : pos + 8 + size]
                self.assertEqual(
                    zlib.crc32(kind + body),
                    struct.unpack_from(">I", png, pos + 8 + size)[0],
                )
                if kind == b"IDAT":
                    pending = body
                    while pending:
                        inflated += len(decoder.decompress(pending, 65536))
                        pending = decoder.unconsumed_tail
                pos += size + 12
            self.assertTrue(decoder.eof)
            self.assertFalse(decoder.unused_data)
            passes = (
                (
                    (0, 0, 8, 8),
                    (4, 0, 8, 8),
                    (0, 4, 4, 8),
                    (2, 0, 4, 4),
                    (0, 2, 2, 4),
                    (1, 0, 2, 2),
                    (0, 1, 1, 2),
                )
                if interlace
                else ((0, 0, 1, 1),)
            )
            expected = sum(
                ((4096 - y + dy - 1) // dy) * (1 + ((4096 - x + dx - 1) // dx) * 8)
                for x, y, dx, dy in passes
            )
            self.assertEqual(inflated, expected)
        maximum_png.cache_clear()

    def test_gray16_and_odd_widths_do_not_include_padding_samples(self):
        pixels = inspect(
            image(3, 2, 4, 0, bytes((0, 0x01, 0x2F, 0, 0xEF, 0x0F))),
            3,
            2,
            "gray16",
            750000,
            generated=True,
        )
        self.assertEqual(pixels, bytes((0, 1, 2, 14, 15, 0)))

    def test_whole_image_gray4_depth_and_migration_input(self):
        png = image(4, 2, 2, 0, bytes((0, 0x03, 0, 0x10)))
        self.assertEqual(
            inspect(png, 4, 2, "gray4", 90000, generated=True, gray4_native4=False),
            bytes((0, 0, 0, 3, 0, 1, 0, 0)),
        )
        with self.assertRaises(AssertionError):
            inspect(png, 4, 2, "gray4", 90000, gray4_native4=True)
        self.assertEqual(
            inspect(png, 4, 2, "gray4", 90000, allow_legacy_gray4=True)[-2], 0
        )
        self.assertEqual(
            inspect(
                image(4, 1, 2, 0, bytes((0, 0x33))),
                4,
                1,
                "gray4",
                90000,
                generated=True,
            ),
            bytes((0, 3, 0, 3)),
        )
        four_bit = image(4, 1, 4, 0, bytes((0, 0x05, 0xAF)))
        self.assertEqual(
            inspect(four_bit, 4, 1, "gray4", 90000, generated=True, gray4_native4=True),
            bytes((0, 1, 2, 3)),
        )
        with self.assertRaises(AssertionError):
            inspect(four_bit, 4, 1, "gray4", 90000, gray4_native4=False)

    def test_spectra_example_and_reordered_subset(self):
        for rotation in (0, 90, 180, 270):
            png = renderer.png(801, 481, "spectra6", rotation, "indexed")
            self.assertEqual(
                set(inspect(png, 801, 481, "spectra6", 750000, generated=True)),
                set(range(6)),
            )
        table = bytes((191, 0, 0, 0, 0, 0))
        self.assertEqual(
            inspect(
                image(3, 1, 1, 3, bytes((0, 0x40)), table), 3, 1, "spectra6", 750000
            ),
            bytes((3, 0, 3)),
        )
        with self.assertRaises(AssertionError):
            inspect(
                image(3, 1, 1, 3, bytes((0, 0x40)), bytes((190, 0, 0, 0, 0, 0))),
                3,
                1,
                "spectra6",
                750000,
            )

    def test_corruption_trailing_data_byte_budget_and_undeclared_index(self):
        good = renderer.png(80, 48, "spectra6", encoding="indexed")
        bad_crc = bytearray(good)
        bad_crc[-1] ^= 1
        for png, limit in [
            (bad_crc, 750000),
            (good + b"extra", 750000),
            (good[:-1], 750000),
            (good, len(good)),
        ]:
            with self.subTest(limit=limit), self.assertRaises(AssertionError):
                inspect(png, 80, 48, "spectra6", limit)
        inspect(good, 80, 48, "spectra6", len(good) + 1)
        with self.assertRaises(AssertionError):
            inspect(
                image(1, 1, 4, 3, bytes((0, 0xF0)), bytes((0, 0, 0))),
                1,
                1,
                "spectra6",
                750000,
            )

    def test_all_png_row_filters(self):
        original = [bytes((0x05, 0xAF)), bytes((0x50, 0xFA))]
        for mode in range(5):
            rows, previous = bytearray(), bytes(2)
            for row in original:
                rows.append(mode)
                for x, value in enumerate(row):
                    a, b, c = (
                        row[x - 1] if x else 0,
                        previous[x],
                        previous[x - 1] if x else 0,
                    )
                    p = a + b - c
                    predictions = (
                        0,
                        a,
                        b,
                        (a + b) // 2,
                        min((a, b, c), key=lambda v: abs(p - v)),
                    )
                    rows.append((value - predictions[mode]) & 255)
                previous = row
            self.assertEqual(
                inspect(
                    image(4, 2, 4, 0, rows), 4, 2, "gray4", 90000, gray4_native4=True
                ),
                bytes((0, 1, 2, 3, 1, 0, 3, 2)),
            )

    def test_overlay_coordinates_for_non_og_dimensions(self):
        for rotation in (0, 90, 180, 270):
            x, y = overlay_checks.physical(0, 0, rotation, 1200, 1600)
            self.assertTrue(0 <= x < 1200 and 0 <= y < 1600)
        self.assertEqual(
            overlay_checks.origin(
                "bottom-right", 40, 90, image_width=1200, image_height=1600
            ),
            (1558, 1180),
        )

    def test_profile_fixtures_and_examples_agree(self):
        spectra = (ROOT / "testdata/png/target-spectra6.png").read_bytes()
        self.assertEqual(
            spectra, renderer.png(800, 480, "spectra6", encoding="indexed")
        )
        inspect(spectra, 800, 480, "spectra6", 750000, generated=True)
        gray = (ROOT / "testdata/png/target-gray16.png").read_bytes()
        self.assertEqual(
            set(inspect(gray, 1872, 1404, "gray16", 750000, generated=True)),
            set(range(16)),
        )
        self.assertEqual(
            (ROOT / "testdata/png/input-rgb.png").read_bytes(),
            renderer.png(800, 480, "gray4"),
        )

    def test_rgb_example_requires_normalization_and_rotates_source(self):
        for rotation, expected in [(0, (257, 129)), (90, (129, 257))]:
            png = renderer.png(800, 480, "gray4", rotation)
            self.assertEqual(struct.unpack_from(">II", png, 16), expected)
            self.assertEqual(png[24:26], bytes((8, 2)))
        self.assertEqual(renderer.SPECTRA6, PALETTES["spectra6"])


if __name__ == "__main__":
    unittest.main()
