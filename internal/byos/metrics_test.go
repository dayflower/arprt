package byos

import (
	"context"
	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"testing"
)

// Read the registered samples so assertions cover the exported metric contract.
func deliveryCounts(t *testing.T) map[string]float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]float64{}
	for _, family := range families {
		if family.GetName() == "arprt_deliveries_total" {
			for _, sample := range family.Metric {
				if len(sample.Label) != 1 || sample.Label[0].GetName() != "result" {
					t.Fatal(sample)
				}
				result[sample.Label[0].GetValue()] = sample.GetCounter().GetValue()
			}
		}
	}
	if len(result) != 4 {
		t.Fatal(result)
	}
	return result
}

func assertDeliveries(t *testing.T, before map[string]float64, expected map[string]float64) {
	t.Helper()
	delta := deliveryCounts(t)
	for key, value := range before {
		delta[key] -= value
		if delta[key] == 0 {
			delete(delta, key)
		}
	}
	if len(delta) == 0 && len(expected) == 0 {
		return
	}
	if !reflect.DeepEqual(delta, expected) {
		t.Fatalf("delivery delta = %v, want %v", delta, expected)
	}
}

func TestDeliveryMetricsForCacheAndMissingAssignment(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	png := pngFixture(t)
	calls := 0
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) {
			calls++
			return render.Output{PNG: png}, nil
		})
	}
	before := deliveryCounts(t)
	for range 2 {
		if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 {
			t.Fatal(response)
		}
	}
	if calls != 1 {
		t.Fatal("normal cache was not reused", calls)
	}
	assertDeliveries(t, before, map[string]float64{"ready": 2})
	d := loadDevice(t, c)
	d.Spec.ScreenRef = &api.LocalReference{Name: "missing"}
	d.Generation++
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	before = deliveryCounts(t)
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 202 {
		t.Fatal(response)
	}
	assertDeliveries(t, before, map[string]float64{"pending": 1})
}
