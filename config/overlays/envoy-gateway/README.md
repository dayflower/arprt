# Envoy Gateway deployment example

Create private overlays outside this repository using these examples as bases,
and override the following values there:

- `registry.example.test/arprt:dev`: your controller image; pin a release tag or digest.
- `https://byos.example.test/trmnl`: your public base URL, including its path prefix.
- HTTPRoute hostname `byos.example.test`: the hostname matching that URL.
- Parent Gateway `gateway-system/public`, listener `https`: your existing HTTPS Gateway.

The Gateway must have a certificate for the chosen hostname and allow HTTPRoutes
from namespace `trmnl`. Configure DNS to reach that Gateway. This example
preserves `/trmnl`; no path rewrite is used.

Install the CRDs first, then apply your private normal overlay. Replace the
`/path/to/private-*` paths below with your overlay directories:

```sh
kubectl apply --validate=strict -k config/crd
kubectl wait --for=condition=Established \
  crd/trmnlscreens.arprt.dayflower.github.io \
  crd/trmnldevices.arprt.dayflower.github.io --timeout=30s
kubectl apply --validate=strict -k /path/to/private-gateway-overlay
```

This installs the controller, PVC, minimal renderer and HTTPRoute in namespace
`trmnl`, including a keyless `living-room-auth` Secret. Register your device
following the main README; setup generates its key. Existing keys are reused.
Keep real MAC addresses, credentials and private environment values outside the
repository. The Role allows `get` on `living-room-auth` and `living-room-identity`,
and `patch` only on `living-room-auth`. Other Secret names require explicit rules.

For device `living-room`, the example Custom Server URL is:

```text
https://byos.example.test/trmnl/devices/trmnl/living-room
```

Setup stays enabled by default. No separate pairing rollout is required;
repeating setup for the registered MAC returns the stored key. The old
`envoy-gateway-pairing` base remains compatible but is redundant.
If needed, explicitly set `--enable-setup=false` in your private Deployment patch.

The root URL has no web UI. Inspect Kubernetes device status to confirm actual
firmware requests and successful display. The renderer defaults to grayscale;
adding `?encoding=indexed` to the `photo` screen URL exercises indexed 2-bit PNG.
