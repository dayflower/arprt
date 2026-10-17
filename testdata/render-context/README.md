# Render Context fixtures

These synthetic Context v1 JSON inputs are shared by POST and Job rendering
tests and renderer examples. They contain allowlisted device observations and
no credentials or hardware identity. The schema and versioning contract belong
in [PROTOCOL](../../notes/PROTOCOL.md#render-context-v1).

| Fixture | Purpose |
| --- | --- |
| [render-context-v1.json](render-context-v1.json) | Complete Context corresponding to the [display request](../protocol/display-request.http), including `renderTarget.rotation: 90` |
| [render-context-no-telemetry.json](render-context-no-telemetry.json) | Omit unavailable observations and rotation to exercise the default of 0 |
| [render-context-spectra6.json](render-context-spectra6.json) | E1002 Spectra 6 target without a firmware observation |
| [render-context-gray16.json](render-context-gray16.json) | E1003 source-reviewed 1872 × 1404 orientation, without telemetry |
| [render-context-fractional-percent.json](render-context-fractional-percent.json) | Extend the complete input with a synthetic measured percentage of 20.5 |

The OG voltage observation does not imply a battery percentage. The fractional
percentage is a separate synthetic measurement, not a voltage conversion.
All fixtures retain Context version v1; final PNG dimensions match the target
for every rotation. Output budgets and value provenance stay internal. New
profile fixtures are software inputs, not physical acceptance evidence. Unit tests check the JSON
serialization, and renderer examples exercise omission and unknown-field handling.
