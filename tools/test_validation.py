"""Check fail-closed change selection, physical reuse and smoke orchestration."""
from contextlib import chdir, redirect_stdout
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

from check_scope import physical_reuse_allowed

ROOT = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ci = load('ci_scope', 'ci-scope.py')
release = load('physical_release', 'release-image.py')
runner = load('smoke_runner', 'run-cluster-smoke.py')
docs = load('doc_checks', 'check-docs.py')


class RepositoryTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.cwd = chdir(self.directory.name)
        self.cwd.__enter__()
        self.addCleanup(self.cwd.__exit__, None, None, None)
        self.git('init', '-b', 'main')
        self.git('config', 'user.name', 'Validation test')
        self.git('config', 'user.email', 'validation@example.com')
        self.write('README.md', '# Guide\n\nProse.\n\n```sh\nmake build\n```\n')
        self.write('internal/controller.go', 'package internal\n')
        self.base = self.commit()
        self.full_tag('v1.0.0', self.base)

    def git(self, *args):
        return subprocess.check_output(['git', *args], text=True, stderr=subprocess.DEVNULL).strip()

    def write(self, filename, contents):
        path = Path(filename)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-m', 'fixture')
        revision = self.git('rev-parse', 'HEAD')
        self.git('update-ref', 'refs/remotes/origin/main', revision)
        return revision

    def message(self, revision):
        return f'Physical-device-acceptance: {revision}\nPhysical-device-date: 2026-10-08\nPhysical-device-scope: {release.SCOPE}\n'

    def full_tag(self, tag, revision):
        self.git('tag', '-a', tag, '-m', self.message(revision))

    def reuse_tag(self, tag, revision, baseline='v1.0.0'):
        self.git('tag', '-a', tag, '-m', self.message(revision) + f'Physical-device-mode: reuse\nPhysical-device-baseline: {baseline}\nPhysical-device-installation: passed\n')


