"""Real-container acceptance shared by the Job and Playlist smoke suites.

Run only inside their disposable namespaces. Never use real device identities.
"""

import concurrent.futures
import hashlib
import http.client
import importlib.util
import json
import threading
import time
import urllib.parse
import urllib.request
from pathlib import Path

import helm_install

kubectl = helm_install.kubectl
OUTPUT = 'printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Acceptance:
    def __init__(self, namespace, apply, headers):
        self.namespace, self.apply, self.headers = namespace, apply, headers
        self.prefix = f"/arprt/devices/{namespace}/living-room"

    def get(self, kind, *args):
        return json.loads(
            kubectl("-n", self.namespace, "get", kind, *args, "-o", "json").stdout
        )

    def patch(self, kind, name, value, *args):
        kubectl(
            "-n",
            self.namespace,
            "patch",
            kind,
            name,
            "--type=merge",
            "-p",
            json.dumps(value),
            *args,
        )

    def screen(self, name, command=OUTPUT, timeout="10s", image="busybox:1.37.0"):
        self.apply(
            {
                "apiVersion": "arprt.dayflower.github.io/v1alpha1",
                "kind": "ArprtScreen",
                "metadata": {"name": name},
                "spec": {
                    "source": {
                        "job": {
                            "executionMode": "OnDemand",
                            "timeout": timeout,
                            "renderer": {
                                "image": image,
                                "command": ["sh", "-ec"],
                                "args": [command],
                                "envFrom": [{"configMapRef": {"name": "input"}}],
                            },
                        }
                    }
                },
            }
        )

    def assign(self, name, playlist=False):
        self.patch(
            "arprtdevice",
            "living-room",
            {
                "spec": {
                    "screenRef": None if playlist else {"name": name},
                    "playlistRef": {"name": name} if playlist else None,
                    "rotation": 0,
                }
            },
        )

    def fetch(self, base, path):
        with urllib.request.urlopen(
            urllib.request.Request(base + path, headers=self.headers), timeout=16
        ) as response:
            require(response.status == 200, "unexpected BYOS HTTP status")
            return response.read()

    def display(self, base):
        started = time.monotonic()
        response = json.loads(self.fetch(base, self.prefix + "/api/display"))
        elapsed = time.monotonic() - started
        require(elapsed < 13.75, "display exceeded application delivery allowance")
        print(f"OnDemand display: status={response['status']} elapsed={elapsed:.3f}s")
        return response

    def parallel(self, base, count=24):
        # A delayed renderer keeps all requests in the same shared flight.
        barrier = threading.Barrier(count)

        def request():
            barrier.wait(timeout=10)
            return self.display(base)

        with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
            futures = [pool.submit(request) for _ in range(count)]
            results = [future.result(timeout=20) for future in futures]
        require(all(r == results[0] for r in results), "shared flight responses differ")
        return results[0]

    def restart(self):
        kubectl("-n", self.namespace, "rollout", "restart", "deployment/arprt-arprt")
        kubectl(
            "-n",
            self.namespace,
            "rollout",
            "status",
            "deployment/arprt-arprt",
            "--timeout=180s",
        )

    def expire(self):
        self.patch(
            "arprtdevice",
            "living-room",
            {
                "status": {
                    "screenCycle": {
                        "entryIndex": self.get("arprtdevice", "living-room")["status"][
                            "screenCycle"
                        ]["entryIndex"],
                        "servedAt": "2000-01-01T00:00:00.000000Z",
                    }
                }
            },
            "--subresource=status",
        )

    def playlist(self, names, timeout="10s"):
        self.apply(
            {
                "apiVersion": "arprt.dayflower.github.io/v1alpha1",
                "kind": "ArprtPlaylist",
                "metadata": {"name": "skip"},
                "spec": {
                    "failurePolicy": "Skip",
                    "renderTimeout": timeout,
                    "screens": [{"name": name} for name in names],
                },
            }
        )


