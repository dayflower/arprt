#!/usr/bin/env python3
"""Rerun pinned firmware callbacks with PNGdec and synthetic panel memory."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import struct
import subprocess
import urllib.request
import zlib

HERE = Path(__file__).resolve().parent
SPECTRA = [(0, 0, 0), (255, 255, 255), (255, 243, 56),
           (191, 0, 0), (100, 64, 255), (67, 138, 28)]


def function(source, name, occurrence=0):
    starts = list(re.finditer(r"^(?:static )?(?:void|int|uint8_t) " + name + r"\(", source, re.M))
    start = starts[occurrence].start()
    # The reviewed functions all have explicit end markers. This preserves their
    # complete original bodies, including comments and conditional compilation.
    end = source.index("} /* " + name + "() */", start) + len("} /* " + name + "() */")
    return source[start:end]


def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))


def png(w, h, depth, palette, samples):
    raw = bytearray()
    for y in range(h):
        raw.append(0)
        row = bytearray((w * depth + 7) // 8)
        for x in range(w):
            row[x * depth // 8] |= samples(x, y) << (8 - depth - x * depth % 8)
        raw.extend(row)
    header = struct.pack(">IIBBBBB", w, h, depth, 3 if palette else 0, 0, 0, 0)
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header)
            + (chunk(b"PLTE", bytes(c for rgb in palette for c in rgb)) if palette else b"")
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


def run(work, name, w, h, depth, palette, samples, mode, plane=0):
    fixture = work / "input.png"
    fixture.write_bytes(png(w, h, depth, palette, samples))
    large_buffer = mode == 1 or (name == "seeed" and w == 1200)
    executable = name + ("-fast-host" if large_buffer else "-host")
    return subprocess.check_output([str(work / executable), str(fixture), str(mode), str(plane), str(w), str(h)])


def check(work, name):
    old = name in ("v1817", "seeed", "e1004")
    native2 = name == "seeed"
    gray = [(v, v, v) for v in (0, 85, 170, 255)]
    count = 0
    def expect(actual, expected):
        nonlocal count
        assert actual == expected, (name, count, actual[:32], expected[:32])
        count += 1
    # Actual full panel dimensions and actual PNGdec row-buffer configuration.
    for w, h in [(800, 480), (1200, 1600)]:
        for depth in (1, 2, 4, 8):
            pal = list(reversed(SPECTRA[:min(1 << depth, 6)]))
            out = run(work, name, w, h, depth, pal, lambda x, y: (x+y) % len(pal), 2)
            expect(out, bytes(SPECTRA.index(pal[(x+y) % len(pal)]) for y in range(h) for x in range(w)))
    # High indices and unused entries do not get confused with displayed colors.
    pal = [(0, 0, 0)] * 255 + [SPECTRA[3]]
    expect(run(work, name, 800, 480, 8, pal, lambda x, y: 255, 2), bytes([3]) * 384000)
    # Reordered gray palette and subsets: inspect both OG output planes.
    for depth in (2, 4, 8):
        pal = list(reversed(gray))
        for plane in (2, 3):
            out = run(work, name, 800, 480, depth, pal, lambda x, y: x % 4, 0, plane)
            expect(out, bytes([0x55 if plane == 2 else 0x33]) * (100*480))
    for pal in ([gray[0], gray[3]], [gray[3], gray[0]]):
        out = run(work, name, 800, 480, 1, pal, lambda x, y: x % 2, 0)
        inverted = (pal[0][0] > pal[1][0]) != (name in ("seeed", "e1004"))
        expect(out, bytes([0xaa if inverted else 0x55]) * (100*480))
    # The firmware counts numbers, ignores the palette and stops after row 430.
    for pal in (None, gray):
        expect(run(work, name, 800, 480, 2, pal, lambda x, y: 1+x%2, 3), b"2\n")
        expect(run(work, name, 800, 480, 2, pal,
                   lambda x, y: (0 if x%2 == 0 else 3) if y <= 430 else 1+x%2, 3), b"2\n")
        expect(run(work, name, 800, 480, 2, pal,
                   lambda x, y: (0 if x%2 == 0 else 3) if y < 479 else 1+x%2, 3), b"2\n")
    # Two-gray 2-bit OG output in the heuristic's selected single-plane mode:
    # it becomes black/white, not the intended intermediate gray pair.
    for pal in (None, gray):
        expect(run(work, name, 800, 480, 2, pal, lambda x, y: 1+x%2, 0, 4), bytes([0xff]) * (100*480))
    # Native 4-bit callback differs from the old broken low-nibble expansion.
    out = run(work, name, 800, 480, 4, None, lambda x, y: [0, 5, 10, 15][x%4], 0, 3)
    expect(out, bytes([0xee if old else 0xcc]) * (100*480))
    # E1003: indexed 1-bit ReduceBpp has no source case; buffer stays black.
    out = run(work, name, 1872, 1404, 1, [gray[3], gray[0]], lambda x, y: x%2, 1)
    expect(out, bytes(len(out)))
    # Upstream expands native 2-bit into calibrated 4-bit. Seeed keeps 2-bit.
    for pal in (None, list(reversed(gray))):
        out = run(work, name, 1872, 1404, 2, pal, lambda x, y: x%4, 1)
        if native2:
            row = bytes([0x1b]) * (1872//4)
        else:
            row = bytes([0xfa, 0x50] if pal else [0x05, 0xaf]) * (1872//4)
        expect(out[:len(row)*1404], row*1404)
    # Gray16 indexed 4/8-bit entries preserve all 16 exact shades.
    for depth in (4, 8):
        pal = [(v*17,)*3 for v in reversed(range(16))]
        out = run(work, name, 1872, 1404, depth, pal, lambda x, y: x%16, 1)
        row = bytes([0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10]) * (1872//16)
        expect(out[:len(row)*1404], row*1404)
    pal = [(17,)*3, (238,)*3]
    out = run(work, name, 1872, 1404, 2, pal, lambda x, y: x%2, 1)
    row = bytes([0xcc] if native2 else [0x1e]) * (1872//(4 if native2 else 2))
    expect(out[:len(row)*1404], row*1404)
    for depth in (1, 4):
        out = run(work, name, 1872, 1404, depth, None, lambda x, y: (x%2)*((1<<depth)-1), 1)
        row = bytes([0x55 if depth == 1 else 0x0f]) * (1872*depth//8)
        expect(out[:len(row)*1404], row*1404)
    print(f"{name}: {count} callback/buffer expectations passed", flush=True)


def check_config(work, name):
    models = ["og", "E1001", "E1002", "E1003", "E1004"]
    if name == "e1004":
        models = ["og", "E1004"]
    for model in models:
        flags = ["-DBOARD_TRMNL"] if model == "og" else ["-DBOARD_HAS_PSRAM"]
        if model != "og":
            flags.append("-DBOARD_SEEED_RETERMINAL_" + model)
        expected_model = "og" if model == "og" else "reterminal_" + model.lower()
        if name == "seeed" and model != "og":
            expected_model = "reTerminal " + model
        else:
            # These environments provide DEVICE_MODEL via platformio.ini.
            flags.append('-DDEVICE_MODEL="' + expected_model + '"')
        output = subprocess.check_output(["cpp", "-dM", *flags, str(work/name/"include/config.h")], text=True)
        macros = dict(re.findall(r"^#define (DEVICE_MODEL|MAX_IMAGE_SIZE) (.+)$", output, re.M))
        expected_limit = 90000 if model == "og" or (name == "seeed" and model != "E1004") else 750000
        assert macros["DEVICE_MODEL"] == '"' + expected_model + '"', (name, model, macros)
        assert macros["MAX_IMAGE_SIZE"] == str(expected_limit), (name, model, macros)
    print(f"{name}: {len(models)} model/budget preprocessing expectations passed", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    args = parser.parse_args()
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=True)
    manifest = json.loads((HERE / "sources.json").read_text())
    for entry in manifest:
        path = work / entry["path"]
        path.parent.mkdir(parents=True, exist_ok=True)
        if not path.exists():
            path.write_bytes(urllib.request.urlopen(entry["url"], timeout=60).read())
        if hashlib.sha256(path.read_bytes()).hexdigest() != entry["sha256"]:
            raise RuntimeError(f"source checksum mismatch: {path}")
    src = work / "pngdec/src"
    objects = []
    for c in sorted(src.glob("*.c")):
        obj = work / (c.stem + ".o")
        subprocess.run(["cc", "-O2", "-D__LINUX__", "-I"+str(src), "-c", str(c), "-o", str(obj)], check=True)
        objects.append(str(obj))
    for name in ("upstream", "v1817", "seeed", "e1004"):
        check_config(work, name)
        source = (work / name / "src/display.cpp").read_text()
        body = function(source, "ReduceBpp")
        body += source[source.index("enum {\n    PNG_1_BIT"):source.index("#ifdef BB_EPAPER", source.index("enum {\n    PNG_1_BIT"))]
        body += source[source.index("const int iSpectraRGB[]"):source.index("void CreateSpectra6Pal")]
        for f in ("CreateSpectra6Pal", "GetSpectraPixel", "png_draw_6clr"):
            body += function(source, f)
        if "static void Expand2bppLineTo4bpp" in source:
            body += function(source, "Expand2bppLineTo4bpp")
        body += function(source, "png_draw", 0).replace("int png_draw(", "int png_draw_og(", 1)
        body += function(source, "png_draw", 1).replace("int png_draw(", "int png_draw_fast(", 1)
        body += source[source.index("const uint8_t ucTwoBitFlags"):source.index("int png_draw_count")]
        body += function(source, "png_draw_count") + function(source, "png_count_colors")
        cpp = work / (name + ".cpp")
        cpp.write_text((HERE / "host/firmware.cpp").read_text().replace("// EXTRACTED_CALLBACKS", body))
        # E1003 uses 14984. Other reviewed environments use 6432; Seeed
        # common E1004 uses 14984. Compile decoder and callers consistently.
        for suffix, size in [("-host", 6432), ("-fast-host", 14984)]:
            subprocess.run(["c++", "-O2", "-std=c++17", "-D__LINUX__", f"-DPNG_MAX_BUFFERED_PIXELS={size}",
                            "-I"+str(src), str(cpp), str(src/"PNGdec.cpp"), *objects, "-o", str(work/(name+suffix))], check=True)
        check(work, name)


if __name__ == "__main__":
    main()
