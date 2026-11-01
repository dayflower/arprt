#!/usr/bin/env python3
"""Run isolated smoke namespaces concurrently after CRDs are prepared."""

import argparse
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

ROOT = Path(__file__).resolve().parent
# Start the longest suite first. Every suite finishes its own cleanup on failure.
SUITES = [
    "job-smoke.py",
    "cluster-smoke.py",
    "playlist-smoke.py",
    "operations-smoke.py",
    "image-smoke.py",
]


def execute(suite, image, helm, packages):
    started = time.monotonic()
    result = subprocess.run(
        [
            sys.executable,
            str(ROOT / suite),
            "--image",
            image,
            "--helm",
            str(helm),
            "--packages",
            str(packages),
        ],
        capture_output=True,
        text=True,
    )
    return (
        suite,
        result.returncode,
        result.stdout + result.stderr,
        time.monotonic() - started,
    )


def select_suites(group):
    if group == "all":
        return SUITES
    if group not in ("jobs", "functional"):
        raise ValueError(f"Unknown smoke group: {group}")
    return [suite for suite in SUITES if (suite == "job-smoke.py") == (group == "jobs")]


def run_suites(image, jobs, helm, packages, group="all"):
    suites = select_suites(group)
    failed = False
    with ThreadPoolExecutor(max_workers=jobs) as executor:
        futures = {
            executor.submit(execute, suite, image, helm, packages): suite
            for suite in suites
        }
        for future in as_completed(futures):
            suite = futures[future]
            try:
                _, code, output, elapsed = future.result()
                print(
                    f"=== {suite}: {elapsed:.1f}s, exit {code} ===\n{output}",
                    flush=True,
                )
                failed |= code != 0
            except OSError as error:
                print(f"FAIL: {suite}: {error}", flush=True)
                failed = True
    return int(failed)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--helm", required=True)
    parser.add_argument("--packages", required=True)
    parser.add_argument("--jobs", type=int, choices=range(1, 5), default=2)
    parser.add_argument("--group", choices=("all", "jobs", "functional"), default="all")
    args = parser.parse_args()
    raise SystemExit(
        run_suites(args.image, args.jobs, args.helm, args.packages, args.group)
    )


if __name__ == "__main__":
    main()
