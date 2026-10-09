# R2 — Make display delivery an explicit sequence

Completed: 2026-10-09. Source baseline: `d9dff47` (R1 shared device state and maintenance extraction).

Implemented the bounded R2 scope from [the refactoring plan](010-REFACTORING-PLAN.md#r2--make-display-delivery-an-explicit-sequence). R3 and the optional R4 phases remain separate decisions.

## Changes

- Kept `Server` construction in [`server.go`](../internal/byos/server.go). Moved routing, admission, authentication and response helpers to [`routing.go`](../internal/byos/routing.go), setup response handling to [`setup.go`](../internal/byos/setup.go), image serving and the bundled setup image to [`images.go`](../internal/byos/images.go), and manager lifecycle handling to [`listener.go`](../internal/byos/listener.go).
- Made [`display.go`](../internal/byos/display.go) explicitly coordinate persisted observation, assignment selection/replay, GET/POST/Job image acquisition, final issuance validation, issuance persistence, cycle persistence, best-effort device reporting and response encoding. HTTP error mapping remains in the endpoint.
- Added a private `selectedDelivery` value containing the selected screen, target, normalized revision, complete assignment fingerprint, cycling mode, copied replay record and reporting owner. POST context construction still overlays current request telemetry on the direct status snapshot returned by observation persistence.
- Replaced nullable assignment and variadic asynchronous booleans with private assignment states (unexamined/failed/resolved), delivery states (pending/fresh/stale), and reporting owners (HTTP/Job/replay). [`status.go`](../internal/byos/status.go) documents which conditions each stage owns.

## Ordering and compatibility review

- Observation status persistence is mandatory. Optimistic retries still re-read device status directly, preserve unrelated fields/conditions and overlay current request capabilities and telemetry. Stable polls still suppress no-op status patches.
- `validateIssuance` remains after image acquisition, including replay lookup and failed/pending rendering, and before `Repository.Issue`. It performs fresh device/screen reads with the original UID, generation, spec, deletion and target predicates; resolves the complete current assignment fingerprint; checks hardware identity; and reads the current Secret. GET and POST publication predicates remain separate and unchanged.
- HTTP screen outcome reporting remains best effort and occurs after final validation but before issuance, including when no image is available. Job delivery preserves Job-owned screen/device rendering status. Replay preserves the prior serving reason/message and independently reported render conditions.
- An image is acknowledged only after issuance and any new cycle turn are persisted. Cycle replay retains its exact digest, filename and persisted serving time. Failed issuance/cycle writes still return HTTP 503; reporting failures still permit delivery.
- Common setup admission, credential provisioning and identity registration retain their existing order in routing. The setup handler still checks identity again and reads the saved current Secret before returning the key. Only the bundled setup image bypasses authentication, after the device lookup.
- Admission remains 64 requests with the same fixed display stripes and timeout scopes. GET/POST still share the existing HTTP rendering budget; listener cancellation still reaches detached GET work.
- CRDs, defaults, protocol bodies, URLs, filenames, Render Context v1, persistent schemas/layouts, repository implementation, Job scheduling and retention policy are unchanged.

## Regression evidence

The [R0 coverage map](011-REFACTORING-R0.md) still applies. Existing tests cover GET sharing/cancellation, POST snapshots/freshness/ordering, Job polling, stale fallback, input/identity/credential changes, cycle selection and exact restart replay, persistence barriers, status conflicts and best-effort reporting errors.

Added two endpoint-level tests in [`status_test.go`](../internal/byos/status_test.go) for condition-preservation assertions not previously explicit:

- `TestPendingDisplayPreservesStatusOwnership`: unknown capabilities preserve the unexamined assignment condition; missing/unsupported screens report failed assignments; all three cases preserve separately owned render and unrelated conditions and return the exact pending body without rendering.
- `TestReplayPreservesRenderOutcomesAndObservesTelemetry`: a POST cycle retry preserves the acknowledged response and cycle record, device serving/render conditions and separately updated screen rendering status, while accepting current telemetry and avoiding another render.

These tests assert public delivery/status behavior, not the extracted function or file layout. No production fault-injection seam, clock framework, public type, interface or dependency was introduced.

## Verification

Go 1.26.8, cached dependencies (`GOPROXY=off`) and bundled envtest binaries were used for Go checks. Cluster verification uses a dedicated kind cluster and a separate kubeconfig under `/tmp`; the existing default cluster is not used.

| Check | Result |
| --- | --- |
| Baseline `GOPROXY=off make verify` | Passed |
| Mechanical file split: existing BYOS suite | Passed |
| Stage extraction: existing BYOS race suite | Passed |
| Compare 15 unchanged constructor/routing/status/response/listener functions against the baseline AST | Identical |
| Final `GOPROXY=off make verify`, including added status tests | Passed |
| API integration with Kubernetes 1.36.0 | Passed |
| API integration with Kubernetes 1.36.2 | Passed |
| `make docker-build IMAGE=arprt:refactor-r2-20261009` | Passed |
| HTTP smoke on disposable kind / Kubernetes 1.36.4 | Passed |
| Job/PVC smoke on the same disposable cluster | Passed |
| Playlist smoke on the same disposable cluster | Passed |
| Operations smoke on the same disposable cluster | Passed |

The cluster matrix uses kind v0.33.0, node image `kindest/node@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed`, cluster `arprt-r2-20261009`, kubeconfig `/tmp/arprt-r2-20261009.kubeconfig`, and controller image `arprt:refactor-r2-20261009`. Smoke scripts verify real renderer Pods, PVC mounts, restart recovery and serving behavior.

The complete `make cluster-smoke IMAGE=arprt:refactor-r2-20261009` matrix exited successfully. All test namespaces and created CRDs were removed, then the dedicated kind cluster was deleted. The default kubeconfig context remains `microk8s`.

No deployment to the existing cluster or physical-device session is part of this refactoring. The pre-existing untracked source plan is left unchanged.
