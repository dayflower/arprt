# Current design

arprt maps declarative arprt resources to HTTP, scheduled or on-demand Job
rendering, validated persistent artifacts and authenticated BYOS delivery.
[PROTOCOL](PROTOCOL.md) owns wire/context/image details;
[DEVELOP](DEVELOP.md) maps packages and checks;
[README](../README.md) owns installation and support limits;
[OPERATIONS](OPERATIONS.md) owns configuration/recovery and
[RENDERERS](RENDERERS.md) owns renderer examples and usage.

## Public API and boundaries

The namespaced API is `arprt.dayflower.github.io/v1alpha1`:

- `ArprtScreen` defines exactly one HTTP or Job source, applicable freshness and
  optional display overlay intent.
- `ArprtDevice` selects a single screen, an ordered playlist, or discovery, with
  an auth Secret, optional direct/Secret-backed hardware identity and desired
  clockwise content rotation (`spec.rotation`, default 0; 0/90/180/270).
- `ArprtPlaylist` defines 1–64 ordered screen references; repeated names are allowed.

All references and discovery stay in the device namespace. Capabilities,
telemetry, registration observations and screen-cycle progress belong in status.
Artifact layout, execution state, cache keys, temporary Jobs/Pods and storage
backend selection remain internal; they do not require artifact/execution CRDs.
Source and assignment exclusivity, identity-source immutability and path/duration
constraints are enforced by structural schema/CEL. Unknown fields require strict
API writes to reject rather than prune; see DEVELOP.

Render Target is physical width/height, semantic palette and desired rotation;
device model is not part of rendering identity when capabilities are equivalent.
Rotation participates in exact-target cache and fallback compatibility for every
source. Changing rotation obsoletes active Jobs and invalidates cycle replay;
output from another rotation cannot supply fallback. Display status remains
observed hardware capabilities, and issued immutable URLs remain authorized
under the existing retention rules. Render Context adds allowlisted device
information, with the same schema for POST and Job. Renderers do not need
Kubernetes clients, upload SDKs or controller credentials. Job arguments are
passed through: the renderer's CLI says where to write, while `imagePath` says
where the controller collects. The controller never interprets arbitrary CLIs.

## Rendering and assignment

| Source | Timing and sharing | Ownership/context |
| --- | --- | --- |
| GET | On demand after miss, expiry or definition change; default freshness 5m. Concurrent work coalesces; equal current definitions/targets share fresh output. | Namespace + screen UID + exact target; no device context |
| POST | Each new authenticated single-screen display request; no fresh reuse or context coalescing. Explicit `refreshInterval` is rejected. | Namespace + screen UID + device UID + exact target; current request overlaid on valid known state |
| Scheduled Job | Scheduled independently of display; initial readiness, definition/assignment/target changes and interval refresh (default 30m). | Same pair ownership as POST; immutable scheduling snapshot |
| OnDemand Job | Each authenticated single-screen display or new cycling turn; no `refreshInterval`. Concurrent single-screen requests share rendering. | Same pair ownership as POST; immutable current-request snapshot |

Device polling and artifact freshness are independent. GET/POST share bounded
HTTP execution. Scheduled Job display reads completed output without starting
or waiting for work. `source.job.executionMode` defaults internally to `Scheduled`;
`OnDemand` opts into display-triggered execution with an internal default `timeout`
of `5s` (integer `1s`–`10s`). Admission rejects timeout on Scheduled Jobs and
refreshInterval on OnDemand Jobs. Explicit Scheduled and omitted executionMode
retain the original revision; explicit/omitted OnDemand `5s` also normalize alike.
Default normalization and input revisions live in
[screen/input.go](../internal/screen/input.go); revisions include context and
collector, processing, encoder and palette semantic versions and exclude
metadata/status/credentials. Screen processing defaults are contain/Atkinson;
equivalent explicit/omitted settings share a revision. Replay also requires
`screenCycle.compatibilityFingerprint`, covering resolved output constraints,
rotation and processing revision. A missing fingerprint denotes a legacy turn.

With `screenRef`, one existing screen is selected. With `playlistRef`, missing
or deleting entries are skipped while remaining order is preserved. With neither,
discovery selects the first 64 nondeleting screens sorted by name. A missing
playlist or empty eligible set returns initial pending. The default playlist
`failurePolicy: Hold` and discovery do not skip an entry merely because its
renderer currently has no image.

Playlist/discovery uses `status.screenCycle` to persist the successful turn,
original zero-based entry index (`entryIndex`, required, 0–63), screen UID,
target, revision, image digest/filename and serving time. Under Hold/discovery,
a compatible stale image counts as success; without one the selected turn holds.
Issuance and the new turn are durable before the response is acknowledged.

BYOS has no verified unique display-request ID. During `device-refresh-seconds`
after a recorded turn, replay the same image and filename, including across
restart and early manual polls. The window begins after rendering and durable
turn recording, uses microsecond timestamps and is not extended by replay.
Telemetry is still observed. **POST and OnDemand Job in playlist/discovery render on a new turn,
not on replay**; a single `screenRef` retains per-request rendering.

The assignment fingerprint includes device UID/generation, assignment identity
and every original reference position, name and resolution (UID, missing or
deleting), including repeated references. Each name is resolved once per snapshot;
eligible entries retain their original positions. Discovery uses positions in the
bounded, name-sorted eligible array; a single screen has internal position zero
and does not create cycle status. Selection validates both the recorded position
and UID, and candidate traversal starts at that exact occurrence. A fingerprint
change resets selection to the first eligible entry. Definition edits invalidate
replay without resetting membership/order; changed targets cannot reuse incompatible output.
All eligible Scheduled Job pairs are scheduled independently of the current turn, bounded
by the global concurrency limit; work can remain queued before its first image.
Repeated entries share one Screen/Device pair, including its active attempt and
due time. Removing some occurrences preserves that pair; removing its last
eligible occurrence cancels work and stops future attempts. Entry positions do
not participate in artifact cache keys or UID ownership.

### Skip playlists

Opt-in `spec.failurePolicy: Skip` visits each eligible candidate at most once,
starting at the normal next position and wrapping at the end. It stops at the
first fresh success, records only that position, and starts the next new turn
at its successor. No success or an exhausted budget returns pending without
changing the last successful turn. Repeated occurrences of the same Screen
are attempted separately, each at most once, within the same total budget.
A one-entry playlist follows the same rules.
Failed candidates never supply stale output. Skip is not inferred from source
mode, and adding OnDemand entries never implicitly selects Skip.

| Source | Usable output in a new Skip turn |
| --- | --- |
| HTTP GET | Current-definition, exact-target cache within its refresh interval, or a successful refresh |
| HTTP POST | A successful render in this shared display operation |
| Scheduled Job | Current-definition, exact-target completed output within its refresh interval; no start or wait |
| OnDemand Job | Successful execution, validation and base publication within this operation's candidate cutoff |

`renderTimeout` is Skip-only, defaults internally to `10s` and accepts integer
`1s`–`10s`. No unconditional admission default is inserted for Hold. The absolute
cutoff starts at request admission, before authentication, assignment and gate
waiting; all candidates share it. An OnDemand candidate retains its default 5s
limit, capped by the remaining overall budget and configured Job safety limit.
A 5s first timeout leaves less than 5s for another candidate; an explicit 10s
candidate can consume the whole default budget. HTTP POST and newly started GET
flights also use the remaining cutoff. GET joins retain the original flight's
independent lifetime: an expired waiter neither cancels peers nor issues late
output. Finalization uses only the remaining 13s application lifetime.

Skip requests with matching device UID/spec/generation, resolved target,
assignment, starting entry position, previous successful turn and complete
candidate revisions share exploration, overlays,
issuance and turn persistence. Individual cancellations leave other waiters
running; the last departure cancels unfinished work. Each response obeys its
own shorter parent deadline without shortening another waiter's operation.
Shared work retains the original admission-based bounds; joins extend neither.
The first authenticated observation snapshot is retained for rendering; each
waiter still records its observations and checks current authorization before
responding. Rendering waits outside the device gate; shared finalization uses
the gate and existing durable corner/turn transaction. A recorded turn replays
its exact issued image and filename after restart without another candidate.
Finalization also checks that the previous successful turn is still current;
an older exploration cannot overwrite a newer success. Completed operations
retained by responding waiters cannot be joined by a later turn, even when it
wraps to the same entry. OnDemand rendering shares only within the same entry
and previous successful turn; artifact ownership and cache keys remain unchanged.
Uncommitted execution is stopped by the existing Job recovery path.

Policy and effective timeout participate in assignment/replay compatibility;
omitted/explicit Hold and omitted/explicit Skip `10s` normalize alike. Candidate
definitions additionally invalidate replay and exploration without resetting
membership/order selection. Recheck the whole snapshot between candidates and
before issuance. UID/target/assignment changes abort for reevaluation; credential
changes retain authentication failures. Renderer/PNG failures and unavailable
Job output may skip; disabling Job execution makes Job candidates unavailable.
Repository, API, issuance and turn-persistence failures retain their failure
response. Skipping never hides a storage failure.

## Publication, fallback and authorization

