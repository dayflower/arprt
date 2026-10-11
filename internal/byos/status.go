package byos

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// observedDisplay retains the persisted observation timestamp used to guard
// later best-effort reports. Observation persistence itself is mandatory.
type observedDisplay struct {
	target    render.Target
	targetErr error
	seen      metav1.Time
}

// observeDisplay owns capabilities, telemetry, AuthenticationReady,
// TargetKnown and TelemetryValid. mutate retries with direct current status.
func (s *Server) observeDisplay(ctx context.Context, d *api.ArprtDevice, o Observation) (observedDisplay, error) {
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	var target render.Target
	var targetErr error
	if err := s.mutate(ctx, d, func(current *api.ArprtDevice) {
		model, fw := "", ""
		if current.Status.Device != nil {
			model = current.Status.Device.Model
			fw = current.Status.Device.FirmwareVersion
		}
		target, model, fw, targetErr = o.Target(model, fw)
		if targetErr == nil {
			target.Rotation = current.Spec.Rotation
			targetErr = target.Validate()
		}
		// A slower earlier request cannot overwrite later observations.
		if current.Status.LastSeenAt != nil && current.Status.LastSeenAt.After(now.Time) {
			targetErr = artifact.ErrObsolete
			return
		}
		condition(current, "AuthenticationReady", true, "CredentialsConfigured", "Authentication Secret is usable")
		if current.Status.LastSeenAt == nil || now.Sub(current.Status.LastSeenAt.Time) >= 30*time.Second {
			current.Status.LastSeenAt = &now
		}
		current.Status.Device = &api.DeviceInfoStatus{Model: model, FirmwareVersion: fw}
		current.Status.Display = nil
		if targetErr == nil {
			current.Status.Display = &api.DisplayStatus{Width: target.Width, Height: target.Height, Palette: target.Palette}
		}
		if o.Voltage != nil {
			if current.Status.Battery == nil {
				current.Status.Battery = &api.BatteryStatus{}
			}
			current.Status.Battery.Voltage = o.Voltage
		}
		if o.Percent != nil {
			if current.Status.Battery == nil {
				current.Status.Battery = &api.BatteryStatus{}
			}
			current.Status.Battery.Percent = o.Percent
		}
		if o.RSSI != nil {
			current.Status.Network = &api.NetworkStatus{RSSI: o.RSSI}
		}
		condition(current, "TargetKnown", targetErr == nil, "CapabilitiesVerified", "Verified OG / 1.8.17 target")
		if targetErr != nil {
			condition(current, "TargetKnown", false, "UnknownCapabilities", "Supply a reviewed model/firmware tuple and matching dimensions")
		}
		condition(current, "TelemetryValid", len(o.Invalid) == 0, "TelemetryAccepted", "Optional telemetry parsed")
		if len(o.Invalid) > 0 {
			condition(current, "TelemetryValid", false, "InvalidTelemetry", "Invalid fields omitted: "+strings.Join(o.Invalid, ", "))
		}
	}); err != nil {
		return observedDisplay{}, err
	}
	return observedDisplay{target: target, targetErr: targetErr, seen: *d.Status.LastSeenAt}, nil
}

// mutate reads each retry directly and patches only status with a resourceVersion
// precondition. It refuses results belonging to replaced or edited devices.
func (s *Server) mutate(ctx context.Context, d *api.ArprtDevice, fn func(*api.ArprtDevice)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.ArprtDevice
		if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current); err != nil {
			return err
		}
		if current.UID != d.UID || current.Generation != d.Generation || !reflect.DeepEqual(current.Spec, d.Spec) || !current.DeletionTimestamp.IsZero() {
			return artifact.ErrObsolete
		}
		before := current.DeepCopy()
		fn(&current)
		current.Status.ObservedGeneration = current.Generation
		if reflect.DeepEqual(before.Status, current.Status) {
			*d = current
			return nil
		}
		err := s.Client.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			*d = current
		}
		return err
	})
}
func condition(d *api.ArprtDevice, kind string, ok bool, reason, message string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: d.Generation})
}

type assignmentState uint8

const (
	assignmentUnexamined assignmentState = iota
	assignmentFailed
	assignmentResolved
)

