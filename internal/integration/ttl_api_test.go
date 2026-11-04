//go:build integration

package integration

import (
	"fmt"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyJobTTLAPI(t *testing.T, c client.Client) {
	for _, mode := range []string{"Scheduled", "OnDemand"} {
		for i, ttl := range []any{nil, int64(0), int64(600), int64(7200), int64(2147483647), int64(-1), int64(2147483648), "600", 0.5} {
			t.Run(fmt.Sprintf("ttl-%s-%d", mode, i), func(t *testing.T) {
				job := map[string]any{"executionMode": mode, "renderer": map[string]any{"image": "renderer:fixture"}}
				if ttl != nil {
					job["ttlSecondsAfterFinished"] = ttl
				}
				o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": api.GroupVersion.String(), "kind": "ArprtScreen", "metadata": map[string]any{"name": fmt.Sprintf("ttl-%s-%d", map[string]string{"Scheduled": "scheduled", "OnDemand": "ondemand"}[mode], i), "namespace": "arprt"}, "spec": map[string]any{"source": map[string]any{"job": job}}}}
				err := c.Create(t.Context(), o)
				if i >= 5 {
					if !apierrors.IsInvalid(err) {
						t.Fatalf("invalid TTL accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				value, present, err := unstructured.NestedInt64(o.Object, "spec", "source", "job", "ttlSecondsAfterFinished")
				if err != nil || (ttl == nil && present) || (ttl != nil && (!present || value != ttl.(int64))) {
					t.Fatalf("TTL changed: %v %v %v", value, present, err)
				}
			})
		}
	}
}
