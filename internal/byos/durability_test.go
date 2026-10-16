package byos

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type issueErrorRepository struct {
	artifact.Repository
	commit bool
	calls  int
}

func (r *issueErrorRepository) Issue(ctx context.Context, uid, digest string, now time.Time) error {
	r.calls++
	if r.commit {
		if err := r.Repository.Issue(ctx, uid, digest, now); err != nil {
			return err
		}
	}
	return errors.New("injected issuance failure")
}

type cycleErrorClient struct {
	client.Client
	calls int
}

func (c *cycleErrorClient) Status() client.SubResourceWriter {
	return cycleErrorWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type cycleErrorWriter struct {
	client.SubResourceWriter
	parent *cycleErrorClient
}

func (w cycleErrorWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if d, ok := obj.(*api.ArprtDevice); ok && d.Status.ScreenCycle != nil && d.Status.ScreenCycle.ScreenUID == "post-uid" {
		w.parent.calls++
		return errors.New("injected cycle persistence failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestDeliveryPersistenceFailuresHoldTurnAndRecover(t *testing.T) {
	for _, failure := range []string{"issuance", "issuance-committed", "cycle"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			s, c, root := newTestServer(t, "http://unused.invalid")
			getPNG := pngFixture(t)
			postPNG, err := os.ReadFile("../../testdata/png/target-indexed-gray4.png")
			if err != nil {
				t.Fatal(err)
			}
			s.GET.NewExecutor = func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					return render.Output{PNG: getPNG}, nil
				})
			}
			s.POST.NewExecutor = func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					return render.Output{PNG: postPNG}, nil
				})
			}
			post := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "post", UID: "post-uid", Generation: 1}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://unused.invalid", Method: "POST"}}}}
			playlist := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "list", UID: "list-uid"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "screen"}, {Name: "post"}}}}
			for _, obj := range []client.Object{post, playlist} {
				if err := c.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			d := loadDevice(t, c)
			d.Spec.ScreenRef = nil
			d.Spec.PlaylistRef = &api.LocalReference{Name: playlist.Name}
			if err := c.Update(ctx, d); err != nil {
				t.Fatal(err)
			}
			if first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); first.Status != 0 {
				t.Fatal("initial GET turn failed", first)
			}
			expireTurn(t, c)
			before := loadDevice(t, c).Status.ScreenCycle
			repository := s.Repository
			issueFailure := &issueErrorRepository{Repository: repository, commit: failure == "issuance-committed"}
			cycleFailure := &cycleErrorClient{Client: c}
			if failure == "cycle" {
				s.Client = cycleFailure
			} else {
				s.Repository = issueFailure
			}
			response := request(s, "GET", devicePath+"/api/display", auth(), nil)
			if response.Code != 503 || response.Body.String() != `{"status":503}` {
				t.Fatal("failed persistence acknowledged delivery", response.Code, response.Body.String())
			}
			if failure == "cycle" && cycleFailure.calls != 1 || failure != "cycle" && issueFailure.calls != 1 {
				t.Fatal("failure window was not exercised")
			}
			if !reflect.DeepEqual(before, loadDevice(t, c).Status.ScreenCycle) {
				t.Fatal("failed persistence advanced the turn")
			}
			unit := artifact.Unit{Namespace: "arprt", ScreenUID: string(post.UID), DeviceUID: string(d.UID), Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
			published, err := repository.Latest(ctx, unit)
			if err != nil {
				t.Fatal("failed delivery lost published output", err)
			}
			if authorized, err := repository.Authorized(ctx, string(d.UID), published.ImageDigest); err != nil || authorized != (failure != "issuance") {
				t.Fatal("unexpected issuance state", authorized, err)
			}
			// Reconstruct from disk and recover the selected screen's compatible
			// success even when its next POST refresh fails.
			repo, err := artifact.NewPVC(root)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewServer(s.Config, c, c, repo)
			if err != nil {
				t.Fatal(err)
			}
			restarted.POST.NewExecutor = func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					return render.Output{}, errors.New("renderer unavailable after restart")
				})
			}
			recovered := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil))
			if recovered.Status != 0 || recovered.Filename != published.Filename || !strings.HasSuffix(recovered.ImageURL, "/"+published.ImageDigest+".png") {
				t.Fatal("compatible output did not recover", recovered)
			}
			image := request(restarted, "GET", strings.TrimPrefix(recovered.ImageURL, "https://byos.example.test"), auth(), nil)
			if image.Code != 200 || !bytes.Equal(image.Body.Bytes(), postPNG) {
				t.Fatal("recovered image bytes unavailable")
			}
			if turn := loadDevice(t, c).Status.ScreenCycle; turn == nil || turn.ScreenUID != string(post.UID) || turn.ImageDigest != published.ImageDigest || turn.Filename != published.Filename {
				t.Fatal("recovered turn not persisted", turn)
			}
		})
	}
}

type statusErrorClient struct {
	client.Client
	reporting                  bool
	deviceErrors, screenErrors int
}

func (c *statusErrorClient) Status() client.SubResourceWriter {
	return statusErrorWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type statusErrorWriter struct {
	client.SubResourceWriter
	parent *statusErrorClient
}

func (w statusErrorWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	switch v := obj.(type) {
	case *api.ArprtDevice:
		if !w.parent.reporting || meta.FindStatusCondition(v.Status.Conditions, "Ready") != nil {
			w.parent.deviceErrors++
			return errors.New("injected device status failure")
		}
	case *api.ArprtScreen:
		w.parent.screenErrors++
		return errors.New("injected screen status failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestObservationRequiredAndOutcomeReportingBestEffort(t *testing.T) {
	for _, reporting := range []bool{false, true} {
		name := "observation"
		if reporting {
			name = "reporting"
		}
		t.Run(name, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			post := postScreen(t, c)
			writer := &statusErrorClient{Client: c, reporting: reporting}
			s.Client = writer
			calls := 0
			b := pngFixture(t)
			s.POST.NewExecutor = func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					calls++
					return render.Output{PNG: b}, nil
				})
			}
			response := request(s, "GET", devicePath+"/api/display", auth(), nil)
			if writer.deviceErrors != 1 {
				t.Fatal("device status failure not exercised", writer.deviceErrors)
			}
			if !reporting {
				if response.Code != 503 || response.Body.String() != `{"status":503}` || calls != 0 {
					t.Fatal("failed observation allowed rendering or delivery", response.Code, response.Body.String(), calls)
				}
				unit := artifact.Unit{Namespace: post.Namespace, ScreenUID: string(post.UID), DeviceUID: "device-1", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
				if _, err := s.Repository.Latest(context.Background(), unit); !errors.Is(err, artifact.ErrNotFound) {
					t.Fatal("failed observation published output", err)
				}
				return
			}
			result := decodeDisplay(t, response)
			if response.Code != 200 || result.Status != 0 || calls != 1 || writer.screenErrors != 1 {
				t.Fatal("best-effort reporting blocked delivery", response.Code, result, calls, writer.screenErrors)
			}
			image := request(s, "GET", strings.TrimPrefix(result.ImageURL, "https://byos.example.test"), auth(), nil)
			if image.Code != 200 || !bytes.Equal(image.Body.Bytes(), b) {
				t.Fatal("reporting failure lost issued image")
			}
		})
	}
}
