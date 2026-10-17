package maintenance

import (
	"context"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
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
		if d.Status.Display == nil {
			continue
		}
		eligible, err := (assignment.Explicit{Reader: m.Reader}).Eligible(ctx, &d)
		if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, assignment.ErrEmpty) {
			return err
		}
		for _, s := range eligible.Screens {
			u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), Target: devicestate.Target(&d)}
			if s.Spec.Source.Job != nil || (s.Spec.Source.HTTP != nil && s.Spec.Source.HTTP.Method == "POST") {
				u.DeviceUID = string(d.UID)
			}
			units[u] = true
			u.Output = render.CurrentTarget(u.Target).Output
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
