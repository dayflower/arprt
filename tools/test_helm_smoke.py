"""Protect disposable Helm cluster isolation and cleanup on failure."""

import importlib.util
import io
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
