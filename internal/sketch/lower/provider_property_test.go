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

var provSeed = flag.Uint64("providers.seed", 0, "run TestKnownProvidersAgree on this seed alone")

// adapterParam is a Sink parameter (s, or both's a) of an adapter's type
// in a Go function's parameter list, after the environment.
var adapterParam = regexp.MustCompile(`, [sa] \*ops[A-Za-z]+AsSink[,)]`)

// provPrelude is every TestKnownProvidersAgree program's operation type
// Sink, its providers (Counter and Log through a pointer, Fixed by value)
// and the helpers the generated functions use: Holder stores a Sink, id
// is generic, and both takes two Sinks. Take(77) on a Counter raises a
// defect.
const provPrelude = `type Empty { at int }

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

type Holder { s Sink }

func id[T](x T) T { x }

func both(a Sink, b Sink, k int) int fails Empty {
	a.Put(k)
	b.Put(k + 1)
	let t = try a.Take(1)
	t + b.Total()
}

`

// provGen writes one TestKnownProvidersAgree program and the copies its
// lowering should make: a copy of fJ for each provider a known call
// passes it, when every use of its parameter keeps the copy (eligible),
// and through each copy, of every function it passes the parameter on to.
type provGen struct {
	r        *rand.Rand
	funcs    strings.Builder
	eligible []bool
	passes   [][]int // the functions fJ passes s on to
	copies   map[string]bool
	reached  map[string]bool
}

// copyOf records the copy of fJ for provider p and, through it, those of
// the functions fJ passes its parameter on to.
func (g *provGen) copyOf(j int, p string) {
	if !g.eligible[j] || g.copies[fmt.Sprintf("f%d %s", j, p)] {
		return
	}
	g.copies[fmt.Sprintf("f%d %s", j, p)] = true
	for _, m := range g.passes[j] {
		g.copyOf(m, p)
	}
}

// function writes fJ: a body of operation calls on s, loops, failures,
// calls that pass s on to an earlier function, and, in some, a use that
// keeps the parameter an interface (a store, a let, a generic argument).
func (g *provGen) function(j int) {
	var b strings.Builder
	fmt.Fprintf(&b, "func f%d(s Sink, k int) int fails Empty {\n\tlet mut acc = %d\n", j, j)
	eligible := true
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
			fmt.Fprintf(&b, "\tlet t%d = try f%d(s, k + %d)\n\tacc = acc + t%d\n", n, m, c, n)
			g.reached["pass on"] = true
		case kind == 7 && g.r.IntN(3) == 0:
			eligible = false
			switch g.r.IntN(3) {
			case 0:
				fmt.Fprintf(&b, "\tlet h%d = Holder{s: s}\n\th%d.s.Put(%d)\n", n, n, c)
				g.reached["kept: store"] = true
			case 1:
				fmt.Fprintf(&b, "\tlet u%d = s\n\tu%d.Put(%d)\n", n, n, c)
				g.reached["kept: let"] = true
			default:
				fmt.Fprintf(&b, "\tlet u%d = id(s)\n\tu%d.Put(%d)\n", n, n, c)
				g.reached["kept: generic"] = true
			}
		default:
			fmt.Fprintf(&b, "\tacc = acc + s.Total()\n")
		}
	}
	b.WriteString("\tacc = acc + 1\n\tacc + s.Total()\n}\n\n")
	g.funcs.WriteString(b.String())
	g.eligible = append(g.eligible, eligible)
	g.passes = append(g.passes, passes)
}

// providers are the providers main passes, as written at a call.
var providers = map[string]string{"Counter": "&c", "Log": "&l", "Fixed": "x"}

