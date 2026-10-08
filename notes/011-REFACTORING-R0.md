# R0 — Behavior preservation baseline

Completed: 2026-10-09. Source baseline: `669695242b7827a259da9d7fb7f51947cecb669b` (Phase 6 operational hardening).

This is the bounded R0 review from [the refactoring plan](010-REFACTORING-PLAN.md#r0--lock-down-the-behavior-being-moved). Scope is test mapping, demonstrated failure-window gaps, and verification. R1-R4 remain separate decisions. Production code, dependencies, CRDs, protocols, and persistent formats are unchanged.

## Extraction and barrier coverage

Test names below are searchable entry points, not proposed test/file boundaries. Keep the broad behavioral tests when extracting code. `internal/integration` requires the `integration` build tag; several checks are helpers/subtests of `TestAPIAndController` rather than independent top-level tests.

| Proposed extraction / barrier | Existing regression evidence | R0 decision |
| --- | --- | --- |
| R1 observation adapter: only allowed device fields enter Render Context v1 | [`devicestate/context_test.go`](../internal/devicestate/context_test.go): `TestContextAllowlist`; [`byos/post_test.go`](../internal/byos/post_test.go): `TestPOSTTelemetryOwnershipFreshnessAndRestart` | Retain unchanged; identity, Secrets, labels, and annotations must remain absent. |
| R1 assignment-aware retention | [`jobrender/screen_cycle_test.go`](../internal/jobrender/screen_cycle_test.go): `TestAllEligiblePairsBoundRemovalAndRetention`; [`jobrender/reconciler_test.go`](../internal/jobrender/reconciler_test.go): `TestMaintenanceDropsPointersForPreviousTransport`; [`artifact/gc_test.go`](../internal/artifact/gc_test.go): `TestCollectionKeepsLatestAndIssuedImages`; [`artifact/operations_test.go`](../internal/artifact/operations_test.go): `TestCollectionDoesNotEraseBindingNewerThanDeviceSnapshot` | Covers collection policy and the shared service via Job maintenance. HTTP-only `Start` wiring and the Job maintenance mutex remain explicit R1 review checks; these tests do not directly exercise the HTTP-only runnable lifecycle. |
| R2 setup, routing, credential provisioning, image serving | [`byos/server_test.go`](../internal/byos/server_test.go): `TestAuthenticationRoutingAndPending`, `TestSetupDisplayPersistenceOutage`, `TestCredentialRotation`; [`byos/setup_test.go`](../internal/byos/setup_test.go): `TestGeneratedKeysForAllIdentityAndDeliveryModes`, `TestRejectedSetupDoesNotProvisionCredentials`, `TestSetupRecoversUnacknowledgedKeyAndStatusWrites`, `TestSetupRechecksSecretsBeforeReturningKey` | Existing coverage; preserve canonical routes, anonymous setup image, second Secret read, and saved-key reuse. |
| R2 observation and request context | [`byos/server_test.go`](../internal/byos/server_test.go): `TestObservationPolicy`; [`byos/post_test.go`](../internal/byos/post_test.go): `TestPOSTContextSurvivesStatusConflict`, `TestPOSTTelemetryOwnershipFreshnessAndRestart` | Existing conflict/telemetry coverage; add failure-policy coverage below. |
| R2 GET publication: current screen identity/definition, shared work, independent cancellation | [`byos/server_test.go`](../internal/byos/server_test.go): `TestConcurrentReuseAndObsoletePublication`, `TestSharedGETIssuanceAndMalformedRefresh`; [`byos/operations_test.go`](../internal/byos/operations_test.go): `TestShutdownCancelsDetachedGETWithoutPublishing` | Existing coverage. Keep GET independent of one device's assignment. |
| R2 POST publication: current screen/device/target/membership and pair ordering | [`byos/post_test.go`](../internal/byos/post_test.go): `TestPOSTChangedInputsCannotPublish`, `TestPOSTLateAttemptAndTimeout`, `TestPOSTTelemetryOwnershipFreshnessAndRestart` | Existing coverage. Do not replace POST snapshots with GET sharing. |
| R2 selection, pending turn, retry window, compatible stale output | [`byos/screen_cycle_test.go`](../internal/byos/screen_cycle_test.go): `TestMixedScreenCycleRestartRetriesAndPending`, `TestScreenCycleFailureHoldsTurnAndUsesOnlyItsOwnStaleImage`; [`assignment/resolver_test.go`](../internal/assignment/resolver_test.go): `TestOrderingScopeEditsAndRetryWindow` | Existing coverage uses persisted serving time; no new clock seam or sleeps needed. |
| R2 final issuance authorization after render/replay lookup | [`byos/identity_test.go`](../internal/byos/identity_test.go): `TestSecretChangesDuringRenderPreventIssuance`; [`byos/screen_cycle_test.go`](../internal/byos/screen_cycle_test.go): `TestScreenCycleEditDuringPOSTRejectsResult`; POST changed-input and obsolete GET tests above | Existing tests cover changed identity/credentials and assignments. Preserve fresh reads and the complete assignment fingerprint, not just membership. |
| R2 issuance and cycle save precede successful response | No existing display test injected these persistence failures | Add `TestDeliveryPersistenceFailuresHoldTurnAndRecover`. |
| R2 acknowledged replay survives a new server/repository without POST or cycle advance | [`byos/screen_cycle_test.go`](../internal/byos/screen_cycle_test.go): `TestMixedScreenCycleRestartRetriesAndPending`, playlist/discovery and GET/POST/Job turns | Already asserts the exact response and renderer-call count after restart, followed by ordered turns. Do not duplicate it. |
| R3 Job publication: successful renderer Pod, immutable context/output validation, current desired pair | [`jobrender/reconciler_test.go`](../internal/jobrender/reconciler_test.go): `TestFailuresBackoffAndObsoleteCompletion`, `TestReplacementAssignmentAndTargetRejectOldJobs`, `TestMountsAndCollectionSafety`, `TestLostJobNeverReusesPartialOutput`; [`jobrender/operations_test.go`](../internal/jobrender/operations_test.go): `TestDamagedContextCannotPublishClaimedSnapshot` | Existing coverage of stale inputs, missing/partial output, context damage, and filesystem safety. |
| R3 concurrency, capacity, retry bounds, pair removal, migration | [`jobrender/operations_test.go`](../internal/jobrender/operations_test.go): `TestCapacityQueuesWithoutAllocatingAttemptAndResumes`, `TestPersistentFailureRateIsBoundedAndCanRecover`, `TestWorstCaseFanoutHasBoundedActiveJobsAndRunDirectories`, `TestCorruptSchedulerFailsClosedAtStartup`; [`jobrender/screen_cycle_test.go`](../internal/jobrender/screen_cycle_test.go): `TestAllEligiblePairsBoundRemovalAndRetention`, `TestPhase4StateMigrationPreservesAttempt` | Existing coverage; retain fresh admission counts, lock scope, and migration acceptance. |
| Optional durable filesystem extraction | [`artifact/pvc_test.go`](../internal/artifact/pvc_test.go): `TestPublicationRecoveryOwnershipAndOrdering`; [`artifact/operations_test.go`](../internal/artifact/operations_test.go): `TestExclusiveWriterAndInterruptedWriteCleanup`, corruption tests; Job context/migration tests above | Defer implementation. Existing caller tests remain relevant if R4a is adopted. |

## Status ownership coverage

Use this together with the plan's status ownership table. A test exercising a writer does not prove every possible conflict or condition combination.

| Owner / contract | Tests to retain |
| --- | --- |
| Device reconciler: identity/uniqueness, credentials, assignment readiness, initial readiness, `DeviceSeen` | [`integration/api_test.go`](../internal/integration/api_test.go): `TestAPIAndController` checks assignment convergence, duplicate identity/recovery, generation changes, and disabled Job status; [`byos/identity_test.go`](../internal/byos/identity_test.go): `TestIdentitySecretChangesAndPendingDevices` exercises Device reconciliation. Initial target/readiness and `DeviceSeen` initialization remain source-review checks; there is no dedicated assertion for every condition. |
| Identity registration: durable binding, `hardwareID` mirror, `IdentityReady` | [`byos/identity_test.go`](../internal/byos/identity_test.go): `TestIdentityModesAndRestart`, `TestRegistrationStatusFailureAndInvalidCredentials`, `TestConcurrentAutoRegistrationAndMixedDuplicates`; [`artifact/identity_test.go`](../internal/artifact/identity_test.go): `TestIdentityPersistenceAndConcurrentClaims`. |
| BYOS observation: last seen, device/target/telemetry fields and authentication/target/telemetry conditions | `TestObservationPolicy`, `TestPOSTContextSurvivesStatusConflict`, [`byos/operations_test.go`](../internal/byos/operations_test.go): `TestAdmissionRejectsBeforeAPIAndStablePollCoalescesStatus`; new `TestObservationRequiredAndOutcomeReportingBestEffort`. |
| BYOS delivery: selected-image `Ready`, assignment result, synchronous HTTP `RenderReady`, screen HTTP outcome | `TestSetupDisplayPersistenceOutage`, `TestPOSTTelemetryOwnershipFreshnessAndRestart`, `TestScreenCycleFailureHoldsTurnAndUsesOnlyItsOwnStaleImage`, `TestPOSTContextSurvivesStatusConflict`; new `TestObservationRequiredAndOutcomeReportingBestEffort` preserves best-effort errors for both device and screen. |
| Job reporting: screen `Ready`, explicit single-screen device `RenderReady`; display preserves Job failure reasons | `TestFailuresBackoffAndObsoleteCompletion`; [`byos/job_test.go`](../internal/byos/job_test.go): `TestJobDisplayOnlyReadsCompletedPair`; [`integration/job_test.go`](../internal/integration/job_test.go): `TestJobWatchAndRestart` exercises real API reporting and watches. The absence of aggregate Job `RenderReady` for cycling devices remains an R3 review check, not a dedicated existing assertion. |
| Screen/playlist reconcilers: initial/disabled screen state and playlist reference readiness | `TestAPIAndController`, including [`integration/playlist_test.go`](../internal/integration/playlist_test.go): `verifyPlaylistAPI`. |

## Failure-window changes

All new fixtures are local to test files and use existing Repository/client interfaces and temporary on-disk stores. No production fault injection or clock abstraction was introduced.

- [`byos/durability_test.go`](../internal/byos/durability_test.go), `TestDeliveryPersistenceFailuresHoldTurnAndRecover`: after an acknowledged GET turn, select a POST turn and fail issuance before writing, return an issuance error after writing, or reject the cycle status patch. Each case requires HTTP 503 with exactly `{"status":503}`, the previous cycle unchanged, and the published POST success retained. Reopening the repository/server and failing the next POST refresh must recover that screen's exact stored digest, filename, and PNG bytes and persist its turn. This deliberately does not require an unacknowledged POST to avoid rendering again.
- The same file, `TestObservationRequiredAndOutcomeReportingBestEffort`: an observation status error stops rendering/publication and returns 503; errors from both HTTP screen outcome and device outcome reporting still permit an acknowledged, fetchable image. This protects the existing availability policy during R2 outcome extraction.
- [`jobrender/recovery_test.go`](../internal/jobrender/recovery_test.go), `TestCreatedJobBeforeSubmittedSaveRecoversSameAttempt`: persist the Job but return an API error before `Submitted` is saved. At creation, verify context/attempt durability; after new Store, repository, and Reconciler instances, require exactly one original Job, identical context bytes, unchanged attempt/sequence, and successful publication of that attempt.
- The same file, `TestTerminalReasonSurvivesInterruptedCancellation`: persist `JobLost` then interrupt cancellation. Remove the unconfirmed output and reconstruct the Store/repository/Reconciler. The saved terminal reason must prevent recreation of the old unsubmitted Job, retain the five-second backoff, and allocate a new attempt only when due.
- [`jobrender/reconciler_test.go`](../internal/jobrender/reconciler_test.go), `TestPreparedAttemptAndRunRetention`: strengthen the existing prepare-before-submit case by reconstructing all persistence owners and checking the complete active attempt, with only `Submitted` changing.
- The same file, `TestRestartSchedulingAndSnapshots`: reconstruct all persistence owners after publication but before active-state clearing; require identical publication metadata and cleared active state after recovery. Existing interval/snapshot assertions remain.

## Preservation checklist for follow-up reviews

These are requirements for R1 onward, not claims that tests simulate every crash or establish power-loss durability.

- [ ] Public contracts: unchanged CRD/defaults, Render Context v1, requests/PNG limits, URL/filename/response encoding; pending is HTTP 200 with JSON `status: 202`, success is `status: 0`.
- [ ] Identity/credentials: all MAC modes, UID binding, setup ordering/key reuse, namespace uniqueness, direct Secret reads, and final authorization after rendering/replay lookup.
- [ ] Artifact ownership: GET screen/target vs POST/Job screen/device/target; compatible older revisions may be stale fallbacks; replacement UIDs and targets cannot inherit them.
- [ ] Distinct transport semantics: GET sharing/waiter cancellation, POST request snapshots/pair ordering, Job scheduling independent of polls.
- [ ] Cycling: unchanged fingerprint/order/reset/pending rules; selected-screen stale output; serving-time retry window; exact acknowledged replay across restart.
- [ ] Durability: unchanged paths, JSON/key/hash/context/attempt/filename formats and migration; prepare/save before Job creation, publish before latest/bookkeeping, issuance/cycle save before response.
- [ ] Concurrency/shutdown: 64-request admission, fixed display stripes, shared eight-slot HTTP budget, Job limit, timeout scopes, detached GET cancellation, and Job maintenance under the existing mutex.
- [ ] Retention/failures: compatible successes, issued URL retention/slack, completed Job/run retention, saturated backoff, capacity admission, and fail-closed identity/issuance/scheduler corruption.
- [ ] Operations/status: fixed/redacted telemetry and logs/Events, scoped RBAC, single-writer POSIX RWO topology, owner-specific conditions, conflict retries/no-op suppression, mandatory observation/issuance/cycle saves vs best-effort delivery outcome reports.

## Verification

Go 1.26.8, cached dependencies (`GOPROXY=off`), and bundled envtest Kubernetes binaries were used. The initial working tree contained the untracked source plan; it was left unchanged.

| Check | Before changes | After changes |
| --- | --- | --- |
| `GOPROXY=off make verify` (generated manifests/deepcopy diff, tidy diff, gofmt, vet, race suite, controller build) | Passed | Passed |
| `GOPROXY=off KUBEBUILDER_ASSETS="$PWD/bin/k8s/k8s/1.36.0-linux-amd64" go test -tags=integration -count=1 -v ./internal/integration` | Passed | Passed |
| Same integration command with `1.36.2-linux-amd64` | Passed | Passed |
| Focused new/strengthened tests with `go test -race -count=1` | Not applicable | Passed |

No Docker build, cluster smoke, deployment, or physical-device session was performed for R0. Cluster/PVC mount and renderer-Pod execution remain the R2/R3 milestone gates in the plan. These unit tests model interrupted boundaries and API failures; they do not kill a process mid-fsync or emulate power loss.

R0 establishes the required evidence for the small R1 moves. The specific runtime-wiring and condition-assertion limitations above stay visible for subsequent review; they do not justify a new general test framework or an architecture change.
