# BYOS and Render Context fixtures

These are synthetic fixtures for the initial controller contract, informed by
official TRMNL firmware `v1.8.17`, commit
`9acd348270a493ca5f1c627ac91a9469543267ae`; they are not device captures.
The `02:00:00:00:00:01` MAC, `.test` host, friendly ID and API key are fabricated.
The API key is a readable fixture marker, not production-strength key material.
The public URL prefix and namespace in these fixtures both use `arprt`.
See [the contract and source links](../../notes/PROTOCOL.md).

Firmware sources establish the request headers, response handling and image
fetch behavior. Error responses and Render Context examples record controller
design choices. They are expected contract data, not claims about responses
captured from an existing controller. Context JSON and PNG fixtures are consumed
by current unit/integration tests and smoke scripts. The HTTP request/response
examples document expected wire behavior; this table describes each file's role.

| Files | Purpose |
| --- | --- |
| `setup-request.http`, `display-request.http` | Parse identity, firmware, dimensions and telemetry; preserve the configured external path prefix |
| `setup-image-request.http`, `display-image-request.http` | Setup logo needs no auth headers; display image carries identity / token on the device URL prefix |
| `setup-response.json`, `display-response.json` | Serialize the agreed response fields, distinguish JSON status from HTTP status, and keep the image URL / filename consistent |
| `pending-response.json`, `error-responses.json` | Exercise no-image and authentication / identity failure behavior with the specified HTTP and JSON statuses |
| `render-context-v1.json`, `render-context-no-telemetry.json` | Share one context format across POST and Job rendering; preserve omitted telemetry and exclude credentials |
| `target-gray4.png` | Validate a complete compatible PNG and exercise image serving with the digest in the display fixture |

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
- `target-gray4.png` is a fixed noninterlaced, grayscale, 2-bit 800 × 480
  image with four equal-width vertical stripes: black, gray 85, gray 170, white.
  Its bytes are retained as the test input; no experiment script is needed to
  consume it. Its SHA-256 is embedded in the display response and image request.
  Image responses use `Content-Type: image/png`, exact Content-Length, no redirects,
  and no transport compression. PNG bytes are subject to the OG's <90,000 limit.
- `render-context-v1.json` corresponds to the display request; OG voltage is
  retained without inventing a percentage. `render-context-no-telemetry.json`
  illustrates omission of unavailable values / objects.

See the [indexed PNG fixture](../phase2/README.md) for the other accepted encoding
and [dated acceptance evidence](../../notes/DEVELOP.md#recorded-acceptance-baseline)
for physical-device verification. Directory names are retained to preserve existing
test/example paths; they do not indicate an unfinished implementation phase.

The complete Render Context fixture includes `renderTarget.rotation: 90`; the
no-telemetry fixture omits rotation to demonstrate the default of 0. Final PNG
dimensions remain 800 × 480 for every rotation.

`render-context-fractional-percent.json` extends the unchanged Context v1 fixture
with a synthetic measured percentage of 20.5. It is not inferred from voltage.
The original and no-telemetry fixtures continue to exercise old readable inputs.
