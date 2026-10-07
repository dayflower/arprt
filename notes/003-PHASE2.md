# Phase 2 — HTTP GET delivery

Software implementation and physical-device acceptance completed 2026-10-07.
On 2026-10-08, the user confirmed that the remaining physical-device checks had
also been completed on 2026-10-07; the acceptance record below reflects that
confirmation.

## Implementation

- BYOS setup/display/log endpoints, strict namespace/device routes, configured
  public origin and path prefix, explicit prefix preservation/stripping, and
  anonymous bundled setup calibration PNG.
- Setup defaults to disabled. Registered hardware identities and Secret-backed
  URL-safe API keys are checked before accepting display telemetry or issuing
  images. Identity/token aliases normalize underscores; duplicates are rejected.
  Images require issuance to the current device UID, matching identity and the
  current token. Secret rotation takes effect without cache delay.
- Reviewed `og` / `1.8.17` capabilities resolve to 800 × 480 / gray4. Missing
  observations may use compatible known state; changed tuples, malformed or
  contradictory dimensions invalidate the target. Invalid optional telemetry
  is omitted and reported by field name. Voltage never becomes inferred percent.
- Status-only optimistic patches with direct reads and conflict retries preserve
  reconciliation updates. Display handling serializes per device; different
  devices can coalesce a GET render. Auth and current resource state use uncached
  API reads, including gates after rendering and before publication/issuance.
- HTTP GET sends target query values and `Accept: image/png` only. Other query
  values survive, including repetitions; all lowercase target keys are replaced.
  No identity, telemetry, tokens or inbound headers enter renderer requests.
- Ten-second render deadline, eight execution slots, redirect rejection, HTTP
  status/content-type checks, <90,000-byte output, strict PNG chunks/CRCs,
  bounded complete scanline inflation and full decoding. Grayscale and indexed
  2-bit output are accepted under the Phase 0 gray4 contract. No conversion.
- PVC atomic publication, immutable digest URLs, <=31-character stable filenames,
  target/screen UID-based GET sharing and concurrent-request coalescing. Freshness
  checks both definition revision and generation. Before publication, re-read the
  screen UID/spec/revision; before issuance, recheck the device, assignment and
  current target. Old UIDs and unequal targets never qualify as fallback.
- GET refreshInterval defaults to 5m, device polling independently to 300s.
  A failed refresh preserves compatible prior success and reports stale delivery.
  Without eligible output, return HTTP 200 / JSON 202 without image fields.
- Service, public URL ConfigMap, BYOS listener and manager shutdown integration;
  minimal dependency-free renderer and pairing/install instructions in README.
  POST, Job, playlist and discovery were unavailable at the Phase 2 milestone;
  [Phase 3](004-PHASE3.md) subsequently adds synchronous HTTP POST.

## Runtime RBAC clarification

Default namespace RBAC grants `get` on **only `living-room-auth`**, with
`resourceNames`, and grants no Secret list/watch. Operators must add each
additional device auth Secret name explicitly to that allowlist. Authentication
uses the device's same-namespace authSecretRef with direct reads. A Secret that
is missing, unreadable or incorrectly encoded produces `AuthenticationReady=False`
with `InvalidCredentials`; no credentials enter status or Render Context.
This narrows runtime permission without changing the public API.

## Conditions

| Condition | Meaning |
| --- | --- |
| Screen Ready | Last requested GET outcome for current generation, or AwaitingRender before the first request |
| Device IdentityUnique | Registered hardware identity has no namespace duplicates |
| AuthenticationReady | Referenced and permitted auth Secret contains a usable apiKey |
| AssignmentReady | Explicit same-namespace HTTP GET screen exists |
| TargetKnown | Reviewed current model/firmware and compatible dimensions |
| TelemetryValid | Invalid optional observations are named, with their values omitted |
| RenderReady | ImageReady on success, RenderFailed on failed refresh |
| Ready | Image available, including StaleImageAvailable fallback |

Conditions retain transition timestamps when status does not change. The
reconciler preserves request observations and render outcomes for the current
device generation. It checks credentials at least once per minute; requests
check them immediately. Screen Ready describes the last demand-driven attempt,
not an active renderer health probe or readiness of every possible target.

## Validation record

Passed on 2026-10-07:

- `make verify`: generated artifacts/module tidiness/formatting, vet, race tests
  and static build.
- Envtest on Kubernetes 1.36.0 and 1.36.2: existing API contracts plus concurrent
  authenticated display and image requests while production reconcilers run;
  latest voltage/RSSI and readiness survive status conflicts, without spec edits.
- HTTP/protocol tests: setup enable/disable, anonymous setup PNG, strict routes
  and both prefix modes, alias conflicts, auth rejection without telemetry writes,
  log size bounds, unknown capabilities, optional invalid telemetry, explicit
  assignment failures, POST/Job/discovery/playlist rejection, and configured URLs.