type renderReporting uint8

const (
	reportHTTP renderReporting = iota
	reportJob
	reportReplay
)

type deliveryState uint8

const (
	deliveryPending deliveryState = iota
	deliveryFresh
	deliveryStale
)

type deliveryOutcome struct {
	assignment assignmentState
	state      deliveryState
	reporting  renderReporting
	reason     string
	message    string
}

// imageOutcome reports HTTP screen readiness before issuance. Job rendering
// owns its screen status; replay retains the last serving reason/message.
func (s *Server) imageOutcome(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery, image deliveryImage) deliveryOutcome {
	assigned, renderErr := selected.screen, image.renderErr
	reason, message := "ImageReady", "Validated HTTP image available"
	if renderErr != nil {
		reason, message = "RenderFailed", "HTTP rendering failed; check renderer availability and the PNG contract"
		if errors.Is(renderErr, artifact.ErrCapacity) {
			reason, message = "StorageCapacityExceeded", "Storage admission limit reached; expand storage or safely release unreferenced data"
		}
	}
	if assigned.Spec.Source.Job != nil {
		message = "Validated Job image available"
		if renderErr != nil {
			reason, message = "JobPending", "Job output is pending or failed; check RenderReady and Job status"
		}
	} else if selected.replay == nil {
		s.screenOutcome(ctx, assigned, renderErr == nil, reason, message)
	}
	if selected.replay != nil {
		if prior := meta.FindStatusCondition(d.Status.Conditions, "Ready"); prior != nil {
			reason, message = prior.Reason, prior.Message
		}
	}
	return deliveryOutcome{assignment: assignmentResolved, state: image.state(), reporting: selected.reporting, reason: reason, message: message}
}

// outcome owns device serving readiness and assignment results. Only ordinary
// HTTP delivery owns RenderReady; Job and replay preserve it. Reporting errors
// are best effort and must not become delivery barriers.
func (s *Server) outcome(ctx context.Context, d *api.ArprtDevice, seen metav1.Time, result deliveryOutcome) {
	_ = s.mutate(ctx, d, func(current *api.ArprtDevice) {
		if current.Status.LastSeenAt == nil || !current.Status.LastSeenAt.Equal(&seen) {
			return
		}
		switch result.assignment {
		case assignmentResolved:
			condition(current, "AssignmentReady", true, "AssignmentResolved", "Same-namespace screen resolved")
		case assignmentFailed:
			condition(current, "AssignmentReady", false, result.reason, result.message)
		}
		condition(current, "Ready", result.state != deliveryPending, result.reason, result.message)
		if result.assignment == assignmentResolved && result.reporting == reportHTTP {
			renderReady := result.state == deliveryFresh
			renderReason := "ImageReady"
			if !renderReady {
				renderReason = "RenderFailed"
			}
			condition(current, "RenderReady", renderReady, renderReason, result.message)
		}
	})
}

// screenOutcome is best-effort HTTP rendering status, with generation/revision
// guards and optimistic conflict retries. It does not authorize issuance.
func (s *Server) screenOutcome(ctx context.Context, screenObj *api.ArprtScreen, ready bool, reason, message string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.ArprtScreen
		if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(screenObj), &current); err != nil {
			return err
		}
		rev, _ := screen.InputRevision(current.Spec)
		expected, _ := screen.InputRevision(screenObj.Spec)
		if current.UID != screenObj.UID || current.Generation != screenObj.Generation || rev != expected {
			return artifact.ErrObsolete
		}
		before := current.DeepCopy()
		state := metav1.ConditionFalse
		if ready {
			state = metav1.ConditionTrue
		}
		current.Status.ObservedGeneration = current.Generation
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: state, Reason: reason, Message: message, ObservedGeneration: current.Generation})
		if reflect.DeepEqual(before.Status, current.Status) {
			return nil
		}
		if err := s.Client.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		ctrl.LoggerFrom(ctx).Info("HTTP render state changed", "namespace", current.Namespace, "screen", current.Name, "reason", reason)
		if s.Recorder != nil && !ready {
			s.Recorder.Eventf(&current, nil, corev1.EventTypeWarning, reason, "Render", "%s", message)
		}
		return nil
	})
}
