package imageprocessing

import (
	"math"
	"sort"
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
func toLab(rgb [3]uint16) lab {
	r, g, b := linear[rgb[0]], linear[rgb[1]], linear[rgb[2]]
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
