# Renderer guide

Use this guide to select a renderer and run the supplied examples. Commands run
from the repository root and assume a paired `arprt/living-room` device using
`screenRef`. When switching from a playlist, remove `playlistRef` before assigning
`screenRef`; the two references are mutually exclusive. See [installation and
pairing](../README.md#installation-and-pairing) for initial setup.

## Choosing a source

| Source | Use when | Delivery behavior |
| --- | --- | --- |
| HTTP GET | Output depends on the screen and display target including rotation, without device telemetry | Render on demand; `refreshInterval` bounds fresh reuse |
| HTTP POST | Output needs device name, model/firmware or current telemetry | Render synchronously for each single-screen request or new cycling turn |
| Kubernetes Job | An existing container CLI writes an image, or rendering should run asynchronously | Render on an interval; display serves completed output |

Device polling is configured separately from screen freshness. POST forbids
`refreshInterval`; playlist/discovery can replay a recorded turn without rendering
again. Job telemetry is a scheduling snapshot and may be older at display time.
On failure, compatible prior output can remain visible; without one, display is
pending. See [rendering and assignment](DESIGN.md#rendering-and-assignment) for
reuse, replay, ordering and fallback rules.

Renderers produce immutable OG-compatible base images: 800 × 480 gray4 PNG. The exact HTTP headers,
query merge, shared execution budget, Context fields/versioning and PNG constraints
are maintained in [PROTOCOL](PROTOCOL.md#http-renderer-contract). The controller
can add validated 2-bit bitmap overlays at delivery time; configure your encoder
for the accepted base format. It does not quantize or repair invalid PNGs.

## Controller display overlays

For timestamp/battery/Wi-Fi display without adding device telemetry to a shared
GET image, set the Screen's `spec.overlay.enabled: true`. Select items with
`items: [timestamp, battery, wifi]`, or omit `items` for all three. `items: []`
draws nothing. Use `cycleCorners: true` to share the device UID's durable clockwise
corner cycle across Screens; omit `position` in that mode. Fixed mode defaults
to top-right. An optional positive `scale` overrides automatic integer scaling.
Use nonnegative `margin.horizontal` and `margin.vertical` (each default 0) to
independently inset foreground from the left/right and top/bottom edges in the
logical viewing orientation while its white background reaches those edges.
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
`0`. Renderers handle layout and rotation and always return an 800 × 480 PNG.
The HTTP examples support all four rotations. The BusyBox Job example copies a
fixed input PNG and does not rotate it; use a renderer that reads the setting
when output must follow device rotation. A rotation change requires compatible
output for that angle before delivery can resume.

## HTTP GET example

The [minimal Python renderer](../examples/http-get/renderer.py) draws four gray
bands using only the standard library and needs no Kubernetes API access.
[Its screen](../examples/http-get/screen.yaml) selects `photo`; Kubernetes install
commands are in the README quickstart. For standalone use:

```sh
python3 examples/http-get/renderer.py --port 8000
```

The endpoint is `/render`; the controller supplies the target query. Append
`?encoding=indexed` to the screen URL to exercise indexed output. Non-target
query values are preserved. Grayscale and indexed output have synthetic fixtures
under [testdata](../testdata/phase0/README.md).

## HTTP POST example

The [device-status renderer](../examples/http-post/renderer.py) displays device
name, model, firmware, voltage and RSSI. It uses only Python's standard library,
ignores unknown Context fields, shows UNKNOWN for missing observations and never
infers percentage from voltage. Install and assign it:

```sh
kubectl -n arprt create configmap device-status-renderer \
  --from-file=renderer.py=examples/http-post/renderer.py
kubectl -n arprt apply --validate=strict -f examples/http-post/renderer.yaml \
  -f examples/http-post/screen.yaml
kubectl -n arprt rollout status deployment/device-status-renderer
kubectl -n arprt patch arprtdevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"device-status"}}}'
```

Run it outside Kubernetes as an HTTP service:

```sh
python3 examples/http-post/renderer.py --port 8000
```

Or render the shared Context fixture directly:

```sh
python3 examples/http-post/renderer.py \
  --context testdata/phase0/render-context-v1.json --output /tmp/device-status.png
```

The complete [Context fixture](../testdata/phase0/render-context-v1.json) and
[no-telemetry fixture](../testdata/phase0/render-context-no-telemetry.json) show
optional-value omission. POST receives current authenticated request observations
overlaid on valid known state, with query and JSON targets agreeing.

## Job example and container requirements

Install the [existing-image Job examples](../examples/job/screens.yaml):

```sh
kubectl -n arprt create configmap job-example-image \
  --from-file=image.png.b64=examples/job/image.png.b64
kubectl -n arprt apply --validate=strict -f examples/job/screens.yaml
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

## Picsum photo Job example

The [Picsum renderer](../examples/job-picsum/renderer.py) downloads a random photo
from [Lorem Picsum](https://picsum.photos/) every 10 minutes and converts it to
the accepted gray4 PNG format. It supports all four rotations and uses
Floyd-Steinberg dithering, falling back to undithered quantization if needed to
fit the PNG size limit. A failed download or conversion leaves compatible prior
output available through the controller's usual fallback behavior.

The renderer's `--palette` argument selects photo shades: `gray4` (the default)
or `bw` (black and white with dithering). To select black and white in the Screen
resource, set:

```yaml
spec:
  source:
    job:
      renderer:
        args: ["--palette", "bw"]
```

This is a fragment to merge into the existing Screen. Both modes still produce
the required 2-bit gray4 PNG and accept the same gray4 Render Context; `bw` only
uses its black and white samples. The reviewed OG firmware can select
black-and-white refresh for two-color images, but actual refresh behavior depends
on its display state. This option does not guarantee a partial update on every
delivery.

The [screen](../examples/job-picsum/screen.yaml) enables timestamp, battery and
Wi-Fi overlays with clockwise corner cycling: top-right, bottom-right,
bottom-left, top-left. Corners advance on new deliveries, independently of photo
generation. The timestamp is the photo's generation time; battery/Wi-Fi use
current display telemetry. Device polling remains separately configured.

This example needs the controller's Job/PVC support and outbound HTTPS access
to `picsum.photos` and its image CDN (`fastly.picsum.photos`). Requests include
only dimensions and a random cache-busting value; no device data is sent.
Pillow is installed during the image build, so Jobs need no package downloads.
There is no prebuilt image for this example. Build and push it to a registry
accessible to your cluster, replacing the synthetic registry below:

```sh
picsum_image=registry.example.com/picsum-renderer:v1
docker build -t "$picsum_image" examples/job-picsum
docker push "$picsum_image"
```

Set `spec.source.job.renderer.image` in `examples/job-picsum/screen.yaml` to that
image reference (prefer its pushed digest), then install and assign it:

```sh
kubectl -n arprt apply --validate=strict -f examples/job-picsum/screen.yaml
kubectl -n arprt patch arprtdevice living-room --type=merge \
  -p '{"spec":{"playlistRef":null,"screenRef":{"name":"picsum"}}}'
```

For standalone rendering, install the pinned
[Python dependency](../examples/job-picsum/requirements.txt) in a virtual
environment, then use the shared Context fixture:

```sh
python3 -m venv /tmp/arprt-picsum-venv
/tmp/arprt-picsum-venv/bin/pip install -r examples/job-picsum/requirements.txt
/tmp/arprt-picsum-venv/bin/python examples/job-picsum/renderer.py \
  --context testdata/phase0/render-context-v1.json --output /tmp/picsum.png
```

Append `--input /path/to/photo.jpg` to render a local image without network
access. This also works with other Pillow-supported image formats. The renderer
draws only the photo; controller overlays are added at delivery time.

Run its conversion/download-boundary tests without external photo requests:

```sh
/tmp/arprt-picsum-venv/bin/python -m unittest discover -s examples/job-picsum -v
```
