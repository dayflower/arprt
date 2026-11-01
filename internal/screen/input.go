package screen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/imageprocessing"
)

// EffectiveInterval resolves conditional defaults without defaulting POST.
func EffectiveInterval(spec api.ArprtScreenSpec) (time.Duration, error) {
	if spec.Source.HTTP != nil && spec.Source.HTTP.Method == "POST" || IsOnDemand(spec) {
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
func InputRevision(spec api.ArprtScreenSpec) (string, error) {
	s := spec.DeepCopy()
	policy, err := ProcessingPolicy(spec)
	if err != nil {
		return "", err
	}
	s.ImageProcessing = &api.ImageProcessing{ResizeMode: string(policy.Fit), Dithering: string(policy.Dither)}
	processingRevision, err := policy.Revision()
	if err != nil {
		return "", err
	}
	s.Overlay = EffectiveOverlay(s.Overlay)
	if s.Source.HTTP != nil && s.Source.HTTP.Method == "" {
		s.Source.HTTP.Method = "GET"
	}
	if s.Source.Job != nil {
		// Keep the pre-OnDemand scheduled serialization byte-for-byte stable.
		if !IsOnDemand(*s) {
			s.Source.Job.ExecutionMode = ""
		} else if s.Source.Job.Timeout == "" {
			s.Source.Job.Timeout = "5s"
		}
		if s.Source.Job.ContextPath == "" {
			s.Source.Job.ContextPath = "/arprt/context.json"
		}
		if s.Source.Job.ImagePath == "" {
			s.Source.Job.ImagePath = "/arprt/output.png"
		}
		job, err := NormalizeJob(*s.Source.Job)
		if err != nil {
			return "", err
		}
		s.Source.Job = &job
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
		Spec               *api.ArprtScreenSpec `json:"spec"`
		ContextVersion     string               `json:"contextVersion"`
		CollectorVersion   string               `json:"collectorVersion"`
		ProcessingRevision string               `json:"processingRevision"`
	}{s, "v1", "v2", processingRevision})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// IsOnDemand resolves the opt-in Job execution policy.
func IsOnDemand(spec api.ArprtScreenSpec) bool {
	return spec.Source.Job != nil && spec.Source.Job.ExecutionMode == "OnDemand"
}

// JobTimeout resolves the conditional request timeout; admission validates it.
func JobTimeout(spec api.ArprtScreenSpec) (time.Duration, error) {
	if !IsOnDemand(spec) {
		return 0, nil
	}
	if spec.Source.Job.Timeout == "" {
		return 5 * time.Second, nil
	}
	return time.ParseDuration(spec.Source.Job.Timeout)
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
	if out.Margin != nil && *out.Margin == (api.OverlayMargin{}) {
		out.Margin = nil
	}
	if out.CycleCorners {
		out.Position = ""
	} else if out.Position == "" {
		out.Position = "top-right"
	}
	return out
}

// ProcessingPolicy resolves omitted API defaults for every renderer source.
func ProcessingPolicy(spec api.ArprtScreenSpec) (imageprocessing.Policy, error) {
	p := imageprocessing.Policy{}
	if spec.ImageProcessing != nil {
		p.Fit = imageprocessing.Fit(spec.ImageProcessing.ResizeMode)
		p.Dither = imageprocessing.Dither(spec.ImageProcessing.Dithering)
	}
	return p.Normalize()
}

// NormalizeJob applies file defaults and validates the single writable output mount.
// It copies the source so callers cannot mutate another attempt's configuration.
func NormalizeJob(source api.JobSource) (api.JobSource, error) {
	source = *source.DeepCopy()
	if source.ContextPath == "" {
		source.ContextPath = "/arprt/context.json"
	}
	if source.ImagePath == "" {
		source.ImagePath = "/arprt/output.png"
	}
	paths := []string{source.ContextPath, source.ImagePath}
	if source.State != nil {
		if source.State.ResultPath == "" {
			source.State.ResultPath = path.Join(path.Dir(source.ImagePath), "result.json")
		}
		paths = append(paths, source.State.ResultPath)
		if path.Dir(source.State.ResultPath) != path.Dir(source.ImagePath) {
			return source, errors.New("result and image must have the same parent")
		}
	}
	for i, p := range paths {
		if utf8.RuneCountInString(p) > 512 || !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
			return source, errors.New("invalid Job file path")
		}
		if p == "/var/lib/arprt" || strings.HasPrefix(p, "/var/lib/arprt/") {
			return source, errors.New("reserved artifact path")
		}
		for _, other := range paths[:i] {
			if p == other || strings.HasPrefix(p, other+"/") || strings.HasPrefix(other, p+"/") {
				return source, errors.New("overlapping Job files")
			}
		}
	}
	parent := path.Dir(source.ImagePath)
	for _, reserved := range []string{"/", "/proc", "/sys", "/dev", "/etc", "/bin", "/sbin", "/usr", "/var/lib/arprt", "/var/run/secrets", "/run/secrets"} {
		if parent == reserved || reserved != "/" && (strings.HasPrefix(parent, reserved+"/") || strings.HasPrefix(reserved, parent+"/")) {
			return source, errors.New("output mount overlaps a reserved directory")
		}
	}
	if source.Renderer.Image == "" {
		return source, errors.New("renderer image is required")
	}
	return source, nil
}
