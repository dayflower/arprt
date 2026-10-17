# Development

This guide covers working on arprt. See [DESIGN](DESIGN.md) for internal
guarantees, [PROTOCOL](PROTOCOL.md) for wire/image contracts, and
[README](../README.md) for installation/support, [OPERATIONS](OPERATIONS.md) for
runtime administration and [RENDERERS](RENDERERS.md) for example usage.

## Tooling and generated files

Use Go, Make, a C compiler for race tests, Python 3 for examples/smoke/release checks,
and Docker for images. The [Makefile](../Makefile) selects the Go toolchain and
pins controller-gen/setup-envtest/kind and the kind node image;
it also pins the Helm 4 client used for chart checks.
[go.mod](../go.mod) pins dependencies and [CI](../.github/workflows/ci.yaml)
defines the Kubernetes test version.
Use those files when changing versions rather than adding another version list.
Build tools and envtest binaries are installed under the ignored `bin/` directory.

CI runs on pull request creation and updates, and on pushes to `main`, including
merges. A manual `workflow_dispatch` always runs full software checks. Pushes to
other branches and tags do not trigger this workflow; version
tags run the separate [image release workflow](../.github/workflows/release-image.yaml).
The lightweight change/documentation job always runs. Changes limited to prose
in README, AGENTS or Markdown under `notes/` skip Go, envtest, Docker and cluster
jobs. Changed fenced/indented examples, other paths and unavailable history run the full
software matrix. The existing `contracts` check remains active and propagates
documentation/check-selection failures, including on prose-only changes.
Docker builds run in the native image/renderer jobs and Helm matrix; `contracts`
checks Go. The image/renderer jobs also build the Picsum example image and run its offline conversion
and download-boundary tests on each native architecture.
A new run cancels any older in-progress run of the same workflow for the same
pull request or branch.

```sh
make build
make verify
```

`make test` runs the race suite both with the default two-bit gray4 build and
with the temporary `gray4_native4` build tag.

`make verify` regenerates deepcopy/CRDs and chart copies, checks their diff, checks module tidiness
and formatting, checks release guards/digest wiring and both charts (including
dependencies, templates and packages), then runs vet, all race tests and the
controller build. It does not run API integration or cluster tests. For API changes, edit types/validation
markers in [types.go](../api/v1alpha1/types.go), then run `make manifests` and
include both the types and generated files in the change. `make generate` only
regenerates deepcopy code. `make fmt` applies Go formatting.

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
publication and anonymous acceptance from published charts remain a separate
distribution milestone.

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
  OCI_CHART_REPOSITORY=oci://ghcr.io/dayflower/charts CHART_VERSION=0.1.0 \
  IMAGE=ghcr.io/dayflower/arprt@sha256:<published-index-digest>
```

Use the actual published repository/version/index digest; the repository above is
an example, not an available distribution endpoint. This target uses empty Helm
registry and Docker configurations, pulls both chart packages, and never falls
back to local charts. Run it with the pinned Helm 4 client when accepting
publication. Until that succeeds, local packaging/lifecycle evidence is not public-artifact
acceptance.

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
| `internal/devicestate` | Request observations, target resolution, status and render-context snapshots |
| `internal/credentials`, `internal/deviceidentity` | Named auth Secret access/provisioning and durable MAC registration |
| `internal/screen` | Effective defaults and normalized render-input revision |
| `internal/imageprocessing` | Shared bounded PNG/APNG decoding, normalization/dithering, admission, output compatibility, canonical palettes and 2/4-bit encoding; one runtime admission lease shared across reception, collection, overlays and buffered serving |
| `internal/overlay` | Vendored CC0 Home Video text, original icons, current-observation levels/charging, inset foreground/edge background and palette-aware composition through shared encoding after renderer normalization; legacy persisted data remains readable |
| `internal/render`, `internal/delivery` | Context/PNG/HTTP contracts and GET/POST execution/publication policies |
| `internal/jobrender` | Durable pair/attempt records, scheduling, Job lifecycle, recovery and collection |
| `internal/artifact`, `internal/maintenance` | PVC publication, identity/issuance, capacity and assignment-aware retention |
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
| Release | Full software matrix and relevant physical-device acceptance |

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

For documentation-only edits, verify local links/anchors, obsolete references
and `git diff --check`. No software matrix is required for prose-only changes.
`python3 tools/check-docs.py` checks tracked local Markdown links/anchors and
whitespace. Executable example changes still need applicable software checks.
Use the manual full run if executable instructions or their scope are uncertain.

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
independent horizontal/vertical margins and white edge bleed, explicit charging without USB inference and original timestamps
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

Before tagging, merge the intended revision into `main`, run the full software
matrix, and complete the applicable [physical acceptance](#physical-device-regression)
on that exact revision with corrected-expansion OG firmware and E1002. Record
the exact firmware version and distribution for each model. A full baseline can
be reused only for the same firmware/distribution tuple and the changes permitted below; current-image installation
checks still apply. For the physical
check, build the revision with its intended version:

```sh
make docker-build IMAGE=arprt:release-check VERSION=0.1.0
```

Record environment and observation scope privately, without credentials or real
identities. After acceptance, create an annotated tag whose message attests the
revision, JST date, required scope and exact firmware/distribution (replace the
example version/date and both firmware placeholders; choose
an unused version):

```sh
arprt_release_revision="$(git rev-parse HEAD)"
cat > /tmp/arprt-release-tag.txt <<EOF
arprt v0.1.0

