# Renderer guide

Use this guide to select a renderer and run the supplied examples. Commands run
from the repository root and assume a paired `arprt/living-room` device using
`screenRef`. When switching from a playlist, remove `playlistRef` before assigning
`screenRef`; the two references are mutually exclusive. See [installation and
pairing](../README.md#installation-and-pairing) for initial setup.

## Choosing a source

| Source | Use when | Delivery behavior |
| --- | --- | --- |
| HTTP GET | Output depends on the screen and display target, without device telemetry | Render on demand; `refreshInterval` bounds fresh reuse |
| HTTP POST | Output needs device name, model/firmware or current telemetry | Render synchronously for each single-screen request or new cycling turn |
| Kubernetes Job | An existing container CLI writes an image, or rendering should run asynchronously | Render on an interval; display serves completed output |

Device polling is configured separately from screen freshness. POST forbids
`refreshInterval`; playlist/discovery can replay a recorded turn without rendering
again. Job telemetry is a scheduling snapshot and may be older at display time.
On failure, compatible prior output can remain visible; without one, display is
pending. See [rendering and assignment](DESIGN.md#rendering-and-assignment) for
reuse, replay, ordering and fallback rules.

Renderers produce an OG-compatible 800 × 480 gray4 PNG. The exact HTTP headers,
query merge, shared execution budget, Context fields/versioning and PNG constraints
are maintained in [PROTOCOL](PROTOCOL.md#http-renderer-contract). The controller
performs no image conversion; configure your encoder for the accepted format.

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
kubectl apply --validate=strict -k examples/http-post
kubectl -n arprt rollout status deployment/device-status-renderer
kubectl -n arprt patch trmnldevice living-room --type=merge \
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
kubectl apply --validate=strict -k examples/job
kubectl -n arprt patch trmnldevice living-room --type=merge \
  -p '{"spec":{"screenRef":{"name":"job-bands"}}}'
```

Unmodified `busybox:1.37.0` checks the context file and writes a supplied PNG using
ordinary CLI commands. It demonstrates file collection without Kubernetes clients
or an upload SDK. A ConfigMap supplies the non-sensitive input image.
Assign `job-bands-custom` to exercise separate context/output parents.

| Example | Context path | Image path |
| --- | --- | --- |
| `job-bands` | `/trmnl/context.json` (default) | `/trmnl/output.png` (default) |
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
