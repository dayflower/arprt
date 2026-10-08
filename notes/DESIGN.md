# TRMNL BYOS Kubernetes Controller

Phase 0 decisions are recorded in [001-PHASE0.md](001-PHASE0.md).
That contract defines the initial single-namespace PVC topology, verified
TRMNL OG / firmware 1.8.17 behavior, routing, authentication, defaults and validation.
Examples below use the agreed `arprt.dayflower.github.io/v1alpha1` API group.

## 1. Overview

This project provides a Kubernetes-native controller for operating TRMNL devices using the TRMNL BYOS protocol.

The primary goals are:

- Manage TRMNL devices declaratively with Kubernetes CRDs.
- Define screens independently from physical devices.
- Support multiple rendering mechanisms.
- Support both synchronous HTTP-based rendering and asynchronous Kubernetes Job-based rendering.
- Keep renderer implementations independent from Kubernetes whenever possible.
- Allow existing container images and HTTP services to be reused as renderers.
- Hide artifact storage, caching, deduplication, and other controller implementation details from the public CRD API.
- Support devices with different display dimensions, palettes, and capabilities.

The primary resources are expected to be:

- `TrmnlScreen`
- `TrmnlDevice`
- `TrmnlPlaylist` — optional

The controller also exposes the HTTP endpoints required by the TRMNL BYOS protocol.

---

## 2. Device Connection to the BYOS Server

A TRMNL device is configured through Wi-Fi Pairing Mode using a custom server URL.

The custom server URL may contain a path prefix.

For example:

```text
http://192.168.0.2:2300/devices/trmnl/living-room
```

The TRMNL firmware appends its API paths to this base URL.

The resulting requests therefore look like:

```text
/devices/trmnl/living-room/api/setup
/devices/trmnl/living-room/api/display
```

A trailing slash should not be included in the configured custom server URL.

This path-based structure provides a straightforward mapping between a physical device and a Kubernetes resource:

```text
/devices/trmnl/living-room/api/display
              │
              ▼
namespace trmnl / TrmnlDevice living-room
```

TRMNL devices may also send a device identity through HTTP headers or other protocol fields.

`spec.hardwareID` optionally restricts the device to a literal MAC;
`spec.hardwareIDSecretRef` can instead select a same-namespace Secret key.
Leaving both unset learns the MAC during successful setup. `spec.authSecretRef`
remains required and references the operator-supplied device key. Registration
is pinned to the device UID on the PVC, with a mirror in `status.hardwareID`.
See [optional MAC registration](005-OPTIONAL-MAC.md) for source immutability,
Secret changes, namespace uniqueness and recovery. Setup is enabled temporarily by runtime configuration;
display and image requests validate both registered identity and credentials.
The namespace in the route must match the controller's single watched namespace.
See the Phase 0 contract for proxy prefixes and the firmware's image-URL prefix check.

For the initial design, the URL path is the preferred canonical mapping to a `TrmnlDevice` resource.

Authentication credentials, access tokens, and other secrets must not be exposed through device status or renderer context.

---

## 3. Core Resource Model

### 3.1 `TrmnlScreen`

A `TrmnlScreen` describes **what can be displayed and how it is rendered**.

It is independent from any specific physical TRMNL device.

A screen may use different rendering sources, initially:

- HTTP
- Kubernetes Job

Example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: weather
spec:
  refreshInterval: 30m

  source:
    job:
      renderer:
        image: example/weather-renderer:latest
```

---

### 3.2 `TrmnlDevice`

A `TrmnlDevice` represents a physical TRMNL device.

It is responsible for:

- screen or playlist assignment
- observed device capabilities
- display characteristics
- telemetry
- last-seen state
- firmware information

Example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlDevice
metadata:
  name: living-room

spec:
  hardwareID: "02:00:00:00:00:01"
  authSecretRef:
    name: living-room-auth
  screenRef:
    name: weather

status:
  display:
    width: 800
    height: 480
    palette: gray4

  device:
    model: og
    firmwareVersion: "1.8.17"

  battery:
    voltage: 3.91

  network:
    rssi: -48

  lastSeenAt: "2026-10-07T00:00:00Z"
```