Physical-device-acceptance: $arprt_release_revision
Physical-device-date: 2026-10-13
Physical-device-scope: OG corrected gray4 firmware + E1002; DEVELOP physical-device regression
Physical-device-og-firmware: <accepted OG distribution, version and source revision>
Physical-device-e1002-firmware: <accepted E1002 distribution, version and source revision>
Physical-device-mode: full
EOF
git tag -a v0.1.0 "$arprt_release_revision" -F /tmp/arprt-release-tag.txt
rm /tmp/arprt-release-tag.txt
git push origin refs/tags/v0.1.0
```

The annotation is an operator attestation, not automated proof of hardware
behavior. The workflow rejects lightweight tags, invalid versions, missing
attestations, a checkout different from the tagged commit, and commits outside
`origin/main`. It explicitly runs `make verify` and envtest on Kubernetes 1.36.0
alongside independent Docker Buildx/cluster jobs on that exact commit on native
`ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` runners. Publication requires all
of these jobs to pass. Both the pre-publication and anonymous acceptance runs
retain the complete five-suite cluster matrix. These are
fresh isolated clusters; release checks do not use an operator's cluster. No
QEMU acceptance is substituted for native execution. A new architecture does
not expand physical-device, Kubernetes or storage support.

Docker follows [Docker's cross-compilation guidance](https://docs.docker.com/build/building/multi-platform/):
the Go builder uses `BUILDPLATFORM` and compiles with `TARGETOS`/`TARGETARCH`;
the runtime remains non-root scratch with CA certificates and embedded zoneinfo.
The workflow archives each exact tested image, then its only `packages: write`
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
set its visibility to **public**. See [GitHub's container registry guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
This setting requires a package owner and cannot be replaced by Docker login.
No private registry credentials are needed by consumers.

After uploading, the workflow reads the index/configuration anonymously,
checks both platform digests, pulls the same version tag on both native runners,
and reruns the complete cluster matrix with `ghcr.io/dayflower/arprt@sha256:...`.
The clusters pull directly without preloaded controller images or pull Secrets,
covering startup/readiness, setup, authenticated delivery, Job execution, replay,
and operations checks through local production Helm chart packages with the
pinned Helm 4 client. Public OCI chart retrieval is not required for image releases.
If the initial anonymous check fails before visibility is changed, set the package public and rerun acceptance only:

```sh
gh workflow run release-image.yaml --ref v0.1.0 -f tag=v0.1.0 \
  -f digest=sha256:<published-index-digest>
