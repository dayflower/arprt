package byos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/delivery"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// skipFlight owns exploration, issuance and the successful turn. Participating
// requests observe independently and authorize again before their own response.
type skipFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	selected selectedDelivery
	response DisplayResponse
	err      error
}

func (s *Server) joinSkip(ctx context.Context, d *api.ArprtDevice, o Observation, seen metav1.Time, selected selectedDelivery, token string, gate chan struct{}) (*skipFlight, func(), error) {
	identity, err := json.Marshal(struct {
		DeviceUID  string
		Generation int64
		Spec       api.ArprtDeviceSpec
		Target     render.ResolvedTarget
		Revision   string
		EntryIndex int32
		Previous   *api.ScreenCycleStatus
	}{string(d.UID), d.Generation, d.Spec, selected.resolvedTarget(), selected.setRevision, selected.entryIndex, d.Status.ScreenCycle})
	if err != nil {
		return nil, nil, err
	}
	key := string(identity)
	s.demandMu.Lock()
	defer s.demandMu.Unlock()
	f := s.skips[key]
	if f == nil {
		started, ok := ctx.Value(admissionTimeKey{}).(time.Time)
		if !ok {
			return nil, nil, errors.New("missing request admission time")
		}
		// Each response obeys its own parent deadline. Shared work keeps the
		// original admission bounds while peers remain; a short first waiter
		// must not impose its private waiting deadline on another response.
		work, cancel := context.WithDeadline(context.WithoutCancel(ctx), started.Add(13*time.Second))
		f = &skipFlight{done: make(chan struct{}), cancel: cancel, selected: selected}
		if s.skips == nil {
			s.skips = map[string]*skipFlight{}
		}
		s.skips[key] = f
		device := d.DeepCopy()
		go func() {
			defer cancel()
			defer close(f.done)
			renderCtx, stop := context.WithDeadline(work, started.Add(selected.renderTimeout))
			image, chosen, err := s.exploreSkip(renderCtx, work, device, o, selected, token, started)
			stop()
			f.selected = chosen
			if err != nil {
				f.err = err
				return
			}
			// Rendering never holds the device gate. Finalization is serialized with
			// observation and corner recovery, and occurs only once for all waiters.
			select {
			case gate <- struct{}{}:
			case <-work.Done():
				f.err = work.Err()
				return
			}
			defer func() { <-gate }()
			// A joining waiter may have observed newer telemetry while rendering.
			// Finalize against the current durable turn and observation timestamp,
			// preserving the original render/overlay observation snapshot.
			var current api.ArprtDevice
			if err := s.Reader.Get(work, client.ObjectKeyFromObject(device), &current); err != nil {
				f.err = err
				return
			}
			// A different turn may have committed while this operation rendered.
			// Never let an older exploration overwrite that successful position.
			if current.UID != device.UID || !artifact.EqualCycle(current.Status.ScreenCycle, device.Status.ScreenCycle) {
				f.err = errDisplayPending
				return
			}
			device.Status = current.Status
			if current.Status.LastSeenAt != nil {
				seen = *current.Status.LastSeenAt
			}
			corner, err := s.recoverCorner(work, device)
			if err == nil {
				f.response, err = s.finishDisplay(work, device, o, seen, chosen, image, corner, token)
			}
			f.err = err
		}()
	} else {
		observe.PlaylistAttempts.WithLabelValues("joined").Inc()
	}
	f.waiters++
	return f, func() {
		s.demandMu.Lock()
		defer s.demandMu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			delete(s.skips, key)
		}
	}, nil
}

func (s *Server) exploreSkip(ctx, request context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery, token string, started time.Time) (deliveryImage, selectedDelivery, error) {
	for _, entry := range selected.candidates {
		candidate := entry.Screen
		// Revalidate the complete candidate snapshot even after a failed attempt.
		// A membership/definition/auth change terminates this exploration.
		if err := s.validateIssuance(request, d, selected, token); err != nil {
			return deliveryImage{}, selected, err
		}
		if renderExpired(ctx) {
			observe.PlaylistAttempts.WithLabelValues("budget_exhausted").Inc()
			break
		}
		selected.screen = candidate
		selected.entryIndex = entry.Index
		var err error
		selected.revision, err = screen.InputRevision(candidate.Spec)
		if err != nil {
			return deliveryImage{}, selected, err
		}
		policy, err := screen.ProcessingPolicy(candidate.Spec)
		if err != nil {
			return deliveryImage{}, selected, err
		}
		selected.compatibility, err = selected.resolvedTarget().Fingerprint(policy)
		if err != nil {
			return deliveryImage{}, selected, err
		}
		selected.compatibility = skipCompatibility(selected.compatibility, selected.setRevision)
		selected.reporting = reportHTTP
		if candidate.Spec.Source.Job != nil {
			selected.reporting = reportJob
		}
		image, err := s.skipCandidate(ctx, d, o, selected, started)
		if err != nil {
			return deliveryImage{}, selected, err
		}
		// A late shared GET success cannot escape the requesting playlist budget.
		if renderExpired(ctx) {
			observe.PlaylistAttempts.WithLabelValues("budget_exhausted").Inc()
			break
		}
		if image.metadata.ImageDigest != "" && image.renderErr == nil {
			observe.PlaylistAttempts.WithLabelValues("success").Inc()
			return image, selected, nil
		}
		s.imageOutcome(request, d, selected, image)
		observe.PlaylistAttempts.WithLabelValues("skipped").Inc()
	}
	return deliveryImage{renderErr: errors.New("playlist has no fresh candidate")}, selected, nil
}

