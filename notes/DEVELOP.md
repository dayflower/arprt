# Development

This guide covers working on arprt. See [DESIGN](DESIGN.md) for internal
guarantees, [PROTOCOL](PROTOCOL.md) for wire/image contracts, and
[README](../README.md) for installation/support, [OPERATIONS](OPERATIONS.md) for
runtime administration and [RENDERERS](RENDERERS.md) for example usage.

## Tooling and generated files

Use Go, Make, a C compiler for race tests, Python 3 for examples/smoke/release checks,
and Docker for images. The [Makefile](../Makefile) selects the Go toolchain and
pins controller-gen/setup-envtest/kind and the kind node image;
it also pins the Helm client used for CRD chart checks.
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
Docker builds run only in the two native cluster jobs; `contracts` checks Go.
Those jobs also build the Picsum example image and run its offline conversion
and download-boundary tests on each native architecture.
A new run cancels any older in-progress run of the same workflow for the same
pull request or branch.

```sh
make build
make verify
```

`make verify` regenerates deepcopy/CRDs and chart copies, checks their diff, checks module tidiness
and formatting, checks release guards/digest wiring and the CRD chart, then runs vet, all race tests
and the controller build. It does not run API integration or cluster tests. For API changes, edit types/validation
markers in [types.go](../api/v1alpha1/types.go), then run `make manifests` and
include both the types and generated files in the change. `make generate` only
regenerates deepcopy code. `make fmt` applies Go formatting.

The generated-file check compares against the Git index, so it also reports
intended unstaged generated changes. Review those changes and check that another
generation produces identical bytes; do not bypass unrelated failures.

## CRD chart generation and checks

The [CRD chart](../charts/arprt-crds/README.md) is an application chart containing
only the public CRDs. API types and markers remain authoritative;
`config/crd/bases` is the canonical controller-gen output. `make manifests`
also runs [crd-chart](../hack/crd-chart/main.go) to copy those files byte-for-byte
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

The Makefile selects the Helm version; [install-helm.py](../hack/install-helm.py)
downloads the official checksummed binary into ignored `bin/`. The current
checks use Helm 4; this does not establish compatibility with other clients.
The full controller chart lifecycle/client matrix is a separate check scope.
`make integration ENVTEST_K8S_VERSION=1.36.0` also exercises the CRD chart on a
disposable API server: fresh install, a real compatible optional-field schema
upgrade, preservation of existing CR/CRD UIDs, uninstall/reinstall with the same
owner, and retention when an upgrade removes CRDs from the release. Upgrade
fixtures exist only in temporary chart copies, never in production API types.

## Controller chart assembly

The [controller chart](../charts/arprt/README.md) packages the existing namespace
Deployment and supporting resources. Its exact `arprt-crds` dependency is pinned
in `Chart.yaml` and committed `Chart.lock`, using a local source for development.
Run `make crd-chart-check`, then `bin/helm-v4.3.0 dependency build charts/arprt --skip-refresh`
after CRD changes and before linting or packaging. Dependency archives under
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
delivery. The full cluster lifecycle/client matrix and OCI publication remain
separate milestones; current `make verify` checks only the CRD chart.

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

## Package responsibilities

| Location | Responsibility |
| --- | --- |
| `api/v1alpha1`, `config/crd` | Public types/markers and generated schema/deepcopy |
| `charts/arprt-crds`, `hack/crd-chart` | CRD packaging, generated schema synchronization and Helm/spec checks |
| `cmd/controller`, `config/default` | Runtime wiring, listeners, namespace scope, deployment and RBAC |
| `internal/controller` | Resource readiness, reference watches and optimistic status reconciliation |
| `internal/assignment` | Ordered eligible sets, fingerprints and screen selection |
| `internal/byos` | Routes, protocol parsing, authentication gates, display/replay and image serving |
| `internal/devicestate` | Request observations, target resolution, status and render-context snapshots |
| `internal/credentials`, `internal/deviceidentity` | Named auth Secret access/provisioning and durable MAC registration |
| `internal/screen` | Effective defaults and normalized render-input revision |
| `internal/overlay` | Vendored CC0 Home Video text, original icons, current-observation levels/charging, inset foreground/edge background and bounded 2-bit encoder |
| `internal/render`, `internal/delivery` | Context/PNG/HTTP contracts and GET/POST execution/publication policies |
| `internal/jobrender` | Durable pair/attempt records, scheduling, Job lifecycle, recovery and collection |
| `internal/artifact`, `internal/maintenance` | PVC publication, identity/issuance, capacity and assignment-aware retention |
| `internal/observe` | Bounded metrics and outcome reporting |
| `internal/integration` | Envtest API validation and running-manager integration |
| `examples`, `testdata`, `hack` | Renderer examples, synthetic contracts/images and cluster smoke checks |

Keep protocol/context models independent of Kubernetes client objects. HTTP
delivery and Job collection share PNG validation and artifact ownership rules.
Use existing package boundaries rather than introducing speculative backends.

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
| Docker/manifests/examples | Docker build and relevant Kustomize assembly/cluster checks |
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
`python3 hack/check-docs.py` checks tracked local Markdown links/anchors and
whitespace. Executable example changes still need applicable software checks.
Use the manual full run if executable instructions or their scope are uncertain.

## Disposable cluster checks

Smoke scripts use the selected kubeconfig context, install/reuse matching CRDs,
create isolated namespaces and clean up their test resources. They refuse to
replace mismatched CRD schemas. Use a dedicated cluster and kubeconfig rather
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
For a focused check, use `python3 hack/<script>.py --image arprt:dev` with that
same `KUBECONFIG`. `make cluster-smoke` runs all four scripts. The Makefile pins
kind and the node image. The following creates its own kubeconfig and cluster,
runs all four scripts, and removes the cluster/kubeconfig on exit, including
failures:

