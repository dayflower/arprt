# API overview

ARPRT exposes three namespaced resources in `arprt.dayflower.github.io/v1alpha1`.
They describe rendering intent and device assignments for this independent,
unofficial TRMNL BYOS controller.

| Resource | Desired configuration (`spec`) | Recorded state (`status`) |
| --- | --- | --- |
| `ArprtScreen` | One HTTP or Job source, image freshness, image processing and optional overlays | Observed generation and reconciliation conditions |
| `ArprtDevice` | Authentication Secret reference, optional hardware identity, Screen/Playlist assignment, display overrides and content rotation | Registered identity, effective display capabilities and their sources, device observations, last-seen time, conditions and the last successful cycling turn |
| `ArprtPlaylist` | An ordered list of 1–64 Screen references, failure policy and an optional Skip render budget | Observed generation and reconciliation conditions |

A Device selects a Screen directly or selects a Playlist that references Screens.
If neither reference is present, it discovers Screens in name order. All resource
and Secret references, including discovery, stay in the same namespace. A Screen
can be reused by multiple Devices. Playlist entries can repeat the same Screen.

The [generated API reference](API_REFERENCE.md) lists fields, types, required
children, admission defaults and summarized constraints. Descriptions and
controller-selected defaults originate in [API type comments](../api/v1alpha1/types.go).
See [generation instructions](DEVELOP.md#api-documentation) before editing the reference.

## Minimal resources

This example selects an HTTP GET Screen through a Playlist. The endpoint and
names are synthetic. Create the named authentication Secret before device setup;
see [setup and credentials](OPERATIONS.md#setup-and-credentials) for key creation
and recovery. The Device omits hardware identity to register it on first setup.

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: ArprtScreen
metadata:
  name: photo
spec:
  source:
    http:
      url: http://renderer.example.test/image.png
---
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: ArprtPlaylist
metadata:
  name: living-room
spec:
  screens:
    - name: photo
---
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: ArprtDevice
metadata:
  name: living-room
spec:
  authSecretRef:
    name: living-room-auth
  playlistRef:
    name: living-room
```

For direct assignment, replace `playlistRef` with `screenRef: {name: photo}`.
For discovery, omit both. Do not set both references. HTTP POST and Job renderers
are described in the [renderer guide](RENDERERS.md), with additional
[resource examples](../examples/custom-resources).

## Reading configuration and state

Edit `spec` to express desired behavior; the controller maintains `status`.
Required fields in an optional nested object are required only when that object
is present. An omitted value can be defaulted by Kubernetes admission or resolved
by the controller; the reference distinguishes those cases.

Device `spec.display.safeAreaInsets` defines a recommended content rectangle in
logical viewing pixels. Renderers may honor it selectively, while controller
overlays always place their padded content inside it. Insets default to zero,
do not scale with overlay bitmaps and must leave positive resolved dimensions.
See [safe area](PROTOCOL.md#safe-area). Screen overlays have no margin setting.

For Screens, inspect conditions for reconciliation readiness. Image artifacts and
execution history are not embedded in Screen status. For Playlists, conditions
report reference validity; actual device progress belongs to Device status.

For Devices, `status.device` retains reported model, firmware and dimensions,
while `status.display` records effective capabilities and their provenance after
applying overrides and model profiles. Battery, network and last-seen values are
retained observations, not a guarantee that the device is currently online.
`status.screenCycle` records the last successful Playlist/discovery delivery for
replay and advancement; it is not a requested screen assignment.

Credentials are stored in Secrets, not in these status fields. Hardware MACs can
appear in a literal Device spec or registered Device status; keep real identities
and credentials out of checked-in manifests. See
[hardware identity](OPERATIONS.md#hardware-identity) for immutable identity sources.

Artifacts, cache keys, temporary renderer Jobs/Pods and renderer-owned state files
are managed internally rather than through additional public CRDs. See
[API boundaries](DESIGN.md#public-api-and-boundaries) and
[rendering and assignment](DESIGN.md#rendering-and-assignment) for behavior,
[operations](OPERATIONS.md) for recovery, and [protocol](PROTOCOL.md) for Render
Context and image compatibility.
