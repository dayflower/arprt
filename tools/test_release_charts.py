"""Protect chart version ownership, immutable publication and anonymous evidence."""

import hashlib
import importlib.util
import io
import json
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


def schema_files():
    return {
        path.name: path.read_bytes()
        for path in sorted((charts.ROOT / "config/crd/bases").glob("*.yaml"))
    }


def package_files(name, schemas):
    prefix = (
        "arprt-crds/files/crds/"
        if name == "arprt-crds"
        else "arprt/charts/arprt-crds/files/crds/"
    )
    return {prefix + path: data for path, data in schemas.items()}


class BundleTests(unittest.TestCase):
    def test_deterministic_bundle_matches_both_charts_and_has_no_helm_metadata(self):
        schemas = schema_files()
        with tempfile.TemporaryDirectory() as temporary:
            archives = charts.release_archives(temporary, VERSIONS)
            for name, path in archives.items():
                archive(path, package_files(name, schemas))
            bundle = charts.crd_bundle(archives)
            self.assertEqual(bundle, b"".join(schemas.values()))
            self.assertEqual(bundle.count(b"kind: CustomResourceDefinition\n"), 3)
            self.assertNotIn(b"meta.helm.sh/", bundle)
            self.assertNotIn(b"helm.sh/resource-policy", bundle)
            digest = charts.write_bundle(temporary, bundle)
            self.assertEqual(digest, hashlib.sha256(bundle).hexdigest())
            self.assertEqual(
                (Path(temporary) / "crds.yaml.sha256").read_text(),
                digest + "  crds.yaml\n",
            )

    def test_missing_extra_and_changed_schema_in_either_chart_are_rejected(self):
        schemas = schema_files()
        first = next(iter(schemas))
        for name in ("arprt", "arprt-crds"):
            for broken in (
                {key: value for key, value in schemas.items() if key != first},
                {**schemas, "unexpected.yaml": b"---\nextra\n"},
                {**schemas, first: schemas[first] + b"# changed\n"},
            ):
                with (
                    self.subTest(name=name, files=tuple(broken)),
                    tempfile.TemporaryDirectory() as temporary,
                ):
                    archives = charts.release_archives(temporary, VERSIONS)
                    for chart, path in archives.items():
                        archive(
                            path,
                            package_files(chart, broken if chart == name else schemas),
                        )
                    with self.assertRaisesRegex(ValueError, "packaged CRDs differ"):
                        charts.crd_bundle(archives)


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
            schemas = {"example.yaml": b"---\nkind: CustomResourceDefinition\n"}
            canonical = root / "config/crd/bases"
            canonical.mkdir(parents=True)
            (canonical / "example.yaml").write_bytes(schemas["example.yaml"])
            original = 'crds:\n  enabled: true\nimage:\n  repository: old\n  digest: old\nimagePullSecrets: []\nbyos:\n  publicBaseURL: ""\n'
            (parent / "values.yaml").write_text(original)
            (parent / "Chart.lock").write_text("unchanged lock")
            image = "sha256:" + "a" * 64
            packaged = []

            def run(*args):
                if args[1] == "package":
                    chart = Path(args[2])
                    packaged.append(chart.name)
                    path = charts.release_archives(Path(args[4]), VERSIONS)[chart.name]
                    archive(path, package_files(chart.name, schemas))
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
            self.assertEqual(
                (directory / "output/crds.yaml").read_bytes(), schemas["example.yaml"]
            )
            self.assertEqual((parent / "values.yaml").read_text(), original)
            self.assertEqual((parent / "Chart.lock").read_text(), "unchanged lock")


