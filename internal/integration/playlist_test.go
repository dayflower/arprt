//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyPlaylistAPI(t *testing.T, c client.Client) {
	ctx := context.Background()
	verifySkipPlaylistAPI(t, c)
	for _, test := range []struct {
		name string
		refs []api.LocalReference
	}{{"empty", nil}, {"duplicate", []api.LocalReference{{Name: "photo"}, {Name: "photo"}}}, {"invalid", []api.LocalReference{{Name: "../other"}}}, {"bounded", make([]api.LocalReference, 65)}} {
		t.Run("playlist-"+test.name, func(t *testing.T) {
			if test.name == "bounded" {
				for i := range test.refs {
					test.refs[i].Name = fmt.Sprintf("screen-%d", i)
				}
			}
			p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "bad-list-" + test.name}, Spec: api.ArprtPlaylistSpec{Screens: test.refs}}
			if err := c.Create(ctx, p); !apierrors.IsInvalid(err) {
				t.Fatal("invalid playlist accepted", err)
			}
		})
	}
	cross := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": api.GroupVersion.String(), "kind": "ArprtPlaylist", "metadata": map[string]interface{}{"namespace": "arprt", "name": "cross-list"}, "spec": map[string]interface{}{"screens": []interface{}{map[string]interface{}{"name": "photo", "namespace": "other"}}}}}
	if err := c.Create(ctx, cross, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
		t.Fatal("playlist accepted namespace on reference")
	}
	listed := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "listed"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}}
	if err := c.Create(ctx, listed); err != nil {
		t.Fatal(err)
	}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "cycle"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "listed"}, {Name: "later"}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "cycling"}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: "auth"}, PlaylistRef: &api.LocalReference{Name: p.Name}}}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		_ = c.Get(ctx, client.ObjectKeyFromObject(p), p)
		_ = c.Get(ctx, client.ObjectKeyFromObject(d), d)
		return reason(p.Status.Conditions, controller.ConditionReady) == "ReferenceNotFound" && meta.IsStatusConditionTrue(d.Status.Conditions, controller.ConditionAssignmentReady)
	})
	// The sequence state must survive the API under its orientation-neutral name.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Status.ScreenCycle = &api.ScreenCycleStatus{Assignment: "assignment", ScreenUID: string(listed.UID), InputRevision: "revision", Target: api.CycleTargetStatus{Width: 800, Height: 480, Palette: "gray4"}, ImageDigest: "fixture", Filename: "fixture.png", ServedAt: metav1.NewMicroTime(time.Now().UTC())}
		return c.Status().Update(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	raw := &unstructured.Unstructured{}
	raw.SetGroupVersionKind(api.GroupVersion.WithKind("ArprtDevice"))
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), raw); err != nil {
		t.Fatal(err)
	}
	if uid, found, err := unstructured.NestedString(raw.Object, "status", "screenCycle", "screenUID"); err != nil || !found || uid != string(listed.UID) {
		t.Fatal("screen-cycle status was pruned or renamed", err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(raw.Object, "status", "rotation"); found {
		t.Fatal("sequence state still uses rotation")
	}
	later := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "later"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}}
	if err := c.Create(ctx, later); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		_ = c.Get(ctx, client.ObjectKeyFromObject(p), p)
		return meta.IsStatusConditionTrue(p.Status.Conditions, controller.ConditionReady)
	})
	// Server-side apply must preserve an explicitly changed sequence.
	applied := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": api.GroupVersion.String(), "kind": "ArprtPlaylist",
		"metadata": map[string]any{"namespace": p.Namespace, "name": p.Name},
		"spec":     map[string]any{"screens": []any{map[string]any{"name": "later"}, map[string]any{"name": "listed"}}},
	}}
	if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(applied), client.FieldOwner("playlist-test"), client.ForceOwnership); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil || p.Spec.Screens[0].Name != "later" {
		t.Fatal("apply did not reorder playlist", err)
	}
	// A playlist edit can remove every locally resolvable reference.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
			return err
		}
		p.Spec.Screens = []api.LocalReference{{Name: "photo"}}
		return c.Update(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		_ = c.Get(ctx, client.ObjectKeyFromObject(d), d)
		return reason(d.Status.Conditions, controller.ConditionAssignmentReady) == "NoEligibleScreens"
	})
}

func verifySkipPlaylistAPI(t *testing.T, c client.Client) {
	t.Helper()
	ctx := t.Context()
	for i, test := range []struct {
		policy, timeout string
		valid           bool
	}{
		{"", "", true}, {"Hold", "", true}, {"Skip", "", true}, {"Skip", "1s", true}, {"Skip", "10s", true},
		{"Skip", "0s", false}, {"Skip", "-1s", false}, {"Skip", "1.5s", false}, {"Skip", "01s", false},
		{"Skip", "11s", false}, {"Skip", "15s", false}, {"Skip", "25s", false}, {"Skip", "1m", false},
		{"Hold", "10s", false}, {"", "10s", false}, {"Unknown", "", false},
	} {
		p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: fmt.Sprintf("policy-%d", i)}, Spec: api.ArprtPlaylistSpec{FailurePolicy: test.policy, RenderTimeout: test.timeout, Screens: []api.LocalReference{{Name: "screen"}}}}
		err := c.Create(ctx, p)
		if test.valid {
			if err != nil {
				t.Fatal(test, err)
			}
			if p.Spec.RenderTimeout != test.timeout {
				t.Fatal("admission inserted an unconditional timeout", p.Spec)
			}
			// Existing CRs can switch policy while preserving UID and omitted defaults.
			uid := p.UID
			setPolicy := func(policy string) {
				t.Helper()
				if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					if err := c.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
						return err
					}
					p.Spec.FailurePolicy, p.Spec.RenderTimeout = policy, ""
					return c.Update(ctx, p)
				}); err != nil {
					t.Fatal(err)
				}
			}
			setPolicy("Skip")
			setPolicy("Hold")
			if p.UID != uid || p.Spec.RenderTimeout != "" {
				t.Fatal(p.UID, p.Spec)
			}
		} else if !apierrors.IsInvalid(err) {
			t.Fatal("invalid playlist accepted", test, err)
		}
	}
}