Immutable blobs are addressed by full image digest, with persisted short firmware
filenames. Latest-success metadata records the rendering unit, screen generation,
input revision, attempt ID/sequence, target, timestamps, image facts and, for
device-aware rendering, context digest. Publish validated image/metadata before
atomically replacing the success pointer on the same filesystem, with file and
directory fsync. Failed work preserves the previous compatible success.

| Operation | Required eligibility |
| --- | --- |
| Fresh GET reuse | Same screen UID, definition revision, generation and exact target, within freshness interval |
| New output publication | Current relevant UIDs, definition, target and attempt ordering; POST/Job also require current pair eligibility |
| Stale fallback | Same screen UID, exact current target, current assignment and device UID for POST/Job; a prior definition may qualify |
| Fetching an issued immutable URL | Current identity/token and issuance to the original current device UID, within retained data; reassignment does not itself revoke an issued URL |

Fresh reuse, stale selection and fetching an already-issued URL have different
rules. A different selected screen, replacement UID, incompatible target or
deleted resource cannot supply fallback. The digest is an integrity identifier,
not an access grant; each receiving UID must be issued access even for shared GET.
Recreating a resource name never transfers identity or issued-image authorization.

GET publication is independent of a particular device's assignment because its
output is shared by screen/target. POST publication checks pair membership.
BYOS separately rechecks the complete assignment fingerprint, including order,
plus current device/screen state, target, identity and token before issuance.
Membership checks alone cannot authorize a previously selected playlist turn.
Use direct API reads for these gates; informer propagation is not an authorization
barrier. Secret changes do not increment device generation and require fresh reads.

POST attempts are ordered per screen/device pair across targets before execution.
A newer attempt supersedes older in-flight responses. Successful sequences resume
from persisted metadata after restart; synchronous failed/incomplete work retries
on a later poll without a restartable execution journal. GET joining callers can
cancel independently; shared work follows manager shutdown. Generation/revision
changes during a joined flight cannot be treated as fresh new-definition output.

## Base images, composites and overlay corners

Renderer success remains the base artifact. Derived-image persistence shares
byte-addressed blobs and firmware filename reservations but never writes base
latest, sequence, attempt, Context digest or `RenderedAt`. Internal provenance
and cache references include the original unit/exact target and full success
identity, original UTC render time, normalized visible timestamp/icon levels,
actual corner, scale, the two active edge margins, rotation, charging-mark selection and bitmap/encoder
version. Hidden observations, raw measurements within one icon level, credentials and MACs are excluded.
Equivalent bytes share filenames while separate units/timestamps keep distinct
provenance. Caches do not grant issuance authorization.

[screen normalization](../internal/screen/input.go) applies omitted defaults and
fixed item order, preserves empty selections, and ignores disabled settings.
Screens without overlays keep their old revisions. Enabled overlay edits are
part of the definition revision; telemetry, actual corner and controller time
zone are not base input. [overlay](../internal/overlay/overlay.go) has no HTTP or
Kubernetes dependencies and draws a vendored CC0 Home Video font and original
icons on validated PNGs. A clockwise arrow prefixes the render timestamp to
identify image generation time; it is part of the timestamp item and does not
change the UTC provenance or display time-zone rules. The content row has fixed two-pixel white padding on all sides, scaled with
the bitmap. The padded row uses the Screen `top`/`bottom` and `left`/`right`
margins adjoining the selected logical corner. Opposite margins are omitted from the
drawing identity and fit check. The white background reaches both adjoining
logical corner edges. Charging uses
a valid current charging observation only; USB presence does not select the
mark. False or unavailable charging keeps ordinary battery-level selection.
Only the visible mark enters cache identity; hidden level measurements do not
change a charging composite.

BYOS selects fresh/stale base output, validates it, composes or reuses a derived
candidate, then performs the existing direct final UID/definition/assignment/
exact-target/identity/token checks. Compositing/cache persistence failures select
the validated base. Issue or required state-write failures return 503. Base
render errors and best-effort rendering reports stay independent of composites.
Replay bypasses drawing and keeps the committed bytes, filename and ServedAt.

One authoritative PVC corner record belongs to each device UID, shared across
cycling Screens and preserved across assignment/definition changes. New drawn
cycling deliveries move clockwise from top-right; fixed, disabled, empty, failed
and replayed overlays do not advance. The hashed-UID BYOS gate spans recovery,
selection, drawing, issuance and commitment. Single-screen delivery persists a
pending record, issues the image, then atomically advances the corner; the next
HTTP request is a new delivery.

For playlist/discovery, persist a pending record with both the previous complete
turn and the exact proposed `screenCycle` (fixed microsecond ServedAt), issue the
image, optimistically persist that proposal, then atomically finalize the next
corner. Before any subsequent selection, directly read status: a complete match
with the proposal finalizes the corner and any selected renderer candidate;
a match with the previous turn cancels the uncommitted proposal without advancement. Other inconsistencies,
corruption and unknown corner versions fail closed. Equal digest alone is not
proof of commitment. Both complete turns include their original entry positions,
so equal UIDs, digests, filenames and timestamps at different positions remain
distinct during recovery. PVC turns require an explicit index in 0–63; missing,
null or out-of-range indices are corrupt, with no old-cycle migration. Bytes
issued before a later failure retain authorization.

Derived/cache references expire after 24 hours plus one minute since their own
last use; digest issuance independently protects bytes/filenames/access for at
least 24 hours after Issue. Temporary pins protect candidate cache hits through
request completion, and durable pending proposals protect interrupted deliveries.
Maintenance in both execution modes covers these directories in capacity and
retention accounting. Validate authoritative roots before destructive collection;
deleted-UID corner records have a 24-hour grace period so old API snapshots
cannot remove new records. Caches are regenerable; corners and issuance are not.

Existing base/issuance formats remain readable. The authoritative device record
uses version 2 in the existing `corners` directory; version 1 corner records are
rejected without automatic migration. UIDs without a device record start at
top-right and existing replay can still return its base image. Downgrading after numeric-percentage or new internal-record writes is
unverified; older maintenance does not understand the new retention roots.

## Delivery observations and dashboard API

`internal/artifact` stores one version-1 observation per device UID under
`deliveries/<hashed-uid>.json`. It contains the device generation, selected
screen name/UID/generation, assignment fingerprint, selected input revision,
resolved target, actual issued image digest, optional original render time,
issuance time, delivery result (`ready`, `stale`, `unknown`), observed rendering
result (`ready`, `failed`, `pending`, `unknown`) and, for cycling,
the complete committed turn including its entry position. This is internal PVC
metadata, not public CRD state.
It contains no credentials or MAC. Recreating a resource name never transfers
its observation to the replacement UID.

BYOS records the observation synchronously under the existing device gate,
after final authorization, successful Issue, required turn/corner commitment
and best-effort status reporting, before writing the display response. The
common finalization path covers GET, POST, Scheduled/OnDemand Job, Hold, Skip,
discovery and replay. Pending and failed transactions leave the previous
observation intact. Skip records only its successful candidate; shared Skip
waiters do not create additional observations. Replay updates the observed
issuance time while preserving original provenance only when the previous
record matches the complete committed turn, screen, target and revision.
Missing legacy observations leave generation time and rendering result unknown.
An equal digest or a newer latest-success pointer cannot supply that provenance.
New render successes with equal bytes retain their own original render times.
Overlay composition and stale fallback use the selected base success's time.

Observation writes use capacity admission, file/directory fsync and atomic
rename under the repository lock. A delayed writer with an equal/older issuance
timestamp is rejected rather than guessing order; a backward clock can therefore
leave the observation behind. Observation failure never changes an otherwise
successful delivery response, issuance authorization, turn or corner. The fixed
storage error category `delivery_record` counts failed writes without logging raw
errors or paths. Stalled filesystem operations retain the existing synchronous
I/O limitations; this is not a hard response-latency guarantee.

An observation is **the last recorded completed delivery**, not proof of the
latest issued image or of physical display success. A write failure or crash
after required commitment can leave an older record; a crash after recording
but before response delivery can leave a record the device never received.
There is no atomic transaction across API status, PVC state and the network.
After restart, the available evidence cannot rule out an unrecorded newer
single-screen delivery. Accordingly the API always returns
`freshness: last-recorded` and `latestGuaranteed: false`, even for an apparently
matching record. It does not silently upgrade uncertain history to current
device rendering health. Successful later delivery replaces a corrupt observation;
unknown versions and malformed records are otherwise read as unavailable.

Observation files count toward managed storage, but are not blob retention or
issuance roots. Existing image/issuance retention remains unchanged. Live UIDs
retain their one observation even after its image is collected; deleted-UID
and invalid observation files have a 24-hour modification-time grace period
before collection. This protects records written after the maintenance API
snapshot. Abandoned atomic writes use existing 24-hour cleanup. Observation
corruption does not block authoritative retention validation or destructive GC.

