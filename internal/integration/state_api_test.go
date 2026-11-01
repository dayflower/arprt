//go:build integration

package integration

import (
	"fmt"
	api "github.com/dayflower/arprt/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"
)

func verifyJobStateAPI(t *testing.T, c client.Client) {
	for i, tc := range []struct {
		context, image, result, token string
		valid                         bool
	}{
		{"", "", "", "", true}, {"/input/context.json", "/output/image.png", "/output/state.json", "reset", true},
		{"", "", "relative", "", false},
		{"", "", "/arprt/re\x00sult.json", "", false},
		{"", "/" + strings.Repeat("a", 500) + "/x", "", "", false},
		{"", "", "", strings.Repeat("r", 256), true}, {"", "", "/other/result.json", "", false}, {"", "", "/arprt/context.json", "", false},
		{"", "", "/arprt/output.png", "", false}, {"", "", "/arprt/../result.json", "", false},
		{"/arprt/result.json", "", "", "", false}, {"/arprt/result.json/input", "", "", "", false}, {"", "/arprt/result.json", "", "", false},
		{"", "/usr/share/out.png", "/usr/share/result.json", "", false}, {"", "", "/var/lib/arprt/result.json", "", false},
		{"", "", "/arprt/" + strings.Repeat("x", 512), "", false}, {"", "", "", strings.Repeat("r", 257), false},
	} {
		s := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("state-api-%d", i), Namespace: "arprt"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:fixture"}, ContextPath: tc.context, ImagePath: tc.image, State: &api.JobState{ResultPath: tc.result, ResetToken: tc.token}}}}}
		err := c.Create(t.Context(), s)
		if tc.valid {
			if err != nil {
				t.Fatal("valid state rejected", i, err)
			}
			if s.Spec.Source.Job.State == nil || s.Spec.Source.Job.State.ResultPath != tc.result {
				t.Fatal("state presence or conditional default changed")
			}
		} else if !apierrors.IsInvalid(err) {
			t.Fatal("invalid state accepted", i, err)
		}
	}
}
