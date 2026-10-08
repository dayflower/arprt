# Phase 5 — Playlists and automatic discovery

## Assignment contract (2026-10-08)

- References are same-namespace. `TrmnlPlaylist.spec.screens` is an ordered,
  nonempty list of at most 64 distinct screen references. Missing/deleting
  screens are skipped; an absent playlist or an empty eligible set returns the
  protocol's initial-pending response with an actionable AssignmentReady reason.
- With neither reference, discovery sorts nondeleting screens by name and uses
  the first 64. This bounds render fan-out per device. Explicit screenRef keeps
  its existing per-request HTTP semantics.
- Screen cycling advances only when a compatible image response is durably recorded.
  A stale success for that same screen also advances. A selected screen without
  any compatible image holds its turn (202); it never borrows another screen's
  artifact. HTTP GET, POST and Job may be mixed.
- BYOS has no verified unique display-request identifier. For devices with screen cycling,
  successful responses within `device-refresh-seconds` are replayed with the
  same image and filename, even after restart. The window starts when rendering
  has finished and the turn is persisted, uses microsecond timestamps, and is
  not extended by replay. Telemetry is still observed; POST receives
  current telemetry on the next new turn. This also treats early manual polls
  as retries. A lost response after the durable write replays that turn.
- Screen-cycle status records device-owned assignment fingerprint, selected screen
  UID, target, screen revision, image digest/filename and serving time. Device
  UID is included in the fingerprint so replacement cannot inherit a turn. Membership/order,
  playlist replacement, device assignment, screen replacement, or target changes
  invalidate replay. Membership/order changes reset to the first eligible screen;
  screen definition changes invalidate replay without resetting the sequence.
- All eligible Job screen/device pairs are prepared, regardless of the current
  turn. Each pair owns its retry/due/attempt state. `--job-concurrency` bounds
  active Jobs globally (default 4); removed pairs are canceled and obsolete
  completions cannot publish. Ordinary telemetry/polling never directly starts
  a Job. The existing single active controller/PVC topology remains required.

The sequence state is named `screenCycle` to distinguish it from future device
orientation settings, for which the word `rotation` may be used.

## Implementation

- Added namespaced `TrmnlPlaylist`, structural CRD, scheme/deepcopy generation,
  namespace RBAC, and reference readiness reconciliation. The ordered list is
  atomic under server-side apply, with CEL validation for unique names. No new
  timing/weighting fields were added to desired state.
- Assignment resolution supplies one ordered eligible set to delivery, POST
  publication gates, Job scheduling and artifact retention. Rechecks reject
  playlist/discovery edits during an HTTP render.
- Successful screen-cycle responses are persisted in Device `status.screenCycle` before
  being sent. Single-screen rendering keeps its earlier freshness contract.
- Scheduler records now use namespace/device/screen keys. Existing Phase 4
  records migrate by durable rename, preserving attempts and context bytes.
  Active recovery/collection precedes queued work; older due pairs are preferred.
  A full concurrency budget leaves pairs queued without allocating run directories.
- Retention protects all eligible per-screen outputs, including screens not
  currently selected. Removed pairs cancel active attempts and stop scheduling;
  existing completed Jobs retain their normal diagnostic lifetime.
- Job screen failures for devices with screen cycling appear on the screen, retained Job and warning
  Event. Device `Ready` describes the selected image. A single device
  `RenderReady` condition is not used as an aggregate for all Job pairs.
- Added playlist/discovery samples, README instructions and
  `hack/playlist-smoke.py`; existing HTTP/Job scripts include the new CRD, pair
  storage layout and explicit reassignment semantics.

## Validation

- Generated deepcopy/CRD hashes are stable under regeneration. Dependency
  consistency (`go mod tidy -diff`), formatting, `go vet ./...`, full race tests,
  controller build, Docker build and Kustomize assembly passed.
