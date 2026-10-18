# Image overlays

This document describes controller-managed bitmap overlays, their configuration,
appearance and measurement thresholds. [DESIGN](DESIGN.md) owns implementation
boundaries, publication, authorization, corner recovery and retention;
[PROTOCOL](PROTOCOL.md) owns the BYOS, Render Context and PNG contracts.
[DEVELOP](DEVELOP.md) describes software checks and physical-device regression.

## Purpose and configuration

The controller can add a compact overlay containing the original image's render
timestamp, a battery icon and a Wi-Fi signal-strength icon. Each screen controls
whether to use an overlay and which information to display.

Public configuration describes display intent. Composite artifacts, cache keys
and corner-cycle bookkeeping remain internal; no artifact or execution CRD is
introduced. Configuration uses `ArprtScreen.spec.overlay` and the controller option
`--overlay-time-zone`; see [README](../README.md#display-overlays) for examples.

| Setting | Scope | Behavior and default |
| --- | --- | --- |
| Enablement | `ArprtScreen` | An unspecified overlay is disabled. |
| Displayed items | `ArprtScreen` | Select render timestamp, battery and/or Wi-Fi signal strength. When enabled with no item selection specified, show all three. |
| Position | `ArprtScreen` | Select top-right, bottom-right, bottom-left or top-left; default to fixed top-right. |
| Corner cycling | `ArprtScreen` | Select a fixed corner or cycling through all four corners; fixed is the default. |
| Scale | `ArprtScreen` | Select automatic scaling or an explicit positive integer; default to automatic. |
| Margin | `ArprtScreen` | Nonnegative `margin.top`, `.bottom`, `.left` and `.right` in unscaled bitmap pixels independently inset the padded content row from each logical viewing edge; each defaults to 0. Only the two edges adjoining the selected corner apply, and the white background extends to those edges. |
| Timestamp format | Fixed | `↻ YYYY-MM-DD HH:mm`, showing image generation time with a 24-hour clock and no seconds. |
| Display time zone | Controller | An explicit IANA time-zone name, such as `Asia/Tokyo`; default to `UTC`. |

The settings below define item selection, missing-value behavior, positioning,
colors, scaling and failure fallback. Measurement thresholds are presentation
conventions rather than additional measured telemetry.

## Base images and delivery composites

Keep renderer output as an immutable, validated base image without any
controller-added overlay. Preserve existing GET sharing, POST ownership and Job
rendering/scheduling behavior. Device telemetry must not be baked into a shared
GET base image.

For a new, authenticated `/api/display` delivery:

1. Select an eligible base image using the existing fresh/stale delivery rules.
2. Read the base image's recorded render timestamp and the current request's
   valid telemetry.
3. Resolve the screen's overlay settings, display time zone, scale and corner.
4. Create or reuse the corresponding composite image.
5. Validate and persist the composite, then authorize and durably issue it to
   the receiving device before returning its URL and filename.

Identical base image and overlay inputs may reuse a composite. Inputs include
the displayed timestamp, battery/RSSI levels, selected items, position, scale,
the two active edge margins, charging-mark selection and appearance. Margins on the opposite edges do not enter the composite identity. Bytes determine
image digests and firmware filenames under the existing protocol contract. Even when devices reuse identical bytes, each
receiving device UID still needs its own issuance authorization.

The composite is finalized during `/api/display`, not generated dynamically
while serving `/images/<digest>.png`. An issued URL must always return the same
bytes. Compositing does not replace the base image, update its `RenderedAt` or
make its content fresh again. A stale base image retains its original timestamp.

If the overlay is disabled or there is nothing to draw, deliver the base image.
Do not reserve blank space for unselected or unavailable items.

## Render timestamp and time zone

Display the controller-recorded `RenderedAt` associated with the selected base
image. This is the image's recorded render time, not the delivery time or the
update time of data depicted inside the image.

Keep the underlying timestamp in UTC and convert it only when drawing the
overlay. The controller's display time-zone setting applies to all devices it
serves. It affects overlay timestamp presentation, not renderer behavior or log
timestamps, and must not implicitly depend on the Pod's `TZ` or local time zone.
No device- or screen-specific time-zone override is required in this feature.

Prefix the timestamp with an original clockwise-arrow bitmap (`↻`) to identify
it as the image's generation time rather than a live clock. The timestamp item
always displays `↻ YYYY-MM-DD HH:mm`; the mark is hidden together with the
unselected timestamp and has a 4-pixel gap before the text. Digits and punctuation
require no locale-specific fonts, month names or weekday names, so no locale
setting is introduced. The arrow is an icon, not a font glyph.

## Telemetry and missing values

Use valid values from the current authenticated display request when creating
a new composite. Do not substitute older stored device telemetry for missing
overlay observations. A missing or invalid item is hidden, rather than shown
as zero or as an unknown-state icon.

Preserve the existing optional-observation rules in PROTOCOL. Valid zero values
must remain distinct from missing values. Battery percentage is usable only
when explicitly supplied as a finite measurement in the range 0 through 100,
inclusive. Voltage must be finite and nonnegative; RSSI is a signed integer in
dBm.

The BYOS parser accepts optional `Percent-Charged` observations. Its presence in
the general TRMNL API does not mean every model sends it, or that arprt supports
that model's display target. OG does not supply it in the reviewed firmware. Target resolution remains
subject to the existing verified support boundary.

Do not smooth measurements or apply hysteresis. Recompute the icon level from
each new delivery's observations; movement between adjacent levels at a boundary
is acceptable.

### Battery

Show three battery levels: low, medium and high, or a charging mark in place
of the level icon. Do not display a numeric percentage.

Prefer valid `Percent-Charged`. If it is missing or invalid, use valid
`Battery-Voltage` with the same presentation thresholds for every device model.
Hide the battery icon if neither input can be used.

| State | Measured percentage | Common voltage fallback |
| --- | --- | --- |
| Low | `0 <= p < 20` | `0 <= v < 3.6 V` |
| Medium | `20 <= p < 60` | `3.6 V <= v < 3.8 V` |
| High | `60 <= p <= 100` | `v >= 3.8 V` |

These are display thresholds. The voltage thresholds are provisional
heuristics shared across models, not official or physically calibrated
state-of-charge boundaries.
The two columns are not a claim that each voltage range equals the corresponding
percentage range. Voltage depends on load, temperature and battery aging; see
[TI's discussion of voltage-based battery estimation](https://www.ti.com/document-viewer/lit/html/SSZT786/GUID-5DD8C3D9-BCBE-467B-90E5-FAA672AAC15D).
Adjust the thresholds if physical-device observations warrant it.

Voltage maps directly to an icon state for presentation. Never derive a numeric
percentage from voltage or store/export such an estimate in device status or
Render Context. This preserves the existing measured-percentage-only contract.

### Charging indication

Use `Battery-Charging` from the current request when valid (`1` means charging,
`0` means not charging). Missing, invalid or duplicate observations are
unavailable; do not fill them from stored device state. USB connection does not
establish active charging and never selects the charging mark.

Show an 18×18 charging mark instead of the battery-level icon when the selected
state is true, even without voltage or a measured percentage. Otherwise use
the battery levels above; hide the item if no level input is usable. Selecting
no battery item hides both kinds of icon. When charging is false or unavailable,
ordinary level selection remains unchanged.

No public status or Render Context charging fields are added. Do not infer
charging from USB connection or high/rising voltage. The reviewed OG firmware
1.8.17 uses a power reader returning unknown for charging and USB presence, so
it sends neither header and keeps its ordinary battery-level icon. See the pinned
[power reader](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/include/power.h)
and the [firmware evidence in PROTOCOL](PROTOCOL.md#firmware-evidence).

### Wi-Fi signal strength

Use four rising vertical signal-strength bars with levels 0 through 4,
progressively filling bars from left to right. Unfilled bars retain their
outlines; missing RSSI hides the icon.

| Level | RSSI in dBm |
| --- | --- |
| 0 | `r < -88` |
| 1 | `-88 <= r < -77` |
| 2 | `-77 <= r < -66` |
| 3 | `-66 <= r < -55` |
| 4 | `r >= -55` |

These thresholds follow the Android Open Source Project's default
[`config_wifiRssiLevelThresholds`](https://android.googlesource.com/platform/packages/modules/Wifi/+/refs/heads/main/service/ServiceWifiResources/res/values/config.xml).
They are a chosen display convention, not a universal smartphone standard.
Level 0 means very weak signal, not disconnected. RSSI represents received signal
strength, not overall network performance or Internet reachability.

## Bitmap appearance and scaling

Use bitmap assets for both icons and text. At scale 1:

| Element | Specification |
| --- | --- |
| Icons | 18 by 18 pixels |
| Font | Home Video 0.90, 12 by 18 pixels per character |
| Item order | Clockwise arrow with render timestamp, battery, Wi-Fi signal strength |
| Layout | One horizontal row; all visible elements use 18-pixel-high cells, with matching 14-pixel ink height |
| Space between visible items | 4 pixels |
| Foreground/background | Black on opaque white |
| Padding | Fixed 2 pixels on all four sides of the content row, scaled with the bitmaps; preserve font cell whitespace |
| Margin outside the padded content row | `spec.overlay.margin.top`, `.bottom`, `.left` and `.right`, each default 0; the two adjoining edge margins apply and contain white background |

Use the 13 timestamp glyphs from
[home-video-font-bitmap](https://github.com/dayflower/home-video-font-bitmap)
at `6034a71f677f4230e6cccadc427a3a18ff1b3d80`, derived pixel-exactly from
Home Video by GGBotNet under CC0 1.0. The vendored
[font notice and license](../internal/overlay/homevideo/README.md) records the
source and regeneration procedure. Icons are original arprt assets, drawn with
14-pixel visible ink height (rows 2 through 15) to match the numeric glyphs;
all text/icon cells start at the same vertical origin. The render-time marker
uses an open circular arc and a large filled arrowhead, with a clear break
between its tip and the trailing arc. Battery/charging outlines
use two-pixel strokes. Wi-Fi strength uses four bottom-aligned vertical bars,
increasing in height from left to right (5, 8, 11 and 14 pixels). Bars are 3 pixels
wide with 2-pixel gaps; the level 0–4 counts filled bars. Inactive bars retain a
one-pixel outline, including all four at level 0. Only current Wi-Fi RSSI controls
this icon; its shape does not indicate a cellular connection.

The content row has two white pixels of padding on every side, including after
the rightmost Wi-Fi bar. Padding applies to every item selection and corner,
independently of the Screen margin. Margin insets the padded row from the two
edges adjoining the selected corner; white continues through that margin to the
display edges. The other two margins do not affect layout or its fit check.

At scale 1, all three items have 258×18 pixels of content in a 262×22 padded row.
`margin: {top: 4, bottom: 8, left: 12, right: 16}` produces a 278×26 white
rectangle at top-right and a 274×30 rectangle at bottom-left. The timestamp
alone, including its arrow, has 214×18 content in a 218×22 padded row. Battery
and Wi-Fi together have 40×18 content in a 44×22 padded row. If no items are
visible, neither padding nor a white rectangle is drawn.

Omit hidden items and their gaps. Covering the screen's underlying content is
allowed; no renderer-reserved area or collision avoidance is required. Negative
margins are rejected by the API; a layout exceeding the logical canvas uses
the existing validated-base fallback rather than clipping or shrinking.

Scale text, icons, padding, the active edge margins and inter-item spacing by the same
positive integer. Replicate bitmap pixels without interpolation or antialiasing.
For automatic scaling, use physical output dimensions and the following rule:

`scale = max(1, floor(min(width, height) / 480))`

| Output resolution | Automatic scale |
| --- | --- |
| 800 by 480 | 1 |
| 1600 by 960 | 2 |
| 2400 by 1440 | 3 |

The scale does not change when content rotation changes. These resolution
examples describe the scaling rule and do not expand arprt's currently verified
device or PNG support. An explicit integer scale overrides automatic scaling.

## Orientation and corner cycling

Corners are expressed in the user's viewing orientation, accounting for
`ArprtDevice.spec.rotation`. Text and icons must appear upright in that
orientation. Renderers already rotate their output; composing the overlay must
not rotate the base image a second time.

Corner cycling is intended to move the overlay away from a permanently fixed
location. It is not a guarantee against e-paper ghosting or other panel effects.

Cycle clockwise: top-right, bottom-right, bottom-left, top-left, then repeat.
Keep one durable cycle position per device UID, shared across screens that opt
into cycling:

- Start at top-right.
- Use the current corner for the composite being delivered; advance the next
  position after successful composite issuance and delivery-state commitment.
- A new delivery can advance the corner even when the base image has not been
  rendered again.
- Fixed-position screens do not advance this cycle.
- Disabled, empty or failed overlays delivered as base images do not advance it.
- Existing replay and retries within the same delivery use the already committed
  image and position, without another advancement.
- Preserve the position across controller restart; a replacement device UID
  does not inherit the old device's state.

## Replay and failure behavior

Within the existing playlist/discovery replay window, return the previously
committed composite or base image with exactly the same digest, filename and
position. Observe telemetry as usual, but do not redraw the overlay, advance its
corner or extend the replay window. Use later observations on the next new
delivery.

BYOS provides no verified unique display-request ID. This feature does not add
general deduplication of HTTP retries or change the existing single-screen
per-request behavior. Follow the existing replay eligibility and invalidation
rules in DESIGN.

If compositing, final PNG validation or composite persistence fails, attempt to
deliver the selected base image without an overlay. The base must still pass
current eligibility, authorization and durable issuance gates. Failure of those
gates cannot be bypassed by overlay fallback. If no compatible base image exists,
keep the existing pending/error behavior.

## Image and artifact guarantees

The controller normalizes renderer input before bounded bitmap compositing.
Validate the normalized base and the final composite against the same resolved
target before publication/issuance. Preserve base pixels outside the overlay,
physical dimensions, rotation compatibility and semantic palette. Active OG
delivery uses the [PNG contract](PROTOCOL.md#png-contract), including generated
the temporary gray4 encoding switch. Legacy appearance/encoding records stay readable.

The internal resolved-target compositing path supports gray4, gray16 and
Spectra 6 within the shared output dimension/allocation bounds. It maps exact
palette colors, including reordered/subset indexed palettes, without resampling,
quantization or dithering. The shared encoder defaults to 2-bit gray4. With the
`gray4_native4` build tag, it chooses depth from the whole final image: 2-bit for
black/white only, otherwise 4-bit. Drawing over the last intermediate gray may
then change the depth. Gray16 uses 4-bit grayscale;
Spectra 6 uses 4-bit indexed output with the six canonical transmission colors.
All untouched pixel colors remain exact; source PNG metadata is not copied.

Both base and final candidate must satisfy the resolved target's exclusive
`encodedBytes < pngByteLimit` budget. An oversized optional composite returns
failure for validated-base fallback; it never changes content or compression
to fit. The resolved drawing cache identity includes bitmap, encoder and palette
versions, preserving the original base render time. Legacy derived records and
previously issued URLs retain their immutable bytes across restart and GC.
These palette primitives do not enable additional Device targets or establish
physical acceptance for another model.

Base and composite artifacts must preserve UID ownership, exact-target
compatibility, ordered publication, current assignment and authentication
checks, and issued-image retention. Record enough internal provenance to retain
the original render timestamp and keep composite generation separate from base
rendering success/freshness metadata.

Do not expose credentials or hardware identities through overlays, Render
Context, logs, Events or metrics. No additional device telemetry display beyond
the specified battery and RSSI icons is required.
