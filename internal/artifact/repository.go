// Package artifact persists internal artifacts on the controller's PVC.
package artifact

import (
	"context"
	"errors"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

var (
	ErrCapacity         = errors.New("storage admission limit reached")
	ErrCorrupt          = errors.New("stored artifact is corrupt")
	ErrNotFound         = errors.New("artifact not found")
	ErrObsolete         = errors.New("render attempt is obsolete")
	ErrIdentityConflict = errors.New("device identity is already bound")
)

// Unit excludes definition revision intentionally: a prior revision may be a
// stale fallback, but UIDs and exact target must always match. GET omits DeviceUID.
type Unit struct {
	Namespace string        `json:"namespace"`
	ScreenUID string        `json:"screenUID"`
	DeviceUID string        `json:"deviceUID,omitempty"`
	Target    render.Target `json:"target"`
}

type Metadata struct {
	Unit             Unit      `json:"unit"`
	ScreenGeneration int64     `json:"screenGeneration"`
	InputRevision    string    `json:"inputRevision"`
	AttemptID        string    `json:"attemptID"`
	Sequence         uint64    `json:"sequence"`
	ContextDigest    string    `json:"contextDigest,omitempty"`
	ImageDigest      string    `json:"imageDigest"`
	Filename         string    `json:"filename"`
	ContentType      string    `json:"contentType"`
	Size             int       `json:"size"`
	RenderedAt       time.Time `json:"renderedAt"`
}

// IdentityStore durably pins a hardware identity to a device UID, independently
// of credentials and observable Kubernetes status. Bind is idempotent.
type IdentityStore interface {
	Binding(context.Context, string) (string, error)
	Bind(context.Context, string, string) error
}

type Repository interface {
	IdentityStore
	// Publish receives an already validated PNG and a current, ordered attempt.
	// The caller rechecks assignment, UIDs and revision before publication.
	Publish(context.Context, Metadata, []byte) (Metadata, error)
	Latest(context.Context, Unit) (Metadata, error)
	Read(context.Context, string) ([]byte, error)
	Issue(context.Context, string, string, time.Time) error
	Authorized(context.Context, string, string) (bool, error)
}
