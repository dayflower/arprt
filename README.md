# arprt

A TRMNL BYOS Kubernetes controller with HTTP GET, POST and asynchronous Job delivery, ordered playlists and automatic screen discovery,
Secret authentication with setup-generated keys and persistent PNG artifacts. Initial device support is
**TRMNL OG (2-bit), firmware 1.8.17, 800 × 480 / gray4**. Phase 2 software and
physical-device acceptance completed on 2026-10-07; see [Phase 2](notes/003-PHASE2.md),
[the plan](notes/000-PLAN.md), and [contracts](notes/001-PHASE0.md).
Device-aware POST implementation, validation and real-device delivery are recorded in [Phase 3](notes/004-PHASE3.md). Job scheduling, collection and recovery are recorded in [Phase 4](notes/007-PHASE4.md).
Playlist and discovery semantics and validation are recorded in [Phase 5](notes/008-PHASE5.md).
Operational hardening and initial release acceptance completed on 2026-10-08;
see [Phase 6](notes/009-PHASE6.md).

## Playlists and automatic discovery

Use `screenRef` for one screen, `playlistRef` for an ordered list, or omit both
for discovery. These choices are mutually exclusive. For example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlPlaylist
metadata:
  name: living-room
spec:
  screens:
    - name: photo
    - name: device-status
    - name: weather
```

Set the device's `spec.playlistRef.name` to `living-room` and remove `screenRef`.
See [playlist](config/samples/playlist.yaml), [playlist device](config/samples/device-playlist.yaml)
and [discovery device](config/samples/device-discovery.yaml) samples. GET, POST
and Job screens can be mixed. Playlists accept 1–64 distinct names and preserve
list order. Discovery uses the first 64 nondeleting screens sorted by name,
within the device namespace. Missing playlist entries are skipped. An absent
playlist or no eligible screens produces the initial-pending response.

Screen cycling moves to the next screen only after recording a successful image
response, including a compatible stale image belonging to that screen. A
selected screen without any valid image holds its turn and returns pending.
Every eligible Job pair is prepared in advance, with global active execution
bounded by `--job-concurrency` (default 4). Removing eligibility cancels active
work and stops new attempts; completed Jobs follow the existing retention policy.

BYOS provides no verified unique request identifier. For devices with screen cycling,
responses within `--device-refresh-seconds` (default 300) replay the same image
and filename. This includes early manual polls. Telemetry is still observed;
POST receives current request telemetry on the next new turn. Device `status.screenCycle`
persists the last turn and replay window across controller restarts. Membership
or order edits reset to the first eligible screen; definition changes invalidate
replay, and changed targets cannot reuse incompatible output. A device using
`screenRef` keeps the existing per-request POST behavior. Phase 4 scheduler
records migrate automatically to per-pair records without replacing active Jobs.

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
python3 hack/job-smoke.py --image <registry>/arprt:<tag>
python3 hack/playlist-smoke.py --image <registry>/arprt:<tag>
python3 hack/operations-smoke.py --image <registry>/arprt:<tag>
```

The scripts reuse identical installed CRDs and refuse to change mismatched schemas. The HTTP check creates an isolated
namespace, tests pairing, display, both proxy path modes, image authentication,
key generation/reuse, GitOps re-apply, PVC restart, renderer outage, initial-image responses and scoped RBAC, then
removes its resources. The Job check covers default/custom paths, interval refresh,
controller restart, failures, timeout, obsolete assignments and same-node RWO mounts.
The playlist check covers mixed sources, Job preparation before selection, screen-cycle retries/restart, edits/deletion, empty discovery and scoped playlist RBAC.
These checks do not configure a physical TRMNL or public ingress.

## Installation and pairing

The initial Kubernetes baseline is **1.36.0 or newer within minor 1.36**, Linux,
one active controller, and a POSIX PVC supporting same-node ReadWriteOnce,
atomic rename and fsync. Other minors, multiple replicas and
multi-node storage failover are unverified. Job mode rejects ReadWriteOncePod
and ReadWriteMany. The supplied Deployment runs as
UID/GID 1000 with Recreate rollout and namespace-only RBAC.

