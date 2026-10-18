"""Conservative change selection for documentation and software CI."""

import subprocess


def changed_paths(base, head):
    # Disable rename detection so both removed and added paths are considered.
    result = subprocess.check_output(
        ["git", "diff", "--no-renames", "--name-only", "-z", base, head]
    )
    return [path.decode() for path in result.split(b"\0") if path]


def documentation(path):
    return path in ("README.md", "AGENTS.md") or (
        path.startswith("notes/") and path.endswith(".md")
    )
