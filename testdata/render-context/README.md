# Render Context fixtures

These synthetic Context v1 JSON inputs are shared by POST and Job rendering
tests and renderer examples. They contain allowlisted device observations and
no credentials or hardware identity. The schema and versioning contract belong
in [PROTOCOL](../../notes/PROTOCOL.md#render-context-v1).

| Fixture | Purpose |
| --- | --- |
| [render-context-v1.json](render-context-v1.json) | Complete Context corresponding to the [display request](../protocol/display-request.http), including `renderTarget.rotation: 90` |
| [render-context-no-telemetry.json](render-context-no-telemetry.json) | Omit unavailable observations and rotation to exercise the default of 0 |
| [render-context-fractional-percent.json](render-context-fractional-percent.json) | Extend the complete input with a synthetic measured percentage of 20.5 |

The OG voltage observation does not imply a battery percentage. The fractional
percentage is a separate synthetic measurement, not a voltage conversion.
All fixtures retain Context version v1 and the 800 × 480 gray4 target; final
PNG dimensions remain unchanged for every rotation. Unit tests check the JSON
serialization, and renderer examples exercise omission and unknown-field handling.
