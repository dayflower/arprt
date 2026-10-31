package byos

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// demandFlight lasts through the participating responses' finalization. Each
// waiter keeps its own authentication, observation and issuance barriers.
type demandFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	metadata artifact.Metadata
	err      error
}

func (s *Server) joinDemand(ctx context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery) (*demandFlight, func(), error) {
	u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(selected.screen.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output}
	identity, err := json.Marshal(struct {
		Unit             artifact.Unit
		DeviceSpec       api.ArprtDeviceSpec
		DeviceGeneration int64
		ScreenGeneration int64
		Revision         string
		Assignment       string
		EntryIndex       int32
		Previous         *api.ScreenCycleStatus
	}{u, d.Spec, d.Generation, selected.screen.Generation, selected.revision, selected.fingerprint, selected.entryIndex, d.Status.ScreenCycle})
	if err != nil {
		return nil, nil, err
	}
	key := string(identity)
	s.demandMu.Lock()
	defer s.demandMu.Unlock()
	f := s.demands[key]
	if f == nil {
		snapshot, err := render.Build(d.Name, selected.target, devicestate.Observation(d.Status), render.Observation{Model: o.Model, FirmwareVersion: o.Firmware, Voltage: o.Voltage, Percent: o.Percent, RSSI: o.RSSI})
		if err != nil {
			return nil, nil, err
		}
		timeout, err := screen.JobTimeout(selected.screen.Spec)
		if err != nil {
			return nil, nil, err
		}
		started, ok := ctx.Value(admissionTimeKey{}).(time.Time)
		if !ok {
			return nil, nil, errors.New("missing request admission time")
		}
		deadline := started.Add(10 * time.Second)
		if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
			deadline = parent
		}
		work, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		f = &demandFlight{done: make(chan struct{}), cancel: cancel}
		if s.demands == nil {
			s.demands = map[string]*demandFlight{}
		}
		s.demands[key] = f
		req := jobrender.ExecutionRequest{Device: client.ObjectKeyFromObject(d), Screen: selected.screen.Name, Unit: u, Revision: selected.revision, StartedAt: started, RenderDeadline: deadline, Timeout: timeout, Snapshot: &snapshot, DeviceGeneration: d.Generation, Assignment: selected.fingerprint}
		go func() {
			defer cancel()
			switch {
			case work.Err() != nil:
				f.err = &jobrender.ExecutionError{Reason: "JobTimedOut"}
			case s.Jobs == nil:
				f.err = &jobrender.ExecutionError{Reason: "JobSchedulingDisabled"}
			default:
				f.metadata, f.err = s.Jobs.Execute(work, req)
			}
			close(f.done)
		}()
	}
	f.waiters++
	return f, func() {
		s.demandMu.Lock()
		defer s.demandMu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			delete(s.demands, key)
		}
	}, nil
}

func (s *Server) waitDemand(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery, f *demandFlight) (deliveryImage, error) {
	select {
	case <-ctx.Done():
		return deliveryImage{}, ctx.Err()
	case <-f.done:
	}
	if err := ctx.Err(); err != nil {
		return deliveryImage{}, err
	}
	if f.err == nil {
		return deliveryImage{metadata: f.metadata}, nil
	}
	var candidate *jobrender.ExecutionError
	if !errors.As(f.err, &candidate) {
		return deliveryImage{}, f.err
	}
	// Candidate failure permits exact-target stale fallback, even across input
	// revisions. Infrastructure failures must retain their existing 503 outcome.
	var m artifact.Metadata
	err := imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		var err error
		m, err = s.Repository.Latest(ctx, artifact.Unit{Namespace: d.Namespace, ScreenUID: string(selected.screen.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output})
		return err
	})
	if err != nil && !errors.Is(err, artifact.ErrNotFound) {
		return deliveryImage{}, err
	}
	return deliveryImage{metadata: m, renderErr: f.err}, nil
}
