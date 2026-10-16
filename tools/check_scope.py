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
    # Historical names remain eligible because baseline comparisons include
    # deleted paths and deliberately disable rename detection.
    if documentation(path) or path.startswith(('.github/', 'tools/', 'hack/', 'charts/',
                                               'examples/networking/envoy-gateway/', 'examples/envoy-gateway/')):
        return True
    return bool(parts and parts[0] in ('api', 'cmd', 'internal') and path.endswith('_test.go'))


def physical_changes(base, head):
    return [path for path in changed_paths(base, head) if not physical_reuse_allowed(path)]