class PublicationTests(unittest.TestCase):
    def test_nested_package_uses_shared_oci_check(self):
        with patch.object(
            charts.image_release, "registry_exists", return_value=True
        ) as exists:
            self.assertTrue(charts.existing("example/arprt", "arprt-crds", "0.1.0"))
            exists.assert_called_once_with("example/charts/arprt-crds", "0.1.0")

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
                    {
                        name + "/Chart.yaml": version.encode(),
                        **package_files(name, schema_files()),
                    },
                )
            bundle = charts.crd_bundle(charts.release_archives(directory, VERSIONS))
            digest = charts.write_bundle(directory, bundle)
            image = directory / "image.json"
            image.write_text(
                json.dumps(
                    {
                        "image": "ghcr.io/example/arprt",
                        "indexDigest": "sha256:" + "a" * 64,
                    }
                )
            )
            accepted = directory / "accepted"
            accepted.mkdir()
            output = accepted / "distribution.json"
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
            self.assertEqual(
                evidence["crds"],
                {
                    "asset": "crds.yaml",
                    "sha256": digest,
                    "chartVersion": "0.1.0",
                },
            )
            self.assertEqual([call[0][4] for call in calls], ["0.1.0", "0.2.0"])
            self.assertEqual(
                evidence["charts"]["arprt"]["digest"], "sha256:" + "b" * 64
            )
            self.assertEqual((accepted / "crds.yaml").read_bytes(), bundle)
            self.assertEqual(
                (accepted / "crds.yaml.sha256").read_text(),
                digest + "  crds.yaml\n",
            )
            for asset in ("crds.yaml", "crds.yaml.sha256"):
                with self.subTest(asset=asset):
                    original = (directory / asset).read_bytes()
                    (directory / asset).write_bytes(original + b"changed\n")
                    with patch.object(charts, "run", side_effect=pull):
                        with self.assertRaisesRegex(
                            ValueError, "bundle or checksum differs"
                        ):
                            charts.verify(
                                "helm",
                                "example/arprt",
                                directory,
                                VERSIONS,
                                image,
                                output,
                            )
                    (directory / asset).write_bytes(original)