Capabilities and telemetry reported by the device are observed state and therefore belong in `status`, rather than `spec`.

---

### 3.3 `TrmnlPlaylist`

A playlist is optional.

It explicitly defines the sequence of screens presented to a device.

Example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlPlaylist
metadata:
  name: living-room
spec:
  screens:
    - name: weather
    - name: power
    - name: calendar
```

A device may refer either to a single screen or to a playlist.

Conceptually:

```text
screenRef XOR playlistRef
```

Example:

```yaml
spec:
  playlistRef:
    name: living-room
```

If neither is specified, the controller may automatically discover available `TrmnlScreen` resources and rotate through them.

This provides a simple default experience:

```text
No assignment
    ↓
Automatically discover screens
```

while still allowing explicit configuration when required.

---

## 4. Rendering Unit

A rendered image cannot generally be associated with a `TrmnlScreen` alone.

Different devices may have different:

- display width
- display height
- palette
- other display capabilities

Therefore the basic rendering unit is:

```text
Screen × Device
```

For example:

```text
weather × living-room
    800 × 480 / gray4

weather × desk
    1200 × 825 / gray16
```

For asynchronous renderers, the controller maintains a rendered artifact for each required screen/device combination.

The device fetch operation must not normally trigger a Job renderer immediately.

Instead:

```text
TrmnlScreen
      +
TrmnlDevice
      ↓
asynchronous rendering
      ↓
latest rendered artifact
      ↓
device GET /api/display
```

The latest available successful artifact is returned to the device.

A future implementation may deduplicate artifacts between devices with identical rendering characteristics.

However, the public API should initially model rendering conceptually as `Screen × Device`.

---

## 5. Render Target

Display properties that directly affect image generation are represented as the **render target**.

The initial render target contains:

```text
width
height
palette
```

Example:

```json
{
  "width": 800,
  "height": 480,
  "palette": "gray4"
}
```

Palette values should use semantic identifiers rather than merely a color count.

Examples:

```text
bw
gray4
gray16
bwry
```

A physical device model should normally not be part of the render target if two different models have identical rendering capabilities.

For example:

```text
model=trmnl-og
```

should not unnecessarily produce a different cache key from another device with the same width, height, and palette.

---

## 6. Render Context

Renderers sometimes require information beyond the display target.

Examples include:

- device name
- battery state
- RSSI
- firmware version
- device capabilities

Renderers should not need direct Kubernetes API access to obtain this information.

Direct Kubernetes access is intentionally avoided because it would:

- make renderers Kubernetes-aware
- require ServiceAccounts and RBAC
- make existing container images harder to reuse
- tightly couple renderer implementations to the controller

Environment variables are also not the primary mechanism because the environment-variable contract would grow whenever new device information is added.

Instead, the controller provides a structured **Render Context**.

---

## 7. Render Context v1

An example Render Context is:

```json
{
  "version": "v1",

  "renderTarget": {
    "width": 800,
    "height": 480,
    "palette": "gray4"
  },

  "device": {
    "name": "living-room",
    "model": "og",

    "battery": {
      "voltage": 3.91
    },

    "network": {
      "rssi": -48
    },

    "firmware": {
      "version": "1.8.17"
    }
  }
}
```

The top-level separation between `renderTarget` and `device` is intentional.

`renderTarget` represents inputs that directly define the rendered output format.

`device` contains physical-device identity, telemetry, and other contextual information.

---

## 8. Render Context Versioning

The context contains an explicit version:

```json
{
  "version": "v1"
}
```

Compatibility rules for a given major context version are:

- New optional fields may be added.
- Existing fields must retain their meaning.
- Existing fields should not be removed.
- Renderers must ignore unknown fields.
- Breaking semantic changes require a new context version.

For example:

```text
v1
v2
```

The same Render Context schema is used by all device-aware rendering transports.

This is an important design goal:

```text
Job renderer
    /path/context.json
             │
             ▼
      Render Context v1
             ▲
             │
