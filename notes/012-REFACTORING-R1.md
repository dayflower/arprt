# R1 — Correct dependency direction

Completed: 2026-10-09. Source baseline: `2dc6b4d` (R0 behavior preservation baseline).

Implemented the bounded R1 scope from [the refactoring plan](010-REFACTORING-PLAN.md#r1--correct-dependency-direction). R2-R4 remain separate decisions.

## Changes

- Moved `controller.Observation` and `TestContextAllowlist` to [`internal/devicestate`](../internal/devicestate/context.go). Updated BYOS, Job rendering, and the POST test helper to use `devicestate.Observation`.
- Moved `controller.StorageMaintenance` to [`internal/maintenance`](../internal/maintenance/maintenance.go). Updated the controller entry point and Job maintenance caller.
- Updated the [R0 coverage map](011-REFACTORING-R0.md) to follow the relocated allowlist test.

The three moved files are byte-for-byte unchanged after their package declarations. No policy, scheduling, capacity, metrics, public protocol, or persistent format was changed. Production BYOS and Job rendering no longer import `internal/controller`; BYOS identity tests still use the actual Device reconciler.

## Wiring and regression review

- HTTP-only mode still registers `StorageMaintenance` with the manager only when `jobConfig.PVC == ""`. Its immediate collection, one-minute ticker, cancellation handling, and leader-election behavior are unchanged.
- Job mode still invokes assignment-aware `Collect` at the end of `Reconciler.maintenance`, under the existing `r.mu` lock and deferred unlock. No additional concurrent maintenance runnable was registered.
- The moved `TestContextAllowlist` and existing POST telemetry tests protect the observation boundary.
- Existing `TestMaintenanceDropsPointersForPreviousTransport`, `TestAllEligiblePairsBoundRemovalAndRetention`, `TestCollectionKeepsLatestAndIssuedImages`, and `TestCollectionDoesNotEraseBindingNewerThanDeviceSnapshot` passed in the race suite.
- As recorded in R0, the HTTP-only runnable lifecycle is a source-review check; the existing suite exercises the shared collection service through Job maintenance and artifact retention tests. No new lifecycle coverage is claimed.

## Verification

Go 1.26.8, cached dependencies (`GOPROXY=off`), and bundled envtest Kubernetes binaries were used.

| Check | Result |
| --- | --- |
| `GOPROXY=off make verify` (generated manifests/deepcopy diff, tidy diff, gofmt, vet, race suite, controller build) | Passed |
| `GOPROXY=off KUBEBUILDER_ASSETS="$PWD/bin/k8s/k8s/1.36.0-linux-amd64" go test -tags=integration -count=1 -v ./internal/integration` | Passed |
| Same integration command with `1.36.2-linux-amd64` | Passed |
| Compare moved files against baseline, excluding only package declarations | Identical |
| Inspect production imports with `go list` | BYOS and Job rendering have no `internal/controller` dependency |

The pre-existing untracked source plan was left unchanged. No deployment, cluster smoke run, Docker build, or physical-device session was performed; the plan reserves the cluster matrix for R2/R3 milestones.
