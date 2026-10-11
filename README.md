# ARPRT - An unofficial Kubernetes controller for TRMNL BYOS

Render your TRMNL screens with HTTP GET, POST or Kubernetes Jobs, and cycle
through them with ordered playlists or automatic discovery.

ARPRT is an independent, unofficial project. It is not affiliated with or
endorsed by TRMNL.

## Security and support boundary

Verified support is **TRMNL OG / firmware 1.8.17 / 800 × 480 gray4** on Linux,
Kubernetes **1.36.0+ within minor 1.36**, one namespace and one active controller.
Use a POSIX filesystem RWO PVC with same-node renderer mounts, atomic rename,
fsync and advisory locking. The supplied Deployment runs as UID/GID 1000 with
Recreate rollout and namespace-scoped RBAC. Job mode rejects RWOP, RWX, block
volumes and subtree mounts. Other devices/firmware, Kubernetes minors, storage
backends, multiple active replicas and multi-node failover are unverified.

Use TLS outside a trusted LAN, preserve device authentication headers, and grant
screen write access only to trusted operators: screen authors can select HTTP
endpoints, run containers and reference renderer Secrets. Network isolation is
operator-managed. Keep real hostnames, registry addresses, MACs and keys in private
configuration. See [operational security](notes/OPERATIONS.md#network-and-namespace-trust)
and [dated acceptance evidence](notes/DEVELOP.md#recorded-acceptance-baseline).

## Build and checks

Build with `make build` or `make docker-build IMAGE=arprt:dev`. Tooling, generation,
`make verify` and API/cluster checks are in [DEVELOP](notes/DEVELOP.md).

## Installation and pairing

The public resources are `ArprtDevice`, `ArprtScreen` and `ArprtPlaylist` in
`arprt.dayflower.github.io/v1alpha1`. This naming is for new installations;
upgrading or migrating an existing installation using the former resource names
is not supported. Install into a fresh namespace with a new artifact PVC.

Examples use `arprt` for both the namespace and external URL prefix. These are
independent: device URLs are `<publicBaseURL>/devices/<namespace>/<device-name>`.
Run commands from the repository root and substitute your private overlay path.

1. Build/push a cluster-accessible controller image. Create a private Kustomize
   overlay outside this repository referencing `config/default`, and set its image.
   Configure the PVC StorageClass if your cluster has no default.
2. Patch ConfigMap `arprt-config`'s `publicBaseURL` to your public URL including
   its prefix, for example `https://byos.example.com/arprt`. Expose Service
   `arprt:8080` through your TLS proxy. Preserve the prefix by default; if your
   proxy strips exactly `/arprt`, add `--proxy-strips-prefix=true`.
   The [Envoy Gateway example](config/overlays/envoy-gateway/README.md) shows the
   configuration with placeholder values.
3. Install the controller and minimal GET renderer:

   ```sh
   kubectl apply --validate=strict -k /path/to/private-overlay
   kubectl -n arprt rollout status deployment/arprt
   kubectl apply --validate=strict -k examples/http-get
   kubectl -n arprt rollout status deployment/example-renderer
   ```

4. The base declares `living-room-auth` without key data. Setup generates a
   missing `apiKey` and reuses an existing valid key. Keep generated keys out of
   Git: omit `data.apiKey` and `stringData.apiKey` from desired manifests.
   For a different `authSecretRef`, declare that Secret in the private overlay
   and add its name to the Role's `get` and `patch` rules.
5. Create a device. The default sample learns its MAC during first successful
   setup and assigns the `photo` GET screen:

   ```sh
   kubectl -n arprt apply --validate=strict -f config/samples/device.yaml
   ```

   [device-inline.yaml](config/samples/device-inline.yaml) and
   [device-secret.yaml](config/samples/device-secret.yaml) are alternatives for
   the same device. Choose the MAC mode before creation; changing it requires
   replacing the device. The default Role permits named `get` on
   `living-room-auth`/`living-room-identity` and `patch` on `living-room-auth`.
   Other Secret names need explicit rules; see [identity and credentials](notes/OPERATIONS.md#hardware-identity).
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
for the firmware constraints behind these requirements.

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
enabled by default and can be disabled with `--enable-setup=false`. Key rotation
requires setup again on the device. Back up live auth Secrets with the PVC and CRs;
see [GitOps and rotation](notes/OPERATIONS.md#setup-and-credentials).

## Renderer contract

| Source | Input and use |
| --- | --- |
| HTTP GET | Display target only; suitable for device-independent screens and fresh reuse |
| HTTP POST | Render Context with current device observations; suitable for device-status screens |
| Job | Immutable Context file plus image output path; suitable for an existing container CLI |

Renderers return compatible base PNGs; optional controller overlays add validated
2-bit bitmaps at delivery time without resampling or quantization.
GET freshness and Job scheduling use `refreshInterval`; POST forbids it. Device
polling is independent. Failures can serve compatible stale output, while an
initial missing image returns pending and preserves existing display content.
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
      horizontal: 12                 # left/right inset in bitmap pixels
      vertical: 4                    # top/bottom inset in bitmap pixels
```

A complete Screen example is in [screen-overlay.yaml](config/samples/screen-overlay.yaml).
Fixed mode defaults to top-right; `position` also accepts bottom-right,
bottom-left and top-left. Optional positive `scale` overrides automatic integer
scaling. Nonnegative `margin.horizontal` and `margin.vertical` (each default 0)
independently inset text/icons from the left/right and top/bottom edges in the
logical viewing orientation. Both scale with the bitmaps; white extends to the
corner edges. Timestamp displays `↻ YYYY-MM-DD HH:mm` for the image generation
time, using the 12×18 Home Video bitmap font; icons use matching 18×18 cells.
Wi-Fi strength appears as four rising vertical bars, with more bars filled for
stronger reception. Set the controller's `--overlay-time-zone` to an IANA name
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
See [playlist](config/samples/playlist.yaml),
[playlist device](config/samples/device-playlist.yaml) and
[discovery device](config/samples/device-discovery.yaml) samples.
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
| Configure, monitor, size, back up or troubleshoot | [OPERATIONS](notes/OPERATIONS.md) |
| Select or implement a renderer, run examples | [RENDERERS](notes/RENDERERS.md) |
| Build, generate, test or review acceptance evidence | [DEVELOP](notes/DEVELOP.md) |
| Change rendering, authentication, persistence or GC | [DESIGN](notes/DESIGN.md) |
| Change BYOS, Render Context or image compatibility | [PROTOCOL](notes/PROTOCOL.md) |

## License

This project is licensed under the [MIT License](LICENSE).