Examples use `arprt` for both the namespace and the public URL prefix. These
are independent settings: device URLs are
`<publicBaseURL>/devices/<namespace>/<device-name>`.

1. Build/push the controller image. Create a private Kustomize overlay outside
   this repository that references `config/default`, and set the image there.
   Configure the PVC's StorageClass if your cluster has no default.
2. In that overlay, patch ConfigMap `arprt-config`'s `publicBaseURL` to your public
   URL, including the external prefix, for example `https://byos.example.com/arprt`.
   Expose Service `arprt:8080` through your proxy. Preserve the prefix by default;
   if your proxy strips exactly `/arprt`, add `--proxy-strips-prefix=true`.
   Keep actual hostnames, registry addresses and other private settings in the
   private overlay. The [Envoy Gateway example](config/overlays/envoy-gateway/README.md)
   shows the required configuration with placeholder values.
3. Install the controller using your private overlay and the example renderer
   below. Replace `/path/to/private-overlay` with your overlay's directory:

   ```sh
   kubectl apply --validate=strict -k /path/to/private-overlay
   kubectl -n arprt rollout status deployment/arprt
   kubectl apply --validate=strict -k examples/http-get
   kubectl -n arprt rollout status deployment/example-renderer
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
   kubectl -n arprt apply --validate=strict -f config/samples/device.yaml
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
   https://byos.example.com/arprt/devices/arprt/living-room
   ```

   The firmware appends `/api/setup` and `/api/display`. Setup returns the
   generated or existing key and an anonymous bundled calibration PNG. Setup
   remains available after pairing; repeating it for the registered MAC returns
   the saved key. No pairing overlay or rollout is necessary.

7. Confirm the four gray bands on the device and inspect status:

   ```sh
   kubectl -n arprt get trmnldevices,trmnlscreens
   kubectl -n arprt get trmnldevice living-room -o yaml
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
kubectl -n arprt rollout status deployment/device-status-renderer
kubectl -n arprt patch trmnldevice living-room --type=merge \
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

Job rendering uses an immutable context snapshot taken at scheduling time,
refreshes asynchronously at `refreshInterval` (default 30m), and serves completed
output without waiting for a Job on each display request. Telemetry changes update
future snapshots; they do not trigger Jobs on every poll. Initial target discovery,
screen definition, target and assignment changes trigger rendering.

Install the [existing-image Job example](examples/job/screens.yaml):

```sh
kubectl apply --validate=strict -k examples/job
kubectl -n arprt patch trmnldevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"job-bands"}}}'
```

The unmodified `busybox:1.37.0` image reads the context file and writes a supplied
PNG using its ordinary CLI. Assign `job-bands-custom` to exercise distinct
`/input/context.json` and `/work/weather.png` parents. Both use a ConfigMap for
non-sensitive image input; Kubernetes clients and upload SDKs are unnecessary.

Job containers run as UID/GID 1000 with a read-only root filesystem, dropped
capabilities, no privilege escalation, and no ServiceAccount token. The writable
mount covers the entire output parent: choose a dedicated work directory that
contains no required image files. Context is a read-only file mount; its parent
may differ or be nested under the output directory. Supply paths to the
renderer CLI explicitly; the controller does not interpret or rewrite arguments.
Additional writable locations such as `/tmp` are not provided.

The controller validates the full shared RWO PVC mount at startup and requires
renderer Pod affinity to the controller in the same namespace and node. Job
`backoffLimit` is zero; controller retries use new attempts with exponential
backoff from 5s to 5m. A pair has one active attempt, with at most four active Jobs
by default. Jobs have a 120s deadline and default CPU/memory limits of 500m/256Mi
(requests 50m/64Mi); `renderer.resources` overrides those values. Collection
requires Job completion, a successful renderer termination, and a valid PNG.
Symlinks escaping the output directory, special files and malformed output fail
collection. Changed UIDs, assignment, revision or target reject old completions.
Compatible prior output remains available on failure.

Immutable URLs and short firmware filenames survive restart. Issued URLs remain
fetchable even after reassignment; a recreated device UID never inherits access.
When Job scheduling is enabled, maintenance runs every minute. Finished Jobs
and run directories are retained for at least one hour; active attempts remain
protected. Compatible latest successes are kept, issued URLs retain their bytes
for at least 24h after last issuance, and older unreferenced artifacts are removed.
HTTP-only local mode runs the same safe artifact maintenance without Job scheduling.

## Runtime and conditions

For local development after CRD installation:

```sh
mkdir -p /tmp/arprt-data
bin/arprt --namespace=arprt --artifact-root=/tmp/arprt-data \
  --public-base-url=http://localhost:8080/arprt
