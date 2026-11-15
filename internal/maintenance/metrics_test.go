package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

type maintenanceReader struct {
	client.Reader
	fail   bool
	cancel context.CancelFunc
}

func (r *maintenanceReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if r.cancel != nil {
		r.cancel()
	}
	if r.fail {
		return errors.New("injected read failure")
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestMaintenanceCompletionMetrics(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := &maintenanceReader{Reader: fake.NewClientBuilder().WithScheme(scheme).Build()}
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1700000000, 0)
	completed := started.Add(17 * time.Second)
	clockCalls := 0
	m := &StorageMaintenance{Reader: reader, Repository: repo, Namespace: "arprt", now: func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return started
		}
		return completed
	}}
	success := metricValue(t, "success")
	failure := metricValue(t, "failure")
	reader.fail = true
	previous := metricValue(t, "timestamp")
	m.runOnce(t.Context())
	if got := metricValue(t, "timestamp"); got != previous {
		t.Fatal(got, previous)
	}
	reader.fail = false
	clockCalls = 0
	m.runOnce(t.Context())
	if got := metricValue(t, "timestamp"); got != float64(completed.Unix()) {
		t.Fatal("must use completion clock", got)
	}
	reader.fail = true
	m.runOnce(t.Context())
	if got := metricValue(t, "timestamp"); got != float64(completed.Unix()) {
		t.Fatal(got)
	}
	for _, fail := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		reader.cancel, reader.fail = cancel, fail
		m.runOnce(ctx)
		cancel()
	}
	if got := metricValue(t, "success"); got-success != 1 {
		t.Fatal(got - success)
	}
	if got := metricValue(t, "failure"); got-failure != 2 {
		t.Fatal(got - failure)
	}
	if got := metricValue(t, "timestamp"); got != float64(completed.Unix()) {
		t.Fatal(got)
	}
}

func metricValue(t *testing.T, key string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "arprt_maintenance_last_success_timestamp_seconds" && key == "timestamp" {
			return family.Metric[0].GetGauge().GetValue()
		}
		if family.GetName() == "arprt_maintenance_total" {
			for _, sample := range family.Metric {
				if len(sample.Label) == 1 && sample.Label[0].GetValue() == key {
					return sample.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
