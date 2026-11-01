"""Exercise the synthetic renderer's JSON and image contracts without a cluster."""

import importlib.util
import json
import struct
import sys
import tempfile
import unittest
import zlib
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "cursor_renderer", ROOT / "examples/renderers/job/cursor/renderer.py"
)
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class CursorRendererTest(unittest.TestCase):
    def test_cursor_round_trip_and_result_variants(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            context, image, result = (
                root / name for name in ("context.json", "output.png", "result.json")
            )
            initial = json.loads(
                (
                    ROOT / "testdata/render-context/render-context-state-initial.json"
                ).read_text()
            )

            def run(mode, state=None):
                value = dict(initial)
                if state is not None:
                    value["rendererState"] = state
                raw = json.dumps(value)
                context.write_text(raw)
                with patch.object(
                    sys,
                    "argv",
                    [
                        "renderer",
                        "--context",
                        str(context),
                        "--output",
                        str(image),
                        "--result",
                        str(result),
                        "--mode",
                        mode,
                    ],
                ):
                    renderer.main()
                self.assertEqual(context.read_text(), raw)
                return result.read_text() if result.exists() else None

            self.assertEqual(json.loads(run("same-png"))["nextState"], {"cursor": 1})
            first = image.read_bytes()
            cursor = 9007199254740993
            self.assertEqual(
                json.loads(run("same-png", {"cursor": cursor}))["nextState"],
                {"cursor": cursor + 1},
            )
            self.assertEqual(image.read_bytes(), first)
            self.assertEqual(
                json.loads(run("same-state", {"cursor": cursor}))["nextState"],
                {"cursor": cursor},
            )
            self.assertIsNone(run("missing", {"cursor": 2}))
            self.assertEqual(json.loads(run("omit", {"cursor": 2})), {"version": "v1"})
            self.assertIsNone(json.loads(run("reset", {"cursor": 2}))["nextState"])
            for mode in ("invalid", "empty"):
                with self.assertRaises(json.JSONDecodeError):
                    json.loads(run(mode))
            self.assertEqual(json.loads(run("unknown"))["version"], "v2")
            self.assertEqual(run("duplicate").count('"nextState"'), 2)
            for mode in ("advance", "same-png"):
                run(mode, {"cursor": 1})
                data = image.read_bytes()
                self.assertEqual(data[:8], b"\x89PNG\r\n\x1a\n")
                offset, pixels = 8, b""
                while offset < len(data):
                    size = struct.unpack_from(">I", data, offset)[0]
                    kind, body = (
                        data[offset + 4 : offset + 8],
                        data[offset + 8 : offset + 8 + size],
                    )
                    self.assertEqual(
                        struct.unpack_from(">I", data, offset + 8 + size)[0],
                        zlib.crc32(kind + body),
                    )
                    if kind == b"IHDR":
                        self.assertEqual(
                            struct.unpack(">IIBBBBB", body), (800, 480, 2, 0, 0, 0, 0)
                        )
                    elif kind == b"IDAT":
                        pixels += body
                    offset += size + 12
                decoded = zlib.decompress(pixels)
                self.assertEqual(len(decoded), 480 * 201)
                self.assertEqual(set(decoded[1:201]), {85 if mode == "advance" else 0})


if __name__ == "__main__":
    unittest.main()
