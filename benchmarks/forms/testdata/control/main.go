// Command control is the hand Go twin of benchmarks/forms: each probe as a
// person would write it in Go for speed, printing what the Ergo probe
// prints (IR-FORMS IRF-0; opcount builds it as the control).
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

// ---- step ----

const (
	red = iota
	green
	amber
	off
)

const (
	tick = iota
	trip
	reset
)

type light struct {
	kind  uint8
	ticks int
}

// step moves k and reports 0 (entered), 1 (emitted) or 2 (ignored).
//
//go:noinline
func step(k *light, s uint8) int {
	switch k.kind {
	case red:
		switch s {
		case tick:
			if k.ticks >= 3 {
				*k = light{green, 0}
				return 1
			}
			k.ticks++
			return 0
		case trip:
			*k = light{off, 0}
			return 0
		}
		return 2
	case green:
		switch s {
		case tick:
			if k.ticks >= 5 {
				*k = light{amber, 0}
				return 1
			}
			k.ticks++
			return 0
		case trip:
			*k = light{off, 0}
			return 0
		}
		return 2
	case amber:
		switch s {
		case tick:
			if k.ticks >= 1 {
				*k = light{red, 0}
				return 1
			}
			k.ticks++
			return 0
		case trip:
			*k = light{off, 0}
			return 0
		}
		return 2
	}
	if s == reset {
		*k = light{red, 0}
		return 1
	}
	return 2
}

func sigOf(i int) uint8 {
	switch i % 64 {
	case 33:
		return trip
	case 40:
		return reset
	}
	return tick
}

func benchStep(n int) int {
	k := light{red, 0}
	out := 0
	for i := 0; i < n; i++ {
		out += step(&k, sigOf(i))
	}
	return out
}

// ---- opt ----

func find(keys []int, k int) (int, bool) {
	i := k & 63
	if keys[i] == k {
		return i * 3, true
	}
	return 0, false
}

func benchOpt(n int) int {
	keys := make([]int, 64)
	for i := range keys {
		keys[i] = i
		if i%4 == 0 {
			keys[i] = -1
		}
	}
	sum := 0
	for i := 0; i < n; i++ {
		if v, ok := find(keys, i&127); ok {
			sum += v
		} else {
			sum++
		}
	}
	return sum
}

// ---- fail ----

type bad struct{ at int }

func digit(c int) (int, bad, bool) {
	if c%8 == 7 {
		return 0, bad{c}, false
	}
	return c % 10, bad{}, true
}

//go:noinline
func pair(a, b int) (int, bad, bool) {
	x, e, ok := digit(a)
	if !ok {
		return 0, e, false
	}
	y, e, ok := digit(b)
	if !ok {
		return 0, e, false
	}
	return x*10 + y, bad{}, true
}

func benchFail(n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		v, e, ok := pair(i, i+3)
		if !ok {
			v = e.at % 5
		}
		sum += v
	}
	return sum
}

// ---- pipe ----

func benchPipe(n int) int {
	xs := make([]int, 0, n)
	for i := 0; i < n; i++ {
		xs = append(xs, i)
	}
	acc := 0
	for _, x := range xs {
		y := x*3 + 1
		if y%4 != 0 {
			acc += y % 7
		}
	}
	return acc
}

// ---- update ----

type stats struct{ a, b, c, d, e, f int }

//go:noinline
func bump(s *stats, x int) {
	if x&1 == 0 {
		s.a += x & 7
	} else {
		s.e++
		s.f ^= x
	}
}

func benchUpdate(n int) int {
	s := stats{0, 1, 2, 3, 0, 0}
	for i := 0; i < n; i++ {
		bump(&s, i)
	}
	return s.a + s.b + s.c + s.d + s.e + (s.f & 1023)
}

// ---- view ----

type tree struct {
	kind, a, b, extra []int
	w                 int
}

func build(rows int) tree {
	var t tree
	for i := 0; i < rows; i++ {
		t.kind = append(t.kind, (i*7+i/5)%5)
		t.a = append(t.a, i%97)
		t.b = append(t.b, i%31)
		t.extra = append(t.extra, i%3)
	}
	t.w = 1
	return t
}