```sh
make disposable-cluster-smoke IMAGE=arprt:dev LOAD_IMAGE=true
```

This wrapper installs the CRDs once in its own cluster and runs two smoke suites
concurrently, starting the longest Job suite first. Each suite uses an isolated
namespace/PVC and reuses those CRDs. All suites finish cleanup, and any failure
fails the wrapper. `make cluster-smoke` remains sequential for explicitly selected
clusters; do not launch its scripts concurrently while they own CRD creation
and deletion. The parallel runner is intended only for the prepared disposable
cluster. Suite durations and failure output appear in the wrapper log.

Omit `LOAD_IMAGE=true` to test a registry image without loading a local copy.
All four scripts accept tagged images and multi-platform index digest references.

The HTTP check covers setup/key reuse, MAC modes, authentication, both proxy
prefix modes, telemetry, rotation, GET reuse, restart, stale output and initial
pending. Overlay checks include fractional telemetry, fallback, pixel preservation
independent horizontal/vertical margins and white edge bleed, explicit charging without USB inference and original timestamps
using embedded Tokyo zoneinfo in the scratch container.
Job checks add actual containers, token absence, same-node mounts, paths,
immutable snapshots, rotation changes, interval refresh, recovery, failures and
obsolete work, plus current overlay telemetry versus immutable Job snapshots.
Playlist checks add mixed-source order, preparation before selection, retry/restart
and membership edits, shared corner cycling and unchanged replay bytes. Operations checks add corruption, capacity pressure,
redaction, composite/base separation, old composite URL retention, HTTP-only GC and installation of the supplied examples.
These scripts do not configure public ingress or a physical device.

## Controller image releases

The [image release workflow](../.github/workflows/release-image.yaml) publishes
`ghcr.io/dayflower/arprt`. A release tag `vMAJOR.MINOR.PATCH` maps to controller
`--version` and OCI version `MAJOR.MINOR.PATCH`, image tag `vMAJOR.MINOR.PATCH`,
and future chart `appVersion: "MAJOR.MINOR.PATCH"`. SemVer prereleases are allowed;
build metadata is excluded because it cannot be represented directly in image
tags. There is no moving `latest` tag. Version and platform tags are immutable
by release policy: the publisher checks existing package versions and refuses
all overwrites. Do not move/delete release Git tags or manually retag images.
Use the published image index digest for installations that support both
architectures. `--version` also records the full source commit, matching OCI
revision metadata. Development images default to version `dev`.

Before tagging, merge the intended revision into `main`, run the full software
matrix, and complete the applicable [physical acceptance](#physical-device-regression)
on that exact revision with the supported OG / firmware 1.8.17. A full baseline
can be reused only for the changes permitted below; current-image installation
checks still apply. For the physical
check, build the revision with its intended version:

```sh
make docker-build IMAGE=arprt:release-check VERSION=0.1.0
```

Record environment and observation scope privately, without credentials or real
identities. After acceptance, create an annotated tag whose message attests the
revision, JST date and required scope (replace the example version/date; choose
an unused version):

```sh
arprt_release_revision="$(git rev-parse HEAD)"
cat > /tmp/arprt-release-tag.txt <<EOF
arprt v0.1.0

Physical-device-acceptance: $arprt_release_revision
Physical-device-date: 2026-10-13
Physical-device-scope: OG / firmware 1.8.17; DEVELOP physical-device regression
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
retain the complete four-suite cluster matrix. These are
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
and operations checks through Kustomize. If the initial anonymous check fails
before visibility is changed, set the package public and rerun acceptance only:

```sh
gh workflow run release-image.yaml -f tag=v0.1.0 \
  -f digest=sha256:<published-index-digest>
```

Take the index digest from the original publishing job summary or
`published-image-index` artifact, not from an unverified new upload. This manual
path validates the annotated tag again and never writes packages. Do not rerun
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

## Physical-device regression

Use the supported OG / firmware 1.8.17 tuple and the operator's private overlay.
Record the source revision/image, environment, date and observation scope.
Keep credentials and real identities out of reports. Restore temporary assignments,
polling settings and renderers after the check.

### Selecting physical acceptance

Keep the supported model/firmware tuple fixed. An initial release, a new tuple,
runtime/PNG/rendering/overlay/assignment/credential/storage changes, dependencies,
Dockerfile, Makefile, examples/fixtures or unknown paths require the full numbered
regression below. Do not infer display correctness or new hardware support from CI.

For documentation, CI/harness, Go test-only or installation changes under
`config/default`, `config/overlays` and `charts`, reuse a full accepted release
baseline when the committed trees contain no other changes. Select it with:

```sh
python3 hack/release-image.py physical-scope v0.1.0
```

The command compares committed Git trees, not uncommitted edits. It validates
the baseline's annotation and main ancestry, reports `full` or `reuse`, and
lists paths that require full regression. Deleted and renamed paths are checked.
It never attests that a device passed a test. Reuse must refer directly to a full
baseline, not to a chain of reused releases; legacy tags without a mode count as
full acceptance. Record that baseline, its tuple/date, the changed paths and
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

The release guard rejects reuse if runtime inputs differ, the baseline is not a
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
2. Display four gray bands in grayscale, then indexed encoding; confirm levels
   and orientation visually. Verify filename reuse for identical bytes and a
   different filename for changed bytes.
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
