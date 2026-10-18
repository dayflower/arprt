# ARPRT - An unofficial Kubernetes controller for TRMNL BYOS

Render your [TRMNL](https://trmnl.com/) screens with HTTP GET, POST or Kubernetes
Jobs, and cycle through them with ordered playlists or automatic discovery.

ARPRT is an independent, unofficial project. It is not affiliated with or
endorsed by TRMNL.

## Security and support boundary

The accepted public release's verified physical baseline is
**TRMNL OG / firmware 1.8.17 / 800 × 480 gray4** on Linux,
Kubernetes **1.36.0+ within minor 1.36**, one namespace and one active controller.
Use a POSIX filesystem RWO PVC with same-node renderer mounts, atomic rename,
fsync and advisory locking. The supplied Deployment runs as UID/GID 1000 with
Recreate rollout and namespace-scoped RBAC. Job mode rejects RWOP, RWX, block
volumes and subtree mounts. Other devices/firmware, Kubernetes minors, storage
backends, multiple active replicas and multi-node failover are unverified.

Runtime model profiles include OG and reTerminal E1001–E1004 with gray4, gray16
and Spectra 6 outputs. These profiles are source reviewed and software tested;
full physical acceptance remains pending. E1002 has
[initial six-color and delivery observations](notes/DEVELOP.md#image-processing-physical-observations).
Remaining hardware sessions are tracked in
[physical checks](notes/DEVELOP.md#remaining-image-processing-physical-checks).
Native four-bit gray4 output assumes corrected firmware expansion: physical
testing confirmed corrupted grayscale on OG / 1.8.17. Normal builds temporarily
use two-bit gray4 for all four shades; the four-bit path remains behind a
[build flag](notes/DEVELOP.md#temporary-gray4-encoding-switch) pending firmware
migration. The temporary mode accepts the historical two-bit color-count bug.
See [profile defaults](notes/DESIGN.md#firmware-evidence-and-profiles)
and [Device display overrides](notes/OPERATIONS.md#display-overrides). Firmware
versions do not gate target resolution; overrides do not expand verified support.

Use TLS outside a trusted LAN, preserve device authentication headers, and grant
screen write access only to trusted operators: screen authors can select HTTP
endpoints, run containers and reference renderer Secrets. Network isolation is
operator-managed. Keep real hostnames, registry addresses, MACs and keys in private
configuration. See [operational security](notes/OPERATIONS.md#network-and-namespace-trust)
and [dated acceptance evidence](notes/DEVELOP.md#recorded-acceptance-baseline).

## Build and checks

Build with `make build` or `make docker-build IMAGE=arprt:dev`. Tooling, generation,
`make verify` and API/cluster checks are in [DEVELOP](notes/DEVELOP.md).
CI runs envtest on Kubernetes 1.36.0 to check the supported minimum version;
patch releases within minor 1.36 remain supported without separate envtest runs.

Helm 4 is the sole supported controller installation and compatible-upgrade path.
The [controller chart](charts/arprt/README.md) bundles CRDs by default; the
[CRD-only chart](charts/arprt-crds/README.md) also supports separate management.
Helm 4.3.0 is verified on Kubernetes 1.36; other Helm 4 versions are unverified
and Helm 3 is unsupported. Chart OCI publication and anonymous public-chart
acceptance remain pending; use this checkout's charts with the accepted public
controller image. See
[CRD lifecycle operations](notes/OPERATIONS.md#helm-managed-crds) before changing
ownership or deleting retained CRDs.

## Installation and pairing

The public resources are `ArprtDevice`, `ArprtScreen` and `ArprtPlaylist` in
`arprt.dayflower.github.io/v1alpha1`. Use a fresh cluster without ARPRT CRDs and a
fresh namespace/PVC for initial installation. Existing installations must follow
[the legacy installation boundary](notes/OPERATIONS.md#legacy-installations)
before attempting Helm adoption; automatic ownership migration and upgrades from
former resource names are unverified.

Examples use `arprt` for both namespace and external URL prefix. These are
independent: device URLs are `<publicBaseURL>/devices/<namespace>/<device-name>`.
Run commands from the repository root with a checked Helm client on `PATH`.
The installer needs cluster-level CRD permissions as well as permissions to
create the namespaced chart resources; the running controller uses only
namespace-scoped RBAC.

1. Use this checkout's chart sources and their pinned controller image.
   Prepare the controller chart's locked CRD dependency:

   ```sh
   helm dependency build ./charts/arprt --skip-refresh
   ```

   Controller image records are available in
   [Releases](https://github.com/dayflower/arprt/releases). The chart defaults
   to the accepted public v0.1.0 multi-platform image index
   for `linux/amd64` and `linux/arm64`. No registry login or image build is needed.
   For another compatible accepted image, set `image.repository` and
   `image.digest` from its `image-digests.json`. A digest takes precedence over
   `image.tag`; clear it explicitly to select a tag. No `latest` tag is required.
2. Create `/path/to/private-values.yaml` outside the repository:

   ```yaml
   byos:
     publicBaseURL: https://byos.example.com/arprt
   credentials:
     authSecrets:
       - name: living-room-auth
         create: true
   ```

   Configure `persistence.storageClass` if the cluster has no default.
   Install the controller chart from this checkout:

   ```sh
   helm install home ./charts/arprt \
     --namespace arprt --create-namespace --wait \
     --values /path/to/private-values.yaml
   ```

   One operation installs the CRDs and controller. For separately managed CRDs,
   follow the [chart guide](charts/arprt/README.md#separate-crd-management) from
   the first installation. Expose Service `home-arprt:8080` through a TLS proxy.
   Preserve the prefix by default; set `byos.proxyStripsPrefix: true` if the proxy
   strips exactly that prefix. See the [Gateway example](examples/networking/envoy-gateway/README.md).
3. Install the minimal GET renderer and screen outside the Helm release:

   ```sh
   kubectl -n arprt create configmap example-renderer \
     --from-file=renderer.py=examples/renderers/http/gray-bands/renderer.py
   kubectl -n arprt apply --validate=strict -f examples/renderers/http/gray-bands/renderer.yaml \
     -f examples/renderers/http/gray-bands/screen.yaml
   kubectl -n arprt rollout status deployment/example-renderer
   ```

4. The selected auth entry declares `living-room-auth` without key data. Setup
   generates a missing `apiKey` and reuses a valid key. Keep generated keys out
   of values and Git. For other auth Secret names, list each name explicitly in
   `credentials.authSecrets`; use `create: false` for an external Secret.
5. Create a device. The default sample learns its MAC during first setup and
   assigns the `photo` GET screen:

   ```sh
   kubectl -n arprt apply --validate=strict -f examples/custom-resources/device.yaml
   ```

   [device-inline.yaml](examples/custom-resources/device-inline.yaml) and
   [device-secret.yaml](examples/custom-resources/device-secret.yaml) are alternatives for
   the same device. Choose the MAC mode before creation; changing it requires
   replacing the device. For a Secret identity source, provision it externally
   and add its name to `credentials.identitySecrets` for named read permission.
   See [identity and credentials](notes/OPERATIONS.md#hardware-identity).
6. Set TRMNL's **Custom Server URL**, without a trailing slash, to:

   ```text
   https://byos.example.com/arprt/devices/arprt/living-room
   ```

   Firmware appends `/api/setup` and `/api/display`. Setup returns the saved key
   and an anonymous calibration PNG. Setup stays enabled by default; repeated
   setup for the registered MAC reuses the key. No pairing rollout is needed.
7. Confirm four gray bands on the device and inspect status:

   ```sh
   kubectl -n arprt get arprtdevices,arprtscreens
   kubectl -n arprt get arprtdevice living-room -o yaml
   ```

Preserve `ID`/`Access-Token` headers and avoid logging them or setup response
bodies. Generated image URLs use the configured public origin and exact device
base path; forwarded headers cannot change them. Keep the image route free of
redirects/compression. See [PROTOCOL](notes/PROTOCOL.md#routes-and-authentication)
for firmware constraints. For compatible upgrades, backup and retained-resource
recovery, follow [OPERATIONS](notes/OPERATIONS.md#helm-controller-state).

### Separate CRD management

The quickstart's CRDs belong to release `home`. To manage schemas independently,
install the `arprt-crds` chart as its own release first and set
`crds.enabled: false` in the controller's private values from its first install.
Follow the [separate installation commands](charts/arprt/README.md#separate-crd-management).
Keep that value for every controller upgrade and reinstall. Controller operations
then leave the independently owned CRDs unchanged; compatible schema upgrades
are a separate operator action. Changing the dependency flag on an existing
release does not transfer CRD ownership. See
[CRD lifecycle operations](notes/OPERATIONS.md#helm-managed-crds).

## Hardware identity

Use a direct `hardwareID`, a same-namespace `hardwareIDSecretRef`, or omit both
for first-setup registration. The options are mutually exclusive. Source selection,
literal MAC and reference name/key are immutable; changing hardware requires a
replacement device. A missing/invalid identity Secret never enables automatic
learning. MACs must be unique within the namespace. See
[identity configuration](notes/OPERATIONS.md#hardware-identity) for YAML and recovery.

## Setup and credentials

The device and its named auth Secret must exist before setup. Setup generates
only an absent key; it never replaces an existing empty/invalid value. It stays
enabled by default and can be disabled with `byos.setupEnabled: false` in Helm
values. Key rotation requires setup again on the device. Back up live auth
Secrets with the PVC and CRs;
see [GitOps and rotation](notes/OPERATIONS.md#setup-and-credentials).

## Renderer contract

| Source | Input and use |
| --- | --- |
| HTTP GET | Display target only; suitable for device-independent screens and fresh reuse |
| HTTP POST | Render Context with current device observations; suitable for device-status screens |
| Job | Immutable Context file plus image output path; suitable for an existing container CLI |

Renderers can return general PNG inputs, including RGB, alpha and APNG default
images. The controller normalizes to the resolved Device display using Screen
`imageProcessing.resizeMode` (`contain`/`stretch`) and `imageProcessing.dithering`
(`atkinson`/`floyd-steinberg`/`bayer`/`none`); defaults are contain/Atkinson.
Compatible renderer bytes and dithering are preserved. Optional bitmap overlays
are drawn afterwards. Generated gray4 temporarily uses native two-bit PNG;
the opt-in four-bit build assumes corrected firmware expansion and needs new
physical acceptance, separate from the historical OG / 1.8.17 baseline.
GET freshness and Job scheduling use `refreshInterval`; POST forbids it. Device
polling is independent. Failures can serve compatible stale output, while an
initial missing image returns pending. Firmware can retain the previous image
on a normal timer wake, but may show its Friendly ID screen after a manual or
power wake; see [pending behavior](notes/PROTOCOL.md#responses-and-polling).
See [RENDERERS](notes/RENDERERS.md) for installation/execution examples and
[PROTOCOL](notes/PROTOCOL.md#png-contract) for exact image requirements.

## Display overlays

A Screen can add its original render timestamp and current battery/Wi-Fi icons:

```yaml
spec:
  overlay:
    enabled: true
    items: [timestamp, battery, wifi]  # omitted means all; [] means none
    cycleCorners: true               # omit position when cycling
    margin:                          # white extends to the corner edges
      top: 4                         # logical edge insets in bitmap pixels
      bottom: 8
      left: 12
      right: 16
```

A complete Screen example is in [screen-overlay.yaml](examples/custom-resources/screen-overlay.yaml).
Fixed mode defaults to top-right; `position` also accepts bottom-right,
bottom-left and top-left. Optional positive `scale` overrides automatic integer
scaling. Nonnegative `margin.top`, `.bottom`, `.left` and `.right` (each default 0)
independently inset the padded content row from each logical viewing edge. Only the two
edges adjoining the selected corner apply, scaling with the bitmaps; white
extends to those edges. The content row has a fixed 2px white padding on all
four sides, scaled with the bitmaps. Timestamp displays `↻ YYYY-MM-DD HH:mm` for the image generation
time, using the 12×18 Home Video bitmap font; icons use matching 18×18 cells.
Wi-Fi strength appears as four rising vertical bars, with more bars filled for
stronger reception. Set `display.overlayTimeZone` in Helm values to an IANA name
(default UTC).
Missing current measurements hide their icons; stored telemetry does not fill
missing overlay inputs. Only a valid current charging state selects a charging
mark; USB connection does not imply charging. OG / 1.8.17 supplies neither power
flag. Replay keeps its already committed image. See
[OVERLAYS](notes/OVERLAYS.md), [renderer usage](notes/RENDERERS.md#controller-display-overlays)
and [operations](notes/OPERATIONS.md#runtime-configuration).
Software compositing checks and physical-device readability/orientation checks
are described separately in [DEVELOP](notes/DEVELOP.md#physical-device-regression).

## Playlists and automatic discovery

Use `screenRef` for one screen, `playlistRef` for an ordered list, or omit both
for name-sorted discovery in the device namespace. GET, POST and Job can be mixed.
For example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: ArprtPlaylist
metadata:
  name: living-room
spec:
  screens:
    - name: photo
    - name: device-status
    - name: weather
```

Set `spec.playlistRef.name` to `living-room` and remove `screenRef`.
See [playlist](examples/custom-resources/playlist.yaml),
[playlist device](examples/custom-resources/device-playlist.yaml) and
[discovery device](examples/custom-resources/device-discovery.yaml) samples.
Playlists contain 1–64 distinct names; discovery uses the first 64 screens.
Missing playlist entries are skipped. No eligible screens means pending.

Cycling advances after a successful image response, including compatible stale
output. A screen with no valid image holds its turn. Early polls within the
configured device-refresh window replay the same image, including across restart;
POST renders on a new turn. A single `screenRef` retains per-request POST rendering.
Membership/order changes reset to the first eligible screen. See
[assignment and replay](notes/DESIGN.md#rendering-and-assignment) for full semantics.

## Documentation

| Task | Guide |
| --- | --- |
| Browse API configuration, renderer and networking examples | [Examples](examples/README.md) |
| Configure, monitor, size, back up or troubleshoot | [OPERATIONS](notes/OPERATIONS.md) |
| Select or implement a renderer, run examples | [RENDERERS](notes/RENDERERS.md) |
| Build, generate, test or review acceptance evidence | [DEVELOP](notes/DEVELOP.md) |
| Change rendering, authentication, persistence or GC | [DESIGN](notes/DESIGN.md) |
| Change BYOS, Render Context or image compatibility | [PROTOCOL](notes/PROTOCOL.md) |

## License

This project is licensed under the [MIT License](LICENSE).
