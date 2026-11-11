package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of the flat unions (flatunion.go, OPT2 and OPT3).
// Each seed writes one program: generated unions of the program's own
// (unionGen, below), and a fixed set of declarations that take Options apart
// and build them in every place the lowering spells one (a generic result,
// a Map read matched at once and kept, a std comma-ok result, maps.Update's
// callback, a record field, a slice and a Map element, a Map key, an
// Option of an Option, a tuple match, constant patterns, Go's == and the
// structural ==, a failure that holds an Option, wire's codec, a resource
// payload whose Close the arm calls, a machine state that holds an Option,
// a machine's after timer, a Go extern's Option, generated and listed law
// arguments, Options of a go.Ref in the pointer niche), and a main that
// runs each statement path once in the seed's order and more at random,
// with random arguments, ending normally, in an uncaught failure whose text
// holds an Option or a generated union, in a defect, or in a machine's zero
// timer.
//
// The program's run build and test build are lowered with the flat unions
// and with the reference, form A for every union (unionLowering
// "interface"); each pair must print the same stdout and stderr and exit
// with the same code. A failing seed prints the Ergo source, both outputs
// and its replay command. The fixed seeds reach every generator path. The
// flat lowering builds flat Options and flat unions and bails on none; each
// generated union is flat exactly where the rule's model (unionGen.flat)
// says; the niche holds in the seeds with no generic go.Ref and not in the
// others; and the reference builds no flat value.

var flatSeed = flag.Uint64("flat.seed", 0, "run TestFlatUnionsAgree on this seed alone")

// optionPaths are the generator's branches; the fixed seeds reach each.
var optionPaths = []string{
	"find", "find-string", "get-match", "get-bound", "eq-go", "eq-structural", "pair", "nested",
	"holder", "fails", "catch-yield", "store", "map-value", "map-key", "update", "std", "closure",
	"cell", "resource", "write", "match-expr", "if-expr", "loop-return", "lost-caught",
	"go-extern", "timed", "catch-variant", "if-header",
	"niche", "handle",
	"u-show", "u-mk", "u-eq", "u-map", "u-slice", "u-wire", "u-fail", "u-tuple", "u-yield", "u-generic",
	// The ends, one per seed (seed % optionEnds).
	"end-ok", "end-fail", "end-defect", "end-timer", "end-u-fail", "end-u-defect",
}

// optionEnds is the number of ends at the tail of optionPaths.
const optionEnds = 6

