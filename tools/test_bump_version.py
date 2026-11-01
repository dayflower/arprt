"""Check release bumps preserve independent chart and image ownership."""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("bump-version.sh")
CHART = """apiVersion: v2
name: arprt
version: 1.2.3 # package
appVersion: "0.4.5" # controller
dependencies:
  - name: arprt-crds
    version: 0.1.0
    repository: file://../arprt-crds
    condition: crds.enabled
"""


class BumpVersionTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="arprt bump test ")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        script = self.root / "tools/bump-version.sh"
        script.parent.mkdir()
        shutil.copy2(SCRIPT, script)
        self.script = script
        self.env = os.environ.copy()
        self.chart = self.root / "charts/arprt/Chart.yaml"
        self.chart.parent.mkdir(parents=True)
        self.chart.write_text(CHART)
        self.chart.chmod(0o644)
        self.other_files = {
            "charts/arprt/Chart.lock": "dependency lock\n",
            "charts/arprt/values.yaml": "image:\n  tag: v0.1.0\n  digest: accepted\n",
            "charts/arprt-crds/Chart.yaml": "version: 0.1.0\n",
        }
        for filename, content in self.other_files.items():
            path = self.root / filename
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)

    def run_bump(self, *arguments):
        return subprocess.run(
            [str(self.script), *arguments],
            cwd=self.root / "charts",
            capture_output=True,
            text=True,
            env=self.env,
        )

    def test_each_bump_preserves_independent_versions_and_other_files(self):
        for part, chart_version, app_version in [
            ("major", "2.0.0", "1.0.0"),
            ("minor", "1.3.0", "0.5.0"),
            ("patch", "1.2.4", "0.4.6"),
        ]:
            with self.subTest(part=part):
                self.chart.write_text(CHART)
                result = self.run_bump(part)
                self.assertEqual(result.returncode, 0, result.stderr)
                expected = CHART.replace("version: 1.2.3", "version: " + chart_version)
                expected = expected.replace(
                    'appVersion: "0.4.5"', f'appVersion: "{app_version}"'
                )
                self.assertEqual(self.chart.read_text(), expected)
                self.assertEqual(self.chart.stat().st_mode & 0o777, 0o644)
                for filename, content in self.other_files.items():
                    self.assertEqual((self.root / filename).read_text(), content)

    def test_dry_run_reports_the_diff_without_writing(self):
        for arguments in [("--dry-run", "patch"), ("patch", "--dry-run")]:
            with self.subTest(arguments=arguments):
                result = self.run_bump(*arguments)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('+appVersion: "0.4.6" # controller', result.stdout)
                self.assertEqual(self.chart.read_text(), CHART)
        self.assertEqual(list(self.chart.parent.glob("Chart.yaml.*")), [])

    def test_invalid_metadata_never_partially_updates_the_chart(self):
        for source in [
            CHART.replace("1.2.3", "01.2.3"),
            CHART.replace("0.4.5", "0.4.5-rc.1"),
            CHART.replace("0.4.5", "0.4.5+build"),
            CHART.replace('"0.4.5"', '"0.4.5'),
            CHART.replace('"0.4.5"', '"0.4.5" extra'),
            CHART.replace('appVersion: "0.4.5" # controller\n', ""),
            CHART + "version: 1.2.3\n",
        ]:
            with self.subTest(source=source):
                self.chart.write_text(source)
                result = self.run_bump("patch")
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.chart.read_text(), source)
                self.assertEqual(list(self.chart.parent.glob("Chart.yaml.*")), [])

    def test_single_quotes_and_large_version_components(self):
        source = CHART.replace("1.2.3", "1.2.999999999999999999999999")
        source = source.replace('"0.4.5"', "'0.4.5'")
        self.chart.write_text(source)
        result = self.run_bump("patch")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "version: 1.2.1000000000000000000000000 # package", self.chart.read_text()
        )
        self.assertIn("appVersion: '0.4.6' # controller", self.chart.read_text())

    def test_usage_errors_and_help_do_not_write(self):
        for arguments in [(), ("release",), ("major", "patch"), ("--dry-run",)]:
            with self.subTest(arguments=arguments):
                self.assertEqual(self.run_bump(*arguments).returncode, 2)
        self.assertEqual(self.run_bump("--help").returncode, 0)
        self.assertEqual(self.chart.read_text(), CHART)