- Renderer/PVC tests: equal-target device sharing with separate issuance,
  concurrent coalescing, unequal-target isolation, restart/reopening, malformed
  refresh and outage preservation, missing initial image, replaced screen/device
  UIDs, changed definitions during rendering, GET privacy, timeouts, redirects,
  status/type/size rejection, grayscale/indexed images, bad dimensions/depth/color,
  CRC errors, truncation, interlace and bounded decoded streams.

- Production Dockerfile build and local MicroK8s Kubernetes v1.36.2 deployment:
  an isolated namespace with the production non-root controller, Recreate
  Deployment and hostpath-backed RWO PVC. Setup returned the supplied Secret key;
  anonymous setup PNG and authenticated indexed display PNG had exact lengths;
  display telemetry reached status and repeated polls reused the same URL/filename.
- Changed the serving mode from prefix preservation to prefix stripping during
  a Recreate restart, then fetched a previously issued image from the reopened
  PVC. Scaling the renderer to zero and changing the screen refresh interval
  preserved the old image with a failed current render. Recreating the screen
  during that outage produced HTTP 200 / JSON 202, without reusing the old UID.
- Local-cluster RBAC checks allowed only the named auth Secret and namespace
  resource access, and denied unrelated Secret reads, Secret list, other-namespace
  resource reads and Job creation. Setup was disabled after pairing. Probes passed;
  the test namespace, PVC and unused test CRDs were removed afterward.

Commands: `make verify`, `make integration ENVTEST_K8S_VERSION=1.36.0`,
`make integration ENVTEST_K8S_VERSION=1.36.2`, `docker build`, and
`python3 hack/cluster-smoke.py --image <cluster-accessible-image>:<tag>`.
The cluster check uses port forwarding to emulate preserved/stripped proxy
paths; it does not deploy an Internet-facing proxy, prove power-loss durability,
or establish physical-device compatibility.

## Physical-device acceptance progress

On 2026-10-07, DNS/TLS verification, route Accepted/ResolvedRefs, authenticated
display and image fetching passed through an Envoy Gateway HTTPS listener with
a preserved `/trmnl` prefix.

The [generic deployment example](../config/overlays/envoy-gateway/README.md)
uses reserved example hostnames and configurable Gateway/image references.
Private hostnames, cluster identifiers, real MAC addresses and credentials are
not recorded in the repository.

Initial physical checks passed on 2026-10-07:

- The actual device paired through the configured HTTPS URL with `/trmnl` prefix.
- Authenticated display reported model `og` and firmware `1.8.17`. The reviewed
  target passed validation, with device `Ready=True`, `RenderReady=True` and
  `ImageReady`.
- Four vertical bands were confirmed on the physical screen. This verifies
  initial grayscale 2-bit PNG delivery/display through the actual Gateway.
- Setup was disabled, requiring a Recreate controller restart. Another actual
  authenticated display succeeded afterward; pairing remained intact and PVC
  artifacts survived. Setup now returns HTTP 403.

The remaining acceptance checks, including indexed output and physical
outage/initial-pending/backoff behavior, were completed on 2026-10-07, as
confirmed by the user on 2026-10-08. Phase 2 physical-device acceptance is complete.

For future regression checks, follow README pairing instructions using the
actual OG / 1.8.17 and a URL such as
`https://<your-host>/trmnl/devices/trmnl/living-room`, then repeat:

1. Enable setup temporarily, pair the registered MAC and retrieve the setup PNG;
   disable setup again and confirm continued authenticated display/image fetch.
2. Display all four gray bands using grayscale output, then change the renderer
   URL to include `?encoding=indexed` and confirm indexed output and orientation.
3. Repeated fresh polls keep the same short filename; changed image bytes use a
   different filename. Observe voltage/RSSI and last-seen status.
4. Stop the renderer and force a refresh; the prior image stays visible. Restart
   the controller and verify the same immutable image can still be fetched.
5. With no eligible initial image, verify JSON 202 leaves existing content intact
   and invokes the firmware's fast-poll backoff without a reset/OTA instruction.

Public ingress/proxy configuration is specific to the user's environment and
is not provisioned by the test script.

## Limits and deferred work

One active controller and the existing POSIX PVC topology remain required.
Artifacts and issuance records are retained indefinitely in Phase 2, satisfying
the minimum 24-hour post-issuance fetch window; GC/capacity limits remain later
work. GET success sequences resume from persisted latest-success metadata;
asynchronous in-flight attempt journals belong to Phase 4. Per-device request
gates last for the serving process lifetime. Operational metrics and further
storage/memory bounds remain Phase 6 work.
