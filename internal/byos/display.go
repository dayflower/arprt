package byos

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/deviceidentity"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errUnsupportedScreen   = errors.New("screen has no HTTP or Job source")
	errDisplayPending      = errors.New("selected delivery is no longer current")
	errDisplayUnauthorized = errors.New("display credentials changed")
)

// selectedDelivery captures one selection. Revalidation compares fresh reads
// against this snapshot, including the complete assignment fingerprint.
type selectedDelivery struct {
	screen      *api.TrmnlScreen
	target      render.Target
	revision    string
	fingerprint string
	cycling     bool
	replay      *api.ScreenCycleStatus
	reporting   renderReporting
}

type deliveryImage struct {
	metadata  artifact.Metadata
	renderErr error
}

func (image deliveryImage) state() deliveryState {
	if image.metadata.ImageDigest == "" {
		return deliveryPending
	}
	if image.renderErr != nil {
		return deliveryStale
	}
	return deliveryFresh
}

func (s *Server) display(w http.ResponseWriter, r *http.Request, d *api.TrmnlDevice, o Observation) {
	ctx := r.Context()
	observed, err := s.observeDisplay(ctx, d, o)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	if observed.targetErr != nil {
		s.outcome(ctx, d, observed.seen, deliveryOutcome{assignment: assignmentUnexamined, reason: "UnknownCapabilities", message: "No verified render target"})
		reply(w, 200, 202)
		return
	}

	selected, err := s.selectDelivery(ctx, d, observed.target)
	if err != nil {
		result := deliveryOutcome{assignment: assignmentFailed, reason: "ReferenceNotFound", message: "Referenced screen does not exist in this namespace"}
		switch {
		case errors.Is(err, assignment.ErrEmpty):
			result.reason, result.message = "NoEligibleScreens", "Assignment contains no existing screens in this namespace"
		case errors.Is(err, errUnsupportedScreen):
			result.reason, result.message = "UnsupportedFeature", "Assign an HTTP or Job screen"
		case !apierrors.IsNotFound(err):
			reply(w, 503, 503)
			return
		}
		s.outcome(ctx, d, observed.seen, result)
		reply(w, 200, 202)
		return
	}

	image, err := s.obtainImage(ctx, d, o, selected)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	headers, _ := Headers(r.Header)
	if err := s.validateIssuance(ctx, d, selected, headers["access-token"]); err != nil {
		switch {
		case errors.Is(err, errDisplayPending):
			reply(w, 200, 202)
		case errors.Is(err, errDisplayUnauthorized):
			reply(w, 401, 401)
		default:
			s.identityError(w, err)
		}
		return
	}

	// Screen reporting remains best effort, before issuance, even for pending
	// HTTP output. Job and replay preserve the separately owned render outcome.
	result := s.imageOutcome(ctx, d, selected, image)
	if result.state == deliveryPending {
		s.outcome(ctx, d, observed.seen, result)
		reply(w, 200, 202)
		return
	}
	if err := s.Repository.Issue(ctx, string(d.UID), image.metadata.ImageDigest, time.Now()); err != nil {
		reply(w, 503, 503)
		return
	}
	if result.state == deliveryStale {
		result.reason, result.message = "StaleImageAvailable", "Renderer failed; serving the previous compatible image"
		if selected.screen.Spec.Source.Job != nil {
			result.message = "Job output pending or failed; serving the previous compatible image"
		}
	}
	if selected.cycling && selected.replay == nil {
		if err := s.persistCycle(ctx, d, selected, image.metadata); err != nil {
			reply(w, 503, 503)
			return
		}
	}
	// Issuance and any new cycle turn must be durable before acknowledgement.
	s.outcome(ctx, d, observed.seen, result)
	jsonResponse(w, 200, DisplayResponse{Status: 0, ImageURL: s.deviceURL(d.Name) + "/images/" + image.metadata.ImageDigest + ".png", Filename: image.metadata.Filename, RefreshRate: s.Config.RefreshSeconds, ImageURLTimeout: 10})
}

func (s *Server) selectDelivery(ctx context.Context, d *api.TrmnlDevice, target render.Target) (selectedDelivery, error) {
	eligible, err := (assignment.Explicit{Reader: s.Reader}).Eligible(ctx, d)
	if err != nil {
		return selectedDelivery{}, err
	}
	assigned, replay := eligible.Select(d, time.Now().UTC(), time.Duration(s.Config.RefreshSeconds)*time.Second)
	revision, _ := screen.InputRevision(assigned.Spec)
	if replay && d.Status.ScreenCycle.InputRevision != revision {
		replay = false
	}
	if assigned.Spec.Source.HTTP == nil && assigned.Spec.Source.Job == nil {
		return selectedDelivery{}, errUnsupportedScreen
	}
	selected := selectedDelivery{screen: assigned, target: target, revision: revision, fingerprint: eligible.Fingerprint, cycling: eligible.Cycling, reporting: reportHTTP}
	if assigned.Spec.Source.Job != nil {
		selected.reporting = reportJob
	}
	if replay {
		selected.replay = d.Status.ScreenCycle.DeepCopy()
		selected.reporting = reportReplay
	}
	return selected, nil
}

