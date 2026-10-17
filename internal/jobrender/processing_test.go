package jobrender

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/delivery"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/render"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func paddedPNG(t *testing.T, size int) []byte {
	t.Helper()
	b := png(t)
	n := size - len(b) - 12
	if n < 0 {
		t.Fatal("padding too small")
	}
	chunk := make([]byte, n+12)
	binary.BigEndian.PutUint32(chunk, uint32(n))
	copy(chunk[4:], "raNd")
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	out := append([]byte(nil), b[:len(b)-12]...)
	out = append(out, chunk...)
	return append(out, b[len(b)-12:]...)
}
func TestJobProcessingInputLimitAndRecovery(t *testing.T) {
	for _, size := range []int{100000, imageprocessing.MaxInputBytes, imageprocessing.MaxInputBytes + 1} {
		t.Run(time.Duration(size).String(), func(t *testing.T) {
			r, d, _, _ := fixture(t)
			step(t, r, d)
			st := load(t, r, d)
			b := paddedPNG(t, size)
			finish(t, r, d, b, false)
			// Recovery must collect and normalize the same completed run after restart.
			store, err := NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r.Store = store
			step(t, r, d)
			latest, err := r.Repository.Latest(t.Context(), st.Unit)
			if size > imageprocessing.MaxInputBytes {
				if err != artifact.ErrNotFound || load(t, r, d).Reason != "CollectionFailed" {
					t.Fatal("oversized Job input published", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				out, err := r.Repository.Read(t.Context(), latest.ImageDigest)
				if err != nil || !bytes.Equal(out, png(t)) || latest.AttemptID != st.Active.ID {
					t.Fatal("Job normalization or recovery changed output", err)
				}
			}
		})
	}
}

func TestRuntimeAdmissionSharedByHTTPAndJob(t *testing.T) {
	r, d, s, _ := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	finish(t, r, d, png(t), false)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png(t))
	}))
	defer server.Close()
	get := delivery.NewGET(r.Repository, r.Reader)
	manager, cancelManager := context.WithCancel(t.Context())
	defer cancelManager()
	get.Context = manager
	post := delivery.NewPOST(get)
	hs := s.DeepCopy()
	hs.Spec.Source = api.ScreenSource{HTTP: &api.HTTPSource{URL: server.URL, Method: "POST"}}
	snapshot, _ := render.Build(d.Name, st.Unit.Target, render.Observation{}, render.Observation{})
	held, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- imageprocessing.RuntimeAdmission.Do(t.Context(), func(context.Context) error { close(held); <-release; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 3)
	go func() { _, err := post.Image(ctx, hs, d, snapshot); done <- err }()
	hsGet := hs.DeepCopy()
	hsGet.Spec.Source.HTTP.Method = "GET"
	go func() { _, err := get.Image(ctx, hsGet, st.Unit.ResolvedTarget()); done <- err }()
	go func() {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
		done <- err
	}()
	for range 3 {
		if err := <-done; err == nil {
			t.Error("queued image work ignored cancellation")
		}
	}
	cancelManager()
	if calls.Load() != 0 {
		t.Fatal("HTTP started reception without capacity")
	}
	// Output remains on disk; no buffer or successful publication was queued.
	if _, err := os.Stat(filepath.Join(r.Store.run(st.Active.ID), "output", filepath.Base(st.Active.Source.ImagePath))); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if _, err := r.Repository.Latest(t.Context(), st.Unit); err != nil {
		t.Fatal("cancelled collection lost recoverable output", err)
	}
}
