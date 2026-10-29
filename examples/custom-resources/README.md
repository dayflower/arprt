# Custom resource examples

These manifests illustrate the public ARPRT API. Install the CRDs and controller
with the [quickstart](../../README.md#installation-and-pairing), and declare the
required named auth Secrets in private Helm values. Apply selected manifests in
the controller's namespace; the guides use `arprt`.

| Manifest | Purpose and prerequisites |
| --- | --- |
| [device.yaml](device.yaml) | Register the MAC during first setup; references `living-room-auth` and the `photo` Screen from the quickstart |
| [device-inline.yaml](device-inline.yaml) | Use a direct hardware identity; replace the synthetic MAC in private configuration |
| [device-secret.yaml](device-secret.yaml) | Read hardware identity from an external `living-room-identity` Secret; grant named identity read permission through Helm values |
| [device-discovery.yaml](device-discovery.yaml) | Discover eligible Screens in name order |
| [device-playlist.yaml](device-playlist.yaml) | Assign the `living-room` Playlist |
| [playlist.yaml](playlist.yaml) | Order the `photo`, `device-status` and `weather` Screens |
| [screens.yaml](screens.yaml) | Illustrate HTTP GET, POST and Job configuration; replace placeholder renderer endpoints, image and ConfigMap references |
| [screen-overlay.yaml](screen-overlay.yaml) | Configure timestamp, battery and Wi-Fi overlays; supply the referenced HTTP renderer |
| [screen-ondemand.yaml](screen-ondemand.yaml) | Run a Job on display requests; create the gray-bands image ConfigMap and enable Job/PVC support as described in the [Job guide](../../notes/RENDERERS.md#job-example-and-container-requirements) |

The Device manifests are alternatives for the same `living-room` resource, not
a set to apply together. Choose the identity mode before creating the Device;
changing it requires replacement. The generic Screen examples require their
referenced services or images and are not standalone renderer installations.
For runnable renderer examples, start from the [example index](../README.md).

The owning guides cover [identity and credentials](../../notes/OPERATIONS.md#hardware-identity),
[playlists and discovery](../../README.md#playlists-and-automatic-discovery), and
[display overlays](../../notes/RENDERERS.md#controller-display-overlays).
