// Package credentials retrieves device keys independently of MAC registration.
package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	api "github.com/dayflower/arprt/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Read currently reads operator-managed Secrets only. Provisioning credentials
// belongs to a separate future setup operation, never ordinary authentication.
func Read(ctx context.Context, reader client.Reader, d *api.TrmnlDevice) (string, error) {
	var secret corev1.Secret
	if d.Spec.AuthSecretRef.Name == "" || reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: d.Spec.AuthSecretRef.Name}, &secret) != nil {
		return "", errors.New("authentication Secret unavailable")
	}
	key := string(secret.Data["apiKey"])
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(key)
	}
	if err != nil || len(raw) < 32 || len(key) > 512 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("authentication apiKey must encode at least 32 bytes as URL-safe base64")
	}
	return key, nil
}
