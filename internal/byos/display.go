package byos

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"reflect"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/deviceidentity"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
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
	screen        *api.ArprtScreen
	target        render.Target
	output        render.OutputConstraints
	revision      string
	fingerprint   string
	cycling       bool
	replay        *api.ScreenCycleStatus
	reporting     renderReporting
	compatibility string
	skip          bool
	renderTimeout time.Duration
	candidates    []*api.ArprtScreen
	setRevision   string
}

func (s selectedDelivery) resolvedTarget() render.ResolvedTarget {
	return render.ResolvedTarget{Target: s.target, Output: s.output}
}

type deliveryImage struct {
	metadata      artifact.Metadata // Original rendering success, for freshness/reporting.
	issued        artifact.Metadata // Actual immutable image sent to the device.
	advanceCorner bool
	renderErr     error
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

func (s *Server) display(w http.ResponseWriter, r *http.Request, d *api.ArprtDevice, o Observation) {
	ctx := r.Context()
	key := sha256.Sum256([]byte(d.UID))
	gate := s.gates[int(key[0])%len(s.gates)]
	held := false
	lock := func() bool {
		select {
		case gate <- struct{}{}:
			held = true
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !lock() {
		reply(w, 503, 503)
		return
	}
	defer func() {
		if held {
			<-gate
		}
	}()
	corner, err := s.recoverCorner(ctx, d)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	observed, err := s.observeDisplay(ctx, d, o)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	if observed.targetErr != nil {
		s.outcome(ctx, d, observed.seen, deliveryOutcome{assignment: assignmentUnexamined, reason: "UnknownCapabilities", message: "No resolved render target"})
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

	if selected.skip && selected.replay == nil {
		headers, _ := Headers(r.Header)
		flight, leave, err := s.joinSkip(ctx, d, o, observed.seen, selected, headers["access-token"], gate)
		if err != nil {
			s.displayError(w, err)
			return
		}
		defer leave()
		<-gate
		held = false
		select {
		case <-ctx.Done():
			s.displayError(w, ctx.Err())
			return
		case <-flight.done:
		}
		if flight.err != nil {
			s.displayError(w, flight.err)
			return
		}
		if err := s.validateIssuance(ctx, d, flight.selected, headers["access-token"]); err != nil {
			s.displayError(w, err)
			return
		}
		writeDisplayResponse(w, flight.response)
		return
	}

	var image deliveryImage
	if screen.IsOnDemand(selected.screen.Spec) && selected.replay == nil {
		flight, leave, joinErr := s.joinDemand(ctx, d, o, selected)
		if joinErr != nil {
			reply(w, 503, 503)
			return
		}
		defer leave()
		// Single-screen waiters observe/authenticate independently. Existing
		// Hold/discovery turns retain their serialized transition semantics.
		if !selected.cycling {
			<-gate
			held = false
		}
		image, err = s.waitDemand(ctx, d, selected, flight)
		if !held && !lock() {
			reply(w, 503, 503)
			return
		}
		if err == nil && !selected.cycling {
			corner, err = s.recoverCorner(ctx, d)
		}
	} else {
		image, err = s.obtainImage(ctx, d, o, selected)
	}
	if err != nil {
		reply(w, 503, 503)
		return
	}
	headers, _ := Headers(r.Header)
	response, err := s.finishDisplay(ctx, d, o, observed.seen, selected, image, corner, headers["access-token"])
	if err != nil {
		s.displayError(w, err)
		return
	}
	writeDisplayResponse(w, response)
}

func writeDisplayResponse(w http.ResponseWriter, response DisplayResponse) {
	if response.Status == 202 {
		reply(w, 200, 202)
		return
	}
	jsonResponse(w, 200, response)
}

func (s *Server) displayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDisplayPending):
		reply(w, 200, 202)
	case errors.Is(err, errDisplayUnauthorized):
		reply(w, 401, 401)
	default:
		s.identityError(w, err)
	}
}

func (s *Server) finishDisplay(ctx context.Context, d *api.ArprtDevice, o Observation, seen metav1.Time, selected selectedDelivery, image deliveryImage, corner artifact.CornerRecord, token string) (DisplayResponse, error) {
	image, release, err := s.overlayImage(ctx, d, o, selected, image, corner)
	if err != nil {
		return DisplayResponse{}, err
	}
	defer release()
	var proposed *api.ScreenCycleStatus
	if image.metadata.ImageDigest != "" && selected.cycling && selected.replay == nil {
		proposed = &api.ScreenCycleStatus{CompatibilityFingerprint: selected.compatibility, Assignment: selected.fingerprint, ScreenUID: string(selected.screen.UID), InputRevision: selected.revision, Target: api.CycleTargetStatus{Width: selected.target.Width, Height: selected.target.Height, Palette: selected.target.Palette}, ImageDigest: image.issued.ImageDigest, Filename: image.issued.Filename, ServedAt: metav1.NewMicroTime(time.Now().UTC().Truncate(time.Microsecond))}
		if image.advanceCorner {
			pending := artifact.CornerPending{Next: corner.Current.Next(), Previous: d.Status.ScreenCycle.DeepCopy(), Proposed: proposed}
			if err := s.Repository.PrepareCorner(ctx, corner, pending); err != nil {
				return DisplayResponse{}, err
			}
			corner.Pending = &pending
		}
	}
	if err := s.validateIssuance(ctx, d, selected, token); err != nil {
		return DisplayResponse{}, err
	}

	// Screen reporting remains best effort, before issuance, even for pending
	// HTTP output. Job and replay preserve the separately owned render outcome.
	result := s.imageOutcome(ctx, d, selected, image)
	if result.state == deliveryPending {
		s.outcome(ctx, d, seen, result)
		return DisplayResponse{Status: 202}, nil
	}
	if err := s.Repository.Issue(ctx, string(d.UID), image.issued.ImageDigest, time.Now()); err != nil {
		return DisplayResponse{}, err
	}
	if result.state == deliveryStale {
		result.reason, result.message = "StaleImageAvailable", "Renderer failed; serving the previous compatible image"
		if selected.screen.Spec.Source.Job != nil {
			result.message = "Job output pending or failed; serving the previous compatible image"
		}
	}
	if selected.cycling && selected.replay == nil {
		if err := s.persistCycle(ctx, d, proposed); err != nil {
			return DisplayResponse{}, err
		}
	}
	if image.advanceCorner {
		if err := s.Repository.ResolveCorner(ctx, corner, true); err != nil {
			return DisplayResponse{}, err
		}
	}
	// Issuance and any new cycle turn must be durable before acknowledgement.
	s.outcome(ctx, d, seen, result)
	return DisplayResponse{Status: 0, ImageURL: s.deviceURL(d.Name) + "/images/" + image.issued.ImageDigest + ".png", Filename: image.issued.Filename, RefreshRate: s.Config.RefreshSeconds, ImageURLTimeout: 10}, nil
}

func (s *Server) selectDelivery(ctx context.Context, d *api.ArprtDevice, resolved render.ResolvedTarget) (selectedDelivery, error) {
	eligible, err := (assignment.Explicit{Reader: s.Reader}).Eligible(ctx, d)
	if err != nil {
		return selectedDelivery{}, err
	}
	assigned, replay := eligible.Select(d, time.Now().UTC(), time.Duration(s.Config.RefreshSeconds)*time.Second)
	revision, err := screen.InputRevision(assigned.Spec)
	if err != nil {
		return selectedDelivery{}, err
	}
	policy, err := screen.ProcessingPolicy(assigned.Spec)
	if err != nil {
		return selectedDelivery{}, err
	}
	compatibility, err := resolved.Fingerprint(policy)
	if err != nil {
		return selectedDelivery{}, err
	}
	if replay && !eligible.Skip && d.Status.ScreenCycle.CompatibilityFingerprint != compatibility {
		replay = false
	}
	if replay && d.Status.ScreenCycle.InputRevision != revision {
		replay = false
	}
	if assigned.Spec.Source.HTTP == nil && assigned.Spec.Source.Job == nil {
		return selectedDelivery{}, errUnsupportedScreen
	}
	selected := selectedDelivery{skip: eligible.Skip, renderTimeout: eligible.RenderTimeout, candidates: eligible.Candidates(assigned), compatibility: compatibility, screen: assigned, target: resolved.Target, output: resolved.Output, revision: revision, fingerprint: eligible.Fingerprint, cycling: eligible.Cycling, reporting: reportHTTP}
	if assigned.Spec.Source.Job != nil {
		selected.reporting = reportJob
	}
	if selected.skip {
		selected.setRevision, err = eligible.Revision()
		if err != nil {
			return selectedDelivery{}, err
		}
		selected.compatibility = skipCompatibility(selected.compatibility, selected.setRevision)
		if replay && d.Status.ScreenCycle.CompatibilityFingerprint != selected.compatibility {
			replay = false
		}
	}
	if replay {
		selected.replay = d.Status.ScreenCycle.DeepCopy()
		selected.reporting = reportReplay
	}
	return selected, nil
}

func (s *Server) obtainImage(ctx context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery) (deliveryImage, error) {
	assigned, target := selected.screen, selected.target
	var m artifact.Metadata
	var renderErr error
	if selected.replay != nil {
		last := selected.replay
		if err := imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
			_, err := s.Repository.Read(ctx, last.ImageDigest)
			return err
		}); err != nil {
			return deliveryImage{}, err
		}
		m = artifact.Metadata{ImageDigest: last.ImageDigest, Filename: last.Filename}
	} else if assigned.Spec.Source.Job != nil {
		// Polling only reads completed output. Scheduling belongs to reconciliation.
		renderErr = imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
			var err error
			m, err = s.Repository.Latest(ctx, artifact.Unit{Namespace: assigned.Namespace, ScreenUID: string(assigned.UID), DeviceUID: string(d.UID), Target: target, Output: selected.output})
			return err
		})
		if errors.Is(renderErr, artifact.ErrNotFound) {
			renderErr = errors.New("job image pending")
		}
		if m.ImageDigest != "" && (m.InputRevision != selected.revision || m.ScreenGeneration != assigned.Generation) {
			renderErr = errors.New("job update pending")
		}
		if c := meta.FindStatusCondition(d.Status.Conditions, "RenderReady"); !selected.cycling && c != nil && c.Status == metav1.ConditionFalse {
			renderErr = errors.New("job output pending or failed")
		}
		if interval, _ := screen.EffectiveInterval(assigned.Spec); selected.cycling && m.ImageDigest != "" && time.Since(m.RenderedAt) >= interval {
			renderErr = errors.New("job refresh pending")
		}
	} else if assigned.Spec.Source.HTTP.Method == "POST" {
		var snapshot render.Context
		// mutate returned the direct status snapshot, and this explicit overlay
		// keeps current request telemetry independent of informer propagation.
		snapshot, renderErr = render.Build(d.Name, target, devicestate.Observation(d.Status), render.Observation{Model: o.Model, FirmwareVersion: o.Firmware, Voltage: o.Voltage, Percent: o.Percent, RSSI: o.RSSI})
		if renderErr == nil {
			m, renderErr = s.POST.Image(ctx, assigned, d, snapshot)
		}
	} else {
		m, renderErr = s.GET.Image(ctx, assigned, selected.resolvedTarget())
	}
	return deliveryImage{metadata: m, renderErr: renderErr}, nil
}