```

`WATCH_NAMESPACE` is the fallback for `--namespace`; `POD_NAMESPACE`, when set,
must match. `PUBLIC_BASE_URL` is the fallback for `--public-base-url`.
The supplied Deployment enables Job rendering with `--job-pvc=arprt-data` and
Downward API `POD_NAME`. `--controller-pod` can supply that Pod name explicitly.
The controller must mount the entire claim at `--artifact-root`; subtree mounts,
RWOP, RWX and block volumes are rejected for Job mode. Outside a Pod, omit
`--job-pvc` for HTTP-only development; Job screens report `JobSchedulingDisabled`.
`--job-concurrency` (4) and `--job-deadline-seconds` (120) bound execution.
BYOS listens on `:8080`; probes listen on `:8081`. SIGINT/SIGTERM stops serving
and the manager within its 20-second shutdown budget. Metrics are opt-in locally
and enabled on internal port 8082 by the supplied Deployment.

Device conditions report `IdentityReady`, `IdentityUnique`, `AuthenticationReady`,
`AssignmentReady`, `TargetKnown`, `TelemetryValid`, `RenderReady` and `Ready`.
Screen `Ready` reports the last render outcome for its current
revision; it does not attest readiness of every assigned device pair.
A stale image can keep device `Ready=True` with `StaleImageAvailable`, while
`RenderReady=False` and screen `Ready=False` expose rendering failure.

Unknown or contradictory capabilities produce no renderer call. An empty
assignment, missing reference, unsupported source or unavailable initial image
returns **HTTP 200 with JSON `status: 202`**, without image fields. This preserves
the device's current e-paper content and uses the firmware's fast-poll backoff.
Successful display returns JSON `status: 0`; renderer failures never return the
firmware's reset-triggering JSON `status: 500`.

`TrmnlPlaylist` references exist only in the device namespace. Its `Ready`
condition validates references; it does not promise a rendered image. Missing
screens are skipped, and an empty eligible set reports `NoEligibleScreens`.
For devices with screen cycling, Job outcomes are reported on their screen and Job; the
single device `RenderReady` condition is not an aggregate across Job pairs.
Job render conditions include `JobPending`, `JobFailed`, `JobTimedOut`,
`JobCreationFailed`, `JobLost`, `RunUnavailable`, `CollectionFailed` and `ImageReady`.
For explicit Job assignments, display polls preserve the reconciler's failure
reason; finished Job status/logs remain available for diagnosis.
Renderer defaults and Render Context v1 remain unchanged. Use strict Kubernetes
field validation; structural CRDs otherwise prune unknown source/reference keys.

## Monitoring and capacity

The supplied Deployment exposes Prometheus metrics on container port 8082.
It has no public metrics Service or Gateway route. For inspection, use
`kubectl -n arprt port-forward deployment/arprt 8082:8082` and fetch
`http://localhost:8082/metrics`. Local development opts in with
`--metrics-bind-address=:8082`; `0` disables the listener. Restrict monitoring
access with your network policy. Do not expose this endpoint on the BYOS proxy.

