# Automatic API keys and default-on setup

Implemented after [optional MAC registration](005-OPTIONAL-MAC.md). Setup now
generates missing API keys and remains enabled during normal operation. Device
routes and MAC eligibility still gate setup; repeated setup returns the current
saved key for the registered MAC without requiring an Access-Token.

## Credential provisioning

`spec.authSecretRef.name` remains required and selects a same-namespace Secret.
The operator declares that resource, without `data.apiKey` or `stringData.apiKey`
when using generated keys. The default base includes `living-room-auth` with
metadata and type only, making manual key preparation unnecessary.

During setup, `internal/credentials.Ensure` reads directly from the API. If
`apiKey` is absent, it generates 32 bytes using `crypto/rand`, encodes unpadded
URL-safe base64, and patches only that data entry with a resourceVersion
precondition. Other Secret data and metadata are preserved. Conflict retries
read fresh state and use the winner's key. Setup reads the saved value before
returning it, including after a lost patch response or controller restart.

A present key, including an empty or malformed value, is never replaced
automatically. Valid operator-managed keys, including immutable Secrets, work
without a write. Invalid keys return 401 and require an explicit correction.
Missing/unreadable Secret resources, denied writes, immutable empty Secrets and
other storage/API errors return 503. Keys are stored only in Kubernetes Secrets,
not device status, PVC artifact metadata, render context, messages or logs.

No Secret resource is created by the controller. Default RBAC adds only `patch`
for the named `living-room-auth` Secret. MAC Secrets remain read-only; no Secret
create, update, delete, list or watch permissions are added. Additional auth
Secret names require explicit `get` and `patch` rules in private overlays.
Kubernetes cannot restrict top-level `create` permission by resource name;
declaring keyless resources keeps runtime writes limited to named destinations.
See the [Kubernetes RBAC documentation](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#referring-to-resources).

## Setup and registration

The CLI default for `--enable-setup` changes from false to true. Normal
Deployment and Envoy Gateway overlays need no pairing rollout. The old pairing
overlay and explicit `--enable-setup=true` remain compatible; operators can
still use `--enable-setup=false` to disable setup explicitly.

Setup checks MAC configuration, persisted binding and namespace uniqueness
before attempting a credential write. It repeats those checks against a fresh
device snapshot when registering, persists MAC state on the PVC, and completes
the optimistic status patch before returning credentials. A final identity and
key read catches external Secret changes during registration. Direct, Secret
and learned MACs retain their previous rules and immutable source selection.

Credential preparation and MAC/status persistence are separate durable steps.
An API failure or a concurrent MAC-source change may leave a saved key without
completed registration. No setup success or key is returned in that case; a
retry reuses the saved key and completes eligible registration. A persisted MAC
cannot be replaced after a status-write failure. Credential write failure does
not register a MAC. These steps do not require an atomic multi-resource API
transaction.

Display, log and image routes use read-only authentication and never generate
keys. Existing explicit-MAC devices continue authenticated display without
re-pairing. Unprovisioned keyless Secrets report
`AuthenticationReady=False / AwaitingSetup`. Missing Secret resources and
malformed keys retain `InvalidCredentials` observation.

## GitOps and migration

Reapply the updated deployment/RBAC/base manifest and controller image; there
is no public schema change. Existing keys and registrations remain valid. For
automatic keys, omit the key field from GitOps desired state and do not place
that same field under another Secret controller's ownership. Server-side apply
of the metadata/type-only declaration preserves generated data. The cluster
smoke test also checks ordinary kubectl apply of that declaration.

Other devices need a declared auth Secret and matching named RBAC rules.
SealedSecret provisioning remains an operator concern when supplying a key.
Back up live auth Secrets alongside the identity/artifact PVC. To rotate, supply
a new valid key or explicitly remove the `apiKey` field and run setup again.
Deleting the Secret resource requires redeclaring it before setup can proceed.

## Validation

Tests exercise concurrent generation and reuse, preservation of operator keys,
extra Secret fields, malformed/empty keys, missing resources, denied patches,
obsolete devices, generated-key display/image authentication, all three MAC
sources with GET and POST, wrong tokens/MACs, duplicate registrations, setup
disabled explicitly, restart, incomplete key/status writes and privacy.

API integration additionally checks real resourceVersion conflicts, status
reconciliation, immutable Secret rejection/reuse and server-side GitOps apply.
The cluster smoke test covers default setup without flags, named read/patch
permissions, no Secret creation permission, persisted key reuse after rollout,
ordinary GitOps re-apply and all existing delivery/PVC checks.

Passed validation:

- `make verify`: generated files, module consistency, formatting, vet, race tests
  and controller build.
- `make integration ENVTEST_K8S_VERSION=1.36.0` and `1.36.2`.
- Docker controller image build.
- `python3 hack/cluster-smoke.py --image <cluster-accessible-image>:<tag>` on a
  disposable Kubernetes 1.36.4 kind cluster, including named Secret patch
  permissions and restart with setup still available by default.

The temporary test namespace, CRDs and cluster were removed. Existing live
deployments and physical devices were not changed. Examples use synthetic MACs
and reserved example URLs only.
