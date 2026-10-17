package imageprocessing

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionSerializesAllSources(t *testing.T) {
	var a Admission
	var active, maximum atomic.Int32
	var wg sync.WaitGroup
	for range 9 {
		wg.Go(func() {
			err := a.Do(t.Context(), func(ctx context.Context) error {
				n := active.Add(1)
				for old := maximum.Load(); old < n; old = maximum.Load() {
					if maximum.CompareAndSwap(old, n) {
						break
					}
				}
				defer active.Add(-1)
				return Process(ctx, func(ctx context.Context) error {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > ProcessingTimeout {
						t.Error("missing processing deadline")
					}
					return nil
				})
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if maximum.Load() != 1 || active.Load() != 0 {
		t.Fatal("image admission overlapped")
	}
}

func TestCancellationRetainsAdmissionUntilStopped(t *testing.T) {
	var a Admission
	started, stop, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		finished <- a.Do(ctx, func(ctx context.Context) error { close(started); <-ctx.Done(); <-stop; return ctx.Err() })
	}()
	<-started
	cancel()
	waiting, cancelWait := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelWait()
	called := false
	err := a.Do(waiting, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal("cancelled active worker released admission early")
	}
	close(stop)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := a.Do(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	deadlineCtx, cancelDeadline := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancelDeadline()
	err = Process(deadlineCtx, func(ctx context.Context) error { <-ctx.Done(); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("enclosing deadline: %v", err)
	}
}

func TestAdmissionReleasesOnPanic(t *testing.T) {
	var a Admission
	func() {
		defer func() { _ = recover() }()
		_ = a.Do(t.Context(), func(context.Context) error { panic("test") })
	}()
	if err := a.Do(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedReception(t *testing.T) {
	for _, size := range []int{0, 1, MaxInputBytes - 1, MaxInputBytes, MaxInputBytes + 1} {
		out, err := ReadInput(t.Context(), io.LimitReader(zeroReader{}, int64(size)))
		if size > MaxInputBytes {
			if err == nil || out != nil {
				t.Fatal("unbounded reception")
			}
			continue
		}
		if err != nil || len(out) != size || cap(out) != MaxInputBytes {
			t.Fatalf("size %d: len %d cap %d err %v", size, len(out), cap(out), err)
		}
	}
	ctx := cancelAt(3)
	defer ctx.cancel()
	if _, err := ReadInput(ctx, bytes.NewReader(make([]byte, MaxInputBytes))); !errors.Is(err, context.Canceled) {
		t.Fatalf("reception cancellation: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Measure the actual engine's allocation and throughput independently of the
// Phase 0 prototype. Run with -bench Maximum -benchmem; deployment CPU/memory
// limits and native architectures remain integration acceptance checks.
func BenchmarkQuantizeMaximum(b *testing.B) {
	d := &Decoded{pixels: image.NewNRGBA64(image.Rect(0, 0, 4096, 4096)), width: 4096, height: 4096}
	for _, method := range []Dither{Atkinson, FloydSteinberg, Bayer, None} {
		b.Run(string(method), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, err := d.Quantize(b.Context(), Target{2048, 2048, Gray16, 750000}, Policy{Dither: method})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type watchedImage struct {
	started chan struct{}
	samples atomic.Int64
}

func (m *watchedImage) Bounds() image.Rectangle { return image.Rect(0, 0, 2, 2) }
func (m *watchedImage) ColorModel() color.Model { return color.NRGBA64Model }
func (m *watchedImage) At(x, y int) color.Color {
	if m.samples.Add(1) == 1 {
		close(m.started)
	}
	return color.NRGBA64{uint16(12000 + x*20000), uint16(10000 + y*20000), 30000, 65535}
}

func TestCancellationStopsPixelsBeforeNextLease(t *testing.T) {
	for _, method := range []Dither{Atkinson, FloydSteinberg, Bayer, None} {
		img := &watchedImage{started: make(chan struct{})}
		d := &Decoded{pixels: img, width: 2, height: 2}
		var admission Admission
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- admission.Do(ctx, func(ctx context.Context) error {
				return Process(ctx, func(ctx context.Context) error {
					_, err := d.Quantize(ctx, Target{4096, 1024, Spectra6, 750000}, Policy{Fit: Stretch, Dither: method})
					return err
				})
			})
		}()
		<-img.started
		start := time.Now()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("normalization did not stop")
		}
		t.Logf("%s cancellation returned after %s", method, time.Since(start))
		stopped := img.samples.Load()
		if stopped >= 4096*1024*4 {
			t.Fatal("cancellation completed the whole image")
		}
		if err := admission.Do(t.Context(), func(context.Context) error {
			if img.samples.Load() != stopped {
				t.Error("pixels continued after releasing admission")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Construct a maximum-area RGBA16 Adam7 input with bounded scratch memory,
// independently of the production decoder. Semi-transparent red forces white
// composition; streaming generation keeps setup below the controller budget.
func maximumPNG(b *testing.B) []byte {
	b.Helper()
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	for _, p := range [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
		w := (4096 - p[0] + p[2] - 1) / p[2]
		row := make([]byte, 1+w*8)
		for x := range w {
			binary.BigEndian.PutUint16(row[1+x*8:], 65535)
			binary.BigEndian.PutUint16(row[7+x*8:], 32896)
		}
		for y := p[1]; y < 4096; y += p[3] {
			if _, err := z.Write(row); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := z.Close(); err != nil {
		b.Fatal(err)
	}
	head := make([]byte, 13)
	binary.BigEndian.PutUint32(head, 4096)
	binary.BigEndian.PutUint32(head[4:], 4096)
	head[8], head[9], head[12] = 16, 6, 1
	return testFile(testChunk{"IHDR", head}, testChunk{"IDAT", compressed.Bytes()}, testChunk{"IEND", nil})
}

func BenchmarkNormalizeMaximum(b *testing.B) {
	data := maximumPNG(b)
	runtime.GC()
	var admission Admission
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		err := admission.Do(b.Context(), func(ctx context.Context) error {
			input, err := ReadInput(ctx, bytes.NewReader(data))
			if err != nil {
				return err
			}
			return Process(ctx, func(ctx context.Context) error {
				_, err := Normalize(ctx, input, Target{2048, 2048, Gray16, 750000}, Policy{})
				return err
			})
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
