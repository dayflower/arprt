# Development

This guide covers working on arprt. See [DESIGN](DESIGN.md) for internal
guarantees, [PROTOCOL](PROTOCOL.md) for wire/image contracts, and
[README](../README.md) for installation/support, [OPERATIONS](OPERATIONS.md) for
runtime administration and [RENDERERS](RENDERERS.md) for example usage.

## Tooling and generated files

Use Go, Make, a C compiler for race tests, Python 3 for examples/smoke checks,
and Docker for images. The [Makefile](../Makefile) selects the Go toolchain and
pins controller-gen/setup-envtest; [go.mod](../go.mod) pins dependencies and
[CI](../.github/workflows/ci.yaml) defines the Kubernetes test matrix and kind image.
Use those files when changing versions rather than adding another version list.
Build tools and envtest binaries are installed under the ignored `bin/` directory.

```sh
make build
make verify
```

`make verify` regenerates deepcopy/CRDs, checks their diff, checks module tidiness
and formatting, then runs vet, all race tests and the controller build. It does
not run API integration or cluster tests. For API changes, edit types/validation
markers in [types.go](../api/v1alpha1/types.go), then run `make manifests` and
include both the types and generated files in the change. `make generate` only
regenerates deepcopy code. `make fmt` applies Go formatting.

The generated-file check compares against the Git index, so it also reports
intended unstaged generated changes. Review those changes and check that another
generation produces identical bytes; do not bypass unrelated failures.

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
| API markers/defaults, status, watches, Secret conflicts or reconciliation | Envtest on both CI Kubernetes versions |
| BYOS/HTTP/credentials/identity | `cluster-smoke.py` for cluster/deployment integration |
| Job scheduling, mounts, collection, recovery or cancellation | `job-smoke.py` |
| Playlist/discovery, replay or per-screen retention | `playlist-smoke.py`, plus HTTP/Job regressions when affected |
| Capacity, corruption, GC, metrics, logs or distribution | `operations-smoke.py` |
| Docker/manifests/examples | Docker build and relevant Kustomize assembly/cluster checks |
| Release | Full software matrix and relevant physical-device acceptance |

```sh
make integration ENVTEST_K8S_VERSION=1.36.0
make integration ENVTEST_K8S_VERSION=1.36.2
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
same `KUBECONFIG`. `make cluster-smoke` runs all four scripts. The CI workflow
contains the pinned kind version and full setup/cleanup recipe.

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

## Physical-device regression

Use the supported OG / firmware 1.8.17 tuple and the operator's private overlay.
Record the source revision/image, environment, date and observation scope.
Keep credentials and real identities out of reports. Restore temporary assignments,
polling settings and renderers after the check.

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
   JSON 202 preserves the display and invokes fast-poll backoff without reset/OTA.

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