HTTP POST request body
```

Renderer authors therefore need to understand only one context schema.

The transport differs, but the data model does not.

---

## 9. HTTP Rendering

HTTP renderers are synchronous renderer services.

The controller supports two HTTP modes:

- `GET`
- `POST`

These methods have specific semantics and are not simply arbitrary HTTP method choices.

---

## 10. HTTP GET Renderer

A GET renderer is device-independent.

It receives only render-target characteristics.

Example resource:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: photo
spec:
  source:
    http:
      url: http://photo-renderer.default.svc/render
      method: GET
```

The controller invokes:

```http
GET /render?width=800&height=480&palette=gray4
Accept: image/png
```

The query parameters are generated automatically by the controller.

The renderer does not receive:

- device identity
- battery status
- RSSI
- other dynamic telemetry

GET therefore means:

> The rendered result depends only on the screen definition and render target characteristics.

This allows normal HTTP caching infrastructure such as:

- reverse proxies
- HTTP caches
- CDNs
- shared controller caches

to reuse the same rendered image.

For example, two devices with:

```text
width=800
height=480
palette=gray4
```

may use the exact same renderer URL.

A conceptual cache key is therefore:

```text
Screen + width + height + palette
```

rather than:

```text
Screen + Device
```

---

## 11. HTTP POST Renderer

A POST renderer is device-aware.

Example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: device-status
spec:
  source:
    http:
      url: http://status-renderer.default.svc/render
      method: POST
```

The controller invokes:

```http
POST /render?width=800&height=480&palette=gray4
Content-Type: application/json
Accept: image/png
```

The request body is the complete Render Context:

```json
{
  "version": "v1",

  "renderTarget": {
    "width": 800,
    "height": 480,
    "palette": "gray4"
  },

  "device": {
    "name": "living-room",
    "battery": {
      "voltage": 3.91
    },
    "network": {
      "rssi": -48
    }
  }
}
```

The duplication of render target values between the query string and JSON body is intentional.

The two representations have different roles:

```text
Query parameters
    routing
    caching
    cheap inspection

JSON body
    complete structured context
```

POST therefore means:

> The rendered output may depend on full device-specific context.

POST rendering is normally performed synchronously with the device request and may therefore use telemetry received in that request.

The initial implementation renders every authenticated display request with
the current telemetry overlaid on known valid state. POST has no fresh artifact
reuse or context coalescing; `refreshInterval` must be omitted. A failed attempt
may serve the same screen/device UID pair's previous successful image only when
the current assignment and exact render target remain compatible.

---

## 12. Job Rendering

Job renderers are asynchronous renderers executed inside Kubernetes.

Example:

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: weather

spec:
  refreshInterval: 30m

  source:
    job:
      renderer:
        image: example/weather-renderer:latest
        args:
          - "--context"
          - "/work/context.json"
          - "--output"
          - "/work/weather.png"

      contextPath: /work/context.json
      imagePath: /work/weather.png
```

The renderer definition should resemble a Kubernetes container specification where useful.

Expected fields include at least:

```yaml
renderer:
  image:
  command:
  args:
  env:
  envFrom:
  resources:
```

This makes existing container images easier to reuse.

---

## 13. Job Renderer Context

Before executing the renderer, the controller creates a Render Context snapshot.

It writes the JSON file to `contextPath`.

For example:

```text
/work/context.json
```

The renderer may read that file if required.

If `contextPath` is omitted, the default is:

```text
/trmnl/context.json
```

The renderer is not required to consume the context.

For example, an existing renderer may only need dimensions supplied through its own command-line arguments.

---

## 14. Job Renderer Image Output

The renderer writes its result to `imagePath`.

Example:

```yaml
imagePath: /work/weather.png
```

If omitted, the default is:

```text
/trmnl/output.png
```

The controller provides writable storage that makes the output available for artifact collection.

The path specified by `imagePath` must reside on writable storage provided by the controller.

Renderer authors should avoid choosing a path whose mounted parent directory contains files required by the renderer image, because the controller may mount a writable volume at that location.

The explicit `imagePath` field and renderer command-line arguments may contain the same path.

For example:

```yaml
source:
  job:
    renderer:
      image: example/weather-renderer:latest
      args:
        - "-o"
        - "/work/weather.png"

    imagePath: /work/weather.png
```

