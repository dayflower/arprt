#!/usr/bin/env python3
"""Install a pinned promtool binary from its checksummed official archive."""

import hashlib
import io
import os
import platform
import re
import sys
import tarfile
from pathlib import Path
from urllib.request import urlopen


def main():
    version, destination = sys.argv[1:]
    if not re.fullmatch(r"v\d+\.\d+\.\d+", version):
        raise SystemExit("expected a pinned Prometheus version vMAJOR.MINOR.PATCH")
    system = platform.system().lower()
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(
        platform.machine()
    )
    if system not in ("linux", "darwin") or not arch:
        raise SystemExit("promtool tooling supports Linux/macOS amd64/arm64")
    archive = f"prometheus-{version[1:]}.{system}-{arch}.tar.gz"
    url = f"https://github.com/prometheus/prometheus/releases/download/{version}"
    with urlopen(url + "/sha256sums.txt", timeout=60) as response:
        checksums = dict(
            line.split()[::-1] for line in response.read().decode().splitlines()
        )
    checksum = checksums[archive]
    with urlopen(url + "/" + archive, timeout=60) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != checksum:
        raise SystemExit("promtool archive checksum mismatch")
    # Extract only the binary, without trusting archive paths or links.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as source:
        member = source.getmember(f"prometheus-{version[1:]}.{system}-{arch}/promtool")
        if not member.isfile():
            raise SystemExit("promtool binary is not a regular file")
        binary = source.extractfile(member).read()
    target = Path(destination)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_suffix(".tmp")
    temporary.write_bytes(binary)
    temporary.chmod(0o755)
    os.replace(temporary, target)


if __name__ == "__main__":
    main()
