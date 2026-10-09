package byos

import (
	"context"
	"reflect"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/render"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPendingDisplayPreservesStatusOwnership(t *testing.T) {
	for _, scenario := range []struct {
		name, reason                                    string
		unknownTarget, missingScreen, unsupportedSource bool
	}{
		{name: "unexamined-assignment", reason: "UnknownCapabilities", unknownTarget: true},
		{name: "missing-screen", reason: "ReferenceNotFound", missingScreen: true},
		{name: "unsupported-source", reason: "UnsupportedFeature", unsupportedSource: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			s, c, _ := newTestServer(t, "http://unused.invalid")
			s.GET.NewExecutor = func(string) render.Executor {
				t.Fatal("pending display started rendering")
				return nil
			}
			d := loadDevice(t, c)
			if scenario.missingScreen {
				d.Spec.ScreenRef = &api.LocalReference{Name: "missing"}
				if err := c.Update(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.unsupportedSource {
				var sc api.TrmnlScreen
				if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.Source = api.ScreenSource{}
				if err := c.Update(ctx, &sc); err != nil {
					t.Fatal(err)
				}
			}
			condition(d, "AssignmentReady", true, "AssignmentResolved", "Previously resolved assignment")
			condition(d, "RenderReady", false, "RendererFailed", "Preserve the rendering owner's outcome")
			condition(d, "OtherOwner", true, "ConcurrentUpdate", "Unrelated status")
			if err := c.Status().Update(ctx, d); err != nil {
				t.Fatal(err)
			}
			before := d.DeepCopy()
			headers := auth()
			if scenario.unknownTarget {
				headers["Model"] = "unreviewed"
			}
			response := request(s, "GET", devicePath+"/api/display", headers, nil)
			if response.Code != 200 || response.Body.String() != `{"status":202}` {
				t.Fatal("unexpected pending response", response.Code, response.Body.String())
			}
			after := loadDevice(t, c)
			ready := meta.FindStatusCondition(after.Status.Conditions, "Ready")
			if ready == nil || !meta.IsStatusConditionFalse(after.Status.Conditions, "Ready") || ready.Reason != scenario.reason {
				t.Fatal("incorrect pending readiness", ready)
			}
			for _, kind := range []string{"RenderReady", "OtherOwner"} {
				if !reflect.DeepEqual(meta.FindStatusCondition(before.Status.Conditions, kind), meta.FindStatusCondition(after.Status.Conditions, kind)) {
					t.Fatal("pending display overwrote another status owner", kind)
				}
			}
			assignment := meta.FindStatusCondition(after.Status.Conditions, "AssignmentReady")
			if scenario.unknownTarget {
				if !reflect.DeepEqual(assignment, meta.FindStatusCondition(before.Status.Conditions, "AssignmentReady")) {
					t.Fatal("unexamined assignment was overwritten", assignment)
				}
			} else if assignment == nil || !meta.IsStatusConditionFalse(after.Status.Conditions, "AssignmentReady") || assignment.Reason != scenario.reason {
				t.Fatal("failed assignment was not reported", assignment)
			}
		})
	}
}

func TestReplayPreservesRenderOutcomesAndObservesTelemetry(t *testing.T) {
	ctx := context.Background()
	s, c, _, _, _ := postPair(t)
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil // One-screen discovery still has durable replay turns.
	if err := c.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	png := pngFixture(t)
	s.POST.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) {
			calls++
			return render.Output{PNG: png}, nil
		})
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 || calls != 1 {
		t.Fatal("first turn was not rendered", first, calls)
	}
	d = loadDevice(t, c)
	condition(d, "RenderReady", false, "ConcurrentRenderFailure", "A separately reported rendering outcome")
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	var sc api.TrmnlScreen
	if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
		t.Fatal(err)
	}
	screenFailure := *meta.FindStatusCondition(d.Status.Conditions, "RenderReady")
	screenFailure.Type = "Ready"
	meta.SetStatusCondition(&sc.Status.Conditions, screenFailure)
	if err := c.Status().Update(ctx, &sc); err != nil {
		t.Fatal(err)
	}
	headers := auth()
	headers["Battery-Voltage"] = "4.01"
	replay := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	after := loadDevice(t, c)
	if replay != first || calls != 1 || !reflect.DeepEqual(d.Status.ScreenCycle, after.Status.ScreenCycle) {
		t.Fatal("replay changed the acknowledged turn or rendered again", replay, calls)
	}
	if after.Status.Battery == nil || after.Status.Battery.Voltage == nil || *after.Status.Battery.Voltage != 4.01 {
		t.Fatal("replay lost current request telemetry")
	}
	for _, kind := range []string{"Ready", "RenderReady"} {
		if !reflect.DeepEqual(meta.FindStatusCondition(d.Status.Conditions, kind), meta.FindStatusCondition(after.Status.Conditions, kind)) {
			t.Fatal("replay changed serving or render outcome", kind)
		}
	}
	var afterScreen api.TrmnlScreen
	if err := c.Get(ctx, client.ObjectKeyFromObject(&sc), &afterScreen); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sc.Status, afterScreen.Status) {
		t.Fatal("replay overwrote screen rendering status")
	}
}