| Metric | Meaning |
| --- | --- |
| `arprt_requests_total` | BYOS requests by fixed route and HTTP result; pending is HTTP success |
| `arprt_request_rejections_total` | Requests rejected by the 64-request admission limit |
| `arprt_render_duration_seconds` | GET, POST and Job attempt duration, with success/failure labels |
| `arprt_http_render_active`, `arprt_jobs_active` | Current execution slots/Jobs |
| `arprt_job_queue_depth` | Due persisted pairs awaiting execution, sampled during maintenance |
| `arprt_devices` | Counts of never-seen, recent and silent devices |
| `arprt_device_last_seen_max_age_seconds` | Oldest observed device request |
| `arprt_artifact_oldest_age_seconds` | Oldest latest successful artifact |
| `arprt_storage_bytes`, `arprt_storage_free_bytes`, `arprt_storage_limit_bytes` | Managed logical usage, filesystem availability and configured admission limit |
| `arprt_storage_errors_total`, `arprt_collection_failures_total` | Storage faults and invalid/unavailable completed output |
| `arprt_maintenance_total` | Successful/failed maintenance passes |

Application metric labels never contain resource names, UIDs, target hashes,
MACs, URLs, credentials or telemetry. Logs and Events describe fixed outcomes
and may identify Kubernetes resource names; request bodies, headers, renderer
URLs and output contents are excluded. Conditions provide per-resource detail.
`DeviceSeen` becomes false after three polling intervals (at least two minutes),
independently of whether a previously delivered e-paper image remains usable.
Device counts, queue depth, artifact age and storage usage refresh each minute;
render/request counters update on activity. Alert on increasing failure counters,
old artifacts, silent devices and a queue that does not drain.

The default 1 GiB PVC uses `--storage-max-bytes=805306368` (768 MiB) and
`--storage-min-free-bytes=67108864` (64 MiB). Usage includes blobs, metadata,
Job snapshots and output files. Before new image publication, identity/issuance
allocation or Job work, the controller checks logical usage and available
filesystem space. An admission failure reports `StorageCapacityExceeded`,
keeps compatible prior output and retries after space becomes available.
Updates to an existing issuance may use the reserve so stored images can still
be delivered. The controller never evicts protected images to satisfy a limit.

These checks are admission controls, not reservations or a filesystem quota.
Concurrent renderers can write beyond the threshold before the next check.
Use trusted renderer images, filesystem quotas and filesystem/PVC monitoring
when a strict disk bound is required. A small compressed PNG limit does not
limit arbitrary intermediate renderer files. Actual allocated disk usage and
inode availability should also be monitored by the storage platform.

Maintenance also runs in HTTP-only mode. Latest compatible successes remain
protected; issued image bytes and access records remain for at least 24 hours
after the last display response. Issuance writes coalesce within one minute,
with an extra retention minute preserving that minimum. Interrupted writes and
unused filename reservations expire after 24h. Jobs and run directories retain
their diagnostic hour and active attempts are never removed. Seven consecutive
Job failures slow further retries to at least the screen interval and five
minutes; success clears the counter. No manual reset is needed after recovery.

Each device can select at most 64 screens, so `D` devices can require `64 × D`
scheduler records. Only `--job-concurrency` attempts allocate active run
directories or nonterminal Jobs. A 16-device × 64-screen test verifies 1,024
pairs with bounded active work; it is a correctness check, not a throughput
promise. Capacity admission also limits new scheduler metadata. Scale and size
the namespace, polling intervals and storage together. With four active Jobs
and the one-hour diagnostic retention, short successful refresh intervals can
still retain many completed Jobs. Use an appropriate namespace ResourceQuota
for a hard Kubernetes object ceiling; quota rejection retains prior output and
uses bounded retries.

## Recovery and troubleshooting

Back up the PVC, CRs and authentication Secrets together. Stop the controller
and renderer Jobs for a consistent filesystem snapshot, or use a storage
snapshot mechanism with the same consistency guarantee. Restore the original
resource UIDs where supported; recreated resources intentionally do not inherit
identity or issued-image authorization. Do not edit internal JSON to transfer
ownership. Keep the backup private because it includes registered MACs and
allowlisted device context snapshots.