func (s *Server) skipCandidate(ctx context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery, started time.Time) (deliveryImage, error) {
	sc := selected.screen
	if sc.Spec.Source.Job != nil && s.Jobs == nil {
		return deliveryImage{renderErr: &jobrender.ExecutionError{Reason: "JobSchedulingDisabled"}}, nil
	}
	var m artifact.Metadata
	var err error
	switch {
	case screen.IsOnDemand(sc.Spec):
		timeout, parseErr := screen.JobTimeout(sc.Spec)
		if parseErr != nil {
			return deliveryImage{}, parseErr
		}
		snapshot, buildErr := render.Build(d.Name, selected.target, devicestate.Observation(d.Status), render.Observation{Model: o.Model, FirmwareVersion: o.Firmware, Voltage: o.Voltage, Percent: o.Percent, RSSI: o.RSSI})
		if buildErr != nil {
			return deliveryImage{}, buildErr
		}
		deadline, _ := ctx.Deadline()
		m, err = s.Jobs.Execute(ctx, jobrender.ExecutionRequest{Device: client.ObjectKeyFromObject(d), Screen: sc.Name, Unit: artifact.Unit{Namespace: d.Namespace, ScreenUID: string(sc.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output}, Revision: selected.revision, StartedAt: started, RenderDeadline: deadline, Timeout: timeout, Snapshot: &snapshot, DeviceGeneration: d.Generation, Assignment: selected.fingerprint})
	case sc.Spec.Source.Job != nil:
		err = imageprocessing.RuntimeAdmission.Do(ctx, func(work context.Context) error {
			var err error
			m, err = s.Repository.Latest(work, artifact.Unit{Namespace: d.Namespace, ScreenUID: string(sc.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output})
			return err
		})
		if errors.Is(err, artifact.ErrNotFound) {
			return deliveryImage{renderErr: err}, nil
		}
		if err == nil {
			interval, parseErr := screen.EffectiveInterval(sc.Spec)
			if parseErr != nil {
				return deliveryImage{}, parseErr
			}
			if m.ImageDigest == "" || m.InputRevision != selected.revision || m.ScreenGeneration != sc.Generation || time.Since(m.RenderedAt) >= interval {
				return deliveryImage{renderErr: errors.New("scheduled Job has no fresh image")}, nil
			}
		}
	case sc.Spec.Source.HTTP != nil && sc.Spec.Source.HTTP.Method != "POST":
		deadline, _ := ctx.Deadline()
		m, err = s.GET.ImageBefore(ctx, sc, selected.resolvedTarget(), deadline)
	default:
		image, obtainErr := s.obtainImage(ctx, d, o, selected)
		if obtainErr != nil {
			return deliveryImage{}, obtainErr
		}
		m, err = image.metadata, image.renderErr
	}
	if err != nil {
		if errors.Is(err, artifact.ErrObsolete) {
			return deliveryImage{}, errDisplayPending
		}
		var job *jobrender.ExecutionError
		var http *delivery.CandidateError
		if errors.As(err, &job) && job.Reason == "ObsoleteAttempt" {
			return deliveryImage{}, errDisplayPending
		}
		if errors.As(err, &job) || errors.As(err, &http) || (ctx.Err() != nil && errors.Is(err, ctx.Err())) {
			return deliveryImage{renderErr: err}, nil
		}
		return deliveryImage{}, err
	}
	return deliveryImage{metadata: m}, nil
}

// Deadline timers may be scheduled just after wall-clock expiry. Check the
// absolute cutoff as well before starting or accepting another candidate.
func renderExpired(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

func skipCompatibility(output, revision string) string {
	digest := sha256.Sum256([]byte(output + "/" + revision))
	return hex.EncodeToString(digest[:])
}
