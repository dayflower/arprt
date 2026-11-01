package screen

import (
	api "github.com/dayflower/arprt/api/v1alpha1"
	"strings"
	"testing"
)

func TestJobStatePathsAndRevision(t *testing.T) {
	for _, tc := range []struct {
		context, image, result string
		valid                  bool
	}{
		{"", "", "", true}, {"/input/context.json", "/output/image.png", "/output/next.json", true},
		{"", "", "relative", false},
		{"", "/" + strings.Repeat("a", 500) + "/x", "", false}, {"", "", "/other/result.json", false}, {"", "", "/arprt/output.png", false},
		{"", "", "/arprt/context.json", false}, {"", "", "/arprt/./result.json", false}, {"", "", "/arprt/a/../result.json", false},
		{"/arprt/result.json", "", "", false}, {"/arprt/result.json/context.json", "", "", false}, {"", "/arprt/result.json", "", false},
		{"", "/proc/out.png", "/proc/result.json", false}, {"", "/var/lib/arprt/output.png", "", false},
		{"", "", "/arprt/" + strings.Repeat("x", 512), false}, {"", "", "/arprt/re\x00sult.json", false},
	} {
		source := api.JobSource{Renderer: api.RendererContainer{Image: "renderer:fixture"}, ContextPath: tc.context, ImagePath: tc.image, State: &api.JobState{ResultPath: tc.result}}
		got, err := NormalizeJob(source)
		if (err == nil) != tc.valid {
			t.Fatalf("path validation %q: %v", tc.result, err)
		}
		if err == nil && got.State.ResultPath == "" {
			t.Fatal("missing default")
		}
		if source.State.ResultPath != tc.result {
			t.Fatal("normalization mutated input")
		}
	}
	spec := api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:fixture"}}}}
	original, err := InputRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Source.Job.State = &api.JobState{}
	state, err := InputRevision(spec)
	if err != nil || original == state {
		t.Fatal("opt-in missing from revision", err)
	}
	spec.Source.Job.State.ResultPath = "/arprt/result.json"
	explicit, err := InputRevision(spec)
	if err != nil || explicit != state {
		t.Fatal("default result path changes revision", err)
	}
	spec.Source.Job.State.ResetToken = "reset-1"
	reset, err := InputRevision(spec)
	if err != nil || reset == state {
		t.Fatal("reset token missing from revision", err)
	}
}
