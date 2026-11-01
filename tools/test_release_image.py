"""Check release safety gates and exact digest Helm wiring without a cluster."""

import copy
import hashlib
import importlib.util
import io
import json
import subprocess
import tempfile
import unittest
import urllib.error
from contextlib import chdir
from pathlib import Path
from unittest.mock import patch

import helm_install

ROOT = Path(__file__).resolve().parent


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, ROOT / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


release = load("release_image", "release-image.py")
SHA = "a" * 40


class ReleaseGateTests(unittest.TestCase):
    def test_release_versions(self):
        for tag in ["v0.2.0", "v1.2.3-rc.1", "v1.0.0-alpha-1"]:
            self.assertEqual(release.validate_tag(tag), tag[1:])
        for tag in [
            "latest",
            "v01.0.0",
            "v1.0",
            "v1.0.0+build",
            "v1.0.0-01",
            "v1.0.0-rc.01",
        ]:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.validate_tag(tag)

    def test_tag_must_be_annotated_on_main_and_checked_out(self):
        with tempfile.TemporaryDirectory() as directory:

            def git(*args):
                return subprocess.check_output(
                    ["git", "-C", directory, *args],
                    text=True,
                    stderr=subprocess.DEVNULL,
                ).strip()

            git("init", "-b", "main")
            git("config", "user.name", "Release test")
            git("config", "user.email", "release@example.com")
            git("commit", "--allow-empty", "-m", "main")
            main_sha = git("rev-parse", "HEAD")
            git("update-ref", "refs/remotes/origin/main", main_sha)
            git("tag", "-a", "v0.2.0", "-m", "arprt v0.2.0")
            with chdir(directory):
                output = Path(directory) / "outputs"
                with patch.dict(
                    release.os.environ,
                    {"GITHUB_OUTPUT": str(output)},
                ):
                    release.guard("v0.2.0")
                self.assertEqual(
                    output.read_text(),
                    f"tag=v0.2.0\nversion=0.2.0\nrevision={main_sha}\n",
                )
                git("tag", "v0.2.1")
                with self.assertRaisesRegex(ValueError, "annotated tag"):
                    release.guard("v0.2.1")
                git("tag", "-a", "v01.0.0", "-m", "invalid version")
                with self.assertRaisesRegex(ValueError, "vMAJOR.MINOR.PATCH"):
                    release.guard("v01.0.0")
                git("checkout", "-b", "other")
                git("commit", "--allow-empty", "-m", "outside main")
                git("tag", "-a", "v0.3.0", "-m", "arprt v0.3.0")
                with self.assertRaises(subprocess.CalledProcessError):
                    release.guard("v0.3.0")
                with self.assertRaisesRegex(ValueError, "exact tagged commit"):
                    release.guard("v0.2.0")

    def test_existing_tags_and_authentication_failures_rejected(self):
        for occupied in range(3):
            with patch.object(
                release, "registry_exists", side_effect=[False] * occupied + [True]
            ):
                with self.assertRaisesRegex(ValueError, "already exists"):
                    release.unused("example/arprt", "v0.1.0")
        with patch.object(release, "registry_exists", return_value=False) as exists:
            release.unused("example/arprt", "v0.1.0")
            self.assertEqual(
                [call.args[1] for call in exists.call_args_list],
                ["v0.1.0", "v0.1.0-amd64", "v0.1.0-arm64"],
            )

    def test_registry_responses_fail_closed_without_leaking_credentials(self):
        token = b'{"token":"registry-secret"}'
        for status, code, absent in [
            (404, "MANIFEST_UNKNOWN", True),
            (404, "NAME_UNKNOWN", True),
            (404, "DENIED", False),
            (401, "UNAUTHORIZED", False),
            (403, "DENIED", False),
            (500, "UNKNOWN", False),
        ]:
            body = json.dumps({"errors": [{"code": code}]}).encode()
            error = urllib.error.HTTPError(
                "https://ghcr.io/secret", status, "secret", {}, io.BytesIO(body)
            )
            with (
                self.subTest(status=status, code=code),
                patch.object(
                    release.urllib.request,
                    "urlopen",
                    side_effect=[io.BytesIO(token), error],
                ),
            ):
                if absent:
                    self.assertFalse(release.registry_exists("example/arprt", "v1.0.0"))
                else:
                    with self.assertRaisesRegex(
                        RuntimeError, "packages:read"
                    ) as caught:
                        release.registry_exists("example/arprt", "v1.0.0")
                    self.assertNotIn("secret", str(caught.exception))
        for error in [
            TimeoutError("secret"),
            urllib.error.URLError("secret"),
            urllib.error.HTTPError("secret", 404, "secret", {}, io.BytesIO(b"{}")),
        ]:
            with patch.object(release.urllib.request, "urlopen", side_effect=error):
                with self.assertRaises(RuntimeError):
                    release.registry_exists("example/arprt", "v1.0.0")
        with patch.object(
            release.urllib.request,
            "urlopen",
            side_effect=[io.BytesIO(token), io.BytesIO(b"{}")],
        ) as fetch:
            self.assertTrue(release.registry_exists("example/arprt", "v1.0.0"))
            self.assertEqual(fetch.call_args.args[0].get_method(), "GET")

    def test_optional_oci_authentication_is_used_only_for_token_exchange(self):
        with (
            patch.dict(
                release.os.environ,
                {"GH_TOKEN": "private-token", "GITHUB_ACTOR": "example"},
            ),
            patch.object(
                release.urllib.request,
                "urlopen",
                side_effect=[
                    io.BytesIO(b'{"token":"scoped-token"}'),
                    io.BytesIO(b"{}"),
                ],
            ) as fetch,
        ):
            self.assertTrue(release.registry_exists("example/arprt", "v1.0.0"))
            first, second = [call.args[0] for call in fetch.call_args_list]
            self.assertTrue(first.get_header("Authorization").startswith("Basic "))
            self.assertEqual(second.get_header("Authorization"), "Bearer scoped-token")


