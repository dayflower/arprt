# arprt

A TRMNL BYOS Kubernetes controller with explicit HTTP GET and POST screen delivery,
Secret authentication with setup-generated keys and persistent PNG artifacts. Initial device support is
**TRMNL OG (2-bit), firmware 1.8.17, 800 × 480 / gray4**. Phase 2 software and
physical-device acceptance completed on 2026-10-07; see [Phase 2](notes/003-PHASE2.md),
[the plan](notes/000-PLAN.md), and [contracts](notes/001-PHASE0.md).
Device-aware POST implementation, validation and real-device delivery are recorded in [Phase 3](notes/004-PHASE3.md).

## Build and checks

Use Go 1.26.8 (selected by the Makefile), a C compiler for race tests, Make,
and Docker for images. Dependencies and generator versions are pinned.

```sh
make verify
make integration ENVTEST_K8S_VERSION=1.36.0
make integration ENVTEST_K8S_VERSION=1.36.2
make docker-build IMAGE=arprt:dev
```

Envtest checks use disposable API servers and never touch your configured
cluster. For a full local-cluster check with a cluster-accessible image:

```sh
python3 hack/cluster-smoke.py --image <registry>/arprt:<tag>
```

This script refuses to overwrite existing arprt CRDs. It creates an isolated
namespace, tests pairing, display, both proxy path modes, image authentication,
key generation/reuse, GitOps re-apply, PVC restart, renderer outage, initial-image responses and scoped RBAC, then
removes its resources. It does not configure a physical TRMNL or public ingress.

## Installation and pairing

The initial Kubernetes baseline is **1.36.0 or newer within minor 1.36**, Linux,
one active controller, and a POSIX PVC supporting same-node ReadWriteOnce,
atomic rename and fsync. Other minors, multiple replicas, ReadWriteOncePod and
multi-node storage failover are unverified. The supplied Deployment runs as
UID/GID 1000 with Recreate rollout and namespace-only RBAC.

1. Build/push the controller image. Create a private Kustomize overlay outside
   this repository that references `config/default`, and set the image there.
   Configure the PVC's StorageClass if your cluster has no default.
2. In that overlay, patch ConfigMap `arprt-config`'s `publicBaseURL` to your public
   URL, including the external prefix, for example `https://byos.example.com/trmnl`.
   Expose Service `arprt:8080` through your proxy. Preserve the prefix by default;
   if your proxy strips exactly `/trmnl`, add `--proxy-strips-prefix=true`.
   Keep actual hostnames, registry addresses and other private settings in the
   private overlay. The [Envoy Gateway example](config/overlays/envoy-gateway/README.md)
   shows the required configuration with placeholder values.
3. Install the controller using your private overlay and the example renderer
   below. Replace `/path/to/private-overlay` with your overlay's directory:

   ```sh
   kubectl apply --validate=strict -k /path/to/private-overlay
   kubectl -n trmnl rollout status deployment/arprt
   kubectl apply --validate=strict -k examples/http-get
   kubectl -n trmnl rollout status deployment/example-renderer
   ```

4. The default base declares `living-room-auth` without key data. The first setup
   generates and saves its `apiKey`; you do not need to supply a key. An existing
   valid key is reused. For a different `authSecretRef`, declare that Secret in
   your private overlay and add its name to the Role's `get` and `patch` rules.
   Keep the generated key out of Git. You may also supply your own key through
   your existing Secret or SealedSecret workflow.

