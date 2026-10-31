package byos

import (
	"context"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/screen"
)

// Recording follows all required writes under the existing device gate. It is
// best effort: a failure (including cancellation) cannot revoke issued access,
// undo a turn, or turn an otherwise successful display response into an error.
func (s *Server) recordDelivery(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery, image deliveryImage, proposed *api.ScreenCycleStatus, issuedAt time.Time) {
	r := artifact.DeliveryRecord{
		Version: 1, DeviceUID: string(d.UID), DeviceGeneration: d.Generation,
		ScreenName: selected.screen.Name, ScreenUID: string(selected.screen.UID), ScreenGeneration: selected.screen.Generation,
		Assignment: selected.fingerprint, InputRevision: selected.revision, Target: selected.resolvedTarget(),
		ImageDigest: image.issued.ImageDigest, IssuedAt: issuedAt, Result: "ready", Rendering: "ready", Cycle: proposed,
	}
	if !image.metadata.RenderedAt.IsZero() {
		t := image.metadata.RenderedAt.UTC()
		r.RenderedAt = &t
	}
	if image.renderErr != nil {
		r.Result, r.Rendering = "stale", "unknown"
		if selected.screen.Spec.Source.HTTP != nil {
			r.Rendering = "failed"
		} else if screen.IsOnDemand(selected.screen.Spec) {
			var failure *jobrender.ExecutionError
			if errors.As(image.renderErr, &failure) {
				switch failure.Reason {
				case "JobSchedulingDisabled":
					r.Rendering = "pending"
				case "", "ObsoleteAttempt":
					// A superseded operation is not proof of rendering failure.
				default:
					r.Rendering = "failed"
				}
			}
		}
	}
	if selected.replay != nil {
		r.Cycle, r.Result, r.RenderedAt = selected.replay.DeepCopy(), "unknown", nil
		r.Rendering = "unknown"
		// A digest is not rendering provenance. Only the complete committed turn
		// can carry its original generation time and stale result across replay.
		previous, err := s.Repository.Delivery(ctx, string(d.UID))
		if err == nil && artifact.EqualCycle(previous.Cycle, selected.replay) && previous.ScreenUID == r.ScreenUID && previous.Target == r.Target && previous.InputRevision == r.InputRevision {
			r.RenderedAt, r.Result = previous.RenderedAt, previous.Result
			r.Rendering = previous.Rendering
		}
	}
	if err := s.Repository.RecordDelivery(ctx, r); err != nil {
		// Never emit raw storage errors, paths, credentials or device identities.
		observe.StorageErrors.WithLabelValues("delivery_record").Inc()
	}
}
