package screen

import (
	"encoding/json"
	api "github.com/dayflower/arprt/api/v1alpha1"
	"testing"
	"time"
)

func TestDefaultsAndRevision(t *testing.T) {
	get := api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}
	job := api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}}}}
	post := api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render", Method: "POST"}}}
	for _, tt := range []struct {
		s    api.ArprtScreenSpec
		want time.Duration
	}{{get, 5 * time.Minute}, {job, 30 * time.Minute}, {post, 0}} {
		d, err := EffectiveInterval(tt.s)
		if err != nil || d != tt.want {
			t.Fatalf("interval: %v %v", d, err)
		}
	}
	a, err := InputRevision(get)
	if err != nil {
		t.Fatal(err)
	}
	get.Source.HTTP.Method, get.RefreshInterval = "GET", "300s"
	b, err := InputRevision(get)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent defaults produce different input revisions")
	}
	get.Source.HTTP.URL = "http://renderer/new"
	c, err := InputRevision(get)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("renderer definition change retained old revision")
	}
	a, err = InputRevision(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Source.Job.ContextPath, job.Source.Job.ImagePath, job.RefreshInterval = "/arprt/context.json", "/arprt/output.png", "1800s"
	b, err = InputRevision(job)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent Job defaults produce different revisions")
	}
}

func TestOverlayRevisionAndRepresentation(t *testing.T) {
	spec := api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}
	old, _ := InputRevision(spec)
	spec.Overlay = &api.ScreenOverlay{Position: "bottom-left", CycleCorners: true, Items: &[]api.OverlayItem{"wifi"}, Margin: &api.OverlayMargin{Left: 8, Right: 8, Top: 8, Bottom: 8}}
	disabled, _ := InputRevision(spec)
	if disabled != old {
		t.Fatal("disabled settings changed existing revision")
	}
	spec.Overlay = &api.ScreenOverlay{Enabled: true}
	defaults, _ := InputRevision(spec)
	if defaults == old {
		t.Fatal("overlay edit did not invalidate revision")
	}
	spec.Overlay.Position = "top-right"
	spec.Overlay.Items = &[]api.OverlayItem{"wifi", "timestamp", "battery"}
	explicit, _ := InputRevision(spec)
	if explicit != defaults {
		t.Fatal("equivalent settings differ")
	}
	revisions := map[string]bool{defaults: true}
	for _, margin := range []*api.OverlayMargin{{Top: 8}, {Bottom: 8}, {Left: 8}, {Right: 8}, {Top: 4, Bottom: 8, Left: 12, Right: 16}} {
		spec.Overlay.Margin = margin
		inset, err := InputRevision(spec)
		if err != nil || revisions[inset] {
			t.Fatal("independent margin did not invalidate definition", margin, err)
		}
		revisions[inset] = true
		copied := EffectiveOverlay(spec.Overlay)
		copied.Margin.Top++
		copied.Margin.Bottom++
		copied.Margin.Left++
		copied.Margin.Right++
		if *copied.Margin == *spec.Overlay.Margin {
			t.Fatal("effective margin aliases input")
		}
	}
	for _, margin := range []*api.OverlayMargin{nil, {}, {Top: 0}, {Bottom: 0}, {Left: 0}, {Right: 0}} {
		spec.Overlay.Margin = margin
		zeroMargin, err := InputRevision(spec)
		if err != nil || zeroMargin != defaults {
			t.Fatal("empty/zero margin differs from omission", margin, err)
		}
	}
	spec.Overlay.Items = &[]api.OverlayItem{}
	empty, _ := InputRevision(spec)
	if empty == defaults {
		t.Fatal("empty items became all items")
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip api.ArprtScreenSpec
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.Overlay.Items == nil || len(*roundtrip.Overlay.Items) != 0 {
		t.Fatal("empty selection was lost")
	}
	copied := spec.DeepCopy()
	*copied.Overlay.Items = append(*copied.Overlay.Items, "wifi")
	if len(*spec.Overlay.Items) != 0 {
		t.Fatal("deep copy aliases items")
	}
	spec.Overlay = &api.ScreenOverlay{Enabled: true, CycleCorners: true}
	effective := EffectiveOverlay(spec.Overlay)
	if effective.Position != "" || len(*effective.Items) != 3 {
		t.Fatal("cycling default incorrectly adds position")
	}
}

func TestProcessingRevision(t *testing.T) {
	spec := api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}
	omitted, err := InputRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []*api.ImageProcessing{{}, {ResizeMode: "contain"}, {Dithering: "atkinson"}, {ResizeMode: "contain", Dithering: "atkinson"}} {
		spec.ImageProcessing = policy
		revision, err := InputRevision(spec)
		if err != nil || revision != omitted {
			t.Fatal("equivalent policy changed revision", policy, err)
		}
	}
	revisions := map[string]bool{omitted: true}
	for _, mode := range []string{"contain", "stretch"} {
		for _, dither := range []string{"atkinson", "floyd-steinberg", "bayer", "none"} {
			spec.ImageProcessing = &api.ImageProcessing{ResizeMode: mode, Dithering: dither}
			revision, err := InputRevision(spec)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "contain" && dither == "atkinson" {
				continue
			}
			if revisions[revision] {
				t.Fatal("different policy reused revision", mode, dither)
			}
			revisions[revision] = true
		}
	}
	for _, invalid := range []*api.ImageProcessing{{ResizeMode: "cover"}, {Dithering: "random"}} {
		spec.ImageProcessing = invalid
		if _, err := InputRevision(spec); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
