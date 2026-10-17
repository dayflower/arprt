package devicestate

import (
	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
)

// Observation exports only approved status fields; desired rotation is added
// separately to Target. Credentials, identity, labels and annotations are excluded.
func Observation(status api.ArprtDeviceStatus) render.Observation {
	var o render.Observation
	if status.Device != nil {
		o.Model, o.FirmwareVersion = status.Device.Model, status.Device.FirmwareVersion
	}
	if status.Battery != nil {
		o.Voltage, o.Percent = status.Battery.Voltage, status.Battery.Percent
	}
	if status.Network != nil {
		o.RSSI = status.Network.RSSI
	}
	return o
}

// Target returns the renderer-facing portion of freshly resolved capabilities.
func Target(d *api.ArprtDevice) render.Target {
	return Resolved(d).Target
}
