# Phase 1 — Foundation, CRDs and Render Context

Implemented 2026-10-07. Phase 1 establishes declarative inputs and internal
contracts; TRMNL delivery starts in Phase 2.

## Implementation

- Go 1.26.8, controller-runtime v0.24.1, Kubernetes libraries v0.36.0,
  controller-gen v0.21.0; reproducible generation/build/check commands, a
  multi-stage non-root image, and GitHub Actions checks.
- Namespaced `arprt.dayflower.github.io/v1alpha1` `TrmnlScreen` and `TrmnlDevice`,
  generated deepcopy and structural CRDs with status subresources, schema
  defaulting, CEL exclusivity/interval/path/immutable-identity validation, and
  Kubernetes container-style renderer fields.
- Single-namespace reconcilers, conflict-safe status-only patches, duplicate
  identity detection, explicit reference resolution and watches for reference /
  identity changes. All unimplemented delivery paths are explicitly unready.
- Kubernetes-independent Render Target and Context v1, current-observation
  overlay, immutable snapshots, optional-value omission and exported-field
  allowlist. Shared Phase 0 fixtures remain the serialization authority.
- Internal resolver/executor/repository interfaces; a PVC repository with
  immutable image digests, collision-checked <=31-character firmware filenames,
  durable success pointers and UID-scoped issuance. Input revision normalizes
  defaults and covers the definition, context and collector contract versions.
- Namespace Role/RoleBinding, RWO PVC, single-replica Recreate Deployment, cache
  readiness, health checks and graceful SIGTERM shutdown.

## API clarifications

The Phase 0 API is preserved. Unknown source/reference keys are rejected at the
**strict API write boundary** (`fieldValidation=Strict`,
`kubectl --validate=strict`). Structural CRDs otherwise prune unknown fields;
CEL cannot inspect fields already removed by pruning. No webhook is needed for
the declared source/assignment/path/default constraints. See the official
[field validation](https://kubernetes.io/docs/reference/using-api/api-concepts/#field-validation)
and [CRD validation/pruning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/)
references.

HTTP URLs are bounded to 2048 characters, path fields to 512, interval strings
to 32, and renderer command/args/env/envFrom lists to 128 entries. URL authority
uses DNS/IP-style hosts (IPv6 uses brackets); userinfo, fragments, malformed
percent escapes, raw query semicolons and control characters are rejected.
Query semicolons can be represented as `%3B`. The internal artifact root is
reserved as `/var/lib/arprt`; output mounts at its ancestors or under it, system
directories, and the ServiceAccount secret tree are rejected.

## Conditions and observed generation

Every condition and status `observedGeneration` records the generation read by
the successful reconciliation. Condition timestamps change only when status
changes; repeated reconciliation does not rewrite unchanged status. Device
telemetry is preserved by optimistic, status-only patches and conflict retries.

| Resource / condition | Reason | Meaning |
| --- | --- | --- |
| Screen `Ready=False` (GET) | `RenderingNotImplemented` | Execution/delivery requires Phase 2 |
| Screen `Ready=False` (POST/Job) | `UnsupportedFeature` | Requires Phase 3/4 |
| Device `Ready=False` | `RenderingNotImplemented` | BYOS delivery requires Phase 2 |
| Device `AssignmentReady=True` | `AssignmentResolved` | Same-namespace screen exists; does not assert rendering readiness |
| Device `AssignmentReady=False` | `ReferenceNotFound` | Referenced screen is missing |
| Device `AssignmentReady=False` | `UnsupportedFeature` | Playlist/discovery requires Phase 5 |
| Device `TargetKnown=Unknown` | `TargetUnobserved` | Authenticated capability resolution starts in Phase 2 |
| Device `IdentityUnique=True/False` | `IdentityUnique` / `DuplicateHardwareID` | Namespace identity uniqueness |

Additional authentication, capability and render outcome reasons will be added
with the corresponding implementation. Phase 1 never exposes credentials or
reports successful rendering.

## Kubernetes support floor

Initial supported API-server baseline: **Kubernetes 1.36.0, within minor 1.36**.
The required APIs are `apiextensions.k8s.io/v1`, `apps/v1`, core/v1 and RBAC/v1;
schema rules use CEL (stable since 1.29), including URL/string/duration helpers.
Those API requirements alone do not establish client compatibility with older
minors. The selected controller-runtime is tested against its matching
Kubernetes dependency minor; see its pinned
[compatibility policy](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.1/README.md#versioning-maintenance-and-compatibility).
The project's support floor follows that tested dependency baseline and the
Phase 1 API integration checks. Other minors remain unverified. This resolves
the open Phase 0 floor without claiming older minors inherently incompatible.

## Validation record

All checks below passed on 2026-10-07:

- Generated-file reproducibility, module tidiness, formatting, vet, race tests
  and static controller build.
- Envtest API integration on 1.36.0 and 1.36.2: generated CRD installation;
  valid samples/defaults; invalid sources, methods, assignments, intervals,
  URLs, paths and identity changes; strict unknown-field rejection; status
  subresources; watches, duplicate identity repair and same-namespace references;
  graceful manager shutdown.
- Context fixture equivalence, omitted optional objects, actual zero, invalid
  numeric observations, snapshot independence, unknown-field tolerance and
  credential/metadata exclusion.
- PVC reopening/recovery, success preservation on failed publication, obsolete
  sequence rejection, UID/target isolation, stable filenames, UID-scoped
  issuance and corrupted-image rejection.
- Container image build and local MicroK8s `v1.36.2` deployment: generated CRDs
  installed; all samples accepted; single-replica Deployment ready with non-root
  UID/GID and RWO hostpath PVC; health/readiness returned HTTP 200; namespace
  Role permitted only resource reads and status patches (other-namespace reads,
  Secret reads and Job creation denied); Recreate restart succeeded. The
  isolated test namespace and newly created CRDs were removed afterward.

Commands: `make verify`, `make integration ENVTEST_K8S_VERSION=1.36.0`,
`make integration ENVTEST_K8S_VERSION=1.36.2`, `docker build`, and
`python3 hack/cluster-smoke.py --image <cluster-accessible-image>:<tag>`.
The cluster script refuses to overwrite existing arprt CRDs. This verifies
controller restart and PVC reopening, not a full power-loss / storage-driver
fault matrix.

Physical device protocol acceptance and image decoding/serving are Phase 2;
Job scheduling and collection through the production controller are Phase 4.
The repository currently retains all blobs/issuances; bounded garbage
collection will follow the retention contract in later phases.
