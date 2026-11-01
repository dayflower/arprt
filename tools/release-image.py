#!/usr/bin/env python3
"""Gate immutable releases and record anonymously verified GHCR image digests."""

import argparse
import base64
import hashlib
import http.client
import json
import os
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

SEMVER = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?"


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def validate_tag(tag):
    if not re.fullmatch("v" + SEMVER, tag):
        raise ValueError(
            "release tag must be vMAJOR.MINOR.PATCH with optional SemVer prerelease"
        )
    return tag[1:]


def guard(tag):
    version = validate_tag(tag)
    ref = "refs/tags/" + tag
    if run("git", "cat-file", "-t", ref) != "tag":
        raise ValueError("release requires an annotated tag")
    revision = run("git", "rev-parse", ref + "^{commit}")
    if revision != run("git", "rev-parse", "HEAD"):
        raise ValueError("checkout must be the exact tagged commit")
    subprocess.run(
        ["git", "merge-base", "--is-ancestor", revision, "origin/main"], check=True
    )
    outputs = {
        "tag": tag,
        "version": version,
        "revision": revision,
    }
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
            for key, value in outputs.items():
                stream.write(f"{key}={value}\n")
    print(json.dumps(outputs, indent=2))


def registry_exists(repository, tag):
    """Read OCI manifests; only an explicit missing-manifest response is absence.

    GH_TOKEN is optional and needs packages:read for private packages. This does
    not use the GitHub Packages API (classic PAT API scopes differ from OCI).
    """
    if not re.fullmatch(r"[a-z0-9][a-z0-9_.-]*(?:/[a-z0-9][a-z0-9_.-]*)+", repository):
        raise ValueError("expected a lowercase OCI repository path")
    target = f"ghcr.io/{repository}:{tag}"
    query = urllib.parse.urlencode(
        {"service": "ghcr.io", "scope": f"repository:{repository}:pull"}
    )
    headers = {}
    if os.environ.get("GH_TOKEN"):
        user = os.environ.get("GITHUB_ACTOR") or repository.split("/")[0]
        credential = base64.b64encode(
            (user + ":" + os.environ["GH_TOKEN"]).encode()
        ).decode()
        headers["Authorization"] = "Basic " + credential
    try:
        request = urllib.request.Request(
            "https://ghcr.io/token?" + query, headers=headers
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            token = json.load(response)["token"]
        request = urllib.request.Request(
            f"https://ghcr.io/v2/{repository}/manifests/{tag}",
            headers={
                "Authorization": "Bearer " + token,
                "Accept": ", ".join(
                    [
                        "application/vnd.oci.image.index.v1+json",
                        "application/vnd.docker.distribution.manifest.list.v2+json",
                        "application/vnd.oci.image.manifest.v1+json",
                        "application/vnd.docker.distribution.manifest.v2+json",
                    ]
                ),
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=30):
                return True
        except urllib.error.HTTPError as error:
            with error:
                if error.code == 404:
                    codes = {item["code"] for item in json.load(error)["errors"]}
                    if codes and codes <= {"MANIFEST_UNKNOWN", "NAME_UNKNOWN"}:
                        return False
            raise
    except (
        urllib.error.URLError,
        OSError,
        http.client.HTTPException,
        ValueError,
        KeyError,
        TypeError,
    ) as error:
        # Never echo response bodies, URLs supplied by a server, or credentials.
        status = (
            f"HTTP {error.code}"
            if isinstance(error, urllib.error.HTTPError)
            else type(error).__name__
        )
        if isinstance(error, urllib.error.HTTPError):
            error.close()
        raise RuntimeError(
            f"cannot establish availability of {target}: {status}; check network "
            "and anonymous visibility, or supply GH_TOKEN with packages:read"
        ) from None


def unused(repository, tag):
    validate_tag(tag)
    for candidate in (tag, tag + "-amd64", tag + "-arm64"):
        if registry_exists(repository, candidate):
            raise ValueError(
                f"image ghcr.io/{repository}:{candidate} already exists; "
                "choose a new controller version; never overwrite it"
            )


def validate_index(index):
    manifests = index["manifests"]
    platforms = {}
    for manifest in manifests:
        platform = manifest["platform"]
        key = platform["os"] + "/" + platform["architecture"]
        if key in platforms or key not in ("linux/amd64", "linux/arm64"):
            raise ValueError(
                "index must contain exactly one amd64 and one arm64 Linux image"
            )
        if platform.get("variant") not in (None, "", "v8") or (
            key == "linux/amd64" and platform.get("variant")
        ):
            raise ValueError("unexpected platform variant")
        platforms[key] = manifest["digest"]
    if set(platforms) != {"linux/amd64", "linux/arm64"}:
        raise ValueError("both release platforms must be present")
    return platforms


def inspect(repository, tag, expected_digest, revision, output):
    # No GitHub token, Docker credential store or caller authentication is used.
    query = urllib.parse.urlencode(
        {"service": "ghcr.io", "scope": f"repository:{repository}:pull"}
    )
    with urllib.request.urlopen(
        "https://ghcr.io/token?" + query, timeout=30
    ) as response:
        token = json.load(response)["token"]
    accept = ", ".join(
        [
            "application/vnd.oci.image.index.v1+json",
            "application/vnd.docker.distribution.manifest.list.v2+json",
            "application/vnd.oci.image.manifest.v1+json",
            "application/vnd.docker.distribution.manifest.v2+json",
        ]
    )

    def fetch(kind, reference):
        request = urllib.request.Request(
            f"https://ghcr.io/v2/{repository}/{kind}/{reference}",
            headers={"Authorization": "Bearer " + token, "Accept": accept},
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read()
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        if reference.startswith("sha256:") and reference != digest:
            raise ValueError("registry content does not match requested digest")
        return json.loads(body), digest

    index, digest = fetch("manifests", tag)
    if digest != expected_digest:
        raise ValueError(
            "version tag no longer refers to the published image index digest"
        )
    platforms = validate_index(index)
    for platform, platform_digest in platforms.items():
        manifest, _ = fetch("manifests", platform_digest)
        config, _ = fetch("blobs", manifest["config"]["digest"])
        if config["os"] + "/" + config["architecture"] != platform:
            raise ValueError("platform descriptor and image configuration disagree")
        if config["config"]["User"] != "1000:1000" or config["config"][
            "Entrypoint"
        ] != ["/arprt"]:
            raise ValueError(
                "release runtime must preserve non-root controller entrypoint"
            )
        labels = config["config"]["Labels"]
        for key, value in {
            "source": "https://github.com/" + repository,
            "version": tag[1:],
            "revision": revision,
            "licenses": "MIT",
            "description": "ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS. It is not affiliated with or endorsed by TRMNL.",
        }.items():
            if labels.get("org.opencontainers.image." + key) != value:
                raise ValueError("incorrect OCI " + key + " label")
    evidence = {
        "image": "ghcr.io/" + repository,
        "tag": tag,
        "version": tag[1:],
        "revision": revision,
        "indexDigest": digest,
        "platformDigests": platforms,
        "anonymousRegistryRead": True,
    }
    Path(output).write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    gate = commands.add_parser("guard")
    gate.add_argument("tag")
    free = commands.add_parser("unused")
    free.add_argument("repository")
    free.add_argument("tag")
    check = commands.add_parser("inspect")
    for argument in ["repository", "tag", "digest", "revision", "output"]:
        check.add_argument(argument)
    args = parser.parse_args()
    if args.command == "guard":
        guard(args.tag)
    elif args.command == "unused":
        unused(args.repository, args.tag)
    else:
        inspect(args.repository, args.tag, args.digest, args.revision, args.output)


if __name__ == "__main__":
    main()
