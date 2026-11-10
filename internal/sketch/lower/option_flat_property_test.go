package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of the flat Option (flatunion.go, OPT2). Each seed
// writes one program: a fixed set of declarations that take Options apart
// and build them in every place the lowering spells one (a generic result,
// a Map read matched at once and kept, a std comma-ok result, maps.Update's
// callback, a record field, a slice and a Map element, a Map key, an
// Option of an Option, a tuple match, constant patterns, Go's == and the
// structural ==, a failure that holds an Option, wire's codec, a resource
// payload whose Close the arm calls, a machine state that holds an Option,
// generated and listed law arguments), and a main of random statements over
// them, ending normally, in an uncaught failure whose text holds an Option,
// or in a defect.
//
// The program's run build and test build are lowered with the flat Option
// and with the reference, form A for every union (unionLowering
// "interface"); each pair must print the same stdout and stderr and exit
// with the same code. A failing seed prints the Ergo source, both outputs
// and its replay command. The fixed seeds reach every generator path, the
// flat lowering builds flat Options and bails on none, and the reference
// builds none.

var optionSeed = flag.Uint64("option.seed", 0, "run TestOptionFlatAgrees on this seed alone")

// optionPaths are the generator's branches; the fixed seeds reach each.
var optionPaths = []string{
	"find", "find-string", "get-match", "get-bound", "eq-go", "eq-structural", "pair", "nested",
	"holder", "fails", "catch-yield", "store", "map-value", "map-key", "update", "std", "closure",
	"cell", "resource", "write", "match-expr", "if-expr", "loop-return", "lost-caught",
	"go-extern", "timed", "catch-variant",
	// The ends, one per seed (seed % 4).
	"end-ok", "end-fail", "end-defect", "end-timer",
}

// optionEnds is the number of ends at the tail of optionPaths.
const optionEnds = 4

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

type Tri = Lo{N int, Last Option[bool]} | Hi{}

type Kick = Up{} | Flip{}

func kick(t Tri, k Kick) Decision[Tri, int] {
	match t {
	case Lo as l:
		match k {
		case Up:
			if l.N >= 2 { Enter[Tri]{Next: Hi} } else { Stay[Tri]{Next: Lo{N: l.N + 1, Last: l.Last}} }
		case Flip:
			match l.Last {
			case Some{Value: true}:
				Stay[Tri]{Next: Lo{N: l.N, Last: Some{false}}}
			case Some{Value: false}:
				Stay[Tri]{Next: Lo{N: l.N, Last: None}}
			case None:
				Stay[Tri]{Next: Lo{N: l.N, Last: Some{true}}}
			}
		}
	case Hi:
		Ignore
	}
}

type Gauge machine[Tri, Kick] {
	initial: Lo{N: 0, Last: None},
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

func (g *optionGen) stmt() {
	path := optionPaths[g.r.IntN(len(optionPaths)-optionEnds)]
	g.reached[path] = true
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
func optionProgram(seed uint64, reached map[string]bool) string {
	g := &optionGen{r: rand.New(rand.NewPCG(seed, 0x4f505432)), reached: reached}
	g.line("let mut xs = []int{4, 3, 1}")
	g.line("let mut ys = [][]int{[]int{1}, []int{2, 3}}")
	g.line(`let mut m = Map[string, int]{"a": 1, "c": 3}`)
	g.line("let mut opts = []Option[int]{}")
	g.line("let mut mo = Map[int, Option[int]]{}")
	g.line("let mut mk = Map[Option[int], int]{}")
	for range 24 {
		g.stmt()
	}
	// Each let mut is written at least once (S110).
	g.line(fmt.Sprintf("xs[2] = %d", g.small()))
	g.line("ys[1][1] = 7")
	g.line(`m["b"] = 2`)
	g.line("opts = opts.Append(None)")
	g.line("mo[3] = None")
	g.line(fmt.Sprintf("mk[Some{%d}] = 9", g.small()))
	g.line("for o in opts {")
	g.line("\tConsole.Println(show(o))")
	g.line("}")
	g.line(`Console.Println(depth(mo.Get(0)) + " " + depth(mo.Get(1)) + " " + depth(mo.Get(2)) + " " + depth(mo.Get(3)))`)
	g.line(`Console.Println(show(m.Get("a")) + " " + show(m.Get("z")) + " \(len(mk))")`)
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
	}
	return optionPrelude + "func main() fails Lost needs Console | go.Foreign {\n" + g.b.String() + "}\n"
}

// lowerOptionMode lowers src's run build, or its test build (test), with
// the flat Option or with the reference (ref): unionLowering
// "interface". It returns the lowering's counts.
func lowerOptionMode(t *testing.T, name, src string, test, ref bool) (*lowered, knownStats) {
	t.Helper()
	savedUnion, savedHook := unionLowering, statsHook
	defer func() { unionLowering, statsHook = savedUnion, savedHook }()
	unionLowering = ""
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

// TestOptionFlatAgrees is the seeded property (see above).
//
// It builds Go because the question is what the built programs print and
// how they end (an uncaught failure, a defect, a refuted law and its
// arguments), which no in-process check observes.
func TestOptionFlatAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *optionSeed != 0 {
		seeds = []uint64{*optionSeed}
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
		src := optionProgram(seed, reached)
		for _, test := range []bool{false, true} {
			p := pair{seed: seed, src: src}
			for _, ref := range []bool{false, true} {
				name := fmt.Sprintf("opt%d", seed)
				if test {
					name += "test"
				}
				if ref {
					name += "ref"
				}
				prog, s := lowerOptionMode(t, name, src, test, ref)
				programs = append(programs, prog)
				if ref {
					p.ref = name
					refStats.FlatOptions += s.FlatOptions
					refStats.OptionBails += s.OptionBails
				} else {
					p.flat = name
					flatStats.FlatOptions += s.FlatOptions
					flatStats.OptionBails += s.OptionBails
					if s.FlatOptions == 0 || s.OptionBails != 0 {
						t.Errorf("%s: the flat lowering did not keep Option flat: %+v", name, s)
					}
				}
			}
			pairs = append(pairs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the Option programs do not build (seeds %v)", seeds)
	}
	for _, p := range pairs {
		out, errOut, code := runExe(t, exes[p.flat])
		rout, rerr, rcode := runExe(t, exes[p.ref])
		if out != rout || errOut != rerr || code != rcode {
			t.Errorf("seed %d, %s: the flat Option and the reference differ\nReplay: go test -run '^TestOptionFlatAgrees$' ./internal/sketch/lower -args -option.seed=%d\n%s\n--- flat (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", p.seed, p.flat, p.seed, p.src, code, out, errOut, rcode, rout, rerr)
		}
	}
	t.Logf("seeds %v: flat %+v, reference %+v", seeds, flatStats, refStats)
	if refStats.FlatOptions != 0 {
		t.Errorf("the reference built a flat Option: %+v", refStats)
	}
	if *optionSeed != 0 {
		return
	}
	for _, path := range optionPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q", path)
		}
	}
}
