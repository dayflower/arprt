package byos

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/render"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProcessingPolicyEditInvalidatesReplay(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			var source bytes.Buffer
			if err := png.Encode(&source, image.NewGray(image.Rect(0, 0, 1, 2))); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(source.Bytes())
			}))
			defer renderer.Close()
			s, c, _ := newTestServer(t, renderer.URL)
			sc := &api.ArprtScreen{}
			if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, sc); err != nil {
				t.Fatal(err)
			}
			sc.Spec.Source.HTTP.Method = method
			sc.Spec.ImageProcessing = &api.ImageProcessing{Dithering: "none"}
			if err := c.Update(t.Context(), sc); err != nil {
				t.Fatal(err)
			}
			d := loadDevice(t, c)
			d.Spec.ScreenRef = nil
			if err := c.Update(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			firstBytes := imageBytes(t, s, first)
			pixels, err := png.Decode(bytes.NewReader(firstBytes))
			if err != nil {
				t.Fatal(err)
			}
			r, _, _, _ := pixels.At(0, 0).RGBA()
			if r != 65535 {
				t.Fatal("contain padding missing")
			}
			turn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
			if turn == nil || turn.CompatibilityFingerprint == "" {
				t.Fatal("new turn lacks processing identity")
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(sc), sc); err != nil {
				t.Fatal(err)
			}
			sc.Spec.ImageProcessing.ResizeMode = "stretch"
			sc.Generation++
			if err := c.Update(t.Context(), sc); err != nil {
				t.Fatal(err)
			}
			second := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if second.Status != 0 || second.Filename == first.Filename || calls.Load() != 2 {
				t.Fatal("policy edit reused old turn", second, calls.Load())
			}
			pixels, err = png.Decode(bytes.NewReader(imageBytes(t, s, second)))
			if err != nil {
				t.Fatal(err)
			}
			r, _, _, _ = pixels.At(0, 0).RGBA()
			if r != 0 {
				t.Fatal("stretch did not fill output")
			}
			if next := loadDevice(t, c).Status.ScreenCycle; next.CompatibilityFingerprint == turn.CompatibilityFingerprint {
				t.Fatal("policy edit retained replay identity")
			}
			if !bytes.Equal(firstBytes, imageBytes(t, s, first)) {
				t.Fatal("policy edit rewrote issued URL")
			}
		})
	}
}

func TestLegacyIssuedImageSurvivesNormalizationUpgrade(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	// Legacy native 2-bit intermediate grays remain retrievable, but cannot be
	// selected for a fresh display or normalized under the same immutable URL.
	legacy, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixtureGET(s, pngFixture(t), &calls)
	result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	d := loadDevice(t, c)
	base := baseImage(t, s, c, false)
	base.Unit.Output = render.OutputConstraints{}
	base.Sequence++
	base.AttemptID = "legacy-upgrade-fixture"
	base.InputRevision = strings.Repeat("a", 64)
	stored, err := s.Repository.Publish(context.Background(), base, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Repository.Issue(t.Context(), string(d.UID), stored.ImageDigest, base.RenderedAt); err != nil {
		t.Fatal(err)
	}
	legacyResponse := DisplayResponse{ImageURL: "https://byos.example.test" + devicePath + "/images/" + stored.ImageDigest + ".png"}
	if !bytes.Equal(legacy, imageBytes(t, s, legacyResponse)) {
		t.Fatal("legacy URL changed bytes")
	}
	if err := render.CurrentTarget(base.Unit.Target).ValidateOutput(t.Context(), legacy); err == nil {
		t.Fatal("legacy gray accepted as current output")
	}
	// The normal current success remains independently addressable.
	if _, err := imageprocessing.Decode(t.Context(), imageBytes(t, s, result)); err != nil {
		t.Fatal(err)
	}
}

// The drawing context can expire after base validation without expiring the
// enclosing display request. That is an optional-overlay failure, not a 503.
type timeoutDerivedRepository struct{ artifact.Repository }

func (r timeoutDerivedRepository) FindDerived(ctx context.Context, base artifact.Metadata, drawing overlay.Drawing) (artifact.Metadata, func(), error) {
	<-ctx.Done()
	return artifact.Metadata{}, nil, ctx.Err()
}
func TestOverlayProcessingTimeoutUsesValidatedBase(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, false)
	b := pngFixture(t)
	calls := 0
	fixtureGET(s, b, &calls)
	s.Repository = timeoutDerivedRepository{Repository: s.Repository}
	response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if response.Status != 0 || !bytes.Equal(b, imageBytes(t, s, response)) {
		t.Fatal("optional processing timeout discarded validated base", response)
	}
}
