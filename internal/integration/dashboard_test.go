//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/dashboard"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyDashboardAPI(t *testing.T, c client.Client) {
	t.Helper()
	const namespace = "dashboard-api"
	if err := c.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: fmt.Sprintf("device-%d", i)}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: "auth"}}}
		if err := c.Create(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := dashboard.NewServer(dashboard.Config{Namespace: namespace}, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	next := ""
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 3 {
			t.Fatal("pagination did not terminate")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/api/devices?limit=2&continue="+url.QueryEscape(next), nil))
		var page dashboard.DevicePage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Devices) > 2 || len(page.Devices) == 0 {
			t.Fatal("invalid bounded page", w.Code, w.Body.String())
		}
		for _, row := range page.Devices {
			if seen[row.Name] || row.Namespace != namespace || row.RecordStatus != "missing" || row.Communication.State != "Never seen" || row.Rendering.State != "Unknown" {
				t.Fatal("duplicate, cross-namespace or invented observation", row)
			}
			seen[row.Name] = true
		}
		next = page.Continue
		if next == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatal("pagination lost devices", seen)
	}
	var devices api.ArprtDeviceList
	if err := c.List(t.Context(), &devices, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	for _, d := range devices.Items {
		if d.Status.LastSeenAt != nil || d.Status.ScreenCycle != nil || len(d.Status.Conditions) != 0 {
			t.Fatal("dashboard changed API status", d.Status)
		}
	}
}
