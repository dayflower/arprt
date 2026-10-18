"""Protect disposable Helm cluster isolation and cleanup on failure."""

import hashlib
import importlib.util
import io
import json
import os
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("helm_smoke", ROOT / "helm-smoke.py")
helm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helm)


class IsolationTests(unittest.TestCase):
    def test_cluster_creation_and_lifecycle_failures_delete_only_owned_cluster(self):
        for failure in ("create", "lifecycle"):
            with (
                self.subTest(failure=failure),
                patch.dict(
                    os.environ,
                    {
                        "KUBECONFIG": "/operator/kubeconfig",
                        "HELM_KUBEAPISERVER": "https://operator.example.test",
                    },
                ),
            ):
                calls = []

                def run(*args, **kwargs):
                    calls.append((args, kwargs))
                    if (
                        args[:3] == ("kind", "create", "cluster")
                        and failure == "create"
                    ):
                        raise RuntimeError("injected cluster creation failure")
                    return subprocess.CompletedProcess(args, 0, "", "")

                with (
                    patch.object(helm, "run", side_effect=run),
                    patch.object(helm, "packages", return_value={}),
                    patch.object(
                        helm.Lifecycle,
                        "exercise",
                        side_effect=RuntimeError("injected lifecycle failure"),
                    ),
                    patch.object(
                        helm,
                        "kubectl",
                        return_value=subprocess.CompletedProcess([], 0, "", ""),
                    ),
                    patch.object(helm.signal, "signal"),
                    patch(
                        "sys.argv",
                        [
                            "helm-smoke.py",
                            "--helm",
                            "/tools/helm",
                            "--image",
                            "arprt:test",
                            "--node-image",
                            "kindest/node:test",
                            "--packages",
                            "/packages",
                        ],
                    ),
                    redirect_stdout(io.StringIO()),
                ):
                    with self.assertRaisesRegex(RuntimeError, "injected"):
                        helm.main()
                create = next(
                    call
                    for call in calls
                    if call[0][:3] == ("kind", "create", "cluster")
                )
                delete = next(
                    call
                    for call in calls
                    if call[0][:3] == ("kind", "delete", "cluster")
                )
                self.assertEqual(create[0][4], delete[0][4])
                self.assertTrue(create[0][4].startswith("arprt-helm-"))
                environment = create[1]["env"]
                self.assertNotEqual(environment["KUBECONFIG"], "/operator/kubeconfig")
                self.assertEqual(
                    delete[1]["env"]["KUBECONFIG"], environment["KUBECONFIG"]
                )
                self.assertNotIn("HELM_KUBEAPISERVER", environment)
                self.assertFalse(Path(environment["KUBECONFIG"]).parent.exists())

    def test_public_source_failure_cannot_fall_back_to_local_packages(self):
        args = helm.argparse.Namespace(
            oci_repository="oci://ghcr.io/example/charts",
            chart_version="1.0.0",
            image="ghcr.io/example/arprt@sha256:" + "1" * 64,
            load_image=False,
            helm="/tools/helm",
            packages="/local/packages",
        )
        with (
            tempfile.TemporaryDirectory() as temporary,
            patch.object(
                helm, "run", side_effect=RuntimeError("anonymous pull failed")
            ),
        ):
            with self.assertRaisesRegex(RuntimeError, "anonymous pull failed"):
                helm.packages(args, Path(temporary), {})

    def test_public_versions_are_independent_and_records_pin_digests(self):
        for use_record, corrupt in ((False, False), (True, False), (True, True)):
            with (
                self.subTest(record=use_record, corrupt=corrupt),
                tempfile.TemporaryDirectory() as temporary,
            ):
                directory = Path(temporary)
                repository = "oci://ghcr.io/example/charts"
                image = "ghcr.io/example/arprt@sha256:" + "1" * 64
                record = directory / "distribution.json"
                record.write_text(
                    json.dumps(
                        {
                            "image": "ghcr.io/example/arprt",
                            "indexDigest": "sha256:" + "1" * 64,
                            "charts": {
                                name: {
                                    "reference": repository + "/" + name,
                                    "digest": "sha256:" + char * 64,
                                    "version": "0.1.0"
                                    if name == "arprt-crds"
                                    else "0.2.0",
                                    "packageSHA256": hashlib.sha256(b"").hexdigest(),
                                }
                                for name, char in (("arprt", "a"), ("arprt-crds", "b"))
                            },
                        }
                    )
                )
                args = helm.argparse.Namespace(
                    oci_repository=repository,
                    chart_version="0.2.0",
                    crd_chart_version="0.1.0",
                    distribution_record=record if use_record else None,
                    image=image,
                    load_image=False,
                    helm="helm",
                )
                calls = []

                def pull(*args, **kwargs):
                    calls.append(args)
                    name = args[2].split("@", 1)[0].rsplit("/", 1)[1]
                    version = "0.1.0" if name == "arprt-crds" else "0.2.0"
                    filename = (
                        f"{name}@sha256-digest.tgz"
                        if "@" in args[2]
                        else f"{name}-{version}.tgz"
                    )
                    (Path(args[-1]) / filename).write_bytes(
                        b"changed" if corrupt else b""
                    )

                with patch.object(helm, "run", side_effect=pull):
                    if corrupt:
                        with self.assertRaisesRegex(RuntimeError, "hash differs"):
                            helm.packages(args, directory, {})
                        self.assertEqual(len(calls), 1)
                        continue
                    selected = helm.packages(args, directory, {})
                self.assertEqual(set(selected), {"arprt", "arprt-crds"})
                if use_record:
                    self.assertEqual(
                        calls[0][2], repository + "/arprt@sha256:" + "a" * 64
                    )
                    self.assertEqual(
                        calls[1][2], repository + "/arprt-crds@sha256:" + "b" * 64
                    )
                    self.assertTrue(all("--version" not in call for call in calls))
                else:
                    self.assertEqual([call[4] for call in calls], ["0.2.0", "0.1.0"])

    def test_staged_default_image_must_match_the_accepted_index(self):
        expected = "ghcr.io/example/arprt@sha256:" + "a" * 64
        for image in (expected, "ghcr.io/example/arprt:old"):
            with self.subTest(image=image), tempfile.TemporaryDirectory() as temporary:
                args = helm.argparse.Namespace(
                    image=expected, oci_repository=None, use_chart_image=True
                )
                lifecycle = helm.Lifecycle(args, Path(temporary), {}, {}, False)
                deployment = {
                    "spec": {"template": {"spec": {"containers": [{"image": image}]}}}
                }
                with (
                    patch.object(lifecycle, "helm"),
                    patch.object(helm, "kubectl"),
                    patch.object(lifecycle, "get", return_value=deployment),
                ):
                    if image == expected:
                        lifecycle.install("staged.tgz")
                    else:
                        with self.assertRaisesRegex(RuntimeError, "default differs"):
                            lifecycle.install("staged.tgz")
                self.assertNotIn("image", json.loads(lifecycle.values.read_text()))

    def test_public_acceptance_rejects_preloaded_images(self):
        args = helm.argparse.Namespace(
            oci_repository="oci://ghcr.io/example/charts", load_image=True
        )
        with (
            tempfile.TemporaryDirectory() as temporary,
            patch.object(helm, "run") as run,
        ):
            with self.assertRaisesRegex(RuntimeError, "anonymously"):
                helm.packages(args, Path(temporary), {})
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
