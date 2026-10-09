# Refactoring assessment and implementation plan

Original assessment: 2026-10-08, source baseline `669695242b7827a259da9d7fb7f51947cecb669b` (Phase 6 operational hardening).

Scope revision: 2026-10-09, source baseline `b29366316d64642216cd084efc9bbc6271251781` (R2 completed). This revision plans a bounded R3; it does not implement R3 or report newly executed checks.

## Recommendation

**Targeted refactoring is worthwhile. A rewrite or a new architecture is not justified.** The project already has useful package boundaries, explicit ownership rules, durable recovery, and substantial behavioral tests. The main opportunity is to make its safety-critical workflows easier to understand and change before adding more capabilities.

R0-R2 are complete; see the [R0 preservation baseline](011-REFACTORING-R0.md), [R1 completion record](012-REFACTORING-R1.md), and [R2 completion record](013-REFACTORING-R2.md). The remaining adopted scope is limited to extracting named Job recovery stages and consolidating scheduler-record enumeration/validation in `Store`. Preserve the existing mutex scopes, persistent formats, and operation ordering.

The bounded R3 has medium priority: it should make future Job changes and recovery reviews easier, but no current production failure or performance bottleneck has been established. Broad Job decomposition, active-Job-count cleanup, durable filesystem extraction, and smoke-test infrastructure changes are outside this scope. If Job behavior remains stable and no further work is planned, stopping after R2 remains reasonable.

At the revised baseline, R1-R2 changed Job production code only to use the extracted device-state and maintenance packages. `reconcilePair` still combines state replacement, admission, preparation, and persistence; `advance` still combines submission recovery, terminal observation, collection, revalidation, publication, and bookkeeping. `Store.records` and Job maintenance both enumerate and decode scheduler records, invoke the existing validators, and check keys. These are the concrete remaining opportunities. Existing `desired`, `cancel`, `failed`, `Store.prepare`, and `Store.collect` already provide useful boundaries and should be retained.

## Evidence and current strengths

The evidence in this section records the original Phase 6 baseline, before R0-R2. At that baseline, the repository contained 27 handwritten Go source files totaling 4,781 lines and 26 Go test files totaling 4,859 lines, excluding generated deepcopy code. The BYOS layout and dependency findings below have since been addressed by R1-R2; they are retained as the rationale for that completed work. File length is a navigation aid, not an independent reason to refactor.

| Area | Concrete evidence | Assessment |
| --- | --- | --- |
| BYOS workflow | [`internal/byos/server.go`](../internal/byos/server.go), 642 lines: `ServeHTTP`, `display`, `mutate`, `outcome`, `screenOutcome`, and `Listener.Start` | Routing, authentication, telemetry, selection/replay, rendering, final authorization, issuance, status ownership, and shutdown coexist. `display` alone is about 190 lines. This makes ordering constraints expensive to review. High value. |
| Job lifecycle | [`internal/jobrender/reconciler.go`](../internal/jobrender/reconciler.go), 750 lines: `reconcilePair`, `advance`, `failed`, `report`, and `maintenance` | One reconciler coordinates desired pairs, persistent attempts, Kubernetes Jobs/Pods, collection, publication, retries, status, and garbage collection. Recovery ordering has extensive regression coverage, but is difficult to see as a whole. High value, higher implementation risk. |
| Dependency direction | BYOS imports `controller` for `Observation`; Job rendering imports it for `Observation` and `StorageMaintenance` | Runtime adapters depend on the controller package for reusable support functions. The intended device-state and maintenance boundaries are only partly expressed. Small, concrete cleanup. |
| Publication and issuance checks | `GET.image`, `POST.Image`, `Server.display`, `Service.Register`, and `Reconciler.desired`/`same` each perform related freshness checks | Repeated UID/generation/definition/target checks can drift, but their contracts intentionally differ. Name and document the distinct gates before sharing any code. Medium value. |
| Status handling | `DeviceReconciler.Reconcile`, BYOS `outcome`/`screenOutcome`, identity `Register`, and Job `report` write overlapping conditions | Ownership is distributed. BYOS uses a nullable assignment flag and variadic asynchronous flag, while some outcome writes intentionally ignore errors. Explicit local outcome types and documented ownership would help. Medium value. |
| Persistence plumbing | [`artifact/pvc.go`](../internal/artifact/pvc.go) exports `WriteAtomic` for Job storage; `syncDir` and `syncDirectory` duplicate directory syncing | Artifact-domain code supplies general filesystem primitives to scheduler storage. A narrow extraction could clarify this boundary. Atomic writes are already shared; this is not a duplicated storage engine. Secondary value. |
| Smoke tests | Four [`hack/`](../hack/) scripts total 854 lines. Three dynamically import helpers from `cluster-smoke.py`; all repeat CRD installation checks and cleanup tracking | There is existing reuse, but the shared utilities live inside one scenario script. A small support module could reduce fixture drift. Secondary value. |

Several parts should be retained:

