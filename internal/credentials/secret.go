// Package credentials retrieves device keys independently of MAC registration.
package credentials

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"

	api "github.com/dayflower/arprt/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrNotProvisioned = errors.New("authentication apiKey has not been provisioned")
	ErrUnavailable    = errors.New("authentication Secret unavailable")
	ErrInvalid        = errors.New("authentication apiKey must encode at least 32 bytes as URL-safe base64")
)

// Read never provisions or repairs credentials during ordinary authentication.
func Read(ctx context.Context, reader client.Reader, d *api.TrmnlDevice) (string, error) {
	var secret corev1.Secret
	if d.Spec.AuthSecretRef.Name == "" || reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: d.Spec.AuthSecretRef.Name}, &secret) != nil {
		return "", ErrUnavailable
	}
	return value(&secret)
}

func value(secret *corev1.Secret) (string, error) {
	if !secret.DeletionTimestamp.IsZero() {
		return "", ErrUnavailable
	}
	encoded, present := secret.Data["apiKey"]
	if !present {
		return "", ErrNotProvisioned
	}
	key := string(encoded)
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(key)
	}
	if err != nil || len(raw) < 32 || len(key) > 512 || strings.ContainsAny(key, "\r\n") {
		return "", ErrInvalid
	}
	return key, nil
}

// Ensure is used only by setup after validating the hardware identity. The
// operator declares the named Secret, and setup fills only an absent apiKey.
// Optimistic patches prevent concurrent requests from replacing a winning key.
func Ensure(ctx context.Context, reader client.Reader, writer client.Client, d *api.TrmnlDevice) (string, error) {
	if d.Spec.AuthSecretRef.Name == "" {
		return "", ErrUnavailable
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var current api.TrmnlDevice
		if reader.Get(ctx, client.ObjectKeyFromObject(d), &current) != nil || current.UID != d.UID || current.Generation != d.Generation || !reflect.DeepEqual(current.Spec, d.Spec) || !current.DeletionTimestamp.IsZero() || current.UID == "" {
			return ErrUnavailable
		}
		var secret corev1.Secret
		if err := reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: d.Spec.AuthSecretRef.Name}, &secret); err != nil {
			return ErrUnavailable
		}
		if _, err := value(&secret); !errors.Is(err, ErrNotProvisioned) {
			return err
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return ErrUnavailable
		}
		before := secret.DeepCopy()
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data["apiKey"] = []byte(base64.RawURLEncoding.EncodeToString(raw[:]))
		return writer.Patch(ctx, &secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return "", ErrInvalid
		}
		return "", ErrUnavailable
	}
	// Always read back the API's value, never an unpersisted candidate or cache.
	return Read(ctx, reader, d)
}