def single(namespace, apply, headers, read, forwarded, directory, wait, installation):
    """Exercise real mounting, shared requests, failure, stopping and recovery."""
    case = Acceptance(namespace, apply, headers)
    case.screen("ondemand", "sleep 2; " + OUTPUT)
    case.assign("ondemand")
    key = hashlib.sha256(f"{namespace}/living-room/ondemand".encode()).hexdigest()

    def state():
        data = read("jobs/" + key + ".json")
        return json.loads(data) if data else {}

    def settled():
        return not state().get("active")

    def idle():
        # Reassignment can leave a Scheduled renderer stopping in the suite's
        # only slot. Measure warm execution only after that earlier work exits.
        for kind in ("jobs", "pods"):
            for obj in case.get(kind, "-l", "arprt.dayflower.github.io/renderer=true")[
                "items"
            ]:
                if obj["metadata"].get("deletionTimestamp"):
                    return False
                if kind == "pods":
                    if obj.get("status", {}).get("phase") not in (
                        "Succeeded",
                        "Failed",
                    ):
                        return False
                elif not any(
                    c["type"] in ("Complete", "Failed") and c["status"] == "True"
                    for c in obj.get("status", {}).get("conditions", [])
                ):
                    return False
        return True

    wait(idle, 90)
    wait(settled, 60)
    before = state().get("sequence", 0)
    time.sleep(2)
    require(state().get("sequence", 0) == before, "assignment launched OnDemand Job")
    with forwarded(namespace, 8080, directory) as base:
        spec = importlib.util.spec_from_file_location(
            "image_smoke", Path(__file__).with_name("image-smoke.py")
        )
        resource_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(resource_module)
        resources = resource_module.Resources(namespace)
        resources.thread.start()
        try:
            first = case.parallel(base)
        finally:
            print(
                "OnDemand resources: " + json.dumps(resources.finish(), sort_keys=True)
            )
        require(
            first["status"] == 0,
            "warm real OnDemand Job failed: " + state().get("reason", "pending"),
        )
        wait(settled, 60)
        require(state()["sequence"] == before + 1, "parallel requests duplicated Job")
        path = urllib.parse.urlsplit(first["image_url"]).path
        original = case.fetch(base, path)
        require(original.startswith(b"\x89PNG"), "OnDemand image missing")
        sequence = state()["sequence"]
        case.fetch(base, path)
        require(state()["sequence"] == sequence, "image GET launched Job")
        second = case.display(base)
        require(
            second["filename"] == first["filename"], "identical bytes changed filename"
        )
        wait(settled, 60)
        require(state()["sequence"] == sequence + 1, "new display did not regenerate")

        def open_display():
            address = urllib.parse.urlsplit(base)
            connection = http.client.HTTPConnection(
                address.hostname, address.port, timeout=16
            )
            connection.request(
                "GET", case.prefix + "/api/display", headers=case.headers
            )
            return connection

        # The departing client must leave a joined waiter alive.
        sequence = state()["sequence"]
        departing = open_display()
        try:
            wait(lambda: state().get("active"), 5)
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                surviving = pool.submit(case.display, base)
                time.sleep(0.5)
                departing.close()
                require(
                    surviving.result(timeout=15)["status"] == 0,
                    "disconnect canceled peer",
                )
        finally:
            departing.close()
        wait(settled, 60)
        require(state()["sequence"] == sequence + 1, "disconnect duplicated shared Job")
        # With no waiter left, execution stops and does not retry autonomously.
        case.screen("ondemand", "sleep 20; " + OUTPUT)
        departing = open_display()
        try:
            wait(lambda: state().get("active"), 5)
        finally:
            departing.close()
        wait(settled, 60)
        sequence = state()["sequence"]
        require(
            state().get("reason") == "RequestCancelled",
            "last disconnect was not durable: " + state().get("reason", "pending"),
        )
        time.sleep(2)
        require(
            state()["sequence"] == sequence, "disconnect scheduled an autonomous retry"
        )
        require(case.fetch(base, path) == original, "disconnect changed issued image")

        for command in (
            "exit 7",
            "exit 0",
            "printf partial > /arprt/output.png",
            "sleep 20; " + OUTPUT,
        ):
            case.screen("ondemand", command, timeout="3s")
            response = case.display(base)
            require(
                response["status"] == 0 and response["filename"] == first["filename"],
                "failed OnDemand Job lost compatible fallback",
            )
            wait(settled, 60)
            require(state().get("reason"), "render failure was not recorded")
            require(case.fetch(base, path) == original, "failure changed issued image")

        # Ignore SIGTERM long enough to actually write output after timeout.
        # File existence alone must never resurrect this terminal execution.
        case.screen("ondemand", "trap '' TERM; sleep 8; " + OUTPUT, timeout="5s")
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            request = pool.submit(case.display, base)
            wait(lambda: state().get("active"), 10)
            late = state()["active"]["id"]
            require(
                request.result(timeout=15)["status"] == 0, "late render lost fallback"
            )
        wait(
            lambda: (
                kubectl(
                    "-n",
                    namespace,
                    "exec",
                    "observer",
                    "--",
                    "test",
                    "-s",
                    "/data/runs/" + late + "/output/output.png",
                    check=False,
                ).returncode
                == 0
            ),
            20,
        )
        wait(settled, 60)
        published = kubectl(
            "-n",
            namespace,
            "exec",
            "observer",
            "--",
            "sh",
            "-c",
            "cat /data/latest/*.json",
        ).stdout
        require(late not in published, "late output was published after timeout")
        require(case.fetch(base, path) == original, "late output changed issued bytes")

        # A cached image with an unavailable executable cannot start. This
        # exercises kubelet startup without depending on a registry/DNS timeout.
        wait(idle, 90)
        case.screen("startup", timeout="5s")
        case.patch(
            "arprtscreen",
            "startup",
            {
                "spec": {
                    "source": {
                        "job": {
                            "renderer": {"command": ["/arprt/missing-renderer"]},
                        }
                    }
                }
            },
        )
        case.assign("startup")
        require(
            case.display(base) == {"status": 202}, "startup delay exceeded its budget"
        )
        startup_key = hashlib.sha256(
            f"{namespace}/living-room/startup".encode()
        ).hexdigest()

        def startup_state():
            data = read("jobs/" + startup_key + ".json")
            return json.loads(data) if data else {}

        wait(
            lambda: startup_state().get("reason") and not startup_state().get("active"),
            90,
        )
        require(
            startup_state().get("reason") in ("JobFailed", "JobTimedOut"),
            "startup ended with " + startup_state().get("reason", "no terminal record"),
        )
        sequence = startup_state()["sequence"]
        time.sleep(2)
        require(
            startup_state()["sequence"] == sequence,
            "startup failure retried without a request",
        )

        # A cold image pull is measured independently; missing the budget is a
        # valid pending result, never a reason to increase production timeouts.
        case.screen("cold", image="busybox:1.36.1")
        case.assign("cold")
        cold = case.display(base)
        require(cold["status"] in (0, 202), "unexpected cold-start response")
        if cold["status"] == 202:
            require(cold == {"status": 202}, "cold timeout included an image")

        # Keep a real Pod in deletion with a test finalizer. A replacement must
        # not start while the same pair still owns the stopping Pod.
        # Re-establish fallback for this assignment; collection may remove an
        # unassigned pair's latest pointer while preserving its issued URLs.
        wait(idle, 90)
        case.screen("ondemand")
        case.assign("ondemand")
        require(
            case.display(base)["status"] == 0, "stopping test has no compatible base"
        )
        wait(settled, 60)
        case.screen("ondemand", "sleep 20; " + OUTPUT, timeout="5s")
        held = None
        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                request = pool.submit(case.display, base)
                wait(lambda: state().get("active"), 10)
                attempt = state()["active"]["id"]
                selector = "batch.kubernetes.io/job-name=arprt-render-" + attempt
                wait(lambda: case.get("pods", "-l", selector)["items"], 10)
                held = case.get("pods", "-l", selector)["items"][0]["metadata"]["name"]
                pod = case.get("pod", held)
                finalizers = pod["metadata"].get("finalizers", [])
                case.patch(
                    "pod",
                    held,
                    {
                        "metadata": {
                            "finalizers": finalizers + ["smoke.arprt.test/hold"],
                        }
                    },
                )
                require(
                    request.result(timeout=15)["status"] == 0, "timeout lost fallback"
                )
            wait(lambda: case.get("pod", held)["metadata"].get("deletionTimestamp"), 30)
            sequence = state()["sequence"]
            require(case.display(base)["status"] == 0, "stopping-pair fallback failed")
            require(
                state()["sequence"] == sequence, "replacement bypassed stopping Pod"
            )
            require(
                read("runs/" + attempt + "/context.json"), "GC removed stopping context"
            )
            require(case.fetch(base, path) == original, "stopping changed issued image")
            with forwarded(namespace, 8082, directory) as metrics_base:

                def maintenance_count():
                    metrics = case.fetch(metrics_base, "/metrics").decode()
                    return sum(
                        float(line.rsplit(" ", 1)[1])
                        for line in metrics.splitlines()
                        if line.startswith('arprt_maintenance_total{outcome="success"}')
                    )

                passes = maintenance_count()
                wait(lambda: maintenance_count() > passes, 75)
            require(
                read("runs/" + attempt + "/context.json"), "GC collected stopping run"
            )
            require(case.fetch(base, path) == original, "GC collected issued image")
            # A second pair must respect the suite's single global execution
            # slot while the first pair still has a Pod in deletion.
            case.screen("blocked", timeout="2s")
            case.assign("blocked")
            before_jobs = {j["metadata"]["uid"] for j in case.get("jobs")["items"]}
            require(case.display(base) == {"status": 202}, "stopping slot was bypassed")
            require(
                {j["metadata"]["uid"] for j in case.get("jobs")["items"]}.issubset(
                    before_jobs
                ),
                "slot waiting created another Job",
            )
        finally:
            if held:
                pod = case.get("pod", held)
                case.patch(
                    "pod",
                    held,
                    {
                        "metadata": {
                            "finalizers": [
                                f
                                for f in pod["metadata"].get("finalizers", [])
                                if f != "smoke.arprt.test/hold"
                            ]
                        }
                    },
                )
        wait(settled, 60)

    case.assign("ondemand")
    # Restart with an active request. Losing the request cannot turn the old
    # output into a success on startup, or schedule an autonomous retry.
    case.screen("ondemand", "sleep 20; " + OUTPUT)
    with forwarded(namespace, 8080, directory) as base:
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            request = pool.submit(case.display, base)
            wait(lambda: state().get("active"), 10)
            sequence = state()["sequence"]
            case.restart()
            try:
                request.result(timeout=20)
            except (OSError, ValueError):
                pass  # Rollout can close the forwarded connection.
    wait(settled, 60)
    time.sleep(2)
    require(state()["sequence"] == sequence, "restart retried OnDemand without request")
    require(state().get("reason"), "restart lost terminal outcome")
    # A normal Helm rollout retains new API fields, PVC, credentials and URL.
    identities = {
        (kind, name): case.get(kind, name)["metadata"]["uid"]
        for kind, name in (
            ("pvc", "arprt-arprt-data"),
            ("secret", "living-room-auth"),
            ("arprtdevice", "living-room"),
            ("arprtscreen", "ondemand"),
        )
    }
    installation.settings["display"] = {"deviceRefreshSeconds": 60}
    installation.upgrade()
    for (kind, name), uid in identities.items():
        require(
            case.get(kind, name)["metadata"]["uid"] == uid, "Helm changed retained UID"
        )
    with forwarded(namespace, 8080, directory) as base:
        require(
            case.fetch(base, path) == original,
            "restart/upgrade lost issued URL or auth",
        )
    print(
        "PASS: real OnDemand sharing, regeneration, fallback, cold pull outcome, stopping-Pod exclusion, restart and Helm retention"
    )