// validateIssuance is the final authorization barrier for fresh, stale and
// replayed output. Publication gates in delivery do not replace these reads.
func (s *Server) validateIssuance(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery, token string) error {
	assigned := selected.screen
	// Recheck identity, assignment, definition and target after the asynchronous
	// network call. Never issue an image selected for a superseded assignment.
	var current api.ArprtDevice
	var currentScreen api.ArprtScreen
	if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current); err != nil {
		if apierrors.IsNotFound(err) {
			return errDisplayPending
		}
		return err
	}
	if current.UID != d.UID || !reflect.DeepEqual(current.Spec, d.Spec) || current.Generation != d.Generation || !current.DeletionTimestamp.IsZero() || devicestate.Resolved(&current) != selected.resolvedTarget() {
		return errDisplayPending
	}
	if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(assigned), &currentScreen); err != nil {
		if apierrors.IsNotFound(err) {
			return errDisplayPending
		}
		return err
	}
	if currentScreen.UID != assigned.UID || currentScreen.Generation != assigned.Generation || !reflect.DeepEqual(currentScreen.Spec, assigned.Spec) || !currentScreen.DeletionTimestamp.IsZero() {
		return errDisplayPending
	}
	// Membership alone misses playlist order and other assignment edits that
	// invalidate the selected turn, including replay.
	currentSet, err := (assignment.Explicit{Reader: s.Reader}).Eligible(ctx, &current)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, assignment.ErrEmpty) {
			return errDisplayPending
		}
		return err
	}
	if currentSet.Fingerprint != selected.fingerprint {
		return errDisplayPending
	}
	if selected.skip {
		revision, err := currentSet.Revision()
		if err != nil {
			return err
		}
		if revision != selected.setRevision {
			return errDisplayPending
		}
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

func (s *Server) persistCycle(ctx context.Context, d *api.ArprtDevice, proposed *api.ScreenCycleStatus) error {
	return s.mutate(ctx, d, func(current *api.ArprtDevice) {
		current.Status.ScreenCycle = proposed.DeepCopy()
	})
}
