package byos

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dayflower/arprt/internal/artifact"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func emptyAuth(t *testing.T, c client.Client) {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	delete(secret.Data, "apiKey")
	if err := c.Update(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
}

func setupKey(t *testing.T, s *Server) string {
	t.Helper()
	w := request(s, "GET", devicePath+"/api/setup", map[string]string{"ID": auth()["ID"]}, nil)
	var setup struct {
		Key string `json:"api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || w.Code != 200 {
		t.Fatal("setup failed", w.Code, err)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(setup.Key); err != nil || len(raw) != 32 {
		t.Fatal("generated key is invalid", err)
	}
	return setup.Key
}

func TestGeneratedKeysForAllIdentityAndDeliveryModes(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, mode := range []string{"inline", "secret", "auto"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				b := pngFixture(t)
				renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != method || r.Header.Get("Access-Token") != "" {
						t.Error("renderer method or privacy contract changed")
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(b)
				}))
				defer renderer.Close()
				s, c, root := newTestServer(t, renderer.URL)
				original := identityMode(t, c, mode)
				if method == "POST" {
					postScreen(t, c)
				}
				emptyAuth(t, c)
				if got := request(s, "GET", devicePath+"/api/display", auth(), nil).Code; got != 401 {
					t.Fatal("display provisioned a key", got)
				}
				if request(s, "POST", devicePath+"/api/log", auth(), []byte(`{"logs":[]}`)).Code != 401 ||
					request(s, "GET", devicePath+"/images/"+strings.Repeat("0", 64)+".png", auth(), nil).Code != 401 {
					t.Fatal("ordinary routes accepted missing credentials")
				}
				var unprovisioned corev1.Secret
				_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "auth"}, &unprovisioned)
				if _, present := unprovisioned.Data["apiKey"]; present {
					t.Fatal("ordinary authentication provisioned a key")
				}
				key := setupKey(t, s)
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						if got := setupKey(t, s); got != key {
							t.Error("repeat setup rotated key")
						}
					})
				}
				wg.Wait()
				if !reflect.DeepEqual(original.Spec, loadDevice(t, c).Spec) {
					t.Fatal("setup rewrote spec")
				}
				headers := auth()
				headers["Access-Token"] = key
				first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
				if first.Status != 0 {
					t.Fatal("generated key did not authenticate display")
				}
				path := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
				if request(s, "GET", path, headers, nil).Code != 200 || request(s, "GET", path, auth(), nil).Code != 401 {
					t.Fatal("generated key image authentication failed")
				}
				repo, err := artifact.NewPVC(root)
				if err != nil {
					t.Fatal(err)
				}
				s, err = NewServer(s.Config, c, c, repo)
				if err != nil {
					t.Fatal(err)
				}
				if setupKey(t, s) != key || request(s, "GET", path, headers, nil).Code != 200 {
					t.Fatal("restart lost the generated key or image issuance")
				}
				other := map[string]string{"ID": "02:00:00:00:00:02"}
				if request(s, "GET", devicePath+"/api/setup", other, nil).Code != 403 {
					t.Fatal("always-available setup changed registered identity")
				}
				if strings.Contains(string(mustJSON(t, loadDevice(t, c).Status)), key) {
					t.Fatal("generated key leaked into status")
				}
			})
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRejectedSetupDoesNotProvisionCredentials(t *testing.T) {
	for _, mode := range []string{"wrong-mac", "duplicate", "disabled", "invalid-source"} {
		t.Run(mode, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			d := loadDevice(t, c)
			headers := auth()
			want := 403
			switch mode {
			case "wrong-mac":
				headers["ID"] = "02:00:00:00:00:02"
			case "duplicate":
				other := d.DeepCopy()
				other.Name, other.UID, other.ResourceVersion = "duplicate", "device-2", ""
				if err := c.Create(context.Background(), other); err != nil {
					t.Fatal(err)
				}
				want = 503
			case "disabled":
				s.Config.EnableSetup = false
			case "invalid-source":
				d = identityMode(t, c, "secret")
				var secret corev1.Secret
				_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "identity"}, &secret)
				_ = c.Delete(context.Background(), &secret)
				want = 503
			}
			emptyAuth(t, c)
			if got := request(s, "GET", devicePath+"/api/setup", headers, nil).Code; got != want {
				t.Fatal("unexpected rejection", got)
			}
			var secret corev1.Secret
			_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "auth"}, &secret)
			if _, present := secret.Data["apiKey"]; present || loadDevice(t, c).Status.HardwareID != "" {
				t.Fatal("rejected setup provisioned credentials or identity")
			}
		})
	}
}

type uncertainPatchClient struct{ client.Client }

func (c uncertainPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	return errors.New("simulated lost API response")
}

func TestSetupRecoversUnacknowledgedKeyAndStatusWrites(t *testing.T) {
	for _, failure := range []string{"key", "status"} {
		t.Run(failure, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			identityMode(t, c, "auto")
			emptyAuth(t, c)
			if failure == "key" {
				s.Client = uncertainPatchClient{c}
			} else {
				writer := &failingStatusClient{Client: c}
				writer.fail.Store(true)
				s.Identity.Writer = writer
			}
			w := request(s, "GET", devicePath+"/api/setup", map[string]string{"ID": auth()["ID"]}, nil)
			if w.Code != 503 {
				t.Fatal("incomplete setup succeeded", w.Code)
			}
			var secret corev1.Secret
			_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "auth"}, &secret)
			key := string(secret.Data["apiKey"])
			if key == "" || strings.Contains(w.Body.String(), key) {
				t.Fatal("incomplete setup lost or exposed a key")
			}
			s.Client = c
			if setupKey(t, s) != key {
				t.Fatal("retry replaced the persisted key")
			}
		})
	}
}

type setupMutationClient struct {
	client.Client
	mutate func()
	once   sync.Once
}

type setupMutationStatusWriter struct {
	client.SubResourceWriter
	owner *setupMutationClient
}

func (c *setupMutationClient) Status() client.SubResourceWriter {
	return &setupMutationStatusWriter{c.Client.Status(), c}
}

func (w *setupMutationStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	w.owner.once.Do(w.owner.mutate)
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestSetupRechecksSecretsBeforeReturningKey(t *testing.T) {
	for _, source := range []string{"auth", "identity"} {
		t.Run(source, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			d := identityMode(t, c, "secret")
			emptyAuth(t, c)
			rotated := strings.Repeat("B", 43)
			s.Identity.Writer = &setupMutationClient{Client: c, mutate: func() {
				var secret corev1.Secret
				if err := c.Get(context.Background(), client.ObjectKey{Namespace: d.Namespace, Name: source}, &secret); err != nil {
					t.Error(err)
					return
				}
				if source == "auth" {
					secret.Data["apiKey"] = []byte(rotated)
				} else {
					secret.Data["mac"] = []byte("02:00:00:00:00:02")
				}
				if err := c.Update(context.Background(), &secret); err != nil {
					t.Error(err)
				}
			}}
			w := request(s, "GET", devicePath+"/api/setup", map[string]string{"ID": auth()["ID"]}, nil)
			var setup struct {
				Key string `json:"api_key"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
				t.Fatal(err)
			}
			if source == "auth" {
				if w.Code != 200 || setup.Key != rotated {
					t.Fatal("setup returned a superseded key", w.Code)
				}
			} else if w.Code != 403 || setup.Key != "" {
				t.Fatal("setup returned a key for changed hardware", w.Code)
			}
			if _, err := s.Repository.Binding(context.Background(), string(d.UID)); err != nil || loadDevice(t, c).Status.HardwareID == "" {
				t.Fatal("external changes discarded durable registration", err)
			}
		})
	}
}
