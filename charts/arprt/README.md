# arprt

ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS.
It is not affiliated with or endorsed by TRMNL.

This chart packages the controller and the [arprt-crds](../arprt-crds/README.md)
dependency. It creates no Namespace, Device, Screen, Playlist, renderer or Gateway.
Kubernetes support is 1.36.0+ within minor 1.36. The
[project support boundary](../../README.md#security-and-support-boundary) applies;
release-specific naming does not support multiple controllers in one namespace.

## Local installation

Use a fresh installation. Existing Kustomize resources and differently owned
CRDs are not automatically adopted. The commands below assume `helm` is
installed on your `PATH`. From the repository root:

```sh
helm dependency build charts/arprt --skip-refresh
```

Create private values outside the repository, replacing the public URL:

```yaml
byos:
  publicBaseURL: https://byos.example.com/arprt
credentials:
  authSecrets:
    - name: living-room-auth
      create: true
```

```sh
helm install home ./charts/arprt \
  --namespace arprt --create-namespace --wait \
  --values /path/to/private-values.yaml
```

The Service/Deployment are `<release>-arprt` (`home-arprt` above), and the PVC
is `<release>-arprt-data`. Long names are truncated before suffixes are added.
Expose Service port 8080 through your TLS proxy, preserving authentication
headers. Preserve the public prefix unless `byos.proxyStripsPrefix` is true.
Device URLs are `<publicBaseURL>/devices/<namespace>/<device-name>`.
Create devices and renderers separately following the
[pairing guide](../../README.md#installation-and-pairing) and
[renderer guide](../../notes/RENDERERS.md); adapt namespaces and Secret references.

The default image is the accepted [v0.1.0 release](https://github.com/dayflower/arprt/releases/tag/v0.1.0),
pinned to its `linux/amd64` and `linux/arm64` image index digest. A nonempty
`image.digest` always takes precedence over `image.tag`. Clear the digest to use
a tag; at least one must be nonempty.

## Separate CRD management

The default dependency belongs to the **parent release**, not an independent
CRD release. For separate management, install CRDs first on a fresh cluster and
disable the dependency from the first controller installation:

```sh
helm install arprt-crds ./charts/arprt-crds \
  --namespace arprt --create-namespace --wait
helm install home ./charts/arprt \
  --namespace arprt --wait --set crds.enabled=false \
  --values /path/to/private-values.yaml
```

CRD names remain cluster-wide regardless of release name/namespace. Switching
between bundled and separate ownership is a migration outside the tested path;
do not use dependency toggles to transfer ownership of an existing installation.

## Values

[values.yaml](values.yaml) documents defaults; [values.schema.json](values.schema.json)
rejects unsupported settings/combinations. Runtime validation remains authoritative,
including IANA zone availability and existing PVC topology. Defaults follow
[OPERATIONS](../../notes/OPERATIONS.md#runtime-configuration).

| Value | Default | Purpose |
| --- | --- | --- |
| `crds.enabled` | `true` | Include CRDs in this release |
| `image.repository` | `ghcr.io/dayflower/arprt` | Image repository |
| `image.tag` | `v0.1.0` | Used only with an empty digest |
| `image.digest` | v0.1.0 index SHA-256 | Takes precedence over tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `imagePullSecrets` | `[]` | Existing registry Secret references (`name`) |
| `byos.publicBaseURL` | **required** | Absolute HTTP(S) URL with prefix; no userinfo, query, fragment or escaped/noncanonical path |
| `byos.proxyStripsPrefix` | `false` | Proxy strips exactly the public prefix |
| `byos.setupEnabled` | `true` | Setup and missing-key provisioning |
| `display.deviceRefreshSeconds` | `300` | Polling interval, 1–86400 seconds |
| `display.overlayTimeZone` | `UTC` | Explicit IANA zone; no `Local` or fixed offsets |
| `persistence.existingClaim` | `""` | External same-namespace filesystem RWO claim |
| `persistence.storageClass` | `null` | Cluster default; `""` explicitly requests no StorageClass |
| `persistence.size` | `1Gi` | Chart-created PVC capacity |
| `storage.maxBytes` | `805306368` | Managed-byte admission limit |
| `storage.minFreeBytes` | `67108864` | Free-space reserve |
| `jobs.enabled` | `true` | Schedule renderer Jobs |
| `jobs.concurrency` | `4` | Active Job limit |
| `jobs.deadlineSeconds` | `120` | Job execution deadline |
| `credentials.authSecrets` | `[]` | Explicit `name`/`create` objects; named `get`/`patch` permission |
| `credentials.identitySecrets` | `[]` | External names; `get` permission only |
| `resources` | 50m/64Mi requests, 500m/256Mi limits | Controller resources |
| `nodeSelector`, `tolerations` | `{}`, `[]` | Pod scheduling |
| `service.type`, `service.annotations` | `ClusterIP`, `{}` | BYOS Service configuration |
| `metrics.enabled` | `true` | Internal port 8082; never added to the BYOS Service |

For existing claims, leave `storageClass` and `size` at their defaults and manage
them externally. A whole-filesystem RWO PVC is mounted at `/var/lib/arprt` even
with Jobs disabled. Ephemeral, RWOP, RWX, block and subtree mounts are unsupported.
One replica, Recreate rollout, Pod/watched namespace equality, UID/GID 1000 and
the supplied security contexts are fixed. ConfigMap changes trigger a rollout
through the Pod checksum.

Auth entries with `create: true` declare only metadata and `Opaque` type; setup
fills a missing key. With `create: false`, provision the Secret externally.
Identity Secrets are always external. No keys or MACs belong in values. Registry
Secrets do not grant direct Secret read permissions. The controller has neither
blanket Secret access nor permission to create Secrets.

Chart-created PVCs and auth Secrets carry `helm.sh/resource-policy: keep`.
Existing claims and external Secrets are neither created nor owned by the chart.
Retention does not replace a consistent backup of PVC, CRs and auth Secrets or
establish safe rollback. Removing an auth entry or changing `create` to false
keeps the live Secret/key; removing the entry also removes its RBAC permission.
The retained object keeps its original release ownership annotations. Use the
same release name/namespace to resume management, and keep the namespace itself.
See [controller state operations](../../notes/OPERATIONS.md#helm-controller-state)
for compatible reinstall, existing-PVC recovery and validation limits, and
[CRD operations](../../notes/OPERATIONS.md#helm-managed-crds) for CRD ownership.
API-server tests verify generated-key/PVC UID retention and named Secret access.
The full deployment, delivery and client matrix remains a separate validation
milestone; these checks do not establish migration or downgrade support.

## Validation and packaging

Supply a valid URL for lint/render:

```sh
helm lint --strict charts/arprt --kube-version 1.36.0 \
  --set byos.publicBaseURL=https://byos.example.test/arprt
helm template home charts/arprt --namespace arprt \
  --kube-version 1.36.0 --values /path/to/private-values.yaml
helm package charts/arprt --destination /tmp/arprt-packages
```

For development verification, the Makefile installs and uses its own pinned
Helm client; see [DEVELOP](../../notes/DEVELOP.md#crd-chart-generation-and-checks)
for the currently checked client scope. Run `make crd-chart-check` before
assembling dependencies after schema changes.
`Chart.lock` pins the local dependency. Assembled `charts/` archives are ignored
build outputs; always run `dependency build` before packaging. The parent package
contains the dependency, so installation needs neither local chart sources nor
dependency resolution. Publication and the full automated lifecycle/client
matrix are separate milestones.