```

Take the index digest from the original publishing job summary or
`published-image-index` artifact, not from an unverified new upload. This manual
path validates the annotated tag again and never writes packages. Select the
matching Git tag with `--ref` so the workflow and helper paths belong to that
release, including releases before directory reorganizations. Do not rerun
the publisher: existing version/platform tags are rejected. An interrupted
partial upload requires investigation and a new version; do not overwrite the
partial version.

Only after both native public acceptance jobs pass does a separate
`contents: write` job create the GitHub release with permanent
`image-digests.json` and an acceptance workflow link. Before that point the
registry upload alone is not a completed release. Workflow artifacts retain the
index/acceptance records for 90 days; GitHub release assets retain the final
record. For compatible upgrades use a tested matching release checkout/image;
existing runtime, data-format and support limits remain authoritative.

## Chart release preparation

Chart source and local packages are available; OCI chart publication and
anonymous published-chart acceptance are not yet complete. The image release
workflow above publishes the controller image and its acceptance records only.
It validates local production chart packages but does not upload either chart.
Do not advertise the example OCI repository as an available installation source.

The release inputs have distinct owners:

| Input | Source of truth |
| --- | --- |
| Controller version and image tag | Annotated image release tag and the image workflow |
| Image index and platform digests | Accepted release's `image-digests.json` |
| Controller chart version and `appVersion` | `charts/arprt/Chart.yaml` |
| Default controller image | `charts/arprt/values.yaml`; index digest takes precedence over tag |
| CRD chart version and metadata | `charts/arprt-crds/Chart.yaml` |
| Exact bundled CRD version/source | Parent `Chart.yaml` and committed `Chart.lock` |
| Public CRD schemas | API types/markers, generated canonical YAML and synchronized chart files |

Chart versions identify packages; `appVersion` describes the controller version
and does not select the image. Keep it consistent with the default image and
record the tested chart/image/schema combination explicitly. Changing an image
tag without updating the default digest does not change the deployed image.
Do not assume that a chart version, image version and dependency version will
always be identical. A controller-only change need not change the CRD chart's
version when its package is unchanged.

For a candidate package, regenerate schemas with `make manifests` when API
types/markers change. Review both generated destinations and verify deterministic
regeneration. When intentionally changing a dependency version, update the exact
parent dependency and regenerate/review `Chart.lock` with the pinned Helm client;
normal `make chart-dependencies` rebuilds archives while requiring that lock to
stay unchanged. Run the chart checks, API integration, Docker build and full
Helm lifecycle/smoke matrix described above against the intended image. Preserve
the image release's physical-device acceptance requirements. `make chart-package`
(also included by `chart-check` and `helm-smoke`) writes the standalone CRD archive
and parent archive containing its pinned dependency. Keep the exact archives
exercised by the checks for distribution.

Public chart distribution still requires publishing the validated image first,
then the CRD and parent packages, without overwriting released versions. Record
both chart versions, the pinned dependency, controller version and index/platform
digests together. Anonymous retrieval and the published-artifact lifecycle matrix
must pass on the release combination, including a fresh install and compatible
upgrade, before declaring public chart distribution complete. The existing
`helm-published-smoke` target supplies the acceptance entrypoint; publication
automation remains a separate implementation step.

## Integrated image acceptance

[image-smoke.py](../tools/image-smoke.py) installs the packaged controller with
its actual 500m CPU / 512 MiB limits and 384 MiB Go memory limit. It exercises
OG and E1001–E1004 profile defaults without firmware/size headers, RGB input
normalization through GET/POST/Job, exact palette samples, final overlay pixels,
indexed Spectra byte preservation across policy edits, exclusive output budgets
and authenticated retrieval of issued images after restart. The original four
suites retain OG regression, playlist recovery and storage/GC coverage.

Its load case starts eight distinct HTTP requests and a real PVC Job using
32 MiB, 4096-square RGBA16 inputs in plain and Adam7 layouts. It checks bounded
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
physical display observations are distinct acceptance layers. Until the native
resource runs and physical sessions pass for the candidate, retain the proposal
and plan and do not expand the support claim.

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
Full OG regression, remaining E1002 sessions and native arm64 resource acceptance
remain required. E1001, E1003 and E1004 have no physical acceptance recorded.

## Physical-device regression

For the historical release baseline, use its OG / firmware 1.8.17 tuple. For
new native four-bit gray4 output, use firmware with the corrected expansion and
record its exact version/distribution; the historical baseline does not accept
this encoding. Use the operator's private Helm values.
Record the source revision/image, environment, date and observation scope.
Keep credentials and real identities out of reports. Restore temporary assignments,
polling settings and renderers after the check.

### Selecting physical acceptance

Keep the candidate model/firmware/distribution tuple fixed during each session.
An initial release, a new tuple,
runtime/PNG/rendering/overlay/assignment/credential/storage changes, dependencies,
Dockerfile, Makefile, examples/fixtures or unknown paths require the full numbered
regression below. Do not infer display correctness or new hardware support from CI.

For documentation, CI/harness, Go test-only or installation changes under
`charts` and `examples/networking/envoy-gateway`, reuse a full accepted release
baseline when the committed trees contain no other changes. Select it with:

```sh
python3 tools/release-image.py physical-scope v0.1.0
```

The command compares committed Git trees, not uncommitted edits. It validates
the baseline's annotation and main ancestry, reports `full` or `reuse`, and
lists paths that require full regression. Deleted and renamed paths are checked.
It never attests that a device passed a test. Reuse must refer directly to a full
baseline, not to a chain of reused releases; legacy tags without a mode count as
full acceptance. The historical OG / 1.8.17 scope remains readable but always
requires full acceptance of the corrected-gray4 OG and E1002 contract, even when
no runtime paths changed. It cannot be reused as evidence for the new scope.
Record that baseline, its tuple/date, the changed paths and
current installation observations privately.

In reuse mode, build/deploy the exact current revision/image through the intended
installation, check readiness, setup/key reuse and authenticated TLS image
delivery, fetch a previously issued URL after restart, and confirm normal device
content/current telemetry/overlay readability visually. Restore the normal
assignment and polling interval. A firmware/model change, unexplained failure or
uncertain baseline requires full regression. Use the normal image polling
interval; no four-corner/rotation/outage tour is required in reuse mode.

After those checks, keep the exact current revision, date and scope fields in
the tag and replace its full-mode line with:

```text
Physical-device-mode: reuse
Physical-device-baseline: v0.1.0
Physical-device-installation: passed
```

The release guard rejects reuse if runtime inputs or firmware/distribution differ,
the baseline has the historical scope or is not a
full accepted ancestor on main, installation acceptance is absent or its date
predates the baseline. GitHub release notes distinguish full regression from
current installation checks with baseline reuse. Full software/native/anonymous
acceptance remains required for both modes.

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
   gray4 with intermediate gray must be four-bit. Verify filename reuse for
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