class PreflightTests(unittest.TestCase):
    def setUp(self):
        environment = patch.dict(charts.os.environ)
        environment.start()
        self.addCleanup(environment.stop)
        charts.os.environ.pop("GH_TOKEN", None)

    def test_isolated_lock_build_and_full_crd_comparison(self):
        # Exercise every package file, including non-schema metadata; timestamps
        # are deliberately different. Registry and Helm are simulated offline.
        for change in (
            None,
            "arprt-crds/files/crds/schema.yaml",
            "arprt-crds/README.md",
            "arprt-crds/Chart.yaml",
        ):
            for reuse in (False, True):
                with (
                    self.subTest(change=change, reuse=reuse),
                    tempfile.TemporaryDirectory() as temporary,
                ):
                    root = Path(temporary)
                    parent = root / "charts/arprt"
                    parent.mkdir(parents=True)
                    crds = root / "charts/arprt-crds"
                    crds.mkdir()
                    (parent / "Chart.lock").write_bytes(b"original lock")
                    (parent / "values.yaml").write_bytes(b"original defaults")
                    (parent / "charts").mkdir()
                    (parent / "charts/ignored.tgz").write_bytes(b"original archive")
                    files = {
                        "arprt-crds/files/crds/schema.yaml": b"schema",
                        "arprt-crds/README.md": b"readme",
                        "arprt-crds/Chart.yaml": b"version: 0.1.0",
                    }
                    snapshot = {
                        p.relative_to(root): p.read_bytes()
                        for p in root.rglob("*")
                        if p.is_file()
                    }
                    commands = []

                    def run(*args, **kwargs):
                        commands.append(args)
                        self.assertNotIn(args[1], ("push", "registry"))
                        if args[1] == "dependency":
                            self.assertFalse(
                                (Path(args[3]) / "charts/ignored.tgz").exists()
                            )
                        if args[1] == "package":
                            archive(Path(args[-1]) / "arprt-crds-0.1.0.tgz", files)
                        if args[1] == "pull":
                            actual = {**files, change: b"changed"} if change else files
                            archive(
                                Path(args[-1]) / "arprt-crds-0.1.0.tgz",
                                actual,
                                timestamp=123,
                            )
                            self.assertIn("HELM_REGISTRY_CONFIG", kwargs["env"])
                        return ""

                    with (
                        patch.object(charts, "ROOT", root),
                        patch.object(charts, "metadata", return_value=VERSIONS),
                        patch.object(charts.image_release, "unused"),
                        patch.object(charts, "existing", side_effect=[False, reuse]),
                        patch.object(charts, "run", side_effect=run),
                    ):
                        if reuse and change:
                            with self.assertRaisesRegex(ValueError, change):
                                charts.preflight("helm", "0.3.0", "example/arprt")
                        else:
                            charts.preflight("helm", "0.3.0", "example/arprt")
                    self.assertEqual(
                        snapshot,
                        {
                            p.relative_to(root): p.read_bytes()
                            for p in root.rglob("*")
                            if p.is_file()
                        },
                    )
                    self.assertFalse((root / "dist").exists())

    def test_lock_mutation_and_build_failure_are_read_only(self):
        for failure in (False, True):
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                parent = root / "charts/arprt"
                parent.mkdir(parents=True)
                (parent / "Chart.lock").write_bytes(b"lock")

                def run(*args):
                    if failure:
                        raise RuntimeError("invalid lock")
                    (Path(args[3]) / "Chart.lock").write_bytes(b"changed")

                with (
                    patch.object(charts, "ROOT", root),
                    patch.object(charts, "metadata", return_value=VERSIONS),
                    patch.object(charts, "run", side_effect=run),
                    patch.object(charts.image_release, "unused") as unused,
                ):
                    with self.assertRaises((ValueError, RuntimeError)):
                        charts.preflight("helm", "0.3.0", "example/arprt")
                    unused.assert_not_called()
                self.assertEqual((parent / "Chart.lock").read_bytes(), b"lock")

    def test_guard_checks_new_releases_before_heavy_jobs_but_skips_acceptance(self):
        workflow = (charts.ROOT / ".github/workflows/release-image.yaml").read_text()
        guard = workflow.split("  guard:\n", 1)[1].split("\n  # Independent", 1)[0]
        self.assertIn("packages: read", guard)
        self.assertIn("if: github.event_name == 'push'", guard)
        self.assertIn("run: make release-preflight", guard)
        self.assertNotIn("packages: write", guard)
        for job in ("software", "race", "build"):
            block = workflow.split(f"  {job}:\n", 1)[1]
            self.assertIn("needs: guard", block.split("    strategy:", 1)[0])

    def test_private_crd_reads_use_isolated_login_and_hide_authentication_errors(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            parent = root / "charts/arprt"
            parent.mkdir(parents=True)
            (parent / "Chart.lock").write_bytes(b"lock")
            with (
                patch.object(charts, "ROOT", root),
                patch.object(charts, "metadata", return_value=VERSIONS),
                patch.object(charts, "run", return_value="") as run,
                patch.object(charts.image_release, "unused"),
                patch.object(charts, "check_parent"),
                patch.object(charts, "check_crds") as crds,
                patch.dict(charts.os.environ, {"GH_TOKEN": "private-token"}),
            ):
                charts.preflight("helm", "0.3.0", "example/arprt")
                login = run.call_args_list[-1]
                self.assertEqual(login.args[1:4], ("registry", "login", "ghcr.io"))
                self.assertEqual(login.kwargs["input"], "private-token")
                self.assertEqual(login.kwargs["env"], crds.call_args.kwargs["env"])
                self.assertNotIn("private-token", str(login.args))

                def fail_login(*args, **kwargs):
                    if args[1] == "registry":
                        raise RuntimeError("private-token")
                    return ""

                run.side_effect = fail_login
                with self.assertRaisesRegex(RuntimeError, "packages:read") as caught:
                    charts.preflight("helm", "0.3.0", "example/arprt")
                self.assertNotIn("private-token", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
