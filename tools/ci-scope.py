#!/usr/bin/env python3
"""Run full CI unless only known documentation prose changed."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

from check_scope import changed_paths, documentation


def code_blocks(source):
    blocks, current, fence = [], [], None
    for line in source.splitlines():
        marker = re.match(r'^\s*(`{3,}|~{3,})(.*)$', line)
        if fence is None and marker:
            fence = marker[1]
            current = [line]
        elif fence is None and re.match(r'^( {4}|\t)', line):
            # Indented executable examples also require software checks.
            blocks.append('indented:' + line)
        elif fence is not None:
            current.append(line)
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                blocks.append('\n'.join(current))
                fence = None
    if fence is not None:
        blocks.append('\n'.join(current))
    return blocks


def file_at(revision, path):
    result = subprocess.run(['git', 'show', revision + ':' + path], capture_output=True, text=True)
    return result.stdout if result.returncode == 0 else ''


def software_required(base, head):
    paths = changed_paths(base, head)
    if not paths or not all(documentation(path) for path in paths):
        return True
    return any(code_blocks(file_at(base, path)) != code_blocks(file_at(head, path)) for path in paths)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('base')
    parser.add_argument('head')
    args = parser.parse_args()
    try:
        required = software_required(args.base, args.head)
    except (OSError, subprocess.CalledProcessError, UnicodeError):
        # Missing history or an unknown change never suppresses software checks.
        required = True
    value = 'true' if required else 'false'
    if os.environ.get('GITHUB_OUTPUT'):
        with Path(os.environ['GITHUB_OUTPUT']).open('a') as stream:
            stream.write('software=' + value + '\n')
    print(json.dumps({'software': required}))


if __name__ == '__main__':
    main()
