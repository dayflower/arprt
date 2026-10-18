# arprt

ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS.
It is not affiliated with or endorsed by TRMNL.

This chart packages the controller and the [arprt-crds](../arprt-crds/README.md)
dependency. It creates no Namespace, Device, Screen, Playlist, renderer or Gateway.
Kubernetes support is 1.36.0+ within minor 1.36. The
[project support boundary](../../README.md#security-and-support-boundary) applies;
release-specific naming does not support multiple controllers in one namespace.
Device profiles and `spec.display` overrides are described in
[operations](../../notes/OPERATIONS.md#display-overrides); these are Device CR
settings rather than controller chart values. New model profiles remain source
reviewed/software tested pending physical acceptance.

## Local installation

Helm 4 is the supported controller installation path.
The commands below assume the verified Helm 4.3.0 client is installed as `helm`
on your `PATH`; Helm 3 is unsupported. For an accepted public distribution,
use the [OCI installation guide](../../README.md#published-chart-installation).
It requires no local dependency assembly. Until a release includes an accepted
`distribution.json`, use the local path below with the accepted public image.
From the repository root, prepare the dependency pinned by `Chart.lock`:

```sh
helm dependency build ./charts/arprt --skip-refresh
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

The dependency command assembles the bundled CRD chart from this checkout.
No chart packaging step is required for this local installation.
The Service/Deployment name is `<release>-arprt` (`home-arprt` above), truncated
to 58 characters with a trailing hyphen removed. The chart-created PVC appends
`-data` to that resolved name.
Expose Service port 8080 through your TLS proxy, preserving authentication
headers. Preserve the public prefix unless `byos.proxyStripsPrefix` is true.
Device URLs are `<publicBaseURL>/devices/<namespace>/<device-name>`.
Create devices and renderers separately following the
[pairing guide](../../README.md#installation-and-pairing) and
[renderer guide](../../notes/RENDERERS.md); adapt namespaces and Secret references.

The default image is the accepted [v0.1.0 release](https://github.com/dayflower/arprt/releases/tag/v0.1.0),
pinned to its `linux/amd64` and `linux/arm64` image index digest for local installs.
Published packages resolve these defaults to their own accepted release image
and index digest; see their `distribution.json`. A nonempty
`image.digest` always takes precedence over `image.tag`. Clear the digest to use
a tag; at least one must be nonempty.

## Separate CRD management

The default dependency belongs to the **parent release**, not an independent
CRD release. For separate management, install CRDs first on a fresh cluster and
disable the dependency from the first controller installation. Add this to the
same private values file shown above, and retain it on upgrades/reinstalls:

```yaml
crds:
  enabled: false
```

After preparing the controller dependency as above:

```sh
helm install arprt-crds ./charts/arprt-crds \
  --namespace arprt --create-namespace --wait
helm install home ./charts/arprt \
  --namespace arprt --wait \
  --values /path/to/private-values.yaml
```

CRD names remain cluster-wide regardless of release name/namespace. Switching
between bundled and separate ownership is a migration outside the tested path;
do not use dependency toggles to transfer ownership of an existing installation.
See [compatible upgrade operations](../../notes/OPERATIONS.md#compatible-upgrades)
for schema/controller ordering when the CRD release is separate.

## Values

[values.yaml](values.yaml) documents defaults; [values.schema.json](values.schema.json)
rejects unsupported settings/combinations. Runtime validation remains authoritative,
including IANA zone availability and existing PVC topology. Defaults follow
[OPERATIONS](../../notes/OPERATIONS.md#runtime-configuration).

| Value | Default | Purpose |
| --- | --- | --- |
| `crds.enabled` | `true` | Include CRDs in this release |
| `image.repository` | Release image repository | Local default: `ghcr.io/dayflower/arprt` |
| `image.tag` | Release image tag | Used only with an empty digest; local default: `v0.1.0` |
| `image.digest` | Release index SHA-256 | Takes precedence over tag; local default: v0.1.0 index |
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
| `resources` | 50m/64Mi requests, 500m/512Mi limits | Controller resources; `GOMEMLIMIT=384MiB` bounds the Go GC budget |
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
The operational procedures and validation limits belong in
[controller state operations](../../notes/OPERATIONS.md#helm-controller-state),
including [same-owner reinstall](../../notes/OPERATIONS.md#uninstall-and-reinstall)
and existing-PVC recovery. See
[CRD operations](../../notes/OPERATIONS.md#helm-managed-crds) for CRD ownership.
Helm rollback can also roll back the bundled schemas; it is not a verified
recovery or internal-record downgrade procedure.

## Development checks

Supply a valid URL for lint/render:

```sh
helm lint --strict charts/arprt --kube-version 1.36.0 \
  --set byos.publicBaseURL=https://byos.example.test/arprt
helm template home charts/arprt --namespace arprt \
  --kube-version 1.36.0 --values /path/to/private-values.yaml
```

For development verification, the Makefile installs and uses its own pinned
Helm client; see [DEVELOP](../../notes/DEVELOP.md#crd-chart-generation-and-checks)
for the currently checked client scope. Run `make crd-chart-check` before
assembling dependencies after schema changes.
`Chart.lock` pins the local dependency. Assembled `charts/` archives are ignored
build outputs. Package generation, output locations and distribution checks are
documented in [DEVELOP](../../notes/DEVELOP.md#helm-chart-checks).
`make chart-check` checks both charts and package contents;
`make helm-smoke` checks the full cluster lifecycle and all five software smoke
suites. See [Helm checks](../../notes/DEVELOP.md#helm-chart-checks)
for client selection and the validation boundary. OCI publication and anonymous
acceptance from published charts are gated by the
[distribution workflow](../../notes/DEVELOP.md#chart-release-preparation).

Image-processing resource acceptance uses the actual default controller limits;
see [integrated image acceptance](../../notes/DEVELOP.md#integrated-image-acceptance)
for native benchmark records, kernel peak/OOM checks and physical-support gates.
