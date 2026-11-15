package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var witSeed = flag.Uint64("witness.seed", 0, "run TestKnownWitnessesAgree on this seed alone")

// witCopy is the Go declaration of a copy of a generated function for a
// known witness: gJ's copy for Counter is gJCounter, with no witness.
var witCopy = regexp.MustCompile(`(?m)^func g[0-9]+(Counter|Log|Fixed)(_[0-9]+)?\(`)

// witPrelude is every TestKnownWitnessesAgree program's operation type
// Sink, its providers (Counter and Log through a pointer, Fixed by value)
// and the helpers: conv passes its S to viaOps, which takes a Sink (a
// conversion of the type parameter, which takes the witness itself), pair
// has two type parameters (no copy), and Tag is the receiver of a dot
// call. Take(77) on a Counter raises a defect.
const witPrelude = `type Empty { at int }

type Sink {
	Put(n int),
	Total() int,
	Take(n int) int fails Empty,
}

type Counter { sum int, puts int }

func Put(c *Counter, n int) {
	c.sum = c.sum + n
	c.puts = c.puts + 1
}

func Total(c *Counter) int { c.sum }

func Take(c *Counter, n int) int fails Empty {
	if n == 77 {
		let z = []int{1}
		c.sum = z[n]
	}
	if n > c.sum {
		return Empty{at: c.puts}
	}
	c.sum = c.sum - n
	n
}

type Log { text string }

func Put(l *Log, n int) { l.text = l.text + "+\(n)" }

func Total(l *Log) int { len(l.text) }

func Take(l *Log, n int) int fails Empty {
	if n > len(l.text) {
		return Empty{at: 0 - len(l.text)}
	}
	l.text = l.text + "-"
	n
}

type Fixed { k int }

func Put(f Fixed, n int) {
	let _ = f.k + n
}

func Total(f Fixed) int { f.k }

func Take(f Fixed, n int) int fails Empty {
	if n > f.k {
		return Empty{at: f.k}
	}
	n
}

func viaOps(s Sink, k int) int {
	s.Put(k)
	s.Total()
}

func conv[S Sink](s S, k int) int { viaOps(s, k) + s.Total() }

type Tag { n int }

func pair[S Sink, T](s S, t T, k int) int {
	s.Put(k)
	s.Total()
}

`

// witGen writes one TestKnownWitnessesAgree program and the copies its
// lowering should make: a copy of gJ for each provider main passes it,
// and through each copy, of every function it passes its parameter on to.
type witGen struct {
	r       *rand.Rand
	funcs   strings.Builder
	passes  [][]int // the functions gJ passes s on to
	copies  map[string]bool
	reached map[string]bool
}

// copyOf records the copy of gJ for provider p and, through it, those of
// the functions gJ passes its parameter on to.
func (g *witGen) copyOf(j int, p string) {
	if g.copies[fmt.Sprintf("g%d %s", j, p)] {
		return
	}
	g.copies[fmt.Sprintf("g%d %s", j, p)] = true
	for _, m := range g.passes[j] {
		g.copyOf(m, p)
	}
}

