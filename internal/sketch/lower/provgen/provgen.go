// Package provgen writes the programs of the known-provider property
// (lower's TestKnownProvidersAgree; provider.go) for the lowering's test
// and for irspike's agreement of the IR path (PORT-INSTCOPY).
package provgen

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

// prelude is every program's operation type Sink, its providers
// (Counter, Log and Fixed, each by value: Borrows 6 refuses a borrow
// converted to Sink at an argument, since the parameter may keep it) and
// the helpers the generated functions use: Holder stores a Sink, id is
// generic, and both takes two Sinks. Take(77) on a Counter raises a
// defect.
const prelude = `type Empty { at int }

type Sink {
	Put(n int),
	Total() int,
	Take(n int) int fails Empty,
}

type Counter { sum int, puts int } implements Sink

func Put(c Counter, n int) {
	let _ = c.sum + n
}

func Total(c Counter) int { c.sum + c.puts }

func Take(c Counter, n int) int fails Empty {
	if n == 77 {
		let z = []int{1}
		return z[n]
	}
	if n > c.sum {
		return Empty{at: c.puts}
	}
	n
}

type Log { text string } implements Sink

func Put(l Log, n int) {
	let _ = len(l.text) + n
}

func Total(l Log) int { len(l.text) }

func Take(l Log, n int) int fails Empty {
	if n > len(l.text) {
		return Empty{at: 0 - len(l.text)}
	}
	n + 1
}

type Fixed { k int } implements Sink

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

func keep[T](s Sink, v T, k int) T {
	s.Put(k)
	v
}

func total(s Sink) int { s.Total() }

func apply(f func(int) int, k int) int { f(k) + 1 }

// later returns a literal that reads its Sink (PROV-COPY-CLOSURE's
// repro): called after later returns.
func later(s Sink, k int) func() int {
	func() int {
		s.Put(k)
		total(s) + k
	}
}

func both(a Sink, b Sink, k int) int fails Empty {
	a.Put(k)
	b.Put(k + 1)
	let t = try a.Take(1)
	t + b.Total()
}

`

// gen writes one program and the copies its lowering should make: a copy
// of fJ for each provider a known call passes it, when every use of its
// parameter keeps the copy (eligible), and through each copy, of every
// function it passes the parameter on to.
type gen struct {
	r        *rand.Rand
	funcs    strings.Builder
	eligible []bool
	passes   [][]int // the functions fJ passes s on to
	copies   map[string]bool
	reached  map[string]bool
	// imports are the std packages a capture shape wrote (slices, task).
	imports map[string]bool
}

// copyOf records the copy of fJ for provider p and, through it, those of
// the functions fJ passes its parameter on to.
func (g *gen) copyOf(j int, p string) {
	if !g.eligible[j] || g.copies[fmt.Sprintf("f%d %s", j, p)] {
		return
	}
	g.copies[fmt.Sprintf("f%d %s", j, p)] = true
	for _, m := range g.passes[j] {
		if m < 0 {
			g.copies["total "+p] = true // a literal's pass-on to total
			continue
		}
		g.copyOf(m, p)
	}
}

