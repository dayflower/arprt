// Command image-investigation measures experimental processing without changing
// production contracts. It intentionally uses only the pinned Go standard library.
package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type lab [3]float64

var linear [65536]float64

func init() {
	for i := range linear {
		v := float64(i) / 65535
		if v <= 0.04045 {
			linear[i] = v / 12.92
		} else {
			linear[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
}
func toLab(rgb [3]float64) lab {
	r, g, b := linear[uint16(math.Round(rgb[0]))], linear[uint16(math.Round(rgb[1]))], linear[uint16(math.Round(rgb[2]))]
	x := (0.4124564*r + 0.3575761*g + 0.1804375*b) / 0.95047
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := (0.0193339*r + 0.1191920*g + 0.9503041*b) / 1.08883
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	// Neutral sRGB has X/Xn=Y/Yn=Z/Zn. Preserve that identity instead of
	// introducing chroma from rounded matrix coefficients.
	if r == g && g == b {
		return lab{116*f(r) - 16, 0, 0}
	}
	fx, fy, fz := f(x), f(y), f(z)
	return lab{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}
func palette(name string) (color.Palette, []lab) {
	var p color.Palette
	if name == "spectra6" {
		for _, v := range [][3]uint8{{0, 0, 0}, {255, 255, 255}, {255, 243, 56}, {191, 0, 0}, {100, 64, 255}, {67, 138, 28}} {
			p = append(p, color.NRGBA{v[0], v[1], v[2], 255})
		}
	} else {
		n := 4
		if name == "gray16" {
			n = 16
		}
		for i := range n {
			v := uint8(i * 255 / (n - 1))
			p = append(p, color.NRGBA{v, v, v, 255})
		}
	}
	ls := make([]lab, len(p))
	for i, c := range p {
		r, g, b, _ := c.RGBA()
		ls[i] = toLab([3]float64{float64(r), float64(g), float64(b)})
	}
	return p, ls
}
func distance(a, b lab) float64 {
	v := 0.0
	for i := range 3 {
		d := a[i] - b[i]
		v += d * d
	}
	return v
}
func nearest(v lab, p []lab) byte {
	best := math.Inf(1)
	idx := 0
	for i, c := range p {
		d := distance(v, c)
		if d < best {
			best, idx = d, i
		}
	}
	return byte(idx)
}

var bayer = [8][8]int{{0, 48, 12, 60, 3, 51, 15, 63}, {32, 16, 44, 28, 35, 19, 47, 31}, {8, 56, 4, 52, 11, 59, 7, 55}, {40, 24, 36, 20, 43, 27, 39, 23}, {2, 50, 14, 62, 1, 49, 13, 61}, {34, 18, 46, 30, 33, 17, 45, 29}, {10, 58, 6, 54, 9, 57, 5, 53}, {42, 26, 38, 22, 41, 25, 37, 21}}

// Evaluate the nearest two discrete allocations to the projected segment point.
// This gives the exact minimum over all 65 ratios for a pair without 65 searches.
func ordered(v lab, p []lab, x, y int) byte {
	best := math.Inf(1)
	a, b, k := 0, 0, 0
	for i := range p {
		for j := i; j < len(p); j++ {
			num, den := 0.0, 0.0
			for c := range 3 {
				d := p[j][c] - p[i][c]
				num += (v[c] - p[i][c]) * d
				den += d * d
			}
			t := 0.0
			if den > 0 {
				t = math.Max(0, math.Min(64, num/den*64))
			}
			for _, n := range []int{int(math.Floor(t)), int(math.Ceil(t))} {
				var mix lab
				for c := range 3 {
					mix[c] = p[i][c] + float64(n)/64*(p[j][c]-p[i][c])
				}
				d := distance(v, mix)
				if d < best {
					best, a, b, k = d, i, j, n
				}
			}
		}
	}
	if bayer[y%8][x%8] < k {
		return byte(b)
	}
	return byte(a)
}

// An exact spatial search over all discrete Bayer mixtures avoids evaluating
// every palette pair for every pixel. Order is an explicit tie breaker.
type mixture struct {
	v              lab
	a, b, k, order int
}
type mixtureNode struct {
	m           mixture
	axis        int
	left, right *mixtureNode
	lo, hi      lab
	neutral     []mixture
}

func mixtureTree(p []lab) *mixtureNode {
	var points []mixture
	for i := range p {
		for j := i; j < len(p); j++ {
			limit := 64
			if i == j {
				limit = 0
			}
			for k := 0; k <= limit; k++ {
				m := mixture{a: i, b: j, k: k, order: len(points)}
				for c := range 3 {
					m.v[c] = p[i][c] + float64(k)/64*(p[j][c]-p[i][c])
				}
				points = append(points, m)
			}
		}
	}
	neutral := true
	for _, c := range p {
		neutral = neutral && c[1] == 0 && c[2] == 0
	}
	if neutral {
		sort.Slice(points, func(i, j int) bool {
			if points[i].v[0] == points[j].v[0] {
				return points[i].order < points[j].order
			}
			return points[i].v[0] < points[j].v[0]
		})
		unique := points[:0]
		for _, m := range points {
			if len(unique) == 0 || unique[len(unique)-1].v[0] != m.v[0] {
				unique = append(unique, m)
			}
		}
		return &mixtureNode{neutral: unique}
	}
	var build func([]mixture, int) *mixtureNode
	build = func(ms []mixture, depth int) *mixtureNode {
		if len(ms) == 0 {
			return nil
		}
		axis := depth % 3
		sort.Slice(ms, func(i, j int) bool {
			if ms[i].v[axis] == ms[j].v[axis] {
				return ms[i].order < ms[j].order
			}
			return ms[i].v[axis] < ms[j].v[axis]
		})
		mid := len(ms) / 2
		n := &mixtureNode{m: ms[mid], axis: axis, left: build(ms[:mid], depth+1), right: build(ms[mid+1:], depth+1), lo: ms[mid].v, hi: ms[mid].v}
		for _, m := range ms {
			for c := range 3 {
				n.lo[c] = math.Min(n.lo[c], m.v[c])
				n.hi[c] = math.Max(n.hi[c], m.v[c])
			}
		}
		return n
	}
	return build(points, 0)
}
func (t *mixtureNode) nearest(v lab, best *mixture, dist *float64) {
	if t == nil {
		return
	}
	lower := 0.0
	for c := range 3 {
		d := math.Max(t.lo[c]-v[c], math.Max(0, v[c]-t.hi[c]))
		lower += d * d
	}
	if lower > *dist {
		return
	}
	d := distance(v, t.m.v)
	if d < *dist || (d == *dist && t.m.order < best.order) {
		*best, *dist = t.m, d
	}
	delta := v[t.axis] - t.m.v[t.axis]
	near, far := t.left, t.right
	if delta > 0 {
		near, far = far, near
	}
	near.nearest(v, best, dist)
	if delta*delta <= *dist {
		far.nearest(v, best, dist)
	}
}
func (t *mixtureNode) pixel(v lab, x, y int) byte {
	best := mixture{order: math.MaxInt}
	d := math.Inf(1)
	if len(t.neutral) > 0 {
		i := sort.Search(len(t.neutral), func(i int) bool { return t.neutral[i].v[0] >= v[0] })
		for _, j := range []int{i - 1, i} {
			if j < 0 || j >= len(t.neutral) {
				continue
			}
			m := t.neutral[j]
			delta := math.Abs(m.v[0] - v[0])
			if delta < d || (delta == d && m.order < best.order) {
				best, d = m, delta
			}
		}
	} else {
		t.nearest(v, &best, &d)
	}
	if bayer[y%8][x%8] < best.k {
		return byte(best.b)
	}
	return byte(best.a)
}

// Use straight sRGB samples composited onto white before interpolation.
func white(img image.Image, x, y int) [3]float64 {
	var r, g, b, a uint16
	if m, ok := img.(*image.NRGBA64); ok {
		c := m.NRGBA64At(x, y)
		r, g, b, a = c.R, c.G, c.B, c.A
	} else {
		c := color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
		r, g, b, a = c.R, c.G, c.B, c.A
	}
	out := [3]float64{}
	for i, v := range []uint16{r, g, b} {
		out[i] = float64((uint64(v)*uint64(a) + 65535*uint64(65535-a) + 32767) / 65535)
	}
	return out
}
func process(ctx context.Context, img image.Image, w, h int, p color.Palette, pl []lab, algorithm string) (*image.Paletted, error) {
	out := image.NewPaletted(image.Rect(0, 0, w, h), p)
	var tree *mixtureNode
	if algorithm == "bayer" {
		tree = mixtureTree(pl)
	}
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	rows := [3][]lab{make([]lab, w+4), make([]lab, w+4), make([]lab, w+4)}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sy := math.Max(0, math.Min(float64(sh-1), (float64(y)+0.5)*float64(sh)/float64(h)-0.5))
		y0 := int(sy)
		y1 := min(y0+1, sh-1)
		fy := sy - float64(y0)
		for x := 0; x < w; x++ {
			if x%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			sx := math.Max(0, math.Min(float64(sw-1), (float64(x)+0.5)*float64(sw)/float64(w)-0.5))
			x0 := int(sx)
			x1 := min(x0+1, sw-1)
			fx := sx - float64(x0)
			a, b, c, d := white(img, x0, y0), white(img, x1, y0), white(img, x0, y1), white(img, x1, y1)
			var rgb [3]float64
			for ch := range 3 {
				rgb[ch] = (a[ch]*(1-fx)+b[ch]*fx)*(1-fy) + (c[ch]*(1-fx)+d[ch]*fx)*fy
			}
			v := toLab(rgb)
			for ch := range 3 {
				v[ch] += rows[0][x+2][ch]
			}
			idx := nearest(v, pl)
			if algorithm == "bayer" {
				idx = tree.pixel(v, x, y)
			}
			out.Pix[y*w+x] = idx
			var weights [][3]int
			switch algorithm {
			case "atkinson":
				weights = [][3]int{{1, 0, 1}, {2, 0, 1}, {-1, 1, 1}, {0, 1, 1}, {1, 1, 1}, {0, 2, 1}}
			case "floyd-steinberg":
				weights = [][3]int{{1, 0, 7}, {-1, 1, 3}, {0, 1, 5}, {1, 1, 1}}
			}
			den := 8.0
			if algorithm == "floyd-steinberg" {
				den = 16
			}
			for _, weight := range weights {
				dx, dy := x+weight[0], weight[1]
				if dx < 0 || dx >= w || y+dy >= h {
					continue
				}
				for ch := range 3 {
					rows[dy][dx+2][ch] += (v[ch] - pl[idx][ch]) * float64(weight[2]) / den
				}
			}
		}
		rows[0], rows[1], rows[2] = rows[1], rows[2], rows[0]
		clear(rows[2])
	}
	return out, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type contextWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}
func decode(ctx context.Context, b []byte) (image.Image, error) {
	m, err := png.Decode(contextReader{ctx, bytes.NewReader(b)})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return m, err
}
func encode(ctx context.Context, img image.Image) ([]byte, error) {
	m, ok := img.(*image.Paletted)
	if !ok {
		return nil, errors.New("probe encoder requires indexed pixels")
	}
	var compressed bytes.Buffer
	z := zlib.NewWriter(contextWriter{ctx, &compressed})
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	row := make([]byte, 1+(w+1)/2)
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clear(row)
		for x := 0; x < w; x++ {
			row[1+x/2] |= m.Pix[y*m.Stride+x] << uint(4*(1-x%2))
		}
		if _, err := z.Write(row); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// An experimental static 4-bit indexed encoder is enough to measure
	// packed-row compression. Production also needs native gray encoding.
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header, uint32(w))
	binary.BigEndian.PutUint32(header[4:], uint32(h))
	header[8], header[9] = 4, 3
	b := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", header)...)
	var plte []byte
	for _, c := range m.Palette {
		r, g, b, _ := c.RGBA()
		plte = append(plte, byte(r>>8), byte(g>>8), byte(b>>8))
	}
	b = append(b, chunk("PLTE", plte)...)
	b = append(b, chunk("IDAT", compressed.Bytes())...)
	b = append(b, chunk("IEND", nil)...)
	return b, nil
}

func main() {
	mode := flag.String("mode", "measure", "fixtures, measure, cancel or load")
	dir := flag.String("dir", "/tmp/arprt-image-fixtures", "fixture directory")
	input := flag.String("input", "rgba16-adam7.png", "input filename")
	w := flag.Int("width", 1872, "target width")
	h := flag.Int("height", 1404, "target height")
	pal := flag.String("palette", "gray16", "target palette")
	algorithm := flag.String("dithering", "atkinson", "processing algorithm")
	stage := flag.String("stage", "decode", "cancellation stage")
	cancelAfter := flag.Duration("cancel-after", 20*time.Millisecond, "delay before cancellation")
	flag.Parse()
	debug.SetMemoryLimit(384 << 20)
	if *mode == "fixtures" {
		must(os.MkdirAll(*dir, 0700))
		for _, c := range []struct {
			name          string
			w, h          int
			adam, entropy bool
		}{{"rgba16.png", 4096, 4096, false, false}, {"rgba16-adam7.png", 4096, 4096, true, false}, {"entropy16.png", 2000, 2000, false, true}, {"wide16.png", 16384, 1024, false, false}, {"tall16.png", 1024, 16384, false, false}} {
			b := fixture(c.w, c.h, 6, 16, c.adam, c.entropy)
			if len(b) > 32<<20 {
				panic("fixture exceeds input limit")
			}
			must(os.WriteFile(filepath.Join(*dir, c.name), b, 0600))
			fmt.Printf("%s %d bytes\n", c.name, len(b))
		}
		return
	}
	if *mode == "load" {
		// Eight waiting HTTP bodies and a completed Job file share admission;
		// input allocation and overlays happen only while holding this slot.
		var wg sync.WaitGroup
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(*dir, *input)) }))
		defer server.Close()
		gate := make(chan struct{}, 1)
		var active, peak atomic.Int32
		for i := range 9 {
			wg.Go(func() {
				gate <- struct{}{}
				defer func() { <-gate }()
				n := active.Add(1)
				defer active.Add(-1)
				peak.Store(max(peak.Load(), n))
				fmt.Fprintf(os.Stderr, "admitted source %d\n", i)
				source := filepath.Join(*dir, *input)
				if i < 8 {
					source = server.URL + "/input.png"
				}
				measure(source, *w, *h, *pal, *algorithm)
			})
		}
		wg.Wait()
		fmt.Printf("peak_admitted=%d\n", peak.Load())
		return
	}
	b, err := os.ReadFile(filepath.Join(*dir, *input))
	must(err)
	if *mode == "cancel" {
		p, pl := palette(*pal)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var img image.Image
		if *stage != "decode" {
			img, err = decode(context.Background(), b)
			must(err)
		}
		if *stage == "encode" {
			img, err = process(context.Background(), img, *w, *h, p, pl, *algorithm)
			must(err)
		}
		var at atomic.Int64
		timer := time.AfterFunc(*cancelAfter, func() { at.Store(time.Now().UnixNano()); cancel() })
		defer timer.Stop()
		switch *stage {
		case "decode":
			_, err = decode(ctx, b)
		case "resize":
			_, err = process(ctx, img, *w, *h, p, pl, *algorithm)
		case "encode":
			_, err = encode(ctx, img)
		default:
			panic("unknown stage")
		}
		if !errors.Is(err, context.Canceled) {
			panic(fmt.Sprintf("work completed before cancellation: %v", err))
		}
		fmt.Printf("stage=%s stopped_after_cancel_ms=%.3f\n", *stage, float64(time.Now().UnixNano()-at.Load())/1e6)
		return
	}
	b = nil
	runtime.GC()
	measure(filepath.Join(*dir, *input), *w, *h, *pal, *algorithm)
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func measure(path string, w, h int, pal, algorithm string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Reserve the maximum body once; io.ReadAll growth can temporarily retain
	// multiple 32 MiB buffers. Production must acquire admission before this.
	var f io.ReadCloser
	var err error
	if strings.HasPrefix(path, "http://") {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		must(e)
		response, e := http.DefaultClient.Do(req)
		must(e)
		if response.StatusCode != 200 {
			panic(response.Status)
		}
		f = response.Body
	} else {
		f, err = os.Open(path)
		must(err)
	}
	input := make([]byte, 32<<20)
	n, err := io.ReadFull(f, input)
	if err != nil && err != io.ErrUnexpectedEOF {
		must(err)
	}
	must(f.Close())
	input = input[:n]
	img, err := decode(ctx, input)
	must(err)
	decoded := time.Since(start)
	p, pl := palette(pal)
	out, err := process(ctx, img, w, h, p, pl, algorithm)
	must(err)
	converted := time.Since(start)
	encoded, err := encode(ctx, out)
	must(err)
	// Model a palette-aware overlay and final encode under the same admission.
	for y := 0; y < min(100, h); y++ {
		for x := 0; x < min(200, w); x++ {
			out.Pix[y*w+x] = byte((x + y) % len(p))
		}
	}
	final, err := encode(ctx, out)
	must(err)
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	peak, _ := os.ReadFile("/sys/fs/cgroup/memory.peak")
	report := map[string]any{"input": filepath.Base(path), "target": fmt.Sprintf("%dx%d/%s", w, h, pal), "dithering": algorithm, "decode_ms": decoded.Milliseconds(), "convert_ms": (converted - decoded).Milliseconds(), "total_ms": time.Since(start).Milliseconds(), "base_bytes": len(encoded), "overlay_bytes": len(final), "heap_sys_mib": stats.HeapSys >> 20, "cgroup_peak_bytes": string(bytes.TrimSpace(peak))}
	must(json.NewEncoder(os.Stdout).Encode(report))
	runtime.KeepAlive(input)
	runtime.KeepAlive(img)
}
