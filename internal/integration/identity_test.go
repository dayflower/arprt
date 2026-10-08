//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyIdentityAPI(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	for _, mode := range []string{"inline", "secret", "auto"} {
		t.Run("identity-schema-"+mode, func(t *testing.T) {
			d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Name: "identity-schema-" + mode, Namespace: "arprt"}, Spec: api.TrmnlDeviceSpec{AuthSecretRef: api.LocalReference{Name: "credentials"}}}
			if mode == "inline" {
				d.Spec.HardwareID = "02:00:00:00:00:20"
			}
			if mode == "secret" {
				d.Spec.HardwareIDSecretRef = &api.HardwareIDSecretReference{Name: "identity", Key: "mac"}
			}
			if err := c.Create(ctx, d); err != nil {
				t.Fatal("valid identity source rejected", err)
			}
			original := d.DeepCopy()
			for _, mutation := range []func(*api.TrmnlDevice){
				func(d *api.TrmnlDevice) {
					d.Spec.HardwareID = "02:00:00:00:00:21"
					d.Spec.HardwareIDSecretRef = nil
				},
				func(d *api.TrmnlDevice) { d.Spec.HardwareID = ""; d.Spec.HardwareIDSecretRef = nil },
				func(d *api.TrmnlDevice) {
					d.Spec.HardwareID = ""
					d.Spec.HardwareIDSecretRef = &api.HardwareIDSecretReference{Name: "changed", Key: "different"}
				},
				func(d *api.TrmnlDevice) {
					if d.Spec.HardwareIDSecretRef != nil {
						d.Spec.HardwareIDSecretRef.Name = "changed"
					}
				},
				func(d *api.TrmnlDevice) {
					if d.Spec.HardwareIDSecretRef != nil {
						d.Spec.HardwareIDSecretRef.Key = "different"
					}
				},
			} {
				changed := original.DeepCopy()
				mutation(changed)
				if changed.Spec.HardwareID == original.Spec.HardwareID && fmt.Sprint(changed.Spec.HardwareIDSecretRef) == fmt.Sprint(original.Spec.HardwareIDSecretRef) {
					continue
				}
				if err := c.Update(ctx, changed); !apierrors.IsInvalid(err) {
					t.Fatal("immutable source transition accepted", err)
				}
			}
			if err := c.Delete(ctx, original); err != nil {
				t.Fatal(err)
			}
		})
	}
	for i, source := range []map[string]any{
		{"hardwareID": ""},
		{"hardwareID": "02:aa:00:00:00:01"},
		{"hardwareID": "02:00:00:00:00:20", "hardwareIDSecretRef": map[string]any{"name": "identity", "key": "mac"}},
		{"hardwareIDSecretRef": map[string]any{"name": "identity"}},
		{"hardwareIDSecretRef": map[string]any{"name": "", "key": "mac"}},
		{"hardwareIDSecretRef": map[string]any{"name": "identity", "key": ""}},
		{"hardwareIDSecretRef": map[string]any{"name": "identity", "key": "bad/key"}},
		{"hardwareIDSecretRef": map[string]any{"name": "identity", "key": "mac", "namespace": "other"}},
	} {
		source["authSecretRef"] = map[string]any{"name": "credentials"}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": api.GroupVersion.String(), "kind": "TrmnlDevice", "metadata": map[string]any{"name": fmt.Sprintf("invalid-identity-%d", i), "namespace": "arprt"}, "spec": source}}
		if err := c.Create(ctx, obj, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
			t.Fatal("invalid identity source accepted", i)
		}
	}
}

func verifyIdentityRegistration(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	token := strings.Repeat("A", 43)
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := byos.NewServer(byos.Config{Namespace: "arprt", PublicBaseURL: "https://byos.example.test/arprt", EnableSetup: true}, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	for i, mode := range []string{"inline", "secret", "auto"} {
		t.Run("identity-registration-"+mode, func(t *testing.T) {
			id := fmt.Sprintf("02:00:00:00:00:%02X", 0x30+i)
			name := "identity-registration-" + mode
			d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "arprt"}, Spec: api.TrmnlDeviceSpec{AuthSecretRef: api.LocalReference{Name: name}}}
			if mode == "inline" {
				d.Spec.HardwareID = id
			}
			if mode == "secret" {
				d.Spec.HardwareIDSecretRef = &api.HardwareIDSecretReference{Name: name, Key: "mac"}
			}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "arprt"}, Data: map[string][]byte{"mac": []byte(id), "apiKey": []byte(token)}}
			for _, obj := range []client.Object{secret, d} {
				if err := c.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			original := d.DeepCopy()
			setup := func(mac string) int {
				req, _ := http.NewRequest("GET", httpServer.URL+"/arprt/devices/arprt/"+name+"/api/setup", nil)
				req.Header.Set("ID", mac)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return 0
				}
				defer resp.Body.Close()
				if resp.StatusCode == 200 {
					var value struct {
						Key string `json:"api_key"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&value); err != nil || value.Key != token {
						t.Error("setup changed credentials", err)
					}
				}
				return resp.StatusCode
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if got := setup(id); got != 200 {
						t.Errorf("setup failed: %d", got)
					}
				})
			}
			wg.Wait()
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			if d.Status.HardwareID != id || d.Spec.HardwareID != original.Spec.HardwareID {
				t.Fatal("registration changed spec or lost status identity")
			}
			// Persisted identity protects against status loss and a different next MAC.
			updateDevice(t, c, d, true, func(current *api.TrmnlDevice) {
				current.Status = api.TrmnlDeviceStatus{}
			})
			if got := setup("02:00:00:00:00:40"); got != 403 {
				t.Fatal("status loss allowed another MAC", got)
			}
			if got := setup(id); got != 200 {
				t.Fatal("same-MAC retry failed", got)
			}
			if mode == "secret" {
				if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
					t.Fatal(err)
				}
				secret.Data["mac"] = []byte("02:00:00:00:00:40")
				if err := c.Update(ctx, secret); err != nil {
					t.Fatal(err)
				}
				if got := setup(id); got != 403 {
					t.Fatal("Secret change silently altered identity", got)
				}
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			if d.Spec.HardwareID != original.Spec.HardwareID || d.Status.HardwareID != id {
				t.Fatal("identity registration not preserved")
			}
		})
	}
}
