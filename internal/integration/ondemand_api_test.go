//go:build integration

package integration

import (
	"fmt"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyOnDemandAPI(t *testing.T, c client.Client) {
	t.Helper()
	ctx := t.Context()
	for i, timeout := range []string{"", "1s", "5s", "10s"} {
		s := onDemandAPIScreen(fmt.Sprintf("on-demand-valid-%d", i))
		s.Spec.Source = api.ScreenSource{Job: &api.JobSource{ExecutionMode: "OnDemand", Timeout: timeout, Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
		if err := c.Create(ctx, s); err != nil {
			t.Fatal("valid on-demand configuration rejected", err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			t.Fatal(err)
		}
		if s.Spec.Source.Job.Timeout != timeout || s.Spec.RefreshInterval != "" {
			t.Fatal("admission inserted conditional defaults", s.Spec)
		}
	}
	for i, invalid := range []struct{ mode, timeout, interval string }{
		{"OnDemand", "0s", ""}, {"OnDemand", "-1s", ""}, {"OnDemand", "1.5s", ""},
		{"OnDemand", "11s", ""}, {"OnDemand", "15s", ""}, {"OnDemand", "25s", ""},
		{"OnDemand", "1m", ""}, {"OnDemand", "01s", ""}, {"OnDemand", "1s", "5m"},
		{"Scheduled", "5s", ""}, {"", "5s", ""}, {"Unknown", "", ""},
	} {
		s := onDemandAPIScreen(fmt.Sprintf("on-demand-invalid-%d", i))
		s.Spec.Source = api.ScreenSource{Job: &api.JobSource{ExecutionMode: invalid.mode, Timeout: invalid.timeout, Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
		s.Spec.RefreshInterval = invalid.interval
		if err := c.Create(ctx, s); !apierrors.IsInvalid(err) {
			t.Fatal("invalid on-demand configuration accepted", invalid, err)
		}
	}
	// Update an existing omitted-mode CR through both modes. Explicit defaults
	// preserve normalized revisions without injecting an invalid timeout.
	s := onDemandAPIScreen("on-demand-upgrade")
	s.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
	if err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	oldUID := s.UID
	oldRevision, err := screen.InputRevision(s.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"Scheduled", "OnDemand", "Scheduled"} {
		s.Spec.Source.Job.ExecutionMode = mode
		s.Spec.Source.Job.Timeout = ""
		if err := c.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		if s.UID != oldUID || s.Spec.Source.Job.Timeout != "" {
			t.Fatal("upgrade replaced resource or invented timeout")
		}
		revision, err := screen.InputRevision(s.Spec)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "Scheduled" && revision != oldRevision {
			t.Fatal("explicit Scheduled changed legacy revision")
		}
		if mode == "OnDemand" {
			s.Spec.Source.Job.Timeout = "5s"
			if err := c.Update(ctx, s); err != nil {
				t.Fatal(err)
			}
			explicit, err := screen.InputRevision(s.Spec)
			if err != nil || explicit != revision {
				t.Fatal("explicit timeout default changes revision", err)
			}
		}
	}
}

func onDemandAPIScreen(name string) *api.ArprtScreen {
	return &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: name}}
}
