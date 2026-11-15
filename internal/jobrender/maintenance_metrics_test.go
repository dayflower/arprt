package jobrender

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

type maintenanceMetricClient struct {
	client.Client
	fail        bool
	devicesOnly bool
	afterList   func(client.ObjectList)
}

func (c *maintenanceMetricClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	_, devices := list.(*api.ArprtDeviceList)
	if c.fail && (!c.devicesOnly || devices) {
		return errors.New("injected maintenance list failure")
	}
	err := c.Client.List(ctx, list, opts...)
	if c.afterList != nil {
		c.afterList(list)
	}
	return err
}

func TestJobMaintenanceCompletionMetrics(t *testing.T) {
	r, _, _, now := fixture(t)
	initial := *now
	completed := initial.Add(19 * time.Second)
	storage := &maintenanceMetricClient{Client: r.Client}
	r.Reader = storage
	sample := func(key string) float64 {
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
				for _, m := range family.Metric {
					if len(m.Label) == 1 && m.Label[0].GetValue() == key {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}
	before := sample("timestamp")
	success, failure := sample("success"), sample("failure")
	storage.fail = true
	r.runMaintenance(t.Context())
	if sample("timestamp") != before {
		t.Fatal("failed Job cleanup advanced success time")
	}
	storage.fail = false
	storage.afterList = func(list client.ObjectList) {
		if _, ok := list.(*api.ArprtDeviceList); ok {
			*now = completed
		}
	}
	r.runMaintenance(t.Context())
	if sample("timestamp") != float64(completed.Unix()) {
		t.Fatal("did not record whole-pass completion", sample("timestamp"))
	}
	storage.devicesOnly = true
	storage.fail = true
	*now = completed.Add(time.Minute)
	r.runMaintenance(t.Context())
	if sample("timestamp") != float64(completed.Unix()) {
		t.Fatal("storage failure advanced success time")
	}
	storage.fail = false
	ctx, cancel := context.WithCancel(t.Context())
	storage.afterList = func(client.ObjectList) { cancel() }
	r.runMaintenance(ctx)
	cancel()
	if sample("timestamp") != float64(completed.Unix()) || sample("success")-success != 1 || sample("failure")-failure != 2 {
		t.Fatal("canceled/failed maintenance counted as success")
	}
}
