# arprt-crds

CRDs for ARPRT, an independent, unofficial Kubernetes controller for TRMNL BYOS.
ARPRT is not affiliated with or endorsed by TRMNL.

This application chart installs only `ArprtDevice`, `ArprtScreen` and
`ArprtPlaylist` CRDs. It does not deploy the controller or create custom
resources. There are no configurable values. Kubernetes compatibility follows
the [project support boundary](../../README.md#security-and-support-boundary):
1.36.0+ within minor 1.36.

## Local installation

The commands below assume `helm` is installed on your `PATH`. From the repository
root, with a fresh cluster that does not already contain ARPRT CRDs:

```sh
helm install arprt-crds ./charts/arprt-crds \
  --namespace arprt --create-namespace --wait
```

The namespace stores Helm release records; the CRDs themselves are
cluster-scoped. Release names and namespaces never change CRD names or the
`arprt.dayflower.github.io` API group. Cluster-level CRD permissions are required.
Use one owner for these CRDs. Existing Kustomize-managed or differently owned
CRDs are not automatically adopted; ownership migration is outside the tested
installation path. Existing operators must read the
[legacy installation boundary](../../notes/OPERATIONS.md#legacy-installations). The [controller chart](../arprt/README.md) can include this
chart as a dependency or use this separately managed installation. Published
chart packages are a separate distribution milestone.

## Updates and retained resources

Compatible chart upgrades update CRD schemas through normal Helm templates:

```sh
helm upgrade arprt-crds ./charts/arprt-crds --namespace arprt --wait
helm uninstall arprt-crds --namespace arprt
```

Every CRD has `helm.sh/resource-policy: keep`, retaining it when uninstall or
another Helm operation would delete it. Custom resources remain because their
CRDs remain. This annotation does not prevent schema updates or make rollback
safe. Retained CRDs keep the original release ownership metadata. See
[OPERATIONS](../../notes/OPERATIONS.md#helm-managed-crds) for ownership, backup,
rollback and explicit removal.

## Generated schemas

Edit API types and markers in
[types.go](../../api/v1alpha1/types.go), then run `make manifests`.
`files/crds/` contains byte-for-byte copies of the canonical generated YAML in
`config/crd/bases`; do not edit either copy by hand. `templates/crds.yaml` reads
the files and adds only Helm management metadata and retention annotations.
Using `templates/` allows Helm to update schemas, unlike its special `crds/`
installation path. See [DEVELOP](../../notes/DEVELOP.md#crd-chart-generation-and-checks)
for synchronization and semantic checks. Those development checks install and
use the Makefile's pinned Helm client; this does not change your `helm` command
or establish compatibility with other client versions. Helm 3.21.4 and 4.3.0
are checked for standalone and bundled lifecycle behavior; see
[Helm checks](../../notes/DEVELOP.md#helm-chart-checks) for the full validation scope.
