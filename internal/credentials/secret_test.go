package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (client.Client, *api.TrmnlDevice) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "living-room", UID: "device-1", Generation: 1}, Spec: api.TrmnlDeviceSpec{AuthSecretRef: api.LocalReference{Name: "auth"}}}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "auth", Labels: map[string]string{"keep": "label"}}, Data: map[string][]byte{"other": []byte("preserve")}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, s).Build(), d
}

func TestConcurrentProvisioningAndReuse(t *testing.T) {
	c, d := fixture(t)
	ctx := context.Background()
	if _, err := Read(ctx, c, d); !errors.Is(err, ErrNotProvisioned) {
		t.Fatal("ordinary read did not report missing key", err)
	}
	keys := make(chan string, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			key, err := Ensure(ctx, c, c, d)
			if err != nil {
				t.Error(err)
			}
			keys <- key
		})
	}
	wg.Wait()
	close(keys)
	key, err := Read(ctx, c, d)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		t.Fatal("generated key format or entropy length invalid", err)
	}
	for got := range keys {
		if got != key {
			t.Fatal("concurrent setup returned a losing candidate")
		}
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "trmnl", Name: "auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["other"]) != "preserve" || secret.Labels["keep"] != "label" {
		t.Fatal("setup changed unrelated Secret content")
	}
	before := secret.DeepCopy()
	if got, err := Ensure(ctx, c, deniedPatch{c}, d); err != nil || got != key {
		t.Fatal("repeat setup requires a write or rotates key", err)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(&secret), &secret)
	if !reflect.DeepEqual(before, &secret) {
		t.Fatal("repeat setup mutated the Secret")
	}
}

type deniedPatch struct{ client.Client }

func (c deniedPatch) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("patch denied"))
}

func TestProvisioningFailuresDoNotRepairOrOverwrite(t *testing.T) {
	for _, mode := range []string{"forbidden", "missing", "invalid", "empty", "obsolete"} {
		t.Run(mode, func(t *testing.T) {
			c, d := fixture(t)
			ctx := context.Background()
			var secret corev1.Secret
			_ = c.Get(ctx, client.ObjectKey{Namespace: "trmnl", Name: "auth"}, &secret)
			writer := client.Client(c)
			want := ErrUnavailable
			switch mode {
			case "forbidden":
				writer = deniedPatch{c}
			case "missing":
				_ = c.Delete(ctx, &secret)
			case "invalid", "empty":
				secret.Data["apiKey"] = []byte("")
				if mode == "invalid" {
					secret.Data["apiKey"] = []byte("invalid")
				}
				_ = c.Update(ctx, &secret)
				want = ErrInvalid
			case "obsolete":
				d.UID = "replaced-device"
			}
			before := secret.DeepCopy()
			if _, err := Ensure(ctx, c, writer, d); !errors.Is(err, want) {
				t.Fatal("unexpected provisioning result", err)
			}
			err := c.Get(ctx, client.ObjectKeyFromObject(&secret), &secret)
			if mode == "missing" {
				if !apierrors.IsNotFound(err) {
					t.Fatal("setup created a missing Secret", err)
				}
			} else if err != nil || !reflect.DeepEqual(before, &secret) {
				t.Fatal("failed setup modified Secret", err)
			}
		})
	}
}

func TestExistingKeysArePreserved(t *testing.T) {
	for _, key := range []string{strings.Repeat("A", 43), base64.URLEncoding.EncodeToString(make([]byte, 32))} {
		c, d := fixture(t)
		ctx := context.Background()
		var secret corev1.Secret
		_ = c.Get(ctx, client.ObjectKey{Namespace: "trmnl", Name: "auth"}, &secret)
		secret.Data["apiKey"] = []byte(key)
		_ = c.Update(ctx, &secret)
		if got, err := Ensure(ctx, c, deniedPatch{c}, d); err != nil || got != key {
			t.Fatal("operator-supplied key changed", err)
		}
	}
}
