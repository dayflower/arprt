# Phase 6 — Operational hardening and initial release

## Operational contract (2026-10-08)

This phase keeps the public CRDs, Render Context v1, renderer image contract,
and single-active-controller topology unchanged.

- Metrics use fixed operation/outcome labels only. Resource names, UIDs, MACs,
  URLs, headers and credentials are never metric labels. The metrics listener
  is opt-in; the supplied Deployment binds it on port 8082 without public routing.
- BYOS admits at most 64 concurrent requests, with the existing 13-second
  request context and 10-second shared HTTP renderer budget. Excess requests
  receive 503. Unchanged observations coalesce last-seen writes within 30s;
  changed telemetry and screen-cycle transitions remain immediate.
- Artifact admission defaults to 768 MiB of managed data and 64 MiB of filesystem
  free-space reserve. Admission failure preserves existing successes and issued
  URLs. These are controller admission limits, not a filesystem quota on renderer
  writes. Size the PVC and use filesystem quotas for untrusted renderer images.
- Maintenance runs every minute in HTTP-only mode as well as Job mode. Retention
  protects eligible successes and all URLs issued in the preceding 24h. Invalid
  retention metadata aborts destructive artifact collection. Identity and Job
  bookkeeping corruption fails closed and requires restoration from backup.
- Regenerable corrupt latest pointers are discarded, and bad blobs can be
  replaced only by newly validated bytes with the same digest. Identity and
  issuance records are never inferred from image files.
- Job failures use the existing bounded exponential retry delay. After seven
  consecutive failures, retrying slows to at least the screen refresh interval
  (and at least five minutes), so a recovered renderer can recover automatically.
  The counter saturates; there is no unbounded tight retry loop.

## Implementation

- Added Prometheus rendering/failure, request admission, collection, queue,
  device freshness, artifact age and storage metrics with fixed labels. The
  Deployment opts into an internal port; standalone mode defaults to disabled.
  HTTP outcome changes emit redacted structured logs and warning Events. Device
  reconciliation exposes a separate `DeviceSeen` condition.
- Bounded BYOS admission at 64 requests, replaced permanent per-UID mutexes with
  fixed stripes, and retire POST ordering records after all active attempts
  complete. Detached GET work now follows manager shutdown cancellation.
  Unchanged device observations coalesce within 30 seconds; current telemetry
  still reaches synchronous POST immediately. Issuance writes coalesce within
  one minute with compensating retention slack.
- Added managed-byte/free-space admission for images, identity/issuance records,
  scheduler metadata and Job attempts. Existing issuance bookkeeping can use
  reserve space to preserve stale delivery. The filesystem is never treated as
  a renderer quota. Saturated failure backoff follows the screen interval.
- Added bounded metadata reads and validation, safe regeneration of invalid
  latest pointers and corrupt blobs, preflight validation of retention roots,
  cleanup of abandoned atomic writes/filename reservations, protection for new
  hardware bindings against older API snapshots, and an exclusive
  writer lock. Durable identity/issuance and scheduler damage fails closed.
  Collection also verifies the immutable context digest and refuses special
  context files.
- Extracted assignment-aware artifact maintenance for HTTP-only and Job mode;
  protected successes and issued URL bytes retain the original ownership
  semantics. Updated operations, recovery, trust-boundary and support docs.
- Added an operations cluster check that installs all documented renderer
  examples, injects pointer/blob corruption and capacity pressure, verifies
  metrics/log redaction and HTTP-only collection. Added the complete four-script
  cluster matrix to Makefile and CI using kind v0.33.0 and a pinned 1.36.4 node.

## Validation

- `make verify`: generation/dependency/format consistency, vet, full race tests
  and controller build passed.
- Final-source API integration on Kubernetes 1.36.0 and 1.36.2 passed, including
  the existing Job watch/restart and HTTP/context/assignment regression cases.
- Fault tests cover preserved issuance under capacity pressure, free-space
  reserve rejection, pointer/blob regeneration, fail-closed retention corruption,
  exclusive locking, abandoned-write cleanup, scheduler and context corruption,
  invalid filename/issuance records, new-binding/GC races, automatic
  recovery after repeated failures, immediate changed telemetry, coalesced stable
  polls and shutdown cancellation without publication.
- A 16-device × 64-screen discovery scenario creates 1,024 durable pairs while
  active Jobs/run directories stay at the configured concurrency limit. This
  verifies bounds, not production throughput.
- The complete E2E matrix passed on disposable Linux kind / Kubernetes 1.36.4
  with the final distribution image (digest
  `sha256:46065a3e7bf768b14a37a0a6a30392d8f98f732b6b5f02afa180ed1f176df119`):
  `cluster-smoke.py`, `job-smoke.py`, `playlist-smoke.py` and
  `operations-smoke.py`. This includes clean manifests/examples, both proxy
  prefix modes, all authentication/MAC modes, GET/POST/Job delivery, playlists
  and discovery, startup/restart and offline Job recovery, failures/timeouts,
  reassignment, corruption, capacity pressure, stale output, authenticated URL
  retention, log/metric redaction, HTTP-only GC and namespace-scoped RBAC.
- CRD/deepcopy generation remained unchanged. Python scripts compile, default
  manifests and the Envoy Gateway overlay assemble, and the final Docker build
  passed. The dedicated cluster/test namespaces were removed; the existing
  cluster and default kubeconfig were not changed by disposable tests.

## Release acceptance

The final-source software matrix is complete. The same verified image was
rolled out to the existing Kubernetes 1.36.2 device deployment. The original
public URL, authentication, PVC, single device-status POST assignment and
300-second polling interval are preserved. Metrics are exposed internally on
8082 without a public Service/Gateway route.

- At 20:44:11 JST on 2026-10-08, the actual OG / firmware 1.8.17 sent its
  ordinary display request to the Phase 6 controller. The POST publication
  (sequence 63, 1,237-byte PNG) matched current telemetry/context digest, screen
  and device UID ownership and target. Authenticated fetching through the
  existing HTTPS Gateway returned the exact image bytes.
- Internal metrics were reachable, contained no auth key/MAC/device-name values,
  and reported successful maintenance. The existing public Service/Gateway does
  not expose the metrics port. Disposable validation left the default kubeconfig
  on its original context.
- A subsequent controller restart preserved the complete latest publication
  metadata and the exact already-issued image URL. Authenticated Gateway fetch
  returned the same 1,237 bytes and digest after restart. Internal metrics and
  successful maintenance also recovered. The original assignment and 300-second
  polling interval remain in place; no test resources were added to this namespace.
- On 2026-10-08, the user confirmed that the physical device displays the
  device-status screen normally, completing the visual acceptance check.

Phase 6 is complete. The initial release is ready within the documented
single-active-controller, single-namespace, POSIX RWO PVC, Kubernetes minor 1.36
and TRMNL OG / firmware 1.8.17 support scope. No Git tag or public release has
been published.
