package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCollectRetainsDesiredRotationAndIssuedImages(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "device", UID: "device-uid"}, Spec: api.ArprtDeviceSpec{Rotation: 90, ScreenRef: &api.LocalReference{Name: "screen"}}, Status: api.ArprtDeviceStatus{Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}}}
	s := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "screen", UID: "screen-uid"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{Method: "GET"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, s).Build()
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	original := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	rotated := original
	rotated.Target.Rotation = 90
	var issuedDigest string
	for _, unit := range []artifact.Unit{original, rotated} {
		if unit == rotated {
			image, err = os.ReadFile("../../testdata/png/target-indexed-gray4.png")
			if err != nil {
				t.Fatal(err)
			}
		}
		m, err := repo.Publish(ctx, artifact.Metadata{Unit: unit, ScreenGeneration: 1, InputRevision: strings.Repeat("a", 64), AttemptID: "attempt", Sequence: 1, RenderedAt: now}, image)
		if err != nil {
			t.Fatal(err)
		}
		if unit == original {
			issuedDigest = m.ImageDigest
			if err := repo.Issue(ctx, string(d.UID), m.ImageDigest, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := now.Add(-25 * time.Hour)
	blobs, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, blob := range blobs {
		if err := os.Chtimes(filepath.Join(root, "blobs", blob.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&StorageMaintenance{Reader: c, Repository: repo, Namespace: d.Namespace}).Collect(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Latest(ctx, rotated); err != nil {
		t.Fatal("GC removed current rotated output", err)
	}
	if _, err := repo.Latest(ctx, original); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("GC retained obsolete rotation pointer", err)
	}
	// The issued blob remains authorized even though its old selection pointer is gone.
	if authorized, err := repo.Authorized(ctx, string(d.UID), issuedDigest); err != nil || !authorized {
		t.Fatal("GC revoked issued image", err)
	}
	if _, err := repo.Read(ctx, issuedDigest); err != nil {
		t.Fatal("GC removed issued bytes", err)
	}
}
