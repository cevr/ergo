package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// The type-argument axis of TestFlatUnionsAgree (MSB1, genericunion.go):
// a generic union G of the program, instantiated with an enum, basic
// types, a generated union U0 (flat, shared or an interface by its seed),
// an Option and a slice, built by functions neither inliner takes, passed
// through generic functions, stored in slices, Maps and records, compared,
// and, in every third seed, held by a record through itself (GLoop), which
// keeps G an interface; and a machine whose state and signal are enums,
// with a seeded transition table, run by std machines.Run and by a loop
// that calls its step and matches the Decision it returns. Each program
// is lowered with every mode of TestFlatUnionsAgree, and genericForms
// says the form each mode gives G, the prelude's Decision and the
// machine's state.

// genericPaths are the type-argument axis's statements; the fixed seeds
// reach each.
var genericPaths = []string{
	"g-mk", "g-swap", "g-slice", "g-eq", "g-map", "g-hold", "g-union-arg", "g-option-arg",
	"g-generic-pass", "g-machine-run", "g-machine-scan",
}

// genericGen writes the axis's declarations and statements for one seed.
type genericGen struct {
	r     *rand.Rand
	cycle bool // GLoop holds itself through G: G stays an interface
	n     int
}

// genericDecls are the declarations: G and its variants, the enum E3, the
// machine GM with a table drawn from r, and the helpers.
func (g *genericGen) decls() string {
	var b strings.Builder
	b.WriteString(`type GA[A] { x A }
type GB[A, B] { x A, y B }
type GC {}
type GD[B] { y B }
type G[A, B] = GA[A] | GB[A, B] | GC | GD[B]

type EA {}
type EB {}
type EC {}
type E3 = EA | EB | EC

type GHold { g G[E3, int], n int }

func e3(k int) E3 {
	match k % 3 {
	case 0:
		EA
	case 1:
		EB
	case _:
		EC
	}
}

func showE3(e E3) string {
	match e {
	case EA:
		"a"
	case EB:
		"b"
	case EC:
		"c"
	}
}

func showInt(i int) string { "\(i)" }

func showStr(s string) string { s }

func showBool(v bool) string { "\(v)" }

func showSl(xs []int) string { "\(len(xs))" }

func gshow[A, B](g G[A, B], sa func(a A) string, sb func(b B) string) string {
	match g {
	case GA[A] as a:
		"A" + sa(a.x)
	case GB[A, B] as v:
		"B" + sa(v.x) + "," + sb(v.y)
	case GC:
		"C"
	case GD[B] as d:
		"D" + sb(d.y)
	}
}

func gswap[A, B](g G[A, B]) G[B, A] {
	match g {
	case GA[A] as a:
		GD[A]{y: a.x}
	case GB[A, B] as v:
		GB[B, A]{x: v.y, y: v.x}
	case GC:
		GC
	case GD[B] as d:
		GA[B]{x: d.y}
	}
}

func gsame(a G[int, string], b G[int, string]) bool { a == b }

func mkGU(k int) G[U0, bool] {
	if k % 2 == 0 {
		return GA[U0]{x: mkU0(k)}
	}
	GB[U0, bool]{x: mkU0(k + 1), y: k > 2}
}

func mkGO(k int) G[Option[int], []int] {
	match k % 3 {
	case 0:
		GB[Option[int], []int]{x: Some{k}, y: []int{1, k}}
	case 1:
		GA[Option[int]]{x: None}
	case _:
		GD[[]int]{y: []int{k}}
	}
}

`)
	t1, t2 := 1+g.r.IntN(3), 3+g.r.IntN(4)
	// mkG and mkS are past the inliners' budgets: their results are
	// returned from a Go call.
	fmt.Fprintf(&b, `func mkG(k int) G[E3, int] {
	if k < %d {
		return GA[E3]{x: e3(k)}
	}
	if k == %d {
		return GC
	}
	match k %% 4 {
	case 0:
		GB[E3, int]{x: e3(k + 1), y: k * 3}
	case 1:
		GD[int]{y: k}
	case 2:
		if k > 9 {
			GB[E3, int]{x: e3(k * 5), y: k - 9}
		} else {
			GD[int]{y: k * k}
		}
	case _:
		GA[E3]{x: e3(k * 7)}
	}
}

func mkS(k int) G[int, string] {
	if k == %d {
		return GC
	}
	match k %% 3 {
	case 0:
		GB[int, string]{x: k, y: "s\(k)"}
	case 1:
		GD[string]{y: "d\(k)"}
	case _:
		if k > 4 {
			GA[int]{x: k * 2}
		} else {
			GB[int, string]{x: -k, y: ""}
		}
	}
}

`, t1, t2, g.r.IntN(5))
	if g.cycle {
		b.WriteString("type GLoop { g G[int, GLoop] }\n\nfunc gloop(l GLoop) string {\n\tmatch l.g {\n\tcase GD[GLoop] as d:\n\t\t\"D\" + gloop(d.y)\n\tcase _:\n\t\t\"end\"\n\t}\n}\n\n")
	}
	// The machine: enum state and signal, a step over a drawn table.
	states, signals := []string{"M0", "M1", "M2", "M3"}, []string{"I0", "I1", "I2"}
	for _, s := range append(append([]string{}, states...), signals...) {
		b.WriteString("type " + s + " {}\n")
	}
	b.WriteString("type MS = M0 | M1 | M2 | M3\ntype MI = I0 | I1 | I2\n\n")
	b.WriteString("func showMS(s MS) string {\n\tmatch s {\n")
	for _, s := range states {
		fmt.Fprintf(&b, "\tcase %s:\n\t\t%q\n", s, s)
	}
	b.WriteString("\t}\n}\n\n")
	b.WriteString("func mstep(s MS, e MI) Decision[MS, E3] {\n\tmatch (s, e) {\n")
	outs := []string{"EA", "EB", "EC"}
	for _, s := range states {
		for _, e := range signals {
			next := states[g.r.IntN(len(states))]
			var d string
			switch g.r.IntN(8) {
			case 0, 1:
				d = "Enter[MS]{Next: " + next + "}"
			case 2, 3:
				d = "Stay[MS]{Next: " + s + "}"
			case 4, 5:
				d = "Emit[MS, E3]{Next: " + next + ", Output: " + outs[g.r.IntN(3)] + "}"
			case 6:
				d = "Ignore"
			default:
				d = "Done[E3]{Output: " + outs[g.r.IntN(3)] + "}"
			}
			if s == "M0" && e == "I0" {
				d = "Enter[MS]{Next: M1}" // a run moves at least once
			}
			fmt.Fprintf(&b, "\tcase (%s, %s):\n\t\t%s\n", s, e, d)
		}
	}
	b.WriteString("\t}\n}\n\n")
	b.WriteString(`type GM machine[MS, MI] {
	initial: M0,
	step:    mstep,
}

func runGM(sigs []MI) string {
	let o = machines.Run(GM{}, sigs) catch {
	case machines.Rejected:
		return "rejected"
	}
	match o {
	case machines.Running[MS, E3] as r:
		"running " + showMS(r.State) + " \(len(r.Emitted))"
	case machines.Finished[MS, E3]:
		"finished"
	}
}

func scanGM(sigs []MI) string {
	let mut s MS = M0
	let mut out = ""
	for e in sigs {
		match mstep(s, e) {
		case Enter[MS] as v:
			s = v.Next
		case Stay[MS] as v:
			s = v.Next
		case Emit[MS, E3] as v:
			s = v.Next
			out = out + showE3(v.Output)
		case Ignore:
			out = out + "."
		case Reject:
			out = out + "!"
		case Done[E3] as v:
			return out + " done " + showE3(v.Output)
		}
	}
	out + " at " + showMS(s)
}

`)
	return b.String()
}

