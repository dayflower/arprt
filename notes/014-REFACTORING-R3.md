# R3 — Expose recoverable Job transitions

Completed: 2026-10-09. Source baseline: `b293663` (R2 display workflow extraction). Implements the bounded R3 scope in [the refactoring plan](010-REFACTORING-PLAN.md#r3--expose-recoverable-job-transitions).

## Changes

Change A extracts six private stages into [`recovery.go`](../internal/jobrender/recovery.go): durable attempt preparation, submission of a prepared attempt, recovery of a submission whose save was interrupted, Job observation, successful collection/direct revalidation, and publication/completion bookkeeping. `reconcilePair` retains state replacement, due checks and admission; `advance` retains terminal-reason recovery and the Job lookup that selects submission or observation. Existing `desired`, `cancel`, `failed`, `Store.prepare` and `Store.collect` retain their policies.

Change B adds a private `Store.walkRecords` operation in [`store.go`](../internal/jobrender/store.go). It enumerates, decodes, validates and checks each record immediately before invoking the caller. `Store.records` still produces the migration-compatible snapshot used by startup and reconciliation. Maintenance processes records and pending-file entries through the same traversal, retaining cleanup, cancellation, desired-pair decisions, metrics and retention policy in the reconciler.

Change A was verified against the existing Job race suite before applying Change B. The added Change B regression tests passed against the pre-consolidation implementation, then the complete Job race suite passed after consolidation.

## Ordering and compatibility review

- `Reconcile` and `maintenance` still hold the same mutex for their complete operations. No independent maintenance runnable, new package/interface or persisted phase is introduced.
- Preparation follows the original admission Job count, spec/context construction and capacity checks. Context is prepared before sequence/Active are saved; both remain durable before Kubernetes creation. Submission retains its separate namespace-scoped fresh Job count, deadline, Pod/output checks and pending report. The admission metric remains in its original position.
- Existing Job ownership is checked before recovering `Submitted`. Saved terminal reasons still enter `failed` before further Job observation. `failed` still saves the terminal reason before its cancellation work. The running-timeout and changed-input branches retain their distinct cancellation ordering.
- Successful renderer-Pod verification precedes immutable-context/output collection. Direct desired-pair revalidation follows collection and precedes publication. Publication precedes clearing/saving Active, which precedes reporting. Capacity, obsolete/lost output, ownership conflicts, retry/backoff and requeue results retain their existing behavior.
- Startup and reconciliation still accept Phase 4 device keys and current pair keys. Maintenance still requires pair keys and retains its generic malformed-JSON error. Migration rename, conflict detection and directory sync are unchanged.
- Traversal remains in `ReadDir` order. A later corrupt record cannot move ahead of earlier cancellation/removal, and an earlier corrupt record prevents later effects. Maintenance still handles `.pending-` entries before the JSON extension check, removes only regular files at least one hour old, and protects young files, directories and links.
- Persistent paths, key/hash inputs, JSON fields/encoding, context bytes/digests, attempt IDs/sequences and atomic writes are unchanged. Active Job counting, unrelated resolution/reporting/watch code, public protocols, CRDs, status ownership, artifact policy and the deferred R4 work are unchanged.

Source comparison confirmed that all six extracted stage bodies match the baseline after local-scope, pointer and helper-call substitutions. AST comparison also confirmed 12 unchanged reconciler functions and 12 unchanged Store functions, including `Reconcile`, `desired`, `cancel`, `failed`, reporting/watch/lifecycle wiring, startup migration, preparation, collection, record validation and serialization.

## Regression evidence

The existing [R0 coverage map](011-REFACTORING-R0.md) remains applicable. Existing tests retain the preparation/create/save/publication recovery boundaries, terminal-reason recovery, immutable-context safety, Job loss, obsolete completion, capacity, bounded retries, migration, removal, retention and 1,024-pair admission assertions.

Before adding tests, existing assertions were mapped to Change B: `TestPhase4StateMigrationPreservesAttempt` covers successful legacy migration and attempt/context preservation; `TestCorruptSchedulerFailsClosedAtStartup` covers invalid-record rejection; pair-removal and run-retention tests cover normal cleanup. They did not explicitly establish all caller-specific key policies, migration conflicts or the placement of corruption relative to cleanup effects.

Added three focused tests in [`records_test.go`](../internal/jobrender/records_test.go):

- `TestSchedulerRecordKeyPolicies`: startup/reconciliation/maintenance acceptance of pair, legacy and incorrect filename keys.
- `TestSchedulerMigrationRejectsConflictingRecords`: legacy/current duplicate records fail closed at startup.
- `TestSchedulerCorruptionStopsMaintenanceInEntryOrder`: unreadable, malformed and invalid records before/after a removed device's active pair preserve cancellation/removal ordering and error behavior. Old pending files, including `.json` names, are removed before the error; young files, old directories and old links remain protected. The existing clock injection establishes the one-hour boundary without sleeps.

These tests exercise caller-visible persistence and Kubernetes effects. No layout tests, fault-injection framework or production test seam was added.

## Verification

Go 1.26.8, cached Go dependencies (`GOPROXY=off`) and bundled envtest binaries were used. Cluster checks use a dedicated kind cluster and a separate kubeconfig under `/tmp`.

| Check | Result |
| --- | --- |
| Baseline Job race suite | Passed |
| Change A: existing Job race suite, uncached | Passed |
| Change B: new regression cases before consolidation | Passed |
| Change B: complete Job race suite, uncached | Passed |
| Final `GOPROXY=off make verify` | Passed |
| API integration with Kubernetes 1.36.0 | Passed |
| API integration with Kubernetes 1.36.2 | Passed |
| Baseline source/AST comparisons | Passed |
| `make docker-build IMAGE=arprt:refactor-r3-20261009` | Passed |
| HTTP smoke on disposable kind / Kubernetes 1.36.4 | Passed |
| Job/PVC smoke on the same disposable cluster | Passed |
| Playlist smoke on the same disposable cluster | Passed |
| Operations smoke on the same disposable cluster | Passed |

Cluster: kind v0.33.0, Kubernetes 1.36.4, node image `kindest/node@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed`, name `arprt-r3-20261009`, kubeconfig `/tmp/arprt-r3-20261009.kubeconfig`, controller image `arprt:refactor-r3-20261009`.

The complete `make cluster-smoke IMAGE=arprt:refactor-r3-20261009` matrix exited successfully. All test namespaces and created CRDs were removed, then the dedicated kind cluster was deleted. The default kubeconfig context remains `microk8s`.

No deployment or physical-device session is included. The pre-existing untracked source plan is preserved unchanged.
