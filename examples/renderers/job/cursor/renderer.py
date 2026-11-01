#!/usr/bin/env python3
"""Synthetic Job renderer: consume a committed cursor and propose its successor.

Only the controller commits the proposal. This program has no API credentials,
network dependencies, or persistent storage beyond its mounted output directory.
"""

import argparse
import json
import struct
import time
import zlib
from pathlib import Path


def png(width, height, shade):
    def chunk(kind, data):
        return (
            struct.pack(">I", len(data))
            + kind
            + data
            + struct.pack(">I", zlib.crc32(kind + data))
        )

    # Two-bit grayscale uses only canonical gray4 values (0, 85, 170, 255).
    row = bytes([shade * 85]) * ((width + 3) // 4)
    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 2, 0, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress((b"\0" + row) * height))
        + chunk(b"IEND", b"")
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", default="/arprt/context.json", type=Path)
    parser.add_argument("--output", default="/arprt/output.png", type=Path)
    parser.add_argument("--result", default="/arprt/result.json", type=Path)
    parser.add_argument(
        "--mode",
        choices=(
            "advance",
            "same-png",
            "same-state",
            "missing",
            "omit",
            "reset",
            "invalid",
            "unknown",
            "duplicate",
            "empty",
        ),
        default="same-png",
    )
    parser.add_argument("--delay", type=float, default=0)
    args = parser.parse_args()
    context = json.loads(args.context.read_text())
    state = context.get("rendererState", {})
    cursor = state.get("cursor", 0)
    if type(cursor) is not int or cursor < 0:
        raise ValueError("cursor must be a nonnegative integer")
    target = context["renderTarget"]
    if target["palette"] != "gray4":
        raise ValueError("the synthetic renderer requires a gray4 target")
    width, height = target["width"], target["height"]
    if target.get("rotation", 0) in (90, 270):
        width, height = height, width
    shade = cursor % 4 if args.mode == "advance" else 0
    args.output.write_bytes(png(width, height, shade))
    # Delay after writing the image exercises incomplete image/result collection.
    time.sleep(args.delay)
    envelope = {"version": "v1", "nextState": {"cursor": cursor + 1}}
    if args.mode == "missing":
        args.result.unlink(missing_ok=True)
        return
    if args.mode == "omit":
        envelope.pop("nextState")
    elif args.mode == "reset":
        envelope["nextState"] = None
    elif args.mode == "same-state":
        if "rendererState" in context:
            envelope["nextState"] = state
        else:
            envelope.pop("nextState")
    elif args.mode == "unknown":
        envelope["version"] = "v2"
    raw = json.dumps(envelope, separators=(",", ":"))
    if args.mode == "invalid":
        raw = "{"
    elif args.mode == "duplicate":
        raw = '{"version":"v1","nextState":0,"nextState":1}'
    elif args.mode == "empty":
        raw = ""
    args.result.write_text(raw)


if __name__ == "__main__":
    main()