- `assignment` already centralizes explicit assignments, playlists, discovery, selection, and eligibility. Do not create a second assignment implementation.
- `screen.InputRevision` and `EffectiveInterval` already normalize defaults and distinguish freshness from polling.
- `render` has a small context/executor contract and shared bounded HTTP/PNG validation. GET and POST transport handling already reuse code appropriately.
- `artifact.Unit`, metadata, immutable blobs, issuance, and durable hardware registration encode important ownership guarantees.
- Identity registration and credential provisioning are already separate services. Keep their admission and persistence ordering.
- Tests cover observable delivery, concurrency, conflicts, corruption, capacity, restart, and legacy scheduler migration. CI also defines API and disposable-cluster checks.
- The 159-line controller entry point is a manageable composition root. Splitting it into a dependency-injection framework would add little value.

The original [component-boundary plan](000-PLAN.md#component-boundaries) supports this direction. The completed [Phase 6 record](009-PHASE6.md) is a regression baseline, not a reason to redesign verified behavior.

## Validation recorded for the original assessment

The following checks were run against the original Phase 6 source baseline using Go 1.26.8 and cached dependencies. R0-R2 verification is recorded in their completion documents; none of these results is a new run for the 2026-10-09 scope revision.

| Check | Result |
| --- | --- |
| `GOPROXY=off go test -race -cover ./...` | Passed. |
| `GOPROXY=off go vet ./...` | Passed. |
| `GOPROXY=off go build -o /tmp/arprt-refactoring-assessment-controller ./cmd/controller` | Passed; output was outside the repository. |
| `GOPROXY=off make manifests`, followed by a diff check of generated deepcopy code and CRDs | Passed; no generated differences. |
| `GOPROXY=off go mod tidy -diff` | Passed; no dependency differences. |
| `gofmt -l api cmd internal` | No files reported. |
| Integration suite with `KUBEBUILDER_ASSETS` pointing to bundled Kubernetes 1.36.2 binaries | Passed using a disposable local API server. |
| Integration suite with `KUBEBUILDER_ASSETS` pointing to bundled Kubernetes 1.36.0 binaries | Passed using a disposable local API server. |

Default Go coverage reports only the package being instrumented. In particular, a zero result for `delivery` or `deviceidentity` does not mean those paths are untested: BYOS tests exercise them. Integration files also require the `integration` build tag. Do not base refactoring priorities on those percentages alone.

For the original assessment, Docker builds, the four cluster-smoke scenarios, and physical-device acceptance were not rerun. Their earlier success was recorded in Phase 6. Later R2 image/cluster checks are recorded separately in the R2 completion document. No configured cluster or physical device was used by the original assessment checks.

## Compatibility constraints

All core phases below are behavior-preserving. Treat these as review requirements:

1. **Public contracts:** Preserve CRD schemas/defaults, API group/version, CLI defaults, Render Context v1, renderer requests, PNG limits, URL construction, firmware filenames, and response bodies. Pending remains HTTP 200 with JSON `status: 202`; successful display remains JSON `status: 0`.
2. **Identity and credentials:** Preserve all three MAC modes, setup key generation/reuse, namespace uniqueness, UID-bound registration, and fresh Secret reads. Identity/authentication changes during rendering must still prevent unauthorized issuance.
3. **Artifact ownership:** GET artifacts remain screen/target-owned; POST and Job artifacts remain screen/device/target-owned. Definition revisions control freshness, while a compatible older revision may remain a stale fallback. Replacement UIDs and incompatible targets cannot inherit output.
4. **Distinct rendering semantics:** GET retains shared in-flight work and independent waiter cancellation. POST retains per-request snapshots and pair ordering across targets. Job rendering retains asynchronous scheduling independent of display polls. Do not merge these into one generic render lifecycle.
5. **Screen cycling:** Preserve the assignment fingerprint, ordering/reset rules, pending-turn behavior, stale output belonging to the selected screen, and the retry window measured from persisted serving time. Replay must preserve the exact image and filename across restart.
6. **Durability:** Preserve directory layout, JSON field names, hash/key inputs, context bytes/digests, attempt IDs/sequences, filenames, and the Phase 4 record migration. Context and attempt records precede Job submission; validated blobs and immutable records precede the latest pointer; issuance and any required cycle write precede the display response.
7. **Concurrency and shutdown:** Preserve the 64-request admission bound, fixed display stripes, shared eight-slot HTTP budget, configured Job limit, timeout scopes, and manager cancellation of detached GET work. Retain the existing Job mutex and its complete reconciliation/maintenance scopes throughout R3; do not move work outside the lock.
8. **Retention and failure policy:** Preserve compatible successes, at least 24-hour issued-URL retention including coalescing slack, completed-Job/run retention, saturated backoff, capacity admission, and corruption handling. Regenerable pointers may be discarded; identity, issuance, and scheduler damage must fail closed.
9. **Operations:** Preserve condition meanings, fixed metric labels, log/Event redaction, scoped RBAC, and the supported single-writer POSIX RWO topology. Refactoring does not expand the support matrix.

## Proposed boundaries

Keep the current packages. The R1 additions are already in place; the adopted R3 stays inside `jobrender`. The optional boundaries below require separate future decisions:

| Boundary | Intended responsibility |
| --- | --- |
| `byos` | Firmware parsing, routes, endpoint handlers, and an explicit display orchestration sequence. Keep workflow helpers private unless another package actually needs them. |
| `devicestate` (established in R1) | The allowlisted Kubernetes-status-to-render-observation adapter. Additional device-specific snapshot/update helpers require real shared use. |
| `maintenance` (established in R1) | The assignment-aware artifact retention service and its runnable lifecycle. Job-specific cleanup stays under the Job reconciler's lock. |
| `controller` | Declarative resource readiness, watches, and reconciliation. |
| `delivery` | GET sharing/freshness and POST snapshot/ordering policies, kept separate. |
| `jobrender` | Retain `Reconciler` as coordinator, with named private recovery stages. `Store` owns scheduler-record enumeration/decoding/validation; scheduling, Kubernetes effects, reporting, and retention policy remain with their current owners. |
| `artifact` | Domain ownership, publication, issuance, retention, capacity, and hardware-binding records. |
| `durablefs` (optional) | Atomic byte writes and directory syncing shared by artifact and scheduler storage. No ownership, recovery, or corruption policy. |
| `hack/smoke_support.py` (optional) | Existing command, wait, forwarding, and isolated-fixture utilities. Scenario assertions stay in their scripts. |

These are dependency directions, not a requirement for an interface around every struct. Keep `render` independent of Kubernetes objects. Do not introduce a general workflow engine, storage abstraction hierarchy, or universal status writer.

### Keep publication gates distinct

| Gate | Current contract to preserve |
| --- | --- |
| GET publication | Re-read the screen directly; check UID, generation, normalized revision, and deletion. It does not require one particular device to remain assigned. |
| POST publication | Check the current screen and device snapshots, target, assignment membership, and latest pair attempt ordering. |
| Job publication | Re-resolve the desired pair after collection and compare device UID, rendering unit, screen generation, and revision. Preserve the current `same` contract. |
| BYOS issuance | Recheck device/screen state, target, complete assignment fingerprint, hardware identity, and current credentials after obtaining output. This also covers replay and stale delivery. |
| Identity registration | Under the shared registration lock, re-read the device and external identity inputs before durable binding and status mirroring. |

A membership check cannot replace the issuance fingerprint check, and a GET guard must not acquire POST's device dependence. First extract named local functions with the same predicates. Share only predicates proven equivalent; do not add a cached read or remove a later check because an earlier one looks similar. Fresh reads are still observations, not a transaction spanning the API server and PVC.

### Make status ownership visible

| Writer | Existing responsibility |
| --- | --- |
| Device reconciler | Identity/uniqueness, credentials, assignment readiness, initial target/readiness state, and `DeviceSeen`. |
| Identity registration | Durable hardware binding, `status.hardwareID` mirror, and `IdentityReady`. |
| BYOS observation | Last seen, device/capability/telemetry fields, `AuthenticationReady`, `TargetKnown`, and `TelemetryValid`. |
| BYOS delivery | Selected-image `Ready`, assignment outcome, and synchronous HTTP `RenderReady`; replay and Job delivery preserve separately owned render outcomes. |
| Job reporting | Screen `Ready` and device `RenderReady` for explicit single-screen assignments. Cycling devices do not get one aggregate Job `RenderReady`. |
| Screen/playlist reconcilers | Screen initialization/disabled status and playlist reference readiness respectively. |

Use this table when extracting status functions. Preserve optimistic conflict retries, generation checks, unrelated conditions, transition times, and no-op patch suppression. Observation, issuance, and cycle-persistence failures are mandatory delivery barriers; `outcome` and `screenOutcome` currently perform best-effort reporting. Changing that distinction or adding new diagnostics should be an explicit follow-up change, not an incidental refactor.

## Expected value, change risk, and investment decisions

The measures below have different returns. R0-R2 rows describe completed investments; R3 rows describe the revised adopted scope, and deferred rows remain future options. Value means easier future changes, reviews, and diagnosis; no runtime speedup, operating-cost reduction, or reduction in observed incidents has been established. Their return depends on how often the affected code will change. Risk describes regressions the change could introduce, not defects known to exist today. The ratings are qualitative judgments based on the inspected code, not measured probabilities.

Distinguish the amount of behavior being changed from the impact of a mistake. Moving a function unchanged provides few opportunities for regression, but moving a maintenance call outside its lock can still have serious consequences. Similarly, a small filesystem extraction can affect the survival of data across a crash. A small diff is not automatically a low-impact change.

| Measure | Value created | Principal change risk | Investment judgment |
| --- | --- | --- | --- |
| R0: map existing tests and fill uncovered failure windows | Establishes evidence for safe changes, particularly interruption between persistence and external effects. Makes review requirements concrete. | Tests tied to helper structure constrain future changes; unnecessary sleeps make CI unreliable; large fault-injection scaffolding increases maintenance. Production clock seams, if introduced, also need review. | High enabling value, low risk when focused on observable behavior. Do the mapping first; add only demonstrated gaps. |
| R1: move the observation adapter | Removes BYOS/Job dependence on the controller package for a small, reusable allowlist. A future context-field change has a clear home. | Accidentally widening the allowlist could expose identity or credentials; an unchanged move mainly risks missed callers or build errors. An extra package adds navigation overhead. | Small standalone value and low change risk if copied unchanged with its test. Worth doing alongside R2/R3; not a reason for a broad package redesign. |
| R1: move assignment-aware maintenance | Gives HTTP-only and Job mode a neutral, explicit shared service. Makes retention changes easier to locate without coupling adapters to reconciliation. | Missed wiring can stop collection and allow storage to fill; changing Job-mode locking can race preparation/publication with cleanup. Incorrect retention inputs could remove protected output. | Medium structural value. Keep the implementation and both call sites unchanged in behavior; wiring deserves more scrutiny than the file move. |
| R2: split endpoint/listener files | Helps locate setup, routing, image serving, and lifecycle code and reduces unrelated edit overlap. | Too many tiny files scatter the call path. Combining moves with edits makes meaningful differences harder to review. | Small value and low risk as a mechanical step. It does not by itself simplify delivery logic; stop splitting when navigation stops improving. |
| R2: extract display stages and a selected-delivery snapshot | Exposes observation, selection, rendering, revalidation, persistence, and response order. Reduces the reasoning needed for a new capability or assignment rule. | A copied stale snapshot, lost current telemetry, changed cancellation scope, or reordered writes/checks can issue an inappropriate image, advance the cycle incorrectly, or acknowledge an unavailable image. | Highest near-term value for continuing delivery development; medium-to-high semantic risk. Keep orchestration readable and preserve barriers with behavioral tests. |
| R2: replace outcome booleans and document status ownership | Makes unresolved/resolved/failed assignments and HTTP/Job/replay ownership explicit. Helps prevent misleading readiness and accidental overwriting of Job failures. | Collapsing nil into false, mishandling a zero-value enum, or turning best-effort reporting into a required step can change pending responses and availability. Incorrect ownership can hide rendering failures. | Medium value and medium risk. Prefer private named fields/types over a universal status writer. |
| R2: name final validation and share only equivalent predicates | Makes the last authorization barrier easy to find and reduces accidental check omissions in later edits. Limited predicate reuse can prevent drift. | Removing a fresh read can authorize using old Secrets or assignments. Replacing a complete fingerprint with membership weakens issuance validation; giving GET a device-specific guard breaks intended reuse. | High value from naming and preserving the gates; high risk from aggressive deduplication. Extraction is recommended; broad unification is not. |
| R3: extract named recovery stages | Makes prepared/submitted/terminal recovery and publish-before-clear ordering reviewable while retaining the existing coordinator and helpers. | Moving a save, API read, cancellation, or report across a boundary can duplicate Jobs, lose recovery state, or accept obsolete output. A smaller diff still has serious failure impact. | Adopted, medium priority. Extract only meaningful recovery boundaries, backed by the existing interruption tests. Broad lifecycle redesign and mandatory file splitting add little immediate value. |
| R3: centralize record enumeration/validation in Store | Removes repeated traversal/decoding/key-check orchestration; `readStateFile` and `validateState` are already shared. Makes each caller's key policy explicit. | A permissive migration scan can weaken maintenance checks; stricter acceptance can reject existing records. Eager validation can reorder maintenance effects and failure boundaries. | Adopted, bounded structural value. Preserve caller-specific acceptance, errors, traversal/effect ordering, and startup migration. No general storage abstraction is needed. |
| Deferred: extract active-Job counting | Gives duplicated admission checks one readable implementation. | Reusing a count across several submissions changes a fresh observation into stale admission data and can exceed the configured limit. | Small value; excluded from R3. Keep the current counts, namespace scopes, metrics, and call positions. |
| R4a: extract durable filesystem primitives | Removes a domain dependency and gives atomic-write mechanics one neutral owner. Directory-sync duplication disappears. | Changed permissions, close/error handling, rename/fsync order, or cleanup semantics can lose acknowledged data after a crash. New abstractions can hide durability requirements. | Small immediate value with potentially severe failure impact. Defer unless the shared persistence code itself needs ongoing work. |
| R4b: extract smoke-test support | A fixture or CRD-check change can be made once, and new scenarios no longer import another scenario script. | Shared setup bugs can affect all checks or make them pass without exercising the intended behavior. Cleanup mistakes can affect pre-existing resources; use disposable clusters. | Medium value when tests keep growing; production runtime is unaffected, but verification integrity matters. Keep scenario assertions explicit. |

Two examples show the difference between adding safety and making existing safety easier to maintain. BYOS already rechecks credentials after rendering. Naming that stage helps a later change preserve the check when a renderer finishes after a key rotation; the refactor does not add a new authorization guarantee. Job publication already supports recovery with the same attempt metadata. Exposing the publish-then-clear boundary helps reviewers preserve that behavior when a crash occurs between those writes; it does not create a new scheduler capability.

The adopted decision sequence after R2 is:

1. Use the completed R0-R2 work and its regression records as the baseline; do not repeat those implementation phases.
2. Plan R3 as two independently reviewable changes: named recovery-stage extraction, then Store enumeration/validation consolidation. Keep each change behavior-preserving and separately verifiable.
3. Stop once those two responsibilities are clear. File moves are optional and limited to helpers that are actually extracted; splitting desired-pair resolution, reporting, watches, and all maintenance code is not an acceptance criterion.
4. Keep active-Job counting and R4a/R4b deferred. Reassess them only when a concrete follow-up needs them; they are not prerequisites for completing the adopted R3.

Binary rollback remains easier when formats stay unchanged, but it does not undo effects already produced by faulty code: duplicate Jobs may already have run, and deleted image bytes do not return with the old binary. Preserve the locks, write/check ordering, and retention rules, and verify on disposable data before rollout. Review and recovery effort therefore belong in the cost estimate, especially for R3 and R4a.

## Phased implementation

The `R` labels distinguish this work from the completed product Phases 0-6. R0-R2 below retain their completed scope for reference. R3 is planned, not implemented; R4a/R4b are deferred options. Each implementation change must leave the project buildable and deployable. Estimates describe relative change size, not a delivery commitment.

| Phase | Scope and relative size | Dependency / stopping point |
| --- | --- | --- |
| R0 | Completed: preservation criteria and specific test gaps; small | Regression baseline for the following changes. |
| R1 | Completed: shared adapters and maintenance in neutral packages; small | Retain both runtime paths and Job locking. |
| R2 | Completed: explicit BYOS orchestration and outcome handling; medium | Current implementation baseline; valid stopping point. |
| R3 | Planned: named recovery stages and Store enumeration/validation; medium | Uses the completed R0-R2 baseline; two focused changes with unchanged locks, formats, and operation order. |
| R4a / R4b | Deferred: optional filesystem plumbing / smoke support | Outside the adopted R3 scope; separate future decisions. |

### R0 — Lock down the behavior being moved

Completed; see [the R0 record](011-REFACTORING-R0.md). The scope below is retained for reference.

Work:

1. Record the baseline commit and run the existing checks. Use the compatibility and ownership tables above as the PR checklist.
2. Map proposed extractions to existing tests before adding any. Key coverage includes `TestPOSTChangedInputsCannotPublish`, `TestSecretChangesDuringRenderPreventIssuance`, `TestMixedScreenCycleRestartRetriesAndPending`, `TestPOSTContextSurvivesStatusConflict`, `TestRestartSchedulingAndSnapshots`, `TestPreparedAttemptAndRunRetention`, and `TestPhase4StateMigrationPreservesAttempt`.
3. Check missing failure windows around issuance, cycle persistence, and Job bookkeeping. Add tests only where the existing suite does not already establish the contract. Use blocked executors, injected API conflicts/errors, temporary repositories, and reconstructed server/store instances to assert external results.
4. For time-bound behavior being touched, prefer explicit timestamps or a small `Now` function consistent with the Job reconciler. Avoid a project-wide clock framework and timing-sensitive sleeps.

Acceptance:

- The team can identify which tests protect each moved barrier and each status owner.
- Failed issuance or cycle persistence cannot produce an acknowledged successful turn; compatible stored output remains recoverable.
- Replaying an acknowledged turn after a new server instance does not rerender POST or advance the cycle.
- No test merely asserts the new file/function layout. Existing broad tests remain in place.

Suggested change: one focused test commit/PR if actual gaps are found; otherwise proceed using the existing tests.

### R1 — Correct dependency direction

Completed; see [the R1 record](012-REFACTORING-R1.md). The scope below is retained for reference.

Work:

1. Move `controller.Observation` and its allowlist test to `internal/devicestate`; update BYOS, Job rendering, and affected test callers.
2. Move `StorageMaintenance` unchanged to `internal/maintenance`. Update `cmd/controller/main.go` and the Job maintenance caller. Preserve the HTTP-only runnable and the Job-mode call inside the existing mutex.
3. Leave policy, metric updates, scheduling, and capacity behavior unchanged. Do not make maintenance an independent concurrent runnable in Job mode.

Acceptance:

- `byos` and `jobrender` no longer import `internal/controller` for these utilities.
- Both Job-enabled and HTTP-only wiring still invoke assignment-aware retention.
- The context allowlist, HTTP-only collection, transport-change cleanup, eligible-pair retention, and new-binding/older-snapshot tests pass.
- Public and persistent outputs have no differences.

Suggested changes: two small mechanical commits, one for each moved responsibility. This phase should be easy to review independently of later workflow changes.

### R2 — Make display delivery an explicit sequence

Completed; see [the R2 record](013-REFACTORING-R2.md). The scope below is retained for reference.

Work:

1. First move routing/response helpers, listener lifecycle, and endpoint handlers into cohesive files within `byos`, keeping signatures and ordering intact. Possible files are `routing.go`, `listener.go`, `setup.go`, `images.go`, `display.go`, and `status.go`; use only the splits that remain cohesive.
2. Then extract the meaningful stages of `display`: observe and persist capabilities/telemetry; resolve/select/replay; obtain an image through the existing GET/POST/Job paths; revalidate current state and authorization; persist issuance and any new cycle turn; report outcome and encode the response.
3. Use a private value for the selected delivery snapshot containing the screen, target, revision, assignment fingerprint, and replay information. Make stale/fresh/pending and reporting ownership explicit where they replace the current scattered booleans. Keep HTTP error mapping at the endpoint boundary.
4. Replace `assigned *bool` and `asynchronous ...bool` with named local outcome fields or enums. Retain the difference between an unexamined assignment, a failed assignment, and a resolved assignment, and between synchronous reporting, Job reporting, and replay.
5. Extract the final issuance validation as a named operation. Keep it after rendering/replay lookup and before `Repository.Issue`; do not treat the earlier POST publication gate as authorization to issue a URL.
6. Preserve registration/provisioning order in setup and the second Secret read before returning the saved key. Preserve route canonicalization and anonymous access to only the bundled setup image.

Acceptance:

- A reviewer can read the display orchestration and locate all externally visible effects and their required order without tracing a long mixed-purpose handler.
- Existing GET reuse, POST freshness/ordering, Job nonblocking polling, stale fallback, auth rotation, assignment edits, target/UID replacement, and cycle replay tests pass.
- Admission, stable-poll no-op writes, changed telemetry, and detached-GET shutdown tests pass under the race detector.
- An injected status conflict preserves current request telemetry and unrelated conditions. Best-effort reporting errors retain their existing delivery semantics.
- No new interface or dependency is needed merely to call a stage; no new public resource/status type is introduced.

Suggested changes: one mechanical file-move commit, followed by one or two workflow commits. Review outcome-type changes separately from endpoint moves.

### R3 — Expose recoverable Job transitions

Status: planned. The scope is limited to named recovery stages and Store-owned scheduler-record enumeration/validation. Existing scheduling, reporting, watches, and retention behavior stay with their current owners.

#### Fixed constraints

- Retain `Reconciler.mu`, acquired and released at the same boundaries around the complete `Reconcile` and `maintenance` operations. No helper may move preparation, publication, cancellation, or cleanup outside those scopes; Job maintenance must not become an independent concurrent runnable.
- Preserve paths, key/hash inputs, JSON fields/encoding, context bytes/digests, attempt IDs/sequences, migration rename/sync behavior, and the existing atomic writer. Transition names describe observed state only; do not persist a phase enum or introduce a storage migration.
- Preserve API reads, capacity checks, Job counts, writes, reports, error exits, and requeue timing at their existing positions. Recover active attempts before new due work, retain pair ordering, and keep each admission/submission count fresh with its existing namespace and metric behavior.
- Preserve branch-specific cancellation order. `failed` saves the terminal reason before its cancellation work; the running-timeout and changed-input paths already have their own cancellation sequence. Do not normalize these branches by moving saves or cancellation across one another.
- Preserve successful renderer-Pod verification, immutable-context/output validation, and direct desired-pair revalidation before publication. Preserve `Publish` before clearing/saving Active, and the existing status reporting after bookkeeping. Condition ownership, retry/backoff, capacity, and Job/run retention remain unchanged.

#### Change A — Extract named recovery stages

1. Keep `Reconcile` and `reconcilePair` as coordinators. Extract meaningful steps from the existing preparation and `advance` branches so the caller can locate: durable preparation; submission/recovered submission; Job observation; successful collection/revalidation; publication/bookkeeping. Reuse `desired`, `cancel`, `failed`, `Store.prepare`, and `Store.collect` rather than duplicating their policy.
2. Make inputs, state mutations, early returns, and requeue/error results explicit. Add a small private result type only when the existing return types cannot express the extracted branch clearly. Keep reads and effects in their original order; naming a stage is not a reason to combine previously distinct branches or observations.
3. Use the transition table below to review every moved boundary. No generic state-machine engine, new package/interface, or persisted state is required. Keep Job-loss, unconfirmed output, ownership-conflict, timeout, and obsolete-completion decisions visible.
4. Move an extracted helper into a cohesive file only if it improves navigation. A preliminary mechanical split of the entire reconciler, extraction of active-Job counting, and relocation of unrelated resolution/reporting/watch code are outside this change.

| Observed state | Required next behavior |
| --- | --- |
| No active attempt; work not due | Requeue for the existing due time. |
| Due but concurrency/capacity unavailable | Wait without allocating a new active attempt/run directory. |
| Due work admitted | Prepare durable context, save the same new attempt/sequence, then enter submission; no Kubernetes creation precedes those writes. |
| Prepared attempt; Job absent; not yet submitted | Apply the existing Pod/output, concurrency, and deadline checks; create the same attempt's Job, save `Submitted`, then report pending. Do not add admission checks to a new position. |
| Job exists but `Submitted` was not saved | Recognize the existing owned Job and persist submission; do not create another attempt. |
| Submitted Job disappeared | Record `JobLost`; partial output cannot prove renderer success. |
| Running Job | Wait, or follow the existing deadline-cancellation path; retain its order relative to `failed`. |
| Successful Job with successful renderer Pod | Validate immutable context/output, re-resolve the desired pair, then publish. |
| Publication succeeded but active-state clearing did not persist | Recover idempotently using the same attempt/sequence and publication metadata. |
| Entered `failed`, or recovered a saved terminal reason | Save the terminal reason before any cancellation performed by `failed`; finish required cancellation, then clear Active and persist the existing bounded retry delay before reporting. |
| Removed/replaced pair | Cancel active work and wait for Jobs/Pods to disappear before removing/replacing its scheduler record; retain sequence and compatible-output rules. |

#### Change B — Consolidate enumeration and validation in Store

1. Move repeated scheduler-directory traversal, record reading/decoding, `validateState` invocation, and filename/key checking behind private `Store` operations. Build on the existing `readStateFile`, `validateState`, and `records`; do not replace them with a general storage abstraction. Keep API-driven desired-pair decisions, cancellation, metrics, and retention policy in `Reconciler`.
2. Make the existing caller-specific key acceptance explicit. Startup migration accepts Phase 4 device keys and current pair keys and retains conflict detection. Reconciliation currently uses the same migration-compatible enumeration; preserve that acceptance. Maintenance requires current pair keys. Sharing decoding must neither let maintenance accept legacy keys nor silently tighten reconciliation; further restriction of legacy acceptance is a separate behavior change.
3. Preserve traversal and per-entry effect/error ordering. In particular, an eager all-records read must not move validation failures ahead of, or behind, existing maintenance cancellation/removal effects. Choose the private Store API around this constraint rather than forcing all callers to consume one preloaded snapshot. Preserve caller-visible corruption/error behavior; unreadable, malformed, or invalid records must not become an empty queue or be skipped.
4. Preserve abandoned `.pending-` cleanup, including its regular-file check, age threshold, and ordering relative to record processing. File inspection may move to Store; cleanup policy and locking must remain explicit. Keep active attempts and retained Jobs/runs protected as before.

#### Regression evidence and acceptance

Use the [R0 coverage map](011-REFACTORING-R0.md) and the actual assertions, not helper layout, to review both changes:

| Boundary / policy | Existing test entry points |
| --- | --- |
| Durable preparation before submission | `TestPreparedAttemptAndRunRetention` |
| Job created before `Submitted` save | `TestCreatedJobBeforeSubmittedSaveRecoversSameAttempt` |
| Publication before Active clearing | `TestRestartSchedulingAndSnapshots` |
| Terminal reason before interrupted cancellation | `TestTerminalReasonSurvivesInterruptedCancellation` |
| Obsolete/lost output and immutable context | `TestFailuresBackoffAndObsoleteCompletion`, `TestReplacementAssignmentAndTargetRejectOldJobs`, `TestLostJobNeverReusesPartialOutput`, `TestDamagedContextCannotPublishClaimedSnapshot`, `TestMountsAndCollectionSafety` |
| Migration, corruption, removal, capacity, backoff, and active bound | `TestPhase4StateMigrationPreservesAttempt`, `TestCorruptSchedulerFailsClosedAtStartup`, `TestAllEligiblePairsBoundRemovalAndRetention`, `TestCapacityQueuesWithoutAllocatingAttemptAndResumes`, `TestPersistentFailureRateIsBoundedAndCanRecover`, `TestWorstCaseFanoutHasBoundedActiveJobsAndRunDirectories` |

Acceptance:

- A reviewer can locate durable preparation, recovered submission, terminal handling, revalidation/publication, and completion bookkeeping from the coordinators, with all required effect ordering visible. No full file split or universal lifecycle abstraction is needed.
- Reconstructing the store/reconciler at the relevant write boundaries preserves attempt identity and does not duplicate Jobs or change context bytes.
- Maintenance no longer duplicates scheduler-record decoding/validation/key checks. Each caller retains its key acceptance, corruption behavior, `.pending-` handling, and operation order.
- Retain the existing behavioral tests above. Before adding tests for Change B, map caller-specific key rejection and corruption/cleanup ordering to existing assertions; add focused cases only for uncovered behavior touched by consolidation. Do not add layout tests, timing sleeps, or a general fault-injection framework.
- Existing corruption, path/regular-file safety, capacity, backoff recovery, playlist/discovery removal, migration, and 1,024-pair bound tests pass. API integration still exercises Job watches and restart recovery; the milestone cluster matrix verifies actual renderer Pods and PVC mounts.
- No mutex scope, persistent representation, API/write/check/report sequence, retry schedule, status ownership, supported topology, or Job retention behavior changes.

Delivery: land Change A and Change B separately, each runnable against the existing regression suite. Review any small necessary file move independently from behavior-sensitive extraction. Stop after these acceptance criteria; active-Job counting and R4 work remain deferred.

### R4a — Optional: isolate durable filesystem primitives

Deferred; outside the adopted R3 scope. Reconsider only if the shared persistence primitives need concrete follow-up work. Completing R3 does not require this extraction.

Work:

- Move the existing atomic byte-write and directory-sync implementations into a small `internal/durablefs` package. Keep JSON serialization, metadata validation, corruption classification, and capacity policy with their existing owners.
- Update artifact and Job callers without changing `.pending-` names, permissions, parent-directory assumptions, write/fsync/close/rename/directory-fsync ordering, or returned errors.
- Remove `artifact.WriteJSONAtomic` only after confirming its current lack of callers; avoid preserving unnecessary wrappers in an internal API.
- Keep the different artifact-metadata, scheduler-record, context, and PNG read limits. Do not generalize renderer output collection into an ordinary JSON/file reader or weaken root-relative regular-file checks.

Acceptance: publication/restart, interrupted-write cleanup, filename reservation, context integrity, migration, and fail-closed corruption tests pass. New primitive tests are justified only for durability/error behavior not already exercised through callers. Do not redesign capacity accounting or remove concrete PVC use as part of this phase.

### R4b — Optional: make smoke-test reuse explicit

Deferred; outside the adopted R3 scope. Reconsider when another smoke scenario or fixture change makes the repeated setup worth consolidating. Completing R3 does not require this extraction.

Work:

- Move shared helpers out of the HTTP scenario into `hack/smoke_support.py`; replace dynamic scenario imports with ordinary support imports.
- Consolidate identical CRD comparison/install tracking and fixture cleanup. Keep the schema-mismatch refusal, cleanup of only resources created by the run, and existing namespace/image arguments.
- Keep scenario assertions and meaningful setup differences visible. Avoid a general-purpose cluster test framework.

Acceptance: all four scripts still run directly with their documented commands; Python compilation succeeds; the complete matrix passes in the explicitly selected disposable cluster; failure cleanup preserves pre-existing CRDs/resources. Keep this separate from production code changes so failures remain attributable.

## Verification and rollout gates

The gates below apply when implementing R3, not when editing this plan. No implementation, test execution, cluster operation, or deployment is part of the scope revision. R2 verification remains in its [completion record](013-REFACTORING-R2.md).

For Change A and Change B, run the focused existing Job tests under the race detector. Before accepting R3, run `make verify` and the API integration matrix for 1.36.0/1.36.2. Compare each moved branch against the R2 baseline for mutex scope, current key policy, read/write/cancellation/report ordering, and error/requeue behavior. A pure helper/file move does not require a new physical-device session.

At the R3 milestone, build the distribution image and run the four-script `make cluster-smoke IMAGE=...` matrix on an explicitly selected disposable cluster, as CI already does. This validates HTTP delivery, Job/PVC behavior, playlists, and operations together; envtest cannot verify actual renderer Pods or storage mounts. Passing R2 checks is historical baseline evidence, not a substitute for checks on the eventual R3 implementation.

Before a deployment, compare generated CRDs/deepcopy output, representative protocol responses, context fixtures, and existing disk records. Verify that a new binary can reopen a copied repository containing identity/issuance records, an acknowledged cycle turn, and prepared/submitted/completed Job attempts. Add a small sanitized fixture only if current restart/migration tests do not already establish the relevant compatibility.

Deploy each milestone as an ordinary single-controller Recreate rollout using the existing PVC and configuration. Check current image URLs, exact bytes/filenames, replay status, recovery of active Jobs, and retained stale output. Follow existing backup procedures.

Rollback should require restoring the previous binary/image, with the same CRDs and disk format. Prove reopening compatibility on disposable data before relying on that path. Do not erase scheduler, identity, or issuance records to make a rollback succeed. Any necessary persistent-format change requires a separate migration and rollback plan.

## Work to defer

- **Broad Job reorganization:** Splitting all desired-pair, scheduling, reporting, watch, and maintenance code into separate files, extracting active-Job counts, or building a generic lifecycle engine is outside R3. Retain useful existing helpers and limit file moves to the two adopted changes.
- **Persistence-policy tightening:** Keep each caller's existing scheduler-key acceptance and error behavior explicit during consolidation. Further restrictions on legacy-key acceptance, new diagnostics, and changes to corruption/cleanup policy need their own behavior-change decision and evidence.
- **Performance changes:** `PVC.Usage` walks the managed tree during admission; Job `desired` repeatedly resolves eligible screens; reconciliations scan scheduler records; watches fan out to namespace resources. These are plausible scaling costs, not measured bottlenecks. Profile filesystem time, API calls, and lock time under representative workloads before adding indices, accounting caches, or concurrency. The 1,024-pair bound test is not a throughput benchmark.
- **New storage backends or multi-replica operation:** They require different publication, locking, authorization, and recovery guarantees. Broad repository interfaces or removal of PVC type assertions are not prerequisites for this refactor.
- **Public API/default consolidation:** CRD admission validation and runtime defense serve different boundaries. Preserve both, even where values or rules repeat. New devices/palettes, renderer sources, playlist features, and CRDs remain feature work.
- **Broad cosmetic cleanup:** Do not rename `assignment.Explicit` solely because it now handles three modes, rewrite generated code, introduce a generic reconciler/status framework, or restructure all tests to mirror production packages. These provide less immediate value than exposing the two workflows.
- **Incidental bug fixes:** If extraction reveals a genuine behavior defect, isolate it with a reproducer and a separate change. Do not silently alter authorization, stale eligibility, diagnostic errors, persistence, or status ownership in a mechanical refactor.

## Completion criteria

R0-R2 are complete. The remaining adopted scope is complete when R3 Change A and Change B are implemented and accepted with the required checks: Job recovery stages and effect ordering are traceable from the coordinators, scheduler-record enumeration/validation belongs to Store without duplicated maintenance decoding, and existing caller-specific policies are preserved. The existing mutex scopes, persistent formats, operation order, protocols, condition ownership, concurrency limits, and supported deployment behavior must remain compatible.

No broad Job file split, active-Job-count extraction, or R4 work is required for completion. Deferring implementation and retaining the completed R2 baseline is also a valid stopping point; this plan revision does not claim R3 completion.

Success is clearer recovery boundaries and preserved regression evidence, not a target number of packages, smaller files alone, a higher coverage percentage, or an unmeasured speedup. Reassess deferred work only when a concrete need arises.
