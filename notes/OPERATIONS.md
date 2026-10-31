# Operations

This guide covers configuration, credentials, monitoring, sizing and recovery.
Start with [README](../README.md#installation-and-pairing) for installation and
support limits. [RENDERERS](RENDERERS.md) covers renderer examples,
[DESIGN](DESIGN.md) explains internal guarantees, and [PROTOCOL](PROTOCOL.md)
defines device responses and image/context contracts.

## Image processing resources

The [integrated acceptance checks](DEVELOP.md#integrated-image-acceptance) distinguish
measured peak memory, deadline failures and physical observations.
The controller chart defaults to a 512 MiB memory limit, 500m CPU limit and
`GOMEMLIMIT=384MiB`. Renderer Jobs retain their independent 500m/256Mi defaults.
One shared image lease covers HTTP reception, Job collection, normalization,
overlays and buffered image retrieval; queued work retains metadata rather than
full PNG buffers. Slow sources/clients can therefore delay other image work.
The transport and processing deadlines are defined in
[PROTOCOL](PROTOCOL.md#png-contract): input up to 32 MiB, 16,777,216 pixels and
16,384 per axis; processing up to 10 seconds bounded by enclosing contexts.
Output uses the resolved Device budget (strictly below 90,000 bytes for OG,
750,000 for E1001–E1004 defaults), independently of input limits. See
[display overrides](#display-overrides) for distribution-specific settings.

An invalid or oversized conversion preserves compatible prior output or pending.
Policy changes invalidate fresh reuse and cycle replay, while an exact-target
compatible prior-policy image may remain a stale fallback. Previously issued
URLs retain their original immutable bytes. Legacy active Jobs become obsolete
under the new processing revision and are cancelled before replacement; completed
current attempts remain recoverable across restart. Physical acceptance of new
profiles and corrected gray4 firmware is tracked in
[DEVELOP](DEVELOP.md#remaining-image-processing-physical-checks), separately from
the completed implementation.

OG firmware 1.8.17 showed corrupted grayscale with native four-bit output in
[physical testing](DEVELOP.md#image-processing-physical-observations). Normal
builds now temporarily generate two-bit gray4, preserving all four shades while
accepting the historical color-count heuristic. The
[`gray4_native4` build flag](DEVELOP.md#temporary-gray4-encoding-switch) retains
four-bit intermediate-gray output for corrected-firmware acceptance. Confirm
the startup encoder identity before restoring a Screen temporarily changed to
[`--palette bw`](https://github.com/dayflower/arprt-renderers/blob/main/renderers/picsum/README.md#compatibility-and-limits) back to `--palette gray4`:
`png-v3-gray4-2bit` is the default; `png-v3-gray4-native4` requires corrected
firmware. Software checks do not establish physical four-bit acceptance.
Issued URLs retain their original bytes across the mode change.

## Display overrides

Omitting `ArprtDevice.spec.display` selects the reviewed model defaults in
[DESIGN](DESIGN.md#firmware-evidence-and-profiles), without a firmware-version
allowlist. Set only the fields that differ for your installed hardware/firmware:

```yaml
spec:
  display:
    pngByteLimit: 90000
```

This exclusive limit is required for reviewed Seeed common E1001/E1002/E1003
firmware; model spelling and firmware version do not identify that distribution.
Palette-only, budget-only and resolution-only overrides are valid. Dimensions
must be a complete positive pair within 4096 per axis and 4,194,304 pixels;
palette is `gray4`, `gray16` or `spectra6`. For example, an unknown model with
valid reported dimensions can use:

```yaml
spec:
  display:
    palette: spectra6
    pngByteLimit: 750000
```

Without reported dimensions, also specify `width` and `height`. Unknown models
never borrow an OG palette or byte budget. Inspect raw model, firmware and
`status.device.reportedDisplay` alongside effective `status.display` and its
`sources`. Removing an override recomputes from raw observations/profile defaults.
Incomplete/unsafe reported dimensions remain visible but cannot supply a target.
An unresolved target reports `TargetKnown=False` and stays pending without
rendering. A budget change invalidates fresh reuse/replay and retires incompatible
Jobs before replacement; a final direct-read gate rejects obsolete delivery.
Old issued URLs remain immutable and retrievable with current authentication.
Operator overrides do not establish physical acceptance or expand verified support.

## E1002 onboarding

Register an `ArprtDevice` with its own named auth Secret and an explicit Screen
before configuring the physical device. Add that Secret to the controller
chart's `credentials.authSecrets`, using `create: true` for setup-generated keys.
Omit hardware identity fields to learn the actual MAC during first setup; do not
test this route with a synthetic setup request before pairing the real device.
Confirm that the deployed controller and served CRDs include the E1002 profile
and display overrides. Creating a Device on an older installation does not
deploy the new image-processing implementation or establish image delivery.

The shipping SenseCraft firmware uses its own provisioning flow. For arprt's
TRMNL BYOS protocol, first install firmware specifically for reTerminal E1002
using the [Seeed TRMNL instructions](https://wiki.seeedstudio.com/reterminal_e10xx_trmnl/).
Record the exact firmware version/distribution; select the E1002 hardware target
in the flasher. Firmware replacement changes the device's software and setup.

With TRMNL firmware installed:

1. Enter Wi-Fi provisioning. Seeed documents a five-second press of the E1002's
   green button for Wi-Fi reconfiguration. Join the device's `TRMNL` Wi-Fi;
   if the portal does not open automatically, visit `http://4.3.2.1`.
2. Under **Advanced → Custom Server → Yes**, enter the full device base URL,
   such as `https://byos.example.com/arprt/devices/arprt/e1002`, without a trailing
   slash or `/api/setup`. Return to Wi-Fi, select a 2.4 GHz SSID and connect.
   Firmware appends the API paths and receives its generated key from setup.
   The [official BYOS connection guide](https://help.trmnl.com/en/articles/12263392-connect-your-device-to-terminus-byos)
   describes these portal controls.

Check `IdentityReady`, the actual reported model/firmware, effective target and
authenticated image delivery after pairing. For the reviewed Seeed common
distribution, apply the explicit 90000-byte override described above once the
deployed API supports it. The Seeed guide describes monochrome TRMNL content;
the software decoder probes and a six-color server image do not prove visible
color acceptance for the firmware installed on the panel. Follow the
[E1002 physical sessions](DEVELOP.md#e1002-acceptance-sessions) separately.

## Hardware identity

`ArprtDevice.spec` accepts at most one of:

```yaml
hardwareID: "02:00:00:00:00:01"  # synthetic direct-MAC example
```

```yaml
hardwareIDSecretRef:
  name: living-room-identity
  key: mac
```

Omit both to learn the first MAC from successful `/api/setup`. The device resource
and its named auth Secret must already exist; `authSecretRef` remains required.
Setup is enabled by default and fills an absent `apiKey` in that Secret. Device
and Secret resource creation remain declarative.

A referenced Secret must be in the device's namespace and contain a canonical
uppercase MAC at the selected key, without whitespace. Missing, unreadable or
invalid identity Secrets make the device unavailable; they never fall back to
learning a MAC. Use SealedSecret or your existing secret-management workflow to
provision the Secret; arprt only reads the resulting Kubernetes Secret.

MAC source selection, a direct value, and a Secret reference's name/key are
immutable after creation. Changing an identity Secret to a different MAC produces
`IdentityReady=False` with `HardwareIdentityChanged`; the registration is retained.
To change hardware or input mode, replace the device resource. Preserve or separately rotate its auth Secret as appropriate. Existing
explicit-MAC devices can establish their durable registration through an
already-authenticated display request without re-pairing.

MACs must be unique within the namespace. Repeated setup for the registered MAC
reuses the key; a different MAC is rejected. `AwaitingSetup` is expected before
an automatic device registers. Back up identity state with the PVC and Secrets;
see [recovery](#recovery-and-troubleshooting) and
[identity persistence](DESIGN.md#identity-and-credential-persistence).

## Setup and credentials

Setup requires the device route and a matching MAC, without an Access-Token.
For automatic MAC registration, it accepts the first eligible MAC. Setup saves
an absent key in the declared Secret and reuses it on concurrent requests, retries
and restart. The generation and persistence rules are in
[DESIGN](DESIGN.md#identity-and-credential-persistence).

An existing `apiKey` is never overwritten, including an empty or invalid value.
Remove an invalid field or supply a valid key explicitly. An unavailable Secret,
an immutable empty Secret or denied patch permission returns 503; an invalid
key returns 401. Display, log and image requests cannot generate keys.
See [PROTOCOL](PROTOCOL.md#responses-and-polling) for response status details.

For GitOps, declare only the auth Secret's metadata and type when using generated
keys. Leave `data.apiKey` and `stringData.apiKey` out of the desired manifest;
do not combine generated keys with another controller that owns that key field.
Store the live auth Secrets in your backup alongside the artifact/identity PVC.
To rotate, supply a new valid key or remove `apiKey` and run setup again on the
device. Deleting the Secret resource requires redeclaring it first. Setup stays
enabled by default; set `byos.setupEnabled: false` in Helm values to disable it
explicitly (`--enable-setup=false` for local execution).

## Dashboard

Set `dashboard.enabled: true` in private Helm values and apply the upgrade to
start the dashboard on port 8083. With release `home` in namespace `arprt`:

```sh
kubectl -n arprt port-forward deployment/home-arprt 8083:8083
```

Open `http://127.0.0.1:8083/`; keep the tunnel bound to localhost. No Service or
Ingress is created for the dashboard, and the existing BYOS Service does not
expose it. Direct execution can instead use
`--dashboard-bind-address=127.0.0.1:8083`. The default `0` registers no dashboard
listener. Setting `dashboard.enabled: false` and upgrading stops it without
removing delivery observations or images.

The dashboard has no built-in authentication or device-token checks. Restrict
Pod/port-forward access to operators. External access requires an authenticating
proxy in front of this separate listener; the BYOS public prefix and
`proxyStripsPrefix` do not apply. All UI assets ship in the controller binary.
Startup/bind failures stop the manager; manager shutdown cancels request work
and allows up to 15 seconds to drain the listener within its existing shutdown
budget.

Device rows refresh every 30 seconds and show communication, delivery and
rendering separately. The preview is the last **recorded completed delivery**,
including overlays, and is not confirmation of the physical display or a
guarantee of the latest issued image. Generation time refers to the original
base render, not issuance, overlay updates or last communication. Replays and
fallbacks retain that provenance when known. Missing legacy metadata stays
unknown. A failed observation write can leave an older record; inspect
`arprt_storage_errors_total{operation="delivery_record"}` when investigating.
See the [observation contract](DESIGN.md#delivery-observations-and-dashboard-api).

Records and retained previews survive controller restart on the same PVC for
the same Device UID. A replacement Device with the same name has a different
UID and does not inherit them. Opening the dashboard never renders, advances a
playlist, updates telemetry, renews issuance or extends retention. Existing
GC can collect a recorded image: `No image available` means no observation,
while `Preview unavailable` means the observation or its recorded bytes cannot
be read. Back up the PVC, resources and Secrets together using the existing
recovery procedure.

## Runtime configuration

The CLI in [main.go](../cmd/controller/main.go) defines runtime options. Configure
[controller chart values](../charts/arprt/README.md#values) and apply them with
`helm upgrade`. The [Deployment template](../charts/arprt/templates/deployment.yaml)
selects the Pod namespace, enables Jobs with its RWO claim, and enables internal
metrics on 8082.

| Option | Default or fallback | Purpose |
| --- | --- | --- |
| `--namespace` | `WATCH_NAMESPACE` | Required watched namespace; `POD_NAMESPACE`, when set, must match |
| `--public-base-url` | `PUBLIC_BASE_URL` | Required public URL including the external prefix |
| `--proxy-strips-prefix` | false | Accept requests after the proxy strips exactly the configured external prefix |
| `--enable-setup` | true | Permit setup/key provisioning for eligible devices |
| `--device-refresh-seconds` | 300 | Device polling and cycling retry window; separate from screen freshness |
| `--overlay-time-zone` | `UTC` | Explicit IANA zone for original base render timestamps; `Local` and fixed-offset strings are rejected |
| `--artifact-root` | `/var/lib/arprt` | Persistent artifact/identity/scheduler filesystem root |
| `--job-pvc` | `JOB_PVC` | Shared RWO claim; empty disables Job scheduling |
| `--controller-pod` | `POD_NAME` | Controller Pod identity for same-node Job affinity |
| `--job-concurrency` | 4 | Global active Job execution limit |
| `--job-deadline-seconds` | 120 | Renderer Job deadline |
| `--byos-bind-address` | `:8080` | Device listener |
| `--health-probe-bind-address` | `:8081` | Health/readiness probes |
| `--metrics-bind-address` | `0` locally; `:8082` in Deployment | Internal metrics listener; `0` disables it |
| `--dashboard-bind-address` | `0`; `:8083` with `dashboard.enabled: true` | Separate read-only operator listener; `0` disables it |
| `--storage-max-bytes` | 805306368 (768 MiB) | Managed storage admission limit |
| `--storage-min-free-bytes` | 67108864 (64 MiB) | Filesystem free-space reserve |

Job mode requires the whole filesystem RWO claim mounted at the artifact root;
subtree mounts, RWOP, RWX and block volumes are rejected. Renderers run on the
controller's node. Keep one active replica and Recreate rollout. For HTTP-only
local execution, see [DEVELOP](DEVELOP.md#local-controller); Job screens then
report `JobSchedulingDisabled`. Job `backoffLimit` is zero; Scheduled retries
use new attempts with exponential backoff from 5s to 5m, slowing after persistent
failure as described below.

`source.job.executionMode: OnDemand` starts only on authenticated displays;
omit `refreshInterval`. `timeout` defaults internally to `5s` and admits integer
`1s`–`10s`. The candidate timeout includes global slot waiting, startup,
collection and successful base-image storage. Admission plus 10s caps rendering;
the existing 13s application and 15s listener limits remain unchanged. A shorter
`--job-deadline-seconds` safety limit wins. Scheduled and OnDemand share
`--job-concurrency`, including stopping Jobs/Pods. One disconnected waiter leaves
shared single-screen work alive; no remaining waiters stop it. Cleanup does not
delay the HTTP response. Timed-out, disconnected or obsolete work cannot publish
late output. OnDemand schedules no periodic retries; the next display is the
next trigger. Restart stops uncommitted work without recreating it, preserving
durable successes and issued URLs. Mode changes stop the old execution first.
Measure real container startup and physical delivery separately using the
[on-demand acceptance procedure](DEVELOP.md#on-demand-job-acceptance). A cold pull
can exhaust the budget before the renderer runs; pre-pull the selected image and
keep its startup/work within the existing budgets. A stopping Pod
continues to occupy capacity even after a display has returned fallback/pending.

Playlist `failurePolicy` defaults to `Hold`; `Skip` opts into finite candidate
exploration without stale fallback. Skip-only `renderTimeout` defaults internally
to `10s` and accepts integer `1s`–`10s`. Authentication and all candidate attempts
consume that same admission-based budget. All failures or expiry return pending
without advancing the successful turn. Storage, authorization, issuance and turn
recording faults terminate the operation instead of trying another Screen.
Concurrent equivalent Skip requests share exploration and one durable turn;
individual disconnects do not stop remaining waiters. Policy/budget edits
invalidate replay. See [selection semantics](DESIGN.md#skip-playlists).

Overlay timestamps use embedded IANA zoneinfo, including in the scratch image,
and do not consult `TZ` or host local time. Set
`display.overlayTimeZone: Asia/Tokyo` in private Helm values and run
`helm upgrade`. Changes apply to new deliveries; a committed replay
keeps its image. Renderer environment and Render Context remain unchanged.

## Conditions

Device conditions report `IdentityReady`, `IdentityUnique`, `AuthenticationReady`,
`AssignmentReady`, `TargetKnown`, `TelemetryValid`, `RenderReady` and `Ready`.
Screen `Ready` describes the last render outcome for its current revision, not
readiness of every pair. Compatible stale output can keep device `Ready=True`
with `StaleImageAvailable`, while `RenderReady=False` and screen `Ready=False`
expose the rendering failure. `DeviceSeen` tracks request freshness separately.

Playlist `Ready` validates references rather than rendered output. Missing entries
are skipped; an empty eligible set reports `NoEligibleScreens`. For cycling,
Job failures appear on the relevant screen/Job, and device `RenderReady` is not
an aggregate across pairs. Explicit Job display polling preserves the reconciler's
failure reason. Job reasons include `JobPending`, `JobFailed`, `JobTimedOut`,
`JobCreationFailed`, `JobLost`, `RunUnavailable`, `CollectionFailed` and `ImageReady`.
OnDemand records `RequestCancelled` when no waiter remains and `RequestLost`
when startup recovers an unconfirmed request. An elapsed candidate deadline is
`JobTimedOut`, including when its last waiter leaves at that same deadline.

No compatible initial image produces pending. Firmware can retain the previous
image on a normal timer wake, but may show its Friendly ID screen when a redraw
is required after a manual or power wake. This response does not request a
credential reset or OTA. See [PROTOCOL](PROTOCOL.md#responses-and-polling) for
HTTP/JSON status, display behavior and backoff.
Use [strict field validation](DEVELOP.md#selecting-checks) when applying resources.

## Monitoring

`arprt_playlist_attempts_total` diagnoses Skip exploration with fixed outcomes
`success`, `skipped`, `budget_exhausted` and `joined`. It contains no device,
Screen, URL or credential labels. Screen/Job Conditions retain render failures;
Playlist `Ready` continues to validate references rather than aggregate execution.

The supplied Deployment exposes Prometheus metrics on container port 8082.
It has no public metrics Service or Gateway route. For release `home`, use
`kubectl -n arprt port-forward deployment/home-arprt 8082:8082` and fetch
`http://localhost:8082/metrics`. Local development opts in with
`--metrics-bind-address=:8082`; `0` disables the listener. Restrict monitoring
access with your network policy. Do not expose this endpoint on the BYOS proxy.

| Metric | Meaning |
| --- | --- |
| `arprt_requests_total` | BYOS requests by fixed route and HTTP result; pending is HTTP success |
| `arprt_request_rejections_total` | Requests rejected by the 64-request admission limit |
| `arprt_render_duration_seconds` | GET, POST and Job attempt duration, with success/failure labels |
| `arprt_http_render_active`, `arprt_jobs_active` | Current execution slots/Jobs |
| `arprt_job_queue_depth` | Due persisted pairs awaiting execution, sampled during maintenance |
| `arprt_devices` | Counts of never-seen, recent and silent devices |
| `arprt_device_last_seen_max_age_seconds` | Oldest observed device request |
| `arprt_artifact_oldest_age_seconds` | Oldest latest successful artifact |
| `arprt_storage_bytes`, `arprt_storage_free_bytes`, `arprt_storage_limit_bytes` | Managed logical usage, filesystem availability and configured admission limit |
| `arprt_storage_errors_total`, `arprt_collection_failures_total` | Storage faults and invalid/unavailable completed output |
| `arprt_overlay_fallbacks_total` | Overlay candidates falling back to their validated base after drawing/cache/persistence failure |
| `arprt_maintenance_total` | Successful/failed maintenance passes |

Application metric labels never contain resource names, UIDs, target hashes,
MACs, URLs, credentials or telemetry. Logs and Events describe fixed outcomes
and may identify Kubernetes resource names; request bodies, headers, renderer
URLs and output contents are excluded. Conditions provide per-resource detail.
`DeviceSeen` becomes false after three polling intervals (at least two minutes),
independently of whether a previously delivered e-paper image remains usable.
Device counts, queue depth, artifact age and storage usage refresh each minute;
render/request counters update on activity. Alert on increasing failure counters,
old artifacts, silent devices and a queue that does not drain.

## Capacity and retention

The default 1 GiB PVC has headroom through the storage admission limit and
free-space reserve listed in [runtime configuration](#runtime-configuration).
Usage includes base/composite blobs, derived provenance/cache references,
authoritative corner records, last-recorded delivery observations, metadata,
Job snapshots and output files.
Overlays can create multiple images per base/device/day; allow headroom for the
24-hour issued-image retention and avoid unnecessarily short polling intervals. Before new image publication, identity/issuance
allocation or Job work, the controller checks logical usage and available
filesystem space. An admission failure reports `StorageCapacityExceeded`,
keeps compatible prior output and retries after space becomes available.
Updates to an existing issuance may use the reserve so stored images can still
be delivered. The controller never evicts protected images to satisfy a limit.
Delivery observations are best effort and do not consume the issuance reserve;
`arprt_storage_errors_total{operation="delivery_record"}` counts failed writes.
An older observation can remain after a write failure or crash, without
preventing device delivery. Observation files neither retain image bytes nor
extend issued-image access. See the
[observation contract](DESIGN.md#delivery-observations-and-dashboard-api) for
freshness and recovery limits and [dashboard operations](#dashboard) for
listener configuration and operator access.

These checks are admission controls, not reservations or a filesystem quota.
Concurrent renderers can write beyond the threshold before the next check.
Use trusted renderer images, filesystem quotas and filesystem/PVC monitoring
when a strict disk bound is required. A small compressed PNG limit does not
limit arbitrary intermediate renderer files. Actual allocated disk usage and
inode availability should also be monitored by the storage platform.

Maintenance runs every minute in both HTTP-only and Job modes. Latest compatible successes remain
protected; issued image bytes and access records remain for at least 24 hours
after the last display response. Jobs and run directories retain their diagnostic
hour and active attempts are never removed. Detailed bookkeeping/collection
protection is described in
[DESIGN](DESIGN.md#storage-topology-retention-and-failure-handling). Seven consecutive
Scheduled Job failures slow further retries to at least the screen interval and five
minutes; success clears the counter. No manual reset is needed after recovery.

Each device can select at most 64 screens, so `D` devices can require `64 × D`
scheduler records. Only `--job-concurrency` attempts allocate active run
directories or nonterminal Jobs. Capacity admission also limits new scheduler
metadata. [Recorded scale checks](DEVELOP.md#recorded-acceptance-baseline) establish
bounded active work, not throughput. Scale and size the namespace, polling
intervals and storage together. With four active Jobs
and the one-hour diagnostic retention, short successful refresh intervals can
still retain many completed Jobs. Use an appropriate namespace ResourceQuota
for a hard Kubernetes object ceiling; quota rejection retains prior output and
uses bounded retries.

## Helm-managed CRDs

The [CRD-only chart](../charts/arprt-crds/README.md) uses ordinary Helm templates
so compatible upgrades update the generated schemas. With the controller chart's
default `crds.enabled: true`, these CRDs belong to the parent release, including
its upgrade and rollback. With separate management, the CRD release owns schemas;
controller operations use `crds.enabled: false` and do not change them.
Helm-rendered CRDs carry `helm.sh/resource-policy: keep`: uninstall and operations that remove a CRD
from the release retain the CRD and its custom resources. See Helm's
[resource retention guidance](https://helm.sh/docs/howto/charts_tips_and_tricks/#tell-helm-not-to-uninstall-a-resource).
Retention does not block schema changes, establish safe downgrade, or substitute
for a consistent backup of the PVC, CRs and auth Secrets. Helm rollback can
apply an older schema; check API and internal-record compatibility before any
downgrade. Do not rely on rollback for automatic recovery.

After uninstall, retained CRDs still carry the original release name and
namespace. Reinstall the compatible owning chart (parent for bundled CRDs,
CRD-only for separate management) using that same name/namespace to resume
management. A different release cannot silently assume ownership. CRDs are
cluster-wide, so changing release namespace does not provide another independent
set. Changes between separate and bundled CRD ownership and forced ownership
transfer are outside the tested installation path.

Explicit removal is a separate destructive operator action: first stop all
controllers using this API, back up their PVCs, CRs and auth Secrets consistently,
and inspect custom resources across all namespaces. Deleting any of the
following CRDs deletes **all of its custom resources in every namespace**:

```sh
kubectl delete crd arprtdevices.arprt.dayflower.github.io \
  arprtscreens.arprt.dayflower.github.io arprtplaylists.arprt.dayflower.github.io
```

Uninstall the owning release before explicit deletion. Keep this deletion out
of routine uninstall, upgrade and recovery procedures.

## Helm controller state

The [controller chart](../charts/arprt/README.md) requires Helm 4 and starts with
empty auth and identity Secret lists. Select each same-namespace auth Secret explicitly with
`credentials.authSecrets[].name` and `create`. With `create: true`, Helm declares
only metadata and `type: Opaque`; it never supplies `data.apiKey` or
`stringData.apiKey`. Setup generates an absent key and reuses the live value on
compatible upgrades and same-owner reinstalls. Keep generated keys and MACs out
of values, Helm notes and logs. Identity Secrets are always provisioned externally.

Auth names receive only Secret `get`/`patch`; identity names receive only `get`.
There is no Secret `create`, `list`, `watch` or blanket access. A name listed for
both purposes receives the union of those permissions; use distinct Secrets if
an identity must remain read-only. Removing a name revokes the corresponding
permission. It does not remove that name from any Device reference; update
references deliberately or the device becomes unavailable.

Chart-created PVCs and auth Secrets carry `helm.sh/resource-policy: keep`.
Uninstall, removing an auth entry, changing `create` to false, or selecting an
existing claim retains the previously managed objects. Once an object leaves
the release manifest, Helm no longer manages it. It still carries the original
release ownership annotations. Reintroducing it under that same release
name/namespace resumes management; a different owner must not take it over
automatically. `create: false` and `persistence.existingClaim` reference objects
without creating or changing them, including their existing ownership metadata.
Removing an auth entry does not rotate or revoke its key for other principals
that can still read it.

Before an upgrade or uninstall, take a consistent private backup of the PVC,
CRs and auth Secrets using the recovery procedure below. Record the release
name/namespace, chart/image versions, private values, CRD ownership mode, and
PVC/Secret names separately from generated credentials. Retention is not a
backup or a guarantee of safe rollback. Do not delete the namespace: Helm's
`keep` annotation does not protect resources from namespace deletion, direct
Kubernetes deletion or storage loss.

### YAML-managed CRDs

On a fresh cluster, the release's `crds.yaml` asset can be applied by `kubectl`
or GitOps before installing the controller with `crds.enabled: false`. See the
[download and checksum procedure](../README.md#yaml-managed-crds). These schemas
match both CRD chart copies but contain no Helm ownership or retention annotations.
The YAML manager owns schema updates; controller install, upgrade, uninstall and
reinstall leave these CRDs unchanged. Deleting CRDs through the YAML manager
still deletes their custom resources across all namespaces. Configure GitOps
pruning accordingly and preserve backups.

For a compatible schema update, download and verify the target controller tag's
bundle and apply it with the same field manager (`arprt-crds` in the example)
before upgrading the controller. The intermediate schema must remain compatible
with the running controller. Controller-only upgrades need no schema operation
when the installed schema is already compatible. Preserve `crds.enabled: false`
on every controller operation. Ownership transfers from existing Helm releases
are outside the tested path; applying the bundle does not perform a migration.

### Compatible upgrades

Review the target release's API and internal-record compatibility, chart defaults
and image index digest before upgrading. A compatible upgrade preserves existing
CR UIDs, auth keys, registered identity, PVC contents and issued-image access.
Breaking API or data-format changes require an explicit validated migration
procedure; the generic commands below do not supply one. Record the source
revision and retain the previous compatible chart packages, image digest and
complete private values with the backup.

Keep the release name, namespace, CRD ownership mode, claim and credential
references stable. Select the explicit chart versions from the compatible
target release. Published parent packages include their locked CRD dependency;
no source checkout or dependency build is needed. Local chart development uses
the [DEVELOP procedure](DEVELOP.md#local-chart-installation).

For separate management, first upgrade the independent CRD release when the
target controller requires a newer compatible schema:

```sh
helm upgrade arprt-crds oci://ghcr.io/dayflower/charts/arprt-crds \
  --version <crd-chart-version> --namespace arprt --wait
```

The intermediate schema must remain compatible with the running controller.
If it cannot, use the release's migration procedure. Controller-only upgrades
need no CRD operation when the installed schema is already compatible.

Upgrade the controller with the complete private values file, retaining
`crds.enabled: false` for separate or YAML management. For bundled CRDs, this one
parent operation updates both schemas and controller:

```sh
helm upgrade home oci://ghcr.io/dayflower/charts/arprt \
  --version <chart-version> --namespace arprt --wait \
  --values /path/to/private-values.yaml
```

Changing `image.tag` alone has no effect while `image.digest` is set; select the
accepted multi-platform index digest for the intended image. ConfigMap value
changes trigger a controller rollout; Recreate temporarily interrupts delivery.

After Helm waiting succeeds, check device/screen conditions, setup/key reuse,
normal authenticated image delivery, Job rendering when enabled, and fetching
of a previously issued URL within its retention window. Keep keys and real
identities out of command output and reports. Readiness alone does not verify
these state and delivery contracts. If an operation fails, inspect Helm release
status, Pods, Events and CR conditions before choosing recovery.

Helm rollback is not an automatic recovery procedure: bundled CRDs can revert to
an older schema while PVC records, CR status and generated keys are not restored
to an earlier snapshot. Internal-record downgrade limits in
[recovery](#recovery-and-troubleshooting) still apply. Test compatibility or use
a release-specific migration/consistent restore procedure before any downgrade.

### Uninstall and reinstall

For a compatible reinstall of `home` in namespace `arprt`, keep the namespace,
CRDs, CRs, PVC and auth Secrets. Stop outstanding renderer Jobs before allowing
another controller to use the PVC. Use the same release name/namespace, compatible
chart/image, auth Secret names and creation flags, and the original CRD mode.
Use the compatible chart version recorded with the backup:

```sh
helm uninstall home --namespace arprt --wait
helm install home oci://ghcr.io/dayflower/charts/arprt \
  --version <chart-version> --namespace arprt --wait \
  --values /path/to/private-values.yaml
```

To explicitly reuse retained or externally restored storage, add the following
to that private values file before installation (use the actual claim name):

```yaml
persistence:
  existingClaim: home-arprt-data
```

Leave `persistence.storageClass` and `persistence.size` at their defaults; manage
the existing claim externally. To use retained auth Secrets without resuming
their Helm management, keep their names in `credentials.authSecrets` and set
`create: false`; named access permission is still required. The chart mounts
the claim at the artifact root and passes its name to Job configuration.
It must satisfy the same whole-filesystem
RWO contract, even with Jobs disabled. Restore auth Secrets consistently and
preserve CR UIDs; creating replacement Devices does not inherit stored identity
or issued-image authorization. After reinstall, check readiness, setup/key reuse
and authenticated fetching of an already-issued URL before normal operation.

API-server integration tests verify key and PVC UID retention, same-owner
reinstall, removal/reintroduction and external-object non-ownership with the
pinned Helm 4 client. The [dedicated kind lifecycle checks](DEVELOP.md#helm-chart-checks)
also exercise mounted PVC state, HTTP/Job rendering and authenticated issued-URL
recovery across config upgrades and same-owner reinstall, with bundled and
separately managed CRDs. These checks use compatible temporary schema fixtures;
they do not establish arbitrary version upgrades or safe rollback.
Bundled-to-separate CRD transfer, release renaming and forced ownership changes
remain outside the tested path.

## Recovery and troubleshooting

Back up the PVC, CRs (including status and original UIDs), auth Secrets and any
external identity Secrets as one consistent set. Include the installed CRD
schemas, chart sources/revision or packages/versions, image index digest, private values,
release names/namespaces and ownership mode in the recovery inventory. Helm
release history does not contain the controller-generated key or PVC contents.
Keep this material private because it includes keys, registered MACs and
allowlisted device context snapshots.

Stop incoming device traffic, stop the controller and ensure renderer Jobs are
no longer writing before taking the filesystem snapshot and matching API/Secret
backup, or use a snapshot mechanism with the same consistency guarantee. Scaling
the controller down alone does not stop existing Jobs. Use the storage platform's
backup/restore mechanism and retain the original state until recovery is checked.

The tested reinstall procedure above reuses live CRs and their original UIDs.
A YAML export followed by `kubectl apply` creates new UIDs after resource deletion;
it does not restore the ownership required by existing PVC records or issued URLs.
Full disaster recovery therefore also needs an API-state restore mechanism that
preserves those UIDs and matches the PVC/Secrets. This project does not provide
a general cluster-restore or cross-installation migration procedure. Do not edit
internal JSON to transfer ownership. Verify readiness, identity/key reuse and
authenticated issued-image access before resuming device traffic.

The controller uses an advisory exclusive filesystem lock to reject a second
writer on the same PVC. Keep one active replica and Recreate rollout. This lock
does not make replicas, multiple PVCs, multi-node failover or shared serving
metadata supported. SIGTERM cancels in-flight HTTP rendering, stops admitting
new connections and closes the listener within the 20-second shutdown budget;
incomplete work cannot publish success. Kubernetes Jobs continue independently and are
recovered by the next controller from their durable snapshots and API status.

| Symptom | Checks and response |
| --- | --- |
| `DeviceSeen=False` | Power, Wi-Fi, configured custom-server URL, proxy and device polling interval |
| 401/403 | Auth Secret validity, registered hardware identity and preserved proxy headers; repeat setup after key rotation |
| JSON `status: 202` | Assignment/target conditions, then renderer readiness; no compatible first image is available |
| `StaleImageAvailable` | The existing image is safe to serve; inspect the screen condition or retained Job for the failed refresh |
| `StorageCapacityExceeded` | PVC usage/free bytes/inodes, configured limits, retained Jobs and output growth; expand storage or wait for safe GC |
| `CollectionFailed` | Successful renderer exit, actual output path, regular PNG file, size/palette/dimensions and read permissions |
| Maintenance failure | Check filesystem access and restore invalid retention metadata from backup; GC deliberately avoids guessing ownership |
| Display/maintenance rejects overlay corner state | Restore `corners/` from a consistent backup; do not delete or reset authoritative pending/corner records |
| Startup rejects scheduler state | Restore `jobs/` metadata from a consistent backup; do not delete active records while their Jobs exist |
| Identity unavailable after storage damage | Restore the matching identity record; deleting it could allow re-registration to different hardware |
| Corrupt image/latest pointer | The affected unit is regenerated on the next render; other valid units and issuance remain intact |
| Existing URL returns 404 | Confirm device UID, authentication, retention window and blob integrity; an old URL cannot recover missing bytes without its original validated output |

Inspect `kubectl -n arprt get arprtdevices,arprtscreens,arprtplaylists -o yaml`,
`kubectl -n arprt get jobs,pods`, and `kubectl -n arprt get events`.
Avoid publishing Secret values, raw device requests or renderer logs containing
private data. A syntactically corrupt latest pointer is regenerable; corrupt
identity, issuance and scheduler records fail closed. Invalid retention metadata
aborts destructive artifact GC. Persistent filesystem errors require operator
repair; a healthy Kubernetes API probe alone does not verify storage integrity.

Derived caches under `derived/` are regenerable and references to absent/corrupt
images become cache misses. Issued image bytes, filename records, issuance and
pending corner records must remain consistent. Restore authoritative damage from
backup rather than deleting records to force delivery. Overlay failures may
fall back to a validated base without changing render freshness or readiness.
Downgrading after fractional status/Context writes or new retention/corner writes
is unverified; older binaries do not understand these records.

## Network and namespace trust

The namespace is the trust boundary. Screen authors can choose HTTP endpoints,
container images, commands and same-namespace Secret/ConfigMap references.
Permission to create screens therefore permits running code and supplying
Secret-backed renderer environment through controller-created Jobs, even when
the controller's own direct Secret reads are restricted by `resourceNames`.
Grant CR write access only to trusted operators. Renderers do not receive the
controller's ServiceAccount token, auth key, MAC or infrastructure credentials
through Render Context. An explicitly configured renderer environment is an
operator-controlled exception and must contain only that renderer's credentials.

HTTP endpoints may be private cluster services or external HTTP(S) services;
redirects and URL userinfo are rejected. There is no built-in IP allowlist or
network isolation. Configure NetworkPolicy/egress controls and DNS/TLS according
to the endpoints you intentionally permit. Keep setup and BYOS behind the
configured TLS proxy, restrict network access where possible, and preserve the
firmware authentication headers. Setup remains enabled by default for the
registered MAC; use `--enable-setup=false` when your operating policy requires it.
Images require current device authentication and durable issuance to that UID.
The bundled setup calibration image is intentionally anonymous.