5. Create the device using one of the three MAC modes. The default sample omits
   the MAC and learns it during successful setup:

   ```sh
   kubectl -n trmnl apply --validate=strict -f config/samples/device.yaml
   ```

   Alternatively use `device-inline.yaml` for a direct MAC or `device-secret.yaml`
   for a same-namespace Secret reference. These are alternatives for the same
   resource; choose before creating it. Keep actual MACs in private configuration
   or a Secret managed by your GitOps tooling. See [hardware identity](#hardware-identity).

   The default Role permits `get` on `living-room-auth` and `living-room-identity`
   and `patch` on `living-room-auth` only. Add other referenced Secret names
   explicitly to the relevant `resourceNames` rules. No Secret list/watch,
   create, update or delete permission is required.

6. Set the TRMNL's **Custom Server URL** to:

   ```text
   https://byos.example.com/trmnl/devices/trmnl/living-room
   ```

   The firmware appends `/api/setup` and `/api/display`. Setup returns the
   generated or existing key and an anonymous bundled calibration PNG. Setup
   remains available after pairing; repeating it for the registered MAC returns
   the saved key. No pairing overlay or rollout is necessary.

7. Confirm the four gray bands on the device and inspect status:

   ```sh
   kubectl -n trmnl get trmnldevices,trmnlscreens
   kubectl -n trmnl get trmnldevice living-room -o yaml
   ```

Use TLS outside a trusted LAN. Preserve `ID` / `Access-Token` headers and avoid
logging them or setup response bodies. Absolute image URLs always come from
`publicBaseURL`; forwarded headers never change their origin. Images live below
the exact device base URL to match firmware 1.8.17's authentication prefix test.
Image routes do not redirect or compress; they return exact Content-Length.

## Hardware identity

`TrmnlDevice.spec` accepts at most one of:

```yaml
hardwareID: "02:00:00:00:00:01"  # synthetic direct-MAC example
```

```yaml
hardwareIDSecretRef:
  name: living-room-identity
  key: mac
```

Omit both to learn the first MAC from successful `/api/setup`. The device resource
and its named auth Secret must already exist; `authSecretRef` remains required.
Setup is enabled by default and fills an absent `apiKey` in that Secret. Device
and Secret resource creation remain declarative.

A referenced Secret must be in the device's namespace and contain a canonical
uppercase MAC at the selected key, without whitespace. Missing, unreadable or
invalid identity Secrets make the device unavailable; they never fall back to
learning a MAC. Use SealedSecret or your existing secret-management workflow to
provision the Secret; arprt only reads the resulting Kubernetes Secret.

MAC source selection, a direct value, and a Secret reference's name/key are
immutable after creation. A registration pins the MAC to the device UID on the
PVC and mirrors it in `status.hardwareID`. Changing a Secret to a different MAC
produces `IdentityReady=False` with `HardwareIdentityChanged`; the existing
registration is retained. To change hardware or input mode, replace the device
resource. Preserve or separately rotate its auth Secret as appropriate. Existing
explicit-MAC devices can establish their durable registration through an
already-authenticated display request without re-pairing.

Registration survives controller restart and status loss. Repeated setup for
the same MAC returns the existing auth key. Setup on a different MAC is rejected.
Unbound automatic devices report `AwaitingSetup`; MACs are unique within the
namespace across direct, Secret-backed and learned identities. Two unbound
automatic devices are not duplicates. MACs, identity Secret values/references
and credentials are excluded from renderer context and controller messages.
Back up the PVC along with Kubernetes resources; replacement UIDs never inherit
prior image issuance. See [implementation and validation](notes/005-OPTIONAL-MAC.md).

## Setup and credentials

Setup requires the device route and a matching MAC, without an Access-Token.
For automatic MAC registration, it accepts the first eligible MAC. It generates
32 random bytes as unpadded URL-safe base64 when `apiKey` is absent from the
declared auth Secret. The API write must succeed before setup returns a key.
Concurrent requests, setup retries and controller restart reuse the saved key.

An existing `apiKey` is never overwritten, including an empty or invalid value.
Remove an invalid field or supply a valid key explicitly. An unavailable Secret,
an immutable empty Secret or denied patch permission returns 503; an invalid
key returns 401. Display, log and image requests only read credentials and
cannot generate them. Keys are excluded from status, render context, artifact
metadata and logs. See [implementation and tests](notes/006-AUTOMATIC-KEYS.md).

For GitOps, declare only the auth Secret's metadata and type when using generated
keys. Leave `data.apiKey` and `stringData.apiKey` out of the desired manifest;
do not combine generated keys with another controller that owns that key field.
Store the live auth Secrets in your backup alongside the artifact/identity PVC.
Rotating a key requires setup again on the device. Setup stays enabled by default;
the existing `--enable-setup=false` option remains available to disable it explicitly.

## Renderer contract

The [minimal Python renderer](examples/http-get/renderer.py) uses only the
standard library. Its Kubernetes example renders four gray bands, accepts
`width=800&height=480&palette=gray4`, and can return indexed PNG by appending
`?encoding=indexed` to the screen URL. It requires no Kubernetes API access.
For a standalone run: `python3 examples/http-get/renderer.py --port 8000`.

GET receives `Accept: image/png` and target query parameters only. Existing
non-target query values, including repeated values, are preserved; all lowercase
`width`, `height`, `palette` values are replaced. No device identity, headers,
tokens, cookies or telemetry are forwarded. Redirects are rejected. Rendering
has a 10-second deadline and eight execution slots shared by GET and POST.

POST receives the same target query parameters, `Accept: image/png`,
`Content-Type: application/json`, and the complete [Render Context v1](testdata/phase0/render-context-v1.json)
body. Query and JSON targets match exactly. Context includes device name,
model/firmware and available battery/network measurements, with current
authenticated request telemetry overlaid on known valid state without waiting
for the Kubernetes informer cache. Missing optional values are omitted; hardware
ID, credentials, Secret references and arbitrary inbound headers are excluded.

The [device-status renderer](examples/http-post/renderer.py) displays those fields
using only Python's standard library. Install and assign it to a paired device:

```sh
kubectl apply --validate=strict -k examples/http-post
kubectl -n trmnl rollout status deployment/device-status-renderer
kubectl -n trmnl patch trmnldevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"device-status"}}}'
```

Run it outside Kubernetes as an HTTP service with
`python3 examples/http-post/renderer.py --port 8000`, or consume the shared context
fixture directly:

```sh
python3 examples/http-post/renderer.py \
  --context testdata/phase0/render-context-v1.json --output /tmp/device-status.png
```

The renderer ignores unknown context fields and shows UNKNOWN for absent
measurements. It does not infer battery percentage from voltage.

Return HTTP 200, Content-Type `image/png`, and **less than 90,000 bytes** of
800 × 480 noninterlaced 2-bit PNG. Accepted color types are grayscale (0), or
indexed (3) with exactly four RGB palette entries `(0,0,0)`, `(85,85,85)`,
`(170,170,170)`, `(255,255,255)`. Alpha/transparency, APNG, truecolor, other bit
depths, bad CRCs, partial images and wrong dimensions are rejected. The controller
validates the complete bounded pixel stream and performs no image conversion.

`refreshInterval` controls GET artifact freshness (default 5m), independently of
device polling (`--device-refresh-seconds`, default 300). Equal screen UIDs,
definition revisions, generations and targets share fresh results. Concurrent
requests for a rendering unit coalesce. Each receiving device UID is separately
issued access to its image. Editing the screen forces a refresh; failures may
serve prior output only for the same screen UID and exact current target.

POST renders every authenticated display request; it has no fresh cache reuse
or request coalescing. Omit `refreshInterval` (the CRD rejects it for POST).
Artifacts belong to a screen/device UID pair and exact target, with an internal
context digest and ordered attempts. A failed render may serve that pair's
compatible prior success, including an older screen definition. A newer attempt
supersedes earlier in-flight responses, and changed assignments, UIDs or targets
cannot publish obsolete output or borrow another device's fallback.

Future Job rendering will use a context snapshot taken at scheduling time,
refresh asynchronously at `refreshInterval` (default 30m), and serve completed
output without waiting for a Job on each display request. Job execution is not
implemented yet; its telemetry will reflect the scheduled snapshot.

Immutable URLs and short firmware filenames survive restart. Issued URLs remain
fetchable even after reassignment; a recreated device UID never inherits access.
This phase retains artifacts and issuance records indefinitely, exceeding the
24-hour minimum; bounded retention/GC is deferred to later phases.

## Runtime and conditions

For local development after CRD installation:

```sh
mkdir -p /tmp/arprt-data
bin/arprt --namespace=trmnl --artifact-root=/tmp/arprt-data \
  --public-base-url=http://localhost:8080/trmnl
```

`WATCH_NAMESPACE` is the fallback for `--namespace`; `POD_NAMESPACE`, when set,
must match. `PUBLIC_BASE_URL` is the fallback for `--public-base-url`.
BYOS listens on `:8080`; probes listen on `:8081`. SIGINT/SIGTERM stops serving
and the manager within its 20-second shutdown budget. Metrics remain disabled.

Device conditions report `IdentityReady`, `IdentityUnique`, `AuthenticationReady`,
`AssignmentReady`, `TargetKnown`, `TelemetryValid`, `RenderReady` and `Ready`.
Screen `Ready` reports the last requested render outcome for its current
revision; it does not attest renderer availability before the first request.
A stale image can keep device `Ready=True` with `StaleImageAvailable`, while
`RenderReady=False` and screen `Ready=False` expose rendering failure.

Unknown or contradictory capabilities produce no renderer call. A missing
assignment, missing screen, unsupported source or unavailable initial image
returns **HTTP 200 with JSON `status: 202`**, without image fields. This preserves
the device's current e-paper content and uses the firmware's fast-poll backoff.
Successful display returns JSON `status: 0`; renderer failures never return the
firmware's reset-triggering JSON `status: 500`.

Job execution, playlists and automatic discovery remain
unavailable and report actionable conditions. Public declaration/default and
Render Context v1 contracts from Phase 1 remain unchanged. Use strict Kubernetes
field validation; structural CRDs otherwise prune unknown source/reference keys.
