# Phase 0 — Contracts and deployment decisions

Recorded 2026-10-07. This document resolves the illustrative parts of
[DESIGN.md](DESIGN.md) before Phase 1 scaffolding. The experiment is a proof
of collection mechanics, not an implementation of the controller.

## Decisions and verification status

| Item | Decision |
| --- | --- |
| Language / framework | Go + controller-runtime, confirmed by the user |
| Namespace scope | One configured namespace per controller, confirmed by the user |
| Initial storage | PVC, confirmed by the user |
| API group | `arprt.dayflower.github.io`, explicitly corrected by the user |
| Initial device | TRMNL OG (2-bit), firmware `1.8.17`, reported by the user |
| API version | `v1alpha1`; all public resources namespaced |
| Verified collection environment | Kubernetes `v1.36.2`; PVC collection experiment only |
| Minimum supported Kubernetes | Settled in [Phase 1](002-PHASE1.md#kubernetes-support-floor): `v1.36.0` within minor 1.36; other minors unverified |
| Initial dependency baseline | Go `1.26`, controller-runtime `v0.24.1`, `k8s.io/*` `v0.36.0` |
| Serving / scheduling | One active controller replica; PVC deployment uses `Recreate` |
| Initial image target | `800 × 480`, semantic palette `gray4`, PNG |

The dependency baseline matches the tested Kubernetes minor; upstream's
[v0.24.1 go.mod](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.1/go.mod)
requires Go 1.26 and Kubernetes libraries 0.36.0. Phase 1 must use a pinned
Go 1.26 build container or provision the required toolchain.
No compiler or controller scaffolding has been installed in Phase 0. These are
build / client-library versions, not evidence of a minimum API-server version.
At the time of Phase 0, the collection experiment verified its behavior on
`v1.36.2`; the controller itself had not been implemented or tested. Phase 1
now establishes the support floor through required APIs, the matching client
dependency baseline and compatibility checks. Older Kubernetes minors remain
unverified, not declared inherently incompatible.

## Protocol evidence and compatibility boundary

The authoritative implementation inspected is firmware tag `v1.8.17`, commit
`9acd348270a493ca5f1c627ac91a9469543267ae`. Relevant pinned sources:

- [Header construction](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/api-client/request_headers.cpp).
- [Setup transport](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/api-client/setup.cpp)
  and [setup / setup-image handling](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/services/device_setup.cpp).
- [Setup JSON parser](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/parse_response_api_setup.cpp)
  and [display JSON parser](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/parse_response_api_display.cpp).
- [Display flow and status handling](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/bl.cpp),
  [HTTP transport](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/services/http_retry_request.cpp),
  [URL / retry helpers](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/http_utils.cpp).
- [Display model, profiles and PNG decoding](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/display.cpp),
  [image limits](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/include/config.h),
  [filename normalization](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/filesystem.cpp),
  [refresh / backoff](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/refresh_interval.cpp).
- [Official BYOS overview](https://docs.trmnl.com/go/diy/byos), used for endpoint scope;
  firmware code takes precedence over examples when they differ.

This is **source verification**, not captured traffic or a physical-device
acceptance test. All fixtures in [testdata/phase0](../testdata/phase0) are synthetic,
sanitized examples derived from the code. No real MAC address or token is stored.
Only this OG / firmware tuple is the initial compatibility target. Other models,
palettes, and firmware versions require a reviewed compatibility entry.

### Requests and telemetry

`GET /api/setup` is appended to the configured Custom Server URL. It carries
`ID` (MAC), `FW-Version`, `Model`, and optional `Panel-Rev`; no device token.
`GET /api/display` also carries `Access-Token`, `Width`, `Height`, `RSSI`,
`Battery-Voltage`, `Refresh-Rate`, and `Image-Cached`. Other observations include
`Update-Source`, `Wake-Time`, optional `FW-Commit`, `WiFi-Band`, `USB-Connected`
and `Battery-Charging`. The header `Temperature-Profile: true` is a feature
indicator, not the selected display profile.

HTTP header names are case insensitive. Accept documented legacy underscore
aliases such as `ACCESS_TOKEN` / `FW_VERSION` by normalizing underscores to
hyphens. Reject duplicate or conflicting identity / token headers rather than
choosing an arbitrary value; configure the reverse proxy to preserve aliases if
legacy firmware is ever admitted. This initial firmware sends hyphenated names.

Voltage is a finite decimal in volts, RSSI a signed integer in dBm, dimensions
positive integers, charging `0` / `1`, cached-image and USB flags `true` / `false`.
X firmware additionally reports percent, health, current, temperature, battery
count and capacity ratio, but those device-specific fields are not initial OG
support. Unknown headers are ignored. Invalid optional telemetry is omitted and
reported by field name only; it never supplies a made-up zero or an inferred
battery percentage. The OG voltage cannot establish a trustworthy percent.

Capabilities are resolved from the reviewed `(model, firmware)` table: `og`,
`1.8.17` maps to `800 × 480 / gray4`. The firmware reports dimensions directly,
but **does not report a semantic palette**. Missing dimensions can use that
verified mapping; contradictory dimensions invalidate the target. Unknown or
missing model / firmware leaves `TargetKnown=False`, with no renderer invocation.
Previous capabilities may be reused only for the same registered identity and
when no contradictory current observation exists. A changed tuple clears the
previous inferred target. Capability fields remain in status, never spec.

### Responses, polling and first-image behavior

HTTP status and JSON `status` are distinct. Successful setup uses HTTP 200 and
JSON `status: 200`, with `api_key`, `friendly_id`, `image_url`, and `message`.
The firmware persists the API key even when it is empty; always return the
registered nonempty key when setup is enabled. The setup image must have a
Content-Length and be downloadable without authentication.

Normal display uses HTTP 200 and JSON `status: 0`, `image_url`, `filename`,
integer `refresh_rate` in seconds, `image_url_timeout: 10`,
`update_firmware: false`, `reset_firmware: false`, and `special_function: ""`.
There is no independent `sleep_duration` response contract. Device polling
defaults to 300 seconds through runtime configuration; screen freshness does not
implicitly change that polling interval. Do not send OTA or remote reset commands.

Use a stable `filename` for the same artifact and a different name for different
bytes; keep it at most 31 ASCII characters (e.g. a persisted unique 26-character
artifact ID plus `.png`). The firmware caches by filename and truncates longer
names, so exposing a full SHA-256 as filename is unsafe. Full digests remain in
immutable URLs and metadata; internal IDs are collision checked and never reused.

| Situation | HTTP / JSON status | Image / controller behavior |
| --- | --- | --- |
| Unknown device route, or unregistered setup MAC | 404 / 404 | No provisioning, no image; setup firmware recognizes unregistered MAC |
| Known route, mismatched `ID` | 403 / 403 | No status update or secret disclosure |
| Missing / incorrect display token | 401 / 401 | No image or telemetry write; firmware treats non-200 as request failure |
| Setup disabled | 403 / 403 | Enable setup deliberately during pairing |
| Registered device, no assignment / missing reference | 200 / 202 | No `image_url` or `filename`; `AssignmentReady=False` |
| Target unknown or inconsistent | 200 / 202 | No renderer call or target guess; `TargetKnown=False` |
| Initial asynchronous image pending | 200 / 202 | Device polls again; display endpoint starts no Job |
| Renderer failed, compatible prior image exists | 200 / 0 | Serve prior immutable image; expose render failure separately |
| Renderer failed, no eligible prior image | 200 / 202 | No manufactured blank image or unrelated screen fallback |

`status: 202` is carried in an **HTTP 200** body: firmware's Wi-Fi transport does
not accept HTTP 202 as a successful response. This leaves the current e-paper
image (or setup screen) and applies firmware fast-poll backoff: 5 seconds for
polls 1–50, 60 for 51–60, 900 for 61–70, then 3600. A successful image resets the
streak. A JSON `refresh_rate` on the 202 response cannot override this branch.
Never use JSON `status: 500` for renderer failure; that has a separate reset path.

Provide `POST /api/log` with device authentication and a bounded body (64 KiB),
responding 204. Initially discard log records after bounded parsing; do not log
their arbitrary messages or retain firmware logs containing credentials. Detailed
log ingestion and device special-function workflows are outside the first milestone.

## Canonical routes and authentication

Require a configured absolute public base URL, e.g. `https://byos.example.test/trmnl`.
Normalize its trailing slash, reject query / fragment / userinfo, and never derive
absolute URLs from `Host`, `Forwarded` or `X-Forwarded-*`. Configure `externalPathPrefix`
from its path (`/trmnl`) and explicitly choose `proxyStripsPrefix` (default false).
The proxy must either preserve that path or strip exactly that configured prefix;
do not autodetect rewriting. Generated URLs always contain the external prefix.

For namespace `trmnl` and device `living-room`, the Custom Server URL is:

```text
https://byos.example.test/trmnl/devices/trmnl/living-room
```

Append `/api/setup`, `/api/display`, `/api/log`,
`/images/<full-image-sha256>.png`, or `/setup-logo.png`. Namespace remains in the
route even though this version watches only one namespace; mismatched namespace
is 404. References and discovery are same-namespace only; reference objects
contain `name` and do not accept a namespace override. Reject escaped separators,
dot segments, duplicate slashes and malformed resource names without redirects.

**Firmware v1.8.17's `isSameOrigin` is actually a string prefix test against the
entire Custom Server URL.** Display images must therefore be beneath the exact
device base path; putting them at a global `/images` route would lose the auth
headers. Return absolute URLs with exactly the configured scheme / host / port
spelling, keep proxy routes on that prefix, and send no image redirects.

The initial MAC contract below is superseded by
[optional MAC registration](005-OPTIONAL-MAC.md): direct, Secret-backed, or
learned identity. [Automatic credentials](006-AUTOMATIC-KEYS.md) later adds key
generation into declared Secrets and changes setup to enabled by default.

The Phase 0 `TrmnlDevice.spec` included required, immutable `hardwareID` (canonical uppercase MAC)
and required `authSecretRef.name`, a same-namespace Secret whose `apiKey` contains
at least 32 random bytes encoded as URL-safe text. Names and MACs are routing
identifiers, not authentication. Verify registered hardware ID and token with
constant-time comparison before accepting telemetry. Duplicate hardware IDs in
the namespace make the affected devices unready; do not resolve ambiguously.
Changing physical identity requires replacing the resource, producing a new
device UID; an existing device's context-derived artifacts cannot follow a new MAC.

`authSecretRef` may change for credential rotation, but remains same-namespace.
The operator supplies the Secret; the controller never writes the token into
status or context. Setup is disabled by default (`enableSetup=false`, runtime
configuration). During pairing the operator enables setup on a trusted network;
a known route and matching pre-registered MAC may then receive its existing key.
Disable setup afterward. Setup cannot prove physical identity because the actual
firmware provides no bootstrap credential. Token rotation requires re-pairing.

Display image requests carry `ID` and `Access-Token` on the required prefix and
must authenticate the requesting device. Check that the immutable artifact was
issued to this current device UID; a digest alone grants no access. Setup-logo
is a bundled, non-sensitive PNG and is the sole unauthenticated image route:
the setup downloader sends no auth headers. Both routes return exact content
types and lengths, without compression or redirects. Require TLS outside a
trusted LAN; prefix routing does not itself secure transport.

Do not forward inbound headers, cookies, tokens or Secret values to renderers.
HTTP renderers receive only generated target parameters, `Accept: image/png`,
and for POST the allowlisted context and `Content-Type: application/json`.
Renderer `env` / `envFrom` are explicitly supplied renderer configuration; device
auth Secrets are never automatically mounted or copied. Proxy / controller logs
must redact tokens, setup response bodies and arbitrary firmware log messages.

## Public fields and defaults

| Field | Initial contract |
| --- | --- |
| `screen.spec.source` | Exactly one non-null `http` or `job`; reject unknown source keys |
| `source.http.method` | Default `GET`; exact enum `GET`, `POST` |
| `source.http.url` | Absolute `http` or `https`; nonempty host; no userinfo or fragment; valid escaping / query |
| URL query merge | Preserve other query parameters (including repeated values); replace **all** `width`, `height`, `palette` values with current target values, case sensitive |
| Renderer redirects | Reject redirects initially; URL is operator controlled and cluster-local services are valid |
| `screen.spec.refreshInterval` | Optional duration string with units `s`, `m`, `h`; positive whole seconds, bounded to `1s`–`24h`, combinations such as `1h30m` valid |
| GET interval omitted | Effective 5m: successful artifacts fresh for 5m; first miss / expiry renders on demand; no proactive GET Job |
| GET interval set | Maximum reuse age since last successful render; not a device polling instruction |
| Job interval omitted | Effective 30m; initial render once target / assignment is ready, then periodic renders |
| Job interval set | Due time = last successful render + interval; failures use bounded retry backoff, not a tight loop on overdue time |
| POST interval | Render each authenticated display request synchronously; no fresh artifact reuse or context coalescing. Omit `refreshInterval`; reject an explicit value rather than accept an ineffective setting |
| `job.contextPath` | Default `/trmnl/context.json`; absolute clean POSIX file path |
| `job.imagePath` | Default `/trmnl/output.png`; absolute clean POSIX file path; output is PNG regardless of filename |
| `job.renderer` | Required nonempty `image`; Kubernetes-style `command`, `args`, `env`, `envFrom`, `resources`; no arbitrary Pod spec |
| Device assignment | At most one of `screenRef` / `playlistRef`; neither is valid, discovery deferred until Phase 5 |
| References | Same-namespace `{name: ...}`; missing references become conditions, not schema rejection |

Conditional effective interval defaults are resolved by the controller, rather
than one global CRD default that would accidentally set an interval on POST.
Source exclusivity, enums, duration / path constraints and reference XOR belong
in structural schema / CEL where possible. Phase 1 must reject unsupported
runtime paths visibly until their phase is implemented; valid unassigned devices
receive the 202 response until discovery is implemented.

Paths cannot contain NUL, `.` / `..` components, repeated or trailing separators,
or denote `/`; context and image paths must differ and neither file may be an
ancestor of the other. Reject output-parent mounts at `/` or in `/proc`, `/sys`,
`/dev`, `/etc`, `/bin`, `/sbin`, `/usr`, or the ServiceAccount secret tree. The
controller reserves its artifact-root path and injects no such mount into a
renderer. The writable output mount hides all image files previously in its
parent directory: authors must choose a dedicated work directory. Mount the
context file read-only using `subPath`; the default shared parent and distinct
custom parents are both supported. Precreate output directories and the context
file on the PVC before scheduling. No renderer writes to the artifact repository.

HTTP requests use a 10-second overall render deadline (below firmware's normal
15-second read timeout). Validate status, content type, byte count, PNG structure,
decode bounds and target dimensions before publication. No renderer auth API,
general image conversion, browser renderer or arbitrary format negotiation is
introduced here.

### Render Context v1

Required: `version: "v1"`, `renderTarget.width`, `.height`, `.palette`,
`device.name`. Optional allowlist: `device.model`, `.firmware.version`,
`.battery.voltage`, `.battery.percent` (only when explicitly measured), and
`.network.rssi`. Missing optional measurements and empty optional objects are
**omitted**, not null / zero. Numeric zero is retained when actually reported and
valid. Do not include header dumps, hardware ID, tokens, Secret references,
Kubernetes objects, arbitrary annotations or labels. Namespace is unnecessary
because a renderer context belongs to one configured scope.

POST combines the latest authenticated request observations with valid known
state directly, without waiting for informer status propagation. Absent current
telemetry may use the latest known valid value for that identity; known stale
capability values cannot override contradictory observations. A Job receives an
immutable snapshot taken when scheduling; GET receives no context or telemetry.
Query and JSON targets must be identical. Optional fields may be added within v1;
renderers ignore unknown fields; changed meanings require a new context version.

### PNG and palette contract

For the verified OG target, require exactly 800 × 480, 2-bit noninterlaced PNG,
compression / filter methods 0, no alpha or transparency. Accept grayscale
(PNG color type 0) or indexed (type 3) with exactly four opaque gray palette
entries in ascending order: RGB `(0,0,0)`, `(85,85,85)`, `(170,170,170)`,
`(255,255,255)`. This is a deliberately narrower renderer contract than all the
firmware decoder's possible inputs. Reject truecolor, 8/16-bit, interlaced,
truncated, CRC-invalid, animated, mismatched-size and oversized output. Compressed
image bytes must be **less than 90,000**, respecting both the download and draw
limits. Validate a full bounded pixel stream, not just IHDR. Do not resample or
quantize in the controller. An ordinary PNG encoder may default to 8-bit or RGB
and must be configured by the renderer author to meet this contract.

`bw`, `gray16`, and `bwry` are semantic names reserved for later reviewed targets;
they are not currently admitted mappings. Firmware's OG display profiles and
2-bit decoder paths provide the source evidence for gray4. Physical gray levels,
orientation and successful display of both accepted encodings remain Phase 2
real-device acceptance checks. The collection experiment exercises grayscale,
not indexed-palette decoding.

## Durable metadata, publication and stale eligibility

Use a PVC containing internal `runs`, immutable `artifacts`, rendering-unit
metadata and issuance records. Never expose that layout as CRDs / cache settings.
Each rendering unit records:

| Data | Purpose |
| --- | --- |
| Namespace, screen UID, screen generation | Scope / replacement / desired-spec tracking |
| Input revision | SHA-256 of a canonical defaulted render-affecting screen definition, context version and collector contract version |
| Device UID (POST / Job) or target key (GET) | Ownership boundary; include screen UID in both keys |
| Render target / target digest | Width, height, semantic palette; exact-match eligibility |
| Attempt ID, durable monotonic sequence | Idempotent Job name / run ownership and late-result rejection |
| Context digest / context snapshot | Exact POST / Job inputs; absent for GET |
| Image digest, content type, size, encoding, immutable filename ID | Integrity, validation and device cache identity |
| Started / completed / rendered / next-due timestamps | Freshness, scheduling and recovery (UTC RFC3339) |
| Latest-success pointer and issued-to device UID records | Atomic serving selection and authorized immutable fetching |

Persist attempt metadata and the context snapshot atomically before creating
the Job. Recover by attempt labels / Job identity after restart; do not duplicate
an active pair. Store the image and metadata durably first, then atomically rename
the latest-success pointer on the same filesystem with file / directory fsync.
Check current screen / device UIDs, input revision, target and attempt ordering
again before pointer publication. A obsolete success is retained only for cleanup,
never allowed to replace current success. GET metadata has no device telemetry.

A generation / input revision change prevents **fresh cache reuse** and triggers
a new render. During a failed update, the prior successful definition's image may
be served as explicitly stale, provided screen UID, device UID for POST / Job,
current assignment and exact target still match. This is the intentional stale
fallback exception, not fresh cache reuse. Different UIDs, changed target,
different selected screen, or a deleted resource never qualify. For GET, sharing
requires equal screen UID, definition revision and target; granting access to its
bytes still records each receiving device UID. Context changes do not trigger
Jobs on every telemetry poll; future interval snapshots receive the new values.

Keep issued immutable URLs for at least 24h after last issuance; retain each
unit's latest eligible success and any active attempt regardless of that age.
Authorization requires the original current device UID even during that retained
URL period; a recreated name must not inherit access. Historical URL fetches may
finish during retention, but selection for a new display response always applies
current stale eligibility. GC owns unreferenced artifacts, issuance records and
run directories; it does not delete a live attempt, latest success or retained
issued image. Storage exhaustion rejects new publication and reports failure
without deleting the last success.

## PVC topology and collection proof

Initial supported topology: Linux, one active controller, one PVC shared with
renderer Jobs, `ReadWriteOnce` with all renderers colocated on the controller's
node by required Pod affinity (`kubernetes.io/hostname`). Kubernetes defines RWO
as a **node**, not a single-Pod, access mode; several colocated Pods can mount it.
`ReadWriteOncePod` cannot support this architecture and is rejected. `ReadWriteMany`
is not initial verified support. The filesystem / driver must support concurrent
same-node mounts, atomic rename and durable fsync.
[Kubernetes storage reference](https://kubernetes.io/docs/concepts/storage/persistent-volumes/).

The controller runs in the watched namespace so Jobs can mount the same claim.
Use `Recreate` to avoid two live writers during a Deployment rollout. While an
attempt is running, do not reschedule the controller to another node and start
new work without first accounting for / terminating old renderers. With a local
hostpath PV, recovery requires its original node; failover to another node is not
supported. With CSI RWO, recovery must await old-Pod termination and detach /
attach; multi-node recovery is a later validation requirement.

The controller creates a per-attempt context file and output directory. A renderer
mounts only that directory read/write and its context file read-only, executes
its own CLI, writes one image and exits. It receives no collector credentials,
ServiceAccount token or upload SDK. Initial Pods use non-root UID / group 1000,
`fsGroup: 1000`, drop capabilities, and forbid privilege escalation; compatible
renderer images must support those permissions and dedicated writable paths.

The controller waits for Job `Complete=True` and successful renderer termination
before reading its mounted PVC, validating output and publishing. With
`backoffLimit: 0`, a failed attempt is retried as a new attempt with a new directory;
failed container output is never published. A completed Job with a missing or
partial file is a collection failure, not success. Keep finished Jobs until
collection / terminal bookkeeping is durable (no premature TTL deletion).
Use owner references for Job cleanup plus controller-managed filesystem GC;
Kubernetes garbage collection does not remove arbitrary PVC directories.
[Kubernetes Job reference](https://kubernetes.io/docs/concepts/workloads/controllers/job/).

### Collection experiment results

Performed 2026-10-07 on Kubernetes `v1.36.2`, single-node MicroK8s, Linux/amd64,
with a hostpath-backed `ReadWriteOnce` PVC. The renderer was `busybox:1.37.0`;
the collector used `python:3.13-alpine` and only the Python standard library.
This section preserves the method, observations and limitations. The disposable
prototype scripts and raw run report are not retained; hostnames and local
StorageClass names are not part of the project record.

The experiment used the following procedure:

1. Create an isolated namespace, a 64 MiB PVC and a collector Pod mounting the
   claim. Precreate each attempt's context file and output directory on the PVC.
2. Create renderer Jobs with required affinity to the collector's node. Mount
   only the attempt's output subtree read/write and its context file read-only
   using `subPath`; use non-root UID/GID 1000 and `fsGroup: 1000`.
3. Inside each renderer, check that the context file is readable and that no
   ServiceAccount token is mounted. Copy a supplied 800 x 480, grayscale 2-bit
   PNG to the configured output path and exit; the renderer performs no API
   calls or uploads. This proves file output / collection, not image generation.
4. Outside the renderer, check the Job's terminal condition and exit code.
   Collect only successful Jobs; validate the bounded PNG structure, CRCs and
   decoded pixel stream. Publish immutable bytes before replacing the
   latest-success pointer with an atomic rename and file / directory fsync.
5. Inject failed and incomplete outputs. Simulate interruption between immutable
   image copy and pointer publication, recreate the collector Pod with the PVC
   intact, and retry collection. Finally remove the isolated namespace and its
   test resources, including the PVC.

| Case | Observed result |
| --- | --- |
| Default `/trmnl/context.json` and `/trmnl/output.png` | Job completed with exit 0; validated image collected and published |
| Custom `/input/context.json` and `/work/weather.png` | Distinct parent directories worked; Job completed with exit 0 and image published |
| Valid image written, then renderer exit 7 | Job failed; output was not collected; previous success preserved |
| Renderer exit 0 with only the first 40 PNG bytes | Job completed; collection rejected the truncated image; previous success preserved |
| Renderer exit 0 without an output file | Job completed; collection failed; previous success preserved |
| Immutable image copied before pointer update, then collector Pod recreated | Previous pointer survived; completed run remained available on the PVC; subsequent collection published it |
| Repeated collection of the same completed run | Image digest and pointer remained unchanged |
| RWO shared mounts | All renderer Pods ran on the collector's node and used the same claim |
| Renderer credentials | Token absence checked inside each renderer; no collector credentials or upload SDK supplied |

All listed cases passed. Namespace cleanup completed after the experiment.
The interruption case was simulated by creating an unreferenced immutable file;
it does not prove recovery from power loss or a crash at every filesystem write.
The single-node experiment also cannot prove affinity behavior across multiple
nodes, CSI detach / attach recovery or RWX support. A deny-egress NetworkPolicy
was configured for renderers, but its enforcement was not probed. PNG validation
covered the fixture's grayscale, unfiltered subset, not all accepted encodings.
Production scheduling, UID / revision gating, GC and real-device display remain
later-phase checks. Phase 4 should implement integration tests for these cases
against the production Go controller and the selected storage driver.

## Phase 0 exit and next acceptance checks

The language, group, resource scope, storage topology, field defaults, firmware
responses, unknown-target policy and initial-image policy are settled here.
The minimum supported Kubernetes version was an open Phase 0 deliverable at the
time of this record. [Phase 1](002-PHASE1.md#kubernetes-support-floor) resolves it
as `v1.36.0` within minor 1.36, with generated-CRD / controller API integration
checks on `v1.36.0` and `v1.36.2`, and local-cluster deployment on `v1.36.2`.
Phase 0 itself adds no production controller or CRD.

Phase 2 must verify pairing, prefix-preserving / prefix-stripping proxy paths,
authenticated display image fetch, anonymous setup logo, grayscale and indexed
2-bit images, filename caching, 202 polling, latest request telemetry and renderer
outage on the user's real OG / 1.8.17. No captured traffic was available in Phase 0.
Phase 4 must validate actual scheduler restart / obsolete-attempt rejection and
the selected deployment's storage driver. These are acceptance checks, not
unresolved choices required to guess the scaffold.