//go:noinline
func walk(t tree, passes int) int {
	sum := 0
	kind, as, bs := t.kind, t.a, t.b
	for p := 0; p < passes; p++ {
		for i := range kind {
			a, b := as[i], bs[i]
			switch kind[i] {
			case 0:
				sum += a
			case 1:
				sum += a - b
			case 2:
				sum -= a
			case 3:
				sum += a ^ b
			default:
				sum++
			}
		}
	}
	return sum
}

func benchView(n int) int { return walk(build(4096), n/4096) }

// ---- segpath ----

// seg is a hand Go path segment: its op and its point, no box.
type seg struct {
	op   uint8 // 0 move, 1 line
	x, y float64
}

func segPath(i int) []seg {
	segs := make([]seg, 0, 16)
	for j := 0; j < 8; j++ {
		x := float64(i + j)
		segs = append(segs, seg{0, x, 1.0}, seg{1, x, x + 2.0})
	}
	return segs
}

func benchSegPath(n int) int {
	acc := 0
	for i := 0; i < n; i++ {
		for _, s := range segPath(i & 1023) {
			switch s.op {
			case 0:
				acc += int(s.x)
			case 1:
				acc += int(s.y)
			default:
				acc++
			}
		}
	}
	return acc
}

// ---- ctx ----

type plain struct{ scale int }

func (p plain) add(x int) int { return x*p.scale + 1 }

//go:noinline
func total(p plain, n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += p.add(i & 1023)
	}
	return s
}

func benchCtx(n int) int { return total(plain{3}, n) }

// ---- provspill ----

type tally interface{ bump(x int) int }

type provStep struct{ k int }

func (s provStep) bump(x int) int { return x*s.k + 1 }

//go:noinline
func bumps(t tally, from, n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += t.bump((i + from) & 1023)
	}
	return s
}

func benchProvSpill(n int) int {
	s := 0
	for i := 0; i < n/64; i++ {
		s += bumps(provStep{3}, bumps(provStep{2}, i, 1), 63)
	}
	return s
}

// ---- ref ----

type cell struct {
	mu sync.Mutex
	v  int
}

func benchRef(n int) int {
	c := &cell{}
	sum := 0
	for i := 0; i < n; i++ {
		c.mu.Lock()
		old := c.v
		c.v = old + (i & 3)
		c.mu.Unlock()
		sum += old & 1
	}
	return sum
}

//go:noinline
func benchLoop(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i & 7
	}
	return s
}

// widen and rebuild: hand Go's sub-union is an interface that embeds the
// larger one, so the conversion is free (UNION-FLAT).
type quiz interface{ isQuiz() }
type settled interface {
	quiz
	isSettled()
}
type calculating struct{ next int }
type asked struct{ q int }
type shown struct{ n int }

func (calculating) isQuiz() {}
func (asked) isQuiz()       {}
func (asked) isSettled()    {}
func (shown) isQuiz()       {}
func (shown) isSettled()    {}

//go:noinline
func settledOf(i int) settled {
	if i%3 == 0 {
		return asked{q: i & 7}
	}
	return shown{n: i & 3}
}

func score(q quiz) int {
	switch v := q.(type) {
	case calculating:
		return v.next
	case asked:
		return v.q
	case shown:
		return v.n + 1
	}
	return 0
}

func benchWiden(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += score(settledOf(i))
	}
	return s
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	out := -1
	switch os.Args[2] {
	case "step":
		out = benchStep(n)
	case "opt":
		out = benchOpt(n)
	case "fail":
		out = benchFail(n)
	case "pipe", "pipeloops", "pipefused":
		out = benchPipe(n)
	case "update", "updatefields":
		out = benchUpdate(n)
	case "ctxdirect":
		out = benchCtx(n)
	case "loop":
		out = benchLoop(n)
	case "viewlocal", "viewcross":
		out = benchView(n)
	case "segpath":
		out = benchSegPath(n)
	case "ctx":
		out = benchCtx(n)
	case "ref":
		out = benchRef(n)
	case "provspill":
		out = benchProvSpill(n)
	case "widen", "rebuild":
		out = benchWiden(n)
	}
	fmt.Println(out)
}
