# Development

This guide covers working on arprt. See [DESIGN](DESIGN.md) for internal
guarantees, [PROTOCOL](PROTOCOL.md) for wire/image contracts, and
[README](../README.md) for installation/support, [OPERATIONS](OPERATIONS.md) for
runtime administration and [RENDERERS](RENDERERS.md) for example usage.

## Tooling and generated files

Use Go, Make, a C compiler for race tests, Python 3.12 or newer for development
checks, [uv](https://docs.astral.sh/uv/getting-started/installation/) for Ruff,
and Docker for images. Install uv and make it available on `PATH` before running
Python lint or formatting targets; set `UV=/path/to/uv` to select another binary.
The [Makefile](../Makefile) selects the Go toolchain and
pins controller-gen/setup-envtest/kind and the kind node image;
it also pins Staticcheck, Ruff and the Helm 4 client used for chart checks.
[go.mod](../go.mod) pins dependencies and [CI](../.github/workflows/ci.yaml)
defines the Kubernetes test version and pins the uv version used by CI.
Use those files when changing versions rather than adding another version list.
Build tools and envtest binaries are installed under the ignored `bin/` directory.

CI runs on pull request creation and updates, and on pushes to `main`, including
merges. A manual `workflow_dispatch` always runs full software checks. Pushes to
other branches and tags do not trigger this workflow; version
tags run the separate [distribution release workflow](../.github/workflows/release-image.yaml).
The lightweight change/documentation job always runs. Changes limited to prose
in README, AGENTS or Markdown under `notes/` skip Go, envtest, Docker and cluster
jobs. Changed fenced/indented examples, other paths and unavailable history run the full
software matrix. The existing `contracts` check remains active and propagates
documentation/check-selection failures, including on prose-only changes.
Controller Docker builds run in the native `cluster` and `helm` jobs; `contracts`
checks Go. Picsum renderer development, image builds and tests are maintained in
[arprt-renderers](https://github.com/dayflower/arprt-renderers).
A new run cancels any older in-progress run of the same workflow for the same
pull request or branch.

```sh
make build
make verify
```

`make test` runs the race suite both with the default two-bit gray4 build and
with the temporary `gray4_native4` build tag.

`make verify` regenerates deepcopy/CRDs and chart copies, checks their diff and
module tidiness, checks release guards/digest wiring and both charts (including
dependencies, templates and packages), then runs Go/Python lint and formatting
checks, all race tests and the controller build. It does not run API integration
or cluster tests. For API changes, edit types/validation
markers in [types.go](../api/v1alpha1/types.go), then run `make manifests` and
include both the types and generated files in the change. `make generate` only
regenerates deepcopy code.

```sh
make lint                 # Go and Python lint
make lint-go              # go vet and Staticcheck
make lint-python          # Ruff lint, including import order
make format-check         # Read-only Go and Python formatting checks
make format-check-go      # gofmt check
make format-check-python  # Ruff formatter check
make format               # Apply Go/Python formatting and Python import sorting
```

`make format-go` (also `make fmt`) and `make format-python` apply formatting for
one language. Go formatting covers `api/`, `cmd/`, `internal/` and all of `tools/`.
Go lint checks both the default build and the combined `integration,gray4_native4`
tags without starting envtest or a cluster. Ruff checks Python files throughout
the repository, including tools and example renderers, using [ruff.toml](../ruff.toml).
The first relevant target installs the pinned Staticcheck binary under `bin/`.
Ruff runs through `uv tool run` at the version pinned in the Makefile; uv manages
its isolated environment and cache. No manual virtual environment setup or global
Python packages are needed. CI installs uv with `astral-sh/setup-uv` and caches
its downloads using the Makefile as the dependency key.
Both the pull request/main workflow and release workflow run these checks through
`make verify`, subject to the prose-only CI scope described above.

The generated-file check compares against the Git index, so it also reports
intended unstaged generated changes. Review those changes and check that another
generation produces identical bytes; do not bypass unrelated failures.

Device target resolution is owned by `internal/devicestate`; its tests cover
reviewed aliases, firmware independence, override combinations, raw/effective
separation and legacy status. HTTP/Job tests exercise every profile and budget
change gates; envtest checks display admission and reconciliation after override
removal or raw observation changes. Disposable-cluster smoke preserves the OG
regression suites and adds all five profiles through GET, POST and real PVC-backed
Jobs. Integrated resource and physical acceptance have separate evidence
requirements below. Context v1 profile fixtures are under
[testdata/render-context](../testdata/render-context/README.md).

## CRD chart generation and checks

The [CRD chart](../charts/arprt-crds/README.md) is an application chart containing
only the public CRDs. API types and markers remain authoritative;
`config/crd/bases` is the canonical controller-gen output. `make manifests`
also runs [crd-chart](../tools/crd-chart/main.go) to copy those files byte-for-byte
into `charts/arprt-crds/files/crds` and remove obsolete copies. The entire
destination directory is generated. Never edit the schemas there by hand.
Repeated generation must preserve the exact file set and bytes.

```sh
make manifests
make crd-chart-check
```

`make crd-chart-check` does not regenerate files. It checks the exact embedded
file set and bytes, lints the chart, and renders default and custom release
names/namespaces. Every rendered CRD spec must semantically match the canonical
spec, including defaults, validation and subresources; CRD names and canonical
annotations must remain intact. It also checks Helm ownership metadata,
cluster scope and `helm.sh/resource-policy: keep`. `make verify` includes these
checks and the chart copies in its generated Git diff check, so existing CI
and release checks exercise them.

The Makefile selects the Helm version; [install-helm.py](../tools/install-helm.py)
downloads the official checksummed binary into ignored `bin/`. Only Helm 4 is
supported, with Helm 4.3.0 verified. Other Helm 4 versions are unverified; Helm 3
is unsupported. `HELM_VERSION` can select another pinned Helm 4 version for
development chart or integration checks.
`make integration ENVTEST_K8S_VERSION=1.36.0` also exercises the CRD chart on a
disposable API server: fresh install, a real compatible optional-field schema
upgrade, preservation of existing CR/CRD UIDs, uninstall/reinstall with the same
owner, and retention when an upgrade removes CRDs from the release. Upgrade
fixtures exist only in temporary chart copies, never in production API types.

## Controller chart assembly

The [controller chart](../charts/arprt/README.md) packages the existing namespace
Deployment and supporting resources. Its exact `arprt-crds` dependency is pinned
in `Chart.yaml` and committed `Chart.lock`, using a local source for development.
Run `make chart-dependencies` after CRD changes and before linting or packaging.
It discards old dependency archives, rebuilds from the local source and checks
that the committed lock file remains unchanged. Dependency archives under
`charts/arprt/charts/` are ignored build outputs; parent packages include them,
so consumers do not need the local source. Required public URL values must be
supplied for lint/render/install. See the chart README for commands and settings.
`make integration ENVTEST_K8S_VERSION=1.36.0` also tests controller-chart state
against a disposable API server, assembling the dependency in a temporary copy.
It verifies setup's actual credential provisioning with the chart ServiceAccount,
named auth/identity permissions and denied undeclared/cross-namespace access,
key/PVC UID retention through config upgrades and uninstall/reinstall, removal
and reintroduction of auth entries, and reuse without ownership of external
Secrets/PVCs. It uses Helm's normal release operations without forced adoption.
No keys or MACs are stored in chart fixtures or command output. Envtest has no
kubelet or storage provisioner; it cannot verify mounted data, readiness or HTTP
delivery. The cluster lifecycle checks below cover those boundaries. OCI
publication and anonymous acceptance are gated by the
[distribution release workflow](#chart-release-preparation); local checks alone
do not establish public availability.

## Helm chart checks

```sh
make chart-check
make chart-package
make helm-smoke IMAGE=arprt:dev LOAD_IMAGE=true
```

`chart-lint` assembles the dependency and strictly lints both charts with a valid
public URL. `chart-package` assembles and writes both packages under ignored
`dist/charts` (override with `CHART_OUTPUT`). `chart-check` combines these with the
read-only CRD source/render checks and [chart-check](../tools/chart-check/main.go).
`dist/` holds distribution archives; `bin/` holds executables and development
tools. Local installation uses `./charts/arprt` after building its locked
dependency; packaging is for distribution and archive checks.
The render matrix covers default/custom release names and namespaces, bundled
and separate CRDs, created/existing PVCs and auth Secrets, and Jobs on/off. It
checks namespace/name/selector references, named Secret permissions, Pod security,
filesystem RWO storage and runtime defaults against explicit contracts in the
checker, independently of installation templates; invalid values must
fail with a relevant diagnostic. Non-default settings check tag/digest precedence,
metrics, ConfigMap wiring and rollout checksums. Archive-only renders from a
temporary directory prove that parent packages contain the dependency without
requiring sibling chart sources. These checks are part of `make verify`.
Do not run dependency-writing targets concurrently in the same checkout.

[helm-smoke.py](../tools/helm-smoke.py) owns a dedicated kind cluster and temporary
kubeconfig, isolates Helm configuration and deletes the cluster on success,
exceptions or termination. It installs packages without preinstalling CRDs,
checks readiness through normal Helm waiting, then tests the bundled/default and
separate/custom-release paths. It reuses the HTTP example by creating a ConfigMap
from its Python source and applying its Deployment/Service manifest directly in
the test namespace. It also reuses the PNG
fixture and smoke protocol/port-forward helpers. Real Jobs check the claim
reference, mounted context/output and same-node execution. Config upgrades and
same-owner reinstalls must preserve CR/CRD/PVC UIDs, generated auth, registered
identity and authenticated fetching of a previously issued URL. Reinstall also
exercises explicit existing storage, external auth and Jobs disabled before
resuming normal management.
Separately owned CRDs must remain unchanged, including their resource versions,
through controller operations;
their uninstall retains CRs. Named, undeclared and cross-namespace RBAC are probed.

A temporary parent package adds an optional playlist field only to its embedded
CRD schema. The old schema must reject it with strict validation; a parent upgrade
must accept and persist it while preserving existing resources. The dedicated
cluster removes this fixture before the next scenario. All five software smoke
suites still refuse mismatched schemas and never see this fixture. No production
API field or install-order hook is introduced.

PR CI runs chart checks, API integration, Helm lifecycle and all five
smoke suites with the pinned Helm 4 client on both native architectures.
`helm-smoke` runs bundled/separate lifecycle scenarios first, removes their schema fixtures,
then prepares a shared CRD-chart release and runs the five isolated suites.
`helm-published-smoke` runs the same complete matrix using only downloaded packages.
Image releases also gate publication on this matrix on native amd64/arm64 runners,
using the exact archived image that passed the initial native smoke matrix.
Local software checks do not establish physical-device or additional storage
support, cross-major Helm release migration, downgrade safety or ownership transfer.

Once public OCI charts exist, run anonymous retrieval and the same complete
lifecycle/five-suite matrix without local chart sources or preloaded controller images:

```sh
make helm-published-smoke \
  OCI_CHART_REPOSITORY=oci://ghcr.io/dayflower/charts \
  CHART_VERSION=<parent-version> CRD_CHART_VERSION=<crd-version> \
  IMAGE=ghcr.io/dayflower/arprt@sha256:<published-index-digest>
```

Use the actual published repository/version/index digest; the repository above is
an example, not an available distribution endpoint. This target uses empty Helm
registry and Docker configurations, pulls both chart packages, and never falls
back to local charts. Run it with the pinned Helm 4 client when accepting
publication. `CRD_CHART_VERSION` defaults to `CHART_VERSION` when omitted.
With an accepted `distribution.json`, set `DISTRIBUTION_RECORD=/path/to/distribution.json`
instead of both version variables to pull the recorded chart manifest digests.
Until that succeeds, local packaging/lifecycle evidence is not public-artifact
acceptance.

## Local chart installation

End users install the published GHCR chart using the
[README quickstart](../README.md#published-chart-installation). Use this section
when developing from a checkout or testing an image you built yourself. Run the
commands from the repository root with the verified Helm client on `PATH`.
The namespace, PVC and credential requirements in
[OPERATIONS](OPERATIONS.md#helm-controller-state) also apply.

Prepare the controller chart's locked CRD dependency:

```sh
helm dependency build ./charts/arprt --skip-refresh
```

Create the private values file described in the
[quickstart](../README.md#published-chart-installation), then install the local chart:

```sh
helm install home ./charts/arprt \
  --namespace arprt --create-namespace --wait \
  --values /path/to/private-values.yaml
```

The local chart defaults to the accepted public v0.1.0 multi-platform controller
image index. Confirm compatibility with this checkout's API and schemas before
selecting another image. For a compatible published image, use its release's
`image-digests.json` to select the repository and index digest. Set `image.repository` and `image.digest` in private values.
A digest takes precedence over `image.tag`; clear it explicitly to select a tag.

To build and install your own controller image, use a registry reachable by the
cluster, replacing the synthetic repository below:

```sh
make docker-build IMAGE=registry.example.com/arprt:dev
docker push registry.example.com/arprt:dev
```

Set these image values before installing or upgrading the local chart:

```yaml
image:
  repository: registry.example.com/arprt
  tag: dev
  digest: ""
```

Configure `imagePullSecrets` for a private registry; keep credentials outside the
repository. Use a new tag or digest for each image revision. For disposable kind
checks, `make docker-build IMAGE=arprt:dev` followed by
`make helm-smoke IMAGE=arprt:dev LOAD_IMAGE=true` loads the local image without
pushing it. This wrapper owns its cluster and kubeconfig.

With local sources, use `./charts/arprt` for controller upgrades/reinstalls and
`./charts/arprt-crds` for separate CRD operations in place of the OCI references
in [OPERATIONS](OPERATIONS.md#compatible-upgrades); omit `--version` because the
local chart metadata selects the version. Rebuild the locked dependency after
CRD changes. All compatibility, backup and retained-resource requirements remain
unchanged. After installation, follow the
[device pairing steps](../README.md#pairing-a-device).

## Local controller

After CRD installation, build the controller and use a local artifact directory
with your development cluster's kubeconfig. The watched namespace, devices,
Secrets and HTTP renderer must already exist; follow the README setup or use a
disposable development environment.

```sh
make build
mkdir -p /tmp/arprt-data
bin/arprt --namespace=arprt --artifact-root=/tmp/arprt-data \
  --public-base-url=http://localhost:8080/arprt
```

Omit `--job-pvc` outside a Pod for HTTP-only execution. Job screens report
`JobSchedulingDisabled`; artifact maintenance still runs. Runtime options and
listener/credential settings are in [OPERATIONS](OPERATIONS.md#runtime-configuration).

### Temporary gray4 encoding switch

Normal builds temporarily generate two-bit gray4 PNGs, retaining all four
shades while accepting the older firmware's color-count heuristic. The
`gray4_native4` Go build tag retains the intended native four-bit path for
intermediate grays and the corresponding stricter passthrough rules. Use it
only with corrected firmware during acceptance:

```sh
make build GO_BUILD_TAGS=gray4_native4
make docker-build IMAGE=arprt:gray4-native4 GO_BUILD_TAGS=gray4_native4
```

The switch applies controller-wide to normalization, output validation and
final overlays; it does not affect gray16 or Spectra 6. Both build variants
have different encoder identities, so freshness, replay and derived caches
cannot reuse the other mode's semantics. Issued URLs retain their original
bytes. Startup logs include the active image encoder identity. There is no
runtime toggle, model/version inference or public API field. Remove the switch
after corrected-firmware acceptance rather than retaining two supported modes.

`make test` covers both builds. Cluster smoke defaults to the normal two-bit
build. When explicitly testing a `gray4_native4` image, set
`ARPRT_GRAY4_NATIVE4=true` for the Python smoke harness so its independent PNG
checks and fixture expectations match the selected image. The environment
variable configures only the harness; it cannot change a controller image.

## Package responsibilities

| Location | Responsibility |
| --- | --- |
| `api/v1alpha1`, `config/crd` | Public types/markers and generated schema/deepcopy |
| `charts/arprt-crds`, `tools/crd-chart` | CRD packaging, generated schema synchronization and Helm/spec checks |
| `cmd/controller`, `charts/arprt` | Runtime wiring, listeners, namespace scope, deployment and RBAC |
| `internal/controller` | Resource readiness, reference watches and optimistic status reconciliation |
| `internal/assignment` | Ordered eligible sets, fingerprints and screen selection |
| `internal/byos` | Routes, protocol parsing, authentication gates, display/replay and image serving |
| `internal/dashboard` | Read-only namespaced device API, independent status projections, recorded-delivery previews, embedded English UI and manager-owned operator listener |
| `internal/devicestate` | Request observations, target resolution, status and render-context snapshots |
| `internal/credentials`, `internal/deviceidentity` | Named auth Secret access/provisioning and durable MAC registration |
| `internal/screen` | Effective defaults and normalized render-input revision |
| `internal/imageprocessing` | Shared bounded PNG/APNG decoding, normalization/dithering, admission, output compatibility, canonical palettes and 2/4-bit encoding; one runtime admission lease shared across reception, collection, overlays and buffered serving |
| `internal/overlay` | Vendored CC0 Home Video text, original icons, current-observation levels/charging, inset foreground/edge background and palette-aware composition through shared encoding after renderer normalization; legacy persisted data remains readable |
| `internal/render`, `internal/delivery` | Context/PNG/HTTP contracts and GET/POST execution/publication policies |
| `internal/jobrender` | Durable pair/attempt records, scheduling, Job lifecycle, recovery and collection |
| `internal/artifact`, `internal/maintenance` | PVC publication, identity/issuance, best-effort delivery observations, capacity and assignment-aware retention |
| `internal/observe` | Bounded metrics and outcome reporting |
| `internal/integration` | Envtest API validation and running-manager integration |
| `examples/custom-resources`, `examples/renderers`, `examples/networking` | API usage patterns, runnable renderers and external routing integrations |
| `testdata/protocol`, `testdata/render-context`, `testdata/png` | Shared synthetic BYOS, Context and image fixtures |
| `tools` | Generation, chart checks, CI/release helpers and cluster smoke checks |

Keep protocol/context models independent of Kubernetes client objects. HTTP
delivery and Job collection share PNG validation and artifact ownership rules.
Use existing package boundaries rather than introducing speculative backends.

The image-processing design has standalone
[firmware and resource probes](../tools/image-investigation/README.md), with
pinned public source checksums, actual PNGdec callback execution, Go decoder
behavior and numerical references. They are research tools and do not alter the
controller's current image contract. Run them when changing the processing
design or pinned decoder/toolchain; remeasure resources under the chart CPU
limit and distinguish prototype evidence from integrated deployment acceptance.

The normalization implementation has maximum-input/output allocation benchmarks,
separate from the investigation prototype:

```sh
go test ./internal/imageprocessing -run '^$' -bench Maximum -benchtime=1x -benchmem
```

`BenchmarkQuantizeMaximum` covers a 16-bit maximum-area source and the
4194304-pixel gray16 output with each dithering method. It reports processing
time and temporary allocations, with source allocation outside the timer and
reception/PNG decoding/encoding excluded. `BenchmarkNormalizeMaximum` streams
a synthetic maximum-area RGBA16 Adam7 fixture during setup, then measures
admission, capped reception, complete-file validation, decoding, white
composition, contain/Atkinson and encoding together. It enforces the processing
deadline; a slow environment may correctly reach that deadline. Allocation
counts are cumulative bytes, not peak live memory. Both exclude informer caches
and image-serving traffic, so they do not establish the integrated controller's
memory/latency budget. Admission tests also check contention, cancellation and
slot retention. Re-run inside the deployment CPU/memory limits
with a 384 MiB Go memory limit before drawing resource conclusions.

## Delayed display transport probe

The [listener tests](../internal/byos/listener_test.go) use actual TCP sockets
and real elapsed time to deliver HTTP 200 / JSON 202 after 10 and 13 seconds,
and reject a write after 16 seconds with the restored 15-second write timeout.
They also exercise ordinary authentication and pending responses, client
disconnect cancellation and the current 13-second application deadline.
These tests run in `make test`; the timed listener cases
alone need at least 16 seconds per build variant. They establish local listener
behavior, not device acceptance.

The isolated [byos-delay harness](../tools/byos-delay/main.go) uses the production
listener with its 15-second write timeout. It delays only the configured exact
display path, sends no headers before the delay and defaults to a synthetic
HTTP 200 / JSON 202 response. It has no pairing or authentication in this mode;
it is a transport probe, not a BYOS server. Start it locally:

```sh
go run ./tools/byos-delay -delay=10s
```

In another terminal:

```sh
curl --max-time 20 --write-out '\nhttp=%{http_code} first_byte=%{time_starttransfer} total=%{time_total}\n' \
  http://127.0.0.1:8081/arprt/devices/arprt/delay-probe/api/display
```

Use separate harness instances with `-delay=10s` and `-delay=12s` for delivery
below the 15-second write limit. The default delay is 10 seconds. Delays of 16
or 25 seconds remain available only as negative probes; the restored production
listener cannot deliver a response written after its 15-second deadline. They
do not reproduce the former 30-second listener's successful long responses.
A shorter client `--max-time` can exercise cancellation, but does not emulate
firmware retries or TLS. The harness limits active requests to 16 and bounds
delay plus upstream work to 28 seconds, independently of the socket deadline,
so negative probes can observe a device's disconnect before attempting a write.
That probe-only bound is not a production request or on-demand Job budget:
ordinary BYOS requests retain the 13-second context. Client disconnect and
process shutdown also cancel the probe work.
Its logs contain elapsed delays and cancellation, without request headers,
device identifiers or upstream addresses. A finished delay log does not prove
delivery; record the client result and proxy timings as well.

For actual TLS/proxy/prefix and device checks, use an isolated route and a
disposable BYOS controller containing only the intended test device. Run the
harness with `-listen=0.0.0.0:8081` only inside that isolated environment and
`-upstream=http://127.0.0.1:8080` (replace with its private controller origin).
Set `-display-path` to the exact incoming path after any prefix stripping; the
upstream receives that same path. Configure the disposable controller's
`publicBaseURL` and prefix handling to match the actual external device URL.
Route that external URL through the usual TLS proxy to the harness. In upstream
mode, setup, image downloads and other paths pass through without delay, and
the controller handles real authentication and JSON/image generation. The
synthetic response mode cannot validate authentication or image display.

For a short observation session, upstream mode also accepts
`-refresh-seconds=5`: it changes only `refresh_rate` in successful display JSON
on the exact probe path. Setup, pending, authentication errors and image bytes
are unchanged. This allows repeated device polls without changing the
controller's global configuration or other devices. Remove the probe and
observe another normal successful poll to restore the controller's original
refresh interval on the device. Keep this override confined to the test device.

Before observing the device, prepare a valid cached image on the disposable
controller so renderer execution does not obscure the transport delay. Compare
no delay and the revised 10/13-second boundaries, including wrong-token rejection
and ordinary authenticated image fetches. If disconnection occurs, locate it
using private device/proxy/harness timings and count actual display retries; record whether
the proxy buffers responses or terminates a request first. Inspect the existing
proxy timeout before overriding it, and report the original and extended-timeout
results separately. Prepare each delay in a separate Pod/Service, then switch
only the isolated route's backend so in-flight requests can finish. Exclude
rollout/shutdown interruptions from timing evidence. Do not advance the
device's display turn with synthetic requests during observation. Restore the
original device URL and temporary routing/settings afterward, including on
failure or interruption.

Keep source revision/image, model, exact firmware/distribution, transport band,
proxy configuration, date, elapsed times and retry counts privately, excluding
credentials and real identities from repository fixtures. Separate source review,
direct HTTP tests, local TLS tests and actual device observations. The reviewed
Wi-Fi sources have a 15-second read timeout; see
[PROTOCOL](PROTOCOL.md#transport-deadlines). Neither the listener limit nor
the harness establishes that an installed device can wait 25 seconds. The
observation below rejects that wait on the tested OG; other installed firmware,
models and additional external prefixes remain unconfirmed. The revised
10-second render / 13-second request design still needs end-to-end acceptance
with real Jobs and finalization.

### Delayed display transport observations

On 2026-10-29 (JST), the existing TRMNL OG reporting firmware `1.8.17` was tested
through its actual TLS/Envoy Gateway device path on native Linux amd64 /
microk8s Kubernetes 1.36.2. The reported version alone does not establish the
installed binary's commit/hash or distribution. The path had no extra external
prefix beyond the canonical device path. The worktree delay harness used the
experimental 30-second listener configuration; it forwarded to the existing
controller without changing its image, assignment, credentials or PVC. Private
evidence retains source/image details and timings without adding real identities or hostnames to
repository fixtures.

| Evidence | Result |
| --- | --- |
| Experimental 30-second listener, synthetic HTTP display response | 16- and 25-second delays delivered; a write after 31 seconds failed. This configuration was subsequently reverted. |
| Actual TLS/Gateway path, synthetic 25-second response, original proxy timeout | HTTP 504 after about 15.08 seconds; Gateway logged `response_timeout` / `UT`. |
| Same synthetic probe, only its temporary route extended to 35 seconds | HTTP 200 / JSON 202 received after about 25.03 seconds. |
| Actual OG, 14-second injected delay, temporary 35-second Gateway timeout | HTTP 200 after 14.260 seconds, followed by a successful authenticated image fetch. |
| Actual OG, 16-second delay, same extended Gateway timeout | Three attempts ended in client disconnect after 15.003–15.021 seconds. |
| Actual OG, 25-second delay, same extended Gateway timeout | Two further attempts ended in client disconnect after 14.964–15.037 seconds. |

The five consecutive failed attempts started about 17.4–17.5 seconds apart,
leaving about 2.4–2.5 seconds after each disconnect before the next start. These
are observed request intervals including firmware/connection work, not an exact
measurement of its configured 2000 ms retry sleep. Gateway logs identified
`downstream_remote_disconnect` / `DC`, with no response bytes, and the harness
canceled its delay before forwarding to the controller. The batch crossed from
the 16-second backend to the already-running 25-second backend; it does not
independently establish five attempts per delay. A separate rollout-interrupted
request was excluded. Physical communication and server/image-fetch evidence
are distinct from visual panel acceptance.

The original Gateway limit and the device read timeout are independent barriers.
Extending the Gateway alone did not make this OG wait 25 seconds, and a
15-second candidate budget leaves no assured margin for the rest of a display
request. The experimental listener extension was reverted to 15 seconds, and
the revised on-demand budgets and finalization reserve are described in
[PROTOCOL](PROTOCOL.md#transport-deadlines). The implemented software limits still need
real cold/warm Job, finalization and response-delivery acceptance; they are not
delivery guarantees. The observations do not
expand model, firmware or long-wait support claims.

The temporary routes, Deployments and Services were removed afterward. Snapshot
comparison confirmed the original route, controller, ConfigMap, Device and
Screen configuration/UIDs and the credential Secret/PVC UIDs were retained.
The normal path then delivered another display response and authenticated image
fetch with HTTP 200, and the observer confirmed the ordinary image returned.
The original controller refresh interval remained 300 seconds; the temporary
five-second override ended with the normal successful response.

## Selecting checks

Start with focused tests for changed behavior, then run the applicable matrix.
Test externally observable guarantees, especially concurrency, restart, UID
replacement and failure preservation; avoid tests that merely repeat code.

| Change | Additional checks beyond `make verify` |
| --- | --- |
| API markers/defaults, status, watches, Secret conflicts or reconciliation | Envtest on the CI Kubernetes version (1.36.0) |
| CRD chart generation/templates/lifecycle | `make crd-chart-check` and envtest on 1.36.0 |
| BYOS/HTTP/credentials/identity | `cluster-smoke.py` for cluster/deployment integration |
| Job scheduling, mounts, collection, recovery or cancellation | `job-smoke.py` |
| Playlist/discovery, replay or per-screen retention | `playlist-smoke.py`, plus HTTP/Job regressions when affected |
| Capacity, corruption, GC, metrics, logs or distribution | `operations-smoke.py` |
| Image processing, profiles, palettes or resource admission | `image-smoke.py`, plus the existing HTTP/Job/playlist/operations suites |
| Docker/manifests/examples | Docker build and relevant Helm/cluster checks |
| Release | Full software/native/anonymous distribution matrix; no physical-device tests |

```sh
make integration ENVTEST_K8S_VERSION=1.36.0
make docker-build IMAGE=arprt:dev
```

Envtest starts disposable API servers and uses the integration build tag; it
does not use the configured cluster. It checks actual admission/defaulting,
strict unknown-field rejection, status conflicts, watches and manager restart.
It simulates renderer termination; actual container execution and mounts require
cluster checks. Use `kubectl --validate=strict` or `fieldValidation=Strict` when
testing unknown fields: structural CRDs otherwise prune them before CEL runs.
See Kubernetes [field validation](https://kubernetes.io/docs/reference/using-api/api-concepts/#field-validation)
and [CRD pruning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/).

The internal request-triggered Job foundation is covered by
`internal/jobrender/execution_test.go`: shared waiters and immutable snapshots,
absolute render/candidate budgets including slot/storage waiting, cancellation,
late completion, stopping-Pod capacity/GC protection, mode changes and restart.
Interruption during an API read must persist `RequestCancelled`/`JobTimedOut`
instead of the generic execution-failure reason, including when cancellation
arrives after the worker's initial context check.
Existing scheduler tests retain legacy-record, interval, backoff, processing and
publication-recovery coverage. `TestJobExecutionWatchCancellationAndRecovery`
uses public OnDemand Screens with a real envtest API server to exercise
status/watch publication, foreground deletion and restart. It emulates Pod
termination and explicitly clears foreground-deletion finalizers because envtest
has no kubelet or garbage collector. Its display subscenario exercises the
authenticated BYOS route, current-observation Context, completed Job publication,
issuance, independent regeneration and filename reuse. API tests cover integer
1–10s admission, conditional omissions, explicit defaults and existing-CR updates.
`internal/byos/ondemand_test.go` covers shared single-screen waiters, independent
cancellation, immutable observations, compatible stale fallback, infrastructure
errors, authorization/UID/configuration gates and the 10s render/finalization
boundary. This is not acceptance of real container startup, PVC mounts or
physical-device delivery.

`internal/byos/skip_test.go` covers bounded traversal, wraparound, all-failed
turn retention, one-entry behavior, mixed-source freshness, shared issuance and
turns, independent/all-waiter cancellation, restart replay, policy edits,
authentication/configuration barriers and admission-based HTTP/Job cutoffs.
It exercises default 5s and explicit 10s first candidates under the default 10s
playlist budget. Playlist envtest admission rejects invalid enums/combinations,
fractions and out-of-range values including 11s/15s/25s; omitted/explicit defaults
and existing-CR updates remain compatible. The Job watch suite also exercises
failed A followed by completed B, concurrent display requests and restart replay
against real API status/watch updates. Real-container/Helm and physical evidence
are collected separately below; envtest does not establish Pod startup or
end-to-end delivery within the budgets.

For documentation-only edits, verify local links/anchors, obsolete references
and `git diff --check`. No software matrix is required for prose-only changes.
`python3 tools/check-docs.py` checks tracked local Markdown links/anchors and
whitespace. Executable example changes still need applicable software checks.
Use the manual full run if executable instructions or their scope are uncertain.

Dashboard foundations are covered by `internal/artifact/delivery_test.go`,
`internal/byos/record_test.go` and `internal/dashboard/server_test.go`: atomic
concurrent observation ordering, restart/legacy/corrupt records, capacity and GC,
GET/POST/Scheduled/OnDemand with single/Hold/Skip/discovery, stale/overlay/replay
provenance, equal-byte independent renders, recording failure isolation, current
generation/status scope, optional telemetry, UID replacement and read side effects.
The envtest API suite also checks real bounded pagination/continuation and
namespace isolation through the dashboard handler. `internal/dashboard/ui_test.go`
also checks embedded asset routing, read-only methods and security headers.

The optional browser suite runs the embedded assets against the actual dashboard
handler, namespaced Kubernetes object fixtures and a temporary PVC repository.
Its 23-device fixture covers normal/stale/silent/never-seen/pending states,
missing and collected images, unknown provenance and optional telemetry.
`tools/dashboard-browser.cjs` checks lazy image requests, pagination, periodic
refresh, error/expired-token recovery, safe text, focus/expanded-state retention,
image enlargement and keyboard controls at desktop, tablet and phone widths.
Backend failures are injected by the browser; fixture pagination is deterministic,
while envtest covers real Kubernetes continuation behavior. This is not live
cluster, physical display or production listener/Helm acceptance. The dashboard
TCP listener tests separately cover request-context cancellation, draining,
closed sockets after shutdown and bind failures.

Install Node.js and Playwright with Chromium separately for this development
check; neither is needed to build or run the controller. For example, keeping
all JavaScript dependencies outside the checkout:

```sh
npm install --prefix /tmp/arprt-dashboard-browser playwright
/tmp/arprt-dashboard-browser/node_modules/.bin/playwright install chromium
NODE_PATH=/tmp/arprt-dashboard-browser/node_modules ARPRT_DASHBOARD_BROWSER=1 go test -count=1 -run TestBrowserUI -v ./internal/dashboard
```

Chromium also needs its platform's shared libraries. Set
`ARPRT_DASHBOARD_SCREENSHOTS` to an absolute output directory to save desktop,
mobile, enlarged-image and empty-state screenshots. The normal Go suite skips
this opt-in browser check; `make verify` continues to require no frontend build.

Documentation previews are kept in [images/dashboard-desktop.png](images/dashboard-desktop.png)
and [images/dashboard-mobile.png](images/dashboard-mobile.png). Both use the
synthetic browser fixture rather than live device data. To refresh them, run the
browser check with `ARPRT_DASHBOARD_SCREENSHOTS` set, then copy `desktop.png` and
`mobile.png` to those paths.

## Disposable cluster checks

Smoke scripts use the selected kubeconfig context, install controllers and missing
CRDs through packaged production Helm charts, create isolated namespaces and
clean up their test resources. Existing complete CRD sets are reused read-only
only when names, scope, versions, schemas and subresources match the canonical
output; partial or different schemas fail before namespace creation. No existing
resource is adopted. Each suite uses explicit named credentials and a dedicated
Helm release in its namespace, and attempts all cleanup even after a failure. Use a dedicated cluster and kubeconfig rather
than relying on the current context. The following uses the CI node image:

```sh
arprt_check_dir="$(mktemp -d)"
kind create cluster --name arprt-dev --kubeconfig "$arprt_check_dir/kubeconfig" \
  --image kindest/node@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
make docker-build IMAGE=arprt:dev
kind load docker-image arprt:dev --name arprt-dev
KUBECONFIG="$arprt_check_dir/kubeconfig" make cluster-smoke IMAGE=arprt:dev
kind delete cluster --name arprt-dev --kubeconfig "$arprt_check_dir/kubeconfig"
```

Run cluster deletion even if a check fails, then remove the temporary kubeconfig.
For a focused check, run `make chart-package` first, then use
`python3 tools/<script>.py --image arprt:dev --helm "$PWD/bin/helm-v4.3.0" --packages "$PWD/dist/charts"`
with that same `KUBECONFIG`. `make cluster-smoke` runs all five scripts. The Makefile pins
kind and the node image. The following creates its own kubeconfig and cluster,
runs all five scripts, and removes the cluster/kubeconfig on exit, including
failures:

```sh
make disposable-cluster-smoke IMAGE=arprt:dev LOAD_IMAGE=true
```

This wrapper installs the CRD chart once in its own cluster and runs two smoke suites
concurrently, starting the longest Job suite first. Each suite uses an isolated
namespace/PVC and reuses those CRDs. All suites finish cleanup, and any failure
fails the wrapper. `make cluster-smoke` remains sequential for explicitly selected
clusters; do not launch its scripts concurrently while they own CRD creation
and deletion. The parallel runner is intended only for the prepared disposable
cluster. Suite durations and failure output appear in the wrapper log.

Omit `LOAD_IMAGE=true` to test a registry image without loading a local copy.
All five scripts accept tagged images and multi-platform index digest references.

The HTTP check covers setup/key reuse, MAC modes, authentication, both proxy
prefix modes, telemetry, rotation, GET reuse, restart, stale output and initial
pending. Overlay checks include fractional telemetry, fallback, pixel preservation
independent top/bottom/left/right margins and white edge bleed, explicit charging without USB inference and original timestamps
using embedded Tokyo zoneinfo in the scratch container.
Job checks add actual containers, token absence, same-node mounts, paths,
immutable snapshots, rotation changes, interval refresh, recovery, failures and
obsolete work, plus current overlay telemetry versus immutable Job snapshots.
Playlist checks add mixed-source order, preparation before selection, retry/restart
and membership edits, shared corner cycling and unchanged replay bytes. Before
restart, pre-render checks wait for the current Screen's published `ImageReady`
condition as well as Job completion; Job completion alone can precede output
collection and durable publication. Operations checks add corruption, capacity pressure,
redaction, composite/base separation, old composite URL retention, HTTP-only GC and installation of the supplied examples.
These scripts do not configure public ingress or a physical device.

The operations suite also checks the dashboard's default-disabled and explicitly
disabled listener through failed port-forward requests, then enables it using
the packaged chart. It verifies embedded assets, namespace-scoped and redacted
device JSON, previews from GET/POST/Job delivery without device credentials,
BYOS Service/route separation, read-only observations and original composite
provenance across a controller restart. Disabling the listener preserves the
record and authenticated BYOS image retrieval. `make chart-check` covers
enabled/disabled rendering with both Job modes, claim modes and CRD modes,
including custom release/namespace names and invalid dashboard settings.

### On-demand Job acceptance

The Job and Playlist suites also run
[ondemand_smoke.py](../tools/ondemand_smoke.py) in their existing isolated
namespaces. The Job suite uses one global execution slot and checks 24 concurrent
authenticated display requests, independent regeneration with identical filename
reuse, image GET without execution, renderer failure/missing/malformed output,
compatible fallback, independent/all-waiter disconnects, an unavailable executable
in a cached image and a separate cold-image pull.
A renderer that ignores SIGTERM writes a real PNG after its deadline; the suite
checks that this output never becomes a published success.
A cold start may correctly return JSON 202 within the budget; that is a measured
timeout outcome, not successful cold-start acceptance.

A test-only Pod finalizer holds deletion while another display waits. The suite
checks same-pair exclusion and global capacity, observes a successful maintenance
pass while the stopping context and issued image remain readable, then removes
only its own finalizer in a `finally` block. Controller restart must terminate
request-dependent work without an autonomous retry. A production-chart config
upgrade must preserve CR/PVC UIDs, authentication and the previously issued URL.
The existing Scheduled scenarios retain their recovery and publication checks.

The Playlist suite checks failed A, successful B, next C, wraparound, shared
requests, replay across restart, mixed GET/POST/Scheduled/OnDemand turns, stale
rejection, one-entry/all-failed pending and unchanged successful turns on failure.
A default 5s first timeout must leave enough of the 10s budget for a fast warm
second candidate; an explicit longer first timeout must respect the remaining
overall budget. These checks intentionally fail when the disposable environment
cannot complete the required successful scenarios in time. Do not increase
production deadlines to make smoke pass.

Display timings are reported separately from render success. The harness allows
750ms of local delivery/scheduling overhead above the 13s application deadline;
this allowance does not extend the Controller's 10s render or 13s request limit.
The shared resource sampler reports kubelet memory during the 24-request burst;
the kind wrapper also checks the Controller cgroup's peak, 500m CPU/512Mi memory
limits and zero OOM events. These finite software scenarios do not establish
production throughput, every cold image's startup time or physical delivery.
Run the full `make helm-smoke IMAGE=arprt:dev LOAD_IMAGE=true` matrix after Docker
build; select an empty `CHART_OUTPUT` directory if older package versions remain.

For physical acceptance, retain the 15s listener and actual Gateway settings.
Record candidate source/image, model, exact firmware and distribution, time and
actual display/image request durations privately. Prepare single OnDemand,
failed-A/successful-B Skip, all-failed JSON 202 and budget-exhausting delayed-output
sessions. Observe real polls and replay without synthetic display requests using
the physical identity. Distinguish cold pulls from warm execution and timer wake
from manual wake; confirm the panel separately from HTTP/PNG metadata. Save and
restore assignments, polling settings and any temporary renderer/proxy changes
on success, failure or interruption. Existing normal authentication, downloads,
overlays and issued-URL retention still need their applicable regression checks.
Unobserved hardware sessions remain incomplete and do not create a release gate.

## Controller image releases

The [image release workflow](../.github/workflows/release-image.yaml) publishes
`ghcr.io/dayflower/arprt`. A release tag `vMAJOR.MINOR.PATCH` maps to controller
`--version` and OCI version `MAJOR.MINOR.PATCH`, image tag `vMAJOR.MINOR.PATCH`,
and chart `appVersion: "MAJOR.MINOR.PATCH"` when selecting that default image.
Chart package versions are tracked separately as described in
[chart release preparation](#chart-release-preparation). SemVer prereleases are allowed;
build metadata is excluded because it cannot be represented directly in image
tags. There is no moving `latest` tag. Version and platform tags are immutable
by release policy: the publisher checks existing package versions and refuses
all overwrites. Do not move/delete release Git tags or manually retag images.
Use the published image index digest for installations that support both
architectures. `--version` also records the full source commit, matching OCI
revision metadata. Development images default to version `dev`.

Before tagging, merge the intended revision into `main` and run the full software
matrix. Release preparation and publication do not include physical-device
testing, including corrected-gray4 OG firmware and E1002 checks. Neither new
hardware observations nor reuse of a physical baseline is required. Historical
observations and optional hardware procedures remain separate evidence for
[device support claims](../README.md#security-and-support-boundary); software
acceptance does not establish panel behavior.

For a release build before tagging, use its intended version:

```sh
make docker-build IMAGE=arprt:release-check VERSION=0.2.0
```

After software validation, create an annotated tag on the intended revision
(use an unused version). No physical-device attestation, firmware annotation or
acceptance date is required:

```sh
arprt_release_revision="$(git rev-parse HEAD)"
git tag -a v0.2.0 "$arprt_release_revision" -m "arprt v0.2.0"
git push origin refs/tags/v0.2.0
```

The workflow rejects lightweight tags, invalid versions, a checkout different
from the tagged commit, and commits outside `origin/main`. It explicitly runs
`make verify` and envtest on Kubernetes 1.36.0 once per architecture in `checks`,
alongside independent `validate` jobs on that exact commit on native
`ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` runners. Each `validate` job builds
and checks the image's architecture/version, runs the resource benchmark, then
runs `make chart-check helm-smoke` against that image. This combines chart
checks, the full Helm lifecycle and the five-suite matrix before archiving the
tested image; there is no separate disposable-cluster or downstream Helm run.
Publication requires both architectures of `checks` and `validate` to pass.
The five-suite matrix and full Helm lifecycle run three times per architecture:
before image publication, against the staged distribution archives, and against
the public chart digests. The latter two stages reuse the source's envtest result.
All cluster checks use fresh isolated clusters; release checks do not use an
operator's cluster. No QEMU acceptance is substituted for native execution.
A new architecture does
not expand physical-device, Kubernetes or storage support.

Docker follows [Docker's cross-compilation guidance](https://docs.docker.com/build/building/multi-platform/):
the Go builder uses `BUILDPLATFORM` and compiles with `TARGETOS`/`TARGETARCH`;
the runtime remains non-root scratch with CA certificates and embedded zoneinfo.
The workflow archives each exact tested image, then its image-publishing `packages: write`
job uploads those archives and uses Buildx to assemble a multi-platform index.
No rebuild occurs between validation and publication. The index and its two
platform digests are recorded; version/revision/license/source/unofficial-project
metadata are verified for both images. PR CI has no package-write permission.
All workflow actions are pinned to commit SHAs.

### Repository and package configuration

Enable Actions and allow the publishing job's `GITHUB_TOKEN` to write its
repository's packages. If a package already exists, link it to this repository
and grant the repository Actions access. The OCI source label also links the
image. Keep the tag creation permission restricted to release operators;
`main` is the intended release branch.

GHCR creates the first package as private, even for a public repository. In the
[package settings](https://github.com/users/dayflower/packages/container/arprt/settings),
set its visibility to **public**. Do the same for `charts/arprt-crds` and
`charts/arprt` after their first uploads, and grant this repository Actions
access to both chart packages. See [GitHub's container registry guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
This setting requires a package owner and cannot be replaced by Docker login.
No private registry credentials are needed by consumers.

After uploading, the workflow reads the index/configuration anonymously,
checks both platform digests, pulls the same version tag on both native runners,
and reruns the complete cluster matrix with `ghcr.io/dayflower/arprt@sha256:...`.
The clusters pull directly without preloaded controller images or pull Secrets,
covering startup/readiness, setup, authenticated delivery, Job execution, replay,
and operations checks through the exact staged chart packages with the pinned
Helm 4 client. After chart publication, both native runners anonymously pull and
compare both charts with those staged packages, then repeat the complete matrix
using public chart manifest digests and the image index digest. These public
runs exercise the chart's default image settings, fresh installs and compatible
configuration upgrades preserving CR UIDs, identity, keys, PVCs and issued URLs.
The temporary schema-upgrade fixture remains a separate harness check; it is
never published. A first release has no earlier public chart version; config
upgrades use the same published package. Cross-version acceptance must also
follow the compatible-upgrade procedure before recommending a later transition.
If an anonymous check fails before visibility is changed, set all three packages
public and rerun acceptance only:

```sh
gh workflow run release-image.yaml --ref v0.2.0 -f tag=v0.2.0 \
  -f digest=sha256:<published-index-digest>
```

Take the index digest from the original publishing job summary or
`published-image-index` artifact, not from an unverified new upload. This manual
path validates the annotated tag again, reruns all software/native checks,
recreates the expected chart contents from that commit and digest, runs the
staged/public matrices, and never writes
packages. Only the two publication jobs may be skipped on this path; failed or
cancelled prerequisites block acceptance. Both chart versions must already have
been uploaded. Select the matching Git tag with `--ref` so the workflow and
helper paths belong to that
release, including releases before directory reorganizations. Do not rerun
the publisher: existing version/platform tags are rejected. An interrupted
partial upload requires investigation and a new version; do not overwrite the
partial version.

Only after both native public acceptance jobs pass does a separate
`contents: write` job create the GitHub release with permanent
`distribution.json`, `image-digests.json`, and an acceptance workflow link. The distribution record
includes the image version/index/platform digests, both chart versions/manifest
digests, archive hashes and the exact bundled dependency version. Before that point the
registry upload alone is not a completed release. Workflow artifacts retain the
index/acceptance records for 90 days; GitHub release assets retain the final
record. For compatible upgrades use a tested matching release chart/image;
existing runtime, data-format and support limits remain authoritative.

## Chart release preparation

The [distribution workflow](../.github/workflows/release-image.yaml) publishes
the image first, then `oci://ghcr.io/<owner>/charts/arprt-crds` and
`oci://ghcr.io/<owner>/charts/arprt`. Public distribution is complete only when
both native anonymous chart acceptance jobs succeed and the release includes
`distribution.json`. Until such a release exists, use the local installation
path; the OCI namespace alone is not evidence of availability.

The release inputs have distinct owners:

| Input | Source of truth |
| --- | --- |
| Controller version and image tag | Annotated image release tag and the image workflow |
| Image index and platform digests | Accepted release's `image-digests.json` |
| Controller chart version and `appVersion` | `charts/arprt/Chart.yaml` |
| Local default controller image | `charts/arprt/values.yaml`; index digest takes precedence over tag |
| Published default controller image | Release tag/version and the validated index digest, resolved in a temporary chart copy |
| CRD chart version and metadata | `charts/arprt-crds/Chart.yaml` |
| Exact bundled CRD version/source | Parent `Chart.yaml` and committed `Chart.lock` |
| Public CRD schemas | API types/markers, generated canonical YAML and synchronized chart files |

Chart versions identify packages; the parent's `appVersion` must match the
controller release tag without `v` and does not select the image by itself.
The guard validates this and the exact standalone/bundled CRD version match
before release work starts. CRD `appVersion` describes its original schema
combination and need not change for a controller-only update. Changing an image
tag without updating the default digest does not change the deployed image.
Do not assume that a chart version, image version and dependency version will
always be identical. A controller-only change need not change the CRD chart's
version when its package is unchanged.

For a stable controller release, run the shell helper directly from the checkout:

```sh
tools/bump-version.sh --dry-run patch
tools/bump-version.sh patch
```

Choose `major`, `minor` or `patch`. The helper bumps the parent chart's top-level
`version` and `appVersion` independently, resetting lower components for major
and minor bumps. For example, a pair of `0.2.0` versions becomes `0.2.1` with
`patch`; unequal chart/controller versions remain independent. It preserves
quoting, inline comments and file permissions. CRD metadata, the dependency lock
and accepted local image defaults retain their existing versions. The helper
requires Bash and awk, plus diff for `--dry-run`; no Make, Helm, Python or YAML
package is needed. Prerelease/build metadata and malformed or duplicate fields
are rejected before replacing the file. Edit prerelease versions explicitly.
Review and commit the resulting diff through a PR before creating a release tag.

For a candidate package, regenerate schemas with `make manifests` when API
types/markers change. Review both generated destinations and verify deterministic
regeneration. When intentionally changing a dependency version, update the exact
parent dependency and regenerate/review `Chart.lock` with the pinned Helm client;
normal `make chart-dependencies` rebuilds archives while requiring that lock to
stay unchanged. Run the chart checks, API integration, Docker build and full
Helm lifecycle/smoke matrix described above against the intended image.
Physical-device tests are outside release preparation and publication.
`make chart-package`
(also included by `chart-check` and `helm-smoke`) writes the standalone CRD archive
and parent archive containing its pinned dependency. Keep the exact archives
exercised by the checks for distribution.

[release-charts.py](../tools/release-charts.py) assembles distribution packages
in a temporary copy after anonymous image validation. It resolves the parent's
default image repository/tag/index digest to this release, keeps the committed
lock unchanged, strictly lints and packages both charts. The checkout's local
defaults are unchanged. Preparation also concatenates the sorted generated files
under `config/crd/bases/` into `crds.yaml`, without adding Helm ownership metadata,
and writes `crds.yaml.sha256`. It rejects missing, extra or different schema bytes
in either the standalone or embedded CRD chart. The exact archives and YAML assets
are retained as `release-charts` for 90 days. Native amd64/arm64 `chart_validate`
jobs run chart checks and the full lifecycle/five-suite matrix through
`make helm-release-smoke CHART_OUTPUT=/path/to/staged/packages IMAGE=... USE_CHART_IMAGE=true`;
this target does not rebuild the archives or repeat envtest from `checks`.
It uses the staged image defaults and checks that the deployed image equals the
accepted index digest. Only after these jobs pass does the chart publisher
receive `packages: write` permission. The image publisher has
that permission separately; validation and PR jobs cannot publish.

The publisher refuses an existing parent version. For an existing CRD version,
it downloads the package and compares every file byte-for-byte with the candidate,
ignoring archive metadata such as timestamps/ownership. It reuses an identical package and
rejects any content change; a changed README also requires a new CRD chart
version. Otherwise it publishes CRDs first, then the parent including its locked
dependency. Consumers need no local dependency source or dependency resolution.
Release workflow concurrency serializes version checks and pushes across tags.
Never delete/retag an existing package to bypass these guards. An interrupted
parent upload needs a new parent version; an identical completed CRD upload can
be reused. If image publication already completed but chart upload failed, use
a new controller release tag as well; manual dispatch performs acceptance only.

Public verification uses empty Helm/Docker configuration, compares the downloaded
packages' complete contents with the validated archives, and records OCI manifest
digests plus downloaded archive hashes in `distribution.json`. It regenerates the
bundle from the checked source after comparing both public chart copies, checks
the staged YAML and checksum, and records the bundle SHA-256 and CRD chart version
in `distribution.json`. The subsequent full lifecycle/five-suite matrix pulls
those chart digests with no local-source
fallback, checks their recorded archive hashes, and normalizes Helm's digest-based
filenames for the existing suite helpers. The lifecycle matrix also applies the
raw YAML with server-side apply, installs the controller with `crds.enabled: false`,
and checks that upgrades, uninstall and reinstall preserve externally managed
CRDs and application state. Both architectures must report identical distribution
records, YAML and checksum files before the announcement job creates a GitHub
release with `crds.yaml` and `crds.yaml.sha256` assets at
`releases/download/<controller-tag>/`. Older releases may not provide these assets.
Registry uploads and software acceptance do not attest physical-device behavior.
Release notes describe the completed software checks and explicitly state that
the workflow does not attest physical-device checks.

## Integrated image acceptance

[image-smoke.py](../tools/image-smoke.py) installs the packaged controller with
its actual 500m CPU / 512 MiB limits and 384 MiB Go memory limit. It exercises
OG and E1001–E1004 profile defaults without firmware/size headers, RGB input
normalization through GET/POST/Job, exact palette samples, final overlay pixels,
indexed Spectra byte preservation across policy edits, exclusive output budgets
and authenticated retrieval of issued images after restart. The original four
suites retain OG regression, playlist recovery and storage/GC coverage.

Its standard load case starts two distinct HTTP requests (GET and POST) and a
real PVC Job using 32 MiB, 4096-square RGBA16 inputs in plain and Adam7 layouts.
This reduces routine CI contention while retaining maximum input/output sizes
and the production resource limits. The original eight HTTP callers plus one
Job remain available with `--stress` on `image-smoke.py`, or by setting
`ARPRT_IMAGE_STRESS=true` on a disposable cluster/Helm smoke command. For example:

```sh
ARPRT_IMAGE_STRESS=true make disposable-cluster-smoke IMAGE=arprt:dev LOAD_IMAGE=true
```

Stress mode is optional; a standard pass does not establish nine-caller
acceptance. The report records `load_profile` and the actual caller count.
Both modes check bounded
success/pending outcomes and HTTP 503 overload when the enclosing 13-second
request deadline expires, collection completion, post-load availability and
controller restarts. Request overload and processing failures are recorded separately; it is not successful
conversion or a latency/throughput guarantee. The fixture has compressible pixels
and ancillary padding; it does not model arbitrary high-entropy photographs.
The package benchmarks above independently cover maximum output allocation and
all four dithering policies. Run `make image-benchmark` to compile those production
benchmarks and execute them in a fresh 500m CPU / 512 MiB cgroup with
`GOMEMLIMIT=384MiB`. It requires Docker/cgroup v2, uses `python:3.13-alpine` only
for its shell and writes the raw timings, peak/OOM counters, binary hash,
architecture, Go version and CPU model to ignored
`dist/acceptance/image-benchmark.json`. Timed processing failures or OOM fail the
command. Source generation and the Go test runner are included in its cgroup
peak; quantization sub-benchmarks exclude reception/decode/encode from their
reported timings. Rerun when accepting an architecture or processing change.

Each smoke run prints an `IMAGE_ACCEPTANCE` JSON record containing per-profile
elapsed times (including Kubernetes scheduling/collection), byte sizes, load results and
sampled kubelet memory usage. These elapsed times are not isolated processing
times. The disposable kind and Helm wrappers additionally enable
`ARPRT_KIND_RESOURCE_PROBE=true`: the script reads only the controller container's
cgroup v2 `memory.peak`, `memory.events`, `memory.max` and `cpu.max` through the
owned kind Docker node. It requires a peak below 512 MiB, zero OOM/OOM-kill events
and unchanged controller restart count. Direct runs on other disposable clusters
report sampled memory only, which cannot establish peak memory. Keep raw records
as CI/run artifacts (set `ARPRT_IMAGE_ACCEPTANCE_OUTPUT` to save the smoke JSON)
with the source revision, image digest, architecture, CPU model and node image; do not commit private cluster metadata or task logs.

CI and image acceptance run the five-suite matrix on native linux/amd64 and
linux/arm64. A local amd64 run or an emulated arm64 build does not attest native
arm64 performance. Source/host decoder probes, software cluster checks and
physical display observations are distinct acceptance layers. Preserve native
architecture/resource checks for processing changes and releases, with their raw
CI/run artifacts. Current requirements and rationale are maintained in
[PROTOCOL](PROTOCOL.md#png-contract), [DESIGN](DESIGN.md#image-processing),
[RENDERERS](RENDERERS.md#choosing-a-source),
[OVERLAYS](OVERLAYS.md#image-and-artifact-guarantees) and
[OPERATIONS](OPERATIONS.md#image-processing-resources). Implementation is complete;
the remaining physical checks below are tracked independently of documentation
integration and do not expand the verified support claim until accepted.

### Image-processing physical observations

On 2026-10-17 (JST), reTerminal E1002 with firmware 1.8.17 installed through
the TRMNL official Web Flasher completed actual setup and authenticated polling.
The observer confirmed six vertical bands in black, white, yellow, red, blue and
green. The issued PNG was 800 × 480, indexed four-bit, with the six exact palette
entries and 968 encoded bytes under an explicit 90000-byte budget. Authenticated
fetches retained the original OG and E1002 issued bytes and both keys after
controller restart; Device, CRD, Secret and PVC UIDs were preserved on upgrade.
Subsequent real E1002 polling after restart reused the same image digest and
base render attempt without another render.

The OG / 1.8.17 observer reported corrupted photographs/grayscale after the new
native four-bit output was delivered, consistent with the pinned firmware's
known expansion bug. Native four-bit gray4 acceptance failed on that tuple.
As an operational recovery, the existing Picsum renderer was explicitly switched
from `--palette gray4` to its supported `--palette bw` option. The final issued
PNG, including timestamp/battery/Wi-Fi overlays, was native two-bit with only
black/white pixels and 33024 bytes. The observer confirmed that the photograph
and overlays were readable again. This changes photo presentation to black/white
dithering; it neither repairs firmware nor validates intermediate grayscale.

This used the controller built from `beb7c2119c32d98a671c787c1787741cb40a950e`,
image digest `sha256:00e752f12d887d2208a2c926d50abcd5141008fb06e0dfe88479f97be566f8f7`,
on native Linux amd64 / Kubernetes 1.36.2 with the existing filesystem RWO PVC.
The firmware binary hash was not recorded.

A subsequent worktree build based on the same source revision used the
[temporary two-bit default](#temporary-gray4-encoding-switch), encoder
`png-v3-gray4-2bit`, image digest
`sha256:d9b164e19e0c65271d69899c0d7961467bcffc5b908a19027577218510ec41c9`,
in the same environment. Picsum was restored to `--palette gray4`. The OG
observer confirmed intermediate grays, no abnormal stripes/smearing, and readable
timestamp/battery/Wi-Fi overlays. Its actual issued PNG was native two-bit,
800 × 480, with all four canonical shades and 48705 encoded bytes. E1002's
actual issued image retained all six colors and 968 bytes. Device, Secret and
PVC UIDs, auth keys and previously issued image bytes remained unchanged,
including the older native four-bit OG image URL. This accepts the temporary
two-bit presentation recovery, including the known counting-heuristic risk;
it does not accept the opt-in native four-bit path on firmware 1.8.17.

These are initial color/delivery,
repeated polling and restart observations, not complete acceptance of physical
sleep/wake, outage/pending recovery or OG's new native four-bit grayscale output.
Full OG regression and remaining E1002 sessions remain required. E1001, E1003 and
E1004 have no physical acceptance recorded.

### Remaining image-processing physical checks

| Target | Remaining confirmation |
| --- | --- |
| OG, temporary two-bit default | Complete the full regression below for the selected candidate, including ordinary timer sleep/wake, outage/pending recovery and overlays; retain the known color-count heuristic limitation. |
| OG, `gray4_native4` | Use firmware with corrected grayscale expansion, record its exact revision/distribution and complete native four-bit grayscale and full regression checks. Firmware 1.8.17 does not accept this path. |
| E1002 | Complete the E1002 sessions below beyond the recorded initial colors, polling and restart observations, including sleep/wake, stale delivery, pending and recovery. |
| E1001, E1003, E1004 | Physical acceptance is unavailable until hardware sessions occur; retain the source-reviewed/software-tested distinction. |

Keep candidate source revision/image, model, firmware/distribution, date and
observation scope with each result. Record visual confirmation separately from
PNG metadata and authenticated fetches. Update README support claims only for
the accepted scope. After corrected-firmware acceptance, remove the temporary
gray4 build switch in a separate implementation change and rerun its applicable
software and physical checks.

## Physical-device regression

For the historical release baseline, use its OG / firmware 1.8.17 tuple. For
new native four-bit gray4 output, use firmware with the corrected expansion and
record its exact version/distribution; the historical baseline does not accept
this encoding. Use the operator's private Helm values.
Record the source revision/image, environment, date and observation scope.
Keep credentials and real identities out of reports. Restore temporary assignments,
polling settings and renderers after the check.

### Hardware evidence and publication

The procedures below are references for optional hardware investigations and
future support claims. They are not part of release preparation or publication,
and the release guard does not select a physical scope or require baseline
reuse. Release tags need no `Physical-device-*` annotations.

Keep the candidate model/firmware/distribution tuple fixed when collecting new
hardware evidence. Record the exact source revision/image, firmware/distribution,
date and observed scope privately. Historical observations apply only to their
recorded tuple and scope; do not infer panel correctness, corrected gray4
expansion or E1002 acceptance from software CI or a published chart. Change
README hardware support claims only when observations support them.

### Full regression procedure

Group visual observations into three sessions: normal/source/encoding display
(steps 1–3), rotation/corners (step 4), and failure/recovery (step 5). Prepare
renderers/Jobs and validate HTTP responses/PNGs before asking the observer to
watch. Collect one report per session rather than per corner or individual
transition. Test transitions must follow actual device polls; a short configured
sleep is not a guarantee of equally short wake/request intervals. Preserve the
timer-wake/manual-wake distinction for pending, and restore settings on failure
or interruption. An observer should report the first unexpected screen promptly.

1. Follow README pairing instructions; verify the anonymous setup image and
   authenticated display/image fetching through the actual TLS proxy/prefix.
   Setup stays enabled by default. Test explicit disablement separately if relevant.
2. Display all-black/all-white, four gray bands and intermediate grays only on
   the final row in grayscale, then indexed encoding; confirm levels and
   orientation visually. Inspect the delivered whole-image depth first: native
   gray4 with intermediate gray must be four-bit in a `gray4_native4` build;
   the temporary default instead uses two-bit and retains its documented
   color-count heuristic limitation. Verify filename reuse for
   identical bytes and a different filename for changed bytes.
3. Verify POST shows current observed voltage/RSSI, without inferred percentage.
   Verify mixed GET/POST/Job playlist order and name-sorted discovery; check Job
   preparation before selection and screen-cycle persistence across restart.
4. Enable timestamp/battery/Wi-Fi overlays and check all rotations/corners, bitmap
   readability, foreground margin/white background bleed, clockwise cycling, replay filename caching and retained issued
   images after restart. Force stale base delivery and confirm its original render
   timestamp. OG voltage thresholds are provisional icon heuristics, not measured
   percentages or calibrated state-of-charge estimates.
5. Force a refresh during renderer outage and confirm compatible stale delivery.
   Restart and fetch an already-issued URL. With no compatible image, verify
   HTTP 200 / JSON 202 with no image URL/filename does not request reset/OTA.
   First obtain a successful image on an ordinary timer wake, then enter pending
   without pressing the button; confirm the previous display remains until a new
   image is available. Test manual wake separately: firmware may display its
   Friendly ID screen when a redraw is required. Check fast-poll sleep/backoff
   separately from complete wake/request intervals, which include awake work.
   See [firmware pending behavior](PROTOCOL.md#responses-and-polling).

Distinguish user visual confirmation from controller metadata and authenticated
image-fetch checks. Avoid synthetic requests that advance the real device's
screen cycle while observing its ordinary polls.

### E1002 acceptance sessions

Prepare E1002 separately with its exact firmware/distribution and effective PNG
budget. Use an explicit 90000-byte override for the reviewed Seeed common
firmware; do not infer the distribution from its version. Keep its model,
firmware, source revision/image and date alongside the private observations.

1. Prepare RGB normalization and compatible indexed Spectra examples in advance.
   Check setup, authenticated image download through the actual TLS prefix,
   800 × 480 dimensions and all six exact reference RGB entries. Ask the observer
   to confirm black, white, yellow, red, blue and green in the intended bands,
   orientation, and overlay readability. Metadata/fetch checks cannot confirm
   panel colors.
2. On ordinary repeated polls, check filename/cache reuse and a changed image;
   restart the controller and retrieve an already-issued URL. Observe timer sleep
   and wake, compatible stale delivery during an outage, pending with the previous
   image preserved, then recovery. Observe manual wake separately. Restore the
   original assignment, renderer, polling interval and overrides on interruption
   as well as success.

Record E1001, E1003 and E1004 physical acceptance as unavailable until those
sessions actually occur. Firmware review and the software matrix do not imply
physical support for those models.

## Recorded acceptance baseline

The following summarizes the completed initial acceptance, not a test of every
later revision or a claim about the current deployment. Full phase/experiment
reports are in Git history before this documentation consolidation (for example,
commit `7977b00`). Synthetic fixtures remain under their existing paths.

| Date (JST) | Evidence and scope |
| --- | --- |
| 2026-10-07 | Kubernetes 1.36.2 single-node Linux RWO PVC collection experiment: default/custom paths, failed/missing/partial output, interrupted pointer publication and repeat collection passed. This was a prototype, not the production controller or power-loss validation. |
| 2026-10-07, confirmed 2026-10-08 | OG / 1.8.17 pairing, grayscale/indexed display, filename caching, restart and outage/initial-pending acceptance completed. Fixtures are synthetic, not traffic captures. |
| 2026-10-08 | Actual-device POST/current-telemetry delivery and authenticated Gateway fetch passed; user confirmed device-status display. Mixed GET/POST/Job playlist order was visually confirmed; discovery order, Job preparation and cycle restart persistence were checked from delivery state/images. |
| 2026-10-08 | Initial distribution passed verify/build, API integration on 1.36.0/1.36.2 and all four E2E scripts on disposable Linux kind 1.36.4 with same-node filesystem RWO PVC. |
| 2026-10-08 | That distribution passed actual-device POST/ownership/context, TLS image fetching, internal metrics and restart/issued-URL recovery on Kubernetes 1.36.2. User confirmed normal device-status display. |

The accepted distribution image was
`sha256:46065a3e7bf768b14a37a0a6a30392d8f98f732b6b5f02afa180ed1f176df119`.
The 16-device × 64-screen software scenario verified 1,024 durable pairs with
bounded active work, not production throughput. Neither the prototype nor kind
tests establish power-loss durability, multi-node CSI detach/attach or RWX support;
prototype NetworkPolicy enforcement was not probed. Current support limits are
maintained in [README](../README.md#security-and-support-boundary).
