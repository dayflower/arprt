"""Protect chart version ownership, immutable publication and anonymous evidence."""

import importlib.util
import io
import json
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "release_charts", ROOT / "release-charts.py"
)
charts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(charts)
VERSIONS = {
    "chart_version": "0.2.0",
    "crd_version": "0.1.0",
    "dependency_version": "0.1.0",
}


def archive(path, files, timestamp=0):
    with tarfile.open(path, "w:gz") as output:
        for name, data in files.items():
            member = tarfile.TarInfo(name)
            member.size = len(data)
            member.mtime = timestamp
            output.addfile(member, io.BytesIO(data))


class VersionTests(unittest.TestCase):
    def test_independent_crd_version_and_matching_controller(self):
        parent = {
            "name": "arprt",
            "version": "0.2.0",
            "appVersion": "0.3.0",
            "dependencyVersion": "0.1.0",
        }
        crds = {"name": "arprt-crds", "version": "0.1.0", "appVersion": "0.1.0"}
        with patch.object(charts, "chart_metadata", side_effect=[parent, crds]):
            self.assertEqual(charts.metadata("helm", "0.3.0"), VERSIONS)
        for wrong in (
            {**parent, "appVersion": "0.2.0"},
            {**parent, "dependencyVersion": "0.2.0"},
        ):
            with patch.object(charts, "chart_metadata", side_effect=[wrong, crds]):
                with self.assertRaises(ValueError):
                    charts.metadata("helm", "0.3.0")

    def test_normalized_helm_metadata_requires_exact_conditional_dependency(self):
        text = """apiVersion: v2
appVersion: 0.3.0
dependencies:
- condition: crds.enabled
  name: arprt-crds
  repository: file://../arprt-crds
  version: 0.1.0
name: arprt
version: 0.2.0
"""
        with patch.object(charts, "run", return_value=text):
            self.assertEqual(
                charts.chart_metadata("helm", "parent")["dependencyVersion"], "0.1.0"
            )
        for invalid in (
            text.replace("0.1.0", "^0.1.0"),
            text.replace("crds.enabled", "wrong"),
            text.replace("file://../arprt-crds", "https://example.test/charts"),
            text.replace("version: 0.2.0", "version: 0.2.0+build"),
        ):
            with patch.object(charts, "run", return_value=invalid):
                with self.assertRaises(ValueError):
                    charts.chart_metadata("helm", "parent")


