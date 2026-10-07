# arprt

A TRMNL BYOS Kubernetes controller. **Phase 1 is the project foundation:** the
controller validates and reconciles declarative resources, but does not serve
TRMNL endpoints or render images yet. See [the phased plan](notes/000-PLAN.md),
[contracts](notes/001-PHASE0.md), and [Phase 1 implementation](notes/002-PHASE1.md).

## Build and checks

Use Go 1.26.8 (the Makefile provisions that exact toolchain through Go's
toolchain selection), a C compiler for race tests, Make, and Docker for images.
Dependencies are pinned in `go.mod` / `go.sum`; controller-gen is v0.21.0.

```sh
make manifests  # structural CRDs and deepcopy methods
make fmt vet test build
make verify    # generated-file drift, module tidiness, formatting, vet, race, build
make integration ENVTEST_K8S_VERSION=1.36.0
make integration ENVTEST_K8S_VERSION=1.36.2
make docker-build IMAGE=arprt:dev
```

Integration tests start a disposable etcd/API server and install the generated
CRDs; they do not modify your kubeconfig's cluster. Binaries are downloaded to
the ignored `bin/` directory. Tests check schema rejection/defaulting, status
subresources, immutable identity, sample manifests, namespace boundaries,
reference changes, duplicate hardware IDs and graceful manager shutdown.

To check Deployment/PVC/probes/RBAC/restart on a local cluster with no existing
arprt CRDs, run `python3 hack/cluster-smoke.py --image <image>:<tag>` after making
that image available to the cluster. The script uses an isolated namespace and
removes its resources afterward; it refuses to overwrite pre-existing CRDs.

## Installation

The initial support baseline is **Kubernetes 1.36.0 or newer within the 1.36
minor**, Linux, one active controller and a POSIX PVC supporting concurrent
same-node `ReadWriteOnce` mounts, atomic rename and fsync. Other Kubernetes minors
are unverified. `ReadWriteOncePod`, multiple active replicas and multi-node
storage failover are outside the supported topology.

Build/push an image to a registry accessible by your cluster, then change the
image in `config/default/deployment.yaml` (or use a Kustomize overlay). A default
StorageClass must provision the PVC; otherwise set `storageClassName` or bind a
suitable pre-provisioned volume. The supplied configuration uses namespace
`trmnl`, a single replica, `Recreate` rollout, namespace-only Role/RoleBinding,
non-root UID/GID 1000, and an artifact volume at `/var/lib/arprt`.

```sh
kubectl apply --validate=strict -k config/default
kubectl -n trmnl rollout status deployment/arprt
kubectl -n trmnl apply --validate=strict -f config/samples/screens.yaml
kubectl -n trmnl apply --validate=strict -f config/samples/device.yaml
kubectl -n trmnl get trmnlscreens,trmnldevices
```

CRD installation needs cluster-level privileges; runtime RBAC is namespace-only.
Use strict field validation on API writes to reject unknown source keys and
namespace overrides in references. Kubernetes otherwise prunes unknown CRD
fields. Validation uses structural schemas and CEL, with no admission webhook.

The sample device references an operator-supplied `living-room-auth` Secret.
Before Phase 2 pairing, create that Secret with a random URL-safe `apiKey` encoding
at least 32 random bytes. Phase 1 never reads Secrets or enables pairing.
Renderer `envFrom` examples also refer to operator-supplied configuration.

For local controller development after installing the CRDs:

```sh
mkdir -p /tmp/arprt-data
bin/arprt --namespace=trmnl --artifact-root=/tmp/arprt-data
```

`WATCH_NAMESPACE` is the fallback for `--namespace`; when `POD_NAMESPACE` is
present it must match. `/healthz` and `/readyz` listen on port 8081; readiness
waits for namespace cache synchronization. SIGINT/SIGTERM stops the manager with
a 20-second shutdown deadline (the Pod grants 30 seconds). Metrics are disabled
until operational instrumentation is implemented.

## Current behavior and contracts

`TrmnlScreen` supports exactly one HTTP or Job declaration. HTTP defaults to GET,
only GET/POST are allowed, and URLs must be absolute HTTP(S) without userinfo or
fragments. `refreshInterval` uses whole seconds/minutes/hours from 1s to 24h;
effective defaults are 5m for GET and 30m for Job. Explicit intervals on POST are
rejected. Job declarations expose `image`, `command`, `args`, `env`, `envFrom`,
and `resources`; clean absolute file paths default to `/trmnl/context.json` and
`/trmnl/output.png`. The writable output mount must use a dedicated directory.

`TrmnlDevice` requires an immutable uppercase MAC and same-namespace auth Secret
reference. It may use either `screenRef` or `playlistRef`, or neither. Telemetry,
capabilities, last-seen time and conditions are status fields.

All screens/devices remain `Ready=False`: HTTP GET execution belongs to Phase 2,
POST to Phase 3 and Job to Phase 4. Playlist/discovery declarations report
`UnsupportedFeature` until Phase 5. An explicit existing screen can have
`AssignmentReady=True` while device delivery remains unavailable. A missing
screen produces `ReferenceNotFound`; duplicate hardware IDs produce
`IdentityUnique=False`. The controller does not invent display capabilities.

Render Context v1 uses a transport-independent model and an explicit status
allowlist. Optional objects/measurements are omitted when absent, actual zero is
preserved, and credentials/MACs/metadata cannot be exported. Additive optional
fields preserve v1; consumers ignore unknown fields. Shared fixtures live in
[`testdata/phase0`](testdata/phase0).

The internal PVC repository stores immutable digest-addressed images, stable
short firmware filenames, success metadata, and issuance records scoped to
device UID. Publication fsyncs bytes/metadata before atomically changing the
latest-success pointer. Invalid/obsolete publications preserve prior success.
PNG/protocol validation, serving, Job scheduling and garbage collection belong
to subsequent phases; the repository assumes validated images from its caller.
