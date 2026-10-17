# Device display profiles and image processing requirements

## Status and scope

This document specifies proposed behavior, not an implemented feature or an
expanded support claim. The current verified baseline remains TRMNL OG /
firmware 1.8.17 / 800 x 480 gray4, as recorded in [README](../README.md).
arprt is independent and unofficial; it is not affiliated with or endorsed by
TRMNL.

The goal is to accept general-purpose renderer PNG output and prepare a suitable
image for each device, while preserving output that is already compatible.
Keep the rendering interface usable by renderers that either optimize for the
target or return an image without device-specific quantization.

The firmware-review scope covers OG and reTerminal E1001, E1002, E1003 and E1004,
including their different resolutions and palettes. E1002 hardware is available
for physical acceptance. Investigate the other models through firmware
sources even without physical access. Record source-reviewed support and
physical acceptance separately; lack of hardware must not exclude a model from
the review. A shared product family or palette alone is not compatibility evidence.

Until implemented, the stricter current contracts in [PROTOCOL](PROTOCOL.md),
[DESIGN](DESIGN.md) and [RENDERERS](RENDERERS.md) remain authoritative. In
particular, the current controller rejects general RGB PNGs, does not resize or
quantize images, and accepts only its reviewed gray4 target. Implementation must
update those owning documents, API schemas, context fixtures and examples
together. This document records the intended changes without changing runtime
behavior.

### Document lifecycle

Commit this document as a requirements proposal before implementation and keep
it versioned while implementation is in progress. Update it when an explicit
requirements decision changes, so implementation and review have a shared
reference. Its presence does not change the current runtime contracts or
verified support scope.

When implementation is complete, integrate the requirements and relevant
rationale into their owning documents and remove this standalone proposal:

| Owning document | Content to integrate |
| --- | --- |
| [PROTOCOL](PROTOCOL.md) | PNG formats and palettes, passthrough conditions, renderer and device wire contracts |
| [DESIGN](DESIGN.md) | Image-processing pipeline and rationale, firmware constraints, cache and publication compatibility |
| [RENDERERS](RENDERERS.md) | Renderer-author guidance and output examples, linking to the canonical protocol contract |
| [OPERATIONS](OPERATIONS.md) and [README](../README.md) | Configuration, resource and processing limits, operational guidance and verified support scope |

Update API declarations, generated schemas, examples and fixtures together with
the corresponding implementation. Keep firmware-specific rationale beside the
relevant code or in DESIGN, including the pinned evidence for the gray4 depth
selection rule. Avoid retaining duplicate specifications or completed task logs
in this proposal after integration.

## Device capabilities and explicit overrides

Resolve the effective display target from model/dimension observations and
operator configuration:

- Maintain reviewed model profiles describing physical width, height,
  semantic palette and compatible PNG encoding constraints.
- Do not gate automatic profile selection or rendering on a firmware-version
  allowlist. Keep the firmware version as an observation available for context
  and diagnostics, and pin source revisions as review evidence, without making
  those versions runtime eligibility conditions.
- Allow explicit palette, resolution and PNG byte-limit overrides in
  `ArprtDevice.spec.display`.
  Explicit values take precedence over automatic profile values.
- A known model must remain eligible with a missing or unknown firmware version.
  For an unknown model, require an explicit palette and PNG byte limit, plus
  a complete valid dimension pair from explicit configuration or observations.
- If neither automatic resolution nor overrides produce a complete valid target,
  including an output byte limit, do not invoke a renderer or guess missing
  capabilities. Report the unresolved state using the existing pending/status
  mechanisms.
- Keep received observations distinguishable from the effective target and the
  source of its values. An override must not rewrite the reported model,
  firmware or raw dimensions as though the device had sent different values.
- Treat an explicit override as operator-provided compatibility information,
  not as physical verification or permission to bypass device authentication.

The agreed optional Device fields are `spec.display.width`,
`spec.display.height`, `spec.display.palette` and `spec.display.pngByteLimit`;
they do not exist in the current CRD. Omitting `display` selects automatic
resolution. Palette-only and PNG byte-limit-only overrides are valid. Width and
height must be specified together as positive integers; a resolution-only
override is also valid.

Resolve the palette in this order: explicit `spec.display.palette`, then the
model profile's default palette. Resolve dimensions as a pair in this order:
explicit `spec.display.width`/`height`, then valid device-reported dimensions,
then the model profile's default dimensions. Do not mix one dimension from one
source with the other dimension from another source.

`spec.display.pngByteLimit` is a positive integer defining an exclusive limit
on the complete encoded output PNG, including all chunks. Accept output only
when `encodedBytes < pngByteLimit`; a file equal to the limit is oversized.
For example:

```yaml
spec:
  display:
    pngByteLimit: 750000
```

This permits files smaller than 750,000 bytes, so the firmware's boundary can
be configured directly without subtracting one. Resolve the limit in this
order: explicit `spec.display.pngByteLimit`, then the reviewed model profile's
default. Do not use zero as an unlimited sentinel.

Use these model defaults, based on the reviewed upstream TRMNL firmware:

| Model | Default `pngByteLimit` (exclusive) |
| --- | --- |
| OG | 90000 |
| E1001 | 750000 |
| E1002 | 750000 |
| E1003 | 750000 |
| E1004 | 750000 |

Use an explicit Device override when the installed firmware has a different
limit. In particular, the reviewed Seeed common firmware requires
`spec.display.pngByteLimit: 90000` for E1001/E1002/E1003. Do not select defaults
by firmware version or attempt to infer the distribution from that version.
Model defaults remain in effect unless explicitly overridden. A default is a
byte-budget policy, not a guarantee that all images can fit or that every
distributed firmware binary has been verified.

When explicit dimensions contradict device-reported dimensions, honor the
explicit dimensions and retain the observations separately so the difference
can be inspected.

When no reviewed model profile is available, use these requirements:

| Required capability | Source for an unknown model |
| --- | --- |
| Semantic palette | Explicit `spec.display.palette` |
| Width and height | Explicit dimension pair, otherwise a valid device-reported pair |
| Exclusive encoded PNG byte limit | Explicit `spec.display.pngByteLimit` |

