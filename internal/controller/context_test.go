package controller

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestContextAllowlist(t *testing.T) {
	d := api.TrmnlDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "living-room", Namespace: "private-namespace", Labels: map[string]string{"token": "secret-label"}, Annotations: map[string]string{"password": "secret-annotation"}},
		Spec:       api.TrmnlDeviceSpec{HardwareID: "02:00:00:00:00:01", AuthSecretRef: api.LocalReference{Name: "private-secret"}, HardwareIDSecretRef: &api.HardwareIDSecretReference{Name: "identity-secret", Key: "mac"}},
		Status:     api.TrmnlDeviceStatus{HardwareID: "02:00:00:00:00:02", Device: &api.DeviceInfoStatus{Model: "og", FirmwareVersion: "1.8.17"}},
	}
	c, err := render.Build(d.Name, render.Target{Width: 800, Height: 480, Palette: "gray4"}, Observation(d.Status), render.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-namespace", "secret-label", "secret-annotation", "private-secret", "02:00:00:00:00:01", "hardwareID", "hardwareIDSecretRef", "identity-secret", "02:00:00:00:00:02", "authSecretRef", "annotations", "labels"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("context leaked %s", forbidden)
		}
	}
}
