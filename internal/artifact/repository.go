// Package artifact persists internal artifacts on the controller's PVC.
package artifact

import (
	"context"
	"errors"
	"time"

	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/render"
)

var (
	ErrPending          = errors.New("device delivery recovery required")
	ErrCapacity         = errors.New("storage admission limit reached")
	ErrCorrupt          = errors.New("stored artifact is corrupt")
	ErrNotFound         = errors.New("artifact not found")
	ErrObsolete         = errors.New("render attempt is obsolete")
	ErrIdentityConflict = errors.New("device identity is already bound")
)

// Unit excludes definition revision intentionally: a prior revision may be a
// stale fallback, but UIDs and exact target must always match. GET omits DeviceUID.
type Unit struct {
	Namespace string                   `json:"namespace"`
	ScreenUID string                   `json:"screenUID"`
	DeviceUID string                   `json:"deviceUID,omitempty"`
	Target    render.Target            `json:"target"`
	Output    render.OutputConstraints `json:"output,omitzero"`
}

func (u Unit) Validate() error {
	if u.Namespace == "" || u.ScreenUID == "" {
		return errors.New("artifact unit requires namespace and screen UID")
	}
	if u.Output.IsZero() {
		return u.Target.Validate()
	}
	return u.ResolvedTarget().Validate()
}

func (u Unit) ResolvedTarget() render.ResolvedTarget {
	return render.ResolvedTarget{Target: u.Target, Output: u.Output}
}

func (u Unit) byteLimit() int {
	if u.Output.IsZero() {
		return 90000
	}
	return u.Output.PNGByteLimit
}

// legacyUnit is the original disk DTO. Validate its original JSON/hash before
// applying any compatibility defaults; changing the old key would hide GC roots.
type legacyUnit struct {
	Namespace string        `json:"namespace"`
	ScreenUID string        `json:"screenUID"`
	DeviceUID string        `json:"deviceUID,omitempty"`
	Target    render.Target `json:"target"`
}

func (u Unit) legacy() legacyUnit { return legacyUnit{u.Namespace, u.ScreenUID, u.DeviceUID, u.Target} }

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

// Fresh prevents a validated legacy fallback from acquiring current freshness.
func (m Metadata) Fresh(u Unit, revision string) bool {
	return m.Unit == u && m.InputRevision == revision
}

// IdentityStore durably pins a hardware identity to a device UID, independently
// of credentials and observable Kubernetes status. Bind is idempotent.
type IdentityStore interface {
	Binding(context.Context, string) (string, error)
	Bind(context.Context, string, string) error
}

type Repository interface {
	IdentityStore
	CornerStore
	DeliveryStore
	// Derived images never update base latest or attempt sequence. Returned pins
	// must be released at request completion, after issuance/state commitment.
	FindDerived(context.Context, Metadata, overlay.Drawing) (Metadata, func(), error)
	StoreDerived(context.Context, Metadata, overlay.Drawing, []byte) (Metadata, func(), error)
	// Publish receives an already validated PNG and a current, ordered attempt.
	// The caller rechecks assignment, UIDs and revision before publication.
	Publish(context.Context, Metadata, []byte) (Metadata, error)
	Latest(context.Context, Unit) (Metadata, error)
	Read(context.Context, string) ([]byte, error)
	Issue(context.Context, string, string, time.Time) error
	Authorized(context.Context, string, string) (bool, error)
}