// k is a small argument.
func (g *genericGen) k() int { return g.r.IntN(14) }

// sigs is a list of the machine's signals.
func (g *genericGen) sigs() string {
	var xs []string
	for range 1 + g.r.IntN(9) {
		xs = append(xs, fmt.Sprintf("I%d", g.r.IntN(3)))
	}
	return "[]MI{" + strings.Join(xs, ", ") + "}"
}

// stmt writes the statement of path into o's main.
func (g *genericGen) stmt(o *optionGen, path string) {
	o.reached[path] = true
	g.n++
	switch path {
	case "g-mk":
		o.line(fmt.Sprintf("Console.Println(gshow(mkG(%d), showE3, showInt))", g.k()))
	case "g-swap":
		o.line(fmt.Sprintf("Console.Println(gshow(gswap(mkS(%d)), showStr, showInt))", g.k()))
	case "g-slice":
		v := fmt.Sprintf("gs%d", g.n)
		o.line(fmt.Sprintf("let %s = []G[int, string]{mkS(%d), mkS(%d), GC}", v, g.k(), g.k()))
		o.line(fmt.Sprintf("for x in %s {", v))
		o.line("\tConsole.Println(gshow(x, showInt, showStr))")
		o.line("}")
	case "g-eq":
		a := g.k()
		b := a
		if g.r.IntN(2) == 0 {
			b = g.k()
		}
		o.line(fmt.Sprintf("Console.Println(\"\\(gsame(mkS(%d), mkS(%d)))\")", a, b))
	case "g-map":
		v := fmt.Sprintf("gm%d", g.n)
		o.line(fmt.Sprintf("let %s = Map[int, G[E3, int]]{%d: mkG(%d), %d: GC}", v, g.k(), g.k(), 20+g.k()))
		o.line(fmt.Sprintf("match %s.Get(%d) {", v, g.k()))
		o.line("case Some[G[E3, int]] as s:")
		o.line("\tConsole.Println(gshow(s.Value, showE3, showInt))")
		o.line("case None:")
		o.line("\tConsole.Println(\"no G\")")
		o.line("}")
	case "g-hold":
		v := fmt.Sprintf("gh%d", g.n)
		o.line(fmt.Sprintf("let %s = GHold{g: mkG(%d), n: %d}", v, g.k(), g.k()))
		o.line(fmt.Sprintf("Console.Println(gshow(%s.g, showE3, showInt) + \" \\(%s.n)\")", v, v))
	case "g-union-arg":
		o.line(fmt.Sprintf("Console.Println(gshow(mkGU(%d), showU0, showBool))", g.k()))
	case "g-option-arg":
		o.line(fmt.Sprintf("Console.Println(gshow(mkGO(%d), show, showSl))", g.k()))
	case "g-generic-pass":
		o.line(fmt.Sprintf("Console.Println(gshow(idAny(mkG(%d)), showE3, showInt))", g.k()))
	case "g-machine-run":
		o.line(fmt.Sprintf("Console.Println(runGM(%s))", g.sigs()))
	case "g-machine-scan":
		o.line(fmt.Sprintf("Console.Println(scanGM(%s))", g.sigs()))
	}
}

