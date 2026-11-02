"""Exercise standard ConfigMap/Secret files through real renderer Jobs."""

import hashlib
import json


def run(apply, screen, state, latest, read, kubectl, wait, namespace):
    def resource(kind, name, data):
        obj = {"apiVersion": "v1", "kind": kind, "metadata": {"name": name}}
        obj["stringData" if kind == "Secret" else "data"] = data
        apply(obj)

    resource(
        "ConfigMap", "files-config", {"config.json": "config-v1", "unused": "skip"}
    )
    resource("ConfigMap", "files-all", {"all.txt": "all-config"})
    resource("Secret", "files-auth", {"token": "synthetic-v1", "unused": "skip-secret"})
    resource("Secret", "files-auth-all", {"all.txt": "synthetic-all"})

    volumes = [
        {
            "name": "config",
            "configMap": {
                "name": "files-config",
                "defaultMode": 0o444,
                "items": [{"key": "config.json", "path": "nested/settings.json"}],
            },
        },
        {"name": "all", "configMap": {"name": "files-all", "optional": False}},
        {
            "name": "auth",
            "secret": {
                "secretName": "files-auth",
                "defaultMode": 0o444,
                "items": [{"key": "token", "path": "credentials", "mode": 0o400}],
            },
        },
        {"name": "auth-all", "secret": {"secretName": "files-auth-all"}},
        {"name": "optional-cm", "configMap": {"name": "missing-cm", "optional": True}},
        {
            "name": "optional-secret",
            "secret": {"secretName": "missing-secret", "optional": True},
        },
        {
            "name": "optional-key",
            "secret": {
                "secretName": "files-auth",
                "optional": True,
                "items": [{"key": "missing", "path": "missing"}],
            },
        },
        {
            "name": "optional-cm-key",
            "configMap": {
                "name": "files-config",
                "optional": True,
                "items": [{"key": "missing", "path": "missing"}],
            },
        },
    ]

    def definition(config, auth):
        checks = [
            'test "$(id -u):$(id -g)" = 1000:1000',
            f'test "$(cat /etc/files/config/nested/settings.json)" = {config}',
            f'test "$(sha256sum /etc/files/auth/credentials | cut -d " " -f 1)" = {hashlib.sha256(auth.encode()).hexdigest()}',
            'test "$(cat /etc/files/all/all.txt)" = all-config',
            f'test "$(sha256sum /etc/files/auth-all/all.txt | cut -d " " -f 1)" = {hashlib.sha256(b"synthetic-all").hexdigest()}',
            'test "$(stat -L -c %a /etc/files/config/nested/settings.json)" = 444',
            # Kubernetes fsGroup adds group-read to a 0400 item in a read-only volume.
            'test "$(stat -L -c %a /etc/files/auth/credentials)" = 440',
            "test ! -e /etc/files/config/unused",
            "test ! -e /etc/files/auth/unused",
            "test ! -e /etc/files/optional-key/missing",
            "test ! -e /etc/files/optional-cm-key/missing",
            "test -d /etc/files/optional-cm",
            "test -d /etc/files/optional-secret",
            "! touch /etc/files/config/new 2>/dev/null",
            "! touch /tmp/new 2>/dev/null",
            "test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token",
            'test -r /arprt/context.json; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png',
        ]
        obj = screen("; ".join(checks), "3s")
        job = obj["spec"]["source"]["job"]
        job["volumes"] = volumes
        job["renderer"]["volumeMounts"] = [
            {
                "name": v["name"],
                "mountPath": "/etc/files/" + v["name"],
                "readOnly": True,
            }
            for v in volumes
        ]
        return obj

    previous = latest()["sequence"]
    obj = definition("config-v1", "synthetic-v1")
    apply(obj)
    wait(lambda: latest().get("sequence", 0) > previous, 90)
    success = latest()
    print(
        "PASS: input file mounts, UID/GID 1000, modes and optional objects/keys",
        flush=True,
    )
    # ARPRT persists references, never the file contents, in its attempt/Context.
    attempt = success["attemptID"]
    stored = json.dumps(state()) + read("runs/" + attempt + "/context.json")
    if "synthetic-v1" in stored or "skip-secret" in stored:
        raise RuntimeError("file credentials escaped into scheduler state or Context")

    # Change only the input objects. The next interval Job uses the same Screen.
    resource(
        "ConfigMap", "files-config", {"config.json": "config-v2", "unused": "skip"}
    )
    resource("Secret", "files-auth", {"token": "synthetic-v2", "unused": "skip-secret"})
    # The unchanged command expects v1 and must fail with v2, proving fresh input.
    wait(lambda: state().get("reason") == "JobFailed" and not state().get("active"), 90)
    if latest()["attemptID"] != success["attemptID"]:
        raise RuntimeError("failed updated-input render replaced previous success")
    obj = definition("config-v2", "synthetic-v2")
    apply(obj)
    wait(lambda: latest().get("sequence", 0) > success["sequence"], 90)

    print(
        "PASS: new Jobs observe input updates and failed renders retain success",
        flush=True,
    )

    # Required missing objects and keys wait at FailedMount and then time out.
    for source, missing_key, optional in [
        ("configMap", False, None),
        ("secret", False, False),
        ("configMap", True, False),
        ("secret", True, None),
    ]:
        obj = definition("config-v2", "synthetic-v2")
        job = obj["spec"]["source"]["job"]
        reference = {
            "name" if source == "configMap" else "secretName": "files-config"
            if source == "configMap"
            else "files-auth"
        }
        if missing_key:
            reference["items"] = [{"key": "missing", "path": "missing"}]
        else:
            reference["name" if source == "configMap" else "secretName"] = (
                "required-missing"
            )
        if optional is not None:
            reference["optional"] = optional
        job["volumes"] = [{"name": "required", source: reference}]
        job["renderer"]["volumeMounts"] = [
            {"name": "required", "mountPath": "/etc/required", "readOnly": True}
        ]
        previous = latest()["attemptID"]
        apply(obj)

        def required_attempt():
            active = state().get("active", {})
            return (
                active
                if active.get("source", {}).get("volumes") == job["volumes"]
                else None
            )

        wait(required_attempt, 30)
        attempt = required_attempt()["id"]

        def mount_error():
            pods = json.loads(
                kubectl(
                    "-n",
                    namespace,
                    "get",
                    "pods",
                    "-l",
                    "batch.kubernetes.io/job-name=arprt-render-" + attempt,
                    "-o",
                    "json",
                ).stdout
            )["items"]
            uids = {p["metadata"]["uid"] for p in pods}
            events = json.loads(
                kubectl("-n", namespace, "get", "events", "-o", "json").stdout
            )["items"]
            return any(
                e.get("reason") == "FailedMount" and e["involvedObject"]["uid"] in uids
                for e in events
            )

        wait(mount_error, 25)
        wait(
            lambda: (
                state().get("reason") == "JobTimedOut" and not state().get("active")
            ),
            90,
        )
        if latest()["attemptID"] != previous:
            raise RuntimeError("missing file input replaced compatible success")
        print(
            f"PASS: required {source} {'key' if missing_key else 'object'} FailedMount/timeout and stale retention",
            flush=True,
        )
