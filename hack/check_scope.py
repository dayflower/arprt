"""Conservative change scopes shared by CI and physical acceptance gates."""
from pathlib import PurePosixPath
import subprocess


def changed_paths(base, head):
    # Disable rename detection so both removed and added paths are considered.
    result = subprocess.check_output(['git', 'diff', '--no-renames', '--name-only', '-z', base, head])
    return [path.decode() for path in result.split(b'\0') if path]


def documentation(path):
    return path in ('README.md', 'AGENTS.md') or (path.startswith('notes/') and path.endswith('.md'))


def physical_reuse_allowed(path):
    """Installation changes still require current-image deployment/TLS checks."""
    parts = PurePosixPath(path).parts
    if documentation(path) or path.startswith(('.github/', 'hack/', 'charts/', 'config/default/', 'config/overlays/')):
        return True
    return bool(parts and parts[0] in ('api', 'cmd', 'internal') and path.endswith('_test.go'))


def physical_changes(base, head):
    return [path for path in changed_paths(base, head) if not physical_reuse_allowed(path)]