- API integration passed on Kubernetes `v1.36.0` and `v1.36.2`: playlist length,
  unique names and local-reference validation; server-side apply reordering;
  partial/missing references and readiness watches; new discovery/playlist Job
  scheduling without another display request; `status.screenCycle` persistence;
  existing manager restart/recovery.
- Focused tests passed for deterministic ordering, 64-screen discovery bounds,
  mixed GET/POST/Job screen cycling, latest POST telemetry, response/filename replay
  across a new server/repository instance, serving-time retry windows, UID
  isolation, deletion/order changes, edits during POST, pending-turn retries,
  per-screen stale fallback, Job concurrency, all eligible output retention,
  removal cancellation and Phase 4 active-record migration.
- Disposable Linux kind cluster validation used Kubernetes `v1.36.4` with a
  same-node local-path filesystem RWO PVC. `hack/playlist-smoke.py` passed for
  mixed sources and discovery, POST telemetry, Job preparation before selection,
  retry/restart persistence, order edits, deletion, empty sets, image fetching
  and namespace RBAC.
- HTTP regression (`hack/cluster-smoke.py`) passed: setup-generated/stable keys,
  all MAC sources, authentication, telemetry, both prefix modes, GET reuse, PVC
  restart, outage fallback and initial-image pending.
- Job regression (`hack/job-smoke.py`) passed: target gating, default/custom
  paths, token absence, same-node mounts, immutable snapshots, interval refresh,
  independent polling, active restart, offline completed-output recovery,
  failures/timeouts, stale output and obsolete reassignment cancellation.

The disposable-cluster checks left the existing device deployment and default
kubeconfig unchanged. The subsequent physical-device verification is recorded
below; operational hardening and release acceptance remain Phase 6. The initial
single-active-controller and Kubernetes minor 1.36 support constraints remain
in force.

Test namespaces, temporary CRDs and the dedicated kind cluster were removed.

## Physical-device verification (2026-10-08)

With explicit approval, the Phase 5 controller and CRDs were deployed to the
existing Kubernetes `v1.36.2` cluster. Namespace-scoped Role permissions were
extended for Jobs, Pods/PVC reads, Events and playlists. The existing PVC,
authentication and HTTPS Gateway path prefix were reused.

- The actual TRMNL OG / firmware 1.8.17 (`800x480`, `gray4`) received the mixed
  playlist in its declared order: device-status (POST) at 19:50:48 JST, grayscale
  bands (GET) at 19:51:58, and JOB SCREEN (Job) at 19:53:08. The user confirmed
  all three screens were displayed in that order.
- The Job completed and published its pair-owned image at 19:47:19, before the
  first playlist display request. Renderer and controller used the same node
  and filesystem RWO PVC.
- A controller restart after the first turn preserved the exact screen-cycle
  status, including image digest, filename and serving timestamp. The next
  actual-device request advanced to the second screen rather than restarting
  the sequence.
- Removing both assignment references enabled discovery: actual-device requests
  received device-status at 19:54:19, JOB SCREEN at 19:55:29, and grayscale bands
  at 19:56:38, matching screen-name order. This sequence was verified from
  persisted delivery state and images; the user's visual confirmation above
  applies to the explicit playlist.
- Each delivered image matched its screen/target/pair metadata and saved digest,
  and authenticated image GETs through the existing public Gateway returned the
  exact PNG bytes. No synthetic display requests advanced the real device.
- Temporary configured polling was 60 seconds; observed display requests were
  about 70 seconds apart including device wake/render time. The original
  device-status single-screen assignment and 300-second polling were restored.
  The test playlist, Job screen, renderer ConfigMap and completed Job were
  removed. A subsequent real-device POST delivery at 19:57:49 JST passed
  current-telemetry/context-digest, pair ownership and authenticated image-fetch
  checks. The updated controller, CRDs and namespace Role remain installed.

This verifies screen cycling on the physical device. Portrait/landscape
orientation configuration remains outside this phase.