The controller uses an advisory exclusive filesystem lock to reject a second
writer on the same PVC. Keep one active replica and Recreate rollout. This lock
does not make replicas, multiple PVCs, multi-node failover or shared serving
metadata supported. SIGTERM cancels in-flight HTTP rendering, stops admitting
new connections and closes the listener within the shutdown budget; incomplete
work cannot publish success. Kubernetes Jobs continue independently and are
recovered by the next controller from their durable snapshots and API status.

| Symptom | Checks and response |
| --- | --- |
| `DeviceSeen=False` | Power, Wi-Fi, configured custom-server URL, proxy and device polling interval |
| 401/403 | Auth Secret validity, registered hardware identity and preserved proxy headers; repeat setup after key rotation |
| JSON `status: 202` | Assignment/target conditions, then renderer readiness; no compatible first image is available |
| `StaleImageAvailable` | The existing image is safe to serve; inspect the screen condition or retained Job for the failed refresh |
| `StorageCapacityExceeded` | PVC usage/free bytes/inodes, configured limits, retained Jobs and output growth; expand storage or wait for safe GC |
| `CollectionFailed` | Successful renderer exit, actual output path, regular PNG file, size/palette/dimensions and read permissions |
| Maintenance failure | Check filesystem access and restore invalid retention metadata from backup; GC deliberately avoids guessing ownership |
| Startup rejects scheduler state | Restore `jobs/` metadata from a consistent backup; do not delete active records while their Jobs exist |
| Identity unavailable after storage damage | Restore the matching identity record; deleting it could allow re-registration to different hardware |
| Corrupt image/latest pointer | The affected unit is regenerated on the next render; other valid units and issuance remain intact |
| Existing URL returns 404 | Confirm device UID, authentication, retention window and blob integrity; an old URL cannot recover missing bytes without its original validated output |

Inspect `kubectl -n arprt get trmnldevices,trmnlscreens,trmnlplaylists -o yaml`,
`kubectl -n arprt get jobs,pods`, and `kubectl -n arprt get events`.
Avoid publishing Secret values, raw device requests or renderer logs containing
private data. A syntactically corrupt latest pointer is regenerable; corrupt
identity, issuance and scheduler records fail closed. Invalid retention metadata
aborts destructive artifact GC. Persistent filesystem errors require operator
repair; a healthy Kubernetes API probe alone does not verify storage integrity.

## Security and support boundary

The namespace is the trust boundary. Screen authors can choose HTTP endpoints,
container images, commands and same-namespace Secret/ConfigMap references.
Permission to create screens therefore permits running code and supplying
Secret-backed renderer environment through controller-created Jobs, even when
the controller's own direct Secret reads are restricted by `resourceNames`.
Grant CR write access only to trusted operators. Renderers do not receive the
controller's ServiceAccount token, auth key, MAC or infrastructure credentials
through Render Context. An explicitly configured renderer environment is an
operator-controlled exception and must contain only that renderer's credentials.

HTTP endpoints may be private cluster services or external HTTP(S) services;
redirects and URL userinfo are rejected. There is no built-in IP allowlist or
network isolation. Configure NetworkPolicy/egress controls and DNS/TLS according
to the endpoints you intentionally permit. Keep setup and BYOS behind the
configured TLS proxy, restrict network access where possible, and preserve the
firmware authentication headers. Setup remains enabled by default for the
registered MAC; use `--enable-setup=false` when your operating policy requires it.
Images require current device authentication and durable issuance to that UID.
The bundled setup calibration image is intentionally anonymous.

The initial support scope is Linux, Kubernetes **1.36.0+ within minor 1.36**,
TRMNL **OG / firmware 1.8.17 / 800×480 gray4**, one namespace, one active
controller and a POSIX filesystem RWO PVC with same-node renderer mounts,
atomic rename, fsync and advisory flock. API integration covers 1.36.0/1.36.2;
local end-to-end validation uses 1.36.4. Other devices, firmware, Kubernetes
minors, non-POSIX backends, multiple active replicas and automatic multi-node
storage failover remain unverified. No extra packaging is required beyond the
versioned CRDs and Kustomize manifests. Phase 6 evidence and completed
release acceptance are recorded in [Phase 6](notes/009-PHASE6.md).
