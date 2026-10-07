package screen

import (
	api "github.com/dayflower/arprt/api/v1alpha1"
	"testing"
	"time"
)

func TestDefaultsAndRevision(t *testing.T) {
	get := api.TrmnlScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}
	job := api.TrmnlScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}}}}
	post := api.TrmnlScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render", Method: "POST"}}}
	for _, tt := range []struct {
		s    api.TrmnlScreenSpec
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
	job.Source.Job.ContextPath, job.Source.Job.ImagePath, job.RefreshInterval = "/trmnl/context.json", "/trmnl/output.png", "1800s"
	b, err = InputRevision(job)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent Job defaults produce different revisions")
	}
}