const optionPrelude = `import machines
import maps
import slices
import strings
import testing
import time
import wire
import go gostrings strings

extern go os.LookupEnv func lookupEnv(key string) Option[string] needs go.Foreign

func env(key string) string needs go.Foreign {
	match lookupEnv(key) {
	case Some[string]:
		"set"
	case None:
		"unset"
	}
}

func replaced(s string) string {
	match gostrings.NewReplacer([]string{"a", "o"}) {
	case Some[go.Ref[gostrings.Replacer]] as r:
		r.Value.Replace(s)
	case None:
		"no replacer"
	}
}

func repl(ok bool) Option[go.Ref[gostrings.Replacer]] {
	if ok {
		return gostrings.NewReplacer([]string{"a", "o"})
	}
	None
}

func useRepl(r Option[go.Ref[gostrings.Replacer]], s string) string {
	match r {
	case Some[go.Ref[gostrings.Replacer]] as x:
		x.Value.Replace(s)
	case None:
		"none"
	}
}

func again(r Option[go.Ref[gostrings.Replacer]], b bool) Option[go.Ref[gostrings.Replacer]] {
	match (r, b) {
	case (Some[go.Ref[gostrings.Replacer]] as x, true):
		Some{x.Value}
	case (None, true):
		repl(false)
	case _:
		None
	}
}

type Handle = Opened{ F File } | Shut{}

func openH(name string, ok bool) Handle {
	if ok {
		return Opened{F: File{name: name}}
	}
	Shut
}

func useH(h Handle) needs Console {
	match h {
	case Opened as o:
		Console.Println("use \(o.F.name)")
		o.F.Close()
	case Shut:
		Console.Println("shut")
	}
}

type Idle { n int }

type Busy {}

type St = Idle | Busy

type Tick {}

type Halt {}

type Sig = Tick | Halt

func tstep(s St, e Sig) Decision[St, int] {
	match s {
	case Idle as a:
		if a.n >= 3 {
			Enter[St]{Next: Busy}
		} else {
			Emit[St, int]{Next: Idle{n: a.n + 1}, Output: a.n}
		}
	case Busy:
		Ignore
	}
}

func timer(s St) Option[Timeout[Sig]] {
	match s {
	case Idle:
		Some{Timeout[Sig]{In: 5s, Signal: Tick}}
	case Busy:
		None
	}
}

func zeroTimer(s St) Option[Timeout[Sig]] { Some{Timeout[Sig]{In: 0s, Signal: Tick}} }

type Loop machine[St, Sig] {
	initial: Idle{n: 0},
	step:    tstep,
	after:   timer,
}

type Zero machine[St, Sig] {
	initial: Idle{n: 0},
	step:    tstep,
	after:   zeroTimer,
}

func timed(end int) string needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let o = machines.RunTimed(Loop{}, []machines.Timed[Sig]{}, duration(int64(end) * 1000000000)) catch {
		case machines.Rejected:
			return "rejected"
		}
		match o {
		case machines.Finished[St, int]:
			"finished"
		case machines.Running[St, int] as r:
			"running \(len(r.Emitted))"
		}
	}
}

func timedZero() needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let _ = machines.RunTimed(Zero{}, []machines.Timed[Sig]{}, 10s) catch {
		case machines.Rejected:
			return
		}
	}
}

func opt(o Option[int]) Option[int] { o }

type Dot {}

type Line { Pts []int }

type Shape = Dot | Line

func extremes(xs []int) string { show(slices.Max(xs)) + " " + show(slices.Min([]int{})) }

func mustSome(i int) Some[int] fails Missing {
	if i < 0 {
		return Missing{At: i}
	}
	Some{i}
}

type Missing { At int }

type Lost { Hint Option[string], Code int }

type File { name string }

type Holder { Name string, O Option[int], Xs []Option[int], W Option[Option[int]] }

type Cell[T] { v Option[T] }

type Pack { o Option[int], n int }

func Close(f File) needs Console {
	Console.Println("close \(f.name)")
}

func find[T](xs []T, i int) Option[T] {
	if i < 0 || i >= len(xs) {
		return None
	}
	Some{xs[i]}
}

func pick(b bool, x int) Option[int] {
	if b { Some{x} } else { None }
}

func must(o Option[int]) int fails Missing {
	match o {
	case Some[int] as s:
		s.Value
	case None:
		return Missing{At: -1}
	}
}

func mustOpt(xs []int, i int) Option[int] fails Missing {
	if i < 0 {
		return Missing{At: i}
	}
	find(xs, i)
}

func same[T comparable](a Option[T], b Option[T]) bool { a == b }

func or[T](o Option[T], d T) T {
	match o {
	case Some[T] as s:
		s.Value
	case None:
		d
	}
}

func show(o Option[int]) string {
	match o {
	case Some{Value: 3}:
		"three"
	case Some[int] as s:
		"some \(s.Value)"
	case None:
		"none"
	}
}

func showS(o Option[[]int]) string {
	match o {
	case Some[[]int] as s:
		"some \(len(s.Value)):\(s.Value[0])"
	case None:
		"none"
	}
}

func depth(o Option[Option[int]]) string {
	match o {
	case Some{Value: Some{Value: 3}}:
		"deep three"
	case Some[Option[int]] as s:
		"outer " + show(s.Value)
	case None:
		"empty"
	}
}

func pair(a Option[int], b Option[int]) string {
	match (a, b) {
	case (Some[int] as x, Some[int] as y):
		"both \(x.Value + y.Value)"
	case (Some[int], None):
		"left"
	case (None, Some[int]):
		"right"
	case _:
		"neither"
	}
}

func lose(h Option[string]) int fails Lost {
	return Lost{Hint: h, Code: 7}
}

func open(name string, ok bool) Option[File] {
	if ok {
		return Some{File{name: name}}
	}
	None
}

func use(o Option[File]) needs Console {
	match o {
	case Some[File] as s:
		Console.Println("use \(s.Value.name)")
		s.Value.Close()
	case None:
		Console.Println("no file")
	}
}

func firstOver(xs []int, lim int) Option[int] {
	for x in xs {
		if x > lim {
			return Some{x}
		}
	}
	None
}

func cut(s string) string {
	match strings.Cut(s, "=") {
	case Some[strings.Halves] as c:
		c.Value.Before + "|" + c.Value.After
	case None:
		"uncut"
	}
}

func bump(m Map[string, int], k string) Map[string, int] {
	maps.Update(m, k, func(old Option[int]) int {
		match old {
		case Some[int] as s:
			s.Value + 10
		case None:
			1
		}
	})
}

func trip(h Holder) string {
	let back = wire.RoundTrip(h) catch {
	case _:
		return "trip failed"
	}
	"trip \(back == h) " + show(back.O) + " " + depth(back.W) + " \(len(back.Xs))"
}

type Oops = Empty{} | Bad{ n int }

func risky(x int) int fails Oops {
	if x == 0 {
		return Empty
	}
	if x < 0 {
		return Bad{n: x}
	}
	x
}

func oops(x int) string {
	let y = risky(x) catch {
	case Oops as o:
		return "oops \(o == Bad{n: x})"
	}
	"ok \(y)"
}

type Mode = Calm{} | Woken{}

type Tri = Lo{N int, Last Option[bool], M Mode} | Hi{}

type Kick = Up{} | Flip{} | Wake{}

func kick(t Tri, k Kick) Decision[Tri, int] {
	match t {
	case Lo as l:
		match k {
		case Wake:
			Stay[Tri]{Next: Lo{N: l.N, Last: l.Last, M: Woken}}
		case Up:
			match l.M {
			case Calm:
				Ignore
			case Woken:
				if l.N >= 2 { Enter[Tri]{Next: Hi} } else { Stay[Tri]{Next: Lo{N: l.N + 1, Last: l.Last, M: Woken}} }
			}
		case Flip:
			match l.Last {
			case Some{Value: true}:
				Stay[Tri]{Next: Lo{N: l.N, Last: Some{false}, M: l.M}}
			case Some{Value: false}:
				Stay[Tri]{Next: Lo{N: l.N, Last: None, M: l.M}}
			case None:
				Stay[Tri]{Next: Lo{N: l.N, Last: Some{true}, M: l.M}}
			}
		}
	case Hi:
		Ignore
	}
}

type Gauge machine[Tri, Kick] {
	initial: Lo{N: 0, Last: None, M: Calm},
	step:    kick,

	law ReachesHi reaches Hi,
	law NeverHi never reaches Hi,
}

law SomeIsNone(o Option[int]) { o == None }

law ListedAllNone(o Option[bool]) { o == None || o == Some{false} }

law PackSmall(p Pack) {
	match p.o {
	case Some[int] as s:
		s.Value < 3
	case None:
		true
	}
}

test OptionsCompare {
	assert find([]int{1}, 0) == Some{1}
	assert show(pick(true, 3)) == "three"
}

`

// optionGen writes one main.
type optionGen struct {
	r       *rand.Rand
	b       strings.Builder
	reached map[string]bool
	n       int
	u       *unionGen
}

func (g *optionGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *optionGen) name(p string) string {
	g.n++
	return fmt.Sprintf("%s%d", p, g.n)
}

// small is a value that 3 (show's constant pattern) and the elements of
// xs meet often.
func (g *optionGen) small() int { return g.r.IntN(6) }

// index is an index into xs, out of range now and then.
func (g *optionGen) index() int { return g.r.IntN(6) - 1 }

func (g *optionGen) boolean() string { return []string{"false", "true"}[g.r.IntN(2)] }