This duplication is intentional.

The renderer arguments tell the renderer **where to write**.

`imagePath` tells the controller **where to collect the rendered image**.

The controller does not need to understand the renderer's command-line interface.

---

## 15. Job Execution Unit

A Job renderer is conceptually executed for:

```text
TrmnlScreen × TrmnlDevice
```

Each execution receives a context snapshot created from the latest state known by the controller.

For example:

```text
weather × living-room
weather × desk
```

may result in separate Job executions.

The controller is responsible for:

- determining when rendering is necessary
- constructing the Job
- providing the Render Context
- providing writable artifact storage
- collecting the generated image
- recording successful rendered artifacts
- handling failed renders

A `CronJob` is not required.

Scheduling is controlled by the `TrmnlScreen` reconciliation logic using fields such as:

```yaml
refreshInterval: 30m
```

This keeps rendering lifecycle management inside the controller.

---

## 16. Dynamic Telemetry and Job Rendering

Job rendering is asynchronous relative to the physical device's `/api/display` request.

Therefore dynamic telemetry embedded in a Job-rendered image may be older than the telemetry from the most recent device request.

Example:

```text
09:00
Job starts
battery = 73%

09:05
Device requests /api/display
battery = 71%

Controller returns image rendered at 09:00.
Image still shows 73%.
```

This is expected behavior.

The contract is:

> Dynamic values in the Render Context are a snapshot of the latest controller-known state at the time rendering begins. They are not guaranteed to represent real-time device state when the image is displayed.

HTTP POST rendering differs because it may run synchronously with the device request and therefore use the telemetry received by that request.

---

## 17. Rendered Artifacts

A rendered image is an internal controller artifact.

It should not initially be represented as a Kubernetes CRD.

Conceptually, an artifact contains metadata such as:

```text
screen UID
device UID or render-target key
render target
rendered timestamp
context digest
image digest
content type
storage location
```

The actual image may be stored using an implementation-defined mechanism such as:

- controller-local persistent storage
- PVC-backed storage
- object storage

The public CRD API should not expose this storage implementation.

Both rendering mechanisms ultimately produce the same internal artifact type:

```text
HTTP renderer ─┐
               ├──▶ Rendered Artifact ───▶ BYOS
Job renderer ──┘
```

This keeps the device-serving layer independent from how an image was produced.

---

## 18. Render Failure Behavior

A failed render should normally not invalidate the last successfully rendered image.

For example:

```text
Current successful artifact
        ↓
new render starts
        ↓
render fails
        ↓
keep current successful artifact
```

This is especially important for e-paper devices, where displaying stale but valid information is generally preferable to displaying nothing.

Failure information may be exposed through `TrmnlScreen`, `TrmnlDevice`, events, metrics, or logs as appropriate.

---

## 19. Caching and Deduplication

Caching and artifact deduplication should initially remain controller implementation details.

They should not require explicit CRD configuration.

### GET renderers

GET semantics guarantee that the renderer depends only on render-target characteristics.

The controller may therefore safely reuse artifacts across devices with the same target:

```text
Screen
+
width
+
height
+
palette
```

Example:

```text
Device A ─┐
          │ 800×480 gray4
Device B ─┼────────────────▶ same cached render
          │
Device C ─┘
```

### POST renderers

POST renderers receive complete device-specific context.

They should initially be treated as:

```text
Screen × Device
```

for rendering and caching purposes.

### Job renderers

Job renderers also receive complete Render Context and should initially use:

```text
Screen × Device
```

as their rendering unit.

Future versions may introduce an explicit optimization hint allowing a Job renderer to declare that it depends only on the render target.

Such a feature should only be added when a concrete use case requires it.

---

## 20. What Should Remain Outside the Public CRD API

The public API should remain focused on user intent.

The following concepts should initially remain controller implementation details:

- render instances
- rendered artifact resources
- artifact storage layout
- cache entries
- cache keys
- HTTP cache implementation
- Job names
- temporary Pods
- temporary volumes
- artifact upload mechanisms
- artifact deduplication
- renderer execution bookkeeping

The public API should initially consist primarily of:

