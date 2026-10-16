# Envoy Gateway example

Install the controller with Helm following the [quickstart](../../README.md#installation-and-pairing).
Keep renderers, custom resources and Gateway resources outside the Helm release.
Copy [httproute.yaml](httproute.yaml) to private configuration and set:

- The hostname to match `byos.publicBaseURL`.
- The parent Gateway namespace/name and HTTPS listener to your existing Gateway.
- The backend Service to `<release>-arprt` (`home-arprt` for release `home`).

The Gateway must have a certificate for the hostname, allow HTTPRoutes from the
controller namespace and be reachable through DNS. This example preserves
`/arprt`, matching `byos.proxyStripsPrefix: false`. Preserve `ID` and
`Access-Token`; avoid redirects, response compression and credential logging.

```sh
kubectl -n arprt apply --validate=strict -f /path/to/private-httproute.yaml
```

For device `living-room`, use Custom Server URL
`https://byos.example.test/arprt/devices/arprt/living-room` without a trailing
slash. Setup stays enabled by default and reuses the registered device's saved
key. Set `byos.setupEnabled: false` with `helm upgrade` only when intentionally
disabling setup. The root URL has no web UI; inspect Kubernetes device status.
Keep real hostnames, identities and credentials outside this repository.