Do not guess an unknown model's palette or assign a global/default PNG byte
limit, including the OG limit. Do not borrow model dimensions from a different
device profile. Missing palette, byte limit or a complete valid dimension pair
leaves the target unresolved; do not invoke a renderer and report the missing
capability through the existing status/pending behavior. Keep these fields
optional in the CRD because known model defaults can resolve them; missing
runtime observations must not require admission-time knowledge of the model.

An unknown model may operate once all three capabilities are resolved. For
example, if the device reports a valid dimension pair, explicit palette and
byte-limit settings suffice without repeating its dimensions. If dimensions
are unavailable, both width and height must also be explicitly configured.
A known model still uses its model defaults when the firmware version is
missing or unknown, unless the relevant Device field is explicitly overridden.

The reviewed TRMNL and Seeed firmware send model, firmware and display dimensions
on display requests, but do not send a semantic palette such as `gray4` or
`spectra6`. Setup requests provide fewer observations. Infer automatic palette
capabilities from the model profile rather than assuming a palette header exists.
See the [firmware header builder](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/lib/trmnl/src/api-client/request_headers.cpp).

A palette describes an available color set, not a numeric maximum color count.
For example, `spectra6` does not include the intermediate neutral grays in
`gray4`, although both include black and white. Do not rank these profiles solely
by their numbers of colors.

