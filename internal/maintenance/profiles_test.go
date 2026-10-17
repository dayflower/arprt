package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCollectionUsesResolvedOverridesBeforeStatusRefresh(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "display", UID: "device-uid"}, Spec: api.ArprtDeviceSpec{ScreenRef: &api.LocalReference{Name: "screen"}, Display: &api.DisplayOverride{Width: ptr.To(int32(320)), Height: ptr.To(int32(240)), Palette: "gray16", PNGByteLimit: ptr.To(int32(750000))}}}
	s := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "screen", UID: "screen-uid"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{Method: "GET"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, s).Build()
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	target := devicestate.Resolved(d)
	b, err := imageprocessing.Encode(t.Context(), make([]byte, 320*240), target.ImageTarget())
	if err != nil {
		t.Fatal(err)
	}
	u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(s.UID), Target: target.Target, Output: target.Output}
	now := time.Now()
	m, err := repo.Publish(t.Context(), artifact.Metadata{Unit: u, InputRevision: strings.Repeat("a", 64), Sequence: 1, AttemptID: "current-budget", RenderedAt: now.Add(-48 * time.Hour)}, b)
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "blobs", m.ImageDigest+".png"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := (&StorageMaintenance{Reader: c, Repository: repo, Namespace: d.Namespace}).Collect(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Latest(t.Context(), u); err != nil {
		t.Fatal("GC lost resolved custom-budget output before status refresh", err)
	}
}
