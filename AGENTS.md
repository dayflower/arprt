# Development guide for agents

arprt is a Go Kubernetes controller for TRMNL BYOS. Keep changes focused on the
requested behavior and preserve the contracts below.

## Read according to the change

- Build, package ownership, generation and testing: [notes/DEVELOP.md](notes/DEVELOP.md).
- Rendering, assignment, authentication, publication, recovery or GC:
  [notes/DESIGN.md](notes/DESIGN.md).
- BYOS routes/headers/responses, Render Context or PNG compatibility:
  [notes/PROTOCOL.md](notes/PROTOCOL.md).
- Installation and support limits: [README.md](README.md).
- Runtime configuration, credentials, monitoring and operator recovery:
  [notes/OPERATIONS.md](notes/OPERATIONS.md).
- Renderer selection, container requirements and example usage:
  [notes/RENDERERS.md](notes/RENDERERS.md).

Read the relevant sections and nearby code/tests; there is no need to load every
document for every task. Completed implementation plans and phase reports live
in Git history. Consult them only when investigating a specific historical
decision, and do not treat their old defaults or deployment state as current.

## Working rules

- Keep public CRDs focused on user intent. Artifact layout, cache keys, temporary
  Jobs and execution bookkeeping remain internal.
- Preserve UID ownership, exact-target compatibility, ordered publication and
  issued-image retention when changing delivery or storage.
- Keep credentials and MACs out of Render Context, logs, Events and metrics.
  Registered MACs have an intentional status/PVC representation.
- Keep private hostnames, registry addresses, real device identities and keys
  outside the repository; use synthetic values in examples and fixtures.
- Update the owning document when changing a contract. Keep examples, context
  fixtures and generated CRDs consistent with the implementation. Resolve any
  disagreement between documentation and code explicitly.
- Edit API types/markers in `api/v1alpha1/types.go`, then regenerate; do not
  hand-edit generated deepcopy or CRD files.
- Keep new design rationale beside the relevant code or in DESIGN. Avoid adding
  completed task logs or copying the same specification across documents.

## Checks

- `make verify` checks generation/dependencies/formatting, vet, race tests and build.
- API/CRD or reconciliation changes also need envtest on both CI versions:
  `make integration ENVTEST_K8S_VERSION=1.36.0` and
  `make integration ENVTEST_K8S_VERSION=1.36.2`.
- Use the relevant cluster smoke checks for Job/PVC/deployment behavior; see
  DEVELOP for coverage and disposable-cluster setup. These scripts use the
  selected kubeconfig context and change cluster resources.
- For documentation-only changes, check local links, anchors, obsolete references
  and `git diff --check`; software/cluster checks are unnecessary unless executable
  examples or contracts change.

Tool versions and exact target recipes are maintained in the Makefile, go.mod
and CI workflow. The verified support scope is recorded in README.
