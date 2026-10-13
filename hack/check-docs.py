#!/usr/bin/env python3
"""Check tracked Markdown local links and heading anchors without a build."""
import os
import re
import subprocess
from pathlib import Path
from urllib.parse import unquote, urlsplit


def headings(source):
    result, counts, fence = set(), {}, None
    for line in source.splitlines():
        marker = re.match(r'^\s*(`{3,}|~{3,})(.*)$', line)
        if marker and fence is None:
            fence = marker[1]
            continue
        if fence is not None:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                fence = None
            continue
        match = re.match(r'^#{1,6}\s+(.+?)\s*#*$', line)
        if not match:
            continue
        name = re.sub(r'[^\w\- ]', '', match[1].lower()).replace(' ', '-')
        count = counts.get(name, 0)
        counts[name] = count + 1
        result.add(name + ('-' + str(count) if count else ''))
    return result


def main():
    base = os.environ.get('DOC_BASE_REVISION')
    if base and subprocess.run(['git', 'cat-file', '-e', base + '^{commit}'], capture_output=True).returncode == 0:
        subprocess.run(['git', 'diff', '--check', base, 'HEAD'], check=True)
    else:
        subprocess.run(['git', 'show', '--check', '--format=', 'HEAD'], check=True)
    subprocess.run(['git', 'diff', '--check'], check=True)
    paths = subprocess.check_output(['git', 'ls-files', '-z', '*.md']).decode().split('\0')
    errors = []
    for filename in filter(None, paths):
        path = Path(filename)
        if not path.exists():
            continue
        source = path.read_text()
        for target in re.findall(r'\]\(<?([^\s)>]+)>?(?:\s+"[^"]*")?\)', source):
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc:
                continue
            destination = path.parent / unquote(parsed.path) if parsed.path else path
            if not destination.exists():
                errors.append(f'{filename}: missing local link {target}')
            elif parsed.fragment and destination.suffix == '.md' and unquote(parsed.fragment) not in headings(destination.read_text()):
                errors.append(f'{filename}: missing heading {target}')
    if errors:
        raise SystemExit('\n'.join(errors))
    print('PASS: tracked Markdown local links and anchors')


if __name__ == '__main__':
    main()
