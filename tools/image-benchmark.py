#!/usr/bin/env python3
"""Run production image benchmarks in a fresh deployment-sized cgroup v2."""

import argparse
import hashlib
import json
import os
import platform
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, cwd=ROOT, **kwargs).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--image",
        default="python:3.13-alpine",
        help="local or pullable image with sh and cgroup v2",
    )
    parser.add_argument(
        "--output", type=Path, default=ROOT / "dist/acceptance/image-benchmark.json"
    )
    args = parser.parse_args()
    binary = ROOT / "bin/imageprocessing-bench"
    subprocess.run(
        ["go", "test", "-c", "-o", str(binary), "./internal/imageprocessing"],
        cwd=ROOT,
        env={**os.environ, "CGO_ENABLED": "0"},
        check=True,
    )
    script = (
        "/probe -test.run=^$ -test.bench=Maximum -test.benchtime=1x -test.benchmem; result=$?; "
        "echo RESOURCE_BEGIN; cat /sys/fs/cgroup/memory.peak; "
        "cat /sys/fs/cgroup/memory.events; cat /sys/fs/cgroup/cpu.max; "
        'cat /sys/fs/cgroup/memory.max; exit "$result"'
    )
    result = subprocess.run(
        [
            "docker",
            "run",
            "--rm",
            "--network=none",
            "--read-only",
            "--cap-drop=ALL",
            "--security-opt=no-new-privileges",
            "--cpus=0.5",
            "--memory=512m",
            "--memory-swap=512m",
            "--env",
            "GOMEMLIMIT=384MiB",
            "--mount",
            f"type=bind,src={binary},dst=/probe,readonly",
            "--entrypoint",
            "sh",
            args.image,
            "-c",
            script,
        ],
        capture_output=True,
        text=True,
        timeout=180,
    )
    report = {
        "architecture": platform.machine(),
        "source_revision": run("git", "rev-parse", "HEAD"),
        "go": run("go", "version"),
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "container_image": args.image,
        "cpu_limit": "500m",
        "memory_limit_bytes": 512 << 20,
        "go_memory_limit": "384MiB",
        "exit_code": result.returncode,
        "stdout": result.stdout,
        "stderr": result.stderr,
    }
    for line in Path("/proc/cpuinfo").read_text().splitlines():
        if line.startswith("model name"):
            report["cpu_model"] = line.split(":", 1)[1].strip()
            break
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(result.stdout, end="", flush=True)
    assert result.returncode == 0, "benchmark failed; see " + str(args.output)
    resources = result.stdout.split("RESOURCE_BEGIN\n")[1].splitlines()
    peak = int(resources[0])
    assert peak < 512 << 20 and resources[-2:] == ["50000 100000", str(512 << 20)]
    assert "oom 0" in resources and "oom_kill 0" in resources
    print("PASS: deployment-sized benchmark; record at " + str(args.output))


if __name__ == "__main__":
    main()
