# Protocol contracts

This document owns the BYOS wire contract, Render Context and renderer image
format. Internal publication/recovery rules are in [DESIGN](DESIGN.md);
installation is in [README](../README.md), runtime configuration in
[OPERATIONS](OPERATIONS.md), and example usage in [RENDERERS](RENDERERS.md).

## Firmware evidence

The reviewed firmware is TRMNL OG `v1.8.17`, commit
`9acd348270a493ca5f1c627ac91a9469543267ae`. These pinned sources explain the
compatibility choices below; firmware behavior takes precedence over generic
BYOS examples:

- [Headers](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/api-client/request_headers.cpp).
- [Setup transport](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/api-client/setup.cpp),
  [setup/image handling](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/services/device_setup.cpp),
  [setup parser](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/parse_response_api_setup.cpp).
- [Display parser](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/parse_response_api_display.cpp),
  [display/status flow](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/bl.cpp),
  [HTTP transport](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/services/http_retry_request.cpp),
  [URL/retry helpers](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/http_utils.cpp).
- [Display/PNG decoder](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/display.cpp),
  [image limits](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/include/config.h),
  [filename normalization](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/filesystem.cpp),
  [refresh/backoff](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/refresh_interval.cpp).
- [Official BYOS overview](https://docs.trmnl.com/go/diy/byos) for endpoint scope.

Source inspection, [synthetic fixtures](../testdata/protocol/README.md) and
[physical acceptance](DEVELOP.md#recorded-acceptance-baseline) are distinct evidence.
Additional pinned OG/reTerminal source reviews and their limits are recorded in
[DESIGN](DESIGN.md#firmware-evidence-and-profiles); they do not establish new
physical acceptance.

## Routes and authentication

The canonical Custom Server URL is
`<publicBaseURL>/devices/<namespace>/<device-name>`, without a trailing slash.
For example: `https://byos.example.test/arprt/devices/arprt/living-room`.
The external prefix and namespace are independent. The namespace must match
the controller's configured scope.

| Appended route | Contract |
| --- | --- |
| `GET /api/setup` | Known device route and eligible MAC; no Access-Token. Setup is enabled by default and returns a saved key only after credential/registration persistence. |
| `GET /api/display` | Registered identity and current token required before telemetry, rendering or image issuance. |
| `POST /api/log` | Authenticated, bounded to 64 KiB, discarded after bounded parsing; returns 204. Arbitrary firmware messages are not logged. |
| `GET /images/<sha256>.png` | Current device authentication and issuance to that device UID required. |
| `GET /setup-logo.png` | Bundled calibration image; anonymous because firmware's setup downloader sends no auth headers. |

`publicBaseURL` is absolute HTTP(S), without query, fragment or userinfo. Its
configured origin is used for every generated URL; Host/Forwarded headers cannot
change it. The proxy preserves the prefix unless explicitly configured to strip
exactly that prefix. Reject escaped separators, dot segments, repeated slashes
and malformed resource names without redirects.

Firmware's `isSameOrigin` is a string-prefix check against the **entire Custom
Server URL**. Therefore display-image URLs must live beneath that exact device
base path, with the configured scheme/host/port spelling. A global image route
or redirect would lose firmware authentication headers. Images have exact
Content-Type/Content-Length, no redirects and no transport compression.
TLS and proxy header preservation are operator responsibilities.

## Request observations and targets

Setup sends `ID`, `FW-Version`, `Model` and optional `Panel-Rev`. Display also
sends `Access-Token`, dimensions, RSSI, voltage and polling/cache observations.
Firmware may send `Update-Source`, `Wake-Time`, `FW-Commit`, `WiFi-Band`,
`USB-Connected`, `Battery-Charging` and `Temperature-Profile`; the last is a
feature flag, not a selected display profile. Receiving a header does not make
it part of the exported context.

Header names are case-insensitive; underscore aliases normalize to hyphens.
Reject duplicate/conflicting identity or token headers rather than selecting a
winner. MACs normalize to uppercase for requests; configured MAC values are
canonical uppercase. Voltage is finite, nonnegative volts; RSSI is signed integer
dBm; dimensions are positive integers. `Percent-Charged` is an optional finite
measured percentage from 0 through 100, including fractional values and zero.
OG does not supply this header in the reviewed firmware. Invalid syntax, empty
values, NaN/Inf, out-of-range values and duplicate optional observations are
omitted; underscore aliases follow the same rule. Charging uses `0`/`1`, cache/USB flags
`true`/`false`. Only the current valid charging flag affects controller overlays;
USB connection is accepted but does not imply charging. Neither flag adds status
or Render Context fields. OG / 1.8.17 sends neither flag.
Unknown headers are ignored; invalid optional telemetry is omitted
and reported by field name only. Never infer percentage from voltage.

The reviewed model profiles are active independently of firmware version; known
models resolve with missing or unknown firmware. Literal model aliases, default
dimensions, palettes and exclusive PNG limits are listed in
[DESIGN](DESIGN.md#firmware-evidence-and-profiles). Model and firmware remain
independent raw observations; omitting either retains its last observed value.
Firmware sends no semantic palette.

`ArprtDevice.spec.display` optionally overrides `width`/`height` as a pair,
`palette` (`gray4`, `gray16`, `spectra6`) and positive `pngByteLimit`. Dimensions
resolve from explicit pair, then a valid reported pair, then model defaults;
palette and byte limit resolve from explicit value, then model defaults. Output
allocation is bounded to 4096 per axis and 4,194,304 pixels. An invalid reported
pair cannot supply dimensions but permits profile fallback. An unknown model
needs explicit palette and byte limit, plus a valid explicit or reported pair;
otherwise display is pending and invokes no renderer. Overrides are operator
compatibility information, not physical verification.

`status.device.reportedDisplay` retains each parsed positive int32 dimension,
including incomplete or oversized pairs. Any dimension header replaces the
whole pair from that request, including invalid/omitted values; absent dimension
headers retain the last pair. Dimensions from separate requests are never mixed.
`status.display` contains effective width, height, palette, `pngByteLimit` and
`sources` (`dimensions`, `palette`, `pngByteLimit`), whose values are `explicit`,
`reported` (dimensions only) or `profile`. Unresolved targets clear effective
status but retain observations. Spec edits recompute from observations, including
removal of overrides; legacy effective status never becomes reported evidence.
Legacy context/status values `bw` and `bwry` remain readable without being
relabelled as `spectra6` or becoming supported override values.

## Responses and polling

HTTP status and JSON `status` are separate. Setup returns HTTP 200 and JSON
`status: 200` with `api_key`, `friendly_id`, `image_url` and `message`. Never return
an empty key: firmware persists it. Successful display returns HTTP 200 and
JSON `status: 0`, `image_url`, `filename`, integer `refresh_rate` seconds,
`image_url_timeout: 10`, `update_firmware: false`, `reset_firmware: false`, and
`special_function: ""`. There is no `sleep_duration` contract or OTA/reset command.

| Situation | HTTP / JSON status | Behavior |
| --- | --- | --- |
| Unknown device route/namespace | 404 / 404 | No provisioning or image |
| Invalid/mismatched request identity; setup explicitly disabled | 403 / 403 | No key disclosure or accepted telemetry |
| Missing/wrong token or invalid stored key | 401 / 401 | No accepted telemetry or image |
| Duplicate identity, unavailable identity/credential storage or incomplete registration | 503 / 503 | Retry without claiming success |
| Missing assignment, unresolved target, initial pending, or failed render with no compatible image | 200 / 202 | No image URL/filename; firmware handles pending as described below |
| Successful render or compatible stale fallback | 200 / 0 | Serve an issued immutable image |

Pending is **HTTP 200 with JSON 202**, since firmware's Wi-Fi transport rejects
HTTP 202 as a successful request. This branch selects sleep durations of 5 seconds
for polls 1–50, 60 for 51–60, 900 for 61–70, then 3600. Complete request intervals
also include awake work such as Wi-Fi connection and HTTP transport. A successful
image resets the streak; JSON `refresh_rate` cannot override this pending branch.
Never use JSON 500 for renderer failure because it follows a firmware reset path.

Pending does not guarantee that firmware retains the previous image. In OG
1.8.17, JSON 202 maps to `HTTPS_NO_REGISTER`. If the RTC-persisted
`need_to_refresh_display` flag is set, firmware displays its Friendly ID screen
and clears the flag. A manual or power wake sets the flag and clears the cached
filename; ordinary timer wakes can preserve the display when the flag is clear.
This is firmware display behavior, not a credential reset requested by arprt.
See the pinned [wake handling](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/bl.cpp#L916),
[pending display handling](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/bl.cpp#L1230)
and [RTC flag](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/globals.cpp#L28).

Successful-image polling is configured independently of screen freshness.
Filenames are stable for identical artifact bytes, differ for different bytes,
and contain at most 31 ASCII characters. Firmware caches by filename and truncates
longer names. Persist collision-checked short names; keep full digests in URLs
and internal metadata.

## HTTP renderer contract

GET receives target query parameters and `Accept: image/png` only. POST receives
the same query plus `Content-Type: application/json` and Render Context v1.
Preserve non-target query values, including repeated values; replace all lowercase
`width`, `height`, `palette`, `rotation` values. `rotation` is always sent as
`0`, `90`, `180` or `270`. Query and JSON targets must agree. Never
forward device headers, credentials, cookies or arbitrary inbound headers.

Require HTTP 200 and `image/png` or `image/apng` (media-type parameters are
allowed); reject redirects. GET/POST share eight execution slots and a
10-second overall rendering deadline. A single controller-wide image lease is
acquired before renderer transport and held through normalization/publication;
Job collection, overlays and buffered image retrieval share that capacity. Reuse/timing differs by source
and assignment mode; see [DESIGN](DESIGN.md#rendering-and-assignment).

## Render Context v1

The same JSON schema is used for POST bodies and immutable Job context files.
The [complete fixture](../testdata/render-context/render-context-v1.json) and
[no-telemetry fixture](../testdata/render-context/render-context-no-telemetry.json) are
serialization examples used by tests; the [fractional-percentage fixture](../testdata/render-context/render-context-fractional-percent.json)
adds an explicitly measured decimal value while preserving the old fixtures.
The [Spectra 6](../testdata/render-context/render-context-spectra6.json) and
[gray16](../testdata/render-context/render-context-gray16.json) fixtures show
reviewed targets without a firmware observation; output constraints and value
provenance are excluded from context v1.

| Field | Contract |
| --- | --- |
| `version` | Required `"v1"` |
| `renderTarget.width`, `.height`, `.palette` | Required dimensions and semantic palette |
| `renderTarget.rotation` | Optional clockwise content rotation in degrees: `0`, `90`, `180`, `270`; omitted means `0` |
| `device.name` | Required device resource name |
| `device.model`, `.firmware.version` | Optional known observations |
| `device.battery.voltage`, `.percent` | Optional valid measurements; percent is a finite number in 0–100 only if explicitly measured |
| `device.network.rssi` | Optional valid measurement |

Existing integer JSON percentages remain readable; fractional measurements are
preserved without rounding. No voltage-derived percentage is exported.
Missing optional observations and empty optional objects are omitted, not null
or fabricated zero. Actual valid zero measurements are retained. Hardware ID, credentials, Secret
references, Kubernetes objects, namespace, arbitrary metadata and header dumps
are excluded. Target is separate from device context so rendering identity does
not depend on telemetry or model when capabilities are equivalent. Rotation comes from
`ArprtDevice.spec.rotation`, rather than observations. Zero rotation is omitted
from JSON to preserve compatibility with existing contexts and artifact keys.
Renderers apply clockwise content rotation themselves. For example, an
800 × 480 target with `90`/`270` uses a logical 480 × 800 canvas, rotated into
the required physical 800 × 480 PNG. Width/height always describe the final PNG.

POST overlays current authenticated observations on valid known state without
waiting for informer propagation. Job snapshots are copied at scheduling and
cannot change in flight; their telemetry may be older at display time. GET has
no context. Optional fields may be added within v1; renderers ignore unknown
fields. Removing fields or changing semantics requires a new context version.

## PNG contract

Renderer input and device output have separate contracts. GET, POST and Job
accept complete legal PNGs of all standard color/depth combinations, including
16-bit grayscale/RGB/alpha, indexed transparency, Adam7 and APNG's IDAT default
image. Both APNG layouts are supported: the default image can be shared with
the first animation frame or separate from it. Subsequent frames are structurally
checked but never decoded or displayed. APNG always becomes a static PNG without
animation chunks; it never qualifies for byte-preserving passthrough.
Input is at most **32 MiB**, **16,777,216 pixels** and **16,384 per axis**. CRCs,
chunk structure, exact inflated image length and trailing bytes are checked
before publication. The complete file, including metadata and skipped animation
data, counts toward the byte limit. Dimensions and area apply to the default
image canvas and are checked before allocation. Invalid PNGs remain rendering
failures; a valid input requiring conversion is accepted.

RGB channels, grayscale samples and indexed palette entries are interpreted as
sRGB, including APNG default images. Embedded ICC, gamma and chromaticity metadata
does not transform samples, change palette membership or by itself require
normalization. Compatible passthrough retains its original chunks; re-encoding
omits ignored source declarations. Metadata still undergoes structure/CRC checks.

The resolved target selects dimensions, palette and an exclusive output budget:
OG defaults to **encoded bytes < 90,000**; E1001–E1004 default to
**encoded bytes < 750,000**. Explicit `spec.display.pngByteLimit` overrides the
budget; reviewed Seeed common E1001/E1002/E1003 firmware needs `90000`.
Neither model spelling nor firmware version selects a distribution-specific
budget. The independent 32 MiB complete-file ceiling still applies.
Compatible opaque, static, noninterlaced renderer output retains its exact bytes:

| Color type | Default gray4 passthrough | With `gray4_native4` build tag |
| --- | --- | --- |
| Grayscale (0), depth 1 | Preserve | Preserve |
| Grayscale (0), depth 2 | Preserve all four shades | Preserve only when every pixel is black/white |
| Grayscale (0), depth 4 | Normalize | Preserve when all samples are 0/5/10/15 |
| Indexed (3), depth 1 | Normalize | Normalize |
| Indexed (3), depth 2 | Preserve with a compatible palette | Preserve only with a compatible palette and black/white pixels |
| Indexed (3), depth 4 or 8 | Normalize | Preserve with a compatible palette |

Every declared PLTE entry, including unused entries, must be exactly RGB
`(0,0,0)`, `(85,85,85)`, `(170,170,170)` or `(255,255,255)`; subsets and arbitrary
order are allowed. Alpha encodings, tRNS, interlace, animation, other formats,
wrong dimensions or incompatible colors require normalization. Compatible
renderer dithering is preserved regardless of Screen policy.

For gray16, native grayscale depths 1, 2 and 4 pass through; indexed depth 1
requires normalization, while depths 2, 4 and 8 pass through with an exact
compatible palette. Gray16 palette entries are `(n,n,n)` for n = 0,17,…,255.
For Spectra 6, indexed depths 1, 2, 4 and 8 pass through with an exact compatible
palette; native grayscale and RGB require normalization. Every declared entry,
including unused ones, must be in the target palette; subsets and arbitrary
order are allowed for every indexed fast path. All fast paths require the exact
resolved dimensions, opacity, static noninterlaced encoding and byte budget.

The Spectra 6 transmission/matching palette is black `(0,0,0)`, white
`(255,255,255)`, yellow `(255,243,56)`, red `(191,0,0)`, blue `(100,64,255)` and
green `(67,138,28)`. Pure red `(255,0,0)` requires conversion. These reference
colors come from the pinned [EPF palette](https://github.com/jwchen119/EPF/blob/65a5f83f21be15ef4525f7ae3069a58b7187e4d2/epf/imaging.py#L21-L30).
Generated gray16 uses native type 0/depth 4, including black/white-only content.
An executable indexed example and all six RGB tuples are in the
[renderer guide](RENDERERS.md#rgb-and-indexed-spectra-example).

Generated Spectra 6 uses type 3/depth 4 with exactly these six PLTE entries in the
listed order, and pixels reference only those six entries. These are selected
reference colors, not calibration evidence for every panel. Profile support is
source reviewed and software tested; physical acceptance remains separate from
the historical OG baseline.

Every controller-generated PNG has the effective target dimensions, is opaque,
static and noninterlaced, and uses PNG compression method 0 and filter method 0.
Native grayscale output has no PLTE chunk. The controller does not automatically
generate one-bit output; compatible one-bit input may still pass through.
The effective palette selects the encoding independently of model defaults.

`ArprtScreen.spec.imageProcessing.resizeMode` selects `contain` (default) or
`stretch`; `.dithering` selects `atkinson` (default), `floyd-steinberg`, `bayer` or
`none`. Omitted and explicit defaults are equivalent. Normalization composites
transparency onto white, resizes with half-pixel four-neighbor
bilinear interpolation, then matches colors in Lab D65. Both resize modes allow
enlargement and reduction. Contain preserves aspect ratio and centers the entire
image with white padding, placing an odd extra pixel on the right/bottom;
stretch fills the target without preserving aspect ratio. Neither mode crops.
`none` disables dithering while retaining necessary conversion. Renderers own content
rotation; the controller does not rotate it again. Numerical rules are in
[DESIGN](DESIGN.md#numerical-processing-semantics).

Generated gray4 temporarily defaults to native grayscale type 0, depth 2 for
all four shades, accepting the historical firmware color-count heuristic. The
`gray4_native4` build tag retains the intended depth selection: depth 2 for
globally black/white pixels and depth 4 otherwise. Exact samples map between
0/1/2/3 and 0/5/10/15 without re-dithering. Re-encoding omits source metadata.
Native 4-bit assumes corrected firmware expansion; the historical OG / 1.8.17
physical baseline does not validate this encoding. See the
[temporary build switch](DEVELOP.md#temporary-gray4-encoding-switch). The build
modes have different encoder identities for freshness, replay and derived
caches. Physical observations are needed before claiming corrected-firmware
support; they are not a publication prerequisite. This switch does not expand
verified device support.

Processing has a 10-second bound after reception/collection, further bounded
by enclosing contexts. The exclusive output limit applies to passthrough,
normalized bases and final overlays, including all chunks; equality with the
limit is oversized. Oversized/invalid processing preserves an
exact-target compatible previous success or pending; neither dithering nor
content changes to fit. Optional overlays run after normalization and may fall
back to the validated base. Issued URLs continue serving their original bytes
without normalization, including old images no longer eligible for new delivery.

The [legacy inputs and normalized output](../testdata/png/README.md) exercise
migration separately from passthrough. Job file/mount/exit requirements are in
[DESIGN](DESIGN.md#job-lifecycle-and-collection).

## Controller overlays

`ArprtScreen.spec.overlay` optionally draws timestamp, battery and Wi-Fi bitmaps
on new authenticated deliveries. Renderer output remains immutable base data;
`/images/<digest>.png` always serves already persisted bytes. Disabled/empty
settings return the original base filename and bytes. Compositing/persistence
failure selects that validated base before the usual final authorization and
issuance gates. Issue or delivery-state failure returns 503, without fallback.

Timestamp is the original base `RenderedAt`, converted using the explicitly
configured IANA zone and displayed as `↻ YYYY-MM-DD HH:mm` with a clockwise-arrow
bitmap identifying image generation time. Overlay observations use
only valid percentage/voltage/RSSI and the charging flag from the current
authenticated request;
renderer Context may still merge valid known state. Missing overlay observations
hide their items and gaps. Percent takes priority; valid voltage maps directly
to low/medium/high icons with shared thresholds for every device model, without
producing a measured percentage. Known charging
selects a charging mark; false or unavailable charging keeps the level icon.
USB connection never selects the mark. A mark needs no level measurement. Configuration,
thresholds and bitmap dimensions are in [OVERLAYS](OVERLAYS.md).

Nonnegative Screen `margin.top`, `.bottom`, `.left` and `.right` independently
inset the padded content row from each logical viewing edge, scaling with the bitmap pixels;
each defaults to zero. Only the two edges adjoining the selected corner apply.
The content row has fixed 2px padding on all sides, scaled with the bitmap.
The opaque white background extends through the margin to the corner edges.
Corners follow the logical viewing orientation; only overlay coordinates are
rotated, preserving the already rotated base. Playlist/discovery replay returns
the committed digest/filename/bytes and serving time even if telemetry, zone or
appearance changes. Definition, assignment, UID and target changes retain their
existing replay invalidation rules. There is no new HTTP request deduplication.