func (g *optionGen) key() string { return []string{`"a"`, `"b"`, `"c"`, `"z"`}[g.r.IntN(4)] }

// option is an Option[int] expression of a random shape.
func (g *optionGen) option() string {
	switch g.r.IntN(5) {
	case 0:
		return fmt.Sprintf("find(xs, %d)", g.index())
	case 1:
		return fmt.Sprintf("pick(%s, %d)", g.boolean(), g.small())
	case 2:
		return fmt.Sprintf("m.Get(%s)", g.key())
	case 3:
		return fmt.Sprintf("firstOver(xs, %d)", g.small())
	}
	return []string{"None", fmt.Sprintf("Some{%d}", g.small())}[g.r.IntN(2)]
}

// typed is an Option[int] expression whose type is Option[int], never a
// bare variant: the left of an ==.
func (g *optionGen) typed() string {
	x := g.option()
	if strings.HasPrefix(x, "None") || strings.HasPrefix(x, "Some") {
		return "opt(" + x + ")"
	}
	return x
}

func (g *optionGen) stmt(path string) {
	g.reached[path] = true
	if strings.HasPrefix(path, "u-") {
		g.u.stmt(g, path)
		return
	}
	switch path {
	case "find":
		g.line(fmt.Sprintf("Console.Println(show(find(xs, %d)))", g.index()))
	case "find-string":
		g.line(fmt.Sprintf(`Console.Println(or(find([]string{"p", "q"}, %d), "d"))`, g.index()))
	case "get-match":
		g.line(fmt.Sprintf("match m.Get(%s) {", g.key()))
		g.line("case Some[int] as s:")
		g.line("\tConsole.Println(\"got \\(s.Value)\")")
		g.line("case None:")
		g.line("\tConsole.Println(\"missing\")")
		g.line("}")
	case "get-bound":
		v := g.name("g")
		g.line(fmt.Sprintf("let %s = m.Get(%s)", v, g.key()))
		g.line(fmt.Sprintf("Console.Println(show(%s) + \" \\(%s == None)\")", v, v))
	case "eq-go":
		g.line(fmt.Sprintf("Console.Println(\"\\(%s == Some{%d}) \\(%s != None) \\(same(%s, %s)) \\(same(find([]string{\"x\"}, %d), Some{\"x\"}))\")",
			g.typed(), g.small(), g.typed(), g.option(), g.option(), g.index()))
	case "eq-structural":
		i := g.r.IntN(3)
		g.line(fmt.Sprintf("Console.Println(\"\\(find(ys, %d) == Some{[]int{%d}}) \\(find(ys, %d) == find(ys, %d)) \" + showS(find(ys, %d)))", i, g.small(), i, g.r.IntN(3), i))
		// A Some whose payload is zero against None: the tags tell them
		// apart.
		g.line(fmt.Sprintf("Console.Println(\"\\(find([][]int{[]int{}}, %d) == find(ys, %d))\")", g.r.IntN(2)-1, 1+g.r.IntN(2)))
		// Options of a union of a slice: None's zero payload is a nil
		// union, which the union's equality does not take.
		g.line(fmt.Sprintf("Console.Println(\"\\(find([]Shape{Dot, Line{Pts: []int{1}}}, %d) == find([]Shape{Line{Pts: []int{%d}}}, %d))\")", g.r.IntN(4)-1, g.r.IntN(2), g.r.IntN(3)-1))
	case "pair":
		g.line(fmt.Sprintf("Console.Println(pair(%s, %s))", g.option(), g.option()))
	case "nested":
		x := []string{fmt.Sprintf("Some{Some{%d}}", g.small()), "Some{None}", "None", fmt.Sprintf("mo.Get(%d)", g.r.IntN(4))}[g.r.IntN(4)]
		g.line(fmt.Sprintf("Console.Println(depth(%s))", x))
	case "holder":
		h, h2 := g.name("h"), g.name("h")
		g.line(fmt.Sprintf("let %s = Holder{Name: \"h\", O: %s, Xs: []Option[int]{%s, %s}, W: Some{%s}}", h, g.option(), g.option(), g.option(), g.option()))
		g.line(fmt.Sprintf("let %s = Holder{..%s, W: %s}", h2, h, []string{"None", fmt.Sprintf("Some{Some{%d}}", g.small())}[g.r.IntN(2)]))
		g.line(fmt.Sprintf("Console.Println(show(%s.O) + \" \" + depth(%s.W) + \" \\(%s == %s) \" + trip(%s) + \" \" + trip(%s))", h, h2, h, h2, h, h2))
	case "fails":
		v := g.name("n")
		g.line(fmt.Sprintf("let %s = must(%s) catch {", v, g.option()))
		g.line("case Missing as e:")
		g.line("\te.At")
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", v))
	case "catch-yield":
		v := g.name("c")
		g.line(fmt.Sprintf("let %s = mustOpt(xs, %d) catch {", v, g.r.IntN(5)-2))
		g.line("case Missing:")
		g.line(fmt.Sprintf("\tSome{%d}", 40+g.small()))
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
	case "store":
		g.line(fmt.Sprintf("opts = opts.Append(%s)", g.option()))
	case "map-value":
		g.line(fmt.Sprintf("mo[%d] = %s", g.r.IntN(4), g.option()))
	case "map-key":
		g.line(fmt.Sprintf("mk[%s] = %d", g.option(), g.small()))
		g.line(fmt.Sprintf("Console.Println(\"\\(or(mk.Get(%s), -1)) \\(len(mk))\")", g.option()))
	case "update":
		g.line(fmt.Sprintf("m = bump(m, %s)", g.key()))
	case "std":
		g.line(fmt.Sprintf("Console.Println(extremes(xs) + \" \" + cut(%q))", []string{"a=b", "ab", "=x"}[g.r.IntN(3)]))
	case "closure":
		f := g.name("f")
		g.line(fmt.Sprintf("let %s = func(i int) Option[int] { find(xs, i + %d) }", f, g.r.IntN(2)))
		g.line(fmt.Sprintf("Console.Println(show(%s(%d)))", f, g.index()))
	case "cell":
		c := g.name("cell")
		g.line(fmt.Sprintf("let %s = Cell[int]{v: %s}", c, g.option()))
		g.line(fmt.Sprintf("Console.Println(show(%s.v) + \" \" + or(Cell[string]{v: find([]string{\"q\"}, %d)}.v, \"none\"))", c, g.index()))
	case "resource":
		g.line(fmt.Sprintf("use(open(%q, %s))", g.name("file"), g.boolean()))
	case "write":
		g.line(fmt.Sprintf("xs[%d] = %d", g.r.IntN(3), g.small()))
		g.line(fmt.Sprintf("ys[%d][0] = %d", g.r.IntN(2), g.small()))
	case "match-expr":
		v := g.name("o")
		g.line(fmt.Sprintf("let %s = match %s {", v, g.typed()))
		g.line("case Some[int] as s:")
		g.line(fmt.Sprintf("\tSome{s.Value + %d}", g.small()))
		g.line("case None:")
		g.line("\tNone")
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
	case "if-expr":
		v := g.name("o")
		g.line(fmt.Sprintf("let %s = if xs[0] > %d { Some{%d} } else { None }", v, g.small(), g.small()))
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
	case "loop-return":
		g.line(fmt.Sprintf("Console.Println(show(firstOver(xs, %d)))", g.small()))
	case "lost-caught":
		v := g.name("k")
		g.line(fmt.Sprintf("let %s = lose(%s) catch {", v, []string{"None", `Some{"why"}`}[g.r.IntN(2)]))
		g.line("case Lost as e:")
		g.line("\tmatch e.Hint {")
		g.line("\tcase Some[string] as h:")
		g.line("\t\tlen(h.Value) + e.Code")
		g.line("\tcase None:")
		g.line("\t\te.Code")
		g.line("\t}")
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", v))
	case "go-extern":
		g.line(fmt.Sprintf(`Console.Println(env(%q) + " " + replaced(%q))`, []string{"PATH", "ERGO_OPT2_NEVER_SET"}[g.r.IntN(2)], []string{"banana", "xyz"}[g.r.IntN(2)]))
	case "timed":
		g.line(fmt.Sprintf("Console.Println(timed(%d))", g.r.IntN(30)))
	case "if-header":
		// A variant compared with an Option: the flat Option built from
		// it stands in the Go if's header.
		s := g.name("s")
		g.line(fmt.Sprintf("let %s = Some{%d}", s, g.small()))
		g.line(fmt.Sprintf("if find(xs, %d) == %s {", g.index(), s))
		g.line("\tConsole.Println(\"header hit\")")
		g.line("}")
	case "niche":
		r := g.name("r")
		g.line(fmt.Sprintf("let %s = repl(%s)", r, g.boolean()))
		g.line(fmt.Sprintf("Console.Println(useRepl(%s, %q) + \" \\(%s == None) \\(%s != None) \\(%s == %s) \\(repl(false) == %s)\")", r, []string{"banana", "xyz"}[g.r.IntN(2)], r, r, r, r, r))
		g.line(fmt.Sprintf("Console.Println(useRepl(again(%s, %s), \"x\"))", r, g.boolean()))
		g.line(fmt.Sprintf("rs = rs.Append(%s)", r))
	case "handle":
		g.line(fmt.Sprintf("useH(openH(%q, %s))", g.name("handle"), g.boolean()))
	case "catch-variant":
		v := g.name("c")
		g.line(fmt.Sprintf("let %s = mustSome(%d) catch {", v, g.r.IntN(4)-2))
		g.line("case Missing:")
		g.line("\tSome{-1}")
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(show(%s) + \" \\(Some{[]int{%d}} == find(ys, %d))\")", v, g.small(), g.r.IntN(3)))
	}
}

