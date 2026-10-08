# Phase 4 — Asynchronous Kubernetes Job rendering

Implemented 2026-10-08. Job screens now schedule independently of BYOS display
requests and publish through the same PNG validation and PVC repository as HTTP
GET/POST. Public resource fields and Phase 0 defaults remain unchanged.

## Scheduling and durable recovery

- The Job reconciler watches devices, screens and owned Jobs within the configured
  namespace. Only an explicit same-namespace assignment with an observed verified
  `800 × 480 / gray4` target creates a render pair. Playlists/discovery remain Phase 5.
- Initial target discovery, screen definition/generation, assignment, resource
  UID and target changes schedule work. Interval refresh defaults to 30m and is
  measured from successful Job completion. Telemetry/status updates alone do not
  create another attempt or mutate an existing snapshot.
- Each device's current explicit pair has one durable active attempt. Its context,
  context digest, source snapshot, revision, UIDs, target, sequence, attempt ID,
  started time and next-due/backoff state are persisted on the PVC before Job creation.
  One active controller serializes preparation, publication and maintenance.
- Job names derive from the attempt ID. Restart before creation recreates the
  recorded attempt; restart during execution adopts the existing Job. Completed
  work is collected after restart, and publication can be replayed idempotently
  after a crash between pointer publication and terminal bookkeeping.
- A submitted Job that disappears becomes `JobLost`. An unconfirmed run containing
  output is also rejected, including the crash window around submission. A retry
  uses a new directory rather than trusting bytes from an unconfirmed execution.
- The controller defaults to four active Jobs, `backoffLimit: 0`, a 120s Job deadline
  and retries from 5s up to 5m with capped exponential backoff. Spec changes bypass
  old backoff. Cancellation waits for old Jobs/Pods before scheduling replacement
  work. A controller-side timeout also bounds attempts when Job status stalls.

## Renderer and collection contract

`renderer.image`, `command`, `args`, `env`, `envFrom` and `resources` are supported.
Default resources are requests 50m CPU / 64Mi memory and limits 500m / 256Mi;
explicit renderer resource entries override those values. Arguments are preserved.
No Kubernetes client, uploader or renderer API credentials are required.

The controller precreates a per-attempt output directory and immutable context
file. Renderer Pods mount only the output directory writable and the context file
read-only using `subPath`. Defaults are `/trmnl/context.json` and
`/trmnl/output.png`; distinct and nested parents work. The output parent hides all
image files originally at that location, so it must be a dedicated work directory.
System directories and the reserved artifact root remain excluded by CRD/runtime
validation. No additional writable `/tmp` mount is supplied.

Pods run as UID/GID 1000 with `fsGroup: 1000`, RuntimeDefault seccomp, a read-only
root filesystem, all capabilities dropped and privilege escalation disabled.
ServiceAccount tokens are not mounted. Renderer `env`/`envFrom` are operator
configuration; device credentials are never automatically injected or exported
in Render Context.

Job mode validates a whole, writable filesystem RWO PVC mount in the controller
Pod and rejects RWOP, RWX, block volumes and controller subtree mounts. Required
Pod affinity keeps renderers on the controller's node in the same namespace.
The Deployment remains one replica with `Recreate`; multi-node failover is outside
the verified topology. Default manifests add namespace-scoped Job lifecycle,
Pod/PVC read and Event permissions without expanding Secret read permissions.

Collection requires `Complete=True` and successful renderer Pod/container
termination. The collector opens the output within a bounded filesystem root,
rejects escaped symlinks and non-regular files (including FIFOs), bounds bytes,
and applies the shared full PNG validator. Missing, malformed, failed or timed-out
output cannot replace a previous success. Uncached API reads recheck assignment,
UIDs, target, revision and generation before ordered atomic publication.

## Serving, conditions and retention

`/api/display` only reads the pair's completed latest success. It creates no Job
and waits for no Job. Before initial success it returns HTTP 200 / JSON 202; after
a failed or pending update it can serve the same pair's compatible old success.
Issuance and immutable image authentication continue to require the current
device UID. Device polling preserves the reconciler's `RenderReady` reason.

Conditions include `JobPending`, `ImageReady`, `JobFailed`, `JobTimedOut`,
`JobCreationFailed`, `JobLost`, `RunUnavailable`, `RendererNotSuccessful` and
`CollectionFailed`. Failures emit a bounded-rate warning Event with generic
diagnostics and keep finished Job status/logs available for one hour. Device
serving readiness remains separate from asynchronous render readiness. Screen
`Ready` reports the last render outcome, rather than readiness of every pair.

Maintenance runs every minute. Active attempts and their run directories remain
protected. Finished Jobs are retained for an hour; unused run directories and
context snapshots are removed after an hour and after their Jobs have gone.
Pointers belonging to removed/replaced assignments, targets or renderer transports
are dropped. Latest compatible successes remain protected, and immutable bytes
issued in the last 24h are retained even through reassignment. Older unreferenced
blobs, issuance records, filename records and deleted-device identity records are
collected. HTTP-only development mode leaves scheduled maintenance disabled.

Job image telemetry reflects scheduling time, including queue delay, renderer
duration and subsequent device polling delay. It need not match the newest display
request. See the [busybox examples](../examples/job/screens.yaml) for ordinary
container CLI rendering with default and custom paths.

## Validation

- `make verify`: generated schemas/deepcopy consistency, dependency consistency,
  formatting, vet, race tests and controller build passed.
- Full API integration passed on Kubernetes `v1.36.0` and `v1.36.2`, including a
  real controller-runtime manager restart, Job identity adoption, lifecycle watch,
  current CRD defaults and durable artifact recovery. Envtest simulates renderer
  termination; the cluster test below runs actual containers and garbage collection.
- Focused tests cover preparation-before-creation recovery, immutable telemetry
  snapshots, context digests, interval scheduling, coalesced active work, publication
  replay, concurrency, hanging/missing/partial/failed output, changed definitions,
  assignment/target/UID replacement, lost Jobs, escaped symlinks/FIFOs, PVC topology,
  active-run GC protection, transport pointer cleanup and 24h issued-image retention.
- `hack/job-smoke.py` passed on a disposable Linux kind cluster, Kubernetes
  `v1.36.4`, using the default local-path hostpath filesystem RWO PVC. Actual
  `busybox:1.37.0` containers exercised default/custom paths, token absence,
  same-node affinity, context digest/telemetry, 3s interval refresh with 6s render
  time, independent display polling, active execution restart, completed output
  recovery while collection was offline, failed/missing/truncated output, deadline
  expiry, compatible stale image serving, authenticated fetching, obsolete
  assignment cancellation and namespace RBAC.
- `hack/cluster-smoke.py` passed against the same isolated cluster for HTTP
  regression: pairing, generated key/reuse, all MAC source modes, credential and
  image authentication, both proxy prefix modes, telemetry, GET reuse, PVC restart,
  renderer outage, first-image pending and scoped RBAC.

The existing local `v1.36.2` cluster still had the earlier required-MAC Device
CRD. The smoke scripts refused to change that shared schema; validation used the
isolated cluster with current CRDs instead. Test namespaces/CRDs and the disposable
kind cluster were removed.
The existing controller, devices, Secrets, Gateway and default kubeconfig were
not updated. API integration still verifies the `v1.36.0` support floor within
minor 1.36; the cluster check adds `v1.36.4` evidence. Other minors, multi-node CSI
detach/attach and a new physical-device Job acceptance remain unverified.

Milestone B is implemented: HTTP and asynchronous Jobs share the same context,
artifact validation, ownership and device delivery contract. Playlists/discovery
and broader operational hardening remain Phases 5 and 6.
