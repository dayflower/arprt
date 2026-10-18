#!/usr/bin/env python3
"""Exercise model profiles and integrated image admission under chart limits.

Uses the explicitly selected disposable cluster, isolated namespace and PVC.
Reports sampled kubelet memory separately from optional kernel cgroup peak.
"""

import argparse
import hashlib
import importlib.util
import json
import os
import platform
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import helm_install
import overlay_checks
from image_checks import GRAY4_NATIVE4, inspect

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "cluster_smoke", ROOT / "tools/cluster-smoke.py"
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
kubectl, wait, forwarded = smoke.kubectl, smoke.wait, smoke.forwarded
PROFILES = [
    ("og", 800, 480, "gray4", 90000),
    ("reterminal_e1001", 800, 480, "gray4", 750000),
    ("reterminal_e1002", 800, 480, "spectra6", 750000),
    ("reterminal_e1003", 1872, 1404, "gray16", 750000),
    ("reterminal_e1004", 1200, 1600, "spectra6", 750000),
]


def pod(namespace):
    pods = json.loads(
        kubectl(
            "-n",
            namespace,
            "get",
            "pods",
            "-l",
            "app.kubernetes.io/name=arprt",
            "-o",
            "json",
        ).stdout
    )["items"]
    assert len(pods) == 1
    return pods[0]