```text
TrmnlScreen
TrmnlDevice
TrmnlPlaylist

Render Context v1
HTTP renderer protocol
Job renderer protocol
```

---

## 21. Controller Responsibilities

The controller is responsible for:

- exposing the TRMNL BYOS endpoints
- resolving a device request to a `TrmnlDevice`
- collecting and storing device telemetry
- updating `TrmnlDevice.status`
- resolving screen or playlist assignment
- discovering screens when no explicit assignment exists
- determining render target characteristics
- creating Render Context snapshots
- invoking HTTP renderers
- scheduling and creating renderer Jobs
- collecting Job-generated images
- storing rendered artifacts
- selecting the latest successful artifact
- serving image URLs to TRMNL devices
- caching and deduplicating renders where safe
- retaining the previous successful artifact after render failures

---

## 22. Renderer Responsibilities

Renderers should remain as independent as possible from Kubernetes and from the controller implementation.

An HTTP renderer needs to understand only one of:

```text
GET + render-target query parameters
```

or:

```text
POST + render-target query parameters + Render Context v1
```

A Job renderer needs to understand only:

```text
optional Render Context JSON file

and

write one image file to the configured path
```

A renderer does not need to:

- query the Kubernetes API
- know about `TrmnlDevice`
- know about `TrmnlScreen`
- know where artifacts are permanently stored
- know how TRMNL BYOS endpoints work
- know how playlists are resolved

---

## 23. Initial `TrmnlScreen` Examples

### Device-independent HTTP renderer

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: photo

spec:
  source:
    http:
      url: http://photo-renderer.default.svc/render
      method: GET
```

### Device-aware HTTP renderer

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: device-status

spec:
  source:
    http:
      url: http://device-status.default.svc/render
      method: POST
```

### Job renderer using defaults

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: weather

spec:
  refreshInterval: 30m

  source:
    job:
      renderer:
        image: example/weather-renderer:latest
```

Defaults:

```text
contextPath = /trmnl/context.json
imagePath   = /trmnl/output.png
```

### Job renderer using an existing container interface

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlScreen
metadata:
  name: weather

spec:
  refreshInterval: 30m

  source:
    job:
      renderer:
        image: example/weather-renderer:latest
        args:
          - "--context"
          - "/work/context.json"
          - "--output"
          - "/work/weather.png"

      contextPath: /work/context.json
      imagePath: /work/weather.png
```

---

## 24. Initial `TrmnlDevice` Example

```yaml
apiVersion: arprt.dayflower.github.io/v1alpha1
kind: TrmnlDevice
metadata:
  name: living-room

spec:
  hardwareID: "02:00:00:00:00:01"
  authSecretRef:
    name: living-room-auth
  screenRef:
    name: weather

status:
  display:
    width: 800
    height: 480
    palette: gray4

  device:
    model: og
    firmwareVersion: "1.8.17"

  battery:
    voltage: 3.91

  network:
    rssi: -48

  lastSeenAt: "2026-10-07T00:00:00Z"

  conditions:
    - type: Ready
      status: "True"
```

The corresponding device may use:

```text
http://controller:2300/devices/trmnl/living-room
```

as its TRMNL Custom Server URL.

---

## 25. Design Principles

The current design is guided by the following principles.

### Declarative

Screens, devices, and playlists are managed as Kubernetes resources.

### Renderer-agnostic

The controller does not know whether a renderer uses Prometheus, Kubernetes metrics, an external weather service, a calendar API, or static files.

### Kubernetes-independent renderers

Renderers should not need Kubernetes API access.

### Existing-image friendly

Existing container images and HTTP services should be reusable with minimal adaptation.

### Transport-independent context

Job and HTTP POST renderers use the same Render Context schema.

### Minimal public API

Caching, storage, temporary Jobs, and artifact lifecycle are implementation details rather than user-facing resources.

### Safe optimization

Caching and deduplication are performed automatically only when renderer semantics make them safe.

### Graceful degradation

The latest successful rendered artifact remains usable when a subsequent render fails.

### Extensible

The API begins with a deliberately small surface and leaves room for additional render sources, device capabilities, playlist behavior, and optimization hints in future versions.
