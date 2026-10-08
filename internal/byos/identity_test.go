package byos

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func identityMode(t *testing.T, c client.Client, mode string) *api.TrmnlDevice {
	t.Helper()
	d := loadDevice(t, c)
	if mode != "inline" {
		d.Spec.HardwareID = ""
	}
	if mode == "secret" {
		d.Spec.HardwareIDSecretRef = &api.HardwareIDSecretReference{Name: "identity", Key: "mac"}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "identity"}, Data: map[string][]byte{"mac": []byte(auth()["ID"])}}
		if err := c.Create(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
	}
	d.Generation++
	if err := c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIdentityModesAndRestart(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, mode := range []string{"inline", "secret", "auto"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				b := pngFixture(t)
				renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != method {
						t.Errorf("wrong render method: %s", r.Method)
					}
					if method == "POST" {
						var contextValue map[string]any
						if err := json.NewDecoder(r.Body).Decode(&contextValue); err != nil {
							t.Error(err)
						}
						raw, _ := json.Marshal(contextValue)
						for _, private := range []string{auth()["ID"], token, "hardwareID", "hardwareIDSecretRef"} {
							if strings.Contains(string(raw), private) {
								t.Error("identity or credential leaked into context")
							}
						}
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(b)
				}))
				defer renderer.Close()
				s, c, root := newTestServer(t, renderer.URL)
				if method == "POST" {
					postScreen(t, c)
				}
				original := identityMode(t, c, mode)
				s.Config.EnableSetup = false
				if got := request(s, "GET", devicePath+"/api/setup", auth(), nil).Code; got != 403 {
					t.Fatalf("disabled setup: %d", got)
				}
				if loadDevice(t, c).Status.HardwareID != "" {
					t.Fatal("disabled setup bound identity")
				}
				wrong := auth()
				wrong["Access-Token"] = "wrong"
				if request(s, "GET", devicePath+"/api/display", wrong, nil).Code != 401 {
					t.Fatal("invalid token accepted")
				}
				if mode == "auto" {
					if request(s, "GET", devicePath+"/api/display", auth(), nil).Code != 403 {
						t.Fatal("display learned unknown identity")
					}
					if _, err := s.Repository.Binding(context.Background(), string(original.UID)); !errors.Is(err, artifact.ErrNotFound) {
						t.Fatal("unauthorized registration persisted", err)
					}
				} else if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
					t.Fatal("legacy authenticated display requires re-pairing")
				}
				s.Config.EnableSetup = true
				for range 2 {
					w := request(s, "GET", devicePath+"/api/setup", auth(), nil)
					var setup struct {
						Key string `json:"api_key"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || w.Code != 200 || setup.Key != token {
						t.Fatal("setup retry changed credentials", w.Code, err)
					}
				}
				d := loadDevice(t, c)
				if d.Status.HardwareID != auth()["ID"] || !reflect.DeepEqual(original.Spec, d.Spec) {
					t.Fatal("registration changed declarative spec or lost identity")
				}
				first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
				if first.Status != 0 {
					t.Fatal("delivery failed after setup")
				}
				// A cleared or incorrectly changed status cannot release a durable binding.
				d = loadDevice(t, c)
				d.Status = api.TrmnlDeviceStatus{}
				if err := c.Status().Update(context.Background(), d); err != nil {
					t.Fatal(err)
				}
				repo, err := artifact.NewPVC(root)
				if err != nil {
					t.Fatal(err)
				}
				s.Config.EnableSetup = false
				s, err = NewServer(s.Config, c, c, repo)
				if err != nil {
					t.Fatal(err)
				}
				other := auth()
				other["ID"] = "02:00:00:00:00:02"
				if request(s, "GET", devicePath+"/api/display", other, nil).Code != 403 {
					t.Fatal("status loss allowed rebinding")
				}
				if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || got.Filename != first.Filename {
					t.Fatal("restart lost delivery")
				}
				if loadDevice(t, c).Status.HardwareID != auth()["ID"] {
					t.Fatal("status mirror was not restored")
				}
				imagePath := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
				if request(s, "GET", imagePath, auth(), nil).Code != 200 {
					t.Fatal("issued image lost after restart")
				}
				// A replaced resource gets a new identity and cannot inherit issued images.
				d = loadDevice(t, c)
				if err := c.Delete(context.Background(), d); err != nil {
					t.Fatal(err)
				}
				d.UID = "replacement-uid"
				d.ResourceVersion = ""
				d.Status = api.TrmnlDeviceStatus{}
				if err := c.Create(context.Background(), d); err != nil {
					t.Fatal(err)
				}
				if request(s, "GET", imagePath, auth(), nil).Code == 200 {
					t.Fatal("replacement inherited issuance")
				}
			})
		}
	}
}

func TestIdentitySecretChangesAndPendingDevices(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	d := identityMode(t, c, "secret")
	var secret corev1.Secret
	for _, value := range []string{"", "02:aa:00:00:00:01", auth()["ID"] + "\n"} {
		_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "identity"}, &secret)
		secret.Data["mac"] = []byte(value)
		_ = c.Update(context.Background(), &secret)
		if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 503 {
			t.Fatal("invalid Secret fell back to auto-registration")
		}
	}
	secret.Data["mac"] = []byte(auth()["ID"])
	_ = c.Update(context.Background(), &secret)
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 200 {
		t.Fatal("valid Secret failed")
	}
	secret.Data["mac"] = []byte("02:00:00:00:00:02")
	_ = c.Update(context.Background(), &secret)
	if request(s, "GET", devicePath+"/api/display", auth(), nil).Code != 403 {
		t.Fatal("Secret update silently replaced identity")
	}
	r := &controller.DeviceReconciler{Client: c, Reader: c, AuthReader: c, Identity: s.Identity, Resolver: assignment.Explicit{Reader: c}}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
	if err != nil {
		t.Fatal(err)
	}
	if ready := meta.FindStatusCondition(loadDevice(t, c).Status.Conditions, "IdentityReady"); ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "HardwareIdentityChanged" {
		t.Fatal("identity mismatch not reported", ready)
	}
	if err := c.Delete(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 503 {
		t.Fatal("missing identity Secret accepted")
	}
}

func TestConcurrentAutoRegistrationAndMixedDuplicates(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	first := identityMode(t, c, "auto")
	second := first.DeepCopy()
	second.Name = "second"
	second.UID = "device-2"
	second.ResourceVersion = ""
	second.Status = api.TrmnlDeviceStatus{}
	if err := c.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, path := range []string{devicePath, strings.Replace(devicePath, "living-room", "second", 1)} {
		wg.Go(func() {
			if code := request(s, "GET", path+"/api/setup", auth(), nil).Code; code == 200 {
				successes.Add(1)
			} else if code != 503 {
				t.Errorf("unexpected registration status %d", code)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("simultaneous claims were not exclusive", successes.Load())
	}
	for _, d := range []*api.TrmnlDevice{first, second} {
		if _, err := s.Repository.Binding(context.Background(), string(d.UID)); err != nil && !errors.Is(err, artifact.ErrNotFound) {
			t.Fatal(err)
		}
	}
	// An explicitly configured identity also reserves the MAC before setup.
	s, c, _ = newTestServer(t, "http://unused.invalid")
	first = loadDevice(t, c)
	second = first.DeepCopy()
	second.Name = "second"
	second.UID = "device-2"
	second.ResourceVersion = ""
	second.Spec.HardwareID = ""
	second.Spec.HardwareIDSecretRef = &api.HardwareIDSecretReference{Name: "identity", Key: "mac"}
	if err := c.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "arprt"}, Data: map[string][]byte{"mac": []byte(first.Spec.HardwareID)}}); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 503 {
		t.Fatal("cross-source duplicate accepted")
	}
}

type failingStatusClient struct {
	client.Client
	fail atomic.Bool
}
type failingStatusWriter struct {
	client.SubResourceWriter
	owner *failingStatusClient
}

func (c *failingStatusClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{c.Client.Status(), c}
}
func (w *failingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.owner.fail.Swap(false) {
		return errors.New("simulated API outage")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestRegistrationStatusFailureAndInvalidCredentials(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	d := identityMode(t, c, "auto")
	var secret corev1.Secret
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "auth"}, &secret)
	secret.Data["apiKey"] = []byte("invalid")
	_ = c.Update(context.Background(), &secret)
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 401 {
		t.Fatal("invalid credentials accepted")
	}
	if _, err := s.Repository.Binding(context.Background(), string(d.UID)); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("credentials failure registered MAC")
	}
	secret.Data["apiKey"] = []byte(token)
	_ = c.Update(context.Background(), &secret)
	failing := &failingStatusClient{Client: c}
	failing.fail.Store(true)
	s.Identity.Writer = failing
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 503 {
		t.Fatal("incomplete setup succeeded")
	}
	if loadDevice(t, c).Status.HardwareID != "" {
		t.Fatal("failed status patch unexpectedly succeeded")
	}
	if id, err := s.Repository.Binding(context.Background(), string(d.UID)); err != nil || id != auth()["ID"] {
		t.Fatal("durable pending registration lost", err)
	}
	other := auth()
	other["ID"] = "02:00:00:00:00:02"
	if request(s, "GET", devicePath+"/api/setup", other, nil).Code != 403 {
		t.Fatal("incomplete setup rebound identity")
	}
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 200 {
		t.Fatal("setup retry did not recover")
	}
}

type forbiddenIdentityReader struct{ client.Reader }

func (r forbiddenIdentityReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, secret := obj.(*corev1.Secret); secret && key.Name == "identity" {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, errors.New("read denied"))
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestIdentitySecretForbidden(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	d := identityMode(t, c, "secret")
	s.Identity.Reader = forbiddenIdentityReader{c}
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 503 {
		t.Fatal("forbidden Secret fell back to automatic registration")
	}
	if _, err := s.Repository.Binding(context.Background(), string(d.UID)); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("forbidden source registered identity", err)
	}
	r := &controller.DeviceReconciler{Client: c, Reader: c, AuthReader: c, Identity: s.Identity, Resolver: assignment.Explicit{Reader: c}}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(loadDevice(t, c).Status.Conditions, "IdentityReady")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "IdentityUnavailable" {
		t.Fatal("forbidden source not reported", ready)
	}
}

func TestSecretChangesDuringRenderPreventIssuance(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, source := range []string{"identity", "auth"} {
			t.Run(method+"/"+source, func(t *testing.T) {
				started, release := make(chan struct{}), make(chan struct{})
				image := pngFixture(t)
				renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(image)
				}))
				defer renderer.Close()
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				s, c, _ := newTestServer(t, renderer.URL)
				if method == "POST" {
					postScreen(t, c)
				}
				d := identityMode(t, c, "secret")
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { result <- request(s, "GET", devicePath+"/api/display", auth(), nil) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("renderer did not start")
				}
				var secret corev1.Secret
				if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: source}, &secret); err != nil {
					t.Fatal(err)
				}
				want := 403
				if source == "identity" {
					secret.Data["mac"] = []byte("02:00:00:00:00:02")
				} else {
					want = 401
					secret.Data["apiKey"] = []byte(strings.Repeat("B", len(token)))
				}
				if err := c.Update(context.Background(), &secret); err != nil {
					t.Fatal(err)
				}
				unblock()
				select {
				case w := <-result:
					if w.Code != want {
						t.Fatalf("changed Secret issued an image: %d", w.Code)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("display did not complete")
				}
				allowed, err := s.Repository.Authorized(context.Background(), string(d.UID), fmt.Sprintf("%x", sha256.Sum256(image)))
				if err != nil || allowed {
					t.Fatal("changed Secret persisted image issuance", err)
				}
			})
		}
	}
}
