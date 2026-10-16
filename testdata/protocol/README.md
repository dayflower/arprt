# BYOS protocol fixtures

These are synthetic fixtures for the initial controller contract, informed by
official TRMNL firmware `v1.8.17`, commit
`9acd348270a493ca5f1c627ac91a9469543267ae`; they are not device captures.
The `02:00:00:00:00:01` MAC, `.test` host, friendly ID and API key are fabricated.
The API key is a readable fixture marker, not production-strength key material.
The public URL prefix and namespace in these fixtures both use `arprt`.
See [the contract and source links](../../notes/PROTOCOL.md).

Firmware sources establish the request headers, response handling and image
fetch behavior. Error responses record controller design choices. These examples
document expected wire behavior rather than responses captured from a controller.

| Files | Purpose |
| --- | --- |
| `setup-request.http`, `display-request.http` | Parse identity, firmware, dimensions and telemetry; preserve the configured external path prefix |
| `setup-image-request.http`, `display-image-request.http` | Setup logo needs no auth headers; display image carries identity / token on the device URL prefix |
| `setup-response.json`, `display-response.json` | Serialize the agreed response fields, distinguish JSON status from HTTP status, and keep the image URL / filename consistent |
| `pending-response.json`, `error-responses.json` | Exercise no-image and authentication / identity failure behavior with the specified HTTP and JSON statuses |

- Setup / display response JSON files are bodies of **HTTP 200** responses with
  `Content-Type: application/json` and exact Content-Length. Setup uses JSON
  `status: 200`; successful display uses JSON `status: 0`.
- `pending-response.json` is also **HTTP 200**. JSON `status: 202` triggers
  firmware fast-poll behavior; it is not an HTTP 202 response.
- Error cases carry explicit HTTP status alongside the JSON body. A failed render
  with compatible prior output uses the ordinary successful display response with
  the prior image's unchanged URL and filename, not a new error image.
- Setup-image fetching has no ID or token headers. Display-image fetching is
  authenticated, and its URL must begin with the entire Custom Server URL.
- The display response and image request embed the SHA-256 of
  [target-gray4.png](../png/target-gray4.png). Image responses use
  `Content-Type: image/png`, exact Content-Length, no redirects and no transport
  compression.

See the [PNG fixtures](../png/README.md) for both accepted encodings,
[Render Context fixtures](../render-context/README.md) for POST/Job JSON, and
[dated acceptance evidence](../../notes/DEVELOP.md#recorded-acceptance-baseline)
for physical-device verification.
