# TRMNL BYOS Kubernetes Controller Implementation Plan

## Purpose and baseline

Implement the architecture in [DESIGN.md](DESIGN.md) through independently verifiable phases. The repository now contains the project foundation, CRDs, build system, deployment manifests, completed Phase 2 HTTP GET rendering/BYOS delivery and physical-device acceptance, and Phase 3 device-aware synchronous HTTP POST. Phase 0 contracts and collection experiments are recorded below.

## Phase status

- **Phase 0 contracts and collection experiment completed (2026-10-07):** [decisions and contracts](001-PHASE0.md), [sanitized protocol fixtures](../testdata/phase0/README.md), and [documented PVC collection results](001-PHASE0.md#collection-experiment-results).
- Confirmed: Go + controller-runtime, one namespace, PVC, API group `arprt.dayflower.github.io`, TRMNL OG (2-bit) firmware 1.8.17.
- The PVC collection experiment was verified on Kubernetes `v1.36.2`. Phase 1 establishes the initial support floor as Kubernetes `v1.36.0` within minor 1.36, using API integration checks on `v1.36.0` / `v1.36.2` and the matching controller-runtime dependency baseline. Other minors remain unverified.
- **Phase 1 completed (2026-10-07):** [implementation and validation](002-PHASE1.md), Go/controller-runtime foundation, generated CRDs, Render Context, internal PVC repository, deployment and CI checks. Local-cluster installation, samples, probes, namespace RBAC and restart passed on `v1.36.2`.
- **Phase 2 completed (2026-10-07):** [implementation, validation and acceptance record](003-PHASE2.md). HTTP GET delivery, pairing/authentication, strict PNG validation, persistent artifacts, target reuse and stale fallback are available. Local-cluster setup/display/image fetching, both prefix modes, PVC restart and renderer outage passed on `v1.36.2`. The actual OG / 1.8.17 paired and displayed grayscale bands through a configured HTTPS Gateway with `/trmnl`; authenticated display continued after controller restart. On 2026-10-08, the user confirmed the remaining physical-device acceptance checks, including indexed output and outage/initial-pending behavior, were also completed on 2026-10-07.
- **Phase 3 completed (2026-10-08):** [implementation and validation](004-PHASE3.md). Synchronous POST receives current request telemetry in Render Context v1, uses pair-owned artifacts and ordered publication, and preserves compatible prior output on failure. Race tests, API integration on `v1.36.0` / `v1.36.2`, and standalone fixture/HTTP rendering passed. The Phase 3 controller and POST renderer were deployed on `v1.36.2`; actual-device request telemetry matched the persisted context digest and pair ownership, authenticated image fetching passed through the existing HTTPS Gateway, and the user confirmed the device-status screen was displayed. Job rendering and later assignment phases remain unavailable.

The first usable milestone is a controller that serves a single explicitly assigned HTTP GET screen to a real TRMNL device. Subsequent phases add device-aware HTTP rendering, asynchronous Job rendering, playlists, automatic discovery, and operational hardening.

This plan describes implementation work; it does not introduce additional public resources for artifacts, caches, or render executions. Phase completion depends on observable behavior and validation, rather than elapsed time.

## Architecture invariants

- Device MACs support literal configuration, same-namespace Secret references, or first-setup registration; see [optional MAC support](005-OPTIONAL-MAC.md). Named auth Secrets are declared by the operator; [setup generates absent keys and stays enabled by default](006-AUTOMATIC-KEYS.md).

- Public resources express user intent: `TrmnlScreen`, `TrmnlDevice`, and, when implemented, `TrmnlPlaylist`.
- Resources are namespaced. Reference scope and device URL mapping must be settled before API implementation.
- A device may specify at most one of `screenRef` and `playlistRef`. Leaving both unset is valid and ultimately enables automatic screen discovery.
- Device capabilities and telemetry are observed state in `status`, not desired configuration in `spec`.
- Render Target contains `width`, `height`, and a semantic `palette`; device model alone does not distinguish equivalent targets.
- HTTP GET receives only target query parameters. HTTP POST receives the same parameters and Render Context v1. Job rendering receives the same context schema as a JSON file.
- POST and Job artifacts belong to a screen/device pair. GET artifacts may be shared only for the same screen definition and render target.
- Renderer containers and HTTP services do not need Kubernetes API access.
- Job creation happens through reconciliation and scheduling. A device display request normally reads a completed artifact rather than starting a Job.
- Successful artifacts are published atomically. A failed attempt preserves the previous compatible successful artifact.
- Authentication credentials never enter device status, Render Context, public artifact metadata, or unredacted logs.
- Storage layout, execution bookkeeping, cache keys, and temporary Kubernetes resources remain internal implementation details.

## Decisions to settle before implementation

The following choices materially affect scaffolding and deployment. They were confirmed by the user and are recorded in [Phase 0](001-PHASE0.md):

| Decision | Impact |
| --- | --- |
| Implementation language and controller framework | Project layout, API generation, reconciliation patterns, build tooling, and tests |
| Single-namespace or multiple-namespace operation | Watch scope, RBAC, reference resolution, device URL format, and tenant isolation |
| Initial artifact backend: PVC or object storage | Artifact collection, deployment topology, persistence, recovery, and backend credentials |

Other details can be resolved within Phase 0 using protocol evidence and small implementation experiments. Keep those decisions internal or explicitly document any public API consequence before proceeding.

## Component boundaries

Establish these responsibilities early, while keeping the actual package structure idiomatic for the selected language:

| Component | Responsibility |
| --- | --- |
| API model and validation | CRD types, defaults, validation, status, and generated schemas |
| Protocol adapter | BYOS request parsing, responses, device routing, and image URLs |
| Device state service | Capability resolution, telemetry updates, status conflict handling, and context snapshots |
| Assignment resolver | Single-screen references, playlists, and automatic discovery |
| Rendering service | Shared input normalization, HTTP renderer invocation, and artifact validation |
| Job reconciler and scheduler | Required render pairs, due times, Job lifecycle, retries, and recovery |
| Artifact repository | Immutable image storage, latest-success pointers, metadata, retention, and collection |
| Runtime and deployment | Kubernetes client, controller lifecycle, configuration, RBAC, probes, and shutdown |

Keep protocol and Render Context models independent of Kubernetes client objects. HTTP and Job adapters converge on one internal artifact contract so the device-serving layer does not depend on renderer transport.

## Phase 0 — Resolve contracts and deployment constraints

**Goal:** Remove architectural uncertainty before scaffolding the controller.

### Work

1. Confirm the language/framework, namespace scope, and initial storage backend listed above.
2. Verify the BYOS contract against authoritative protocol/firmware sources and, where available, captured device traffic. Record supported firmware/device assumptions and create sanitized request/response fixtures.
   - Identify setup and display request headers, authentication behavior, response fields, refresh/sleep instructions, and telemetry encodings.
   - Establish which display capabilities are reported directly and which require a verified model/capability mapping.
   - Verify image encodings and palette requirements; do not assume every PNG is device-compatible.
   - Define protocol-compatible responses for an unknown device, missing assignment, unknown target, renderer failure, and an unavailable initial image.
3. Define canonical device routing, including namespace handling, external path prefixes, configured public base URL, and reverse-proxy behavior. Resource names alone must not become ambiguous across namespaces.
4. Specify initial authentication and credential handling based on the actual firmware contract. Define authorized image fetching and avoid blindly forwarding device credentials to renderers.
5. Settle public defaults and validation:
   - Exactly one HTTP or Job source; HTTP methods limited to GET and POST.
   - HTTP method default, URL validation, and query-parameter merge rules.
   - Meaning and default of `refreshInterval` for GET and Job rendering; decide whether it has any effect on synchronous POST rendering.
   - Behavior when a Job screen omits `refreshInterval`.
   - Render Context required/optional fields and representation of missing telemetry.
   - Job path defaults, absolute-path constraints, and writable-mount rules.
6. Design durable internal metadata: screen/device UIDs, input revision, render target, attempt identity, context digest, image digest, timestamps, and a latest-success pointer. Generation and target changes must prevent reuse of an obsolete cache entry.
7. Prototype the Job image-collection path for the chosen storage backend. Prove that an ordinary container can write one image file and exit without Kubernetes access or an upload SDK.
   - For PVC storage, establish supported access modes and scheduling constraints for the controller and renderer Pods. Do not assume a volume can be mounted concurrently on arbitrary nodes.
   - For object storage, specify a controller-managed collector or equivalent mechanism that works with existing renderer images. Collector credentials must not be required by the renderer.
   - Address publication only after renderer success, collection failures, restart recovery, partial files, and cleanup ownership.
8. Specify stale-image eligibility: preserve an older successful image only for the requested screen and a compatible current target. A replaced resource UID or changed target must not accidentally serve another rendering unit's image.
9. Record initial scope: one active controller replica, PNG output subject to firmware verification, no image conversion unless required by the verified contract, and no artifact CRD or user-configured cache API. Revisit these proposed limits if a verified protocol or deployment requirement conflicts with them.

### Deliverables

- Recorded architecture decisions, public field/default definitions, and sanitized protocol fixtures.
- A storage/collection proof of concept and deployment constraints.
- A minimum supported Kubernetes version and verified device compatibility assumptions.

### Exit criteria

- The project can be scaffolded without guessing the language, resource scope, storage topology, or firmware response contract.
- The collection experiment demonstrates the renderer contract from DESIGN.md, including configurable output paths.
- Unknown capability and first-image behavior are explicitly defined.

## Phase 1 — Project foundation, CRDs, and Render Context

**Depends on:** Phase 0.

**Goal:** Establish validated declarative inputs and a transport-independent data model.

### Work

1. Create the project, reproducible build/generation commands, controller entry point, container image build, and CI checks.
2. Implement `TrmnlScreen` and `TrmnlDevice` using `arprt.dayflower.github.io/v1alpha1`, the Phase 0 API group.
3. Generate structural CRD schemas with status subresources and schema/default validation. Use admission components only if required behavior cannot be expressed in the CRD schema.
4. Model HTTP and Job source fields from DESIGN.md, including renderer `image`, `command`, `args`, `env`, `envFrom`, and `resources`. Validate source exclusivity and unsafe path combinations.
5. Establish device status fields for display, model/firmware, battery, network, last-seen time, and conditions. Define condition reasons and `observedGeneration` conventions.
6. Define Render Target and Render Context v1 serialization, a context builder, and an explicit allowlist of exported device fields. Document additive versioning and unknown-field tolerance.
7. Introduce internal interfaces for assignment resolution, renderer execution, and artifact persistence. Implement the minimal selected backend needed by Phase 2; avoid speculative backend implementations.
8. Add minimal deployment manifests, namespace-scoped or cluster-scoped RBAC as decided, health/readiness probes, and graceful shutdown.

### Validation and exit criteria

- Generated CRDs install on a local Kubernetes cluster and valid sample resources are accepted.
- Invalid source combinations, conflicting device assignments, unsupported HTTP methods, and invalid paths are rejected at the intended boundary.
- Context serialization matches a shared fixture, preserves omitted optional values, and excludes credentials.
- Generation/build checks pass reproducibly. Unsupported later-phase behavior is reported explicitly rather than silently accepted as working.

## Phase 2 — First end-to-end delivery with HTTP GET

**Depends on:** Phase 1.

**Goal:** A real device displays an image from an explicitly assigned HTTP GET screen.

### Work

1. Implement verified BYOS setup/display endpoints, canonical device routing, and the agreed authentication behavior.
2. Parse and normalize capabilities and telemetry. Update device status safely under concurrent requests and reconciliation, without changing `spec`.
3. Resolve an explicit same-scope `screenRef`; reject missing references or unsupported sources with actionable conditions and protocol-compatible responses.
4. Derive a verified Render Target. When capabilities are unknown, follow the Phase 0 policy instead of inventing dimensions or palettes.
5. Implement GET rendering with generated `width`, `height`, and `palette` query parameters and `Accept: image/png`. Preserve allowed renderer URL parameters while handling target-key collisions consistently.
6. Apply bounded timeouts, response-size and decoded-image limits, HTTP status checks, content/format validation, and target-dimension validation. Treat redirects according to the agreed renderer access policy.
7. Store validated artifacts and serve immutable image URLs using the public base URL. Keep artifact files alive long enough for the later device image fetch.
8. Add target-based GET reuse and concurrent-request coalescing. Include screen identity and definition revision in freshness decisions; never forward identity or telemetry to GET renderers.
9. Implement refresh/freshness behavior from Phase 0. On failure, retain the last compatible successful image; when none exists, return the verified initial-image response.
10. Document pairing, a minimal renderer, and a single-screen example.

### Validation and exit criteria

- A local-cluster scenario covers setup, display, artifact URL fetching, controller restart, and a renderer outage.
- A real device completes the same path using a URL with a path prefix.
- Equal targets can reuse a GET artifact without leaking device context; unequal targets cannot share incompatible images.
- Failed or malformed responses never replace a valid stored image, and image URLs remain fetchable after the display response.

**Milestone A:** A usable single-screen HTTP GET controller with persistent artifacts and graceful failure behavior.

## Phase 3 — Device-aware synchronous HTTP POST

**Depends on:** Phase 2.

**Goal:** Device-aware renderers receive the latest request telemetry through the common context contract.

### Work

1. Add POST invocation with target query parameters, `Content-Type: application/json`, `Accept: image/png`, and the complete Render Context v1 body.
2. Build context from the current display request plus known device state. Rendering must not rely on a Kubernetes cache having already observed the latest status write.
3. Scope artifact ownership and reuse to screen/device identity. Apply the Phase 0 POST freshness policy without reusing another device's context-derived output.
4. Reuse bounded HTTP execution, image validation, atomic publication, image serving, and stale-success fallback from Phase 2.
5. Prevent a late response from an older render attempt from replacing a newer successful artifact or an artifact for a changed target.
6. Provide a device-status renderer example and document the different freshness semantics of GET, POST, and future Job rendering.

### Validation and exit criteria

- POST query targets exactly match the body target, and current request telemetry appears in context.
- Two devices with equal dimensions receive isolated POST artifacts.
- Concurrent requests, timeouts, invalid output, and status conflicts preserve correct ownership and the latest successful image.
- The same context fixture can be consumed outside Kubernetes.

## Phase 4 — Asynchronous Kubernetes Job rendering

**Depends on:** Phases 0–3.

**Goal:** Schedule rendering independently of device requests and recover completed artifacts durably.

### Work

1. Reconcile the required screen/device pairs for explicit assignments. Render only after a usable target is known.
2. Schedule initial rendering, interval refreshes, relevant screen-spec changes, target changes, and assignment changes. Ordinary telemetry updates update future snapshots without starting a Job on every device poll.
3. Persist or reconstruct due times and in-flight attempts after restart. Use bounded retries/backoff and one active attempt per pair; make reconcile retries idempotent.
4. Build Jobs with renderer-compatible container fields, context snapshots, writable output mounts, ownership labels, deadlines, resource limits, and the required security settings. Disable renderer ServiceAccount token mounting by default.
5. Implement default and custom `contextPath`/`imagePath` behavior. Handle differing parent directories and prevent mounts from hiding required image files without a clear documented constraint.
6. Implement the collection mechanism proven in Phase 0. Renderer success and valid collected output are both required before publishing success. Account for missing files, failed uploads, partial writes, and jobs that time out.
7. Validate outputs and publish artifacts through the shared repository. Reject obsolete completion results using resource UIDs, target/input revisions, and attempt ordering.
8. Watch Job lifecycle changes; retain the previous successful artifact on Job or collection failure. Report conditions/events without disclosing credentials.
9. Clean up Jobs, context snapshots, run directories, and orphaned artifacts under a bounded retention policy. Deletion, reassignment, and resource replacement must reconcile ownership safely.
10. Serve the latest completed successful artifact on `/api/display`. Display polling must not create a Job directly or wait for Job completion.
11. Document an existing-image example and the expected age of telemetry in a Job-rendered image.

### Validation and exit criteria

- A container with no Kubernetes dependencies can render using both default and custom paths.
- A short refresh interval produces successive artifacts while device polling stays independent of Job duration.
- Restart during execution or collection recovers without publishing a partial artifact or duplicating active work.
- Failing, hanging, obsolete, and missing-output Jobs preserve compatible prior output and expose actionable failure reasons.
- Namespace, volume-access, scheduling, and credential constraints are verified in the supported deployment topology.

**Milestone B:** Both renderer transports work with a shared artifact and context contract.

## Phase 5 — Playlists and automatic screen discovery

**Depends on:** Phase 4.

**Goal:** Complete the assignment behavior described in DESIGN.md.

### Work

1. Define playlist advancement semantics before implementing them: advancement trigger, ordering, retry behavior, state persistence, and missing/unready screen handling. Keep new public fields out of the API unless an explicit requirement demands them.
2. Implement `TrmnlPlaylist`, reference validation, ordered resolution, and device `playlistRef` support.
3. Implement deterministic automatic discovery when both device references are absent, scoped according to Phase 0. Define empty-set and screen-addition/deletion behavior.
4. Maintain rotation state per device and make it survive restart if the chosen advancement policy requires persistence. Avoid advancing twice for a retried display request when protocol identity allows detecting a retry.
5. Extend required Job render pairs to every eligible playlist/discovered Job screen, so images can be prepared before their turn. Bound fan-out and concurrent Job execution.
6. Define selection when the next screen has no valid artifact or its render fails. Preserve per-screen artifact ownership and do not silently present an image as belonging to another screen.
7. Document explicit ordering, discovery ordering, and unavailable-screen behavior.

### Validation and exit criteria

- Single-screen, playlist, and unassigned discovery paths all work with GET, POST, and Job sources.
- Ordering and advancement remain predictable through restarts, retries, edits, and missing references.
- Newly eligible Job pairs are scheduled, and removed pairs stop generating new work.
- Resource discovery and references respect the agreed namespace boundary.

**Milestone C:** The complete initial resource and assignment model is usable.

## Phase 6 — Operational hardening and initial release

**Depends on:** Phase 5.

**Goal:** Deliver a supportable initial release with verified recovery and bounded resource use.

### Work

1. Complete metrics, structured logs, events, and conditions for device last-seen state, rendering duration/failures, queue depth, artifact age, storage usage, and collection failures. Bound label cardinality and redact sensitive values.
2. Verify startup/restart recovery, graceful shutdown, storage corruption handling, and retention of images still referenced by device URLs. Define storage-capacity limits and garbage-collection safeguards.
3. Enforce concurrency limits, request deadlines, image limits, Job deadlines, retry limits, and status-update coalescing. Check the worst-case device × screen scheduling fan-out.
4. Review RBAC, Secret usage, renderer egress/redirect policy, image URL access, pod security, and namespace boundaries. Keep infrastructure credentials outside renderer context and user-visible metadata.
5. Finish installation/configuration docs, renderer contract docs, troubleshooting, supported-version statements, and example manifests. Add packaging only as needed for the agreed installation path.
6. Run the complete end-to-end matrix in a local cluster and perform a real-device acceptance pass before calling the release ready.
7. Keep the initial single-active-replica limit explicit. Multiple active serving replicas require shared metadata, consistent image availability, and status/scheduling coordination before they can be declared supported.

### Validation and exit criteria

- Installation from documented manifests and all documented examples work from a clean cluster.
- Recovery and fault scenarios preserve compatible successful artifacts without unbounded Jobs, temporary data, or storage growth.
- Device images remain retrievable through the configured proxy/base URL and authentication setup.
- The release clearly states supported devices, Kubernetes versions, backend topology, and limitations.

## Validation strategy

Build validation around externally observable contracts rather than tests that duplicate implementation details:

- **Pure contracts:** Render Context serialization, target normalization, URL/query generation, assignment ordering, input revision tracking, and stale-artifact eligibility.
- **API integration:** CRD defaults/validation, status conflict retries, reconciliation idempotency, reference changes, deletion, and replacement UIDs.
- **Protocol fixtures:** Setup/display parsing, response compatibility, prefix routing, missing-image behavior, and credential exclusion.
- **HTTP integration:** GET privacy, POST telemetry freshness, equal/unequal target reuse, concurrent attempts, slow services, malformed/oversized images, and retained successful output.
- **Cluster end-to-end:** Job scheduling, mounts, collection, cleanup, namespace scope, controller restart, and artifact retrieval using actual storage.
- **Physical device acceptance:** Pairing, setup, display, supported image palettes, polling behavior, telemetry, and outage recovery.

Use meaningful focused checks in each phase. Run the broader matrix at milestone boundaries and before release; CI checks should not require a physical device.

## Deferred work

The following items are outside the initial release unless Phase 0 identifies a concrete prerequisite:

- Artifact/render-instance CRDs or public cache/storage settings on screen/device resources.
- Target-only deduplication hints for Job renderers.
- Automatic device discovery/provisioning from unknown incoming identities.
- Additional rendering sources, renderer Kubernetes API integrations, and arbitrary user-supplied Pod specifications.
- Advanced playlist scheduling, weighted rotation, or per-screen timing fields.
- Multiple active controller replicas and multiple storage backends.
- General-purpose image conversion and support for unverified image formats/palettes.

## Execution rules

- Complete and review Phase 0 decisions before choosing implementation scaffolding.
- Implement a phase in small reviewable changes, preserving a working milestone after each vertical slice.
- Keep examples, generated CRDs, context fixtures, and renderer documentation consistent with implementation.
- Add behavior only when its preceding dependencies and validation are complete; do not claim deferred paths are ready.
- Record changes to public defaults or contracts before implementation so DESIGN.md and this plan remain consistent.
