#!/usr/bin/env python3
"""Measure a prebuilt static probe in fresh 500m CPU / 512 MiB containers."""
import argparse
import hashlib
import json
from pathlib import Path
import platform
import subprocess


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--fixtures", type=Path, required=True)
    p.add_argument("--image", required=True, help="local image with /proc and cgroup v2")
    p.add_argument("--output", type=Path, required=True)
    args = p.parse_args()
    report = {"architecture": platform.machine(), "go": subprocess.check_output(["go", "version"], text=True).strip(),
              "cpu_limit": "500m", "memory_limit_bytes": 512 << 20,
              "probe_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "cases": []}
    for line in Path("/proc/cpuinfo").read_text().splitlines():
        if line.startswith("model name"):
            report["cpu_model"] = line.split(":", 1)[1].strip()
            break
    base = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cpus", "0.5",
            "--memory", "512m", "--memory-swap", "512m", "--mount",
            f"type=bind,src={args.binary.resolve()},dst=/probe,readonly", "--mount",
            f"type=bind,src={args.fixtures.resolve()},dst=/fixtures,readonly", "--entrypoint", "/probe",
            args.image, "-dir", "/fixtures"]
    cases = [
        ["-input", "rgba16.png", "-palette", "gray4", "-width", "800", "-height", "480"],
        ["-input", "rgba16-adam7.png"],
        ["-input", "entropy16.png"],
        ["-input", "wide16.png"],
        ["-input", "tall16.png"],
        ["-palette", "spectra6", "-width", "800", "-height", "480"],
        ["-palette", "spectra6", "-width", "1200", "-height", "1600"],
        ["-dithering", "floyd-steinberg"],
        ["-dithering", "none"],
        ["-dithering", "bayer"],
        ["-palette", "spectra6", "-width", "1200", "-height", "1600", "-dithering", "bayer"],
        ["-width", "2048", "-height", "2048", "-dithering", "bayer"],
        ["-mode", "cancel", "-stage", "decode"],
        ["-mode", "cancel", "-stage", "decode", "-cancel-after", "200ms"],
        ["-mode", "cancel", "-stage", "decode", "-cancel-after", "400ms"],
        ["-mode", "cancel", "-stage", "resize"],
        ["-mode", "cancel", "-stage", "encode"],
        ["-mode", "load"],
    ]
    for case in cases:
        print("measuring " + " ".join(case), flush=True)
        result = subprocess.run(base + case, text=True, capture_output=True, timeout=180)
        entry = {"args": case, "exit_code": result.returncode, "measurements": []}
        for line in result.stdout.splitlines():
            try:
                entry["measurements"].append(json.loads(line))
            except json.JSONDecodeError:
                entry.setdefault("observations", []).append(line)
        if result.stderr.strip():
            entry["stderr"] = result.stderr.strip()
        report["cases"].append(entry)
        args.output.write_text(json.dumps(report, indent=2) + "\n")
        print(result.stdout.strip(), flush=True)
        if result.returncode:
            raise RuntimeError(result.stderr)


if __name__ == "__main__":
    main()
