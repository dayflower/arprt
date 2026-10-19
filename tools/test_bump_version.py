"""Check release bumps preserve independent chart and image ownership."""

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


if __name__ == "__main__":
    unittest.main()