// stmts writes each path once in the seed's order, then more at random,
// and GLoop's use in a cycle seed.
func (g *genericGen) stmts(o *optionGen) {
	for _, i := range g.r.Perm(len(genericPaths)) {
		g.stmt(o, genericPaths[i])
	}
	for range 6 {
		g.stmt(o, genericPaths[g.r.IntN(len(genericPaths))])
	}
	if g.cycle {
		o.reached["g-cycle"] = true
		o.line(`Console.Println(gloop(GLoop{g: GD[GLoop]{y: GLoop{g: GD[GLoop]{y: GLoop{g: GC}}}}}))`)
	}
}

// genericForm is the form of the generic Go type whose declaration starts
// with head: "interface", "slots" (a slot per variant) or "shared".
func genericForm(main, head string) string {
	at := strings.Index(main, head)
	if at < 0 {
		return "missing"
	}
	rest := main[at+len(head):]
	if strings.HasPrefix(strings.TrimSpace(rest), "interface") {
		return "interface"
	}
	body := rest[:strings.Index(rest, "}")]
	for _, line := range strings.Split(body, "\n") {
		if end := strings.LastIndex(line, "*/"); end >= 0 {
			line = line[end+2:]
		}
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "v") {
			return "slots"
		}
	}
	return "shared"
}

// genericForms checks the forms the mode gives G, Decision and the
// machine's state MS, and returns what differs.
func (g *genericGen) genericForms(main, mode string) []string {
	want := map[string]string{"": "shared", "register": "shared", "slots": "slots", "interface": "interface"}[mode]
	wantG := want
	if g.cycle {
		wantG = "interface"
	}
	var bad []string
	check := func(name, head, want string) {
		if got := genericForm(main, head); got != want {
			bad = append(bad, fmt.Sprintf("%s is %s, want %s", name, got, want))
		}
	}
	check("G", "type G[A any, B any] ", wantG)
	check("Decision", "type Decision[S any, O any] ", want)
	// An enum is a tag alone: shared and slots alike.
	enum := "shared"
	if mode == "interface" {
		enum = "interface"
	}
	check("the machine's state MS", "type MS ", enum)
	return bad
}
