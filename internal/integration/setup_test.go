//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyAutomaticCredentials(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Name: "automatic-key", Namespace: "arprt"}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: "automatic-key"}}}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	// A GitOps-managed manifest declares the Secret without owning its key data.
	// Apply writes its response into the configuration, so recreate the intent on each call.
	placeholder := func() *corev1apply.SecretApplyConfiguration {
		return corev1apply.Secret(d.Name, d.Namespace).WithType(corev1.SecretTypeOpaque)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return c.Apply(ctx, placeholder(), client.FieldOwner("setup-gitops"))
	}); err != nil {
		t.Fatal(err)
	}
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
	setup := func() (int, string) {
		req, _ := http.NewRequest("GET", httpServer.URL+"/arprt/devices/arprt/automatic-key/api/setup", nil)
		req.Header.Set("ID", "02:00:00:00:00:50")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0, ""
		}
		defer resp.Body.Close()
		var value struct {
			Key string `json:"api_key"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
			t.Error(err)
		}
		return resp.StatusCode, value.Key
	}
	var wg sync.WaitGroup
	keys := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			status, key := setup()
			if status != 200 {
				t.Error("real API concurrent setup failed", status)
			}
			keys <- key
		})
	}
	wg.Wait()
	close(keys)
	key, err := credentials.Read(ctx, c, d)
	if raw, decodeErr := base64.RawURLEncoding.DecodeString(key); err != nil || decodeErr != nil || len(raw) != 32 {
		t.Fatal("generated API key invalid", err, decodeErr)
	}
	for returned := range keys {
		if returned != key {
			t.Fatal("setup returned a losing candidate")
		}
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return c.Apply(ctx, placeholder(), client.FieldOwner("setup-gitops"))
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := credentials.Read(ctx, c, d); err != nil || got != key {
		t.Fatal("GitOps re-apply deleted or replaced the generated key", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.HardwareID != "" || d.Status.HardwareID != "02:00:00:00:00:50" {
		t.Fatal("generated credentials changed identity behavior")
	}
	// API server rejection of a write must not claim registration succeeded.
	immutable := true
	locked := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "immutable-auth"}, Immutable: &immutable}
	if err := c.Create(ctx, locked); err != nil {
		t.Fatal(err)
	}
	updateDevice(t, c, d, false, func(current *api.ArprtDevice) {
		current.Spec.AuthSecretRef.Name = locked.Name
	})
	if code, key := setup(); code != 503 || key != "" {
		t.Fatal("immutable empty Secret was overwritten", code)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(locked), locked); err != nil || len(locked.Data) != 0 {
		t.Fatal("failed provisioning changed immutable Secret", err)
	}
	// Existing valid immutable keys are usable without write permissions.
	operator := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "immutable-operator-auth"}, Immutable: &immutable, Data: map[string][]byte{"apiKey": []byte(strings.Repeat("A", 43))}}
	if err := c.Create(ctx, operator); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	updateDevice(t, c, d, false, func(current *api.ArprtDevice) {
		current.Spec.AuthSecretRef.Name = operator.Name
	})
	if code, key := setup(); code != 200 || key != string(operator.Data["apiKey"]) {
		t.Fatal("immutable operator key rejected", code)
	}
}

// The manager is running during these scenarios. Test mutations must tolerate
// its status patches instead of failing on an unrelated resourceVersion change.
func updateDevice(t *testing.T, c client.Client, d *api.ArprtDevice, status bool, mutate func(*api.ArprtDevice)) {
	t.Helper()
	ctx := context.Background()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		mutate(d)
		if status {
			return c.Status().Update(ctx, d)
		}
		return c.Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
}
