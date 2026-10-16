# Examples

Choose an example by what you want to configure or run. Install the controller
and CRDs using the [Helm quickstart](../README.md#installation-and-pairing) first.
Examples remain outside the controller Helm release. Run commands from the
repository root; the guides use namespace `arprt` unless stated otherwise.

| Goal | Example | Prerequisites and instructions |
| --- | --- | --- |
| Configure Device identity, assignment, playlists or overlays | [Custom resources](custom-resources/README.md) | CRDs, controller, named credentials and the renderers referenced by the selected manifest |
| Run a minimal HTTP GET renderer | [HTTP gray bands](renderers/http/gray-bands/) | Python 3 or the supplied Deployment; [GET guide](../notes/RENDERERS.md#http-get-example) |
| Render current device observations through HTTP POST | [HTTP device status](renderers/http/device-status/) | Python 3 or the supplied Deployment; [POST guide](../notes/RENDERERS.md#http-post-example) |
| Render with an existing container image | [Job gray bands](renderers/job/gray-bands/) | Job scheduling and the controller's RWO PVC; [Job guide](../notes/RENDERERS.md#job-example-and-container-requirements) |
| Build a photo-rendering container | [Job Picsum](renderers/job/picsum/) | A built/pushed renderer image, Job/PVC support and HTTPS egress; [Picsum guide](../notes/RENDERERS.md#picsum-photo-job-example) |
| Expose BYOS through an existing TLS Gateway | [Envoy Gateway](networking/envoy-gateway/README.md) | Gateway API, an existing Envoy Gateway, DNS and certificate |

`custom-resources/` illustrates API configuration patterns. Each renderer example
keeps its implementation, image inputs, deployment resources and corresponding
Screen manifests together. `networking/` contains external routing integrations.
Detailed renderer contracts and execution instructions belong in
[RENDERERS](../notes/RENDERERS.md); configuration and credentials belong in
[OPERATIONS](../notes/OPERATIONS.md).

Select examples individually. Several files describe alternatives for the same
resource name, and some use placeholder endpoints or images. Keep adapted
manifests, real hostnames, identities, registry addresses and credentials in
private configuration outside the repository.