// function writes fJ: a body of operation calls on s, loops, failures,
// calls that pass s on to an earlier function, and, in some, a use that
// keeps the parameter an interface (a store, a let, a generic argument).
func (g *gen) function(j int) {
	var b strings.Builder
	fmt.Fprintf(&b, "func f%d(s Sink, k int) int fails Empty {\n\tlet mut acc = %d\n", j, j)
	eligible := true
	var passes []int
	for n := range 2 + g.r.IntN(4) {
		c := g.r.IntN(5)
		switch kind := g.r.IntN(11); {
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
		case kind >= 9:
			// A read of s inside a function literal or a task's block
			// (PROV-COPY-CLOSURE): the copy's parameter, captured, is
			// still the adapter, and keeps the copy.
			switch g.r.IntN(6) {
			case 5:
				// A known callback (known.go; ir_kcb.go): apply's call
				// with its literal goes to apply's known variant.
				fmt.Fprintf(&b, "\tacc = acc + apply(func(i int) int {\n\t\ts.Put(i)\n\t\ts.Total() + i\n\t}, %d)\n", c)
				g.reached["capture: known callback"] = true
			case 0:
				fmt.Fprintf(&b, "\tlet g%[1]d = func(n int) int {\n\t\ts.Put(n)\n\t\ts.Total() + n\n\t}\n\tacc = acc + g%[1]d(%[2]d)\n", n, c)
				g.reached["capture: literal"] = true
			case 1:
				fmt.Fprintf(&b, "\tlet q%[1]d = func(n int) int {\n\t\tlet h = func() int { s.Total() + n }\n\t\th()\n\t}\n\tacc = acc + q%[1]d(%[2]d)\n", n, c)
				g.reached["capture: nested"] = true
			case 2:
				fmt.Fprintf(&b, "\tacc = acc + slices.Fold([]int{1, 2, %d}, 0, func(a int, x int) int {\n\t\ts.Put(x)\n\t\ta + x + s.Total()\n\t})\n", c)
				g.reached["capture: std callback"], g.imports["slices"] = true, true
			case 3:
				fmt.Fprintf(&b, "\tlet t%[1]d = task.Go() {\n\t\ts.Put(%[2]d)\n\t\ts.Total()\n\t}\n\tacc = acc + t%[1]d.Wait()\n", n, c)
				g.reached["capture: task"], g.imports["task"] = true, true
			default:
				fmt.Fprintf(&b, "\tlet w%[1]d = func(n int) int {\n\t\ttotal(s) + n\n\t}\n\tacc = acc + w%[1]d(%[2]d)\n", n, c)
				passes = append(passes, -1) // total, a function of Sink
				g.reached["capture: pass on"] = true
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
var providers = map[string]string{"Counter": "c", "Log": "l", "Fixed": "x"}

// Program is the program of seed, the copies its lowering should make,
// and whether it ends in a defect. reached collects the shapes the seed
// wrote (TestKnownProvidersAgree checks the fixed seeds reach them all).
func Program(seed uint64, reached map[string]bool) (src string, copies int, defect bool) {
	g := &gen{r: rand.New(rand.NewPCG(seed, 0x70726f76)), copies: map[string]bool{}, reached: reached, imports: map[string]bool{}}
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
	// keep is generic: its copy for a provider is one instance, the
	// first call's; a call at another type argument takes keep itself
	// (Q4: the copy was reused at the other instance's types).
	first := map[string]string{}
	for n := range 3 {
		p := names[g.r.IntN(len(names))]
		ty, v := "int", strconv.Itoa(g.r.IntN(9))
		if g.r.IntN(2) == 0 {
			ty, v = "string", strconv.Quote("w"+v)
		}
		g.copies["keep "+p] = true
		if t, ok := first[p]; !ok {
			first[p] = ty
		} else if t != ty {
			reached["keep: other instance"] = true
		}
		fmt.Fprintf(&calls, "\tlet g%d = keep[%s](%s, %s, %d)\n\tConsole.Println(\"keep %d \\(g%d) \\(c.sum)\")\n", n, ty, providers[p], v, n, n, n)
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
		fmt.Fprintf(&calls, "\tlet rd = f%d(c, 1) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tlet re = boom(c, rd) catch {\n\tcase Empty as e:\n\t\te.at\n\t}\n\tConsole.Println(\"boom \\(re)\")\n", j)
		reached["defect"] = true
	}
	lp := names[g.r.IntN(len(names))]
	g.copies["later "+lp], g.copies["total "+lp] = true, true
	reached["capture: returned"] = true
	fmt.Fprintf(&calls, "\tlet lf = later(%s, 2)\n\tConsole.Println(\"later \\(lf()) \\(c.sum)\")\n", providers[lp])
	var s strings.Builder
	for _, p := range []string{"slices", "task"} {
		if g.imports[p] {
			s.WriteString("import " + p + "\n")
		}
	}
	s.WriteString("\n")
	s.WriteString(prelude)
	s.WriteString(g.funcs.String())
	s.WriteString("func main() needs Console {\n\tlet c = Counter{sum: 6, puts: 2}\n\tlet l = Log{text: \"abc\"}\n\tlet x = Fixed{k: 4}\n")
	s.WriteString(calls.String())
	s.WriteString("}\n")
	return s.String(), len(g.copies), defect
}