class Resources:
    def __init__(self, namespace):
        self.namespace, self.samples, self.errors = namespace, [], []
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.run)

    def run(self):
        while not self.stopped.is_set():
            try:
                current = pod(self.namespace)
                summary = json.loads(
                    kubectl(
                        "get",
                        "--raw",
                        "/api/v1/nodes/"
                        + current["spec"]["nodeName"]
                        + "/proxy/stats/summary",
                    ).stdout
                )
                entry = next(
                    p
                    for p in summary["pods"]
                    if p["podRef"]["uid"] == current["metadata"]["uid"]
                )
                self.samples.extend(
                    c["memory"]["usageBytes"]
                    for c in entry["containers"]
                    if c["name"] == "controller"
                )
            except (RuntimeError, KeyError, StopIteration, AssertionError) as error:
                self.errors.append(type(error).__name__)
            self.stopped.wait(1)

    def finish(self):
        self.stopped.set()
        self.thread.join(timeout=30)
        assert not self.thread.is_alive() and self.samples, (
            "resource sampling unavailable"
        )
        current = pod(self.namespace)
        status = current["status"]["containerStatuses"][0]
        assert status["restartCount"] == 0 and status["ready"], (
            "controller restarted or became unready"
        )
        result = {
            "sampled_memory_max_bytes": max(self.samples),
            "sample_count": len(self.samples),
            "sampling_errors": self.errors,
            "restart_count": status["restartCount"],
        }
        # Only the disposable kind wrapper opts into host Docker access. Locate
        # this controller's cgroup, never a node-wide or unrelated process peak.
        if os.environ.get("ARPRT_KIND_RESOURCE_PROBE") == "true":
            node = current["spec"]["nodeName"]
            container = status["containerID"].split("://", 1)[1]
            paths = subprocess.check_output(
                [
                    "docker",
                    "exec",
                    node,
                    "find",
                    "/sys/fs/cgroup",
                    "-name",
                    "*" + container + "*",
                    "-type",
                    "d",
                ],
                text=True,
            ).splitlines()
            assert len(paths) == 1, "controller cgroup not found"
            for key, filename in [
                ("memory_peak_bytes", "memory.peak"),
                ("memory_events", "memory.events"),
                ("cpu_max", "cpu.max"),
                ("memory_max", "memory.max"),
            ]:
                value = subprocess.check_output(
                    ["docker", "exec", node, "cat", paths[0] + "/" + filename],
                    text=True,
                ).strip()
                result[key] = (
                    int(value) if key in ("memory_peak_bytes", "memory_max") else value
                )
            assert result["memory_peak_bytes"] < 512 << 20
            assert (
                result["memory_max"] == 512 << 20
                and result["cpu_max"] == "50000 100000"
            )
            assert all(
                line in result["memory_events"].splitlines()
                for line in ("oom 0", "oom_kill 0")
            )
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    helm_install.arguments(parser)
    args = parser.parse_args()
    namespace = "arprt-image-" + uuid.uuid4().hex[:8]
    report = {
        "architecture": platform.machine(),
        "image": args.image,
        "gray4_native4": GRAY4_NATIVE4,
        "cases": [],
        "cpu_limit": "500m",
        "memory_limit_bytes": 512 << 20,
        "source_revision": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], text=True
        ).strip(),
        "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    }
    with tempfile.TemporaryDirectory(prefix="arprt-image-smoke-") as temporary:
        directory = Path(temporary)
        installation = helm_install.Installation(args, namespace, directory)
        resources = None
        try:
            installation.install()

            def apply(obj):
                path = directory / "resource.json"
                path.write_text(json.dumps(obj))
                kubectl("-n", namespace, "apply", "--validate=strict", "-f", path)

            def patch(kind, name, spec):
                kubectl(
                    "-n",
                    namespace,
                    "patch",
                    kind,
                    name,
                    "--type=merge",
                    "-p",
                    json.dumps({"spec": spec}),
                )

            kubectl(
                "-n",
                namespace,
                "create",
                "configmap",
                "renderer",
                "--from-file=renderer.py="
                + str(ROOT / "examples/renderers/http/palette/renderer.py"),
                "--from-file=image_inputs.py=" + str(ROOT / "tools/image_inputs.py"),
            )
            apply(
                {
                    "apiVersion": "apps/v1",
                    "kind": "Deployment",
                    "metadata": {"name": "renderer"},
                    "spec": {
                        "replicas": 1,
                        "selector": {"matchLabels": {"app": "renderer"}},
                        "template": {
                            "metadata": {"labels": {"app": "renderer"}},
                            "spec": {
                                "automountServiceAccountToken": False,
                                "containers": [
                                    {
                                        "name": name,
                                        "image": "python:3.13-alpine",
                                        "command": ["python3", "/app/" + script],
                                        "readinessProbe": {
                                            "tcpSocket": {"port": port},
                                            "periodSeconds": 1,
                                        },
                                        "volumeMounts": [
                                            {
                                                "name": "script",
                                                "mountPath": "/app",
                                                "readOnly": True,
                                            }
                                        ],
                                    }
                                    for name, script, port in [
                                        ("palette", "renderer.py", 8000),
                                        ("maximum", "image_inputs.py", 8001),
                                    ]
                                ],
                                "volumes": [
                                    {
                                        "name": "script",
                                        "configMap": {"name": "renderer"},
                                    }
                                ],
                            },
                        },
                    },
                }
            )
            apply(
                {
                    "apiVersion": "v1",
                    "kind": "Service",
                    "metadata": {"name": "renderer"},
                    "spec": {
                        "selector": {"app": "renderer"},
                        "ports": [
                            {"name": str(port), "port": port} for port in (8000, 8001)
                        ],
                    },
                }
            )
            kubectl(
                "-n",
                namespace,
                "rollout",
                "status",
                "deployment/renderer",
                "--timeout=180s",
            )
            current = pod(namespace)
            node = json.loads(
                kubectl("get", "node", current["spec"]["nodeName"], "-o", "json").stdout
            )
            report["node_architecture"] = node["status"]["nodeInfo"]["architecture"]
            report["kubernetes_version"] = node["status"]["nodeInfo"]["kubeletVersion"]
            report["controller_image_id"] = current["status"]["containerStatuses"][0][
                "imageID"
            ]
            limits = current["spec"]["containers"][0]["resources"]["limits"]
            assert limits == {"cpu": "500m", "memory": "512Mi"}
            resources = Resources(namespace)
            resources.thread.start()
            api = "arprt.dayflower.github.io/v1alpha1"

            def screen(name, mode="GET", encoding="rgb", overlay=False):
                source = {
                    "http": {
                        "url": "http://renderer:8000/?encoding=" + encoding,
                        "method": mode,
                    }
                }
                if mode == "Job":
                    source = {
                        "job": {
                            "renderer": {
                                "image": "python:3.13-alpine",
                                "command": [
                                    "python3",
                                    "-c",
                                    'import os; exec(os.environ["RENDERER_SCRIPT"])',
                                ],
                                "args": [
                                    "--context",
                                    "/arprt/context.json",
                                    "--output",
                                    "/arprt/output.png",
                                    "--encoding",
                                    encoding,
                                ],
                                "env": [
                                    {
                                        "name": "RENDERER_SCRIPT",
                                        "valueFrom": {
                                            "configMapKeyRef": {
                                                "name": "renderer",
                                                "key": "renderer.py",
                                            }
                                        },
                                    }
                                ],
                            }
                        }
                    }
                value = {
                    "source": source,
                    "overlay": {
                        "enabled": overlay,
                        "items": ["battery", "wifi"],
                        "scale": 1,
                    },
                }
                if mode != "POST":
                    value["refreshInterval"] = "1h"
                apply(
                    {
                        "apiVersion": api,
                        "kind": "ArprtScreen",
                        "metadata": {"name": name},
                        "spec": value,
                    }
                )

            def device(name, screen_name, target=None):
                value = {
                    "authSecretRef": {"name": "living-room-auth"},
                    "screenRef": {"name": screen_name},
                }
                if target:
                    value["display"] = target
                apply(
                    {
                        "apiVersion": api,
                        "kind": "ArprtDevice",
                        "metadata": {"name": name},
                        "spec": value,
                    }
                )

            sessions, issued = {}, []
            with forwarded(namespace, 8080, directory) as base:

                def fetch(path, headers, allow_overload=False):
                    try:
                        with urllib.request.urlopen(
                            urllib.request.Request(base + path, headers=headers),
                            timeout=90,
                        ) as response:
                            assert response.status == 200
                            return response.read()
                    except urllib.error.HTTPError as error:
                        if not allow_overload or error.code != 503:
                            raise
                        body = error.read()
                        assert json.loads(body) == {"status": 503}
                        return body

                def session(name, model):
                    if name not in sessions:
                        headers = {
                            "ID": "02:00:00:00:01:" + format(len(sessions) + 1, "02x"),
                            "Model": model,
                            "Percent-Charged": "20",
                            "RSSI": "-70",
                        }
                        path = "/arprt/devices/" + namespace + "/" + name
                        headers["Access-Token"] = json.loads(
                            fetch(path + "/api/setup", headers)
                        )["api_key"]
                        sessions[name] = path, headers
                    return sessions[name]

                def display(name, model, allow_overload=False):
                    path, headers = session(name, model)
                    return json.loads(
                        fetch(path + "/api/display", headers, allow_overload)
                    )

                def ready(name, model):
                    result = {}

                    def check():
                        result.update(display(name, model))
                        return result.get("status") == 0

                    wait(check, 90)
                    return result

                for model, width, height, palette, limit in PROFILES:
                    for mode in ("GET", "POST", "Job"):
                        name = model.replace("_", "-") + "-" + mode.lower()
                        started = time.monotonic()
                        screen(name, mode)
                        device(name, name)
                        result = ready(name, model)
                        path, headers = session(name, model)
                        image_path = urllib.parse.urlsplit(result["image_url"]).path
                        image = fetch(image_path, headers)
                        inspect(image, width, height, palette, limit, generated=True)
                        issued.append((image_path, headers, image))
                        patch("arprtscreen", name, {"overlay": {"enabled": True}})
                        composite = ready(name, model)
                        drawn = fetch(
                            urllib.parse.urlsplit(composite["image_url"]).path, headers
                        )
                        overlay_checks.assert_overlay(
                            image,
                            drawn,
                            width=40,
                            image_width=width,
                            image_height=height,
                            palette=palette,
                            png_byte_limit=limit,
                        )
                        if mode in ("GET", "Job"):
                            assert (
                                display(name, model)["filename"]
                                == composite["filename"]
                            )
                        report["cases"].append(
                            {
                                "model": model,
                                "source": mode,
                                "seconds": round(time.monotonic() - started, 3),
                                "encoded_bytes": len(image),
                            }
                        )
                        print(
                            "PASS:",
                            model,
                            mode,
                            "normalization, overlay and authenticated fetch",
                            flush=True,
                        )

                # Exercise policy edits, explicit unknown-model capabilities and
                # replay on a color device rather than inheriting OG-only proof.
                for dithering in ("atkinson", "floyd-steinberg", "bayer", "none"):
                    patch(
                        "arprtscreen",
                        "og-get",
                        {
                            "imageProcessing": {
                                "resizeMode": "stretch",
                                "dithering": dithering,
                            }
                        },
                    )
                    result = ready("og-get", "og")
                    _, headers = session("og-get", "og")
                    inspect(
                        fetch(urllib.parse.urlsplit(result["image_url"]).path, headers),
                        800,
                        480,
                        "gray4",
                        90000,
                    )
                screen("override")
                device(
                    "override",
                    "override",
                    {
                        "width": 321,
                        "height": 241,
                        "palette": "gray16",
                        "pngByteLimit": 750000,
                    },
                )
                result = ready("override", "future-model")
                _, headers = session("override", "future-model")
                original_path = urllib.parse.urlsplit(result["image_url"]).path
                original = fetch(original_path, headers)
                inspect(original, 321, 241, "gray16", 750000, generated=True)
                issued.append((original_path, headers, original))
                patch("arprtdevice", "override", {"display": {"palette": "spectra6"}})
                changed = ready("override", "future-model")
                assert changed["filename"] != result["filename"]
                inspect(
                    fetch(urllib.parse.urlsplit(changed["image_url"]).path, headers),
                    321,
                    241,
                    "spectra6",
                    750000,
                    generated=True,
                )
                patch("arprtdevice", "override", {"display": {"palette": None}})
                assert display("override", "future-model") == {"status": 202}
                assert fetch(original_path, headers) == original
                patch("arprtdevice", "override", {"display": {"palette": "spectra6"}})
                apply(
                    {
                        "apiVersion": api,
                        "kind": "ArprtPlaylist",
                        "metadata": {"name": "spectra-cycle"},
                        "spec": {
                            "screens": [
                                {"name": "reterminal-e1002-" + mode}
                                for mode in ("get", "post", "job")
                            ]
                        },
                    }
                )
                apply(
                    {
                        "apiVersion": api,
                        "kind": "ArprtDevice",
                        "metadata": {"name": "spectra-cycle"},
                        "spec": {
                            "authSecretRef": {"name": "living-room-auth"},
                            "playlistRef": {"name": "spectra-cycle"},
                        },
                    }
                )
                replay = ready("spectra-cycle", "reterminal_e1002")
                assert display("spectra-cycle", "reterminal_e1002") == replay

                # Renderer dithering and arbitrary byte-compatible metadata must
                # survive the pipeline, even with a different controller policy.
                name = "indexed"
                screen(name, encoding="indexed")
                device(name, name)
                result = ready(name, "reterminal_e1002")
                _, headers = session(name, "reterminal_e1002")
                actual = fetch(urllib.parse.urlsplit(result["image_url"]).path, headers)
                with forwarded(
                    namespace, 8000, directory, "service/renderer"
                ) as renderer:
                    expected = urllib.request.urlopen(
                        renderer
                        + "/?width=800&height=480&palette=spectra6&encoding=indexed"
                    ).read()
                assert actual == expected
                patch("arprtscreen", name, {"imageProcessing": {"dithering": "bayer"}})
                assert (
                    fetch(
                        urllib.parse.urlsplit(
                            ready(name, "reterminal_e1002")["image_url"]
                        ).path,
                        headers,
                    )
                    == expected
                )
                patch("arprtdevice", name, {"display": {"pngByteLimit": len(expected)}})
                bounded = display(name, "reterminal_e1002")
                if bounded["status"] == 0:
                    normalized = fetch(
                        urllib.parse.urlsplit(bounded["image_url"]).path, headers
                    )
                    assert normalized != expected
                    inspect(normalized, 800, 480, "spectra6", len(expected))
                else:
                    assert bounded == {"status": 202}
                patch(
                    "arprtdevice",
                    name,
                    {"display": {"pngByteLimit": len(expected) + 1}},
                )
                assert (
                    fetch(
                        urllib.parse.urlsplit(
                            ready(name, "reterminal_e1002")["image_url"]
                        ).path,
                        headers,
                    )
                    == expected
                )

                # Eight distinct callers retain metadata while one shared lease
                # receives/converts/overlays/serves. Include real PVC collection.
                load_targets = []
                for i in range(8):
                    name = "load-" + str(i)
                    screen(name, "POST" if i % 2 else "GET", overlay=True)
                    patch(
                        "arprtscreen",
                        name,
                        {
                            "source": {
                                "http": {
                                    "url": "http://renderer:8001/"
                                    + ("plain" if i % 2 else "adam7")
                                }
                            }
                        },
                    )
                    profile = (
                        PROFILES[3]
                        if i == 1
                        else PROFILES[4]
                        if i == 2
                        else PROFILES[2]
                    )
                    model, width, height, palette, limit = profile
                    override = (
                        {
                            "width": 2048,
                            "height": 2048,
                            "palette": "gray16",
                            "pngByteLimit": 750000,
                        }
                        if i == 0
                        else None
                    )
                    if override:
                        width, height, palette = 2048, 2048, "gray16"
                    device(name, name, override)
                    session(name, model)
                    load_targets.append((name, model, width, height, palette, limit))
                screen("load-job", "Job", overlay=True)
                patch(
                    "arprtscreen",
                    "load-job",
                    {
                        "source": {
                            "job": {
                                "renderer": {
                                    "args": ["--output", "/arprt/output.png"],
                                    "env": [
                                        {
                                            "name": "RENDERER_SCRIPT",
                                            "valueFrom": {
                                                "configMapKeyRef": {
                                                    "name": "renderer",
                                                    "key": "image_inputs.py",
                                                }
                                            },
                                        }
                                    ],
                                }
                            }
                        }
                    },
                )
                device("load-job", "load-job")
                session("load-job", "reterminal_e1002")
                started = time.monotonic()
                with ThreadPoolExecutor(max_workers=9) as executor:
                    outcomes = list(
                        executor.map(
                            lambda item: display(item[0], item[1], allow_overload=True),
                            load_targets + [("load-job", "reterminal_e1002")],
                        )
                    )
                assert all(result.get("status") in (0, 202, 503) for result in outcomes)
                # A deadline failure is a valid bounded outcome. Existing normal
                # rendering and issued bytes must remain available afterwards.
                for i, outcome in enumerate(outcomes):
                    if outcome["status"] == 0:
                        name, model, width, height, palette, limit = (
                            load_targets[i] if i < 8 else ("load-job", *PROFILES[2])
                        )
                        _, headers = session(name, model)
                        inspect(
                            fetch(
                                urllib.parse.urlsplit(outcome["image_url"]).path,
                                headers,
                            ),
                            width,
                            height,
                            palette,
                            limit,
                        )
                ready("indexed", "reterminal_e1002")
                report["load"] = {
                    "callers": 9,
                    "input_bytes": 32 << 20,
                    "input_pixels": 16777216,
                    "seconds": round(time.monotonic() - started, 3),
                    "statuses": [result["status"] for result in outcomes],
                }
                job_condition = {}

                def collected():
                    current = json.loads(
                        kubectl(
                            "-n",
                            namespace,
                            "get",
                            "arprtscreen",
                            "load-job",
                            "-o",
                            "json",
                        ).stdout
                    )
                    for condition in current.get("status", {}).get("conditions", []):
                        if (
                            condition["type"] == "Ready"
                            and condition.get("observedGeneration")
                            == current["metadata"]["generation"]
                            and condition["reason"]
                            in ("ImageReady", "CollectionFailed")
                        ):
                            job_condition.update(condition)
                            return True
                    return False

                wait(collected, 150)
                report["load"]["job_reason"] = job_condition["reason"]
                report["replay"] = {"model": "reterminal_e1002", "passed": True}
                report["resources"] = resources.finish()
                resources = None
            kubectl("-n", namespace, "rollout", "restart", "deployment/arprt-arprt")
            kubectl(
                "-n",
                namespace,
                "rollout",
                "status",
                "deployment/arprt-arprt",
                "--timeout=180s",
            )
            with forwarded(namespace, 8080, directory) as base:
                for path, headers, image in issued:
                    assert fetch(path, headers) == image, "restart changed issued bytes"
                assert display("spectra-cycle", "reterminal_e1002") == replay, (
                    "restart lost Spectra cycle replay"
                )
            report["result"] = "passed"
            print("IMAGE_ACCEPTANCE " + json.dumps(report, sort_keys=True), flush=True)
        finally:
            if resources is not None:
                try:
                    report["resources"] = resources.finish()
                except Exception as error:
                    report["resource_error"] = str(error)
            report.setdefault("result", "failed")
            if os.environ.get("ARPRT_IMAGE_ACCEPTANCE_OUTPUT"):
                output = Path(os.environ["ARPRT_IMAGE_ACCEPTANCE_OUTPUT"])
                output.parent.mkdir(parents=True, exist_ok=True)
                output.write_text(json.dumps(report, indent=2) + "\n")
            installation.cleanup()


if __name__ == "__main__":
    main()