class ChangeSelectionTests(RepositoryTest):
    def test_only_known_prose_skips_software(self):
        self.write('README.md', Path('README.md').read_text().replace('Prose.', 'Revised prose.'))
        head = self.commit()
        self.assertFalse(ci.software_required(self.base, head))
        self.write('notes/guide.md', '# New prose\n')
        head = self.commit()
        self.assertFalse(ci.software_required(self.base, head))
        self.write('new-unknown-file', 'anything')
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_example_body_changes_run_software(self):
        self.write('README.md', Path('README.md').read_text().replace('make build', 'make integration'))
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_indented_examples_run_software(self):
        self.write('README.md', Path('README.md').read_text() + '\n    make integration\n')
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_renamed_document_cannot_hide_runtime_input(self):
        Path('internal/input.txt').parent.mkdir(exist_ok=True)
        self.git('mv', 'README.md', 'internal/input.txt')
        head = self.commit()
        self.assertTrue(ci.software_required(self.base, head))
        self.assertEqual(release.physical_scope('v1.0.0', head)['mode'], 'full')

    def test_missing_history_falls_back_to_full_software(self):
        output = Path(self.directory.name) / 'output'
        result = subprocess.run(['python3', str(ROOT / 'ci-scope.py'), '0' * 40, self.base],
                                env={**ci.os.environ, 'GITHUB_OUTPUT': str(output)}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(output.read_text(), 'software=true\n')


class PhysicalReuseTests(RepositoryTest):
    def test_tooling_and_gateway_renames_preserve_reuse_scope(self):
        paths = [('hack/helper.py', 'tools/helper.py'),
                 ('examples/envoy-gateway/httproute.yaml', 'examples/networking/envoy-gateway/httproute.yaml')]
        for old, _ in paths:
            self.write(old, 'synthetic installation helper\n')
        baseline = self.commit()
        self.full_tag('v1.1.0', baseline)
        for old, new in paths:
            Path(new).parent.mkdir(parents=True, exist_ok=True)
            self.git('mv', old, new)
        head = self.commit()
        self.assertEqual(release.physical_scope('v1.1.0', head)['mode'], 'reuse')
        self.assertTrue(ci.software_required(baseline, head))

    def test_installation_and_tests_can_reuse_full_baseline(self):
        self.write('charts/arprt/templates/deployment.yaml', 'synthetic deployment\n')
        self.write('internal/controller_test.go', 'package internal\n')
        head = self.commit()
        self.assertEqual(release.physical_scope('v1.0.0', head)['mode'], 'reuse')
        self.reuse_tag('v1.0.1', head)
        with redirect_stdout(io.StringIO()):
            release.guard('v1.0.1')

    def test_runtime_change_rejects_reuse(self):
        self.write('internal/controller.go', 'package internal\n// changed\n')
        head = self.commit()
        self.reuse_tag('v1.0.1', head)
        with self.assertRaisesRegex(ValueError, 'require full'):
            release.guard('v1.0.1')

    def test_runtime_deletion_also_requires_full_regression(self):
        Path('internal/controller.go').unlink()
        head = self.commit()
        self.assertEqual(release.physical_scope('v1.0.0', head)['physicalChanges'], ['internal/controller.go'])

    def test_baseline_must_be_annotated_and_accepted(self):
        self.git('tag', 'v0.9.0')
        with self.assertRaisesRegex(ValueError, 'annotated'):
            release.physical_scope('v0.9.0', self.base)
        self.git('tag', '-a', 'v0.8.0', '-m', 'no physical evidence')
        with self.assertRaises(ValueError):
            release.physical_scope('v0.8.0', self.base)

    def test_current_installation_date_cannot_predate_baseline(self):
        self.write('notes/guide.md', '# Prose\n')
        head = self.commit()
        message = self.message(head).replace('2026-10-08', '2026-10-07')
        message += 'Physical-device-mode: reuse\nPhysical-device-baseline: v1.0.0\nPhysical-device-installation: passed\n'
        self.git('tag', '-a', 'v1.0.1', '-m', message)
        with self.assertRaisesRegex(ValueError, 'predate'):
            release.guard('v1.0.1')

    def test_missing_installation_attestation_and_reuse_chains_rejected(self):
        self.write('notes/guide.md', '# Prose\n')
        head = self.commit()
        message = self.message(head) + 'Physical-device-mode: reuse\nPhysical-device-baseline: v1.0.0\n'
        with self.assertRaises(ValueError):
            release.validate_tag('v1.0.1', head, message)
        self.reuse_tag('v1.0.1', head)
        with self.assertRaisesRegex(ValueError, 'not another reuse'):
            release.physical_scope('v1.0.1', head)
        for suffix in ['Physical-device-mode: mystery\n', 'Physical-device-mode: full\nPhysical-device-mode: full\n', 'Physical-device-baseline: v1.0.0\n']:
            with self.assertRaises(ValueError):
                release.validate_tag('v1.0.1', head, self.message(head) + suffix)

    def test_runtime_inputs_and_unknown_paths_fail_closed(self):
        for filename in ['Dockerfile', 'Makefile', 'go.mod', 'go.sum', 'api/v1alpha1/types.go',
                         'config/crd/bases/device.yaml', 'internal/overlay/assets.go',
                         'examples/renderers/http/gray-bands/renderer.py',
                         'examples/renderers/http/device-status/renderer.py',
                         'examples/renderers/job/gray-bands/screens.yaml',
                         'examples/renderers/job/picsum/renderer.py',
                         'examples/custom-resources/device.yaml',
                         'examples/networking/unknown/route.yaml',
                         'testdata/png/target-gray4.png',
                         'testdata/render-context/render-context-v1.json',
                         'testdata/protocol/display-response.json', 'unknown/file']:
            with self.subTest(filename=filename):
                self.assertFalse(physical_reuse_allowed(filename))


class SmokeRunnerTests(unittest.TestCase):
    def test_failure_does_not_skip_other_suites(self):
        with tempfile.TemporaryDirectory() as directory:
            scripts = ['ok.py', 'failed.py', 'also-ok.py']
            for name in scripts:
                code = 1 if name == 'failed.py' else 0
                Path(directory, name).write_text(f"from pathlib import Path\nPath({str(Path(directory, name + '.ran'))!r}).touch()\nraise SystemExit({code})\n")
            with patch.object(runner, 'ROOT', Path(directory)), patch.object(runner, 'SUITES', scripts), redirect_stdout(io.StringIO()):
                self.assertEqual(runner.run_suites('arprt:test', 2, '/tools/helm', '/packages'), 1)
            self.assertTrue(all(Path(directory, name + '.ran').exists() for name in scripts))

    def test_two_suites_overlap_without_exceeding_limit(self):
        barrier, lock = threading.Barrier(2), threading.Lock()
        active, peak, completed = 0, 0, []
        def execute(name, image, helm, packages):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            barrier.wait(timeout=5)
            with lock:
                active -= 1
                completed.append(name)
            return name, 0, '', 0
        with patch.object(runner, 'execute', side_effect=execute), redirect_stdout(io.StringIO()):
            self.assertEqual(runner.run_suites('arprt:test', 2, '/tools/helm', '/packages'), 0)
        self.assertEqual(peak, 2)
        self.assertCountEqual(completed, runner.SUITES)


class DocumentationTests(unittest.TestCase):
    def test_duplicate_headings_and_fenced_examples(self):
        source = '# Guide\n```sh\n# Not a heading\n```\n## Same\n## Same\n'
        self.assertEqual(docs.headings(source), {'guide', 'same', 'same-1'})
        source = '# Guide\n````markdown\n```sh\n# Not a heading\n```\n````\n## Visible\n'
        self.assertEqual(docs.headings(source), {'guide', 'visible'})


if __name__ == '__main__':
    unittest.main()
