// Package deviceidentity resolves and registers device identities. Credentials
// and setup admission remain separate from this service.
package deviceidentity

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"sync"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrMismatch  = errors.New("hardware identity does not match registration")
	ErrUnbound   = errors.New("setup is required to register this device")
	ErrDuplicate = errors.New("hardware identity is already registered in this namespace")
	ErrSource    = errors.New("hardware identity source is unavailable or invalid")
	macPattern   = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)
)

func Valid(id string) bool { return macPattern.MatchString(id) }

type Service struct {
	Reader client.Reader
	Writer client.Client
	Store  artifact.IdentityStore
	// One active controller serves one namespace. All setup/display registration
	// must use this shared service, serializing claims across different devices.
	mu sync.Mutex
}

func (s *Service) Declared(ctx context.Context, d *api.ArprtDevice) (string, error) {
	id := d.Spec.HardwareID
	if ref := d.Spec.HardwareIDSecretRef; ref != nil {
		if id != "" || ref.Name == "" || ref.Key == "" {
			return "", ErrSource
		}
		var secret corev1.Secret
		if s.Reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: ref.Name}, &secret) != nil {
			return "", ErrSource
		}
		id = string(secret.Data[ref.Key])
		if !Valid(id) {
			return "", ErrSource
		}
	}
	if id != "" && !Valid(id) {
		return "", ErrSource
	}
	return id, nil
}

// Binding reads authoritative PVC state. Status is a recoverable mirror and a
// fallback for readers without the store; it cannot override an existing record.
func (s *Service) Binding(ctx context.Context, d *api.ArprtDevice) (string, error) {
	if s.Store != nil {
		id, err := s.Store.Binding(ctx, string(d.UID))
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, artifact.ErrNotFound) {
			return "", err
		}
	}
	if d.Status.HardwareID != "" && !Valid(d.Status.HardwareID) {
		return "", ErrSource
	}
	return d.Status.HardwareID, nil
}

func (s *Service) Resolve(ctx context.Context, d *api.ArprtDevice) (string, error) {
	declared, err := s.Declared(ctx, d)
	if err != nil {
		return "", err
	}
	bound, err := s.Binding(ctx, d)
	if err != nil {
		return "", err
	}
	if bound != "" {
		if declared != "" && declared != bound {
			return "", ErrMismatch
		}
		return bound, nil
	}
	return declared, nil
}

func (s *Service) Unique(ctx context.Context, d *api.ArprtDevice, id string) (bool, error) {
	if id == "" {
		return false, ErrUnbound
	}
	var devices api.ArprtDeviceList
	if err := s.Reader.List(ctx, &devices, client.InNamespace(d.Namespace)); err != nil {
		return false, err
	}
	for i := range devices.Items {
		other := &devices.Items[i]
		if other.UID == d.UID || !other.DeletionTimestamp.IsZero() {
			continue
		}
		reserved, err := s.Binding(ctx, other)
		if err != nil {
			return false, err
		}
		if reserved == "" {
			reserved, err = s.Declared(ctx, other)
			// A missing/invalid external Secret does not reserve an unknown MAC. The
			// affected device remains unusable until its own source can be resolved.
			if err != nil {
				continue
			}
		}
		if reserved == id {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) Check(ctx context.Context, d *api.ArprtDevice, id string) error {
	return s.ValidateRegistration(ctx, d, id, false)
}

// ValidateRegistration checks eligibility without learning a MAC. Setup uses it
// before provisioning credentials; Register repeats it against a fresh snapshot.
func (s *Service) ValidateRegistration(ctx context.Context, d *api.ArprtDevice, id string, setup bool) error {
	if !Valid(id) {
		return ErrMismatch
	}
	expected, err := s.Resolve(ctx, d)
	if err != nil {
		return err
	}
	if expected == "" && !setup {
		return ErrUnbound
	}
	if expected != "" && expected != id {
		return ErrMismatch
	}
	unique, err := s.Unique(ctx, d, id)
	if err != nil {
		return err
	}
	if !unique {
		return ErrDuplicate
	}
	return nil
}

// Register is called only after setup admission and credential validation, or
// an authenticated display with an explicit identity (legacy-compatible).
// Durable publication precedes the optimistic status patch and setup response.
func (s *Service) Register(ctx context.Context, d *api.ArprtDevice, id string, setup bool) error {
	if !Valid(id) {
		return ErrMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var current api.ArprtDevice
		if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current); err != nil {
			return err
		}
		if current.UID != d.UID || current.Generation != d.Generation || !reflect.DeepEqual(current.Spec, d.Spec) || !current.DeletionTimestamp.IsZero() {
			return artifact.ErrObsolete
		}
		if err := s.ValidateRegistration(ctx, &current, id, setup); err != nil {
			return err
		}
		if s.Store == nil {
			return errors.New("identity registration requires durable storage")
		}
		if err := s.Store.Bind(ctx, string(current.UID), id); err != nil {
			return err
		}
		before := current.DeepCopy()
		current.Status.HardwareID = id
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "IdentityReady", Status: metav1.ConditionTrue, Reason: "IdentityRegistered", Message: "Hardware identity is registered", ObservedGeneration: current.Generation})
		if !reflect.DeepEqual(before.Status, current.Status) {
			if err := s.Writer.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
		}
		*d = current
		return nil
	})
}
