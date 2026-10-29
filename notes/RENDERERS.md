# Renderer guide

Use this guide to select a renderer and run the supplied examples. Commands run
from the repository root and assume a paired `arprt/living-room` device using
`screenRef`. When switching from a playlist, remove `playlistRef` before assigning
`screenRef`; the two references are mutually exclusive. See [installation and
pairing](../README.md#installation-and-pairing) for initial setup.
The [example index](../examples/README.md) groups API configuration, HTTP/Job
renderers and networking integrations by purpose.

These examples are installed separately from the controller Helm release. They
use namespace `arprt`; adapt every command and same-namespace reference when
using another namespace. Renderer Service names such as `example-renderer` stay
as declared in their manifests; the controller's release-specific Service name
does not replace them. Job examples require `jobs.enabled: true` and the
controller's whole-filesystem RWO PVC. See
[controller values](../charts/arprt/README.md#values) for Job limits and storage.

## Choosing a source

| Source | Use when | Delivery behavior |
| --- | --- | --- |
| HTTP GET | Output depends on the screen and display target including rotation, without device telemetry | Render on demand; `refreshInterval` bounds fresh reuse |
| HTTP POST | Output needs device name, model/firmware or current telemetry | Render synchronously for each single-screen request or new cycling turn |
| Scheduled Job | An existing container CLI writes an image asynchronously | Render on an interval; display serves completed output |
| OnDemand Job | A fast container CLI needs current display observations | Render on each single-screen display or new cycling turn; concurrent single-screen requests share work |

Device polling is configured separately from screen freshness. POST and OnDemand forbid
`refreshInterval`; playlist/discovery can replay a recorded turn without rendering
again. Scheduled Job telemetry is a scheduling snapshot and may be older at display time.
On failure, compatible prior output can remain visible; without one, display is
pending. See [rendering and assignment](DESIGN.md#rendering-and-assignment) for
reuse, replay, ordering and fallback rules.

Renderers may return general RGB/RGBA, grayscale or indexed PNG inputs within
the [input limits](PROTOCOL.md#png-contract). The controller normalizes them to
the resolved Device dimensions/palette/budget, then draws optional bitmap overlays.
See [Device profiles and overrides](OPERATIONS.md#display-overrides); all source
types receive the same effective renderer target. Model/firmware and value
provenance do not fragment equivalent GET capabilities.
Compatible PNGs retain their original bytes and renderer-authored dithering.
The bundled gray-bands and device-status examples keep their OG scope. Their
two-bit intermediate-gray output passes through by default. The
[`gray4_native4` build](DEVELOP.md#temporary-gray4-encoding-switch) converts it
exactly to native four-bit gray4 before delivery.

For a renderer returning an RGB PNG from an image library, configure the Screen:

```yaml
spec:
  imageProcessing:
    resizeMode: contain  # default; stretch fills the target without preserving aspect ratio
    dithering: atkinson # default; floyd-steinberg, bayer and none are also available
```

Transparency is composed onto white. Input dimensions may differ from the target;
renderers should still honor the target query/Context and own content rotation.
`none` disables dithering while retaining resizing and palette conversion.
Invalid files remain failures; conversion never changes the selected dithering
to fit the output budget. The exact HTTP headers, query merge, execution budget,
Context fields and output depth rules belong to
[PROTOCOL](PROTOCOL.md#http-renderer-contract).

## Controller display overlays

For timestamp/battery/Wi-Fi display without adding device telemetry to a shared
GET image, set the Screen's `spec.overlay.enabled: true`. Select items with
`items: [timestamp, battery, wifi]`, or omit `items` for all three. `items: []`
draws nothing. Use `cycleCorners: true` to share the device UID's durable clockwise
corner cycle across Screens; omit `position` in that mode. Fixed mode defaults
to top-right. An optional positive `scale` overrides automatic integer scaling.
Use nonnegative `margin.top`, `.bottom`, `.left` and `.right` (each default 0) to
independently inset the padded content row from each logical viewing edge. Top-right uses
`top` and `right`; bottom-left uses `bottom` and `left`, with the same rule for
the other corners. The content row has fixed 2px white padding on every side, scaled with the
bitmaps. The white background reaches those two edges.
The timestamp displays `↻ YYYY-MM-DD HH:mm` for image generation time. Home Video
text cells are 12×18; the arrow and other icons use 18×18 cells matching the font
height. Charging replaces the level icon only when current charging is known
true; USB connection does not imply charging. These power flags stay outside
Render Context.
See [overlay settings](OVERLAYS.md) for thresholds, layout and missing inputs.

Keep these same indicators out of renderer output when controller overlays are
enabled, to avoid displaying them twice. The POST device-status example already
displays voltage/RSSI as content; choose the controller timestamp alone for that
example if you want only a render-time annotation. Job content uses its immutable
snapshot while the overlay uses current display telemetry. Renderer rotation
still owns the base; controller drawing rotates only its own bitmap coordinates.

## Device rotation

Set `ArprtDevice.spec.rotation` to `0`, `90`, `180` or `270` (clockwise degrees;
omitted means `0`). For example:

```sh
kubectl -n arprt patch arprtdevice living-room --type=merge \
  -p '{"spec":{"rotation":90}}'
```

GET/POST receive the `rotation` query parameter. POST and Job also receive
`renderTarget.rotation` in Render Context; zero is omitted and should default to
`0`. Renderers handle layout and rotation using the physical target dimensions.
The OG-only examples return 800 × 480 PNGs.
The HTTP examples support all four rotations. The BusyBox Job example copies a
fixed input PNG and does not rotate it; use a renderer that reads the setting
when output must follow device rotation. A rotation change requires compatible
output for that angle before delivery can resume.

## HTTP GET example

The [minimal Python renderer](../examples/renderers/http/gray-bands/renderer.py) draws four gray
bands using only the standard library and needs no Kubernetes API access.
[Its screen](../examples/renderers/http/gray-bands/screen.yaml) selects `photo`; Kubernetes install
commands are in the README quickstart. For standalone use:

```sh
python3 examples/renderers/http/gray-bands/renderer.py --port 8000
```

The endpoint is `/render`; the controller supplies the target query. Append
`?encoding=indexed` to the screen URL to exercise indexed output. Non-target
query values are preserved. Grayscale and indexed output have synthetic fixtures
under [PNG fixtures](../testdata/png/README.md).

## Edge calibration Screen

The [calibration renderer](../examples/renderers/http/calibration/renderer.py)
draws a target-sized, pure black/white calibration pattern using Python's
standard library. It supports all four logical rotations and dimensions from
400 to 4096 pixels per side, with at most 4194304 pixels. The output is an
eight-bit grayscale input; the controller converts it to the device palette.
The [Screen](../examples/renderers/http/calibration/screen.yaml) selects no
dithering, preserving the exact black/white pixel locations.

```sh
kubectl -n arprt create configmap calibration-renderer \
  --from-file=renderer.py=examples/renderers/http/calibration/renderer.py
kubectl -n arprt apply --validate=strict \
  -f examples/renderers/http/calibration/renderer.yaml \
  -f examples/renderers/http/calibration/screen.yaml
```

`edge-calibration` enables the controller's timestamp, battery and Wi-Fi overlay
with `cycleCorners: true`. The renderer draws no overlay or telemetry. One
corner is covered at a time; use the remaining edges/corners, or observe the next
ordinary update. Corner cycling is separate from Device content rotation.

The pattern includes frames at 0, 4, 8, 12, 16, 24 and 32 pixels from every
logical edge. Distance 0 is the outermost pixel row/column. Each edge has a
0–40 pixel ruler with alternating one-pixel steps, five-pixel marks and labels
every ten pixels. Direction/corner labels and one-/two-pixel checkerboards help
identify rotation and unintended scaling. White label backgrounds locally
interrupt frame lines beside the rulers.

The initial four Screen margins are zero; the controller still adds its fixed
two-pixel padding around the content row. Change `spec.overlay.margin.top`,
`.bottom`, `.left` and `.right` while measuring. These are unscaled bitmap
pixels; choose `scale: 1` when comparing a nondefault overlay scale with the
single-pixel ruler. The static base may be cached for 24 hours; overlay changes
and corner selection remain controller-owned.

Generate a local preview without a server:

```sh
python3 examples/renderers/http/calibration/renderer.py --output /tmp/edge-calibration.png
python3 -m unittest discover -s tools -p 'test_calibration_renderer.py' -v
```

## RGB and indexed Spectra example

The [palette renderer](../examples/renderers/http/palette/renderer.py) supports
GET, POST and a file-based CLI using only Python's standard library. Install
[its Deployment and Screens](../examples/renderers/http/palette/renderer.yaml)
with the [screen definitions](../examples/renderers/http/palette/screens.yaml):

```sh
kubectl -n arprt create configmap palette-renderer \
  --from-file=renderer.py=examples/renderers/http/palette/renderer.py
kubectl -n arprt apply --validate=strict -f examples/renderers/http/palette/renderer.yaml \
  -f examples/renderers/http/palette/screens.yaml
```

`palette-rgb` returns a small RGB gradient with off-palette samples and a different
aspect ratio. The controller applies the Screen's contain/stretch and dithering
policy to the effective target. `palette-spectra` returns target-sized indexed
four-bit bands, including renderer-owned clockwise rotation, with this exact PLTE:

| Index | Color | RGB |
| --- | --- | --- |
| 0 | Black | `(0, 0, 0)` |
| 1 | White | `(255, 255, 255)` |
| 2 | Yellow | `(255, 243, 56)` |
| 3 | Red | `(191, 0, 0)` |
| 4 | Blue | `(100, 64, 255)` |
| 5 | Green | `(67, 138, 28)` |

Assign the indexed Screen only to a resolved Spectra target. Compatible indexed
Spectra at depths 1/2/4/8 preserves bytes and renderer-authored dithering regardless
of the controller's selected dithering. Subsets and arbitrary entry order work;
every declared entry, including unused entries, must exactly match the table.
All pixels must reference declared entries. Output must also be opaque, static,
noninterlaced, match the physical dimensions and fit the exclusive device budget.
Nearby colors, animation/transparency, incompatible dimensions or an unused
off-palette entry require normalization. See the complete
[PNG contract](PROTOCOL.md#png-contract).

For standalone files, including a Job renderer's mounted Context/output paths:

```sh
python3 examples/renderers/http/palette/renderer.py \
  --context testdata/render-context/render-context-spectra6.json \
  --output /tmp/spectra.png --encoding indexed
python3 examples/renderers/http/palette/renderer.py \
  --context testdata/render-context/render-context-gray16.json --output /tmp/rgb.png
```

The CLI honors the target and rotation from Context; RGB deliberately leaves
resizing/quantization to the controller. For a deployed Job, bake this script
into a renderer image and pass `--context`/`--output` explicitly using the
[Job container contract](#job-example-and-container-requirements).

## HTTP POST example

The [device-status renderer](../examples/renderers/http/device-status/renderer.py) displays device
name, model, firmware, voltage and RSSI. It uses only Python's standard library,
ignores unknown Context fields, shows UNKNOWN for missing observations and never
infers percentage from voltage. Install and assign it:

```sh
kubectl -n arprt create configmap device-status-renderer \
  --from-file=renderer.py=examples/renderers/http/device-status/renderer.py
kubectl -n arprt apply --validate=strict -f examples/renderers/http/device-status/renderer.yaml \
  -f examples/renderers/http/device-status/screen.yaml
kubectl -n arprt rollout status deployment/device-status-renderer
kubectl -n arprt patch arprtdevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"device-status"}}}'
```

Run it outside Kubernetes as an HTTP service:

```sh
python3 examples/renderers/http/device-status/renderer.py --port 8000
```

Or render the shared Context fixture directly:

```sh
python3 examples/renderers/http/device-status/renderer.py \
  --context testdata/render-context/render-context-v1.json --output /tmp/device-status.png
```

The complete [Context fixture](../testdata/render-context/render-context-v1.json) and
[no-telemetry fixture](../testdata/render-context/render-context-no-telemetry.json) show
optional-value omission. POST receives current authenticated request observations
overlaid on valid known state, with query and JSON targets agreeing.

## Job example and container requirements

Job `executionMode` is `Scheduled` when omitted. Set `OnDemand` to start only
on authenticated display requests, omit `refreshInterval`, and optionally set
`timeout` to an integer from `1s` to `10s` (default `5s`). The candidate budget
includes execution-slot waiting, Pod startup, collection and successful image
storage. Rendering stops at 10 seconds from request admission; authentication
and device-gate waiting reduce the remaining time. A shorter controller Job
deadline also wins. Use a renderer/image whose startup and work fit the budget.
Cold/warm container and physical-device acceptance remain pending.

The [single-screen OnDemand example](../examples/custom-resources/screen-ondemand.yaml)
uses the same image ConfigMap as the example below. Apply it and assign
`job-bands-ondemand` through `spec.screenRef`. Each independent display attempts
generation, even with a previous image; failure can serve compatible stale
output or HTTP 200 / JSON 202 when none exists. Image downloads never start Jobs.
Existing playlist/discovery retain replay, stale fallback and turn holding;
Skip policy is not available yet.

Install the [existing-image Job examples](../examples/renderers/job/gray-bands/screens.yaml):

```sh
kubectl -n arprt create configmap job-example-image \
  --from-file=image.png.b64=examples/renderers/job/gray-bands/image.png.b64
kubectl -n arprt apply --validate=strict -f examples/renderers/job/gray-bands/screens.yaml
kubectl -n arprt patch arprtdevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"job-bands"}}}'
```

Unmodified `busybox:1.37.0` checks the context file and writes a supplied PNG using
ordinary CLI commands. It demonstrates file collection without Kubernetes clients
or an upload SDK. A ConfigMap supplies the non-sensitive input image.
Assign `job-bands-custom` to exercise separate context/output parents.

| Example | Context path | Image path |
| --- | --- | --- |
| `job-bands` | `/arprt/context.json` (default) | `/arprt/output.png` (default) |
| `job-bands-custom` | `/input/context.json` | `/work/weather.png` |

Supply paths explicitly in your renderer CLI. `contextPath`/`imagePath` describe
mounting and collection; the controller does not rewrite command arguments.
Containers run as UID/GID 1000 with a read-only root, dropped capabilities, no
privilege escalation and no ServiceAccount token. The entire output parent is a
writable mount that hides image files at that path: choose a dedicated directory.
Context is a read-only file mount and may have a distinct or nested parent.
No extra writable locations such as `/tmp` are provided.

Default renderer resources are requests 50m CPU/64Mi memory and limits
500m/256Mi; `renderer.resources` overrides them. Renderers must exit successfully
and write a complete valid PNG at the declared path. Missing/malformed output,
special files and symlinks escaping the output directory fail collection.
Controller deadlines/concurrency and operator diagnostics are in
[OPERATIONS](OPERATIONS.md#runtime-configuration); durable recovery, publication
and PVC topology are in [DESIGN](DESIGN.md#job-lifecycle-and-collection).

## Picsum Job renderer

The [Picsum renderer](https://github.com/dayflower/arprt-renderers/blob/main/renderers/picsum/README.md)
is maintained in `arprt-renderers`. Its README owns photo-rendering options,
local execution, OG gray4 and Spectra 6 RGB Screen examples, and image usage.
[Picsum 0.1.0](https://github.com/dayflower/arprt-renderers/releases/tag/picsum/v0.1.0)
publishes `ghcr.io/dayflower/arprt-renderer-picsum:0.1.0` for Linux amd64 and arm64,
independently versioned from ARPRT. Prefer the release's immutable image digest
when pinning the image. Jobs require image-pull access, Job/PVC support and
outbound HTTPS to Picsum and its image CDN, as described in the renderer README.

ARPRT remains authoritative for the [Job container requirements](#job-example-and-container-requirements),
[Render Context](PROTOCOL.md#render-context-v1), [PNG contract](PROTOCOL.md#png-contract)
and [controller rendering/fallback behavior](DESIGN.md#rendering-and-assignment).
