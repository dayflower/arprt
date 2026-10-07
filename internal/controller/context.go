package controller

import (
	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
)

// Observation exports only approved status fields. Spec, credentials, identity,
// labels and annotations cannot enter the transport-independent context.
func Observation(status api.TrmnlDeviceStatus) render.Observation {
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