class CRDBumpTests(unittest.TestCase):
    run_bump = BumpVersionTests.run_bump

    def setUp(self):
        BumpVersionTests.setUp(self)
        self.crd = self.root / "charts/arprt-crds/Chart.yaml"
        self.crd.write_text(
            'apiVersion: v2\nname: arprt-crds\nversion: 0.1.0 # crds\nappVersion: "0.7.8" # schema\n'
        )
        self.lock = self.root / "charts/arprt/Chart.lock"
        self.helm = self.root / "fake-helm"
        self.helm.write_text("""#!/usr/bin/env python3
import os, re, sys
from pathlib import Path
parent = Path(sys.argv[3])
if os.environ.get('BUMP_FAIL'):
    sys.exit(1)
if os.environ.get('BUMP_EDIT'):
    path = Path(os.environ['BUMP_EDIT'])
    path.write_text(path.read_text() + '# concurrent edit\\n')
if sys.argv[2] == 'update':
    version = re.search(r'    version: ([0-9.]+)', (parent / 'Chart.yaml').read_text())[1]
    (parent / 'Chart.lock').write_text('version: ' + version + '\\ngenerated: new timestamp\\n')
""")
        self.helm.chmod(0o755)
        (self.root / "Makefile").write_text(
            "bump-helm:\n\t@printf '%s\\n' \"$(CURDIR)/fake-helm\"\n"
        )

    def snapshot(self):
        return {
            p: (p.read_bytes(), p.stat().st_mode & 0o777)
            for p in (self.chart, self.crd, self.lock)
        }

    def test_independent_crd_bumps_and_complete_dry_run(self):
        self.chart.chmod(0o640)
        self.crd.chmod(0o600)
        self.lock.chmod(0o640)
        original = self.snapshot()
        for crds, version, app in [
            ("major", "1.0.0", "1.0.0"),
            ("minor", "0.2.0", "0.8.0"),
            ("patch", "0.1.1", "0.7.9"),
        ]:
            with self.subTest(crds=crds):
                result = self.run_bump("--dry-run", "minor", "--crds", crds)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.snapshot(), original)
                for name in (
                    "charts/arprt/Chart.yaml",
                    "charts/arprt-crds/Chart.yaml",
                    "charts/arprt/Chart.lock",
                ):
                    self.assertIn(name, result.stdout)
                self.assertIn("+generated: new timestamp", result.stdout)
                self.assertIn(f"+version: {version} # crds", result.stdout)
                self.assertIn(f'+appVersion: "{app}" # schema', result.stdout)
        result = self.run_bump("minor", "--crds", "patch")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("version: 1.3.0 # package", self.chart.read_text())
        self.assertIn('appVersion: "0.5.0" # controller', self.chart.read_text())
        self.assertIn("    version: 0.1.1", self.chart.read_text())
        self.assertIn("version: 0.1.1", self.lock.read_text())
        self.assertEqual(list(self.root.glob(".bump-version.*")), [])

    def test_crd_errors_and_helm_failure_are_non_destructive(self):
        original = self.snapshot()
        for arguments in [
            ("patch", "--crds"),
            ("patch", "--crds", "prerelease"),
            ("patch", "--crds", "minor", "--crds", "patch"),
        ]:
            self.assertEqual(self.run_bump(*arguments).returncode, 2)
            self.assertEqual(self.snapshot(), original)
        for invalid in [
            self.crd.read_text() + "version: 1.0.0\n",
            self.crd.read_text().replace("0.7.8", "0.7.8+build"),
        ]:
            self.crd.write_text(invalid)
            before = self.snapshot()
            self.assertNotEqual(self.run_bump("patch", "--crds", "minor").returncode, 0)
            self.assertEqual(self.snapshot(), before)
        self.crd.write_bytes(original[self.crd][0])
        self.env["BUMP_FAIL"] = "true"
        self.assertNotEqual(self.run_bump("patch", "--crds", "minor").returncode, 0)
        self.assertEqual(self.snapshot(), original)
        del self.env["BUMP_FAIL"]
        self.helm.unlink()
        self.assertNotEqual(self.run_bump("patch", "--crds", "minor").returncode, 0)
        self.assertEqual(self.snapshot(), original)

    def test_invalid_dependency_metadata_is_not_silently_repaired(self):
        for source in [
            CHART.replace(
                "    version: 0.1.0", "    version: 0.1.0\n    version: 0.1.0"
            ),
            CHART.replace("    version: 0.1.0", "    version: 0.2.0"),
            CHART.replace("    version: 0.1.0", "    version: '0.1.0\""),
            CHART.replace("crds.enabled", "other.enabled"),
        ]:
            with self.subTest(source=source):
                self.chart.write_text(source)
                before = self.snapshot()
                self.assertNotEqual(
                    self.run_bump("patch", "--crds", "minor").returncode, 0
                )
                self.assertEqual(self.snapshot(), before)

    def test_concurrent_edits_are_preserved(self):
        original = self.snapshot()
        self.env["BUMP_EDIT"] = str(self.crd)
        result = self.run_bump("patch", "--crds", "minor")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed during preparation", result.stderr)
        self.assertTrue(self.crd.read_bytes().startswith(original[self.crd][0]))
        self.assertEqual(self.chart.read_bytes(), original[self.chart][0])
        self.assertEqual(self.lock.read_bytes(), original[self.lock][0])

    def test_apply_failure_restores_all_files_and_permissions(self):
        self.chart.chmod(0o640)
        self.crd.chmod(0o600)
        original = self.snapshot()
        shim = self.root / "shim"
        shim.mkdir()
        (shim / "mv").write_text(
            '#!/usr/bin/env bash\ncase "$2" in *arprt-crds*) exit 1 ;; esac\nexec /bin/mv "$@"\n'
        )
        (shim / "mv").chmod(0o755)
        self.env["PATH"] = str(shim) + os.pathsep + self.env["PATH"]
        result = self.run_bump("minor", "--crds", "minor")
        self.assertNotEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.snapshot(), original)


if __name__ == "__main__":
    unittest.main()
