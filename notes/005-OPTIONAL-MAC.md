# Optional MAC sources and registration

Implemented 2026-10-08 after Phase 3. This change supports GitOps device
configuration without a literal MAC in the device manifest.

The credential provisioning and setup defaults recorded below describe this
change's original scope. They are superseded by
[automatic key generation and default-on setup](006-AUTOMATIC-KEYS.md).

## Public API and scope

A device may use `spec.hardwareID`, `spec.hardwareIDSecretRef` (`name`, `key`), or
omit both. The first two restrict setup and subsequent authenticated requests to
the configured canonical uppercase MAC. Omission registers the MAC from the
first successful setup request. Both fields cannot be specified together.
Source selection, literal MAC and Secret reference are immutable, including
addition/removal of optional fields. Changing identity requires a new device UID.

`authSecretRef` remains required and reads an operator-supplied `apiKey`.
`--enable-setup` remains false by default. Key generation, Secret writes, always-on
setup, expiring registration URLs and automatic creation of device resources
are outside this change. SealedSecret creation, encryption and distribution
remain the operator's responsibility.

## Responsibilities and persistence

- `internal/credentials` validates and reads the auth key. Registration consumes
  usable credentials through the protocol adapter; it does not depend on how a
  future credential provider might provision them. Ordinary authentication
  cannot implicitly create credentials.
- `internal/deviceidentity` resolves MAC sources, checks namespace uniqueness,
  serializes registration claims across devices, and patches only observed status.
- The artifact PVC stores immutable `identities/<hash-of-device-UID>.json` records
  independently of keys and render metadata. It fsyncs publication before the
  status patch or setup response. Repeated binding to the same MAC is idempotent;
  a different MAC cannot replace the existing record.
- `status.hardwareID` mirrors the durable binding. A cleared or altered status
  cannot override PVC state. Retry after API failure completes the same pending
  binding. Different resource UIDs cannot inherit identities or image issuance.
- Existing explicit-MAC devices can establish a binding during authenticated
  display, preserving operation without re-pairing. Unknown-MAC devices require
  successful setup before display can register them.

One active controller per namespace and the existing Recreate/POSIX PVC topology
remain required. The server and reconciler share the identity service in
production. Registration locks do not encompass rendering or unrelated display
processing. Deleted device UIDs no longer reserve MACs for live resources; old
internal records are retained with the existing retention policy.

## Failure behavior and observation

Missing/unreadable identity Secrets, missing keys and noncanonical values produce
`IdentityReady=False` / `IdentityUnavailable`; they never enable automatic learning.
A changed Secret MAC conflicts with a durable binding and reports
`HardwareIdentityChanged`. MAC values and key contents do not enter error
messages, logs or Render Context. Status contains the registered identity.

Unbound automatic devices report `IdentityReady=False` / `AwaitingSetup` and
`IdentityUnique=Unknown`. Empty identities do not collide. Unavailable external
Secret sources reserve no unknown MAC; existing durable bindings still reserve
known MACs even if their external configuration becomes unavailable.

Device status identity changes enqueue namespace duplicate checks. Other status
updates do not trigger unnecessary all-device reconciliation. Secret changes
are read directly on each request and on the existing one-minute reconciler
poll; no Secret informer or list/watch permission is added. Rendering rechecks
identity and the current auth key before image issuance, since external Secret
changes do not increment device generation.

Malformed/mismatched/unregistered request identities return HTTP/JSON 403.
Duplicate identities, unavailable identity sources/storage or incomplete API
status persistence return 503. Unusable auth keys or tokens retain the existing
401 behavior. Disabled setup remains 403 and performs no registration. The
anonymous bundled setup logo and normal display status codes retain their
existing contracts. No credentials are returned before durable registration and
its status patch succeed.

## Deployment and migration

`config/samples/device.yaml` now demonstrates automatic MAC registration.
`device-inline.yaml` retains the old explicit example; `device-secret.yaml`
selects `living-room-identity` / `mac`. These are alternatives for one device.
All sample MACs are synthetic. Default RBAC adds only named `get` permission for
`living-room-identity` alongside `living-room-auth`; no Secret writes are granted.
Add other Secret names explicitly to the operator's Role.

Install the regenerated CRD before deploying the updated controller. Existing
literal-MAC resources and auth keys remain valid. Do not remove a literal source
from an existing resource; choose a new source when recreating it. Back up PVC
identity/artifact state alongside Kubernetes resources. Restoring status alone
is not a substitute for retaining issuance and registration state.

## Validation

The tests cover direct, Secret and learned identities with GET and POST,
idempotent setup, legacy authenticated display, malformed/missing Secret data,
external Secret changes, duplicate sources, concurrent first setup, wrong tokens,
setup disabled, partial API failure, status loss, restart, resource replacement,
image issuance isolation and renderer-context privacy. Envtest exercises real
schema rejection, optional-field source immutability and running reconcilers
racing setup/status patches on Kubernetes 1.36.0 and 1.36.2.

Passed validation:

- `make verify`: generation consistency, module consistency, formatting, vet,
  race tests and controller build. For the intentionally changed generated API
  files, the generation check used a temporary Git index containing those files;
  the working index was left untouched.
- `make integration ENVTEST_K8S_VERSION=1.36.0` and `1.36.2`: real API validation
  and reconciliation, including absent/present source transitions.
- Docker controller image build.
- `python3 hack/cluster-smoke.py --image <cluster-accessible-image>:<tag>` on
  a disposable kind cluster running Kubernetes 1.36.4 with no pre-existing arprt
  CRDs: all three MAC sources, namespace duplicate rejection, changed Secret
  rejection, status recovery, persistent binding/issuance after restart, both
  proxy prefix modes, renderer outage and named read-only Secret permissions.

The temporary test cluster and its namespace/CRDs were removed. Existing
deployments were not modified. Examples use synthetic identities and reserved
example URLs; no home-specific configuration is recorded.
Physical-device setup for the new optional modes is separate from synthetic
and Kubernetes checks; no additional physical observation is claimed here.