class AssemblyTests(unittest.TestCase):
    def test_preparation_resolves_only_the_default_image_in_a_temporary_copy(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            root = directory / "source"
            parent = root / "charts/arprt"
            parent.mkdir(parents=True)
            (root / "charts/arprt-crds").mkdir()
            original = 'crds:\n  enabled: true\nimage:\n  repository: old\n  digest: old\nimagePullSecrets: []\nbyos:\n  publicBaseURL: ""\n'
            (parent / "values.yaml").write_text(original)
            (parent / "Chart.lock").write_text("unchanged lock")
            image = "sha256:" + "a" * 64
            packaged = []

            def run(*args):
                if args[1] == "package":
                    chart = Path(args[2])
                    packaged.append(chart.name)
                    if chart.name == "arprt":
                        values = (chart / "values.yaml").read_text()
                        resolved = json.loads(
                            values.split("image: ", 1)[1].splitlines()[0]
                        )
                        self.assertEqual(resolved["digest"], image)
                        self.assertEqual(
                            resolved["repository"], "ghcr.io/example/arprt"
                        )
                        self.assertEqual(resolved["tag"], "v0.3.0")
                        self.assertIn('publicBaseURL: ""', values)
                        self.assertIn("imagePullSecrets: []", values)
                return ""

            with (
                patch.object(charts, "ROOT", root),
                patch.object(charts, "metadata", return_value=VERSIONS),
                patch.object(charts, "run", side_effect=run),
            ):
                charts.prepare(
                    "helm", "0.3.0", "example/arprt", image, directory / "output"
                )
            self.assertEqual(packaged, ["arprt-crds", "arprt"])
            self.assertEqual((parent / "values.yaml").read_text(), original)
            self.assertEqual((parent / "Chart.lock").read_text(), "unchanged lock")


class PublicationTests(unittest.TestCase):
    def test_nested_package_endpoint_and_fail_closed_errors(self):
        for status in (401, 403, 500):
            with (
                patch.object(charts.image_release, "run", return_value="User"),
                patch.object(
                    charts.subprocess,
                    "run",
                    return_value=subprocess.CompletedProcess(
                        [], 1, stderr=f"failure (HTTP {status})"
                    ),
                ),
            ):
                with self.assertRaises(RuntimeError):
                    charts.existing("example/arprt", "arprt-crds", "0.1.0")
        response = subprocess.CompletedProcess(
            [], 0, stdout='[[{"metadata":{"container":{"tags":["0.1.0"]}}}]]'
        )
        with (
            patch.object(charts.image_release, "run", return_value="User"),
            patch.object(charts.subprocess, "run", return_value=response) as run,
        ):
            self.assertTrue(charts.existing("example/arprt", "arprt-crds", "0.1.0"))
            self.assertIn("charts%2Farprt-crds", run.call_args.args[0][-1])

    def test_existing_parent_is_never_overwritten(self):
        with (
            patch.object(charts, "existing", return_value=True),
            patch.object(charts, "run") as run,
        ):
            with self.assertRaisesRegex(ValueError, "never overwrite"):
                charts.publish("helm", "example/arprt", "/packages", VERSIONS)
            run.assert_not_called()

    def test_reuse_requires_identical_files_and_new_crds_are_published_first(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            expected = directory / "arprt-crds-0.1.0.tgz"
            files = {
                "arprt-crds/Chart.yaml": b"version: 0.1.0",
                "arprt-crds/files/schema.yaml": b"schema",
            }
            archive(expected, files)
            for changed in (False, True):
                calls = []

                def run(*args, **kwargs):
                    calls.append(args)
                    destination = Path(args[-1]) / expected.name
                    archive(
                        destination,
                        {**files, "arprt-crds/files/schema.yaml": b"different"}
                        if changed
                        else files,
                        123,
                    )
                    return ""

                with (
                    patch.object(charts, "existing", side_effect=[False, True]),
                    patch.object(charts, "run", side_effect=run),
                ):
                    if changed:
                        with self.assertRaisesRegex(ValueError, "contents differ"):
                            charts.publish("helm", "example/arprt", directory, VERSIONS)
                    else:
                        # Only the parent push follows the successful CRD comparison.
                        def reuse_run(*args, **kwargs):
                            if args[1] == "pull":
                                return run(*args, **kwargs)
                            calls.append(args)
                            return ""

                        with patch.object(charts, "run", side_effect=reuse_run):
                            charts.publish("helm", "example/arprt", directory, VERSIONS)
                    self.assertEqual(
                        sum(call[1] == "push" for call in calls), 0 if changed else 1
                    )
            with (
                patch.object(charts, "existing", return_value=False),
                patch.object(charts, "run") as run,
            ):
                charts.publish("helm", "example/arprt", directory, VERSIONS)
                self.assertEqual(
                    [Path(call.args[2]).name for call in run.call_args_list],
                    ["arprt-crds-0.1.0.tgz", "arprt-0.2.0.tgz"],
                )

    def test_anonymous_evidence_checks_full_packages_and_independent_versions(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name, version in (("arprt", "0.2.0"), ("arprt-crds", "0.1.0")):
                archive(
                    directory / f"{name}-{version}.tgz",
                    {name + "/Chart.yaml": version.encode()},
                )
            image = directory / "image.json"
            image.write_text(
                json.dumps(
                    {
                        "image": "ghcr.io/example/arprt",
                        "indexDigest": "sha256:" + "a" * 64,
                    }
                )
            )
            output = directory / "distribution.json"
            calls = []

            def pull(*args, env):
                calls.append((args, env))
                name, version = args[2].rsplit("/", 1)[1], args[4]
                source = directory / f"{name}-{version}.tgz"
                destination = Path(args[6]) / source.name
                destination.write_bytes(source.read_bytes())
                self.assertNotEqual(
                    env["HELM_REGISTRY_CONFIG"], "/operator/registry.json"
                )
                self.assertNotIn("HELM_PLUGINS", env)
                return "Digest: sha256:" + "b" * 64 + "\n"

            with (
                patch.dict(
                    charts.os.environ,
                    {
                        "HELM_REGISTRY_CONFIG": "/operator/registry.json",
                        "HELM_PLUGINS": "/operator/plugins",
                    },
                ),
                patch.object(charts, "run", side_effect=pull),
            ):
                charts.verify(
                    "helm", "example/arprt", directory, VERSIONS, image, output
                )
            evidence = json.loads(output.read_text())
            self.assertEqual(evidence["dependencyVersion"], "0.1.0")
            self.assertTrue(evidence["anonymousChartRead"])
            self.assertEqual([call[0][4] for call in calls], ["0.1.0", "0.2.0"])
            self.assertEqual(
                evidence["charts"]["arprt"]["digest"], "sha256:" + "b" * 64
            )


if __name__ == "__main__":
    unittest.main()