def playlist(namespace, apply, headers, token, forwarded, directory, wait):
    """Check Skip traversal and durable shared turns with real render Jobs."""
    case = Acceptance(namespace, apply, {**headers, "Access-Token": token})
    case.screen("skip-a", "exit 7", timeout="3s")
    case.screen("skip-b", "sleep 2; " + OUTPUT)
    case.screen("skip-c")
    case.playlist(["skip-a", "skip-b", "skip-b", "skip-c"])
    case.assign("skip", playlist=True)

    def turn():
        return (
            case.get("arprtdevice", "living-room")
            .get("status", {})
            .get("screenCycle", {})
        )

    def uid(name):
        return case.get("arprtscreen", name)["metadata"]["uid"]

    with forwarded(namespace, 8080, directory) as base:
        first = case.parallel(base)
        require(
            first["status"] == 0
            and turn().get("screenUID") == uid("skip-b")
            and turn().get("entryIndex") == 1,
            "Skip failed A did not select B",
        )
        committed = turn()
        jobs = {j["metadata"]["uid"] for j in case.get("jobs")["items"]}
        require(
            case.display(base) == first and turn() == committed,
            "Skip replay changed turn",
        )
        require(
            {j["metadata"]["uid"] for j in case.get("jobs")["items"]}.issubset(jobs),
            "Skip replay launched Job",
        )
        case.expire()
        require(
            case.parallel(base)["status"] == 0
            and turn().get("screenUID") == uid("skip-b")
            and turn().get("entryIndex") == 2,
            "second B occurrence did not render as a new shared turn",
        )
        require(
            turn()["servedAt"] != committed["servedAt"], "new occurrence reused turn"
        )
        later_jobs = {j["metadata"]["uid"] for j in case.get("jobs")["items"]}
        require(
            len(later_jobs - jobs) == 1,
            "second occurrence did not create exactly one shared Job",
        )
        replay = case.display(base)
    case.restart()
    with forwarded(namespace, 8080, directory) as base:
        require(case.display(base) == replay, "second B replay lost across restart")
        require(turn().get("entryIndex") == 2, "restart lost repeated position")
        case.expire()
        require(
            case.display(base)["status"] == 0
            and turn().get("screenUID") == uid("skip-c")
            and turn().get("entryIndex") == 3,
            "next turn did not start after successful B",
        )
        case.expire()
        require(
            case.display(base)["status"] == 0
            and turn().get("screenUID") == uid("skip-b"),
            "Skip wraparound did not retry failed A then B",
        )
        replay = case.display(base)
    case.restart()
    with forwarded(namespace, 8080, directory) as base:
        require(case.display(base) == replay, "Skip replay lost across restart")
        # The same Skip path must accept cached GET, current POST, prepared
        # Scheduled output and newly executed OnDemand output in one playlist.
        for name, source in {
            "mixed-get": {"http": {"url": "http://renderer:8080/render"}},
            "mixed-post": {
                "http": {"url": "http://renderer:8080/render", "method": "POST"}
            },
            "mixed-scheduled": {
                "job": {
                    "renderer": {
                        "image": "busybox:1.37.0",
                        "command": ["sh", "-ec"],
                        "args": [OUTPUT],
                        "envFrom": [{"configMapRef": {"name": "input"}}],
                    }
                }
            },
        }.items():
            apply(
                {
                    "apiVersion": "arprt.dayflower.github.io/v1alpha1",
                    "kind": "ArprtScreen",
                    "metadata": {"name": name},
                    "spec": {"source": source},
                }
            )
        mixed = [
            "mixed-get",
            "mixed-post",
            "mixed-get",
            "mixed-post",
            "mixed-scheduled",
            "skip-c",
            "skip-c",
        ]
        case.playlist(mixed)
        wait(
            lambda: any(
                c["type"] == "Ready"
                and c["status"] == "True"
                and c.get("reason") == "ImageReady"
                for c in case.get("arprtscreen", "mixed-scheduled")
                .get("status", {})
                .get("conditions", [])
            ),
            90,
        )
        for index, name in enumerate(mixed):
            case.expire()
            response = case.display(base)
            require(
                response["status"] == 0
                and turn().get("screenUID") == uid(name)
                and turn().get("entryIndex") == index,
                "mixed Skip selected the wrong source",
            )
            image_path = urllib.parse.urlsplit(response["image_url"]).path
            require(
                case.fetch(base, image_path).startswith(b"\x89PNG"),
                "mixed image unavailable",
            )
        # Previously successful B is now failing: Skip must not borrow stale B.
        case.screen("skip-b", "exit 9", timeout="3s")
        case.playlist(["skip-a", "skip-b"])
        before = turn()
        require(
            case.display(base) == {"status": 202}, "all-failed Skip served stale image"
        )
        require(turn() == before, "all-failed Skip committed a successful turn")
        case.playlist(["skip-a"])
        require(case.display(base) == {"status": 202}, "one-entry Skip used fallback")
        # Default 5s first timeout leaves only the remainder of 10s for B.
        case.screen("skip-a", "sleep 20; " + OUTPUT, timeout="5s")
        case.screen("skip-b")
        case.playlist(["skip-a", "skip-b"])
        result = case.display(base)
        require(
            result["status"] == 0 and turn().get("screenUID") == uid("skip-b"),
            "default first timeout left no successful second candidate",
        )
        case.screen("skip-a", "sleep 20; " + OUTPUT)
        case.expire()
        # Reset ordering so A, rather than the previous success, starts.
        case.playlist(["skip-a", "skip-b"], timeout="9s")
        before = turn()
        require(
            case.display(base) == {"status": 202}, "overall budget issued late success"
        )
        require(turn() == before, "budget exhaustion committed a successful turn")
    print(
        "PASS: real Skip failed A/successful B/repeated B/next C, wraparound, 24 shared waiters per occurrence, restart replay, stale rejection, one-entry and absolute budgets"
    )