// optionProgram is the program of seed.
func optionProgram(seed uint64, reached map[string]bool) (src string, u *unionGen) {
	g := &optionGen{r: rand.New(rand.NewPCG(seed, 0x4f505432)), reached: reached}
	g.u = newUnionGen(g.r, reached)
	g.line("let mut xs = []int{4, 3, 1}")
	g.line("let mut ys = [][]int{[]int{1}, []int{2, 3}}")
	g.line(`let mut m = Map[string, int]{"a": 1, "c": 3}`)
	g.line("let mut opts = []Option[int]{}")
	g.line("let mut mo = Map[int, Option[int]]{}")
	g.line("let mut mk = Map[Option[int], int]{}")
	g.line("let mut rs = []Option[go.Ref[gostrings.Replacer]]{}")
	g.u.lets(g)
	// Every statement path once, in the seed's order, then more drawn at
	// random.
	paths := optionPaths[:len(optionPaths)-optionEnds]
	for _, i := range g.r.Perm(len(paths)) {
		g.stmt(paths[i])
	}
	for range 16 {
		g.stmt(paths[g.r.IntN(len(paths))])
	}
	// Each let mut is written at least once (S110).
	g.line(fmt.Sprintf("xs[2] = %d", g.small()))
	g.line("ys[1][1] = 7")
	g.line(`m["b"] = 2`)
	g.line("opts = opts.Append(None)")
	g.line("mo[3] = None")
	g.line(fmt.Sprintf("mk[Some{%d}] = 9", g.small()))
	g.line("rs = rs.Append(repl(true))")
	g.line("for o in opts {")
	g.line("\tConsole.Println(show(o))")
	g.line("}")
	g.line("for r in rs {")
	g.line("\tConsole.Println(useRepl(r, \"ab\"))")
	g.line("}")
	g.line(`Console.Println(depth(mo.Get(0)) + " " + depth(mo.Get(1)) + " " + depth(mo.Get(2)) + " " + depth(mo.Get(3)))`)
	g.line(`Console.Println(show(m.Get("a")) + " " + show(m.Get("z")) + " \(len(mk))")`)
	g.line(fmt.Sprintf(`Console.Println(oops(%d) + " " + oops(%d))`, g.r.IntN(3)-1, g.r.IntN(3)-1))
	g.u.finish(g)
	// An odd seed passes a go.Ref through a generic function or holds it in
	// a generic record, which keeps the pointer niche off for the whole
	// program (nicheAdmitted).
	g.u.nicheOff = seed%2 == 1
	if g.u.nicheOff && seed%4 == 3 {
		reached["niche off by a record"] = true
		g.line(`Console.Println(useRepl(Cell{v: repl(true)}.v, "abc"))`)
	} else if g.u.nicheOff {
		reached["niche off"] = true
		g.line(`Console.Println(useRepl(idAny(repl(true)), "abc"))`)
	} else {
		reached["niche on"] = true
	}
	switch seed % optionEnds {
	case 3:
		g.reached["end-timer"] = true
		g.line("timedZero()")
	case 0:
		g.reached["end-ok"] = true
	case 1:
		g.reached["end-fail"] = true
		g.line(fmt.Sprintf("let _ = try lose(%s)", []string{"None", `Some{"end"}`}[g.r.IntN(2)]))
	case 2:
		g.reached["end-defect"] = true
		g.line(fmt.Sprintf("match find(xs, %d) {", 5+g.r.IntN(3)))
		g.line("case Some[int] as s:")
		g.line("\tConsole.Println(\"\\(s.Value)\")")
		g.line("case None:")
		g.line("\tUnreachable(\"no element past the end\")")
		g.line("}")
	case 4:
		// An uncaught failure whose text holds a union.
		g.reached["end-u-fail"] = true
		g.line("let _ = try failU0(mkU0(0))")
	case 5:
		g.reached["end-u-defect"] = true
		g.line(fmt.Sprintf("match mkU0(%d) {", g.r.IntN(3)))
		g.line("case U0V0:")
		g.line("\tUnreachable(\"the first variant\")")
		g.line("case _:")
		g.line("\tConsole.Println(\"another variant\")")
		g.line("}")
	}
	return optionPrelude + g.u.decls() + "func main() fails Lost | BadU0 needs Console | go.Foreign {\n" + g.b.String() + "}\n", g.u
}

