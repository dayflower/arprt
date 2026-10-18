"""Check fail-closed change selection and smoke orchestration."""

import importlib.util
import io
import subprocess
import tempfile
import threading
import unittest
from contextlib import chdir, redirect_stdout
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ci = load("ci_scope", "ci-scope.py")
runner = load("smoke_runner", "run-cluster-smoke.py")
docs = load("doc_checks", "check-docs.py")


class RepositoryTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.cwd = chdir(self.directory.name)
        self.cwd.__enter__()
        self.addCleanup(self.cwd.__exit__, None, None, None)
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Validation test")
        self.git("config", "user.email", "validation@example.com")
        self.write("README.md", "# Guide\n\nProse.\n\n```sh\nmake build\n```\n")
        self.write("internal/controller.go", "package internal\n")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.check_output(
            ["git", *args], text=True, stderr=subprocess.DEVNULL
        ).strip()

    def write(self, filename, contents):
        path = Path(filename)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-m", "fixture")
        revision = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", revision)
        return revision


class ChangeSelectionTests(RepositoryTest):
    def test_only_known_prose_skips_software(self):
        self.write(
            "README.md",
            Path("README.md").read_text().replace("Prose.", "Revised prose."),
        )
        head = self.commit()
        self.assertFalse(ci.software_required(self.base, head))
        self.write("notes/guide.md", "# New prose\n")
        head = self.commit()
        self.assertFalse(ci.software_required(self.base, head))
        self.write("new-unknown-file", "anything")
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_example_body_changes_run_software(self):
        self.write(
            "README.md",
            Path("README.md").read_text().replace("make build", "make integration"),
        )
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_indented_examples_run_software(self):
        self.write(
            "README.md", Path("README.md").read_text() + "\n    make integration\n"
        )
        self.assertTrue(ci.software_required(self.base, self.commit()))

    def test_renamed_document_cannot_hide_runtime_input(self):
        Path("internal/input.txt").parent.mkdir(exist_ok=True)
        self.git("mv", "README.md", "internal/input.txt")
        head = self.commit()
        self.assertTrue(ci.software_required(self.base, head))

    def test_missing_history_falls_back_to_full_software(self):
        output = Path(self.directory.name) / "output"
        result = subprocess.run(
            ["python3", str(ROOT / "ci-scope.py"), "0" * 40, self.base],
            env={**ci.os.environ, "GITHUB_OUTPUT": str(output)},
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(output.read_text(), "software=true\n")


class SmokeRunnerTests(unittest.TestCase):
    def test_failure_does_not_skip_other_suites(self):
        with tempfile.TemporaryDirectory() as directory:
            scripts = ["ok.py", "failed.py", "also-ok.py"]
            for name in scripts:
                code = 1 if name == "failed.py" else 0
                Path(directory, name).write_text(
                    f"from pathlib import Path\nPath({str(Path(directory, name + '.ran'))!r}).touch()\nraise SystemExit({code})\n"
                )
            with (
                patch.object(runner, "ROOT", Path(directory)),
                patch.object(runner, "SUITES", scripts),
                redirect_stdout(io.StringIO()),
            ):
                self.assertEqual(
                    runner.run_suites("arprt:test", 2, "/tools/helm", "/packages"), 1
                )
            self.assertTrue(
                all(Path(directory, name + ".ran").exists() for name in scripts)
            )

    def test_two_suites_overlap_without_exceeding_limit(self):
        barrier, lock = threading.Barrier(2), threading.Lock()
        active, peak, completed = 0, 0, []

        def execute(name, image, helm, packages):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            if name in runner.SUITES[:2]:
                barrier.wait(timeout=5)
            with lock:
                active -= 1
                completed.append(name)
            return name, 0, "", 0

        with (
            patch.object(runner, "execute", side_effect=execute),
            redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(
                runner.run_suites("arprt:test", 2, "/tools/helm", "/packages"), 0
            )
        self.assertEqual(peak, 2)
        self.assertCountEqual(completed, runner.SUITES)


class DocumentationTests(unittest.TestCase):
    def test_duplicate_headings_and_fenced_examples(self):
        source = "# Guide\n```sh\n# Not a heading\n```\n## Same\n## Same\n"
        self.assertEqual(docs.headings(source), {"guide", "same", "same-1"})
        source = (
            "# Guide\n````markdown\n```sh\n# Not a heading\n```\n````\n## Visible\n"
        )
        self.assertEqual(docs.headings(source), {"guide", "visible"})


if __name__ == "__main__":
    unittest.main()
