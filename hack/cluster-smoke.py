#!/usr/bin/env python3
"""Exercise Phase 1 on the current cluster, with an isolated namespace.

Requires kubectl and a controller image already available to the cluster. Refuses
to change pre-existing arprt CRDs. Removes only resources created by this run;
keeps newly installed CRDs if another namespace has begun using them.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
CRDS = ["trmnlscreens.arprt.dayflower.github.io", "trmnldevices.arprt.dayflower.github.io"]


def kubectl(*args, check=True):
    result = subprocess.run(["kubectl", *args], capture_output=True, text=True)
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result


def wait(check, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.5)
    raise RuntimeError("cluster check did not converge")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    namespace = "arprt-phase1-" + uuid.uuid4().hex[:8]
    created_crds = []
    created_namespace = False
    for name in CRDS:
        if kubectl("get", "crd", name, "--ignore-not-found", "-o", "name").stdout.strip():
            raise RuntimeError("refusing to change pre-existing arprt CRDs")
    try:
        for name in CRDS:
            plural, group = name.split(".", 1)
            kubectl("create", "--validate=strict", "-f", str(ROOT / "config/crd/bases" / (group + "_" + plural + ".yaml")))
            created_crds.append(name)
            kubectl("wait", "--for=condition=Established", "crd/" + name, "--timeout=30s")
        kubectl("create", "namespace", namespace)
        created_namespace = True
        with tempfile.TemporaryDirectory(prefix="arprt-smoke-") as temporary:
            directory = Path(temporary)
            image_name, image_tag = args.image.rsplit(":", 1)
            (directory / "kustomization.yaml").write_text(json.dumps({
                "apiVersion": "kustomize.config.k8s.io/v1beta1",
                "kind": "Kustomization",
                "namespace": namespace,
                "resources": [os.path.relpath(ROOT / "config/default", directory)],
                "images": [{"name": "arprt", "newName": image_name, "newTag": image_tag}],
            }))
            kubectl("apply", "--validate=strict", "-k", str(directory))
            kubectl("-n", namespace, "rollout", "status", "deployment/arprt", "--timeout=120s")
            for sample in ["screens.yaml", "device.yaml"]:
                kubectl("-n", namespace, "apply", "--validate=strict", "-f", str(ROOT / "config/samples" / sample))

            def conditions_ready():
                device = json.loads(kubectl("-n", namespace, "get", "trmnldevice", "living-room", "-o", "json").stdout)
                conditions = {c["type"]: c for c in device.get("status", {}).get("conditions", [])}
                return (conditions.get("Ready", {}).get("reason") == "RenderingNotImplemented"
                        and conditions.get("AssignmentReady", {}).get("status") == "True"
                        and device["status"]["observedGeneration"] == device["metadata"]["generation"])

            wait(conditions_ready)
            account = "system:serviceaccount:" + namespace + ":arprt"
            for ns, verb, resource, allowed in [
                (namespace, "list", CRDS[0], True),
                (namespace, "patch", CRDS[1] + "/status", True),
                ("default", "list", CRDS[0], False),
                (namespace, "get", "secrets", False),
                (namespace, "create", "jobs", False),
            ]:
                subresource = []
                if resource.endswith("/status"):
                    resource = resource.removesuffix("/status")
                    subresource = ["--subresource=status"]
                answer = kubectl("auth", "can-i", verb, resource, "--as=" + account, "-n", ns, *subresource, check=False)
                if (answer.stdout.strip() == "yes") != allowed:
                    raise RuntimeError("runtime RBAC does not match Phase 1 permissions")
            # Bind an ephemeral local port; inspect kubectl's chosen port.
            with (directory / "port-forward.log").open("w+") as log:
                forward = subprocess.Popen(["kubectl", "-n", namespace, "port-forward", "deployment/arprt", ":8081"], stdout=log, stderr=log)
                try:
                    def forwarded():
                        log.seek(0)
                        return "Forwarding from 127.0.0.1:" in log.read()
                    wait(forwarded, 15)
                    log.seek(0)
                    port = next(line.split(" ->")[0].rsplit(":", 1)[1] for line in log.read().splitlines() if line.startswith("Forwarding from 127.0.0.1:"))
                    for endpoint in ["healthz", "readyz"]:
                        with urllib.request.urlopen("http://127.0.0.1:" + port + "/" + endpoint, timeout=5) as response:
                            if response.status != 200:
                                raise RuntimeError("probe failed")
                finally:
                    forward.terminate()
                    forward.wait(timeout=10)
            # Rollout through Recreate checks graceful termination and PVC reopen.
            kubectl("-n", namespace, "rollout", "restart", "deployment/arprt")
            kubectl("-n", namespace, "rollout", "status", "deployment/arprt", "--timeout=120s")
            logs = kubectl("-n", namespace, "logs", "deployment/arprt").stdout
            if "Starting Phase 1 controller" not in logs:
                raise RuntimeError("controller did not restart")
        print("PASS: CRDs, samples, Deployment, PVC, conditions, probes, namespace RBAC and restart")
    finally:
        if created_namespace:
            kubectl("delete", "namespace", namespace, "--wait=true", "--timeout=120s")
        for name in created_crds:
            remaining = json.loads(kubectl("get", name, "--all-namespaces", "-o", "json").stdout)
            if remaining["items"]:
                print("Keeping CRD now used by another namespace: " + name)
            else:
                kubectl("delete", "crd", name, "--wait=true", "--timeout=30s")
        print("Test namespace and unused test CRDs removed")


if __name__ == "__main__":
    main()
