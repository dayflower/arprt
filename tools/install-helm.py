#!/usr/bin/env python3
"""Install a pinned Helm binary from its checksummed official archive."""
import hashlib
import io
import os
from pathlib import Path
import platform
import re
import sys
import tarfile
from urllib.request import urlopen


def main():
    version, destination = sys.argv[1:]
    if not re.fullmatch(r'v\d+\.\d+\.\d+', version):
        raise SystemExit('expected a pinned Helm version vMAJOR.MINOR.PATCH')
    system = platform.system().lower()
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    if system not in ('linux', 'darwin') or not arch:
        raise SystemExit('Helm tooling supports Linux/macOS amd64/arm64')
    archive = f'helm-{version}-{system}-{arch}.tar.gz'
    url = f'https://get.helm.sh/{archive}'
    with urlopen(url + '.sha256sum', timeout=60) as response:
        checksum = response.read().decode().split()[0]
    with urlopen(url, timeout=60) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != checksum:
        raise SystemExit('Helm archive checksum mismatch')
    # Extract only the binary, without trusting archive paths or links.
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as source:
        member = source.getmember(f'{system}-{arch}/helm')
        if not member.isfile():
            raise SystemExit('Helm binary is not a regular file')
        binary = source.extractfile(member).read()
    target = Path(destination)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_suffix('.tmp')
    temporary.write_bytes(binary)
    temporary.chmod(0o755)
    os.replace(temporary, target)


if __name__ == '__main__':
    main()