The allocation policy decided by the pre-implementation investigation is
independent of PNG input limits: effective output dimensions must be at most
4096 on each axis and at most 4194304 pixels in total. Apply these bounds to
explicit and reported dimensions as well as profiles, using checked arithmetic.
An invalid explicit pair is rejected by admission; an invalid reported pair
cannot supply dimensions and permits a known model's default pair instead.
The reviewed E1003 model default is 1872 x 1404, following FastEPD's actual
panel configuration, with the explicit/reported/default precedence unchanged.
Recognize upstream's `reterminal_e100N` and Seeed common's `reTerminal E100N`
as explicit reviewed aliases for each reTerminal profile. Retain the received
model string as an observation; neither spelling changes the agreed byte-limit
default or removes the need for a distribution-specific explicit override.
See [the design decisions](DESIGN.md#image-processing-design-before-integration)
for the status representation, internal target and migration design.

## reTerminal extension boundary

Use the following hardware characteristics to guide extensibility, not as a list
of models already supported by arprt:

| Device | Hardware resolution | Hardware palette candidate |
| --- | --- | --- |
| TRMNL OG | 800 x 480 | `gray4` |
| reTerminal E1001 | 800 x 480 | `gray4` |
| reTerminal E1002 | 800 x 480 | `spectra6` |
| reTerminal E1003 | 1404 x 1872 | `gray16` |
| reTerminal E1004 | 1200 x 1600 | `spectra6` |

The reTerminal entries come from the [Seeed hardware comparison](https://wiki.seeedstudio.com/reterminal_e10xx_main_page/#specification-comparison).
The firmware's actual dimensions, orientation, decoder and transport limits
must be reviewed independently. In particular, the design must not retain a
global 800 x 480 assumption or apply OG image-size limits to all future devices.
All four reTerminal models are in scope for firmware investigation. The resulting
profile coverage must identify source-reviewed and physically tested models
separately, without requiring an exact firmware-version match at runtime.

E1002 color support has source evidence: the
[TRMNL v1.8.17 display implementation](https://github.com/usetrmnl/trmnl-firmware/blob/9acd348270a493ca5f1c627ac91a9469543267ae/src/display.cpp)
selects a Spectra panel, decodes PNG colors through `png_draw_6clr`, maps them to
six colors and writes the panel data. The
[reviewed Seeed source](https://github.com/Seeed-Projects/OSHW-reTerminal-Series-E-D/blob/8d97d8912987c07529287629f7e73039bb0e6384/examples/official/TRMNL/src/display.cpp)
also has an E1002-specific six-color path. This does not establish acceptance of
every distributed binary or physical-device behavior. Seeed's monochrome-only
documentation and the color implementation disagree; the scope of that
documentation claim remains unresolved. Do not turn that claim into an assumed
firmware color restriction.

### Firmware source findings

The reviewed upstream source at
[`d5f98f2`](https://github.com/usetrmnl/trmnl-firmware/tree/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa)
selects the OG-style gray4 path for E1001, the FastEPD grayscale path for E1003,
and the six-color Spectra path for E1002/E1004. Its E1003 dependency
[FastEPD panel definition](https://github.com/bitbank2/FastEPD/blob/9a84c35e52849f9f22d2a7ad5231ef501be49e04/src/FastEPD.inl)
sets 1872 x 1404, rather than the hardware table's portrait ordering. Preserve
the agreed explicit/reported/model dimension precedence when resolving the
actual output orientation. The FastEPD PNG path accepts native 4-bit grayscale.

The reviewed upstream grayscale callbacks also support the lower-depth type 0
fast paths. OG/E1001 select a single-plane black/white path for 1-bit PNGs.
E1003 uses 1-bit pixels directly, and expands 2-bit samples 0/1/2/3 to 4-bit
samples 0/5/10/15 before the calibrated gray refresh. That expansion preserves
the four intended grayscale values; it does not require controller-side
quantization or dithering. The reviewed Seeed common callback likewise handles
1/2/4-bit grayscale, but uses a native FastEPD 2-bit mode rather than the
upstream expansion for 2-bit E1003 input. Preserve this distinction in physical
acceptance: source support for decoding does not establish identical panel
appearance across firmware distributions.

The reviewed Seeed
[dedicated E1004 display source](https://github.com/Seeed-Projects/OSHW-reTerminal-Series-E-D/blob/8d97d8912987c07529287629f7e73039bb0e6384/examples/official/TRMNL_E1004/src/display.cpp)
uses the same RGB333 six-color matching calculation as the reviewed E1002 code.
All six selected reference RGB values map to their intended panel colors under
that calculation as well. This supports using the reference RGB values directly
in a Spectra 6 PNG palette, subject to decoder/physical acceptance.

Output byte limits vary between these sources. The reviewed
[upstream configuration](https://github.com/usetrmnl/trmnl-firmware/blob/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa/include/config.h)
and dedicated Seeed E1004 configuration select 750,000 bytes with PSRAM and
90,000 without; PNG drawing requires strictly less than the selected value.
The reviewed
[Seeed common configuration](https://github.com/Seeed-Projects/OSHW-reTerminal-Series-E-D/blob/8d97d8912987c07529287629f7e73039bb0e6384/examples/official/TRMNL/include/config.h)
instead selects the larger limit only for specific board macros, leaving its
E1001/E1002/E1003 environments at 90,000 bytes despite PSRAM. The agreed model
defaults use the upstream TRMNL limits; use the explicit Device PNG byte-limit
override for this distribution difference, as specified above.

### Indexed PNG depth review

The following are source and host-execution findings, not a physical support
claim. The agreed indexed passthrough rules appear in the image compatibility
section below. Compare the reviewed
[upstream display code](https://github.com/usetrmnl/trmnl-firmware/blob/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa/src/display.cpp),
the upstream v1.8.17 tag, the Seeed common source linked above, and the dedicated
Seeed E1004 source. The upstream v1.8.17 callbacks have the same relevant
indexed-depth behavior as the reviewed upstream revision.

| Target path | Indexed 1-bit | Indexed 2-bit | Indexed 4-bit | Indexed 8-bit |
| --- | --- | --- | --- | --- |
| OG/E1001 | Upstream supports black/white only; Seeed common reverses polarity | Palette conversion exists, but the two-index update heuristic can discard grays | Palette colors preserved | Palette colors preserved |
| E1003 | Missing 1-bit palette conversion produces black pixels | Upstream preserves gray16 palette entries; Seeed common uses inverted 2-bit output and cannot retain arbitrary gray16 entries | Palette colors preserved | Palette colors preserved |
| E1002/E1004 Spectra 6 | Palette colors preserved | Palette colors preserved | Palette colors preserved | Palette colors preserved |

The entries assume exact target-palette membership, opaque noninterlaced input
and otherwise compatible dimensions and capacity. Indexed 1-bit and 2-bit
Spectra input can contain at most two and four palette entries respectively;
all six Spectra colors require at least 4-bit PNG encoding.

Host checks used the actual
[PNGdec 1.1.6 source](https://github.com/bitbank2/PNGdec/tree/9a9c585fd39d148c5517597b02ce490e0fdb1bb4)
and extracted firmware callbacks without changing their conversion logic, with
mocked panel buffers and writes. Cases covered reversed palettes, subsets,
unused entries, intermediate grays, high 8-bit palette indices and representative
full-resolution inputs. Checks compare resulting pixel/buffer values, not
physical panel waveforms or visible appearance. The source dependencies use
device-specific PNG row-buffer sizes; do not substitute the decoder's smaller
library default when assessing full-resolution input.

The grayscale differences are material:

- OG/E1001 1-bit indexed input selects a black/white path that compares the two
  palette entries' brightness; intermediate gray entries cannot survive. The
  Seeed common callback uses the opposite comparison from upstream, reversing
  black and white even for an otherwise compatible two-entry black/white palette.
- E1003 routes indexed 1-bit input through `ReduceBpp`, which has no 1-bit
  source case. Its initialized gray value remains zero. In Seeed common,
  indexed 2-bit input also uses the inverted gray4 representation from
  `ReduceBpp` with the native FastEPD 2-bit mode; upstream instead converts
  indexed 2-bit input to 4-bit samples. These constraints do not apply to the
  separate lower-depth type 0 paths.
- Spectra callbacks explicitly unpack all four indexed depths and use the RGB
  palette entry before selecting a panel color. Arbitrary entry order and
  target-palette subsets preserve their intended colors in the reviewed code.

OG/E1001 have an additional issue affecting both type 0 and type 3 at 2-bit
depth. `png_count_colors` counts sample/index numbers without consulting palette
colors, and ignores rows after y=430. If it finds exactly two numbers, the draw
path switches to black/white output, potentially discarding intermediate grays.
Host controls reproduce this with two intermediate grays, and with black/white
content above row 431 but intermediate grays below it. A complete PLTE containing
all four grays does not prevent the issue if only two indices occur in the
counted rows. The counter's use of index numbers is not equivalent to detecting
the actual displayed colors.

The agreed gray4 encoding rules below address this behavior by allowing 2-bit
output only for globally black/white pixels and using type 0 4-bit output when
any intermediate gray is present. This deliberately avoids depending on the
firmware's counting region or on a count of three or more used values. The
semantic device palette remains gray4. Indexed passthrough choices must also
account for the upstream/Seeed decoder differences above.

The v1.8.17 and Seeed common `ReduceBpp` low-nibble grayscale branch uses
`(s[0] & 0xf) | (s[0] << 4)` with an integer gray accumulator, retaining unwanted
upper bits. Host checks show corrupted gray values; the reviewed upstream
revision masks the low nibble before shifting. Indexed 4-bit lookup avoids that
branch and preserves the intended colors in the same cases. This difference
is source evidence about older firmware, not a requirement for a workaround.

As of 2026-10-17 (JST), the latest published upstream release is
[v1.8.17](https://github.com/usetrmnl/trmnl-firmware/releases/tag/v1.8.17),
published on 2026-10-03 (JST), and its source still contains the incorrect
low-nibble formula. The fix was merged in
[PR #671](https://github.com/usetrmnl/trmnl-firmware/pull/671) on 2026-10-06
(JST), commit `47cc1f7c9a7a1e31cf09e75e329d8146dcfd7704`, and assigned to the
v1.8.18 milestone. Main-branch inclusion is not evidence of a published release
containing the fix.

The agreed policy is not to add an old-firmware workaround for this 4-bit
grayscale expansion bug. Assume the corrected expansion when considering
type 0 4-bit output; do not choose indexed encoding solely to avoid this bug,
add a compatibility Resource option, or introduce firmware-version eligibility
checks for it. This does not establish that the fix has already been released.

PR #671 only changes 4-bit grayscale expansion. It does not change the separate
2-bit two-color refresh heuristic. The selected type 0 4-bit gray4 encoding
assumes the corrected expansion and uses only samples 0/5/10/15. Host evidence
is not physical verification.

## Renderer interface

Pass the resolved output width, height and semantic palette to every renderer.
Preserve the existing interfaces: GET and POST receive target query parameters;
POST and Job also receive `renderTarget` in Render Context.

Renderer output may be optimized for those values, but matching palette and
dimensions are no longer preconditions for accepting an otherwise supported
input PNG. Renderers may return RGB PNGs, higher-depth grayscale PNGs, or images
of different dimensions. The controller must normalize them to the effective
target. This does not require supporting every PNG feature or arbitrary input
size; accepted input formats and limits must be explicit.

Preserve renderer-authored dithering when indexed output already meets the
effective target's palette, dimensions, encoding, opacity and byte budget.
Do not quantize or dither such output again merely because the Screen selects
a controller dithering method. Renderers may use their own algorithms; they do
not need to identify them or match the controller's selected algorithm. The
byte-preserving path retains their original pattern before optional overlays.

When implementing this feature, publish the exact Spectra 6 RGB tuples and the
indexed passthrough contract in the PNG section of [PROTOCOL](PROTOCOL.md).
Explain accepted bit depths, palette subsets and arbitrary entry order, exact
RGB matching of all declared entries including unused ones, and the remaining
dimension/opacity/encoding/capacity checks. Link that canonical contract from
[RENDERERS](RENDERERS.md), with a concrete indexed PNG example for renderer
authors. Explain how complying with the contract preserves a renderer's own
dithering and how nonmatching output enters controller normalization. Keep the
current runtime documentation unchanged until implementation updates the
contract; this requirement is not a claim that Spectra delivery works today.

Retain the existing rotation contract: renderers receive the requested clockwise
rotation and handle content layout/orientation. Target width and height describe
the final physical PNG. Controller resizing must not apply the rotation a second
time.

Credentials, MACs and arbitrary request headers remain excluded from Render
Context. GET rendering remains independent of device telemetry.

## PNG and APNG input

Accept the standard PNG color types and their legal bit-depth combinations,
including 16-bit grayscale/RGB/alpha input, indexed transparency and Adam7
interlacing, subject to the input resource limits. Device output remains subject
to the narrower target-specific encoding contract.

Interpret all decoded RGB channels, grayscale samples and indexed PLTE colors
as sRGB-encoded values. Do not apply embedded color-space metadata, including
ICC profiles (`iCCP`), `gAMA` or `cHRM`, to alter these values or perform profile
conversion. This interpretation is fixed rather than a Resource setting, and
also applies to the default/static image selected from APNG. Standard sRGB
transfer-function decoding for Lab processing still applies; ignoring embedded
metadata does not mean treating sRGB samples as linear-light RGB.

Color-space metadata alone must not force normalization or change exact palette
membership decisions. An otherwise compatible byte-preserving input retains its
original chunks, whose color-space declarations are ignored for this contract.
When re-encoding, do not copy ignored source color-space declarations into the
generated PNG. Input metadata bytes still count toward the complete-file limit,
and ordinary PNG structure and CRC validation remain required.

For APNG, extract the static/default image stored in the IDAT chunks, using
ordinary static PNG decoding. Always choose that image whether it is also the
animation's first frame or is a separate fallback image. Do not extract the
first animation frame from fdAT chunks, play the animation, or decode subsequent
animation frames. See the [PNG/APNG structure specification](https://www.w3.org/TR/png-3/#structure).

Apply the usual white-background transparency composition, resizing, palette
conversion and optional overlays to the selected static image. Publish a static
PNG without animation chunks; an APNG must not qualify for byte-preserving
passthrough merely because its default image meets the target's pixel format.
HTTP renderer input may use either `image/png` or `image/apng` for APNG; device
output remains `image/png`.

Still bound the entire input stream and validate the static PNG structure and
checksums. Avoiding animation-frame decoding does not remove input byte limits
or the need to handle malformed/truncated input.

### Input and processing limits

Use the following initial limits. They are separate from the device-specific
compressed output limits and apply to both HTTP renderer responses and Job
output collection:

| Item | Limit |
| --- | --- |
| Complete input file | 32 MiB (33,554,432 bytes) |
| Decoded image/canvas area | 16,777,216 pixels |
| Each input width and height | 16,384 pixels |
| Concurrent controller image conversions | 1 across all rendering sources |
| Decoding through output encoding | 10 seconds, bounded further by any enclosing request deadline |

For APNG, count the entire input file, including skipped animation data, toward
the byte limit. Apply the pixel and dimension limits to the selected static
image's canvas. Check dimensions and overflow before allocating image buffers;
a small compressed file does not imply a small decoded image.

Conversion time does not extend renderer transport timeouts or the enclosing
BYOS request deadline. Work waiting for the conversion slot must also respect
its enclosing context. Timed-out work must stop rather than continue in an
unbounded background task or release capacity while still consuming resources.

When implementing this feature, raise the default controller memory limit from
256 MiB to 512 MiB. A maximum-size 16-bit RGBA input needs 128 MiB for pixel data
alone. Resize before allocating Lab working data, and use row-based diffusion
error buffers rather than retaining a full-resolution Lab copy of the input.
Account for decoder/interlace buffers and overlapping allocations as well as
the final pixel buffer.

These are agreed starting values, not benchmark results. Verify peak memory and
processing time with representative and boundary-case inputs before release;
any later adjustment must be explicit rather than silently changing the image
processing policy. Input-limit or processing-deadline failure follows the
existing compatible-success fallback/pending rules.

## Image compatibility and processing

Separate input validation, output compatibility and image conversion. A valid
PNG can require conversion without being a rendering failure.

Skip normalization only when the image meets the target dimensions, palette,
encoding, opacity and output-size constraints. Preserve the original PNG bytes
in that case, unless an enabled overlay subsequently changes the image.

The required fast paths are:

- `gray4` (OG/E1001 defaults): accept compatible noninterlaced PNG color type 0
  with 1-bit depth without normalization. Preserve 2-bit input bytes only when
  all pixels are black or white (samples 0/3). If any intermediate gray occurs,
  rewrite to type 0 4-bit by mapping samples 0/1/2/3 to 0/5/10/15, without
  dithering or changing colors. Compatible 4-bit type 0 input using only
  gray4 samples 0/5/10/15 may also pass through unchanged.
- `gray16` (E1003 default): accept compatible noninterlaced PNG color type 0
  with 1-bit, 2-bit or 4-bit depth without normalization.
- Each supported target: accept compatible indexed-color PNGs (color type 3)
  without normalization according to the depth and palette rules below.

Use the following indexed passthrough rules, based on the reviewed upstream
TRMNL firmware behavior. All entries also require compatible dimensions,
opacity, static noninterlaced encoding and the resolved PNG byte limit:

| Effective palette | Type 3 1-bit | Type 3 2-bit | Type 3 4-bit and 8-bit |
| --- | --- | --- | --- |
| `gray4` | Normalize | Preserve only if the palette is compatible and all pixels are black/white | Preserve if the palette is compatible |
| `gray16` | Normalize | Preserve if the palette is compatible | Preserve if the palette is compatible |
| `spectra6` | Preserve if the palette is compatible | Preserve if the palette is compatible | Preserve if the palette is compatible |

Normalization means accepting the PNG as renderer input and producing the
controller-generated format below, not rejecting an otherwise valid input.
Gray4 indexed 1-bit input must be normalized even if it contains only black and
white; gray16 indexed 1-bit input must also be normalized. This restriction
does not change type 0 1-bit passthrough. Keep firmware-distribution differences
as documented compatibility limits rather than runtime version gates; in
particular, the reviewed older Seeed common E1003 indexed 2-bit path differs
from the upstream behavior assumed by this table.

Type 0 has fixed grayscale samples rather than a PLTE palette. The grayscale
sets of 1-bit and 2-bit input are subsets of `gray4`, and those of 1-bit, 2-bit
and 4-bit input are subsets of `gray16`. Type and depth establish these
grayscale-set relationships, but gray4 2-bit passthrough additionally requires
the whole-image black/white check above. Check actual samples for gray4 4-bit
passthrough as well, since its encoding can represent more than the four target
grays. All other fast-path checks still apply, including dimensions, opacity,
static and noninterlaced encoding, target decoder compatibility and byte limit.

For the gray4 black/white decision, reuse decoded pixels from input validation
or collect usage during quantization; do not decode a PNG again solely for this
decision. Consider all rows, including the final row. Classification may stop
once an intermediate gray is found, but complete PNG validation must still run.
Do not count the firmware's first 431 rows, reproduce its current cutoff, or
keep intermediate-gray 2-bit output merely because three or four values occur.

The implementation must place an English comment beside the gray4 depth
selection/passthrough decision explaining that the reviewed firmware's
`png_draw_count` stops at `y > 430` and can select black/white updates despite
gray pixels elsewhere. Include a pinned source link to the
[reviewed counting callback](https://github.com/usetrmnl/trmnl-firmware/blob/d5f98f2b88d09eb0c644a7d19a480ebce6d2b5aa/src/display.cpp#L1574)
and explain why the controller checks the complete image and uses 4-bit output
whenever intermediate gray is present. Treat 431 rows as documented firmware
evidence, not a controller algorithm constant or Resource setting.

For indexed fast-path palette matching, require every PLTE entry to equal an
exact RGB member of the effective device palette. Entry order does not matter,
and a subset such as black and white is sufficient. Check all declared entries,
including unused ones; an off-palette entry requires conversion even if no pixel
references it. Do not scan pixel usage merely to exclude unused entries from
this decision, or accept approximate RGB matches on the basis that firmware may
map them to the intended color. Opacity and all other compatibility checks still
apply. Supported indexed bit depths remain a separate decoder constraint.

For `spectra6`, palette compatibility means every declared PLTE RGB entry is
exactly one of the six reference RGB tuples below. All six colors need not be
present: black/white-only or black/red reference subsets also qualify. Palette
order is arbitrary. A nearby RGB tuple, such as pure red `(255, 0, 0)` instead
of the selected red `(191, 0, 0)`, does not qualify for passthrough even if
firmware would map it to red. An unused off-palette entry still requires
normalization. Determine this membership from PLTE without classifying actual
pixel colors; opacity is a separate compatibility condition.

For gray4 indexed 2-bit input, globally black/white pixels are also a necessary
condition for passthrough. If any used pixel is an intermediate gray, re-encode
to type 0 4-bit without re-dithering already compatible gray4 colors. This
pixel-usage check addresses firmware behavior; it does not allow unused
off-palette PLTE entries to be ignored or bypass any other decoder constraints.

RGB images containing only black and white still require conversion. Do not add
a full-image color-classification pass merely to give them a fast path.
Similarly, route type 0 input for Spectra 6 through the common conversion path,
even when it happens to contain only black and white. Exact matches to output
colors need not acquire quantization error during normal conversion.

For `gray4`, normalize type 0 4-bit input with samples outside 0/5/10/15, and
normalize 8-bit and 16-bit type 0 input. For `gray16`, normalize 8-bit and 16-bit
type 0 input. Use the selected dithering policy where palette reduction is
required. For Spectra 6, type 0 input with intermediate grays must be converted
to its available colors using that policy. Do not rely on firmware-side
reduction for these cases, even if the firmware can decode the original image.

The processing order is:

1. Validate and decode the renderer input within defined resource limits.
2. Composite transparency against white and resize as necessary.
3. Quantize to the target palette using the selected dithering policy, and encode
   a device-compatible PNG.
4. Apply an enabled controller overlay using colors available in that palette.
5. Validate the resulting image before publication and issuance.

Resize before quantization because interpolation can introduce colors outside
the target palette. Draw overlay text and icons after dithering so their edges
are not diffused into surrounding pixels. Compatible renderer output must not
be dithered again solely because a dithering method is configured.

Keep the existing timestamp meaning: the overlay shows the base image's recorded
render time. A delivery-time clock is not established by these requirements.
Preserve overlay enablement, item selection, placement, rotation, corner cycling
and fallback behavior described in [OVERLAYS](OVERLAYS.md).

## Controller-generated PNG output

Use the following encoding rules when the controller normalizes or re-encodes
an image, including when producing an overlay composite:

| Effective palette | Model defaults using this palette | PNG color type | Bit depth | Encoded colors |
| --- | --- | --- | --- | --- |
| `gray4` | OG, E1001 | 0 (grayscale) | 2 for black/white only; otherwise 4 | Neutral grays 0, 85, 170, 255 |
| `gray16` | E1003 | 0 (grayscale) | 4 | Neutral grays 0, 17, 34, ..., 255 |
| `spectra6` | E1002, E1004 | 3 (indexed) | 4 | Exactly the six RGB entries in the Spectra 6 reference palette below |

The effective palette, including an explicit Device override, selects the
encoding; the model column describes defaults rather than an additional gate.
Every generated PNG must have the effective target dimensions, be opaque,
static and noninterlaced, and use PNG compression method 0 and filter method 0.
The encoded file must also meet the effective device's output-byte budget.
These format choices do not imply that every image can fit that budget.

For grayscale, sample value 0 means black and the maximum sample value means
white. For generated gray4 output, choose 2-bit encoding only when all final
pixels are black or white, using samples 0/3. If any pixel is either intermediate
gray, use 4-bit encoding with samples 0/5/10/15. This includes images using three
or four grays; do not use the number of colors or a restricted image region to
keep such output at 2 bits. All-black and all-white images qualify as
black/white-only. Choose the depth after quantization, and again after any
overlay changes; an overlay introducing intermediate gray requires 4-bit output.
For gray16, retain fixed 4-bit output even for black/white content.

Do not emit a PLTE chunk for type 0. Automatic 1-bit output is not part of this
policy.
PNG itself permits type 0 with 1-bit depth, but that is not the selected
controller-generated encoding. Compatible renderer output still retains its
original bytes under the fast-path rules above. See the
[PNG color-type and palette specification](https://www.w3.org/TR/png-3/#11IHDR).

For Spectra 6, emit exactly six opaque PLTE entries in the order listed below:
black, white, yellow, red, blue and green. Use those exact reference RGB values
as the transmission palette as well as the Lab matching references. Pixels
must index one of those six entries; unused 4-bit values must not appear.
No separate pure-RGB transmission palette is required for these profiles.

These requirements define generated output, not a requirement to re-encode all
renderer output. Preserve the agreed byte-preserving fast paths for images
that already meet the target's compatibility and capacity constraints. In
particular, a compatible indexed grayscale PNG can still pass through without
being rewritten to type 0. Use the exact palette-membership rules above;
permitted indexed fast-path bit depths are defined by the agreed table above.

## Screen processing policy

Place resizing and dithering choices on `ArprtScreen`, so different content can
choose different presentation policies on the same device. Device overrides
describe the output target; Screen settings describe how to prepare content for
that target. A separate Device-level dithering override is not required.

The agreed API direction is `spec.imageProcessing`, with the following settings;
these fields are now connected to GET, POST and Job normalization for OG:

| Setting | Values | Default |
| --- | --- | --- |
| `resizeMode` | `contain`, `stretch` | `contain` |
| `dithering` | `atkinson`, `floyd-steinberg`, `bayer`, `none` | `atkinson` |

`contain` preserves aspect ratio, fits the entire image inside the target and
fills remaining space with white. `stretch` scales directly to the target width
and height without preserving aspect ratio. Both allow enlargement and
reduction; a dimension mismatch alone must not fail rendering. Cropping is not
part of the selected modes.

Center the contained image horizontally and vertically. If the remaining
padding on an axis is odd, place the extra pixel on the right or bottom.
Image alignment is fixed rather than exposed as another resource setting.

Use bilinear interpolation for both enlargement and reduction. A configurable
resampling filter or a more elaborate filter is not required.

The investigation fixes this as classic point-sampled, four-neighbor bilinear
with half-pixel coordinates for both directions. Reduction does not widen the
tent kernel or add an antialiasing filter. This distinction matters when
evaluating `BiLinear` and `ApproxBiLinear` from `golang.org/x/image/draw`;
[DESIGN](DESIGN.md#numerical-processing-semantics) specifies the coordinates,
composition, rounding and numerical reference expectations. Use a bounded
internal implementation so resizing can stop during CPU work.

`none` still performs required resizing, nearest-palette-color quantization and
encoding conversion; it only disables dithering.

Use the algorithms described in
[Tanner Helland's dithering reference](https://tannerhelland.com/2012/12/28/dithering-eleven-algorithms-source-code.html):

- Floyd-Steinberg uses error weights 7/16 to the right, then 3/16, 5/16 and 1/16
  to the next row's left, center and right pixels.
- Atkinson distributes 1/8 of the error to each of six neighbors: the next two
  pixels on the current row, left/center/right on the next row, and center two
  rows below. It distributes 6/8 in total; do not renormalize that to the full
  error. Loss of some highlight/shadow detail is an accepted characteristic of
  the selected default.
- Bayer uses a fixed 8 x 8 ordered threshold matrix. Matrix size is not a
  resource setting.
- Floyd-Steinberg and Atkinson propagate the L*, a* and b* error components
  in CIE Lab, rather than RGB-channel errors.

Fix and document scan order, arithmetic, boundary handling and rounding so the
same inputs and effective policy produce deterministic output. Additional
algorithms are not required.

### Color matching and diffusion space

Use CIE Lab internally for palette matching and dithering. This choice is fixed,
not exposed as a Resource setting. Convert both the input colors and target
palette reference colors from sRGB through linear-light RGB and XYZ to Lab,
using the sRGB D65 reference white for both. D65 is fixed rather than exposed as
a Resource setting; do not adapt these working values to D50. Use consistent
standard conversion constants throughout the pipeline. For gray4/gray16,
compare against the target's neutral gray entries rather than using a separate
weighted-sRGB grayscale approximation.

Choose the nearest palette color by Euclidean distance in Lab (Delta E 1976).
Floyd-Steinberg and Atkinson calculate the difference from the selected palette
color in Lab and distribute its L*, a* and b* components using their respective
weights. Keep working values and accumulated errors at sufficient precision;
the Lab coordinates are not 8-bit RGB channels.

Preserve temporary out-of-range Lab working values and accumulated errors in
floating point; do not clamp them before nearest-palette selection. Each output
pixel must still select an actual target palette entry. Discard error weights
whose destination lies outside the image, without renormalizing or redistributing
them among the remaining in-bounds neighbors.

All quantized output must select an existing target palette entry. Encode its
corresponding grayscale sample or the index of its exact reference RGB value
using the generated-output formats above, rather than round-tripping the
selected Lab value through RGB and creating off-palette colors due to rounding.
Compatible input still retains the existing fast paths.

Do not add image-brightness gamma adjustment or a user-selectable gamma control.
The standard sRGB transfer-function decoding required for sRGB-to-Lab conversion
is part of correct color-space conversion and must still be performed. See the
[W3C conversion reference](https://www.w3.org/TR/css-color-4/#color-conversion-code)
and [Lab color-distance definition](https://www.w3.org/TR/css-color-4/#color-difference).

Bayer uses two-color allocation for arbitrary target palettes. For each input
color, determine two palette colors and their allocation ratio by approximating
the input in Lab. Use the fixed 8 x 8 Bayer threshold at the output pixel's
coordinates to select one of the two colors according to that ratio. For a
uniform region with a 25%/75% allocation, each complete 8 x 8 tile contains 16
pixels of the first color and 48 of the second. Endpoint ratios select a single
palette color. Do not diffuse errors to neighboring pixels or depend on their
image content when choosing the pattern.

This adapts the two-color allocation principle described in
[Yliluoma's arbitrary-palette ordered dithering](https://bisqwit.iki.fi/story/howto/dither/jy/)
to the agreed Lab working space; it does not adopt that implementation's RGB
matching or additional algorithms. Fix palette-pair selection, ratio rounding,
matrix orientation and tie-breaking as deterministic implementation rules.
With `none`, use nearest-color Lab matching without dithering.

## Spectra 6 reference palette

Use the six reference colors from
[EPF imaging.py, fixed revision](https://github.com/jwchen119/EPF/blob/65a5f83f21be15ef4525f7ae3069a58b7187e4d2/epf/imaging.py#L21-L30)
as the sRGB reference values, converted to Lab for Spectra 6 color matching and
quantization-error calculation:

| Color | Reference RGB |
| --- | --- |
| Black | `(0, 0, 0)` |
| White | `(255, 255, 255)` |
| Yellow | `(255, 243, 56)` |
| Red | `(191, 0, 0)` |
| Blue | `(100, 64, 255)` |
| Green | `(67, 138, 28)` |

The source calls these measured panel colors. Treat them as the selected
reference values, not calibration evidence for every E1002 or other Spectra 6
panel. Do not import the rest of EPF's imaging pipeline as a requirement.

The same reference RGB values are the agreed PNG transmission palette. The
firmware performs its own color matching, so a reference color must select its
intended panel color as well as represent the appearance used for quantization.
Reproducing the reviewed upstream and Seeed E1002/E1004 RGB333 lookup and
nearest-color calculation maps all six reference values to their corresponding
panel colors. This is source-level evidence, not a physical test. Validate
visible colors on available hardware before claiming physical support; EPF's
separate pure-RGB definitions are not the selected transmission palette.

## Persistence, failure behavior and compatibility

Validate the renderer input and normalize it before publishing a successful base
artifact. That base is the device-compatible image before controller overlays;
retaining the unprocessed source image is not a requirement. Conversion and
overlay work must not change the base image's recorded render time.

Preserve UID ownership, ordered publication, GET sharing, POST/Job device
ownership, exact-target stale fallback and issued-image retention. Effective
target and processing policy must participate in cache/revision compatibility,
including a conversion/palette version when it changes output semantics. A
policy change must not reuse a fresh image produced with a different policy.
Already-issued URLs must continue to return their original immutable bytes.

Keep input byte/pixel/work limits separate from device output limits. General
RGB input may exceed the existing OG compressed-output budget before reduction.
Resolve each device's output budget from the agreed model default and any
explicit Device override. Do not silently switch dithering methods or discard
requested content merely to meet a byte limit.

Check the effective device's encoded-byte budget on every output path, including
byte-preserving renderer output, normalized images and overlay composites. Check
before publishing a successful base and again before issuing an overlay result.
Matching dimensions, palette and encoding do not imply that a PNG fits the
device's download/decoder budget. Enforce the resolved exclusive `pngByteLimit`
exactly; do not rely on firmware download/decode failure to detect oversized
output. Renderer input remains subject to the separate input limits, rather than
the device output budget, until output compatibility is evaluated.

If normalization still produces oversized output, treat it as an image-processing
failure and preserve the previous compatible success or pending response. An
oversized optional overlay may fall back to the validated normalized base under
the existing overlay-failure rules. Do not publish or newly issue an oversized
result as a successful image. Apply the resolved budget without adding a
firmware-version gate.

Invalid, unsupported or unconvertible input must preserve the previous compatible
success. If none exists, retain the current pending response behavior. Conversion
must not weaken authorization or allow fallback from a different target. Optional
overlay failure may fall back to the validated normalized base as it does today.

## Acceptance requirements and remaining decisions

Implementation acceptance must cover:

- Model-profile resolution regardless of missing/unknown firmware version,
  explicit override precedence, palette-only, resolution-only and PNG
  byte-limit-only overrides, rejection of incomplete dimension pairs,
  contradictory explicit/reported dimensions, unknown models with sufficient
  overrides, and invalid targets.
- Unknown models with explicit palette and PNG byte limit plus either reported
  or explicit dimensions must resolve. Missing any required capability must
  leave the target unresolved without a renderer invocation; do not apply a
  guessed palette, global byte limit or another model's resolution. Cover
  reported-dimension absence and incomplete pairs, and known models resolving
  from defaults regardless of missing or unknown firmware version.
- Exclusive PNG byte-limit defaults of 90000 for OG and 750000 for all four
  reTerminal models, with explicit overrides taking precedence, including
  90000 for the reviewed Seeed common E1001/E1002/E1003 firmware. Missing or
  unknown firmware versions must not change those defaults or block rendering.
- Correct target delivery through GET, POST and Job without secret exposure.
- Byte-preserving gray4 type 0 1-bit and globally black/white 2-bit fast paths;
  2-bit input with any intermediate gray must instead become type 0 4-bit with
  exact sample mapping and no re-dithering, even when three or four values are
  used. Also cover compatible gray4 4-bit samples and gray16 type 0 1/2/4-bit
  passthrough. Other higher-depth type 0 and RGB black/white input still follow
  the conversion rules.
- Indexed fast-path palette matching with exact RGB membership, arbitrary
  entry order and palette subsets; an unused off-palette entry must still
  trigger conversion without pixel-usage classification.
- The indexed depth table: normalize 1-bit gray4/gray16 input; permit 2-bit
  gray4 passthrough only for compatible palettes with globally black/white
  pixels; permit compatible 4/8-bit gray4, 2/4/8-bit gray16 and all four indexed
  Spectra depths. Spectra subsets, including black/white and black/reference-red,
  must preserve bytes; nearby RGB values or unused off-palette entries require
  normalization. All other compatibility checks still apply.
- A compatible Spectra indexed PNG with a renderer-authored dithering pattern
  must retain its original bytes before overlays even when the Screen selects
  a different controller dithering algorithm. Renderer documentation must
  expose the canonical six RGB values, the complete passthrough conditions and
  an indexed output example.
- Indexed decoder cases at 1/2/4/8-bit depth, including E1003's missing 1-bit
  conversion, upstream/Seeed low-depth differences, and Spectra subsets with
  reordered entries. Include gray4 type 0 and indexed 2-bit cases with two
  intermediate grays and grays appearing only below row 430; decoder success
  must not be treated as evidence that the intended shades are preserved.
  The controller's decision must depend on global black/white membership,
  without mirroring the firmware's row cutoff or counting used values.
- All standard PNG color/depth combinations, transparency and Adam7 input;
  APNG default-image extraction with both shared-first-frame and separate-default
  layouts; static output without decoding the animation or forwarding its chunks.
- Fixed sRGB interpretation of RGB, grayscale and indexed input regardless of
  embedded ICC, gamma or chromaticity metadata, including APNG default images.
  Equivalent decoded samples with different valid color-space declarations
  must produce the same normalized pixels; compatible passthrough retains
  original bytes, while re-encoding does not copy ignored declarations. Keep
  standard sRGB-to-linear decoding for Lab and input size/CRC checks intact.
- Generated gray4 type 0 2-bit output for black/white-only pixels and 4-bit
  output whenever intermediate gray is present. Cover all-black/all-white
  images, three/four-gray images and overlays introducing intermediate gray.
  Gray16 type 0/4-bit and Spectra 6 type 3/4-bit keep their fixed depths.
  Verify exact grayscale samples, six-entry reference RGB palette in the
  specified order, opaque static
  noninterlaced encoding, and effective target dimensions. Include firmware
  color selection for all six reference colors and grayscale gradients.
- Bilinear enlargement/reduction with both resize modes, centered contain
  placement with odd padding on the right/bottom, white transparency composition
  and processing before palette-aware overlays.
- All four dithering settings, the Atkinson default, the fixed 8 x 8 Bayer
  matrix with Lab-based two-color allocation and expected tile proportions,
  deterministic output and no repeated dithering of compatible output.
- Lab palette matching with the fixed D65 reference white, Lab error diffusion
  for Floyd-Steinberg/Atkinson, standard sRGB transfer-function decoding without
  additional gamma adjustment, and exact palette RGB output without round-trip
  drift.
- Floating-point out-of-range Lab working values without intermediate clamping,
  and discarded image-edge diffusion weights without redistribution.
- Exact-boundary and over-limit input files/canvases, controller-wide conversion
  concurrency, processing cancellation and the 512 MiB default memory budget;
  device output one byte below, equal to and above the exclusive `pngByteLimit`
  on passthrough, normalization and overlay paths, failure fallback,
  policy/target cache invalidation, replay and immutable issued-image retention.
- Physical OG regression and E1002 acceptance for setup, authenticated image
  download, visible colors, repeated polling, cached-image reuse and sleep/wake.
  E1001, E1003 and E1004 must receive firmware review; explicitly record where
  physical acceptance remains unavailable.

The pre-implementation source review and reproducible decoder/resource probes
are recorded in [DESIGN](DESIGN.md#image-processing-design-before-integration)
and [image-investigation](../tools/image-investigation/README.md). They resolve
the model decoder/transport review, output allocation, arithmetic and migration
design questions. They do not establish physical acceptance or integrated
controller performance. In particular, Seeed common's modem image request lacks
the authenticated headers used by its Wi-Fi path, and native type 0 / 4-bit OG
acceptance must use firmware with the corrected expansion. Preserve these
distribution and physical-evidence distinctions during implementation.

Controller-generated PNG formats, the Spectra 6 transmission palette, model PNG
byte-limit defaults, the explicit override policy and the whole-image
black/white rule for gray4 depth selection, and indexed passthrough depths and
palette membership are settled above. An old-firmware workaround for the
separate 4-bit expansion bug is explicitly not required.
Unknown-model capability requirements and unresolved-target behavior are also
settled above, as is fixed sRGB input interpretation without embedded-profile
conversion.

These details must be resolved in the owning API/protocol/design contracts when
the feature is implemented. They do not change the agreed algorithms, default,
resource ownership or normalization behavior above.