// provProgram is the program of seed, the copies its lowering should
// make, and whether it ends in a defect.
func provProgram(seed uint64, reached map[string]bool) (src string, copies int, defect bool) {
	g := &provGen{r: rand.New(rand.NewPCG(seed, 0x70726f76)), copies: map[string]bool{}, reached: reached}
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
		if !g.eligible[j] {
			reached["call kept"] = true
		}
		fmt.Fprintf(&calls, "\tlet r%d = f%d(%s, %d) catch {\n\tcase Empty as e:\n\t\t-1000 - e.at\n\t}\n", n, j, providers[p], g.r.IntN(6))
		fmt.Fprintf(&calls, "\tConsole.Println(\"%d \\(r%d) \\(c.sum) \\(c.puts) \\(l.text)\")\n", n, n)
	}
	a, b := names[g.r.IntN(len(names))], names[g.r.IntN(len(names))]
	g.copies["both "+a] = true
	fmt.Fprintf(&calls, "\tlet rb = both(%s, %s, 3) catch {\n\tcase Empty as e:\n\t\t-1000 - e.at\n\t}\n\tConsole.Println(\"both \\(rb) \\(c.sum) \\(l.text)\")\n", providers[a], providers[b])
	defect = seed%2 == 0
	if defect {
		j := g.r.IntN(nf)
		g.copyOf(j, "Counter")
		g.funcs.WriteString("func boom(s Sink, k int) int fails Empty {\n\ts.Put(k)\n\ttry s.Take(77)\n}\n\n")
		g.copies["boom Counter"] = true
		fmt.Fprintf(&calls, "\tlet rd = f%d(&c, 1) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tlet re = boom(&c, rd) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tConsole.Println(\"boom \\(re)\")\n", j)
		reached["defect"] = true
	}
	var s strings.Builder
	s.WriteString(provPrelude)
	s.WriteString(g.funcs.String())
	s.WriteString("func main() needs Console {\n\tlet mut c = Counter{sum: 0, puts: 0}\n\tlet mut l = Log{text: \"\"}\n\tlet x = Fixed{k: 4}\n")
	s.WriteString(calls.String())
	s.WriteString("}\n")
	return s.String(), len(g.copies), defect
}

// TestKnownProvidersAgree is the seeded property of the copies for known
// providers (provider.go): each seed writes a program of functions on an
// operation type Sink that call its operations, loop, fail, pass the
// Sink on, and in some keep it an interface (a store in a record, a let,
// a generic argument); main calls them on three providers (two through a
// pointer, one by value), calls a function of two Sinks, and on even
// seeds ends in a defect raised inside a provider's operation. The
// lowering makes exactly the copies the generator's model names
// (knownStats.Providers), the reference (providerLowering "dynamic")
// none, and the two programs print the same and exit alike. A failure
// prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestKnownProvidersAgree$' ./internal/sketch/lower -args -providers.seed=N
//
// It builds Go because the question is what the built copies do at run
// time (operation results, failures, the defect and its report); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestKnownProvidersAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *provSeed != 0 {
		seeds = []uint64{*provSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, copies, defect := provProgram(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "dynamic"} {
			var made int
			saved, savedHook := providerLowering, statsHook
			providerLowering = mode
			statsHook = func(s knownStats) { made = s.Providers }
			p := mustLower(t, fmt.Sprintf("providers%d%s", seed, mode), src)
			providerLowering, statsHook = saved, savedHook
			want := copies
			if mode == "dynamic" {
				want = 0
			}
			if made != want {
				t.Errorf("seed %d, lowering %q: %d copies for known providers, want %d\n%s", seed, mode, made, want, src)
			}
			// The promised form: each copy's Sink parameter is its
			// adapter, a Go pointer, so its operation calls are static.
			var concrete int
			for _, f := range p.files {
				concrete += len(adapterParam.FindAll(f.Data, -1))
			}
			if concrete != want {
				t.Errorf("seed %d, lowering %q: %d Sink parameters of an adapter type, want one per copy (%d)", seed, mode, concrete, want)
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the provider programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("providers%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("providers%ddynamic", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Contains(on, "both ")
		if on != off || onErr != offErr || onCode != offCode || !ended {
			t.Errorf("seed %d: the copies and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestKnownProvidersAgree$' ./internal/sketch/lower -args -providers.seed=%d\n%s\n--- copies (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *provSeed == 0 {
		var missing []string
		for _, k := range []string{"take", "pass on", "kept: store", "kept: let", "kept: generic", "provider Counter", "provider Log", "provider Fixed", "call kept", "defect"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}