`internal/dashboard` provides a read-only HTTP handler with an English UI
embedded in the Go binary. `cmd/controller` registers its independent,
manager-owned listener only when `--dashboard-bind-address` is not `0`.
Helm defaults `dashboard.enabled` to false; enabling it binds `:8083` and adds
only a container port, without changing the BYOS Service or RBAC. Startup
failures propagate to the manager. Shutdown cancels request contexts and drains
the HTTP server for up to 15 seconds. Runtime access is documented in
[OPERATIONS](OPERATIONS.md#dashboard).
Its constructor requires the watched namespace, an uncached Kubernetes reader
and an artifact `DeliveryReader`, with
no client writer, renderer or credential service. The handler is intended for a
separate operator listener; it does not implement device-token authentication.

| Route | Contract |
| --- | --- |
| `GET /` and `GET /assets/{dashboard.css,dashboard.js,mark.svg}` | Embedded UI and explicitly allowed assets; no frontend build or external asset service. Unknown paths return 404 rather than the HTML page. |
| `GET /api/devices?limit=20&continue=...` | A bounded page of nondeleting declared Devices in the configured namespace, including never-seen Devices; `limit` is 1–100, default 20. `continue` is the opaque Kubernetes continuation token, URL-encoded by the caller. Expired tokens return 410; other backend list failures return 503. Deleting Devices may make a page shorter or empty while a continuation remains. |
| `GET /api/devices/{name}/preview/{digest}.png` | Only the image in the current UID's observation, with a second current UID/deletion check before response. Unknown/replaced devices, unrelated digests, corrupt/missing observations and collected/corrupt images return 404. It never exposes arbitrary artifact reads. |

Responses use `no-store` and `nosniff`. Only GET is accepted; malformed/duplicate
page parameters and unknown query keys return 400. Concurrency is bounded to 16
requests with a 10-second context deadline. List assembly caches related
Screen/Playlist reads only within that page and checks preview presence without
loading image bytes. Preview reads share runtime image admission with BYOS and
rendering, held through response writing. Neither path repairs storage, updates
timestamps, grants issuance, pins images, advances turns, observes telemetry or
starts work. A preview can disappear after listing because existing GC continues.

The list DTO explicitly selects names/namespace, model/firmware, resolved
dimensions/palette/rotation, saved battery/network values, LastSeenAt, assignment
mode/ordered members and the last recorded image. It excludes MACs, Secret
references/contents, renderer configuration, internal records and storage paths.
Battery percentage is reported only when present; voltage never supplies an
inferred percentage. Omitted telemetry retains its own observation history, not
a guaranteed measurement at LastSeenAt. BYOS LastSeenAt persistence may coalesce
within 30 seconds; the dashboard does not invent more precise communication time.

Communication is `Never seen`, `Ready`, `Silent` after more than three polling
intervals (minimum two minutes), or `Unknown` for future timestamps. Device
delivery uses only current-generation Ready conditions and distinguishes
`Serving stale image`, `Image pending` and `Unknown`. Rendering is separately
scoped to `recorded-delivery`, requiring matching device/assignment/target and
screen UID/generation/revision, plus the complete current turn for cycling.
A current single-screen negative RenderReady report is shown independently as
`device-report`, including without any observation; JobPending and disabled Job
execution are `Image pending`. A failed HTTP refresh or OnDemand execution is
recorded as such. Stale Scheduled Job output alone leaves rendering `Unknown`: an overdue
Scheduled Job image may simply be awaiting its next run. Positive device conditions
alone do not establish per-screen provenance. Related Screen failures remain
separate screen-scoped problems; Screen Ready and Playlist Ready never become
per-device rendering aggregates. Multiple communication, authentication, target,
assignment, telemetry and rendering problems remain visible together, using
fixed explanations rather than forwarding arbitrary condition/storage messages.
Image age alone does not imply render failure. Missing observations have no image
(`No image available` in the UI); retained records without blobs have
`Preview unavailable`, retaining known generation time. Record read failures
are distinguished from missing/legacy records and never supply invented facts.

The UI uses device rows that stack on narrow viewports. Communication, delivery
and rendering remain separate, with the rendering report's scope and all related
problems visible. Assignment mode/target and the recorded image's screen are
separate fields; playlist/discovery members expand in their original order.
Generation, issuance and last-seen timestamps use the browser's local timezone
and include relative ages updated every ten seconds. Missing generation time
remains unknown; neither issuance time nor image age supplies a rendering result.
Battery percentage and voltage appear independently, without voltage conversion.

The browser requests 20 devices at a time and loads only previews near the
viewport. Image enlargement uses a keyboard-accessible modal with Escape/Close
and focus restoration. Visible pages refresh every 30 seconds independently of
the device poll interval; hidden tabs stop scheduling polls and refresh when
visible again. Successful refreshes retain unchanged previews, expanded members
and keyboard focus. API failures preserve the previous page and update timestamp
with a visible error and retry; expired continuation tokens require an explicit
return to the first page. Missing preview bytes have an unavailable state;
failed image requests also offer retry. Assets use a same-origin Content Security Policy; resource names and all
API text are inserted as text, never HTML. Browsing has the same read-only and
retention guarantees as the API.

## Identity and credential persistence

MAC source selection, direct value and Secret reference name/key are immutable.
No source means learning the first eligible MAC during successful setup. Invalid
or unavailable external identity sources never fall back to learning. Namespace
uniqueness covers direct, Secret-backed and learned MACs; unbound automatic
devices do not collide. Existing durable bindings reserve their MAC even if an
external source becomes unavailable. Changing hardware requires a new device UID.

The PVC binding is authoritative once present; `status.hardwareID` mirrors it and
cannot replace it. Shared identity-service locking serializes cross-device claims.
Persist the binding before the optimistic status patch and setup response.
Repeated registration of the same MAC is idempotent. A status-write failure can
be retried but cannot permit a different MAC. Existing explicit-MAC devices can
establish durable registration on authenticated display without re-pairing.

The operator declares each named auth Secret. Setup is enabled by default;
`credentials.Ensure` generates 32 cryptographically random bytes as unpadded
URL-safe base64 only for an absent `apiKey`. It patches that entry with a
resourceVersion precondition and reads the saved winner on conflicts/retries.
A present empty/invalid key is never silently overwritten. No Secret resource
is created. Ordinary display/log/image authentication is read-only. Declaring
Secrets in advance permits named get/patch rules; Kubernetes
[create permission cannot be restricted by resource name](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#referring-to-resources).
GitOps omits the generated key field. Operator procedures and error responses
are in [OPERATIONS](OPERATIONS.md#setup-and-credentials) and PROTOCOL.

Setup validates MAC eligibility before credential preparation, repeats checks
on a fresh device when registering, persists the binding and status, and finally
rechecks identity/key before returning success. Credential creation and MAC/status
writes are separate durable steps, not an atomic multi-resource transaction.
A failure may leave a saved key without completed registration; retries reuse it.
Credential-write failure never registers a MAC, and incomplete registration
never returns setup success or discloses the key.

## Job lifecycle and collection

Each eligible device/screen pair owns due time, retry state, monotonic sequence
and at most one active attempt. Preparation persists the context, its digest,
source snapshot and attempt record **before** the Kubernetes Job is created.
Attempt IDs determine Job identity. Restart can recreate prepared scheduled work,
adopt existing scheduled work or collect completed scheduled work without
duplicating an active pair.
Publication precedes clearing the active record so interrupted bookkeeping can
replay publication idempotently.

`jobrender.Reconciler.Execute` connects authenticated BYOS display requests for
OnDemand Screens to the same preparation, observation, collection and publication
transitions. Assignment, configuration changes and image URL GETs never trigger
OnDemand work. Pair and attempt records add an optional trigger and, for
request-triggered attempts, an absolute deadline. OnDemand pairs also capture
device generation and assignment fingerprint so changes stop old executions.
Omitted trigger fields
retain scheduled semantics and do not change old IDs, context digests, sequences,
due times or backoff. A policy change terminalizes and stops old work before a
new execution is allowed.

The display caller supplies the admission timestamp, absolute render cutoff,
expected UID/target/revision and current observation allowlist. Freeze context
bytes before slot waiting. The candidate deadline is the earliest of that
cutoff, admission plus 10 seconds, candidate start plus its 5-second default
(integer 1–10 seconds), the configured Job safety limit and the parent deadline.
Slot waiting, preparation, Kubernetes startup, image admission, collection,
processing and publication all consume this same budget. Kubernetes
`activeDeadlineSeconds` is an additional stop mechanism. Publication rechecks
cancellation after preparing/fsyncing the latest pointer, immediately before
atomic rename; synchronous filesystem operations still depend on filesystem
progress and cannot guarantee transport delivery under a stalled disk.

Concurrent identical candidates share the first immutable context, attempt and
deadline. Each waiter keeps its own cancellation/cutoff; a join cannot extend
the shared execution. No reconciliation/maintenance lock or image lease is held
across Kubernetes/slot polling or waiter suspension. Collection and publication
remain serialized through the reconciler, so watch events, maintenance and the
request worker cannot collect twice. Operation-lock admission respects the
caller's cutoff, and manager shutdown cancels and drains execution workers.
The surrounding display flow retains
responsibility for authentication, assignment compatibility, fallback, issuance
and its separate 13-second application deadline. Admission precedes authentication
and device-gate waiting. A single-screen flight survives individual waiter
disconnects, ends when all waiters leave, and retains the first snapshot through
finalization. Its identity includes device UID/spec/generation, screen
UID/generation/revision, resolved target and assignment. Every response observes
its own telemetry and revalidates current authorization before issuance.
Hold/discovery retain their serialized turn/replay and stale-image rules.
Skip shares the complete turn as described above.

Jobs without renderer state try a new Job for each independent single-screen
request even with an existing image. Candidate failure permits the same pair's compatible stale output;
no image returns HTTP 200 / JSON 202. Storage/API/issuance failures retain their
failure response. Rendering and successful base storage stop by admission plus
10s. Compatible fallback, overlays and issuance use the remaining request time
through 13s; cleanup never waits in that reserve. Equal regenerated bytes reuse
the immutable digest/filename, while an issued result survives response failure.

When the last waiter leaves or the deadline expires, persist a terminal reason
and stop the Job independently of the HTTP waiter. Do not wait for Pod deletion
in the finalization reserve. Stopping Jobs and remaining Pods consume the shared
global execution budget and prevent pair replacement; GC protects their run
directories even if their Job has disappeared. Request-triggered work schedules
no periodic retry or backoff. On restart, uncommitted work with no live waiters
becomes terminal and is collected, without creating a replacement. A matching
already-durable publication survives interrupted active-record cleanup; terminal
attempts cannot regain success from late notifications or output files. Storage
and API errors remain infrastructure errors instead of candidate failures.

Recover active work before starting queued pairs; older due work is preferred.
The global concurrency budget bounds active Jobs/run directories, not the number
of persisted pairs. The legacy one-record-per-device scheduler format migrates
by durable rename to namespace/device/screen records, preserving active attempts
and context bytes; conflicting or invalid records are rejected rather than guessed.

Missing submitted Jobs become `JobLost`; unconfirmed execution output cannot be
trusted merely because a file exists. Failed, timed-out or obsolete attempts use
new directories. Cancellation accounts for old Jobs/Pods before replacement work.
`backoffLimit: 0` leaves Scheduled retries to the controller. Bounded exponential backoff
slows after seven consecutive failures to at least the screen interval and five
minutes; success resets it. Definition changes can bypass obsolete backoff.

Renderer Pods mount only their output directory writable and their immutable
context file read-only. The entire output parent is hidden by the mount, so use
a dedicated directory. Default and distinct/nested parents are supported;
system paths, conflicting files and the reserved artifact root are rejected.
There is no extra writable `/tmp`. Pods run as non-root with a read-only root,
dropped capabilities, no privilege escalation and no ServiceAccount token.

Collection requires Job `Complete=True` and successful renderer termination.
Verify context integrity, acquire shared image capacity, open output beneath
its directory, reject escaping symlinks/special files, collect at most 32 MiB
and normalize/validate the complete PNG before publication. Missing/partial
output or exit failure never becomes success. Recheck desired pair state after
collection and before publication. The reconciliation/maintenance lock spans
preparation, publication and durable bookkeeping so cleanup cannot remove work
between stages. Kubernetes resource GC does not remove arbitrary PVC directories.

### Renderer state persistence

The [state store](../internal/artifact/state.go) is connected to opted-in Job
preparation, collection, scheduling and BYOS delivery commitment. Successful
rendering alone never advances the committed state.
The existing corner API uses the same authoritative device file as renderer
pairs and a single pending delivery. Pair ownership uses namespace, Device UID
and Screen UID. Definition revision and a separate compatibility fingerprint
bind state to resolved target/output, rotation and processing policy. The caller
must compute that fingerprint from the effective rendering constraints.

Normalized Screen input changes, including renderer settings, result path,
reset token, execution mode and refresh interval, reset state conservatively.
Metadata, credentials and telemetry are excluded. Epochs fence resets and
assignment departure; generations advance only when a candidate is consumed,
even if its state and PNG bytes are unchanged. Candidate identity is the attempt
ID, separate from image digest. A pair has at most one unconsumed candidate.

Both Scheduled and OnDemand attempts persist the committed pair snapshot,
including epoch, generation, definition and target, beside the context digest.
Only committed state enters `rendererState`; supplied display snapshots cannot
inject state. Collection reads the optional result beneath the existing output
root with nonblocking, regular-file and bounded-read checks. Missing files keep
state; unreadable files are not treated as omission. PNG processing and result
validation must both succeed before publication. API, filesystem I/O and capacity
failures remain infrastructure failures for both execution modes; malformed
renderer output uses the existing Scheduled backoff or OnDemand cleanup.

BYOS carries candidate reference, epoch and generation separately from the issued
image digest through optional compositing and finalization. Replay, PNG fetches,
preview and compatible consumed-image fallback never consume a candidate. Skip
accepts an opted-in unconsumed candidate regardless of age, retains unselected
candidates and never serves a consumed image as a fresh candidate. Other Job
freshness rules remain unchanged. Equal PNGs from different attempts remain
distinct candidates, and late OnDemand waiters cannot consume newer work.

An unconsumed candidate blocks periodic replacement regardless of elapsed time,
and OnDemand returns it without starting work. Scheduled due remains
`RenderedAt + refreshInterval`; reconciliation and minute maintenance can start
the next attempt after consumption when due. OnDemand adds no periodic retry.
Recovery reads authoritative candidate/committed references before Job files,
confirms directory durability, and completes interrupted Active cleanup without
republishing consumed work. Reset, departure and changed definitions fence late
attempts. Pending delivery blocks preparation, collection and pair mutations
until common delivery recovery resolves it. Startup, display, Job snapshot and
publication, and maintenance use the same complete-turn comparison. Interrupted
single-screen transactions abort; finalized transactions remain committed even
if the HTTP response fails. Deleted UIDs terminate independently of replacements.

Publication saves a content-addressed image and immutable candidate manifest,
then atomically replaces the device record using a complete snapshot CAS.
The manifest binds metadata, context digest, epoch, input generation and resolved
next state. Repeated collection recognizes a published or consumed attempt and
cannot resurrect it. `latest` is not an authoritative state root. Blobs and
manifests are synced before publication; a failed intermediate write leaves only
unreferenced objects. After the authoritative CAS, publication repairs a derived
latest pointer for compatible stale fallback, including across definition resets.
A failed cache update is retried from the manifest; it never binds another image
to state. If latest is absent, delivery can reuse the compatible committed
manifest image without adopting state again. In-memory snapshots are copied
before mutation.

A pending transaction records its ID, prior record revision, complete previous
and proposed turns, selected pair/candidate, expected epoch/generation, issued
digest/filename and next corner. Single-screen proposals omit both turns;
fixed/disabled corners can keep the current corner. Finalize atomically consumes
the candidate, adopts its manifest as committed state, increments generation,
updates corner and clears pending. Abort preserves candidate/state/corner.
The last transaction decision makes retries idempotent after an ambiguous rename
or directory fsync. Cycling callers must establish the decision by comparing
complete API turns; issuance or equal image digests alone are insufficient.

Repository operations hold only their mutex during filesystem operations. They
never read Kubernetes, wait for Jobs, acquire the device gate/reconciler mutex,
or enter image-processing admission. Callers may hold the device gate around
storage operations and must release repository locks before external recovery
work. A process-wide striped decision lock serializes finalization and recovery
so maintenance cannot abort an active single-screen request. Lock order is BYOS
gate or reconciler operation, optional image admission, decision lock, repository
mutex. BYOS drawing releases image admission before acquiring the decision lock;
finalization never waits for image admission or a Job. Revision/snapshot checks
reject publication concurrent with a transition.

A device record admits at most 64 pairs and 64 KiB of serialized data; manifests
are bounded at 48 KiB with state limits defined in PROTOCOL. Capacity failure
preserves every existing pair. Finalization can use the storage reserve.
Departure drops the candidate and retains committed state for 24 hours; compatible
rejoining within that period reuses state. Expiry removes the payload and retains
an epoch tombstone, so old work cannot become current. Tombstones count toward
the pair limit; retained pairs are never silently evicted.

Referenced manifests and their images are retention roots, independently of
latest and issuance. Unreferenced manifests have a 24-hour diagnostic grace.
An unresolved state delivery stops destructive collection until recovery;
invalid/unknown records, missing referenced manifests and invalid image bindings
fail closed. Old authoritative corner formats are not migrated, and downgrade
after version 2 writes is unsupported. Test-only per-repository hooks inject
failures after write, file fsync, rename and directory fsync; production exposes
no arbitrary-stop interface. Filesystem restart tests do not establish power-loss
or multi-node durability.

## Storage topology, retention and failure handling

One active controller watches one configured namespace, using a POSIX filesystem
PVC and Recreate rollout. Job mode requires the whole filesystem RWO claim mounted
by the controller, with required same-namespace/same-node Pod affinity for renderers.
RWO permits multiple Pods on one node; RWOP cannot support this topology, and RWX is unverified/rejected.
Atomic rename, fsync and advisory exclusive writer locking are required. The lock
does not establish multi-replica or multi-node failover support. Account for old
renderers before moving the controller; local storage requires its original node.
The [Kubernetes storage](https://kubernetes.io/docs/concepts/storage/persistent-volumes/)
and [Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/) references
explain the underlying mount and lifecycle model.

Maintenance runs every minute in both HTTP-only and Job modes. Protect active
attempts, all eligible per-screen successes and image bytes/issuance for at least
24h after last issuance. Issuance writes coalesce within one minute, with an
extra retention minute preserving the minimum. Abandoned atomic writes and unused
filename reservations expire after 24h. Removed pairs stop scheduling; completed Jobs and unused run/context data
have a diagnostic hour, with run deletion waiting for their Jobs to disappear.
Unreferenced older blobs, issuance, filenames and deleted-device identities are
collected. Old API snapshots must not collect newly established bindings.

Managed-byte/free-space admission covers publication, identity/issuance, scheduler
metadata and attempts. Failure preserves protected successes and issued URLs;
updating existing issuance may use reserve space. Admission is not a filesystem
quota or renderer reservation. Operational defaults and sizing guidance belong
in [OPERATIONS](OPERATIONS.md#capacity-and-retention). Invalid retention metadata aborts destructive artifact GC. Regenerable
latest pointers can be discarded and corrupt blobs replaced by freshly validated
bytes with the same digest. Identity, issuance and scheduler damage fails closed;
never reconstruct ownership from image files or delete records to bypass it.

Status writes are optimistic, status-only patches; transition times change only
on condition status changes. Stable observations coalesce, while changed telemetry
and screen-cycle transitions remain immediate. Device delivery readiness can
remain true for stale output while rendering readiness is false. Screen readiness
reports its last outcome, not every pair; cycling Job outcomes are not aggregated
into a single device RenderReady. `DeviceSeen` is independent of image usability.

Request concurrency, HTTP execution, metadata reads and retry counters are bounded.
Metrics have fixed labels; logs/Events exclude keys, MACs, raw headers, arbitrary
firmware messages and context/output contents. Screen authors are trusted namespace
operators who can select endpoints, images and explicit renderer env/envFrom;
these never implicitly receive device credentials. See
[OPERATIONS](OPERATIONS.md#network-and-namespace-trust) for the network trust
boundary and [recovery](OPERATIONS.md#recovery-and-troubleshooting) for backup/restore
and shutdown procedures.

The Kubernetes support floor follows the tested dependency minor and API matrix,
as described by the selected controller-runtime's
[compatibility policy](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.1/README.md#versioning-maintenance-and-compatibility).
CEL/API availability alone does not establish older-client/server compatibility;
other minors are unverified rather than declared inherently incompatible.

## Installation lifecycle

Helm packages deployment and schema management without changing the runtime's
UID-based ownership. CRDs are ordinary templates so compatible upgrades can
update schemas; the default dependency belongs to the controller's release and
participates in its rollback. Separate CRD management selects an independent
owner from the first installation. It does not change the cluster-wide API names.

CRDs, chart-created PVCs and auth Secrets carry `helm.sh/resource-policy: keep`
because uninstalling a controller must not silently erase API objects, durable
identity/issuance or generated credentials. Auth templates omit key fields so
setup remains their writer. External claims and Secrets remain externally owned.
Retention preserves objects, not a coordinated snapshot, and leaves the original
Helm ownership metadata intact. A same-owner compatible reinstall can reuse that
state; a new release owner, recreated CR UID or older binary is not equivalent.
Helm rollback cannot coordinate older schemas with newer internal records.
Operational steps and migration limits belong in
[OPERATIONS](OPERATIONS.md#helm-controller-state); the supporting lifecycle
checks are described in [DEVELOP](DEVELOP.md#helm-chart-checks).

## Image processing

HTTP GET/POST and Job collection share PNG normalization, Screen processing
policy, Device profiles/overrides and palette-aware overlays. This section owns
their design rationale, firmware evidence, resource control and persistence
compatibility. [PROTOCOL](PROTOCOL.md#png-contract) owns the input/output contract;
[DEVELOP](DEVELOP.md#integrated-image-acceptance) owns software checks and the
remaining physical acceptance. Reproducible experiments live beside
[image-investigation](../tools/image-investigation/README.md). Source review and
host execution do not establish physical acceptance or expand the verified
support scope in [README](../README.md#security-and-support-boundary).

### Firmware evidence and profiles

Use literal model header values, independently of firmware-version observations:

| Header model | Profile dimensions | Palette | Exclusive PNG bytes |
| --- | --- | --- | --- |
| `og` | 800 x 480 | gray4 | 90000 |
| `reterminal_e1001`, `reTerminal E1001` | 800 x 480 | gray4 | 750000 |
| `reterminal_e1002`, `reTerminal E1002` | 800 x 480 | spectra6 | 750000 |
| `reterminal_e1003`, `reTerminal E1003` | 1872 x 1404 | gray16 | 750000 |
| `reterminal_e1004`, `reTerminal E1004` | 1200 x 1600 | spectra6 | 750000 |

The profile review uses upstream
[`d5f98f2`](https://github.com/usetrmnl/trmnl-firmware/tree/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa),
v1.8.17 at
[`9acd348`](https://github.com/usetrmnl/trmnl-firmware/tree/9acd348270a493ca5f1c627ac91a9469543267ae),
and Seeed common and dedicated E1004 at
[`8d97d89`](https://github.com/Seeed-Projects/OSHW-reTerminal-Series-E-D/tree/8d97d8912987c07529287629f7e73039bb0e6384/examples/official).
[sources.json](../tools/image-investigation/sources.json) pins individual source
URLs and SHA-256 values, including the exact PNGdec 1.1.6 implementation and
FastEPD dependency. Its downloader refuses changed cached/downloaded bytes.
Panel I/O is mocked; conversion callback bodies are extracted unchanged.
PNGdec and its bundled inflate implementation are compiled together, with
`PNG_MAX_BUFFERED_PIXELS` set consistently for callers and decoder: 14984 for
E1003, 6432 for the other upstream/dedicated environments, and 14984 for the
Seeed common E1004 environment. These are byte-buffer settings despite the
macro's name; the small library default is not a valid full-panel experiment.

The upstream device table selects the OG monochrome path for E1001, Spectra for
E1002/E1004, and IT8951/FastEPD for E1003. The pinned FastEPD
[ED103TC2 definition](https://github.com/bitbank2/FastEPD/blob/9a84c35e52849f9f22d2a7ad5231ef501be49e04/src/FastEPD.inl#L1808)
sets 1872 x 1404. Thus E1003 defaults use landscape firmware ordering; the
hardware comparison's [1404 x 1872 ordering](https://wiki.seeedstudio.com/reterminal_e10xx_main_page/#specification-comparison)
is not the profile default. Explicit
dimensions win, followed by a valid reported pair, then this model default.

Profiles describe semantic color sets, not ranked color counts: Spectra 6 lacks
the intermediate neutral grays in gray4. A shared family or palette does not
establish decoder or transport compatibility for another model. The reviewed
E1002 display sources have a six-color path; monochrome-only vendor guidance
does not establish a firmware color restriction. The selected Spectra reference
colors are transmission/matching values, not calibration evidence for every panel.

The reviewed display header builders send `Model`, `FW-Version`, `Width` and
`Height`; setup builders omit dimensions. They send no semantic palette.
Upstream and dedicated E1004 use `reterminal_e100N`; Seeed common's config
macros use `reTerminal E100N`. Resolve only these explicit reviewed aliases,
without general punctuation/case stripping or changing the raw model
observation. Both spellings use the agreed model budget; do not infer or switch
distribution limits from the spelling. The reviewed
upstream display flow and both Seeed flows support setup provisioning, display
polling, filename-based cache reuse and status 202 without a new image. Their
202 branches return an unregistered/pending outcome and sleep; this is not a
new-image success and must not advance controller delivery state. Image requests
use `ID` and `Access-Token`; Seeed Wi-Fi adds them only when the image URL has
the API base prefix. Keep issued image URLs under the same public BYOS base.
Seeed common's modem branch lacks these image headers, unlike the dedicated
E1004 branch; source review does not establish authenticated modem delivery.
No source review here verifies sleep/wake, networking or visible color on a
physical reTerminal.

Upstream PSRAM environments and dedicated E1004 have a 750000-byte budget.
Seeed common E1001/E1002/E1003 retain 90000 despite PSRAM because their macros
do not enter its larger-budget branch. Every reviewed PNG drawing gate uses
strictly less than the selected limit. Retain the agreed upstream-based model
defaults and require an explicit 90000 override for those Seeed common builds;
do not infer distribution from a version string.

The host harness compares full-resolution output, not just decoder return codes:

- OG/E1001 indexed 1-bit polarity differs between upstream and Seeed; 2-bit
  counting counts indices/samples and stops after row 430. Two intermediate
  grays, grays only below that region, and grays only on the final row reproduce
  the loss of intended shades in the selected black/white path.
- Indexed 4/8-bit reordered gray palettes preserve shades. E1003 indexed 1-bit
  produces black because `ReduceBpp` has no 1-bit case. Upstream indexed 2-bit
  expands through the palette into gray16; Seeed common's inverted native
  2-bit output cannot preserve arbitrary gray16 entries. Native grayscale
  1/2/4-bit paths are distinct from these indexed limitations.
- Spectra callbacks preserve intended colors for all four indexed depths,
  reordered subsets, high indices and unused entries. All six selected EPF
  reference RGB tuples select their corresponding panel colors through the
  extracted RGB333 lookup. This is transmission evidence, not panel calibration.
- v1.8.17/Seeed's unmasked low-nibble grayscale expansion corrupts one output
  plane. The corrected upstream formula preserves it. This is separate from
  the two-color counting heuristic and remains observable with native 4-bit
  samples 0/5/10/15.

On the native grayscale path, E1003 uses one-bit samples directly. Upstream
expands two-bit samples 0/1/2/3 to 0/5/10/15 for FastEPD's four-bit mode; Seeed
common instead uses its native two-bit mode. Decode support therefore does not
establish identical panel appearance across distributions.

The [pinned counting callback](https://github.com/usetrmnl/trmnl-firmware/blob/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa/src/display.cpp#L1574)
counts sample/index numbers rather than palette colors, and stops after y=430.
A full four-gray PLTE does not prevent black/white updates when only two numbers
occur in that region. The whole-image gray4 decision deliberately avoids both
the cutoff and a color-count approximation.

For the separate expansion bug, v1.8.17/Seeed uses
`(s[0] & 0xf) | (s[0] << 4)`, retaining unwanted upper bits. Upstream
[PR #671](https://github.com/usetrmnl/trmnl-firmware/pull/671), commit
`47cc1f7c9a7a1e31cf09e75e329d8146dcfd7704`, masks the low nibble before shifting.
Its inclusion in source is not evidence of acceptance of a distributed binary,
and it does not fix the two-bit counting heuristic. Do not select indexed output
solely to avoid the expansion bug or add a runtime firmware eligibility gate.

The intended gray4 encoding inspects every final pixel, uses native type 0 /
2-bit only for black/white, and uses type 0 / 4-bit otherwise, without
re-dithering exact gray4 samples. It remains available behind the temporary
`gray4_native4` build tag. Normal builds currently generate native 2-bit gray4
for all four shades, explicitly accepting the historical counting heuristic
until corrected firmware is accepted. Both encoding and passthrough use the
same immutable build setting; higher-depth gray4 inputs normalize to 2-bit in
the default mode. There is no new CRD field or runtime firmware-version gate.
The build variants have distinct encoder semantic identities, invalidating
freshness, replay and derived caches while preserving immutable issued URLs.
Remove this switch after the firmware migration. Build commands are in
[DEVELOP](DEVELOP.md#temporary-gray4-encoding-switch).
The English rationale beside the shared encoding decision
links to the pinned callback above. Row 430 is firmware evidence, never a
controller scan limit. Native 4-bit assumes corrected expansion: v1.8.17's
existing physical baseline cannot validate it. Any future OG acceptance must
record the actual corrected firmware revision and user visual observations;
do not introduce a runtime firmware gate or an indexed workaround. Full E1002
physical acceptance remains unrecorded. These observations concern hardware
support claims and are outside release preparation and publication.
E1001/E1003/E1004 remain source reviewed only.

### Shared PNG foundations

[`internal/imageprocessing`](../internal/imageprocessing/palette.go) owns the
transport-independent image primitives. HTTP GET/POST and Job collection
normalize inputs for resolved Device targets. Current artifact Units carry explicit
output constraints; BYOS uses resolved palette-aware overlays. Device profiles
and overrides share one resolution contract in `internal/devicestate`.
Input/output contracts belong to
[PROTOCOL](PROTOCOL.md#png-contract).

`Decode` validates the complete bounded file, independently checks IDAT's exact
inflated scanline length and compressed-stream end, and then decodes its default
image once. IDAT is streamed directly from the validated consecutive chunk
region, avoiding both a compressed copy and a per-chunk span allocation. The
length check inflates into bounded scratch space, not a retained pixel canvas.
The resulting `Decoded` holds read-only pixels and encoding metadata, without
retaining the source buffer. `Compatible` determines whether callers may retain
the original bytes; conversion-required inputs remain valid. It implements the
agreed depth table, checks every declared PLTE entry, and reuses decoded pixels
for whole-image gray4 checks. Alpha encodings and tRNS declarations require
normalization, as do APNG and Adam7.

`Palette.Colors` supplies private copies of the canonical gray4, gray16 and
Spectra 6 colors. `Encode` accepts indices in this order and emits deterministic
packed rows with exact samples, no source metadata, and an exclusive output-byte
budget. The default gray4 encoder uses depth 2; `gray4_native4` selects depth
after inspecting all final indices. Gray16 and Spectra 6 always use 4-bit output.
Internal `Target` bounds output allocation to
4096 per axis and 4194304 pixels independently of input limits, and contains
only effective dimensions, palette and byte budget. Device profile resolution
is owned by `internal/devicestate`, separate from renderer-facing context v1.

### Palette-aware overlay primitives

`overlay.NormalizeResolved` and `ComposeResolved` use the effective
`render.ResolvedTarget`, including its exclusive PNG byte budget. They validate
the base with shared decoding/compatibility, map exact opaque samples to canonical
indices, and draw black/white bitmap pixels after dithering. Indexed palette
order does not affect sample preservation. Content is already rotated by the
renderer; only overlay coordinates are transformed. The final image is encoded
with `imageprocessing.Encode` and revalidated against the same target. Gray4
classification runs after drawing across every final pixel. With
`gray4_native4`, removing the last intermediate gray can select depth 2 while
any surviving gray requires depth 4; default builds always generate depth 2
for gray4. Gray16 and Spectra 6 remain depth 4. No compression, palette or content
change is attempted to fit the byte budget; failure permits validated-base
fallback at the delivery adapter.

Current drawing identity combines the bitmap appearance, shared encoder and
palette semantic versions. It changes the derived cache key without rewriting
old Base/Drawing hashes, timestamps, blobs, filenames or issued URLs. Old
issued blobs remain GC roots under the existing retention rules. Replay continues to read the selected immutable bytes without
recompositing or advancing a corner.

Active BYOS uses `NormalizeResolved`/`ComposeResolved` after base normalization.
Legacy `Normalize`/`Compose` adapters and `EncodeLegacyGray4` retain the
unresolved-target and 2-bit gray4 encoding rules. New composites use the
`bitmap-v2` appearance, including fixed padding. Original `bitmap-v1` issued
bytes remain readable through the artifact repository. A compatible legacy fallback can supply a new derived image using current
constraints without changing its original latest pointer or immutable bytes.
Runtime compositing holds the shared admission lease and a processing deadline
through cache lookup/storage and buffer release. Required issuance/state writes
remain outside optional-overlay fallback.

### Normalization and persistence primitives

`Normalize` decodes once and returns the original slice when compatible. Otherwise
`Decoded.Quantize` composes straight 16-bit samples onto white, applies contain
or stretch using the numerical rules below, and emits canonical indices for
`Encode`. It retains no full-size resized RGB/Lab canvas: output indices and
three Lab error rows suffice. Padding is white, never receives diffusion and
uses the final output coordinates for the Bayer tile. Exact palette pixels in
format-only conversions retain their colors without introducing new dithering.
No content rotation or brightness correction is added.

Encoding uses a fixed zlib default compression level. Maximum compression's
long match search on error-diffusion patterns can exhaust the ten-second budget
at 500m CPU even after quantization finishes. Encoder identities are
`png-v3-gray4-2bit` by default and `png-v3-gray4-native4` for the opt-in build.
Compression never changes by content or byte budget, and output
must still pass the same exclusive limit; no samples or dithering are changed
to make an image fit. Compatible renderer bytes retain their own compression.

Processing policy belongs to the Screen so different content can choose resizing
and dithering on the same device. Device overrides describe the effective output
target; there is no separate Device-level dithering override.
Internal `Policy` canonicalizes omitted fit/dither to contain/Atkinson. Its
revision includes processing, encoder and palette semantic versions.
`screen.ProcessingPolicy` maps the public resizeMode/dithering settings to this
policy for all sources, and Screen revisions include its semantic identity.
`Admission.Do` provides one shared synchronous lease; reception, processing,
durable storage and buffer release belong inside its callback. `ReadInput`
reserves one capped 32 MiB allocation and rejects the next byte. `Process`
starts the earlier of ten seconds and the enclosing deadline after collection;
all work must stop before the lease returns. Runtime wiring shares the same
admission instance across all image paths and preserves GET waiter lifetime
rules; merely calling `Normalize` does not acquire capacity or set a deadline.

`render.ResolvedTarget` keeps the context-v1 target separate from effective PNG
budget and output compatibility version. Its fingerprint additionally includes
the normalized policy revision for cycle replay. The comparable artifact
`Unit` adds explicit output constraints only for new delivery. A zero constraint
value explicitly means legacy semantics, not current defaults. Legacy keys are
computed through the original Unit DTO without injecting fields before hashing.
Derived records remain v1 for legacy units and use v2 for explicit constraints;
their original Base/Drawing hashes, immutable blob digests, filenames and issuance
keys stay readable. New output validation uses shared PNG compatibility while
legacy derived validation retains its original encoder rules.

For an explicit target, `Latest` can select a legacy pointer only after validating
the original bytes against the current output contract. The returned metadata
keeps the original Unit, so `Metadata.Fresh` rejects it as a fresh current result.
An incompatible legacy image remains readable at its issued URL and is not
discarded merely for being incompatible. `Sequence` and publication enumerate
all keys for the same owned physical target under the repository lock, preserving
the maximum validated sequence even when an image is no longer deliverable.
GC enumerates this same directory plus derived, pending-corner and issuance
roots, so old keys remain visible. Blob reads have an independent 32 MiB ceiling;
each pointer retains its own exclusive output budget.

Scheduler records explicitly accept the original unversioned DTO and version 2
with output constraints. Startup preserves active ID, sequence, submission flag,
run path and context digest without rewriting the record or snapshot. Preparing
an existing ID cannot overwrite a different context. When desired work changes,
the scheduler saves a terminal reason before cancellation; an interrupted cancel
cannot revive that ID after a definition reversion. A replacement attempt follows
cancellation and durable state replacement. Pending device records require
version 2 and keep both complete original turns, including any legacy target
fields, for recovery. Earlier corner records are rejected without migration;
unknown authoritative versions fail closed.

### Complete-file validation and cancellation

The Go 1.26.8 probes exercise all 15 legal color/depth combinations, with and
without Adam7, indexed alpha, both APNG default-image layouts, truncation,
per-chunk CRC failures and too much/little inflated image data. `image/png`
decodes IDAT's default image for both APNG layouts and ignores color-space
declarations. It validates encountered CRCs and rejects incorrect inflated
lengths, but accepts trailing bytes, unknown critical chunks and malformed APNG
sequence numbers. Therefore `png.Decode` alone is not an input validator.

Use a context-aware complete-file scanner before decoding. Bound the entire
file to 32 MiB, axes to 16384 and canvas to 16777216 pixels before image
allocation, using checked 64-bit arithmetic. Verify signature, ASCII chunk
type/reserved bit, lengths, every CRC, unique first IHDR, legal type/depth,
PLTE/tRNS lengths and ordering, consecutive IDAT, an empty final IEND and no
trailing data. Reject unknown critical chunks. Reference
[PNG Third Edition](https://www.w3.org/TR/2025/REC-png-3-20250624/).
Check large CRC payloads in bounded blocks rather than one unchecked operation.
Keep IDAT spans into the original buffer instead of concatenating a second
32 MiB compressed copy. Decode once; do not retain a full inflated validation
buffer alongside the decoded image. Independently account for exact expected
scanline bytes across all seven Adam7 passes and reject an extra zlib member or
compressed tail, even if a decoder would otherwise ignore it.

For animation structure require a unique 8-byte acTL before IDAT, nonzero frame
count matching fcTL count, 26-byte fcTL records and fdAT records of at least
4 bytes. Check contiguous shared sequence numbers from zero without overflow,
positive frame dimensions within the canvas using subtraction-safe bounds,
dispose 0..2 and blend 0..1. A pre-IDAT fcTL must cover the complete canvas at
offset zero. Require data for every frame, IDAT only for the default/shared
first frame and fdAT only for a subsequent or separate animation frame. Allow
ancillary chunks between fdAT chunks as the specification does. Reject orphan
animation chunks, missing frame data and truncated sequences. Do not inflate
fdAT, allocate frame canvases or evaluate animation timing. Re-encode IDAT's
default image as static PNG regardless of its compatibility; no APNG passthrough.

CRC/ordering checks still apply to ignored color metadata; do not decompress
ICC/text payloads or apply gamma, ICC or chromaticity transforms. Treat decoded
samples as sRGB and omit these declarations in generated images. Byte-preserving
static input may retain them. The scanner's interpretation is deliberately
narrower than full animation decoding, not a promise to validate later frames'
compressed pixel streams.

Use synchronous Go decoding with a context-checking Reader and a final context
check, bounded resizing loops, and a packed-row encoder checking context before
each row and compressed write. No goroutine may return a timeout while leaving
decode/resize/encode running. PNGdec is only firmware evidence, not the server
decoder. The pinned Go inflater has bounded buffers; the remaining noninterruptible
work includes one scanline and Adam7 pass merging (at most half the bounded
canvas for a pass). The experiments cancel inside decoding at several offsets,
inside resizing and inside encoding, and measure return after the actual work
stops. Keep admission held through that return. Use 250 ms cancellation latency
under the deployment CPU limit as the initial acceptance threshold; add explicit
row/merge checkpoints in a licensed local decoder fork if adversarial or native
arm64 measurements exceed it. Re-run these probes on toolchain upgrades.

### Numerical processing semantics

Use a bounded internal implementation rather than adding `x/image/draw`.
The reviewed `x/image` v0.47.0
[interpolators](https://github.com/golang/image/blob/v0.47.0/draw/scale.go)
distinguish a scale-widened `BiLinear` tent kernel from `ApproxBiLinear`'s
four-neighbor reduction. Select classic point-sampled bilinear for both
enlargement and reduction: exactly four neighboring samples, without widening
the kernel or adding an antialiasing filter. This keeps interpolation bounded
and cancellable with the same sampling rule in both directions.

Determine contain dimensions with integer rational arithmetic: choose the
limiting axis by cross multiplication, set it to the target extent and round
the other extent to nearest, halves upward, with a minimum of one pixel. Clamp
that extent to the target. Center with floor-half offsets, leaving odd padding
on the right/bottom. Stretch uses the complete target rectangle. Sample only
inside the content rectangle; padding stays white and receives no diffusion.

For a resized dimension `D` from source `S`, source coordinate is
`clamp((d + 0.5) * S / D - 0.5, 0, S - 1)`. Use floor and its clamped successor
on each axis, combine horizontally then vertically in float64, and round once
to a 16-bit sample, halves upward. Examples: `[0,65535]` enlarged to 3 becomes
`[0,32768,65535]`; `[0,0,65535,65535]` reduced to 1 becomes `[32768]`.

First composite each straight 16-bit sRGB source channel `c` and alpha `a`
onto white using `(c*a + 65535*(65535-a) + 32767)/65535` with integer division
and 64-bit intermediates. Then interpolate these opaque sRGB samples; do not
resize premultiplied transparent colors or perform linear-light interpolation.
Opaque channels remain exact. Fully transparent colored pixels become white.
An 8-bit half-alpha red `(255,0,0,128)` becomes `(65535,32639,32639)`.

For Lab use float64, standard [sRGB-to-Lab conversion](https://www.w3.org/TR/css-color-4/#color-conversion-code)
with breakpoint 0.04045 and
exponent 2.4, XYZ matrix rows `(0.4124564,0.3575761,0.1804375)`,
`(0.2126729,0.7151522,0.0721750)` and `(0.0193339,0.1191920,0.9503041)`,
D65 white `(0.95047,1,1.08883)`, epsilon `216/24389` and kappa `24389/27`.
For exact neutral RGB preserve the mathematical identity `a*=b*=0` and compute
L* from its linear gray channel, avoiding tiny chroma from rounded coefficients.
A 65536-entry transfer lookup avoids repeated powers without reducing 16-bit
precision. Reference Lab values include white `(100,0,0)`, red
`(53.2408,80.0925,67.2032)` and blue `(32.2970,79.1875,-107.8602)`.

Scan top to bottom and left to right, without serpentine traversal. Choose
minimum squared Delta E 1976, with lower canonical palette index winning exact
ties. Keep Lab working/error values unclamped. Accumulate Atkinson neighbors
in order right, right-two, next-row-left/center/right, row-two-center, each 1/8;
Floyd-Steinberg uses right 7/16 then next-row-left 3/16, center 5/16, right 1/16.
Discard out-of-content destinations, without renormalization. Three float64 Lab
error rows suffice; do not allocate a full float64 Lab image. Emit exact target
samples/indices, never a Lab-to-RGB round trip. Resize before quantization;
renderer rotation is never applied again.

Atkinson distributes 6/8 of the error in total; its highlight/shadow detail loss
is an accepted property of the default. Neither diffusion algorithm normalizes
discarded or undistributed weights. No additional brightness gamma adjustment
or D50 chromatic adaptation is applied. The Bayer allocation below adapts
[Yliluoma's two-color principle](https://bisqwit.iki.fi/story/howto/dither/jy/)
to Lab D65 rather than adopting its RGB matching or additional algorithms.

Bayer considers every canonical pair `i <= j` and each integer allocation
`k=0..64` of color `j`. Minimize squared distance from input Lab to
`palette[i] + k/64*(palette[j]-palette[i])`; ties use lexicographic `(i,j,k)`.
Choose `j` where `B[y%8][x%8] < k`, otherwise `i`. Use this matrix orientation:

```text
 0 48 12 60  3 51 15 63
32 16 44 28 35 19 47 31
 8 56  4 52 11 59  7 55
40 24 36 20 43 27 39 23
 2 50 14 62  1 49 13 61
34 18 46 30 33 17 45 29
10 58  6 54  9 57  5 53
42 26 38 22 41 25 37 21
```

Thus an exact 25/75 mixture yields 16/48 pixels per full tile. A brute-force
pair search per pixel exceeds the CPU budget for gray16. Precompute discrete
mixtures: neutral palettes reduce exactly to a sorted L* lookup (chroma distance
is common to all candidates); other palettes use an exact spatial search with
bounding-box distance pruning and original-order ties. Do not substitute an
approximate nearest-neighbor search or content-dependent pattern. Numerical
reference tests compare the accelerated selection with exhaustive pair results.

### Admission, allocation and resource feasibility

Separate output allocation from input limits. Set output axes to at most 4096
and output area to at most 4194304 pixels; apply the same checked bounds to
explicit dimensions, reported pairs and profiles. Reject invalid explicit
pairs at admission; invalid reported pairs remain observable but cannot supply
dimensions, allowing a known profile default. Unknown models remain unresolved
without sufficient valid overrides/observations. A positive PNG byte-limit
override remains an exclusive budget, not permission to bypass the independent
32 MiB complete-file/input ceiling or output allocation bound.

Use one controller-wide memory/CPU admission lease shared by HTTP reception,
Job collection, normalization and overlays. Acquire it before creating a large
HTTP response buffer or opening/reading a Job image; hold it until all image
work and owned buffers are released. Waiting requests retain only bounded
metadata and contexts, not eight 32 MiB bodies. Stream network response data
into one capped allocation, avoiding `io.ReadAll` growth and whole-file copies.
Overlay-only requests must acquire the same lease before decoding a base.
Never wait for admission while holding repository/publication locks. GET
coalescing keeps independent waiter cancellation and a manager-owned shared
work context; a single cancelled waiter must not stop other waiters' work.

Start the processing deadline after reception/collection, at complete-file
validation, using the earlier of 10 seconds and enclosing deadlines. Admission
waiting and HTTP transport retain their own enclosing deadlines. On timeout,
wait for work to stop before releasing admission. Store completed base and
optional derived bytes durably within admission, then release buffers; do not
leave large result buffers in GET flights or queued overlay callers. Image URL
retrieval must use bounded streaming from immutable blobs, or the same memory
admission if retaining whole-byte reads. Merely serializing normalization does
not bound serving, reception or overlay memory.

The measured probe reserves a 32 MiB reception buffer, decodes maximum-area
RGBA16/Adam7, converts using only output indices plus row errors, and packs,
compresses, draws an overlay and encodes again. Tests cover all profile
dimensions, high-entropy 32011838-byte RGBA16 input and both 16384-axis shapes.
The load experiment uses eight real local HTTP clients and one completed Job
file competing for the same lease; waiting clients do not send their GET before
admission. This is an admission/pipeline prototype, not a running controller or
Kubernetes Job acceptance test. The integrated controller uses the same
384 MiB Go memory limit as the experiments with the
512 MiB container limit, leaving room for non-Go memory and runtime variation.

Linux amd64 measurements under actual Docker `--cpus 0.5 --memory 512m
--memory-swap 512m`, Go 1.26.8, showed maximum-input Adam7 to E1003 Atkinson
including overlay at about 5.5 seconds, gray16 Bayer about 4.2 seconds, E1004
Spectra Bayer about 4.3 seconds, and the 2048-square output allocation boundary
about 5.8 seconds. Peak container memory in those individual experiments was
under 307 MiB. Output sizes may exceed a model budget: high entropy and Bayer
gray16 examples exceed 750000 bytes and must fail normalization in production,
preserving fallback, without changing dithering to fit. Probe encoding measures
static indexed packed rows and excludes production native grayscale encoding.
The [integrated acceptance harness](DEVELOP.md#integrated-image-acceptance)
measures production normalization, overlays, transport and real Job collection
under the deployed controller limits; keep its candidate-specific records
separate from these prototype findings. These timings establish feasibility,
not universal 10-second success or integrated-controller acceptance. Native
architecture/resource checks remain part of the software release matrix;
physical acceptance is tracked separately in DEVELOP.
The [raw amd64 measurements](../tools/image-investigation/measurements-linux-amd64.json)
retain the CPU model, toolchain, binary hash and per-case outputs. The mixed
HTTP/Job run admitted one source at a time across all nine requests and stayed
under 334 MiB without forced GC between requests; measured cancellation return
latencies for decode, resizing and row encoding were all below 34 ms.
These prototype measurements exclude informer caches and concurrent production
image-serving traffic; use integrated-controller resource records when assessing
the deployed controller budget.

### API, internal targets and migration

`internal/devicestate.Resolve` recomputes from spec and raw observations for
BYOS, Device reconciliation, Job desired-state/publication, final issuance,
assignment replay selection and maintenance. No gate trusts effective status as
capability evidence. Device watches include raw capability changes; telemetry and
effective-status updates alone do not enqueue all devices. GET flight/cache keys
include the complete resolved output constraints and exclude value provenance.

Keep `status.device.model` and `firmwareVersion` as observations. Use
`status.device.reportedDisplay` with optional int32 `width` and `height`,
containing parsed observations before overrides or allocation validation.
Keep the current rejection of nonnumeric, nonpositive or overflowing header
values; preserve an individually valid but incomplete pair and a pair exceeding
output allocation limits for diagnosis. When either dimension header is sent,
replace the whole observed pair from that request, never combine dimensions
from separate polls. When both are absent, retain the last observed pair.
Preserve existing `status.display.width/height/palette`
as effective values for API readability, with effective `pngByteLimit` and
`sources` with `dimensions`, `palette` and `pngByteLimit`, each `explicit`,
`reported` or `profile` as applicable. Dimensions always have one source for
the complete pair. An unresolved device has no effective display and gets the
existing unresolved/pending condition; it retains observations. Spec edits
must recompute effective values from observations, never from a previously
overridden effective display.

Optional `spec.display` overrides and Screen processing settings are connected
to every renderer path. Normalize omitted
processing to contain/Atkinson before hashing. Read legacy `bw` and `bwry` in
old contexts/status as legacy semantic values without relabeling them as
spectra6 or claiming delivery support. New display overrides accept only the
three reviewed palettes. A missing new provenance field in old display status
is not observation evidence: resolve the old known model afresh; do not
retroactively mark its effective dimensions as device-reported.

Keep `render.Target` as width, height, semantic palette and rotation for renderer
query/context v1. The internal resolved target contains that value,
effective PNG budget and an output-compatibility version. Provenance,
model, firmware and telemetry stay outside its comparable delivery identity.
Normalized Screen input revisions include processing/encoder/palette semantic
versions. Thus equivalent effective capabilities share GET work; budget changes
invalidate target compatibility, while policy changes invalidate freshness and
cycle replay. Prior-policy output may be stale fallback only after validating
its original bytes against the current exact resolved target and budget.

Use explicit old/current disk DTOs and version-aware key functions, rather than
adding defaulted fields to existing structs before validating their hashes.
The current Unit hash is SHA-256 of its original JSON; defaulting a new field
before that calculation would falsely classify every old pointer as corrupt.

| Persisted state | Upgrade behavior |
| --- | --- |
| Unversioned latest metadata/Unit | Validate original legacy key/JSON, preserve its dimensions/rotation and map missing constraints to the legacy gray4 delivery rules, 90000-byte budget and legacy compatibility. Search legacy and current keys when appropriate; a valid old image is eligible only as stale after current-output validation. Carry the maximum validated attempt sequence forward under the publication lock before new writes. |
| Blob, filename and issued records | Keep digest, filename and device-UID issuance keys unchanged. Serve old immutable bytes through the existing authorization/retention path even when no longer eligible for a new display. Never normalize on retrieval or infer ownership from a blob. |
| Derived v1 | Validate with original Base/Drawing hash and old appearance/encoder rules. Treat as old cache, retain issuance roots, and generate new composites under a bumped drawing/encoder identity. Never rewrite an issued composite in place. |
| Device record / pending proposal | Require version 2. Corner v1 records are rejected without migration. For supported records, compare complete Previous/Proposed turns before changing fields; finalize or abort while preserving issuance. Unknown/corrupt authoritative versions fail closed. |
| Public screenCycle target | Missing new compatibility facts denotes a legacy turn, never a current replay. Preserve original turn for pending-corner reconciliation; invalidate replay and validate fallback against current target. Retain the existing compact width/height/palette JSON through a separate target-status type, without embedding new display provenance. Add a compatibility fingerprint covering resolved constraints, rotation, processing revision and semantic versions to new turns. |
| Scheduler pair and Active attempt | Decode legacy state, preserve UID ownership, ID, sequence, Submitted state, run directory and original context digest. Never overwrite its snapshot or reuse the attempt ID. Current model/spec/revision gates mark it obsolete, cancel any owned Job, record terminal state durably and only then prepare a new attempt. Recover a prepared/unsubmitted attempt with the same cancellation ordering. |

GC must enumerate old and current key namespaces, latest pointers, scheduler
attempts, pending corners, issued records and in-memory pins before pruning.
A key-version change must never make protected bytes invisible. Unknown or
corrupt authoritative identity/issuance/corner/scheduler records retain their
existing fail-closed behavior; old optional fields alone are not corruption.
Use atomic fsync/rename updates and make every transition restart-idempotent.
The legacy target-compatibility rules above apply only to turns that already
carry an explicit entry index inside a supported device record. They do not
migrate earlier corner or cycle-position formats. This is an upgrade design,
not a promise of downgrade compatibility.

Recheck UIDs, assignment, resolved target/budget and normalized revision using
direct reads at publication and new issuance, including Job collection and
replay. Validate normalized base before success and final composite before
issuance, always with exclusive byte comparison. Optional overlay failure may
use a validated base; required delivery-state failures preserve existing
failure behavior. Keep the base render timestamp through normalization and
derived images, including Job completion time. Integration tests cover supported
old-PVC image/issuance data, in-flight rendering changes, current-format pending
recovery and GC. Authoritative corner v1 migration is excluded; preserve the
format-rejection and supported recovery coverage when changing these rules.

## Scope of future work

Additional device/firmware targets beyond the reviewed profiles, additional image
formats, target-only Job sharing,
advanced playlist timing/weighting, automatic creation of unknown devices,
arbitrary Pod specifications, additional backends and multiple active controllers
need concrete requirements and compatibility/recovery validation. These are
outside the current support contract, not an implementation queue.
