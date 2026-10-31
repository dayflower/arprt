package dashboard

import (
	"context"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These DTOs whitelist display fields rather than serializing Kubernetes objects
// or internal records containing identity, credential references or storage data.
type Device struct {
	Name          string             `json:"name"`
	Namespace     string             `json:"namespace"`
	Model         string             `json:"model,omitempty"`
	Firmware      string             `json:"firmware,omitempty"`
	Display       *Display           `json:"display,omitempty"`
	LastSeenAt    *metav1.Time       `json:"lastSeenAt,omitempty"`
	Battery       *api.BatteryStatus `json:"battery,omitempty"`
	Network       *api.NetworkStatus `json:"network,omitempty"`
	Assignment    Assignment         `json:"assignment"`
	Communication Signal             `json:"communication"`
	Delivery      Signal             `json:"delivery"`
	Rendering     Signal             `json:"rendering"`
	Problems      []Problem          `json:"problems"`
	RecordStatus  string             `json:"recordStatus"`
	Image         *RecordedImage     `json:"image,omitempty"`
}

type Display struct {
	Width    int32  `json:"width"`
	Height   int32  `json:"height"`
	Palette  string `json:"palette"`
	Rotation int32  `json:"rotation"`
}

type Signal struct {
	State string `json:"state"`
	Scope string `json:"scope"`
}

type Problem struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Scope   string `json:"scope"`
	Screen  string `json:"screen,omitempty"`
}

type Assignment struct {
	Mode          string           `json:"mode"`
	Name          string           `json:"name,omitempty"`
	FailurePolicy string           `json:"failurePolicy,omitempty"`
	Screens       []AssignedScreen `json:"screens"`
}

type AssignedScreen struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type RecordedImage struct {
	ScreenName       string     `json:"screenName"`
	Digest           string     `json:"digest"`
	GeneratedAt      *time.Time `json:"generatedAt,omitempty"`
	IssuedAt         time.Time  `json:"issuedAt"`
	Result           string     `json:"result"`
	PreviewState     string     `json:"previewState"`
	PreviewURL       string     `json:"previewURL,omitempty"`
	Freshness        string     `json:"freshness"`
	LatestGuaranteed bool       `json:"latestGuaranteed"`
}

func currentCondition(d *api.ArprtDevice, kind string) *metav1.Condition {
	c := meta.FindStatusCondition(d.Status.Conditions, kind)
	if c == nil || c.ObservedGeneration != d.Generation {
		return nil
	}
	return c
}

