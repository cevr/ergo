// Command control is the hand Go control of examples/raytrace: the same
// seeded scene, bounding-volume tree, path tracing and binary PPM, as a Go
// programmer writes it for speed: shapes and materials as tagged structs
// in slices (no interfaces), vectors by value, a fixed stack for the tree
// walk, one goroutine per tile from a shared counter. It writes byte for
// byte what the Ergo program writes on the same host, with the same exit
// status; every float operation is in the Ergo program's order. Every
// product of floats is written float64(a*b), which Go rounds, so no host
// fuses it into a sum (the Go spec, Arithmetic operators): the Ergo
// lowering writes its products so, and the bytes agree on every host.
//
//	control W H SPP SEED OBJECTS WORKERS TILE
package main

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"sync"
)

const usage = "usage: raytrace W H SPP SEED OBJECTS WORKERS TILE"

func main() {
	args := os.Args[1:]
	if len(args) != 7 {
		fail(usage)
	}
	w := count(args[0], "W", 1)
	h := count(args[1], "H", 1)
	spp := count(args[2], "SPP", 1)
	seed := uint64(count(args[3], "SEED", 0))
	objects := count(args[4], "OBJECTS", 0)
	workers := count(args[5], "WORKERS", 1)
	tile := count(args[6], "TILE", 1)
	sc := generate(seed, objects, float64(w)/float64(h))
	img := render(&sc, w, h, spp, seed, workers, tile)
	out := make([]byte, 0, len(img)+32)
	out = fmt.Appendf(out, "P6\n%d %d\n255\n", w, h)
	out = append(out, img...)
	os.Stdout.Write(out)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

func count(arg, name string, least int) int {
	n, err := strconv.Atoi(arg)
	if err != nil || n < least {
		fail("raytrace: " + name + " must be a count\n" + usage)
	}
	return n
}

type vec struct{ x, y, z float64 }

func add(a, b vec) vec { return vec{a.x + b.x, a.y + b.y, a.z + b.z} }
func sub(a, b vec) vec { return vec{a.x - b.x, a.y - b.y, a.z - b.z} }
func scale(a vec, s float64) vec {
	return vec{float64(a.x * s), float64(a.y * s), float64(a.z * s)}
}
func mul(a, b vec) vec {
	return vec{float64(a.x * b.x), float64(a.y * b.y), float64(a.z * b.z)}
}
func dot(a, b vec) float64 { return float64(a.x*b.x) + float64(a.y*b.y) + float64(a.z*b.z) }
func cross(a, b vec) vec {
	return vec{float64(a.y*b.z) - float64(a.z*b.y), float64(a.z*b.x) - float64(a.x*b.z), float64(a.x*b.y) - float64(a.y*b.x)}
}
func unit(a vec) vec { return scale(a, 1.0/math.Sqrt(dot(a, a))) }
func axis(a vec, k int) float64 {
	if k == 0 {
		return a.x
	}
	if k == 1 {
		return a.y
	}
	return a.z
}

type ray struct{ origin, dir vec }

func at(r ray, t float64) vec { return add(r.origin, scale(r.dir, t)) }

// shape is a sphere (tri false: center a, radius) or a triangle (a, b, c).
type shape struct {
	tri     bool
	a, b, c vec
	radius  float64
	mat     int
}

const miss = -1.0

func intersect(s *shape, r ray, lo, hi float64) float64 {
	if !s.tri {
		oc := sub(r.origin, s.a)
		a := dot(r.dir, r.dir)
		hb := dot(oc, r.dir)
		c := dot(oc, oc) - float64(s.radius*s.radius)
		disc := float64(hb*hb) - float64(a*c)
		if disc < 0 {
			return miss
		}
		sq := math.Sqrt(disc)
		near := (-hb - sq) / a
		if near > lo && near <= hi {
			return near
		}
		far := (-hb + sq) / a
		if far > lo && far <= hi {
			return far
		}
		return miss
	}
	e1 := sub(s.b, s.a)
	e2 := sub(s.c, s.a)
	p := cross(r.dir, e2)
	det := dot(e1, p)
	if det > -1e-12 && det < 1e-12 {
		return miss
	}
	inv := 1.0 / det
	s0 := sub(r.origin, s.a)
	u := float64(dot(s0, p) * inv)
	if u < 0 || u > 1 {
		return miss
	}
	q := cross(s0, e1)
	v := float64(dot(r.dir, q) * inv)
	if v < 0 || u+v > 1 {
		return miss
	}
	t := float64(dot(e2, q) * inv)
	if t > lo && t <= hi {
		return t
	}
	return miss
}

func normal(s *shape, p vec) vec {
	if !s.tri {
		return scale(sub(p, s.a), 1.0/s.radius)
	}
	return unit(cross(sub(s.b, s.a), sub(s.c, s.a)))
}

type box struct{ min, max vec }

const pad = 1e-7

func lower(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

func higher(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func union(a, b box) box {
	return box{vec{lower(a.min.x, b.min.x), lower(a.min.y, b.min.y), lower(a.min.z, b.min.z)}, vec{higher(a.max.x, b.max.x), higher(a.max.y, b.max.y), higher(a.max.z, b.max.z)}}
}

func padded(b box) box {
	m := math.Abs(b.min.x)
	for _, x := range [...]float64{math.Abs(b.min.y), math.Abs(b.min.z), math.Abs(b.max.x), math.Abs(b.max.y), math.Abs(b.max.z)} {
		if x > m {
			m = x
		}
	}
	m = 1.0 + m
	e := vec{float64(m * pad), float64(m * pad), float64(m * pad)}
	return box{sub(b.min, e), add(b.max, e)}
}

func bounds(s *shape) box {
	if !s.tri {
		r := vec{s.radius, s.radius, s.radius}
		return padded(box{sub(s.a, r), add(s.a, r)})
	}
	return padded(union(union(box{s.a, s.a}, box{s.b, s.b}), box{s.c, s.c}))
}

func centroid(s *shape) vec {
	b := bounds(s)
	return scale(add(b.min, b.max), 0.5)
}

func crosses(b *box, r ray, lo, hi float64) bool {
	near, far := lo, hi
	for k := range 3 {
		inv := 1.0 / axis(r.dir, k)
		o := axis(r.origin, k)
		t0 := float64((axis(b.min, k) - o) * inv)
		t1 := float64((axis(b.max, k) - o) * inv)
		if inv < 0 {
			t0, t1 = t1, t0
		}
		if t0 > near {
			near = t0
		}
		if t1 < far {
			far = t1
		}
		if far < near {
			return false
		}
	}
	return true
}

// node is a leaf (leaf true: count shapes from first in order) or a
// branch (left, right).
type node struct {
	bounds       box
	leaf         bool
	first, count int
	left, right  int
}

type tree struct {
	shapes []shape
	order  []int
	nodes  []node
}

const leafSize = 4

func build(shapes []shape) tree {
	t := tree{shapes: shapes}
	cents := make([]vec, len(shapes))
	for i := range shapes {
		cents[i] = centroid(&shapes[i])
	}
	all := make([]int, len(shapes))
	for i := range all {
		all[i] = i
	}
	if len(shapes) > 0 {
		t.grow(cents, all)
	}
	return t
}

func (t *tree) grow(cents []vec, items []int) int {
	b := bounds(&t.shapes[items[0]])
	for _, i := range items {
		b = union(b, bounds(&t.shapes[i]))
	}
	at := len(t.nodes)
	if len(items) <= leafSize {
		t.nodes = append(t.nodes, node{bounds: b, leaf: true, first: len(t.order), count: len(items)})
		t.order = append(t.order, items...)
		return at
	}
	lo := cents[items[0]]
	hi := lo
	for _, i := range items {
		c := cents[i]
		lo = vec{lower(lo.x, c.x), lower(lo.y, c.y), lower(lo.z, c.z)}
		hi = vec{higher(hi.x, c.x), higher(hi.y, c.y), higher(hi.z, c.z)}
	}
	spread := sub(hi, lo)
	ax := 0
	if spread.y > spread.x {
		ax = 1
	}
	if spread.z > axis(spread, ax) {
		ax = 2
	}
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(x, y int) int {
		cx, cy := axis(cents[x], ax), axis(cents[y], ax)
		if cx < cy {
			return -1
		}
		if cx > cy {
			return 1
		}
		return x - y
	})
	t.nodes = append(t.nodes, node{bounds: b, leaf: true})
	mid := len(sorted) / 2
	left := t.grow(cents, sorted[:mid])
	right := t.grow(cents, sorted[mid:])
	t.nodes[at] = node{bounds: b, left: left, right: right}
	return at
}

func (t *tree) nearest(r ray, lo, hi float64) (float64, int) {
	if len(t.nodes) == 0 {
		return 0, -1
	}
	best, found := hi, -1
	var stack [64]int
	top := 1
	for top > 0 {
		top--
		n := &t.nodes[stack[top]]
		if !crosses(&n.bounds, r, lo, best) {
			continue
		}
		if n.leaf {
			for _, i := range t.order[n.first : n.first+n.count] {
				d := intersect(&t.shapes[i], r, lo, best)
				if d != miss && (d < best || i < found) {
					best, found = d, i
				}
			}
			continue
		}
		stack[top] = n.right
		stack[top+1] = n.left
		top += 2
	}
	return best, found
}

// material kinds
const (
	diffuse = iota
	metal
	glass
	light
)

type material struct {
	kind  int
	color vec // albedo or emission
	fuzz  float64
	index float64
}

type stream struct{ state uint64 }

func (s *stream) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *stream) float() float64 {
	return float64(float64(s.next()>>11) * (1.0 / 9007199254740992.0))
}

func (s *stream) inBall() vec {
	for {
		x := float64(2.0*s.float()) - 1.0
		y := float64(2.0*s.float()) - 1.0
		z := float64(2.0*s.float()) - 1.0
		p := vec{x, y, z}
		d := dot(p, p)
		if d < 1.0 && d > 1e-12 {
			return p
		}
	}
}

type camera struct{ origin, corner, horiz, vert vec }

const viewHeight = 0.7279404685324047

func lookAt(from, to vec, aspect float64) camera {
	w := unit(sub(from, to))
	u := unit(cross(vec{0, 1, 0}, w))
	v := cross(w, u)
	horiz := scale(u, float64(aspect*viewHeight))
	vert := scale(v, viewHeight)
	corner := sub(sub(sub(from, scale(horiz, 0.5)), scale(vert, 0.5)), w)
	return camera{from, corner, horiz, vert}
}

func (c *camera) through(u, v float64) ray {
	to := add(add(c.corner, scale(c.horiz, u)), scale(c.vert, v))
	return ray{c.origin, sub(to, c.origin)}
}

type scene struct {
	tree tree
	mats []material
	cam  camera
}

func generate(seed uint64, n int, aspect float64) scene {
	s := stream{seed}
	shapes := []shape{{a: vec{0, -1000, 0}, radius: 1000, mat: 0}, {a: vec{0, 7, 0}, radius: 2, mat: 1}}
	mats := []material{{kind: diffuse, color: vec{0.5, 0.5, 0.5}}, {kind: light, color: vec{4, 4, 4}}}
	for range n {
		x := float64(8.0*s.float()) - 4.0
		z := float64(8.0*s.float()) - 4.0
		size := 0.2 + float64(0.5*s.float())
		mat := len(mats)
		if s.next()%3 == 0 {
			a := vec{x, 0, z}
			bx := x + float64(size*(float64(2.0*s.float())-1.0))
			by := float64(2.0 * size)
			bz := z + float64(size*(float64(2.0*s.float())-1.0))
			cx := x + float64(size*(float64(2.0*s.float())-1.0))
			cy := float64(size * s.float())
			cz := z + float64(size*(float64(2.0*s.float())-1.0))
			shapes = append(shapes, shape{tri: true, a: a, b: vec{bx, by, bz}, c: vec{cx, cy, cz}, mat: mat})
		} else {
			shapes = append(shapes, shape{a: vec{x, size, z}, radius: size, mat: mat})
		}
		kind := s.next() % 10
		cr := 0.2 + float64(0.8*s.float())
		cg := 0.2 + float64(0.8*s.float())
		cb := 0.2 + float64(0.8*s.float())
		color := vec{cr, cg, cb}
		switch {
		case kind < 5:
			mats = append(mats, material{kind: diffuse, color: color})
		case kind < 8:
			mats = append(mats, material{kind: metal, color: color, fuzz: float64(0.5 * s.float())})
		case kind < 9:
			mats = append(mats, material{kind: glass, index: 1.5})
		default:
			mats = append(mats, material{kind: light, color: scale(color, 3.0)})
		}
	}
	return scene{build(shapes), mats, lookAt(vec{0, 2, -9}, vec{0, 0.5, 0}, aspect)}
}

const (
	maxBounces = 8
	far        = 1e300
	nearT      = 0.001
)

func reflect(v, n vec) vec { return sub(v, scale(n, float64(2.0*dot(v, n)))) }

func schlick(cos, ratio float64) float64 {
	r0 := (1.0 - ratio) / (1.0 + ratio)
	r0 = float64(r0 * r0)
	return r0 + float64((1.0-r0)*math.Pow(1.0-cos, 5.0))
}

func trace(sc *scene, r ray, s *stream) vec {
	color := vec{}
	through := vec{1, 1, 1}
	for bounce := 0; ; bounce++ {
		t, i := sc.tree.nearest(r, nearT, far)
		if i < 0 {
			tt := float64(0.5 * (unit(r.dir).y + 1.0))
			sky := add(scale(vec{1, 1, 1}, 1.0-tt), scale(vec{0.5, 0.7, 1.0}, tt))
			return add(color, mul(through, sky))
		}
		sh := &sc.tree.shapes[i]
		p := at(r, t)
		outward := normal(sh, p)
		front := dot(r.dir, outward) < 0
		n := outward
		if !front {
			n = vec{-outward.x, -outward.y, -outward.z}
		}
		m := &sc.mats[sh.mat]
		switch m.kind {
		case light:
			return add(color, mul(through, m.color))
		case diffuse:
			dir := add(n, unit(s.inBall()))
			if dot(dir, dir) < 1e-16 {
				dir = n
			}
			r = ray{p, dir}
			through = mul(through, m.color)
		case metal:
			dir := add(reflect(unit(r.dir), n), scale(s.inBall(), m.fuzz))
			if dot(dir, n) <= 0 {
				return color
			}
			r = ray{p, dir}
			through = mul(through, m.color)
		case glass:
			ratio := m.index
			if front {
				ratio = 1.0 / m.index
			}
			u := unit(r.dir)
			cos := -dot(u, n)
			if cos > 1 {
				cos = 1
			}
			k := 1.0 - float64(cos*cos)
			if k < 0 {
				k = 0
			}
			sin := math.Sqrt(k)
			dir := reflect(u, n)
			if float64(ratio*sin) <= 1.0 && schlick(cos, ratio) <= s.float() {
				perp := scale(add(u, scale(n, cos)), ratio)
				along := scale(n, -math.Sqrt(math.Abs(1.0-dot(perp, perp))))
				dir = add(perp, along)
			}
			r = ray{p, dir}
		}
		if bounce+1 >= maxBounces {
			return color
		}
	}
}

func level(c float64) byte {
	g := 0.0
	if c > 0 {
		g = math.Sqrt(c)
	}
	if g > 0.999 {
		g = 0.999
	}
	return byte(int(float64(g * 256.0)))
}

func render(sc *scene, w, h, spp int, seed uint64, workers, size int) []byte {
	type tile struct{ x0, y0, x1, y1 int }
	var tiles []tile
	for y := 0; y < h; y += size {
		for x := 0; x < w; x += size {
			tiles = append(tiles, tile{x, y, min(x+size, w), min(y+size, h)})
		}
	}
	img := make([]byte, w*h*3)
	var next int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(workers, len(tiles)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				k := next
				next++
				mu.Unlock()
				if k >= len(tiles) {
					return
				}
				t := tiles[k]
				for y := t.y0; y < t.y1; y++ {
					for x := t.x0; x < t.x1; x++ {
						s := stream{seed ^ (uint64(y*w+x) * 0xD1B54A32D192ED03)}
						sum := vec{}
						for range spp {
							du := s.float()
							dv := s.float()
							u := (float64(x) + du) / float64(w)
							v := (float64(h-1-y) + dv) / float64(h)
							sum = add(sum, trace(sc, sc.cam.through(u, v), &s))
						}
						c := scale(sum, 1.0/float64(spp))
						at := (y*w + x) * 3
						img[at] = level(c.x)
						img[at+1] = level(c.y)
						img[at+2] = level(c.z)
					}
				}
			}
		}()
	}
	wg.Wait()
	return img
}
