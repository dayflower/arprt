package observe

import (
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"testing"
)

func TestInitialDeliveryAndMaintenanceSamples(t *testing.T) {
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	deliveries, maintenance := 0, false
	for _, family := range families {
		switch family.GetName() {
		case "arprt_deliveries_total":
			for _, sample := range family.Metric {
				if len(sample.Label) != 1 || sample.Label[0].GetName() != "result" || sample.GetCounter().GetValue() != 0 {
					t.Fatal(sample)
				}
				switch sample.Label[0].GetValue() {
				case "ready", "stale", "pending", "unknown":
					deliveries++
				default:
					t.Fatal(sample)
				}
			}
		case "arprt_maintenance_last_success_timestamp_seconds":
			maintenance = len(family.Metric) == 1 && family.Metric[0].GetGauge().GetValue() == 0 && len(family.Metric[0].Label) == 0
		}
	}
	if deliveries != 4 || !maintenance {
		t.Fatal(deliveries, maintenance)
	}
}
