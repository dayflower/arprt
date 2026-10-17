# Image processing implementation plan

## Status and scope

This plan uses [IMAGE-PROCESSING](IMAGE-PROCESSING.md) as its requirements
baseline and compares it with the current code, [AGENTS](../AGENTS.md) and the
owning documents. It proposes implementation phases and PR boundaries; it does
not change runtime behavior or expand verified support.

The initial planning review was static. The Phase 0 investigation now has
[durable design decisions](DESIGN.md#image-processing-design-before-integration)
and [reproducible probes](../tools/image-investigation/README.md) covering pinned
firmware callbacks, the Go decoder, numerical processing, cancellation and
resource feasibility. Runtime integration and physical acceptance remain later
phases. The existing verified support baseline is unchanged.

arprt is independent and unofficial; it is not affiliated with or endorsed by
TRMNL. Keep source-reviewed compatibility separate from physical acceptance.

Use the sequence: technical investigation, image-processing foundations,
persistence and overlays, renderer integration, device profiles, then acceptance.
The proposed scope is eight PRs, including the initial investigation PR.

## Current implementation and requirement gaps

Most differences below are intentional extensions of the current contract, not
bugs against the existing support baseline. Until the corresponding implementation
lands, the current contracts remain authoritative.

| Area | Current implementation | Required change |
| --- | --- | --- |
| Target resolution | Only `og` / `1.8.17` resolves to 800 x 480 gray4 | Resolve reviewed model profiles and explicit overrides without a runtime firmware-version allowlist |
| Device API | No `spec.display` | Add optional width, height, palette and exclusive PNG byte-limit overrides; validate dimensions as a pair |
| Observations and effective values | `status.display` stores resolved dimensions and palette | Preserve reported dimensions separately from the effective target and record the source of effective values |
| PNG input | Noninterlaced 2-bit gray4, type 0 or type 3 | Accept standard legal PNG color/depth combinations, alpha, Adam7 and APNG default images within separate input limits |
| Indexed compatibility | Exactly four ascending gray4 PLTE entries | Apply the agreed depth table and exact palette membership to every declared entry, allowing subsets and arbitrary order |
| Conversion | No resizing or general quantization | Add white-background composition, bilinear resizing, Lab D65 matching and four dithering settings |
| Screen API | No `spec.imageProcessing` | Add contain/stretch and Atkinson/Floyd-Steinberg/Bayer/none, with contain and Atkinson defaults |
| Output limits | Less than 90,000 bytes is embedded in several layers | Separate input limits from resolved device output budgets and enforce exclusive limits on every output path |
| Generated images and overlays | Maximum 384,000 pixels and a 2-bit gray4 encoder | Support gray4 depth selection, gray16, Spectra 6 and other target dimensions |
| Persistence and replay | Target identity contains dimensions, palette and rotation; definition revisions already exist | Include output constraints and processing semantic versions in compatibility decisions and preserve old records |
| Controller memory | Default limit is 256 MiB | Raise it to 512 MiB and measure concurrent reception, conversion, overlays and waiting inputs together |

Relevant implementation locations:

- [protocol.go](../internal/byos/protocol.go),
  [status.go](../internal/byos/status.go) and
  [devicestate/context.go](../internal/devicestate/context.go): observations and
  target resolution.
- [types.go](../api/v1alpha1/types.go): public API and schema markers.
- [render/png.go](../internal/render/png.go) and
  [render/http.go](../internal/render/http.go): PNG validation, response limits,
  media types and HTTP deadlines.
- [jobrender/reconciler.go](../internal/jobrender/reconciler.go) and
  [jobrender/store.go](../internal/jobrender/store.go): fixed target eligibility
  and bounded output collection.
- [artifact/pvc.go](../internal/artifact/pvc.go),
  [derived.go](../internal/artifact/derived.go) and
  [corner.go](../internal/artifact/corner.go): fixed size/palette assumptions,
  persisted metadata and pending delivery records.
- [overlay.go](../internal/overlay/overlay.go) and
  [overlay/png.go](../internal/overlay/png.go): gray4 pixel handling and encoding.
- [screen/input.go](../internal/screen/input.go),
  [assignment/resolver.go](../internal/assignment/resolver.go) and
  [maintenance.go](../internal/maintenance/maintenance.go): revision, replay and
  retention compatibility.
- [controller values](../charts/arprt/values.yaml): controller resource defaults.

### Conflicts and details requiring resolution

1. **The current physical baseline does not validate the new gray4 encoding.**
   The requirements deliberately select type 0 / 4-bit for intermediate gray
   and do not require a workaround for the old firmware expansion bug. Current
   verified support is OG / 1.8.17. Record the corrected-expansion assumption
   and the firmware actually used for new physical acceptance separately. Do
   not infer acceptance from the previous baseline or add a runtime version gate.
2. **Existing accepted images may require conversion under the new contract.**
   Existing 2-bit fixtures and examples with intermediate grays become conversion
   inputs. Tests and smoke scripts that expect identical renderer and delivered
   bytes must distinguish passthrough from normalization. Keep old images as
   migration fixtures where useful.
3. **OVERLAYS also owns a contract that must change.**
   Its current image guarantees prohibit depth changes and require 2-bit gray4.
   Update [OVERLAYS](OVERLAYS.md#image-and-artifact-guarantees), in addition to the
   documents listed in the requirements' integration table.
4. **Output allocation has an independent bound.**
   Phase 0 selects at most 4096 per output axis and 4194304 output pixels;
   the requirements proposal records this separately from input limits.
5. **Deterministic arithmetic is specified in DESIGN.**
   Use the recorded half-pixel four-neighbor interpolation, composition,
   scan order, Lab constants, palette ties and discrete Bayer allocation.
6. **Firmware host evidence is reproducible.**
   The investigation tools preserve source hashes, original extracted callbacks,
   pinned PNGdec/inflate dependencies and full-resolution buffer expectations.

## Phase 0: investigations before implementation

PR 0 must produce reviewable findings and decisions for the following items.

| Investigation | Questions and expected result |
| --- | --- |
| Model-specific firmware | Review OG and E1001-E1004 model header values, dimensions/orientation, decoder configuration, depth paths, setup, authenticated image transport, caching and pending behavior at pinned revisions |
| Distribution differences | Compare upstream, v1.8.17, Seeed common and dedicated E1004 sources; preserve the agreed model byte-limit defaults and explicit override policy |
| Decoder reproduction | Reproduce indexed low-depth differences, intermediate grays below the firmware counting region, reordered palettes/subsets and all six Spectra reference colors; configure actual full-resolution row buffers |
| PNG/APNG decoding | Check the project's pinned Go decoder against legal formats, CRC/structure, trailing data, decompressed length, both APNG layouts and ignored color-space metadata |
| Cancellation | Demonstrate how decode, resize and encode stop, including bounded cancellation latency; returning while CPU work continues is insufficient |
| Performance and memory | Measure maximum RGBA16, Adam7, high-entropy inputs and all model dimensions, including HTTP concurrency, Job collection, overlays and queued input buffers |
| API and migration | Decide observation/effective-target status shape, value provenance, safe output allocation limits, internal target representation and old record/active attempt migration |
| Deterministic processing | Specify interpolation, transparency composition arithmetic, Lab conversion, diffusion order and Bayer allocation details with reference expectations |

Resolve E1003's source-reported 1872 x 1404 orientation against the hardware
table's portrait ordering. Preserve explicit dimensions, then a valid reported
pair, then reviewed model defaults; do not select a model default by copying the
hardware table without firmware review.

For APNG, always decode IDAT's default image, whether it is shared with the first
animation frame or separate. Bound and inspect the complete file without decoding
later frames. Determine the required animation-chunk structural checks separately
from static-image decoding. Use the [PNG specification](https://www.w3.org/TR/png-3/#structure)
as the structural reference.

Evaluate decoder cancellation against the pinned toolchain, not only the current
online API. For resizing, distinguish `BiLinear` from `ApproxBiLinear` when
evaluating [golang.org/x/image/draw](https://pkg.go.dev/golang.org/x/image/draw).
Choose a dependency or bounded implementation only after fixing the required
downscaling semantics and cancellation behavior.

The investigation exit criteria are reproducible decoder evidence, an explicit
API/persistence design and a processing approach that can satisfy the agreed
10-second processing bound and 512 MiB controller budget. Any necessary change
to an agreed requirement must be made explicitly in the requirements proposal.

## Phases, PR boundaries and dependencies

PRs 1-4 establish internal foundations. Do not expose public settings whose
behavior is not connected. Preserve current behavior until the relevant
integration PR, and update owning contracts as each behavior becomes active.

| Phase / PR | Scope and main change targets | Dependencies and acceptance |
| --- | --- | --- |
| 0 / PR 0: investigation and design | Complete the investigations above; record durable rationale and pinned evidence in DESIGN or beside its owning code; update the proposal for explicit requirement changes | Before implementation; review decoder evidence, arithmetic rules, cancellation, memory feasibility and migration design |
| 1 / PR 1: shared PNG foundations | Separate input validation/decode, output compatibility and encoding; introduce shared palettes and 2/4-bit encoders in the transport-independent image code; expand PNG fixtures | PR 0; legal formats, APNG, CRC/structure, dimension limits, passthrough depth table, complete PLTE checks and whole-image gray4 classification |
| 2 / PR 2: normalization engine | Implement white composition, bilinear resize, Lab matching, Atkinson/Floyd-Steinberg/Bayer/none, shared processing admission and cancellation | PR 1; numerical references, enlargement/reduction, odd padding, edge errors, Bayer proportions, determinism, memory and stopping behavior |
| 2 / PR 3: persistence and compatibility foundations | Remove fixed OG storage limits; represent output constraints and semantic versions; read legacy latest/derived/corner/scheduler records; preserve issuance and GC roots in `internal/artifact` and scheduler storage | PR 1; old-PVC startup, boundary sizes, corruption preservation, ordered publication, active attempt recovery and immutable issued URLs |
| 3 / PR 4: palette-aware overlays | Replace the private gray4 encoder with shared encoding; preserve colors outside overlays for all palettes/dimensions; recalculate final gray4 depth and enforce budget; update derived appearance/encoder identity | PRs 1 and 3; palettes, rotations, corners, margins, render timestamps, oversized-overlay fallback, replay and restart |
| 4 / PR 5: renderer integration and Screen API | Connect GET/POST/Job to common processing; add `spec.imageProcessing`, normalized defaults/revisions, APNG MIME support and 32 MiB Job input collection; share processing capacity across sources; set controller memory to 512 MiB | PRs 2, 3 and 4; first complete all paths for the current OG target, including API/envtest, failures, concurrency, Job recovery and fallback |
| 5 / PR 6: Device API and profiles | Add `spec.display`, reviewed profiles and separate observations/effective status; use one resolution contract in BYOS, Device reconciliation, Job scheduling, publication/issuance checks, replay and maintenance | PR 5; override combinations, unknown models, absent capabilities, unknown/missing firmware, conflicting dimensions and obsolete in-flight work |
| 6 / PR 7: acceptance and documentation integration | Expand smoke suites, benchmark in deployment conditions, perform OG regression and E1002 physical acceptance, publish support/evidence distinctions and complete owning-document integration | PR 6; all software/cluster checks, resource evidence and physical results; remove the proposal only after its requirements and rationale are integrated |

Dependency edges:

```text
PR 0 -> PR 1
PR 1 -> PR 2
PR 1 -> PR 3
PR 1 + PR 3 -> PR 4
PR 2 + PR 3 + PR 4 -> PR 5
PR 5 -> PR 6 -> PR 7
```

PRs 2 and 3 can proceed independently once PR 1's image and target contracts are
stable. PR 5 must not connect only HTTP and leave Job collection under the old
input contract. PR 6 must not enable new targets before storage and overlays can
handle their output.

## Cross-cutting design constraints

### Public API and target resolution

- Keep renderer-facing width, height, semantic palette and clockwise rotation
  distinct from internal output constraints and value provenance. Do not expand
  Render Context solely to carry storage or execution bookkeeping.
- Device palette precedence is explicit value, then profile default. Dimensions
  are resolved as a complete pair: explicit, valid reported, then profile default.
  PNG byte limit is explicit, then profile default.
- Known models must resolve without a known firmware version. Unknown models
  require explicit palette and PNG byte limit plus a valid explicit or reported
  dimension pair. An unresolved target must invoke no renderer.
- Use exclusive defaults of 90,000 for OG and 750,000 for E1001-E1004. Document the
  explicit 90,000 override for reviewed Seeed common E1001/E1002/E1003 firmware;
  do not infer the distribution from firmware version.
- Preserve actual model, firmware and raw dimensions. Recompute effective values
  after spec edits, including in Job scheduling and final direct-read gates.
- Decide how existing recognized `bw`/`bwry` context/status values remain readable
  when introducing `spectra6`; their current enum presence is not support evidence.
- Edit API types/markers and regenerate deepcopy, canonical CRDs and chart copies.
  Do not hand-edit generated files.

### Image processing and resource control

- Keep validation, compatibility and conversion separate. Reuse decoded pixels
  for gray4 classification rather than decoding solely for that decision.
- Preserve compatible input bytes and renderer-authored dithering. Inspect every
  PLTE entry, including unused entries. Do not classify used colors to excuse an
  off-palette entry.
- Gray4 type 0/indexed 2-bit intermediate grays require exact 4-bit re-encoding
  without re-dithering compatible colors. Generated gray4 uses 2-bit only for
  globally black/white final pixels, otherwise 4-bit; gray16 and Spectra use
  their fixed generated depths.
- Add the required English rationale and pinned firmware link beside the gray4
  depth decision. The controller checks every row; 431 rows is firmware evidence,
  not a controller constant.
- Interpret decoded samples as sRGB, ignore embedded profile/gamma/chromaticity
  transformations, and omit ignored declarations when re-encoding. Preserve
  CRC and complete-file limits. Do not add brightness gamma adjustment.
- Resize before quantization and draw overlays after dithering. Renderer rotation
  remains responsible for content orientation; do not rotate content twice.
- Preserve floating-point Lab errors without intermediate clamping, discard
  out-of-bounds diffusion weights and emit exact palette samples/indices.
- Enforce the agreed 32 MiB input, 16,777,216-pixel canvas and 16,384-per-axis input
  limits before allocation. Processing is limited to one controller-wide
  conversion and 10 seconds, further bounded by enclosing contexts.
- Account for buffered input awaiting processing. Reading eight 32 MiB responses
  before acquiring one conversion slot does not establish a 512 MiB memory bound.
  Include Job collection and concurrent overlay work in the admission design.
- Resize before Lab work and use row-based error buffers. Hold processing capacity
  until work has actually stopped. Preserve independent cancellation for callers
  joining shared GET work and manager cancellation for that work's lifetime.
- Change the controller memory limit, not the renderer Job's independent default
  resources. Measure under the chart's controller CPU limit as well.

### Publication, replay and migration

- Preserve GET sharing and POST/Job device ownership, UID checks, ordered attempts,
  atomic publication, current assignment and authentication barriers.
- Equivalent effective capabilities must not acquire different GET identities
  merely because model, firmware, telemetry or value provenance differ. Include
  effective output constraints in compatibility decisions.
- Processing policy and semantic versions must invalidate fresh reuse and cycle
  replay. Define stale behavior consistently with existing prior-definition
  fallback: only output compatible with the current exact target may be selected.
- Validate the normalized base before successful publication and the final
  candidate before issuance. Enforce `encodedBytes < pngByteLimit` on passthrough,
  normalized output and overlays. Never change dithering or remove content to fit.
- Oversized or invalid normalization preserves a compatible prior success or
  pending. Optional overlay failure may use the validated base; issuance and
  required delivery-state failures retain their existing failure behavior.
- Keep previously issued URLs bound to their original immutable bytes. Retrieval
  must not normalize an old blob or rewrite it under a new policy.
- Read old records explicitly rather than treating all missing new fields as
  corruption. Preserve old issuance even when an old image no longer qualifies
  for new delivery. Prevent key changes from making protected data invisible to GC.
- Cover scheduler attempts already prepared or running during upgrade, pending
  corner proposals and persisted screen-cycle targets. Keep corrupt authoritative
  retention/ownership records fail closed; do not invent ownership from blobs.
- Preserve Job completion/exit checks, context integrity and safe bounded file
  access. Display requests continue to read completed Job output without creating
  or waiting for Jobs.
- Capture the base render timestamp once and preserve it through normalization,
  caching and overlays. Retain Job completion time semantics.

## Verification matrix

| Layer | Required coverage |
| --- | --- |
| PNG and compatibility | All legal color/depth combinations, transparency, Adam7, both APNG layouts, metadata equivalence, malformed/truncated files, exact palette membership, reordered/subset palettes and each passthrough depth |
| Gray4 firmware regression | All-black/all-white, two intermediate grays, three/four shades, grays only below row 430 or on the final row, exact 2-to-4-bit mapping and unchanged compatible renderer patterns |
| Generated output | Correct type/depth/samples, gray16 fixed depth, exact six-entry Spectra palette/order, opacity, static noninterlaced encoding, physical dimensions and final overlay depth |
| Numerical processing | D65 Lab references, standard sRGB transfer, no extra gamma, nearest-color ties, no Lab clamping, edge error discard, Atkinson 6/8 weights and Bayer tile proportions |
| Layout | Contain/stretch enlargement and reduction, bilinear reference pixels, white transparency composition, centered placement and odd padding on the right/bottom |
| Resource bounds | Input byte/dimension/area boundaries, arithmetic overflow, waiting and active cancellation, slot retention until stopped, one global conversion, peak memory and deadline behavior under deployment limits |
| Output budgets | Limit minus one, equal to limit and limit plus one for passthrough, normalization and overlays; defaults/overrides, fallback and no oversized publication or new issuance |
| API and resolution | Paired dimension admission, partial overrides, all profiles, absent/unknown firmware, unknown models, missing capability with zero renderer calls and observed/explicit contradictions |
| Persistence and delivery | Fresh/stale/replay distinctions, target/policy changes in flight, GET sharing, POST ordering, obsolete Job output, old-PVC/active-attempt restart, pending corner recovery, corruption, capacity, GC and issued-byte retention |
| Physical acceptance | OG regression; E1002 setup, authenticated download, visible colors, repeated polls, filename cache reuse and sleep/wake; record unavailable physical acceptance for E1001/E1003/E1004 |

Follow [DEVELOP](DEVELOP.md#selecting-checks) and the actual
[Makefile](../Makefile) recipes:

- Run focused behavioral tests, then `make verify` for implementation changes.
- Run `make integration ENVTEST_K8S_VERSION=1.36.0` for API and reconciliation
  changes, including strict field validation and manager restart behavior.
- Keep CRD chart generation and package checks in the matrix. Use
  `make crd-chart-check` and `make chart-check` as applicable.
- Build the controller image and run
  `make disposable-cluster-smoke IMAGE=arprt:dev LOAD_IMAGE=true` for integration.
  Extend HTTP, Job, playlist and operations suites and their image inspection
  helpers instead of keeping old 2-bit/800 x 480 assumptions.
- Validate both linux/amd64 and linux/arm64. Measure resources under the actual
  controller limits; software correctness tests do not establish performance.
- Follow [physical-device regression](DEVELOP.md#physical-device-regression),
  preparing each observation session in advance and distinguishing user visual
  confirmation from controller metadata or authenticated fetches. Keep real
  identities, keys and private environments out of repository artifacts.

Cluster scripts mutate the selected cluster; use the documented disposable
cluster setup. Physical acceptance results cannot be inferred from CI.

## Documentation, examples and completion

Update contracts in the PR that activates their behavior:

| Owner | Required updates |
| --- | --- |
| [PROTOCOL](PROTOCOL.md) | Resolution and renderer contracts, accepted input, fixed sRGB interpretation, passthrough table, exact palettes and generated formats |
| [DESIGN](DESIGN.md) | Pipeline, deterministic rationale, firmware evidence, admission, target/cache compatibility, migration and publication |
| [RENDERERS](RENDERERS.md) | General PNG input guidance and an indexed Spectra example with all six RGB tuples, allowed depths, subsets/order, unused-entry checks and renderer dithering preservation |
| [OVERLAYS](OVERLAYS.md) | Palette-aware composition, generated depth rules, preserved pixels and budget fallback |
| [OPERATIONS](OPERATIONS.md), [README](../README.md) and [chart README](../charts/arprt/README.md) | 512 MiB default, input/output/processing limits, override operations, recovery limits and separate source-reviewed/physical support claims |
| [DEVELOP](DEVELOP.md) | Package responsibilities, reusable firmware checks, expanded smoke/performance checks and physical acceptance procedure |
| [Context fixtures](../testdata/render-context/README.md), [PNG fixtures](../testdata/png/README.md) and [examples](../examples/README.md) | New targets and input/output cases, updated expectations and executable renderer guidance |

Do not automatically generalize every existing OG-specific renderer; either
extend it deliberately or document its remaining scope. Add explicit examples
for general RGB input and compatible indexed Spectra output.

Keep the requirements proposal versioned during implementation. Once acceptance
is complete and requirements/rationale have been integrated into their owners,
remove `IMAGE-PROCESSING.md`. Remove this plan when completed rather than retain
it as a task log; Git history preserves the implementation sequence.

Phase 0's findings and design are available for review. The next implementation
boundary is PR 1's shared PNG foundations; retain the recorded cancellation,
admission, migration and corrected-firmware assumptions in its design. Full
controller/native-architecture and physical acceptance remain PR 7 gates.
