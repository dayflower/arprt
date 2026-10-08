# Phase 3 — Device-aware synchronous HTTP POST

Software implementation, validation and initial real-device POST delivery
completed 2026-10-08. This phase builds
on completed Phase 2. The user confirmed on 2026-10-08 that Phase 2's remaining
physical-device acceptance checks had been completed on 2026-10-07; see
[the Phase 2 acceptance record](003-PHASE2.md#physical-device-acceptance-progress).

## Implementation

- HTTP POST sends normalized target query parameters, `Accept: image/png`,
  `Content-Type: application/json`, and complete Render Context v1. Query and
  body targets must match. It shares GET's bounded HTTP execution, redirect
  rejection, ten-second deadline and strict PNG validation.
- Display handling builds context from the direct device-state snapshot plus
  the current authenticated request's allowlisted observations. It never waits
  for the Kubernetes informer cache to observe a status write. Missing or invalid
  current measurements can use known valid values; credentials, hardware IDs,
  Secret references, cookies and arbitrary headers remain excluded.
- Each authenticated POST display request renders independently, including
  unchanged telemetry. There is no fresh cache reuse or context coalescing.
  `refreshInterval` remains forbidden for POST by the existing CRD validation.
  The Phase 0 freshness row and DESIGN.md now state this policy explicitly;
  public fields, defaults and Context v1 serialization remain unchanged.
- Internal artifacts are keyed by screen UID, device UID and exact target.
  Successful metadata includes the context digest, input revision, generation,
  random attempt identity and sequence. Immutable bytes may use the existing
  digest storage, but fallback pointers and issuance stay device-specific.
- Attempt ordering is allocated before HTTP execution, per screen/device pair
  across targets, and resumed from persisted successful sequences after restart.
  A newer attempt supersedes earlier in-flight responses. Publication directly
  rechecks current screen/device UIDs, desired state, assignment and observed
  target; obsolete output cannot replace a success pointer.
- GET and POST share eight execution slots. Existing per-device display gates
  serialize telemetry/status writes and requests; different devices can render
  independently. Status conflict retries preserve concurrent reconciler changes.
- Atomic PVC publication, authenticated immutable image serving and stale-success
  fallback reuse Phase 2's repository. Invalid output and timeouts preserve a
  pair's compatible prior image, including a prior screen definition. Without
  eligible output, display returns HTTP 200 / JSON 202.
- Screen/device reconcilers now accept GET and POST assignments. Readiness
  conditions retain the existing meanings; Job/playlist/discovery remain deferred.

## Example and freshness semantics

[The device-status example](../examples/http-post/renderer.py) uses only Python's
standard library to draw name, model, firmware, voltage and RSSI as a gray4 PNG.
Its Kustomize manifests deploy a non-root renderer without a ServiceAccount
token. [README](../README.md#renderer-contract) documents installation, assignment,
standalone HTTP use and direct consumption of the shared Context v1 fixture.
Missing telemetry displays UNKNOWN, unknown context fields are ignored, and
battery percentage is never inferred from voltage.

| Source | Render timing | Context/freshness |
| --- | --- | --- |
| GET | First miss, expiry or definition change, on demand | No device context; equal targets share output for up to `refreshInterval`, default 5m |
| POST | Every authenticated display request, synchronously | Latest request plus known state; pair-owned output; no fresh reuse or interval |
| Future Job | On scheduling, asynchronously | Immutable scheduling snapshot; interval default 30m; display reads completed output |

Job execution is a Phase 4 contract, not an implemented feature.

## Validation record

Passed on 2026-10-08:

- `make verify`: generated CRD/deepcopy consistency, module tidiness, formatting,
  vet, all race tests and static controller build.
- `make integration ENVTEST_K8S_VERSION=1.36.0` and `1.36.2`: real API servers
  and running production reconcilers, concurrent GET/POST display and authenticated
  image fetches, current voltage/RSSI in POST context, eight POST requests causing
  eight renders, and status/readiness preservation without spec edits.
- HTTP contract tests: the common Context v1 fixture, target query/body equality,
  target-key replacement and repeated custom parameters, required headers,
  mismatched/missing contexts, redirects, timeouts, bad status/content type,
  oversized output and malformed PNG for both GET and POST.
- BYOS/PVC tests: current and absent/invalid telemetry, credential/header
  exclusion, equal-target device isolation, per-device initial failure and stale
  fallback, persisted context metadata, reopening/restarting, changed-definition
  fallback, replaced UIDs, and timeout preservation of latest success.
- Deterministic concurrency tests: a late older response cannot replace a newer
  successful attempt; changes to target, screen definition, screen/device UID
  or assignment during rendering reject publication. An injected status conflict
  preserves both current request context and the concurrent controller condition.
- Python example: both shared context fixtures rendered outside Kubernetes;
  HTTP POST returned identical output, accepted unknown fields, rejected a
  mismatched query target, and produced bounded 800 × 480, grayscale 2-bit PNG
  with valid CRCs and complete scanlines. `kubectl kustomize examples/http-post`
  successfully expanded its ConfigMap, Deployment, Service and POST screen.

## Real-device POST delivery

Verified on 2026-10-08 using the existing Kubernetes `v1.36.2` deployment, POSIX
ReadWriteOnce PVC, HTTPS Gateway with its configured path prefix preserved, and the actual
OG / firmware `1.8.17`:

- Re-ran `make verify`, built the production Dockerfile and pushed the Phase 3
  image to the existing registry. The controller was updated to the immutable
  image digest using its existing Recreate strategy; rollout completed.
- Applied `examples/http-post`, checked its non-root renderer's HTTP PNG output
  in the deployed container, and changed the actual device's explicit assignment
  from `photo` to `device-status`.
- An actual authenticated display request at 08:55:23 JST succeeded with
  `RenderReady=True / ImageReady` for the new device generation. The POST screen
  became `Ready=True / ImageReady`.
- Read the successful PVC metadata and checked screen/device UIDs, exact target,
  screen generation and issuance ownership. The persisted context digest matched
  Render Context v1 rebuilt from that request's observed voltage/RSSI and device
  state. The first POST success had sequence 1 and a 1,253-byte PNG.
- Fetched that immutable image with device authentication through the existing
  public HTTPS Gateway. HTTP status, content type, content length and bytes
  matched the validated stored artifact. Credentials were used only in memory.
- The user confirmed the physical display showed DEVICE STATUS with the voltage
  and RSSI fields.

The device remains assigned to `device-status` for continued POST use. The
previous controller image and `photo` assignment were saved outside the
repository for rollback. Actual hostnames, hardware IDs and credentials are not
recorded here. This pass verifies initial POST delivery and current telemetry;
fault/concurrency scenarios are covered by the software tests above.

Phase 2's completed physical-device acceptance is recorded separately in
[the Phase 2 acceptance record](003-PHASE2.md#physical-device-acceptance-progress).

## Limits

One active controller and the existing POSIX PVC topology remain required.
Artifacts/issuance and per-process device gates/POST pair counters are retained
without collection in this phase; operational bounds and GC remain later work.
Synchronous HTTP attempts do not need restartable execution journals: interrupted
requests retry on the next display poll, with successful pointers/issuance durable.
Asynchronous Job scheduling, in-flight recovery and collection remain Phase 4.
