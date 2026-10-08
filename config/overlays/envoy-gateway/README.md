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
`trmnl`. Register your device and create its auth Secret separately, following the
main README. Keep real MAC addresses, credentials and private environment values
outside the repository. The default Role permits only `living-room-auth` and `living-room-identity`.

For device `living-room`, the example Custom Server URL is:

```text
https://byos.example.test/trmnl/devices/trmnl/living-room
```

Pairing defaults to disabled. After registering the device and Secret,
temporarily enable setup on a trusted network. Your private pairing overlay
should use `envoy-gateway-pairing` as its base with the same private overrides:

```sh
kubectl apply --validate=strict -k /path/to/private-pairing-overlay
kubectl -n trmnl rollout status deployment/arprt
```

After pairing, apply the normal overlay again to disable setup:

```sh
kubectl apply --validate=strict -k /path/to/private-gateway-overlay
kubectl -n trmnl rollout status deployment/arprt
```

The root URL has no web UI. Inspect Kubernetes device status to confirm actual
firmware requests and successful display. The renderer defaults to grayscale;
adding `?encoding=indexed` to the `photo` screen URL exercises indexed 2-bit PNG.