func (s *Server) obtainImage(ctx context.Context, d *api.TrmnlDevice, o Observation, selected selectedDelivery) (deliveryImage, error) {
	assigned, target := selected.screen, selected.target
	var m artifact.Metadata
	var renderErr error
	if selected.replay != nil {
		last := selected.replay
		if _, err := s.Repository.Read(ctx, last.ImageDigest); err != nil {
			return deliveryImage{}, err
		}
		m = artifact.Metadata{ImageDigest: last.ImageDigest, Filename: last.Filename}
	} else if assigned.Spec.Source.Job != nil {
		// Polling only reads completed output. Scheduling belongs to reconciliation.
		m, renderErr = s.Repository.Latest(ctx, artifact.Unit{Namespace: assigned.Namespace, ScreenUID: string(assigned.UID), DeviceUID: string(d.UID), Target: target})
		if errors.Is(renderErr, artifact.ErrNotFound) {
			renderErr = errors.New("Job image pending")
		}
		if m.ImageDigest != "" && (m.InputRevision != selected.revision || m.ScreenGeneration != assigned.Generation) {
			renderErr = errors.New("Job update pending")
		}
		if c := meta.FindStatusCondition(d.Status.Conditions, "RenderReady"); !selected.cycling && c != nil && c.Status == metav1.ConditionFalse {
			renderErr = errors.New("Job output pending or failed")
		}
		if interval, _ := screen.EffectiveInterval(assigned.Spec); selected.cycling && m.ImageDigest != "" && time.Since(m.RenderedAt) >= interval {
			renderErr = errors.New("Job refresh pending")
		}
	} else if assigned.Spec.Source.HTTP.Method == "POST" {
		var snapshot render.Context
		// mutate returned the direct status snapshot, and this explicit overlay
		// keeps current request telemetry independent of informer propagation.
		snapshot, renderErr = render.Build(d.Name, target, devicestate.Observation(d.Status), render.Observation{Model: o.Model, FirmwareVersion: o.Firmware, Voltage: o.Voltage, RSSI: o.RSSI})
		if renderErr == nil {
			m, renderErr = s.POST.Image(ctx, assigned, d, snapshot)
		}
	} else {
		m, renderErr = s.GET.Image(ctx, assigned, target)
	}
	return deliveryImage{metadata: m, renderErr: renderErr}, nil
}

// validateIssuance is the final authorization barrier for fresh, stale and
// replayed output. Publication gates in delivery do not replace these reads.
func (s *Server) validateIssuance(ctx context.Context, d *api.TrmnlDevice, selected selectedDelivery, token string) error {
	assigned, target := selected.screen, selected.target
	// Recheck identity, assignment, definition and target after the asynchronous
	// network call. Never issue an image selected for a superseded assignment.
	var current api.TrmnlDevice
	var currentScreen api.TrmnlScreen
	if s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current) != nil || current.UID != d.UID || !reflect.DeepEqual(current.Spec, d.Spec) || current.Generation != d.Generation || !current.DeletionTimestamp.IsZero() || current.Status.Display == nil || devicestate.Target(&current) != target {
		return errDisplayPending
	}
	if s.Reader.Get(ctx, client.ObjectKeyFromObject(assigned), &currentScreen) != nil || currentScreen.UID != assigned.UID || currentScreen.Generation != assigned.Generation || !reflect.DeepEqual(currentScreen.Spec, assigned.Spec) || !currentScreen.DeletionTimestamp.IsZero() {
		return errDisplayPending
	}
	// Membership alone misses playlist order and other assignment edits that
	// invalidate the selected turn, including replay.
	currentSet, err := (assignment.Explicit{Reader: s.Reader}).Eligible(ctx, &current)
	if err != nil || currentSet.Fingerprint != selected.fingerprint {
		return errDisplayPending
	}
	// Secret contents do not change device generation. Re-resolve identity and
	// credentials after rendering, before issuing an immutable image.
	if err := s.Identity.Check(ctx, &current, d.Status.HardwareID); err != nil {
		if errors.Is(err, deviceidentity.ErrDuplicate) {
			return errDisplayPending
		}
		return err
	}
	key, keyErr := s.key(ctx, &current)
	if keyErr != nil || !equal(key, token) {
		return errDisplayUnauthorized
	}
	return nil
}

func (s *Server) persistCycle(ctx context.Context, d *api.TrmnlDevice, selected selectedDelivery, m artifact.Metadata) error {
	return s.mutate(ctx, d, func(current *api.TrmnlDevice) {
		current.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: selected.fingerprint, ScreenUID: string(selected.screen.UID), InputRevision: selected.revision, Target: *current.Status.Display, ImageDigest: m.ImageDigest, Filename: m.Filename, ServedAt: metav1.NewMicroTime(time.Now().UTC())}
	})
}
