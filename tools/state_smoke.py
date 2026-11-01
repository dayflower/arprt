"""Renderer-state acceptance using real Jobs, mounted PVCs and BYOS delivery."""

import hashlib
import json
import time
import urllib.parse
from pathlib import Path

from ondemand_smoke import Acceptance, kubectl, require

ROOT = Path(__file__).resolve().parents[1]


def run(namespace, headers, forwarded, directory, wait, scenario):
    def apply(obj):
        path = directory / "state-resource.json"
        path.write_text(json.dumps(obj))
        kubectl("-n", namespace, "apply", "--validate=strict", "-f", str(path))
        path.unlink()

    case = Acceptance(namespace, apply, headers)
    # A separate read-only observer is colocated with the sole PVC writer.
    apply(
        {
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {"name": "state-observer"},
            "spec": {
                "automountServiceAccountToken": False,
                "securityContext": {
                    "runAsUser": 1000,
                    "runAsGroup": 1000,
                    "fsGroup": 1000,
                },
                "affinity": {
                    "podAffinity": {
                        "requiredDuringSchedulingIgnoredDuringExecution": [
                            {
                                "labelSelector": {
                                    "matchLabels": {"app.kubernetes.io/name": "arprt"}
                                },
                                "topologyKey": "kubernetes.io/hostname",
                            }
                        ]
                    }
                },
                "containers": [
                    {
                        "name": "observer",
                        "image": "busybox:1.37.0",
                        "command": ["sleep", "3600"],
                        "volumeMounts": [
                            {"name": "data", "mountPath": "/data", "readOnly": True}
                        ],
                    }
                ],
                "volumes": [
                    {
                        "name": "data",
                        "persistentVolumeClaim": {"claimName": "arprt-arprt-data"},
                    }
                ],
            },
        }
    )
    kubectl(
        "-n",
        namespace,
        "wait",
        "--for=condition=Ready",
        "pod/state-observer",
        "--timeout=120s",
    )

    def read(path):
        result = kubectl(
            "-n",
            namespace,
            "exec",
            "state-observer",
            "--",
            "cat",
            "/data/" + path,
            check=False,
        )
        return json.loads(result.stdout) if result.returncode == 0 else {}

    program = (ROOT / "examples/renderers/job/cursor/renderer.py").read_text()

    # Keep modes outside the Screen definition to test omitted/invalid results
    # without implicitly resetting state through an InputRevision change.
    def mode(value):
        apply(
            {
                "apiVersion": "v1",
                "kind": "ConfigMap",
                "metadata": {"name": "cursor-renderer"},
                "data": {"STATE_RENDERER": program, "CURSOR_MODE": value},
            }
        )

    mode("same-png")
    command = "import os,sys; sys.argv=['renderer','--mode',os.environ['CURSOR_MODE'],'--delay','2']; exec(os.environ['STATE_RENDERER'])"
    apply(
        {
            "apiVersion": "arprt.dayflower.github.io/v1alpha1",
            "kind": "ArprtScreen",
            "metadata": {"name": "cursor"},
            "spec": {
                "refreshInterval": "2s",
                "source": {
                    "job": {
                        "state": {},
                        "renderer": {
                            "image": "python:3.13-alpine",
                            "command": ["python3", "-c"],
                            "args": [command],
                            "envFrom": [{"configMapRef": {"name": "cursor-renderer"}}],
                        },
                    }
                },
            },
        }
    )
    case.assign("cursor")
    device_uid = case.get("arprtdevice", "living-room")["metadata"]["uid"]
    screen_uid = case.get("arprtscreen", "cursor")["metadata"]["uid"]
    record_path = "corners/" + hashlib.sha256(device_uid.encode()).hexdigest() + ".json"
    pair_key = hashlib.sha256((namespace + "\0" + screen_uid).encode()).hexdigest()
    job_path = (
        "jobs/"
        + hashlib.sha256((namespace + "/living-room/cursor").encode()).hexdigest()
        + ".json"
    )

    def record():
        return read(record_path)

    def pair():
        return record().get("pairs", {}).get(pair_key, {})

    def candidate():
        ref = pair().get("candidateRef")
        return read("manifests/" + ref + ".json") if ref else {}

    wait(lambda: candidate(), 120)
    first = candidate()
    require(
        first["nextState"] == {"cursor": 1} and pair()["generation"] == 0,
        "Job completion committed state",
    )
    first_id = first["metadata"]["attemptID"]
    require(
        "rendererState" not in read("runs/" + first_id + "/context.json"),
        "initial Context contains state",
    )
    time.sleep(5)
    require(
        candidate() == first and pair()["generation"] == 0,
        "interval replaced unconsumed candidate",
    )

    if scenario == "playlist":
        case.playlist(["cursor", "cursor"])
        case.assign("skip", playlist=True)
    case.restart()  # Startup recovery and GC must retain the unconsumed manifest.
    require(candidate() == first, "restart/GC lost unconsumed candidate")
    with forwarded(namespace, 8080, directory) as base:
        response = case.display(base)
        require(
            response["status"] == 0 and pair()["generation"] == 1,
            "display did not commit candidate",
        )
        image_path = urllib.parse.urlsplit(response["image_url"]).path
        issued = case.fetch(base, image_path)
        wait(lambda: candidate(), 90)
        second = candidate()
        second_id = second["metadata"]["attemptID"]
        require(
            second_id != first_id
            and second["metadata"]["imageDigest"] == first["metadata"]["imageDigest"],
            "same PNG did not produce a distinct candidate",
        )
        require(
            read("runs/" + second_id + "/context.json")["rendererState"]
            == {"cursor": 1},
            "next Job did not snapshot committed cursor",
        )
        require(
            case.fetch(base, image_path) == issued and pair()["generation"] == 1,
            "PNG fetch consumed candidate",
        )
        if scenario == "playlist":
            require(
                case.display(base) == response and pair()["generation"] == 1,
                "replay consumed next candidate",
            )
            case.expire()
        mode("invalid" if scenario == "single" else "missing")
        require(
            case.display(base)["status"] == 0 and pair()["generation"] == 2,
            "second candidate did not commit",
        )
        if scenario == "single":
            wait(
                lambda: (
                    read(job_path).get("reason") == "CollectionFailed"
                    and not read(job_path).get("active")
                ),
                90,
            )
            require(
                not pair().get("candidateRef") and pair()["generation"] == 2,
                "invalid output advanced state",
            )
            require(
                case.display(base)["status"] == 0 and pair()["generation"] == 2,
                "fallback advanced state",
            )
        else:
            wait(lambda: candidate(), 90)
            require(
                candidate()["nextState"] == {"cursor": 2},
                "missing result failed to preserve input",
            )
            if scenario == "playlist":
                case.expire()
            mode("reset")
            require(
                case.display(base)["status"] == 0 and pair()["generation"] == 3,
                "missing result did not consume a generation",
            )
            wait(lambda: candidate(), 90)
            require("nextState" not in candidate(), "null result did not propose clear")
            if scenario == "playlist":
                case.expire()
            mode("same-png")
            require(
                case.display(base)["status"] == 0 and pair()["generation"] == 4,
                "clear result did not commit",
            )
            wait(lambda: candidate(), 90)
            current = candidate()
            require(
                "rendererState"
                not in read(
                    "runs/" + current["metadata"]["attemptID"] + "/context.json"
                ),
                "cleared state leaked into next Context",
            )
    mode("same-png")
    before = pair()
    case.patch(
        "arprtscreen",
        "cursor",
        {"spec": {"source": {"job": {"state": {"resetToken": "restart-cursor"}}}}},
    )
    wait(lambda: pair().get("epoch", 0) > before["epoch"] and candidate(), 90)
    require(
        pair()["generation"] == 0 and candidate()["nextState"] == {"cursor": 1},
        "resetToken retained cursor",
    )
    case.restart()
    with forwarded(namespace, 8080, directory) as base:
        require(
            case.fetch(base, image_path) == issued,
            "reset/restart GC removed issued URL",
        )
    if scenario == "operations":
        previous_epoch = pair()["epoch"]
        case.patch(
            "arprtscreen",
            "cursor",
            {
                "spec": {
                    "refreshInterval": None,
                    "source": {"job": {"executionMode": "OnDemand", "timeout": "10s"}},
                }
            },
        )
        wait(
            lambda: (
                pair().get("epoch", 0) > previous_epoch
                and not pair().get("candidateRef")
            ),
            90,
        )
        with forwarded(namespace, 8080, directory) as base:
            for generation in (1, 2):
                require(
                    case.display(base)["status"] == 0
                    and pair()["generation"] == generation,
                    "OnDemand did not commit the next cursor",
                )
                committed = read("manifests/" + pair()["stateRef"] + ".json")
                require(
                    committed["nextState"] == {"cursor": generation},
                    "OnDemand snapshot lost committed cursor",
                )
                snapshot = read(
                    "runs/" + committed["metadata"]["attemptID"] + "/context.json"
                )
                require(
                    snapshot.get("rendererState")
                    == (None if generation == 1 else {"cursor": generation - 1}),
                    "OnDemand Context differs from committed input",
                )
    logs = kubectl("-n", namespace, "logs", "deployment/arprt-arprt").stdout
    require(
        '"nextState"' not in logs and '"rendererState"' not in logs,
        "logs expose renderer state",
    )
    print(
        "PASS: renderer state "
        + scenario
        + ": real mounts, candidate retention, cursor snapshots, same PNG, restart/GC, reset and issued URL retention",
        flush=True,
    )
