"""Check photo conversion and download boundaries without external services."""

import io
import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import MagicMock, patch
from urllib.parse import parse_qs, urlsplit

from PIL import Image, ImageStat

import renderer


class RendererTests(unittest.TestCase):
    def context(self, rotation=0):
        return {
            "version": "v1",
            "renderTarget": {"width": 800, "height": 480, "palette": "gray4", "rotation": rotation},
            "device": {"name": "example", "battery": {"voltage": 3.91}},
        }

    def photo(self):
        image = Image.new("L", (800, 480))
        for shade, bounds in zip((0, 85, 170, 255), (
            (0, 0, 400, 240), (400, 0, 800, 240),
            (0, 240, 400, 480), (400, 240, 800, 480),
        )):
            image.paste(shade, bounds)
        output = io.BytesIO()
        image.save(output, format="PNG")
        return output.getvalue()

    def assert_png(self, png, shades=(0, 85, 170, 255)):
        self.assertLess(len(png), 90_000)
        self.assertEqual(png[:8], b"\x89PNG\r\n\x1a\n")
        self.assertEqual(struct.unpack(">IIBBBBB", png[16:29]), (800, 480, 2, 0, 0, 0, 0))
        with Image.open(io.BytesIO(png)) as image:
            image.load()
            self.assertEqual(image.size, (800, 480))
            gray = image.convert("L")
            self.assertEqual({shade for _, shade in gray.getcolors()}, set(shades))
            return gray

    def test_gray4_contract_and_clockwise_rotation(self):
        expected = {
            0: (0, 85, 170, 255), 90: (170, 0, 255, 85),
            180: (255, 170, 85, 0), 270: (85, 255, 0, 170),
        }
        for rotation, corners in expected.items():
            with self.subTest(rotation=rotation):
                image = self.assert_png(renderer.render(self.context(rotation), self.photo()))
                self.assertEqual(tuple(image.getpixel(point) for point in (
                    (100, 60), (700, 60), (100, 420), (700, 420),
                )), corners)

    def test_omitted_rotation_and_unknown_fields(self):
        context = self.context()
        del context["renderTarget"]["rotation"]
        context["futureField"] = {"ignored": True}
        self.assert_png(renderer.render(context, self.photo()))

    def test_bw_contract_dithering_and_clockwise_rotation(self):
        expected = {
            0: (0, 85, 170, 255), 90: (170, 0, 255, 85),
            180: (255, 170, 85, 0), 270: (85, 255, 0, 170),
        }
        for rotation, corners in expected.items():
            with self.subTest(rotation=rotation):
                image = self.assert_png(renderer.render(self.context(rotation), self.photo(), palette="bw"),
                                        shades=(0, 255))
                for (x, y), shade in zip(((100, 60), (700, 60), (100, 420), (700, 420)), corners):
                    mean = ImageStat.Stat(image.crop((x - 20, y - 20, x + 20, y + 20))).mean[0]
                    self.assertAlmostEqual(mean, shade, delta=5)

    def test_bw_cli_with_local_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            context, source, output = (root / name for name in ("context.json", "photo.png", "output.png"))
            context.write_text(json.dumps(self.context()))
            source.write_bytes(self.photo())
            result = subprocess.run([
                sys.executable, renderer.__file__, "--palette", "bw",
                "--context", str(context), "--input", str(source), "--output", str(output),
            ], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assert_png(output.read_bytes(), shades=(0, 255))
            self.assertFalse(output.with_suffix(".tmp").exists())

    def test_incompatible_targets_fail_before_download(self):
        for key, value in (("width", 480), ("height", 800), ("palette", "rgb"), ("rotation", 45), ("rotation", False)):
            with self.subTest(key=key, value=value), patch.object(renderer.urllib.request, "urlopen") as fetch:
                context = self.context()
                context["renderTarget"][key] = value
                with self.assertRaises(ValueError):
                    renderer.render(context)
                fetch.assert_not_called()

    def test_download_is_bounded_random_and_has_no_device_data(self):
        response = MagicMock()
        response.__enter__.return_value = response
        response.read.return_value = self.photo()
        response.headers = {"Picsum-ID": "123"}
        with patch.object(renderer.urllib.request, "urlopen", return_value=response) as fetch:
            for _ in range(2):
                self.assert_png(renderer.render(self.context(90)))
        requests = [call.args[0] for call in fetch.call_args_list]
        self.assertNotEqual(requests[0].full_url, requests[1].full_url)
        for request in requests:
            url = urlsplit(request.full_url)
            self.assertEqual((url.scheme, url.netloc, url.path), ("https", "picsum.photos", "/480/800.jpg"))
            self.assertEqual(set(parse_qs(url.query)), {"random"})
            self.assertIsNone(request.data)
            self.assertEqual(request.get_method(), "GET")
        response.read.assert_called_with(8_000_001)

    def test_failed_download_has_finite_retries(self):
        with patch.object(renderer.urllib.request, "urlopen", side_effect=OSError("offline")) as fetch, \
                patch.object(renderer.time, "sleep"):
            with self.assertRaisesRegex(OSError, "offline"):
                renderer.render(self.context())
        self.assertEqual(fetch.call_count, 3)

    def test_oversized_download_is_rejected(self):
        response = MagicMock()
        response.__enter__.return_value = response
        response.read.return_value = b"x" * 8_000_001
        with patch.object(renderer.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(ValueError, "size limit"):
                renderer.render(self.context())


if __name__ == "__main__":
    unittest.main()
