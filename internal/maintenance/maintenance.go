package maintenance

import (
	"context"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/deliverycommit"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StorageMaintenance also runs when Job scheduling is disabled.
type StorageMaintenance struct {
	Reader     client.Reader
	Repository *artifact.PVC
	Namespace  string
	Refresh    time.Duration
}

func (m *StorageMaintenance) NeedLeaderElection() bool { return false }
func (m *StorageMaintenance) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		err := m.Collect(ctx, time.Now().UTC())
		outcome := "success"
		if err != nil && ctx.Err() == nil {
			outcome = "failure"
			observe.StorageErrors.WithLabelValues("maintenance").Inc()
			// Do not log filesystem paths or corrupt record contents.
			ctrl.LoggerFrom(ctx).Info("Artifact maintenance failed; retained data is protected", "reason", "StorageMaintenanceFailed")
		}
		observe.Maintenance.WithLabelValues(outcome).Inc()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (m *StorageMaintenance) Collect(ctx context.Context, now time.Time) error {
	if err := deliverycommit.RecoverAll(ctx, m.Repository, m.Reader, m.Namespace); err != nil {
		return err
	}
	refresh := m.Refresh
	if refresh <= 0 {
		refresh = 5 * time.Minute
	}
	var devices api.ArprtDeviceList
	if err := m.Reader.List(ctx, &devices, client.InNamespace(m.Namespace)); err != nil {
		return err
	}
	uids := map[string]bool{}
	units := map[artifact.Unit]bool{}
	counts := map[string]int{"never": 0, "recent": 0, "silent": 0}
	maxAge := 0.0
	for _, d := range devices.Items {
		if !d.DeletionTimestamp.IsZero() {
			continue
		}
		uids[string(d.UID)] = true
		if d.Status.LastSeenAt == nil {
			counts["never"]++
		} else {
			age := max(0, now.Sub(d.Status.LastSeenAt.Time).Seconds())
			maxAge = max(maxAge, age)
			if age > max(3*refresh, 2*time.Minute).Seconds() {
				counts["silent"]++
			} else {
				counts["recent"]++
			}
		}
		resolved := devicestate.Resolved(&d)
		if resolved.Validate() != nil {
			if err := m.syncPairs(ctx, &d, now); err != nil {
				return err
			}
			continue
		}
		eligible, err := (assignment.Explicit{Reader: m.Reader}).Eligible(ctx, &d)
		if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, assignment.ErrEmpty) {
			return err
		}
		if err := m.syncPairs(ctx, &d, now); err != nil {
			return err
		}
		for _, entry := range eligible.Entries {
			s := entry.Screen
			u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), Target: resolved.Target}
			if s.Spec.Source.Job != nil || (s.Spec.Source.HTTP != nil && s.Spec.Source.HTTP.Method == "POST") {
				u.DeviceUID = string(d.UID)
			}
			units[u] = true
			u.Output = resolved.Output
			units[u] = true
		}
		// A replayed turn may still refer to an older successful revision.
		// Issuance protection independently keeps its bytes for at least 24h.
	}
	for state, count := range counts {
		observe.Devices.WithLabelValues(state).Set(float64(count))
	}
	observe.LastSeenAge.Set(maxAge)
	if _, _, err := m.Repository.Usage(ctx); err != nil {
		return err
	}
	return imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		return m.Repository.Collect(ctx, func(u artifact.Unit) bool { return units[u] }, uids, now)
	})
}

// syncPairs covers resets and assignment departure even with Job execution
// disabled or after scheduler records have been removed. CAS fences stale scans.
func (m *StorageMaintenance) syncPairs(ctx context.Context, d *api.ArprtDevice, now time.Time) error {
	r, err := m.Repository.Corner(ctx, string(d.UID))
	if err != nil {
		return err
	}
	for key := range r.Pairs {
		// Take the CAS snapshot before fresh API reads. An older List snapshot must
		// not detach/reset work published while maintenance was scanning devices.
		r, err = m.Repository.Corner(ctx, string(d.UID))
		if err != nil {
			return err
		}
		if r.Pending != nil {
			return artifact.ErrPending
		}
		var current api.ArprtDevice
		if err := m.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current); err != nil {
			return err
		}
		if current.UID != d.UID || !current.DeletionTimestamp.IsZero() {
			return artifact.ErrObsolete
		}
		target := devicestate.Resolved(&current)
		var assigned *api.ArprtScreen
		if target.Validate() == nil {
			eligible, err := (assignment.Explicit{Reader: m.Reader}).Eligible(ctx, &current)
			if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, assignment.ErrEmpty) {
				return err
			}
			for _, entry := range eligible.Entries {
				if artifact.PairKey(current.Namespace, string(entry.Screen.UID)) == key {
					assigned = entry.Screen
					break
				}
			}
		}
		if assigned != nil {
			revision, err := screen.InputRevision(assigned.Spec)
			if err != nil {
				return err
			}
			unit := artifact.Unit{Namespace: current.Namespace, DeviceUID: string(current.UID), ScreenUID: string(assigned.UID), Target: target.Target, Output: target.Output}
			r, err = m.Repository.ConfigurePair(ctx, r, artifact.DefinePair(unit, revision))
			if err != nil {
				return err
			}
			if assigned.Spec.Source.Job != nil && assigned.Spec.Source.Job.State != nil {
				continue
			}
		}
		if err := m.Repository.DetachPair(ctx, r, key, now); err != nil {
			return err
		}
	}
	return nil
}
