# Current design

arprt maps declarative TRMNL resources to synchronous HTTP or asynchronous Job
rendering, validated persistent artifacts and authenticated BYOS delivery.
[PROTOCOL](PROTOCOL.md) owns wire/context/image details;
[DEVELOP](DEVELOP.md) maps packages and checks;
[README](../README.md) owns installation and support limits;
[OPERATIONS](OPERATIONS.md) owns configuration/recovery and
[RENDERERS](RENDERERS.md) owns renderer examples and usage.

## Public API and boundaries

The namespaced API is `arprt.dayflower.github.io/v1alpha1`:

- `TrmnlScreen` defines exactly one HTTP or Job source and applicable freshness.
- `TrmnlDevice` selects a single screen, an ordered playlist, or discovery, with
  an auth Secret and optional direct/Secret-backed hardware identity.
- `TrmnlPlaylist` defines 1–64 distinct screen names in explicit order.

All references and discovery stay in the device namespace. Capabilities,
telemetry, registration observations and screen-cycle progress belong in status.
Artifact layout, execution state, cache keys, temporary Jobs/Pods and storage
backend selection remain internal; they do not require artifact/execution CRDs.
Source and assignment exclusivity, identity-source immutability and path/duration
constraints are enforced by structural schema/CEL. Unknown fields require strict
API writes to reject rather than prune; see DEVELOP.

Render Target is width/height/semantic palette; device model is not part of format
identity when capabilities are equivalent. Render Context adds allowlisted device
information, with the same schema for POST and Job. Renderers do not need
Kubernetes clients, upload SDKs or controller credentials. Job arguments are
passed through: the renderer's CLI says where to write, while `imagePath` says
where the controller collects. The controller never interprets arbitrary CLIs.

## Rendering and assignment

| Source | Timing and sharing | Ownership/context |
| --- | --- | --- |
| GET | On demand after miss, expiry or definition change; default freshness 5m. Concurrent work coalesces; equal current definitions/targets share fresh output. | Namespace + screen UID + exact target; no device context |
| POST | Each new authenticated single-screen display request; no fresh reuse or context coalescing. Explicit `refreshInterval` is rejected. | Namespace + screen UID + device UID + exact target; current request overlaid on valid known state |
| Job | Scheduled independently of display; initial readiness, definition/assignment/target changes and interval refresh (default 30m). | Same pair ownership as POST; immutable scheduling snapshot |

Device polling and artifact freshness are independent. GET/POST share bounded
HTTP execution; display reads completed Job output and never creates or waits
for a Job. Telemetry changes affect future Job snapshots without starting work
on every poll. Default normalization and input revisions live in
[screen/input.go](../internal/screen/input.go); revisions include context and
collector contract versions and exclude metadata/status/credentials.

With `screenRef`, one existing screen is selected. With `playlistRef`, missing
or deleting entries are skipped while remaining order is preserved. With neither,
discovery selects the first 64 nondeleting screens sorted by name. A missing
playlist or empty eligible set returns initial pending. Selection does not skip
a screen merely because its renderer currently has no image.

Playlist/discovery uses `status.screenCycle` to persist the successful turn,
target, revision, image digest/filename and serving time. A compatible stale
image counts as success; without a compatible image the selected turn holds.
Issuance and the new turn are durable before the response is acknowledged.

BYOS has no verified unique display-request ID. During `device-refresh-seconds`
after a recorded turn, replay the same image and filename, including across
restart and early manual polls. The window begins after rendering and durable
turn recording, uses microsecond timestamps and is not extended by replay.
Telemetry is still observed. **POST in playlist/discovery renders on a new turn,
not on replay**; a single `screenRef` retains per-request rendering.

The assignment fingerprint includes device UID/generation, assignment identity
and ordered screen names/UIDs. A fingerprint change resets selection to the
first eligible screen. Definition edits invalidate replay without resetting
membership/order; changed targets cannot reuse incompatible output.
All eligible Job pairs are scheduled independently of the current turn, bounded
by the global concurrency limit; work can remain queued before its first image.
Removing eligibility cancels work and stops future attempts.

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
Attempt IDs determine Job identity. Restart can recreate prepared work, adopt
existing work or collect completed work without duplicating an active pair.
Publication precedes clearing the active record so interrupted bookkeeping can
replay publication idempotently.

Recover active work before starting queued pairs; older due work is preferred.
The global concurrency budget bounds active Jobs/run directories, not the number
of persisted pairs. The legacy one-record-per-device scheduler format migrates
by durable rename to namespace/device/screen records, preserving active attempts
and context bytes; conflicting or invalid records are rejected rather than guessed.

Missing submitted Jobs become `JobLost`; unconfirmed execution output cannot be
trusted merely because a file exists. Failed, timed-out or obsolete attempts use
new directories. Cancellation accounts for old Jobs/Pods before replacement work.
`backoffLimit: 0` leaves retries to the controller. Bounded exponential backoff
slows after seven consecutive failures to at least the screen interval and five
minutes; success resets it. Definition changes can bypass obsolete backoff.

Renderer Pods mount only their output directory writable and their immutable
context file read-only. The entire output parent is hidden by the mount, so use
a dedicated directory. Default and distinct/nested parents are supported;
system paths, conflicting files and the reserved artifact root are rejected.
There is no extra writable `/tmp`. Pods run as non-root with a read-only root,
dropped capabilities, no privilege escalation and no ServiceAccount token.

Collection requires Job `Complete=True` and successful renderer termination.
Verify context integrity, open bounded output beneath its directory, reject
escaping symlinks/special files and validate the complete PNG. Missing/partial
output or exit failure never becomes success. Recheck desired pair state after
collection and before publication. The reconciliation/maintenance lock spans
preparation, publication and durable bookkeeping so cleanup cannot remove work
between stages. Kubernetes resource GC does not remove arbitrary PVC directories.

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

## Scope of future work

Additional device/firmware targets, image conversion, target-only Job sharing,
advanced playlist timing/weighting, automatic creation of unknown devices,
arbitrary Pod specifications, additional backends and multiple active controllers
need concrete requirements and compatibility/recovery validation. These are
outside the current support contract, not an implementation queue.