class PlatformAndImageTests(unittest.TestCase):
    def test_index_requires_both_platforms_without_duplicates(self):
        index = {
            "manifests": [
                {
                    "platform": {"os": "linux", "architecture": arch},
                    "digest": "sha256:" + char * 64,
                }
                for arch, char in [("amd64", "a"), ("arm64", "b")]
            ]
        }
        self.assertEqual(
            set(release.validate_index(index)), {"linux/amd64", "linux/arm64"}
        )
        for manifests in [
            index["manifests"][:1],
            index["manifests"] + index["manifests"][:1],
            index["manifests"] + [{"platform": {"os": "linux", "architecture": "386"}}],
        ]:
            with self.assertRaises(ValueError):
                release.validate_index({"manifests": manifests})
        variant = copy.deepcopy(index)
        variant["manifests"][1]["platform"]["variant"] = "v7"
        with self.assertRaises(ValueError):
            release.validate_index(variant)

    def test_tag_and_index_digest_references(self):
        for image, expected in [
            (
                "ghcr.io/dayflower/arprt:v0.1.0",
                {"repository": "ghcr.io/dayflower/arprt", "tag": "v0.1.0"},
            ),
            (
                "localhost:5000/arprt:dev",
                {"repository": "localhost:5000/arprt", "tag": "dev"},
            ),
            (
                "ghcr.io/dayflower/arprt@sha256:" + "a" * 64,
                {
                    "repository": "ghcr.io/dayflower/arprt",
                    "digest": "sha256:" + "a" * 64,
                },
            ),
        ]:
            self.assertEqual(
                helm_install.image_values(image), {"digest": "", "tag": "", **expected}
            )
        for image in [
            "ghcr.io/dayflower/arprt",
            "localhost:5000/arprt",
            "arprt@sha256:bad",
            "arprt@sha256:" + "z" * 64,
        ]:
            with self.subTest(image=image), self.assertRaises(ValueError):
                helm_install.image_values(image)


class AnonymousRegistryTests(unittest.TestCase):
    def fixture(self, wrong_revision=False, corrupt_blob=False):
        registry = {}
        manifests = []

        def encoded(value):
            body = json.dumps(value).encode()
            return body, "sha256:" + hashlib.sha256(body).hexdigest()

        for arch in ["amd64", "arm64"]:
            config = {
                "os": "linux",
                "architecture": arch,
                "config": {
                    "User": "1000:1000",
                    "Entrypoint": ["/arprt"],
                    "Labels": {
                        "org.opencontainers.image." + key: value
                        for key, value in {
                            "source": "https://github.com/dayflower/arprt",
                            "version": "0.1.0",
                            "revision": "wrong" if wrong_revision else SHA,
                            "licenses": "MIT",
                            "description": "ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS. It is not affiliated with or endorsed by TRMNL.",
                        }.items()
                    },
                },
            }
            body, digest = encoded(config)
            registry["blobs/" + digest] = b"{}" if corrupt_blob else body
            body, digest = encoded({"config": {"digest": digest}})
            registry["manifests/" + digest] = body
            manifests.append(
                {"platform": {"os": "linux", "architecture": arch}, "digest": digest}
            )
        body, digest = encoded({"manifests": manifests})
        registry["manifests/v0.1.0"] = body

        def urlopen(request, **kwargs):
            if isinstance(request, str):
                self.assertTrue(request.startswith("https://ghcr.io/token?"))
                return io.BytesIO(b'{"token":"anonymous-token"}')
            self.assertEqual(
                request.get_header("Authorization"), "Bearer anonymous-token"
            )
            path = request.full_url.removeprefix("https://ghcr.io/v2/dayflower/arprt/")
            return io.BytesIO(registry[path])

        return digest, urlopen

    def test_anonymous_index_and_platform_evidence(self):
        digest, urlopen = self.fixture()
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(release.urllib.request, "urlopen", side_effect=urlopen),
        ):
            output = Path(directory) / "digests.json"
            release.inspect("dayflower/arprt", "v0.1.0", digest, SHA, output)
            evidence = json.loads(output.read_text())
            self.assertEqual(evidence["indexDigest"], digest)
            self.assertEqual(
                set(evidence["platformDigests"]), {"linux/amd64", "linux/arm64"}
            )
            self.assertTrue(evidence["anonymousRegistryRead"])

    def test_wrong_index_revision_and_corrupt_content_rejected(self):
        for options in [{}, {"wrong_revision": True}, {"corrupt_blob": True}]:
            digest, urlopen = self.fixture(**options)
            with (
                tempfile.TemporaryDirectory() as directory,
                patch.object(release.urllib.request, "urlopen", side_effect=urlopen),
            ):
                output = Path(directory) / "digests.json"
                with self.assertRaises(ValueError):
                    release.inspect(
                        "dayflower/arprt",
                        "v0.1.0",
                        digest if options else "sha256:" + "f" * 64,
                        SHA,
                        output,
                    )
                self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
