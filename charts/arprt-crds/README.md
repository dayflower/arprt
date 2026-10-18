# arprt-crds

CRDs for ARPRT, an independent, unofficial Kubernetes controller for TRMNL BYOS.
ARPRT is not affiliated with or endorsed by TRMNL.

This application chart installs only `ArprtDevice`, `ArprtScreen` and
`ArprtPlaylist` CRDs. It does not deploy the controller or create custom
resources. There are no configurable values. Kubernetes compatibility follows
the [project support boundary](../../README.md#security-and-support-boundary):
1.36.0+ within minor 1.36.

## Local installation

Helm 4 is required; Helm 4.3.0 is verified and Helm 3 is unsupported. The commands
below assume the verified client is installed as `helm` on your `PATH`.
For a release with an accepted `distribution.json`, use
`oci://ghcr.io/dayflower/charts/arprt-crds` and its recorded CRD chart version
with `--version`, following the [OCI guide](../../README.md#published-chart-installation).
The CRD version may differ from the parent version; unchanged CRD packages can
be reused. Until that acceptance exists, use the local path below.
From the repository root, with a fresh cluster that does not already contain
ARPRT CRDs, install directly from the chart directory:

```sh
helm install arprt-crds ./charts/arprt-crds \
  --namespace arprt --create-namespace --wait
```

The namespace stores Helm release records; the CRDs themselves are
cluster-scoped. Release names and namespaces never change CRD names or the
`arprt.dayflower.github.io` API group. Cluster-level CRD permissions are required.
Use one Helm release to own these CRDs.
The [controller chart](../arprt/README.md#separate-crd-management) can include this
chart as a dependency or use this separately managed installation. In the latter
case, keep `crds.enabled: false` in the controller's private values throughout
its lifecycle. The standalone CRD chart has no dependencies to assemble.

## Updates and retained resources

Compatible chart upgrades update CRD schemas through normal Helm templates:

```sh
helm upgrade arprt-crds ./charts/arprt-crds --namespace arprt --wait
```

Every CRD has `helm.sh/resource-policy: keep`, retaining it when uninstall or
another Helm operation would delete it. Custom resources remain because their
CRDs remain. This annotation does not prevent schema updates or make rollback
safe. Retained CRDs keep the original release ownership metadata. See
[OPERATIONS](../../notes/OPERATIONS.md#helm-managed-crds) for ownership, backup,
rollback and explicit removal.

For separate management, upgrade compatible schemas before a controller that
requires them, following the
[upgrade procedure](../../notes/OPERATIONS.md#compatible-upgrades). With bundled
management, upgrade the parent release; the dependency is part of that same
release, including rollback. To uninstall a standalone CRD release while retaining
its schemas and custom resources:

```sh
helm uninstall arprt-crds --namespace arprt --wait
```

Reinstall the compatible chart with the same release name and namespace to
resume ownership. Uninstall does not stop controllers or delete their PVCs;
coordinate controller lifecycle separately. Do not delete the release namespace
as a substitute for uninstall.

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
or establish compatibility with other client versions. Helm 4.3.0 is verified
for standalone and bundled lifecycle behavior; see
[Helm checks](../../notes/DEVELOP.md#helm-chart-checks) for the full validation scope.