func (s *Server) device(ctx context.Context, reader client.Reader, d *api.ArprtDevice, now time.Time) (Device, error) {
	row := Device{Name: d.Name, Namespace: d.Namespace, LastSeenAt: d.Status.LastSeenAt, Battery: d.Status.Battery, Network: d.Status.Network,
		Communication: Signal{State: "Never seen", Scope: "device"}, Delivery: Signal{State: "Unknown", Scope: "device"},
		Rendering: Signal{State: "Unknown", Scope: "recorded-delivery"}, Problems: []Problem{}, RecordStatus: "missing"}
	if d.Status.Device != nil {
		row.Model, row.Firmware = d.Status.Device.Model, d.Status.Device.FirmwareVersion
	}
	target, display, targetErr := devicestate.Resolve(d)
	if targetErr == nil {
		row.Display = &Display{Width: display.Width, Height: display.Height, Palette: display.Palette, Rotation: d.Spec.Rotation}
	}
	if seen := d.Status.LastSeenAt; seen != nil {
		state := "Ready"
		if seen.After(now) {
			state = "Unknown"
		} else if now.Sub(seen.Time) > max(3*time.Duration(s.config.RefreshSeconds)*time.Second, 2*time.Minute) {
			state = "Silent"
			row.Problems = append(row.Problems, Problem{Reason: "DeviceSilent", Message: "No display request within three polling intervals; check power and network", Scope: "communication"})
		}
		row.Communication.State = state
	}
	set, err := (assignment.Explicit{Reader: reader}).Eligible(ctx, d)
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, assignment.ErrEmpty) {
		return Device{}, err
	}
	row.Assignment, err = describeAssignment(ctx, reader, d, set)
	if err != nil {
		return Device{}, err
	}
	if set.Fingerprint == "" {
		row.Problems = append(row.Problems, Problem{Reason: "AssignmentUnavailable", Message: "Assignment has no eligible screens", Scope: "assignment"})
	}
	for _, kind := range []struct{ name, reason, message string }{
		{"AuthenticationReady", "AuthenticationUnavailable", "Device authentication is unavailable"},
		{"TargetKnown", "TargetUnknown", "Display capabilities are unknown"},
		{"AssignmentReady", "AssignmentUnavailable", "The device reports an unresolved assignment"},
		{"TelemetryValid", "InvalidTelemetry", "Some optional telemetry was invalid; stored fields may have different observation times"},
	} {
		if c := currentCondition(d, kind.name); c != nil && c.Status == metav1.ConditionFalse {
			row.Problems = append(row.Problems, Problem{Reason: kind.reason, Message: kind.message, Scope: "device"})
		}
	}
	if c := currentCondition(d, "Ready"); c != nil {
		switch c.Status {
		case metav1.ConditionTrue:
			row.Delivery.State = "Ready"
			if c.Reason == "StaleImageAvailable" {
				row.Delivery.State = "Serving stale image"
			}
		case metav1.ConditionFalse:
			row.Delivery.State = "Image pending"
		}
	}
	if targetErr != nil || set.Fingerprint == "" {
		row.Delivery.State = "Unknown"
	}
	reportedScreens := map[string]bool{}
	for _, entry := range set.Entries {
		sc := entry.Screen
		if reportedScreens[string(sc.UID)] {
			continue
		}
		reportedScreens[string(sc.UID)] = true
		// Screen Ready is a separate report, never a per-device/pair aggregate.
		if c := meta.FindStatusCondition(sc.Status.Conditions, "Ready"); c != nil && c.ObservedGeneration == sc.Generation && c.Status == metav1.ConditionFalse {
			row.Problems = append(row.Problems, Problem{Reason: "ScreenNotReady", Message: "A related screen reports pending or failed rendering", Scope: "screen", Screen: sc.Name})
		}
	}
	if d.Spec.ScreenRef != nil {
		if c := currentCondition(d, "RenderReady"); c != nil && c.Status == metav1.ConditionFalse {
			row.Rendering = Signal{State: "Render failed", Scope: "device-report"}
			problem := Problem{Reason: "RenderFailed", Message: "The device reports failed rendering", Scope: "device-report"}
			if c.Reason == "JobPending" || c.Reason == "JobSchedulingDisabled" {
				row.Rendering.State = "Image pending"
				problem.Reason, problem.Message = "RenderPending", "Job output is pending or execution is disabled"
			}
			row.Problems = append(row.Problems, problem)
		}
	}
	record, err := s.store.Delivery(ctx, string(d.UID))
	if errors.Is(err, artifact.ErrNotFound) {
		return row, nil
	}
	if err != nil {
		// Missing legacy observations and corrupt/unreadable observations are
		// distinct. Neither supplies inferred provenance or a preview URL.
		row.RecordStatus = "unavailable"
		row.Problems = append(row.Problems, Problem{Reason: "DeliveryRecordUnavailable", Message: "The delivery observation could not be read", Scope: "record"})
		return row, nil
	}
	row.RecordStatus = "last-recorded"
	row.Image = &RecordedImage{ScreenName: record.ScreenName, Digest: record.ImageDigest, GeneratedAt: record.RenderedAt, IssuedAt: record.IssuedAt,
		Result: record.Result, Freshness: "last-recorded", PreviewState: "Preview unavailable"}
	available, err := s.store.DeliveryPreviewAvailable(ctx, string(d.UID), record.ImageDigest)
	if err != nil && !errors.Is(err, artifact.ErrNotFound) && !errors.Is(err, artifact.ErrCorrupt) {
		return Device{}, err
	}
	if available {
		row.Image.PreviewState = "available"
		row.Image.PreviewURL = "/api/devices/" + d.Name + "/preview/" + record.ImageDigest + ".png"
	}
	matching := record.DeviceUID == string(d.UID) && record.DeviceGeneration == d.Generation && record.Assignment == set.Fingerprint && record.Target == target
	matchedScreen := false
	for _, entry := range set.Entries {
		sc := entry.Screen
		if string(sc.UID) == record.ScreenUID && sc.Generation == record.ScreenGeneration && (record.Cycle == nil || entry.Index == record.Cycle.EntryIndex) {
			rev, err := screen.InputRevision(sc.Spec)
			matchedScreen = err == nil && rev == record.InputRevision
		}
	}
	if d.Spec.ScreenRef == nil {
		matching = matching && artifact.EqualCycle(record.Cycle, d.Status.ScreenCycle) && record.Cycle != nil
	}
	if matching && matchedScreen && row.Rendering.State == "Unknown" {
		switch record.Rendering {
		case "ready":
			row.Rendering.State = "Ready"
		case "failed":
			row.Rendering.State = "Render failed"
		case "pending":
			row.Rendering.State = "Image pending"
		}
		if record.Result == "stale" {
			row.Problems = append(row.Problems, Problem{Reason: "RecordedStaleImage", Message: "The recorded delivery used a compatible previous image", Scope: "recorded-delivery", Screen: record.ScreenName})
		}
	}
	return row, nil
}

func describeAssignment(ctx context.Context, reader client.Reader, d *api.ArprtDevice, set assignment.Set) (Assignment, error) {
	a := Assignment{Mode: "automatic", Screens: []AssignedScreen{}}
	var refs []api.LocalReference
	switch {
	case d.Spec.ScreenRef != nil:
		a.Mode, a.Name, refs = "single", d.Spec.ScreenRef.Name, []api.LocalReference{*d.Spec.ScreenRef}
	case d.Spec.PlaylistRef != nil:
		a.Mode, a.Name, a.FailurePolicy = "playlist", d.Spec.PlaylistRef.Name, "Hold"
		var p api.ArprtPlaylist
		if err := reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: a.Name}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return a, nil
			}
			return Assignment{}, err
		}
		refs = p.Spec.Screens
		if p.Spec.FailurePolicy == "Skip" {
			a.FailurePolicy = "Skip"
		}
	default:
		for _, entry := range set.Entries {
			sc := entry.Screen
			a.Screens = append(a.Screens, AssignedScreen{Name: sc.Name, Available: true})
		}
		return a, nil
	}
	for _, ref := range refs {
		available := false
		for _, entry := range set.Entries {
			sc := entry.Screen
			available = available || sc.Name == ref.Name
		}
		a.Screens = append(a.Screens, AssignedScreen{Name: ref.Name, Available: available})
	}
	return a, nil
}