// lowerOptionMode lowers src's run build, or its test build (test), with
// the flat unions or with the reference (ref): unionLowering "interface";
// noTree lowers every match without its tree (matchLowering "flat"). It
// returns the lowering's counts.
func lowerOptionMode(t *testing.T, name, src string, test, ref, noTree bool) (*lowered, knownStats) {
	t.Helper()
	savedUnion, savedHook, savedMatch := unionLowering, statsHook, matchLowering
	defer func() { unionLowering, statsHook, matchLowering = savedUnion, savedHook, savedMatch }()
	unionLowering = ""
	if noTree {
		matchLowering = "flat"
	}
	if ref {
		unionLowering = "interface"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	if test {
		return lowerTestBuild(t, name, src), stats
	}
	return mustLower(t, name, src), stats
}

// TestFlatUnionsAgree is the seeded property (see above).
//
// It builds Go because the question is what the built programs print and
// how they end (an uncaught failure, a defect, a refuted law and its
// arguments), which no in-process check observes.
func TestFlatUnionsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *flatSeed != 0 {
		seeds = []uint64{*flatSeed}
	}
	reached := map[string]bool{}
	type pair struct {
		seed      uint64
		src       string
		flat, ref string
	}
	var pairs []pair
	var programs []*lowered
	var flatStats, refStats knownStats
	for _, seed := range seeds {
		src, u := optionProgram(seed, reached)
		replay := fmt.Sprintf("Replay: go test -run '^TestFlatUnionsAgree$' ./internal/sketch/lower -args -flat.seed=%d", seed)
		for _, test := range []bool{false, true} {
			p := pair{seed: seed, src: src}
			for _, ref := range []bool{false, true} {
				name := fmt.Sprintf("flat%d", seed)
				if test {
					name += "test"
				}
				if ref {
					name += "ref"
				}
				// Seed 6 (the niche on) lowers its flat programs with no match
				// tree, so the tag tests are reached as well as the switches.
				noTree := seed%6 == 0 && !ref
				if noTree {
					reached["no match tree"] = true
				}
				prog, s := lowerOptionMode(t, name, src, test, ref, noTree)
				programs = append(programs, prog)
				if ref {
					p.ref = name
					refStats = addStats(refStats, s)
					continue
				}
				p.flat = name
				flatStats = addStats(flatStats, s)
				if s.FlatOptions == 0 || s.OptionBails != 0 || s.FlatUnions == 0 || s.UnionBails != 0 {
					t.Errorf("%s: the flat lowering did not keep Option and the unions flat: %+v\n%s", name, s, replay)
				}
				if (s.NicheOptions > 0) == u.nicheOff {
					t.Errorf("%s: the pointer niche is taken %v, want %v: %+v\n%s", name, s.NicheOptions > 0, !u.nicheOff, s, replay)
				}
				// Each generated union is flat exactly where the rule's
				// model says (size, fields, recursion).
				for i := range u.unions {
					flat := strings.Contains(prog.mainGo(), fmt.Sprintf("type U%d struct {", i))
					if want := u.flat(i); flat != want {
						t.Errorf("%s: U%d is flat %v, want %v (%s)\n%s\n%s", name, i, flat, want, u.why(i), replay, src)
					}
				}
			}
			pairs = append(pairs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the flat union programs do not build (seeds %v)", seeds)
	}
	for _, p := range pairs {
		out, errOut, code := runExe(t, exes[p.flat])
		rout, rerr, rcode := runExe(t, exes[p.ref])
		if out != rout || errOut != rerr || code != rcode {
			t.Errorf("seed %d, %s: the flat unions and the reference differ\nReplay: go test -run '^TestFlatUnionsAgree$' ./internal/sketch/lower -args -flat.seed=%d\n%s\n--- flat (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", p.seed, p.flat, p.seed, p.src, code, out, errOut, rcode, rout, rerr)
		}
	}
	t.Logf("seeds %v: flat %+v, reference %+v", seeds, flatStats, refStats)
	if refStats.FlatOptions != 0 || refStats.FlatUnions != 0 || refStats.NicheOptions != 0 {
		t.Errorf("the reference built a flat value: %+v", refStats)
	}
	if *flatSeed != 0 {
		return
	}
	for _, path := range append(optionPaths, unionPaths...) {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q", path)
		}
	}
}

// addStats sums the flat union counts of a and b.
func addStats(a, b knownStats) knownStats {
	a.FlatOptions += b.FlatOptions
	a.OptionBails += b.OptionBails
	a.FlatUnions += b.FlatUnions
	a.UnionBails += b.UnionBails
	a.NicheOptions += b.NicheOptions
	return a
}

// unionPaths are the shapes of generated unions the fixed seeds reach: a
// union of each outcome of the rule, and each kind of field.
var unionPaths = []string{
	"union flat", "union too large", "union too many fields", "union recursive", "union recursive through Option",
	"union in a flat union", "field int", "field string", "field bool", "field slice", "field Option", "field union",
	"niche on", "niche off", "niche off by a record", "no match tree",
}

// gField is a field of a generated variant: its kind and, for a field of
// another union ("u"), that union.
type gField struct {
	name, kind string
	of         int
}

// unionGen writes the generated unions U0, U1, … of one program, their
// helpers, and the statements of the "u-" paths over them.
type unionGen struct {
	r        *rand.Rand
	reached  map[string]bool
	unions   [][][]gField // by union, by variant
	nicheOff bool
}

// fieldKinds are the kinds of a variant's fields: "self" is a field of the
// union itself and "next" an Option of it.
var fieldKinds = []string{"int", "string", "bool", "bool", "bool", "slice", "option", "union", "union", "self", "next"}

func newUnionGen(r *rand.Rand, reached map[string]bool) *unionGen {
	u := &unionGen{r: r, reached: reached}
	n := 3 + r.IntN(2)
	for i := range n {
		nv := 2 + r.IntN(3)
		var variants [][]gField
		for j := range nv {
			nf := r.IntN(6)
			if j == 0 || i == 0 {
				// A base variant, and U0, which later unions hold: small,
				// never recursive.
				nf = r.IntN(2)
			}
			var fields []gField
			seen := map[string]bool{}
			for range nf {
				k := fieldKinds[r.IntN(len(fieldKinds))]
				if i == 0 {
					k = []string{"int", "bool"}[r.IntN(2)]
				}
				if (k == "self" || k == "next") && (j == 0 || r.IntN(2) == 0) || k == "union" && i == 0 {
					k = "int"
				}
				f := gField{kind: k, name: map[string]string{"int": "N", "string": "S", "bool": "B", "slice": "Xs", "option": "O", "union": "W", "self": "R", "next": "Next"}[k]}
				if k == "union" {
					f.of = r.IntN(i)
				}
				for seen[f.name] {
					f.name += "x"
				}
				seen[f.name] = true
				fields = append(fields, f)
				u.reached["field "+map[string]string{"int": "int", "string": "string", "bool": "bool", "slice": "slice", "option": "Option", "union": "union", "self": "int", "next": "int"}[k]] = true
			}
			variants = append(variants, fields)
		}
		u.unions = append(u.unions, variants)
	}
	if r.IntN(2) == 0 {
		// A wide union: one variant of five or six small fields fits the
		// size bound but not the field count bound.
		base := []gField{}
		if r.IntN(2) == 0 {
			base = append(base, gField{kind: "int", name: "N"})
		}
		var wide []gField
		name := "B"
		for range 5 + r.IntN(2) {
			wide = append(wide, gField{kind: "bool", name: name})
			name += "x"
		}
		u.reached["field bool"] = true
		u.unions = append(u.unions, [][]gField{base, wide})
	}
	for i := range u.unions {
		lay, why := u.layout(i, map[int]bool{})
		switch {
		case why == "recursive":
			u.reached["union recursive"] = true
		case why == "recursive through Option":
			u.reached["union recursive through Option"] = true
		case lay.size > 32:
			u.reached["union too large"] = true
		case !lay.ssa:
			u.reached["union too many fields"] = true
		default:
			u.reached["union flat"] = true
			for _, v := range u.unions[i] {
				for _, f := range v {
					if f.kind == "union" && u.flat(f.of) {
						u.reached["union in a flat union"] = true
					}
				}
			}
		}
	}
	return u
}

// layout is the model of the rule (flatunion.go, steps 3 and 4) for the
// generated union i: its Go layout as a flat struct, and why it is not
// flat ("" when it is).
func (u *unionGen) layout(i int, path map[int]bool) (goLayout, string) {
	if path[i] {
		return goLayout{}, "recursive"
	}
	path[i] = true
	defer delete(path, i)
	word := func(size int) goLayout { return goLayout{size: size, align: 8, ssa: true} }
	slots := []goLayout{{size: 1, align: 1, ssa: true}}
	for _, v := range u.unions[i] {
		if len(v) == 0 {
			continue
		}
		var fs []goLayout
		for _, f := range v {
			switch f.kind {
			case "int":
				fs = append(fs, word(8))
			case "string":
				fs = append(fs, word(16))
			case "bool":
				fs = append(fs, goLayout{size: 1, align: 1, ssa: true})
			case "slice":
				fs = append(fs, word(24))
			case "option":
				fs = append(fs, word(16)) // Option[int]: a tag and Some[int]
			case "self":
				return goLayout{}, "recursive"
			case "next":
				return goLayout{}, "recursive through Option"
			case "union":
				if l, why := u.layout(f.of, path); why == "" && l.ssa {
					fs = append(fs, l)
				} else {
					fs = append(fs, word(16)) // an interface
				}
			}
		}
		slots = append(slots, structLayout(fs))
	}
	return structLayout(slots), ""
}

// flat reports whether the rule makes union i flat.
func (u *unionGen) flat(i int) bool {
	lay, why := u.layout(i, map[int]bool{})
	return why == "" && lay.ssa
}

// why is the model's reason for union i's form.
func (u *unionGen) why(i int) string {
	lay, why := u.layout(i, map[int]bool{})
	if why != "" {
		return why
	}
	return fmt.Sprintf("%d bytes, ssa %v", lay.size, lay.ssa)
}

// comparable reports whether union i's values are Go comparable, so a Map
// key: no slice in it.
func (u *unionGen) comparable(i int, seen map[int]bool) bool {
	if seen[i] {
		return true
	}
	seen[i] = true
	for _, v := range u.unions[i] {
		for _, f := range v {
			switch {
			case f.kind == "slice":
				return false
			case f.kind == "union" && !u.comparable(f.of, seen):
				return false
			}
		}
	}
	return true
}

// variantName is the name of variant j of union i.
func variantName(i, j int) string { return fmt.Sprintf("U%dV%d", i, j) }

// lit is a literal of variant j of union i, of depth at most d.
func (u *unionGen) lit(i, j, d int) string {
	v := u.unions[i][j]
	if len(v) == 0 {
		return variantName(i, j)
	}
	var parts []string
	for _, f := range v {
		var x string
		switch f.kind {
		case "int":
			x = fmt.Sprint(u.r.IntN(5) - 1)
		case "string":
			x = fmt.Sprintf("%q", []string{"", "a", "bc"}[u.r.IntN(3)])
		case "bool":
			x = []string{"false", "true"}[u.r.IntN(2)]
		case "slice":
			x = []string{"[]int{}", "[]int{1}", "[]int{2, 3}"}[u.r.IntN(3)]
		case "option":
			x = []string{"None", "Some{1}", "Some{3}"}[u.r.IntN(3)]
		case "union":
			x = u.lit(f.of, u.r.IntN(len(u.unions[f.of])), d-1)
		case "self":
			x = u.lit(i, u.pickVariant(i, d-1), d-1)
		case "next":
			if d <= 0 || u.r.IntN(2) == 0 {
				x = "None"
			} else {
				x = "Some{" + u.lit(i, u.pickVariant(i, d-1), d-1) + "}"
			}
		}
		parts = append(parts, f.name+": "+x)
	}
	return variantName(i, j) + "{" + strings.Join(parts, ", ") + "}"
}

// pickVariant is a variant of union i to write at depth d: the base
// variant 0 when d runs out.
func (u *unionGen) pickVariant(i, d int) int {
	if d <= 0 {
		return 0
	}
	return u.r.IntN(len(u.unions[i]))
}

// any is a literal of a random variant of union i.
func (u *unionGen) any(i int) string { return u.lit(i, u.r.IntN(len(u.unions[i])), 2) }

// decls are the unions' declarations and helpers.
func (u *unionGen) decls() string {
	var b strings.Builder
	b.WriteString("func idAny[T](x T) T { x }\n\n")
	for i, variants := range u.unions {
		var vs []string
		for j, v := range variants {
			var fs []string
			for _, f := range v {
				t := map[string]string{"int": "int", "string": "string", "bool": "bool", "slice": "[]int", "option": "Option[int]", "self": fmt.Sprintf("U%d", i), "next": fmt.Sprintf("Option[U%d]", i)}[f.kind]
				if f.kind == "union" {
					t = fmt.Sprintf("U%d", f.of)
				}
				fs = append(fs, f.name+" "+t)
			}
			vs = append(vs, variantName(i, j)+"{ "+strings.Join(fs, ", ")+" }")
		}
		fmt.Fprintf(&b, "type U%d = %s\n\n", i, strings.Join(vs, " | "))
		// show: every field of every variant.
		fmt.Fprintf(&b, "func showU%d(u U%d) string {\n\tmatch u {\n", i, i)
		for j, v := range variants {
			if len(v) == 0 {
				fmt.Fprintf(&b, "\tcase %s:\n\t\t\"V%d\"\n", variantName(i, j), j)
				continue
			}
			var parts []string
			for _, f := range v {
				switch f.kind {
				case "int", "bool":
					parts = append(parts, fmt.Sprintf("\"\\(x.%s)\"", f.name))
				case "string":
					parts = append(parts, "x."+f.name)
				case "slice":
					parts = append(parts, fmt.Sprintf("\"\\(len(x.%s))\"", f.name))
				case "option":
					parts = append(parts, "show(x."+f.name+")")
				case "union":
					parts = append(parts, fmt.Sprintf("showU%d(x.%s)", f.of, f.name))
				case "self":
					parts = append(parts, fmt.Sprintf("showU%d(x.%s)", i, f.name))
				case "next":
					parts = append(parts, fmt.Sprintf("showOU%d(x.%s)", i, f.name))
				}
			}
			fmt.Fprintf(&b, "\tcase %s as x:\n\t\t\"V%d(\" + %s + \")\"\n", variantName(i, j), j, strings.Join(parts, " + \",\" + "))
		}
		b.WriteString("\t}\n}\n\n")
		fmt.Fprintf(&b, "func showOU%d(o Option[U%d]) string {\n\tmatch o {\n\tcase Some[U%d] as s:\n\t\t\"some \" + showU%d(s.Value)\n\tcase None:\n\t\t\"none\"\n\t}\n}\n\n", i, i, i, i)
		// mk: variant k % n, built where the function returns.
		fmt.Fprintf(&b, "func mkU%d(k int) U%d {\n", i, i)
		for j := range variants[:len(variants)-1] {
			fmt.Fprintf(&b, "\tif k %% %d == %d {\n\t\treturn %s\n\t}\n", len(variants), j, u.lit(i, j, 2))
		}
		fmt.Fprintf(&b, "\t%s\n}\n\n", u.lit(i, len(variants)-1, 2))
		// A record of the union and an Option of it, for wire's codec.
		fmt.Fprintf(&b, "type HU%d { V U%d, W Option[U%d] }\n\n", i, i, i)
		fmt.Fprintf(&b, "func tripU%d(h HU%d) string {\n\tlet back = wire.RoundTrip(h) catch {\n\tcase _:\n\t\treturn \"trip failed\"\n\t}\n\t\"trip \\(back == h) \" + showU%d(back.V) + \" \" + showOU%d(back.W)\n}\n\n", i, i, i, i)
		// A failure that holds the union: its text is the union's.
		fmt.Fprintf(&b, "type BadU%d { V U%d }\n\n", i, i)
		fmt.Fprintf(&b, "func failU%d(u U%d) string fails BadU%d {\n\tmatch u {\n\tcase %s:\n\t\treturn BadU%d{V: u}\n\tcase _:\n\t\tshowU%d(u)\n\t}\n}\n\n", i, i, i, variantName(i, 0), i, i)
		fmt.Fprintf(&b, "func pairU%d(a U%d, b U%d) string {\n\tmatch (a, b) {\n\tcase (%s, %s):\n\t\t\"both\"\n\tcase (%s, _):\n\t\t\"left\"\n\tcase (_, %s):\n\t\t\"right\"\n\tcase _:\n\t\t\"neither\"\n\t}\n}\n\n", i, i, i, variantName(i, 0), variantName(i, 0), variantName(i, 0), variantName(i, 0))
		// A law the generator refutes: its counterexample prints the
		// union's literal.
		fmt.Fprintf(&b, "law U%dIsFirst(u U%d) {\n\tmatch u {\n\tcase %s:\n\t\ttrue\n\tcase _:\n\t\tfalse\n\t}\n}\n\n", i, i, variantName(i, 0))
	}
	return b.String()
}

// lets are main's lets over the unions.
func (u *unionGen) lets(g *optionGen) {
	for i := range u.unions {
		g.line(fmt.Sprintf("let mut us%d = []U%d{}", i, i))
		if u.comparable(i, map[int]bool{}) {
			g.line(fmt.Sprintf("let mut mu%d = Map[U%d, int]{}", i, i))
		}
	}
}

// finish writes each let mut once and prints what the paths stored.
func (u *unionGen) finish(g *optionGen) {
	for i := range u.unions {
		g.line(fmt.Sprintf("us%d = us%d.Append(mkU%d(1))", i, i, i))
		g.line(fmt.Sprintf("for x in us%d {", i))
		g.line(fmt.Sprintf("\tConsole.Println(showU%d(x))", i))
		g.line("}")
		if u.comparable(i, map[int]bool{}) {
			g.line(fmt.Sprintf("mu%d[mkU%d(0)] = 1", i, i))
			g.line(fmt.Sprintf("Console.Println(\"keys \\(len(mu%d))\")", i))
		}
	}
}

// stmt writes the statements of the path over a random union.
func (u *unionGen) stmt(g *optionGen, path string) {
	i := u.r.IntN(len(u.unions))
	n := len(u.unions[i])
	k := func() int { return u.r.IntN(2 * n) }
	switch path {
	case "u-show":
		g.line(fmt.Sprintf("Console.Println(showU%d(%s))", i, u.any(i)))
	case "u-mk":
		g.line(fmt.Sprintf("Console.Println(showU%d(mkU%d(%d)))", i, i, k()))
	case "u-eq":
		g.line(fmt.Sprintf("Console.Println(\"\\(mkU%d(%d) == mkU%d(%d)) \\(mkU%d(%d) != %s) \\(%s == mkU%d(%d))\")", i, k(), i, k(), i, k(), u.any(i), u.any(i), i, k()))
	case "u-map":
		if !u.comparable(i, map[int]bool{}) {
			g.line(fmt.Sprintf("Console.Println(showU%d(mkU%d(%d)))", i, i, k()))
			return
		}
		g.line(fmt.Sprintf("mu%d[mkU%d(%d)] = %d", i, i, k(), g.small()))
		g.line(fmt.Sprintf("mu%d[%s] = %d", i, u.any(i), g.small()))
		g.line(fmt.Sprintf("Console.Println(\"\\(or(mu%d.Get(mkU%d(%d)), -1)) \\(len(mu%d))\")", i, i, k(), i))
	case "u-slice":
		g.line(fmt.Sprintf("us%d = us%d.Append(%s)", i, i, u.any(i)))
	case "u-wire":
		w := []string{"None", fmt.Sprintf("Some{mkU%d(%d)}", i, k())}[u.r.IntN(2)]
		g.line(fmt.Sprintf("Console.Println(tripU%d(HU%d{V: mkU%d(%d), W: %s}))", i, i, i, k(), w))
	case "u-fail":
		v := g.name("f")
		g.line(fmt.Sprintf("let %s = failU%d(mkU%d(%d)) catch {", v, i, i, k()))
		g.line(fmt.Sprintf("case BadU%d as e:", i))
		g.line(fmt.Sprintf("\t\"caught \" + showU%d(e.V)", i))
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(%s)", v))
	case "u-tuple":
		g.line(fmt.Sprintf("Console.Println(pairU%d(mkU%d(%d), mkU%d(%d)))", i, i, k(), i, k()))
	case "u-yield":
		v, w := g.name("y"), g.name("y")
		g.line(fmt.Sprintf("let %s U%d = if xs[0] > %d { %s } else { %s }", v, i, g.small(), u.any(i), u.any(i)))
		g.line(fmt.Sprintf("let %s U%d = match mkU%d(%d) {", w, i, i, k()))
		g.line(fmt.Sprintf("case %s:", variantName(i, 0)))
		g.line("\t" + u.any(i))
		g.line("case _:")
		g.line(fmt.Sprintf("\tmkU%d(%d)", i, k()))
		g.line("}")
		g.line(fmt.Sprintf("Console.Println(showU%d(%s) + \" \" + showU%d(%s))", i, v, i, w))
	case "u-generic":
		g.line(fmt.Sprintf("Console.Println(showU%d(or(find([]U%d{mkU%d(%d)}, %d), mkU%d(%d))) + \" \" + showOU%d(find([]U%d{%s}, %d)))", i, i, i, k(), g.index(), i, k(), i, i, u.any(i), g.index()))
	}
}
