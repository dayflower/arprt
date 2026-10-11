package screen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
)

// EffectiveInterval resolves conditional defaults without defaulting POST.
func EffectiveInterval(spec api.TrmnlScreenSpec) (time.Duration, error) {
	if spec.Source.HTTP != nil && spec.Source.HTTP.Method == "POST" {
		return 0, nil
	}
	if spec.RefreshInterval != "" {
		return time.ParseDuration(spec.RefreshInterval)
	}
	if spec.Source.Job != nil {
		return 30 * time.Minute, nil
	}
	return 5 * time.Minute, nil
}

// InputRevision excludes metadata, status and credentials. Kubernetes admission
// validates the input; normalizing defaults gives explicit/omitted defaults the
// same revision. Transport and collection semantic changes bump these versions.
func InputRevision(spec api.TrmnlScreenSpec) (string, error) {
	s := spec.DeepCopy()
	s.Overlay = EffectiveOverlay(s.Overlay)
	if s.Source.HTTP != nil && s.Source.HTTP.Method == "" {
		s.Source.HTTP.Method = "GET"
	}
	if s.Source.Job != nil {
		if s.Source.Job.ContextPath == "" {
			s.Source.Job.ContextPath = "/trmnl/context.json"
		}
		if s.Source.Job.ImagePath == "" {
			s.Source.Job.ImagePath = "/trmnl/output.png"
		}
	}
	interval, err := EffectiveInterval(*s)
	if err != nil {
		return "", err
	}
	s.RefreshInterval = interval.String()
	if interval == 0 {
		s.RefreshInterval = ""
	}
	b, err := json.Marshal(struct {
		Spec             *api.TrmnlScreenSpec `json:"spec"`
		ContextVersion   string               `json:"contextVersion"`
		CollectorVersion string               `json:"collectorVersion"`
	}{s, "v1", "v1"})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// EffectiveOverlay canonicalizes display defaults without changing old Screens'
// revisions. Disabled settings have no rendering effect; items always follow the
// fixed timestamp, battery, Wi-Fi order. Callers receive independent storage.
func EffectiveOverlay(in *api.ScreenOverlay) *api.ScreenOverlay {
	if in == nil || !in.Enabled {
		return nil
	}
	out := in.DeepCopy()
	items := []api.OverlayItem{}
	for _, item := range []api.OverlayItem{"timestamp", "battery", "wifi"} {
		selected := in.Items == nil
		if in.Items != nil {
			for _, value := range *in.Items {
				selected = selected || value == item
			}
		}
		if selected {
			items = append(items, item)
		}
	}
	out.Items = &items
	if out.Margin != nil && out.Margin.Horizontal == 0 && out.Margin.Vertical == 0 {
		out.Margin = nil
	}
	if out.CycleCorners {
		out.Position = ""
	} else if out.Position == "" {
		out.Position = "top-right"
	}
	return out
}