// function writes gJ: a body of operation calls on s, loops, failures,
// calls that pass s on to an earlier function (plain or as a dot call on
// t), and calls of pair, which has no copy and takes the witness.
func (g *witGen) function(j int) {
	var b strings.Builder
	fmt.Fprintf(&b, "func g%d[S Sink](t Tag, s S, k int) int fails Empty {\n\tlet mut acc = %d + t.n\n", j, j)
	var passes []int
	for n := range 2 + g.r.IntN(4) {
		c := g.r.IntN(5)
		switch kind := g.r.IntN(9); {
		case kind < 3:
			fmt.Fprintf(&b, "\ts.Put(k + %d)\n", c)
		case kind == 3:
			fmt.Fprintf(&b, "\tlet t%d = try s.Take(%d)\n\tacc = acc + t%d\n", n, c, n)
			g.reached["take"] = true
		case kind == 4:
			fmt.Fprintf(&b, "\tfor i in 0..%d {\n\t\ts.Put(i + k)\n\t}\n", c)
		case kind < 7 && j > 0:
			m := g.r.IntN(j)
			passes = append(passes, m)
			if g.r.IntN(2) == 0 {
				fmt.Fprintf(&b, "\tlet t%d = try g%d(t, s, k + %d)\n\tacc = acc + t%d\n", n, m, c, n)
				g.reached["pass on"] = true
			} else {
				fmt.Fprintf(&b, "\tlet t%d = try t.g%d(s, k + %d)\n\tacc = acc + t%d\n", n, m, c, n)
				g.reached["pass on, dot call"] = true
			}
		case kind == 7:
			fmt.Fprintf(&b, "\tacc = acc + pair(s, \"x\", %d)\n", c)
			g.reached["witness: no copy"] = true
		default:
			fmt.Fprintf(&b, "\tacc = acc + s.Total()\n")
		}
	}
	b.WriteString("\tacc = acc + 1\n\tacc + s.Total()\n}\n\n")
	g.funcs.WriteString(b.String())
	g.passes = append(g.passes, passes)
}

// witProgram is the program of seed, the copies its lowering should
// make, and whether it ends in a defect.
func witProgram(seed uint64, reached map[string]bool) (src string, copies int, defect bool) {
	g := &witGen{r: rand.New(rand.NewPCG(seed, 0x77697473)), copies: map[string]bool{}, reached: reached}
	nf := 4 + g.r.IntN(4)
	for j := range nf {
		g.function(j)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range providers {
			if !yield(k) {
				return
			}
		}
	})
	var calls strings.Builder
	for n := range 8 {
		j, p := g.r.IntN(nf), names[g.r.IntN(len(names))]
		g.copyOf(j, p)
		reached["provider "+p] = true
		fmt.Fprintf(&calls, "\tlet r%d = g%d(Tag{n: %d}, %s, %d) catch {\n\tcase Empty as e:\n\t\t-1000 - e.at\n\t}\n", n, j, n, providers[p], g.r.IntN(6))
		fmt.Fprintf(&calls, "\tConsole.Println(\"%d \\(r%d) \\(c.sum) \\(c.puts) \\(l.text)\")\n", n, n)
	}
	// Each provider is passed at least once (a let mut never written
	// through is a diagnostic).
	for _, p := range names {
		g.copyOf(0, p)
		fmt.Fprintf(&calls, "\tlet z%s = g0(Tag{n: 9}, %s, 1) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tConsole.Println(\"%s \\(z%s)\")\n", p, providers[p], p, p)
	}
	defect = seed%2 == 0
	if defect {
		j := g.r.IntN(nf)
		g.copyOf(j, "Counter")
		g.funcs.WriteString("func boom[S Sink](s S, k int) int fails Empty {\n\ts.Put(k)\n\ttry s.Take(77)\n}\n\n")
		fmt.Fprintf(&calls, "\tlet rd = g%d(Tag{n: 1}, &c, 1) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tlet re = boom(&c, rd) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tConsole.Println(\"boom \\(re)\")\n", j)
		reached["defect"] = true
	}
	// A conversion of the type parameter to Sink takes the witness
	// itself; Borrows 6 admits it for a provider by value alone.
	calls.WriteString("\tConsole.Println(\"conv \\(conv(x, 2))\")\n")
	reached["witness: conversion"] = true
	calls.WriteString("\tConsole.Println(\"end \\(c.sum) \\(l.text)\")\n")
	var s strings.Builder
	s.WriteString(witPrelude)
	s.WriteString(g.funcs.String())
	s.WriteString("func main() needs Console {\n\tlet mut c = Counter{sum: 0, puts: 0}\n\tlet mut l = Log{text: \"\"}\n\tlet x = Fixed{k: 4}\n")
	s.WriteString(calls.String())
	s.WriteString("}\n")
	n := len(g.copies) + 1 // conv's copy for Fixed
	if defect {
		n++ // boom's copy for Counter
	}
	return s.String(), n, defect
}

