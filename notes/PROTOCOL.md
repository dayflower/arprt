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

Source inspection, [synthetic fixtures](../testdata/phase0/README.md) and
[physical acceptance](DEVELOP.md#recorded-acceptance-baseline) are distinct evidence.
Other model/firmware tuples require explicit compatibility review.

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
and reported by field name only. Never infer percentage from OG voltage.

Only reviewed `og` / `1.8.17` resolves to `800 × 480 / gray4`. Firmware does not
send a semantic palette. Missing observations may use known valid state for that
identity; contradictory/invalid dimensions invalidate the target. A changed or
partial changed model/firmware tuple cannot borrow the old tuple's missing half.
An unresolved target invokes no renderer. Capabilities and telemetry are status,
not desired device configuration. Other semantic palettes recognized by context
types do not imply device support.

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
| Missing assignment, unresolved target, initial pending, or failed render with no compatible image | 200 / 202 | No image URL/filename; keep existing e-paper content |
| Successful render or compatible stale fallback | 200 / 0 | Serve an issued immutable image |

Pending is **HTTP 200 with JSON 202**, since firmware's Wi-Fi transport rejects
HTTP 202 as a successful request. This branch fast-polls at 5 seconds for polls
1–50, 60 for 51–60, 900 for 61–70, then 3600. A successful image resets the streak;
JSON `refresh_rate` cannot override this pending branch. Never use JSON 500 for
renderer failure because it follows a firmware reset path.

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

Require HTTP 200 and `image/png`; reject redirects. GET/POST share eight execution
slots and a 10-second overall rendering deadline. Reuse/timing differs by source
and assignment mode; see [DESIGN](DESIGN.md#rendering-and-assignment).

## Render Context v1

The same JSON schema is used for POST bodies and immutable Job context files.
The [complete fixture](../testdata/phase0/render-context-v1.json) and
[no-telemetry fixture](../testdata/phase0/render-context-no-telemetry.json) are
serialization examples used by tests; the [fractional-percentage fixture](../testdata/phase0/render-context-fractional-percent.json)
adds an explicitly measured decimal value while preserving the old fixtures.

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
`TrmnlDevice.spec.rotation`, rather than observations. Zero rotation is omitted
from JSON to preserve compatibility with existing contexts and artifact keys.
Renderers apply clockwise content rotation themselves: for `90`/`270`, lay out
content on a logical 480 × 800 canvas, then rotate into the required physical
800 × 480 PNG. Width/height always describe the final PNG.

POST overlays current authenticated observations on valid known state without
waiting for informer propagation. Job snapshots are copied at scheduling and
cannot change in flight; their telemetry may be older at display time. GET has
no context. Optional fields may be added within v1; renderers ignore unknown
fields. Removing fields or changing semantics requires a new context version.

## PNG contract

Require exactly 800 × 480, noninterlaced 2-bit PNG, compression/filter methods 0,
and **less than 90,000 compressed bytes**. Accept grayscale (color type 0) or
indexed (3) with exactly four opaque ascending palette entries: RGB `(0,0,0)`,
`(85,85,85)`, `(170,170,170)`, `(255,255,255)`. This is intentionally narrower than
the firmware decoder's possible inputs. Reject alpha/transparency, APNG,
truecolor, other depths, bad CRCs, truncation, wrong dimensions and oversized data.
Validate the complete bounded pixel stream, not only IHDR. The controller validates renderer base bytes before limited bitmap compositing
and validates composites with the same contract. It preserves exact gray4
samples outside the opaque overlay; it performs no resampling or quantization
and does not accept otherwise invalid renderer output. Generic PNG encoders often need
explicit configuration to avoid their RGB/8-bit defaults.

Both [grayscale](../testdata/phase0/target-gray4.png) and
[indexed](../testdata/phase2/README.md) fixtures are maintained test inputs.
Job file/mount/exit requirements are in [DESIGN](DESIGN.md#job-lifecycle-and-collection).

## Controller overlays

`TrmnlScreen.spec.overlay` optionally draws timestamp, battery and Wi-Fi bitmaps
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
hide their items and gaps. Percent takes priority; OG voltage maps directly to
low/medium/high icons without producing a measured percentage. Known charging
selects a charging mark; false or unavailable charging keeps the level icon.
USB connection never selects the mark. A mark needs no level measurement. Configuration,
thresholds and bitmap dimensions are in [OVERLAYS](OVERLAYS.md).

Nonnegative Screen `margin.horizontal` and `margin.vertical` independently inset
foreground along the logical viewing axes, scaling with the bitmap pixels; each
defaults to zero. The opaque white background extends through both insets to the
adjoining corner edges.
Corners follow the logical viewing orientation; only overlay coordinates are
rotated, preserving the already rotated base. Playlist/discovery replay returns
the committed digest/filename/bytes and serving time even if telemetry, zone or
appearance changes. Definition, assignment, UID and target changes retain their
existing replay invalidation rules. There is no new HTTP request deduplication.