// sameDefect reports whether the copies' report on (standard error) is
// the reference's off: the same first line, and the copies' frames the
// reference's in order, less any the reference adds. The reference's
// witness holds a closure built at the generic call, and a defect raised
// in an operation names that closure's site once more ("main calls boom"
// twice); a copy calls the operation where the generic body names it.
func sameDefect(on, off string) bool {
	a, b := strings.Split(on, "\n"), strings.Split(off, "\n")
	if len(a) == 0 || len(b) == 0 || a[0] != b[0] {
		return false
	}
	j := 1
	for _, line := range a[1:] {
		for j < len(b) && b[j] != line {
			j++
		}
		if j == len(b) {
			return false
		}
		j++
	}
	return true
}

// TestKnownWitnessesAgree is the seeded property of the copies for known
// witnesses (witspec.go): each seed writes a program of generic functions
// over an operation type Sink, [S Sink], that call its operations, loop,
// fail, pass s on to an earlier function (plain or as a dot call), and use
// the witness itself (a conversion of s to Sink at a call, a
// call of a function of two type parameters, which has no copy); main
// calls them on three providers (two through a pointer, one by value),
// and on even seeds ends in a defect raised inside a provider's
// operation. The lowering makes exactly the copies the generator's model
// names (knownStats.Witnessed), each a Go function with no witness, the
// reference (witnessLowering "dynamic") none, and the two programs print
// the same, exit alike and report a defect alike (sameDefect). A failure prints the seed, the Ergo source and
// both outputs.
// Replay: go test -run '^TestKnownWitnessesAgree$' ./internal/sketch/lower -args -witness.seed=N
//
// It builds Go because the question is what the built copies do at run
// time (operation results, failures, the defect and its report); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestKnownWitnessesAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *witSeed != 0 {
		seeds = []uint64{*witSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, copies, defect := witProgram(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "dynamic"} {
			var made int
			saved, savedHook := witnessLowering, statsHook
			witnessLowering = mode
			statsHook = func(s knownStats) { made = s.Witnessed }
			p := mustLower(t, fmt.Sprintf("witnesses%d%s", seed, mode), src)
			witnessLowering, statsHook = saved, savedHook
			want := copies
			if mode == "dynamic" {
				want = 0
			}
			if made != want {
				t.Errorf("seed %d, lowering %q: %d copies for known witnesses, want %d\n%s", seed, mode, made, want, src)
			}
			// The promised form: each copy of a generated function is a
			// Go function of its provider with no witness parameter.
			var decls int
			for _, f := range p.files {
				decls += len(witCopy.FindAll(f.Data, -1))
			}
			wantDecls := want
			if mode == "" {
				wantDecls-- // conv's copy is no gJ
			}
			if defect && mode == "" {
				wantDecls-- // nor is boom's
			}
			if decls != wantDecls {
				t.Errorf("seed %d, lowering %q: %d copies of the generated functions declared, want %d", seed, mode, decls, wantDecls)
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the witness programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("witnesses%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("witnesses%ddynamic", seed)])
		ended := (onCode != 0) == defects[seed] && (defects[seed] || strings.Contains(on, "end "))
		if on != off || !sameDefect(onErr, offErr) || onCode != offCode || !ended {
			t.Errorf("seed %d: the copies and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestKnownWitnessesAgree$' ./internal/sketch/lower -args -witness.seed=%d\n%s\n--- copies (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *witSeed == 0 {
		var missing []string
		for _, k := range []string{"take", "pass on", "pass on, dot call", "witness: conversion", "witness: no copy", "provider Counter", "provider Log", "provider Fixed", "defect"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}
