package check

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The inserted-operations property (G9; docs/spec/inserted.md). The
// compiler inserts operations that no source text names: releases, the
// adapter of an operation-type conversion, a codec's by-name calls, a
// generic body's instance, the borrow of a dot call's receiver, a call
// site, a zero value. Each must obey the rules of the written form it
// stands for. Each seed writes a program of sites; each site is one
// inserted operation in a function sN. An elaboration rule writes the same
// site with the operation spelled out (inserted.md, Elaboration rules):
// the program it gives is the written form, and the checker reads it as
// any program. The checker's verdict on each site (the codes of the
// diagnostics in the site's lines) and the inferred rows of sN must be the
// same in both programs.
//
// A disagreement is a hole: the inserted operation skipped a rule its
// written form obeys. insertedGap names the open holes, by the finding
// that owns each; the property fails on a disagreement it does not name
// and on a named one that no longer occurs, so the unit that closes a
// hole removes its line.
//
// Replay: go test -run '^TestInsertedOpsAgree$' ./internal/sketch/check -args -insops.seed=N
// Survey (every disagreement, no failure): add -insops.survey.

var (
	insopsSeed   = flag.Uint64("insops.seed", 0, "run TestInsertedOpsAgree on this seed alone")
	insopsSurvey = flag.Bool("insops.survey", false, "TestInsertedOpsAgree prints every disagreement and fails on none")
)

// insParams are the rows of a program's prelude functions.
type insParams struct {
	closeNeeds bool   // Close(r Res) needs Console
	addNeeds   bool   // Add(t *Tally, k int) needs Console
	wireNeeds  bool   // Wire(l Level) string needs Console
	parseNeeds bool   // ParseLevel(s string) Level fails Bad needs Console
	getFails   string // Get(s Src)'s fails row: "", "Bad" or "Other"
	getNeeds   bool   // Get(s Src) needs Console
}

// insSite is one inserted operation: the declarations of the implicit
// form and of its written form. Each declares the function name.
type insSite struct {
	rule, variant string
	name          string
	implicit      string
	written       string
}

func needsClause(on bool) string {
	if on {
		return " needs Console"
	}
	return ""
}

func (p insParams) prelude() string {
	var b strings.Builder
	b.WriteString("type Res { id int }\n\n")
	if p.closeNeeds {
		b.WriteString("func Close(r Res) needs Console { Console.Println(\"close \\(r.id)\") }\n\n")
	} else {
		b.WriteString("func Close(r Res) {}\n\n")
	}
	b.WriteString(`type Pair { a Res, b Res }

type Closer { Close() }

func mk(id int) Res { Res{id: id} }

func lend(r Res) {}

type Tally { n int }

`)
	if p.addNeeds {
		b.WriteString("func Add(t *Tally, k int) needs Console {\n\tt.n = t.n + k\n\tConsole.Println(\"add\")\n}\n\n")
	} else {
		b.WriteString("func Add(t *Tally, k int) { t.n = t.n + k }\n\n")
	}
	b.WriteString(`func Two(x *Tally, y *Tally) {
	x.n += 1
	y.n += 2
}

type Holder { t Tally }

type Level { name string }

type Bad { Text string }

type Other { Text string }

`)
	b.WriteString("func ParseLevel(s string) Level fails Bad" + needsClause(p.parseNeeds) + " {\n")
	if p.parseNeeds {
		b.WriteString("\tConsole.Println(s)\n")
	}
	b.WriteString("\tif s == \"\" {\n\t\treturn Bad{Text: \"empty\"}\n\t}\n\tLevel{name: s}\n}\n\n")
	b.WriteString("func Wire(l Level) string" + needsClause(p.wireNeeds) + " {\n")
	if p.wireNeeds {
		b.WriteString("\tConsole.Println(l.name)\n")
	}
	b.WriteString("\tl.name\n}\n\n")
	b.WriteString(`type Count int where it >= 0

type Port int where 1 <= it && it <= 65535

type Empty {}

type Walk { steps int }

type Getter { Get() int fails Bad }

type Src { n int }

`)
	b.WriteString("func Get(s Src) int" + p.getRow() + " {\n")
	if p.getNeeds {
		b.WriteString("\tConsole.Println(\"get\")\n")
	}
	if p.getFails != "" {
		fmt.Fprintf(&b, "\tif s.n < 0 {\n\t\treturn %s{Text: \"neg\"}\n\t}\n", p.getFails)
	}
	b.WriteString("\ts.n\n}\n\n")
	b.WriteString(`func useGetter(g Getter) int fails Bad { try g.Get() }

func check(n int) fails Bad {
	if n > 9 {
		return Bad{Text: "big"}
	}
}

func at(n int, s CallSite) int { n + s.Line }

func pass[T](x T) T { x }

func wrap[T](x T) []T { []T{x} }

func hold[T](x T, use escaping func(v T)) func() {
	func() { use(x) }
}

func run[E, R](f func() int fails E needs R) int fails E needs R {
	try f()
}

func main() {}

`)
	return b.String()
}

func (p insParams) getRow() string {
	s := ""
	if p.getFails != "" {
		s += " fails " + p.getFails
	}
	return s + needsClause(p.getNeeds)
}

// The elaboration rules. Each takes the site's choices and writes the
// implicit form and the written form of one inserted operation.

// insReleaseLocal: a local's release at the end of its owner scope
// (resources R5, R6) is the call x.Close() as the scope's last statement.
// A return or a failure that leaves the scope releases it there too: the
// written form closes x before the return, and in a catch arm that passes
// the failure on (rows.md, Catch).
func insReleaseLocal(r *rand.Rand, name string) insSite {
	ctx := []string{"body", "loop", "if", "frame", "return", "fail"}[r.IntN(6)]
	typ := []string{"Res", "Closer"}[r.IntN(2)]
	use := "lend(x)"
	if typ == "Closer" {
		use = "keep" + name + "(x)"
	}
	write := func(release string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "func keep%s(c Closer) {}\n\n", name)
		fmt.Fprintf(&b, "func %s(n int) {\n", name)
		ind := "\t"
		switch ctx {
		case "loop":
			b.WriteString("\tfor i in 0..n {\n")
			ind = "\t\t"
		case "if":
			b.WriteString("\tif n > 0 {\n")
			ind = "\t\t"
		case "frame":
			b.WriteString("\tlet f = func() {\n")
			ind = "\t\t"
		}
		if ctx == "loop" {
			fmt.Fprintf(&b, "%slet x %s = mk(i)\n", ind, typ)
		} else {
			fmt.Fprintf(&b, "%slet x %s = mk(n)\n", ind, typ)
		}
		early := ""
		if release != "" {
			early = "\t\t" + release
		}
		switch ctx {
		case "return":
			fmt.Fprintf(&b, "\tif n > 5 {\n%s\t\treturn\n\t}\n", early)
		case "fail":
			if release == "" {
				b.WriteString("\ttry check(n)\n")
			} else {
				fmt.Fprintf(&b, "\tcheck(n) catch {\n\tcase Bad as e:\n%s\t\treturn e\n\t}\n", early)
			}
		}
		fmt.Fprintf(&b, "%s%s\n", ind, use)
		if release != "" {
			b.WriteString(ind + release)
		}
		switch ctx {
		case "loop", "if":
			b.WriteString("\t}\n")
		case "frame":
			b.WriteString("\t}\n\tf()\n")
		}
		b.WriteString("}\n")
		return b.String()
	}
	return insSite{rule: "release-local", variant: ctx + "/" + typ, name: name,
		implicit: write(""),
		written:  write("x.Close()\n")}
}

// insReleaseField: an aggregate's release (R3) is its fields' releases,
// last declared first.
func insReleaseField(r *rand.Rand, name string) insSite {
	head := fmt.Sprintf("func %s() {\n\tlet p = Pair{a: mk(1), b: mk(2)}\n\tlend(p.a)\n", name)
	return insSite{rule: "release-field", name: name,
		implicit: head + "}\n",
		written:  head + "\tp.b.Close()\n\tp.a.Close()\n}\n"}
}

// insReleaseElement: a container's release (R37) is its elements'
// releases, last index first, each moved out by slices.Take (R36).
func insReleaseElement(r *rand.Rand, name string) insSite {
	head := fmt.Sprintf("func %s() {\n\tlet xs = []Res{mk(1), mk(2)}\n\tlend(xs[0])\n", name)
	return insSite{rule: "release-element", name: name,
		implicit: head + "}\n",
		written: head + "\tlet mut rest = xs\n\tfor len(rest) > 0 {\n\t\tlet last = len(rest) - 1\n" +
			"\t\tlet v, more = slices.Take(rest, last)\n\t\tv.Close()\n\t\trest = more\n\t}\n}\n"}
}

// insReleaseVariant: a union's release is its active variant's (R3, Gap
// 36): a match whose arm binds the value and closes it.
func insReleaseVariant(r *rand.Rand, name string) insSite {
	head := fmt.Sprintf("func %s() {\n\tlet o Option[Res] = Some{mk(1)}\n", name)
	return insSite{rule: "release-variant", name: name,
		implicit: head + "}\n",
		written:  head + "\tmatch o {\n\tcase Some{Value: v}:\n\t\tv.Close()\n\tcase None:\n\t}\n}\n"}
}

// insReleaseQueue: each value a for-in over q.Values() owns is released at
// the iteration's end (R35, runtime Queues 7).
func insReleaseQueue(r *rand.Rand, name string) insSite {
	write := func(release string) string {
		return fmt.Sprintf("func %s(q queue.Queue[Res]) {\n\tfor v in q.Values() {\n\t\tlend(v)\n%s\t}\n}\n", name, release)
	}
	return insSite{rule: "release-queue", name: name,
		implicit: write(""),
		written:  write("\t\tv.Close()\n")}
}

// insReleaseTemporary: a statement's temporary (R43) is a local of the
// statement, released after it.
func insReleaseTemporary(r *rand.Rand, name string) insSite {
	return insSite{rule: "release-temporary", name: name,
		implicit: fmt.Sprintf("func %s() {\n\tlend(mk(1))\n}\n", name),
		written:  fmt.Sprintf("func %s() {\n\tlet t = mk(1)\n\tlend(t)\n\tt.Close()\n}\n", name)}
}

// insReleaseAssignment: an assignment releases the old value first (R41).
func insReleaseAssignment(r *rand.Rand, name string) insSite {
	write := func(release string) string {
		return fmt.Sprintf("func %s() Res {\n\tlet mut x = mk(1)\n\tlend(x)\n%s\tx = mk(2)\n\tx\n}\n", name, release)
	}
	return insSite{rule: "release-assignment", name: name,
		implicit: write(""),
		written:  write("\tx.Close()\n")}
}

// insReleaseTask: a capture the task owns (R12) is released when the
// task's block ends.
func insReleaseTask(r *rand.Rand, name string) insSite {
	write := func(release string) string {
		return fmt.Sprintf("func %s() Task[int] {\n\tlet x = mk(1)\n\ttask.Go() {\n\t\tlend(x)\n%s\t\t1\n\t}\n}\n", name, release)
	}
	return insSite{rule: "release-task", name: name,
		implicit: write(""),
		written:  write("\t\tx.Close()\n")}
}

// insConversion: a conversion to an operation type (Assignability 3) is
// an adapter: a record that holds the value and whose operations call the
// implementing functions, with their rows.
func insConversion(r *rand.Rand, name string, p insParams) insSite {
	at := []string{"let", "return", "argument"}[r.IntN(3)]
	adapter := "A" + name
	body := func(value string) string {
		switch at {
		case "let":
			return fmt.Sprintf("func %s() Getter {\n\tlet g Getter = %s\n\tg\n}\n", name, value)
		case "return":
			return fmt.Sprintf("func %s() Getter {\n\t%s\n}\n", name, value)
		}
		return fmt.Sprintf("func %s() int {\n\tuseGetter(%s) catch {\n\tcase _:\n\t\t0\n\t}\n}\n", name, value)
	}
	call := "a.v.Get()"
	if p.getFails != "" {
		call = "try " + call
	}
	written := fmt.Sprintf("type %s { v Src }\n\nfunc Get(a %s) int%s {\n\t%s\n}\n\n", adapter, adapter, p.getRow(), call) +
		body(adapter+"{v: Src{n: 1}}")
	return insSite{rule: "conversion", variant: at, name: name,
		implicit: body("Src{n: 1}"),
		written:  written}
}

// insEncode: encode of a type with a by-name pair (Codecs 1) writes its
// Wire form.
func insEncode(r *rand.Rand, name string) insSite {
	write := func(v string) string {
		return fmt.Sprintf("func %s(l Level, e codec.Events) {\n\tencode(%s, e)\n}\n", name, v)
	}
	return insSite{rule: "codec-encode", name: name, implicit: write("l"), written: write("Wire(l)")}
}

// insDecode: decode of a type with a by-name pair reads its Wire form and
// parses it (Codecs 1 and 3).
func insDecode(r *rand.Rand, name string) insSite {
	write := func(v string) string {
		return fmt.Sprintf("func %s(r Reader) Level {\n\t%s\n}\n", name, v)
	}
	return insSite{rule: "codec-decode", name: name,
		implicit: write("try decode[Level](r)"),
		written:  write("try ParseLevel(try decode[string](r))")}
}

// insInstance: a call of a generic function is a call of its instance, the
// body with each type parameter replaced by its argument (Generics 5 and 9).
func insInstance(r *rand.Rand, name string) insSite {
	body := []string{"pass", "wrap", "hold", "run"}[r.IntN(4)]
	mono := body + name
	if body == "run" {
		fails := []string{"", " fails Bad"}[r.IntN(2)]
		needs := needsClause(r.IntN(2) == 0)
		row := fails + needs
		call := "f()"
		if fails != "" {
			call = "try f()"
		}
		site := func(callee string) string {
			return fmt.Sprintf("func %s(f func() int%s) int {\n\t%s(f)\n}\n", name, row, callee)
		}
		return insSite{rule: "instance", variant: "run/" + strings.TrimSpace(row), name: name,
			implicit: site("run"),
			written:  fmt.Sprintf("func %s(f func() int%s) int%s {\n\t%s\n}\n\n", mono, row, row, call) + site(mono)}
	}
	arg := []string{"int", "Res", "*Tally"}[r.IntN(3)]
	var helper, site func(callee string) string
	switch body {
	case "pass":
		helper = func(t string) string { return fmt.Sprintf("func %s(x %s) %s { x }\n\n", mono, t, t) }
		switch arg {
		case "int":
			site = func(c string) string { return fmt.Sprintf("func %s(n int) int {\n\t%s(n)\n}\n", name, c) }
		case "Res":
			site = func(c string) string { return fmt.Sprintf("func %s() Res {\n\tlet x = mk(1)\n\t%s(x)\n}\n", name, c) }
		default:
			site = func(c string) string {
				return fmt.Sprintf("func %s(t *Tally) int {\n\tlet u = %s(t)\n\tu.n\n}\n", name, c)
			}
		}
	case "wrap":
		helper = func(t string) string { return fmt.Sprintf("func %s(x %s) []%s { []%s{x} }\n\n", mono, t, t, t) }
		switch arg {
		case "int":
			site = func(c string) string { return fmt.Sprintf("func %s(n int) []int {\n\t%s(n)\n}\n", name, c) }
		case "Res":
			site = func(c string) string { return fmt.Sprintf("func %s() []Res {\n\tlet x = mk(1)\n\t%s(x)\n}\n", name, c) }
		default:
			site = func(c string) string {
				return fmt.Sprintf("func %s(t *Tally) int {\n\tlet u = %s(t)\n\tlen(u)\n}\n", name, c)
			}
		}
	default: // hold
		helper = func(t string) string {
			return fmt.Sprintf("func %s(x %s, use escaping func(v %s)) func() {\n\tfunc() { use(x) }\n}\n\n", mono, t, t)
		}
		switch arg {
		case "int":
			site = func(c string) string {
				return fmt.Sprintf("func %s(n int) func() {\n\t%s(n, func(v int) {})\n}\n", name, c)
			}
		case "Res":
			site = func(c string) string {
				return fmt.Sprintf("func %s(x Res) func() {\n\t%s(x, func(v Res) { lend(v) })\n}\n", name, c)
			}
		default:
			site = func(c string) string {
				return fmt.Sprintf("func %s(t *Tally) func() {\n\t%s(t, func(v *Tally) { v.n = 1 })\n}\n", name, c)
			}
		}
	}
	return insSite{rule: "instance", variant: body + "/" + arg, name: name,
		implicit: site(body),
		written:  helper(arg) + site(mono)}
}

// insReceiver: a dot call of a function whose first parameter is *T
// borrows its receiver (Borrows 2): x.F(a) is F(&x, a).
func insReceiver(r *rand.Rand, name string) insSite {
	switch r.IntN(4) {
	case 0:
		return insSite{rule: "receiver", variant: "local", name: name,
			implicit: fmt.Sprintf("func %s() int {\n\tlet mut a = Tally{n: 0}\n\ta.Add(2)\n\ta.n\n}\n", name),
			written:  fmt.Sprintf("func %s() int {\n\tlet mut a = Tally{n: 0}\n\tAdd(&a, 2)\n\ta.n\n}\n", name)}
	case 1:
		return insSite{rule: "receiver", variant: "field", name: name,
			implicit: fmt.Sprintf("func %s() int {\n\tlet mut h = Holder{t: Tally{n: 0}}\n\th.t.Add(2)\n\th.t.n\n}\n", name),
			written:  fmt.Sprintf("func %s() int {\n\tlet mut h = Holder{t: Tally{n: 0}}\n\tAdd(&h.t, 2)\n\th.t.n\n}\n", name)}
	case 2:
		return insSite{rule: "receiver", variant: "borrow", name: name,
			implicit: fmt.Sprintf("func %s(t *Tally) {\n\tt.Add(1)\n}\n", name),
			written:  fmt.Sprintf("func %s(t *Tally) {\n\tAdd(t, 1)\n}\n", name)}
	}
	ix := []string{"0", "1", "i", "j"}
	a, b := ix[r.IntN(4)], ix[r.IntN(4)]
	write := func(call string) string {
		return fmt.Sprintf("func %s(i int, j int) int {\n\tlet mut xs = []Tally{Tally{n: 0}, Tally{n: 0}}\n\t%s\n\txs[0].n\n}\n", name, call)
	}
	return insSite{rule: "receiver", variant: "overlap " + a + "," + b, name: name,
		implicit: write(fmt.Sprintf("xs[%s].Two(&xs[%s])", a, b)),
		written:  write(fmt.Sprintf("Two(&xs[%s], &xs[%s])", a, b))}
}

// insCallSite: a call that leaves out a final CallSite parameter passes
// its own position (Call sites 2). The written form passes the literal;
// the property fills the position in once the line is known.
func insCallSite(r *rand.Rand, name string) insSite {
	return insSite{rule: "call-site", name: name,
		implicit: fmt.Sprintf("func %s(n int) int {\n\tat(n)\n}\n", name),
		written:  fmt.Sprintf("func %s(n int) int {\n\tat(n, CallSite{File: \"main.ergo\", Line: @LINE, Column: 2})\n}\n", name)}
}

// insZeroValues are the types a let with no value may take, with the
// written zero of each (Zero values 1 to 3; Refinements 8; Loops 5). A
// refined type whose where refuses its base's zero (Port) gets that zero
// written, which the checker refuses as it refuses the let. A record with
// fields, a union and a resource have no zero, so the compiler inserts
// nothing for them and they are not listed.
var insZeroValues = [][2]string{
	{"int", "0"}, {"string", "\"\""}, {"bool", "false"}, {"duration", "0s"},
	{"[]int", "[]int{}"}, {"Map[string, int]", "Map[string, int]{}"},
	{"(int, string)", "(0, \"\")"}, {"[2]int", "[2]int{}"},
	{"Count", "0"}, {"Port", "0"}, {"Empty", "Empty"},
}

// insZero: a let with no value holds its type's zero, the value written.
func insZero(r *rand.Rand, name string) insSite {
	z := insZeroValues[r.IntN(len(insZeroValues))]
	write := func(value string) string {
		return fmt.Sprintf("func %s() %s {\n\tlet x %s%s\n\tx\n}\n", name, z[0], z[0], value)
	}
	return insSite{rule: "zero", variant: z[0], name: name,
		implicit: write(""),
		written:  write(" = " + z[1])}
}

// insRules are the elaboration rules, by name, for the coverage check.
var insRules = []string{"release-local", "release-field", "release-element", "release-temporary",
	"release-assignment", "release-task", "conversion", "codec-encode", "codec-decode",
	"instance", "receiver", "call-site", "zero", "release-variant", "release-queue"}

// insProgram is one seed's two programs and the line span of each site.
type insProgram struct {
	params             insParams
	sites              []insSite
	implicit, written  string
	implSpan, wrotSpan [][2]int
}

func insertedProgram(seed uint64) insProgram {
	r := rand.New(rand.NewPCG(seed, 0x1a5e))
	p := insParams{
		closeNeeds: r.IntN(2) == 0, addNeeds: r.IntN(2) == 0,
		wireNeeds: r.IntN(2) == 0, parseNeeds: r.IntN(2) == 0,
		getFails: []string{"", "Bad", "Other"}[r.IntN(3)], getNeeds: r.IntN(2) == 0,
	}
	var sites []insSite
	for i := range 2 + r.IntN(5) {
		name := fmt.Sprintf("s%d", i)
		var s insSite
		switch r.IntN(len(insRules)) {
		case 0:
			s = insReleaseLocal(r, name)
		case 1:
			s = insReleaseField(r, name)
		case 2:
			s = insReleaseElement(r, name)
		case 3:
			s = insReleaseTemporary(r, name)
		case 4:
			s = insReleaseAssignment(r, name)
		case 5:
			s = insReleaseTask(r, name)
		case 6:
			s = insConversion(r, name, p)
		case 7:
			s = insEncode(r, name)
		case 8:
			s = insDecode(r, name)
		case 9:
			s = insInstance(r, name)
		case 10:
			s = insReceiver(r, name)
		case 11:
			s = insCallSite(r, name)
		case 12:
			s = insZero(r, name)
		case 13:
			s = insReleaseVariant(r, name)
		default:
			s = insReleaseQueue(r, name)
		}
		sites = append(sites, s)
	}
	prog := insProgram{params: p, sites: sites}
	prog.implicit, prog.implSpan = insAssemble(p, sites, func(s insSite) string { return s.implicit })
	prog.written, prog.wrotSpan = insAssemble(p, sites, func(s insSite) string { return s.written })
	// The written call site is the position of the implicit call.
	for i, s := range sites {
		if s.rule == "call-site" {
			line := prog.implSpan[i][0] + 1
			prog.written = strings.Replace(prog.written, "@LINE", fmt.Sprint(line), 1)
		}
	}
	return prog
}

// insAssemble writes the imports the text uses, the prelude and each
// site's form, and returns the text with the first and last line of each
// site.
func insAssemble(p insParams, sites []insSite, form func(insSite) string) (string, [][2]int) {
	var body strings.Builder
	body.WriteString(p.prelude())
	var spans [][2]int
	for _, s := range sites {
		start := strings.Count(body.String(), "\n") + 1
		body.WriteString(form(s))
		spans = append(spans, [2]int{start, strings.Count(body.String(), "\n")})
		body.WriteString("\n")
	}
	var head strings.Builder
	for _, pkg := range []string{"codec", "queue", "slices", "task"} {
		if strings.Contains(body.String(), pkg+".") {
			fmt.Fprintf(&head, "import %s\n", pkg)
		}
	}
	if head.Len() > 0 {
		head.WriteString("\n")
	}
	off := strings.Count(head.String(), "\n")
	for i := range spans {
		spans[i][0] += off
		spans[i][1] += off
	}
	return head.String() + body.String(), spans
}

// insOutcome is the checker's verdict on each site and on the rest of the
// program: the codes of the diagnostics in its lines, and sN's rows.
type insOutcome struct {
	rest  string
	sites []string
}

func insertedOutcome(src string, spans [][2]int, sites []insSite) (insOutcome, error) {
	pkg, diags := CheckSource(src)
	if pkg == nil {
		return insOutcome{}, fmt.Errorf("does not parse: %v", diags)
	}
	codes := make([][]string, len(spans))
	var rest []string
	for _, d := range diags {
		at := -1
		for i, sp := range spans {
			if d.Line >= sp[0] && d.Line <= sp[1] {
				at = i
			}
		}
		if at < 0 {
			rest = append(rest, d.String())
			continue
		}
		codes[at] = append(codes[at], d.Code)
	}
	out := insOutcome{rest: strings.Join(rest, "; ")}
	for i, s := range sites {
		slices.Sort(codes[i])
		row := "no function"
		for _, f := range pkg.Funcs {
			if f.Name == s.name && f.Sig != nil {
				row = fmt.Sprintf("fails {%s} needs {%s}", f.Sig.Errors, f.Sig.Uses)
			}
		}
		out.sites = append(out.sites, fmt.Sprintf("[%s] %s", strings.Join(codes[i], " "), row))
	}
	return out, nil
}

// insGaps are the findings insertedGap names.
var insGaps = []string{"REL-needs", "G3", "G4"}

// insertedGap names the finding that owns a site's known disagreement at
// this head, or "" when the two forms must agree.
func insertedGap(p insParams, s insSite) string {
	switch {
	case strings.HasPrefix(s.rule, "release-") && p.closeNeeds && !strings.HasSuffix(s.variant, "/Closer"):
		// An automatic release charges no row with its Close's needs.
		return "REL-needs"
	case s.rule == "codec-encode" && p.wireNeeds, s.rule == "codec-decode" && p.parseNeeds:
		// encode and decode charge no row with their pair's needs.
		return "G3"
	case s.rule == "instance" && strings.HasSuffix(s.variant, "/*Tally"):
		// A borrow instantiates T and escapes where *T may not.
		return "G4"
	}
	return ""
}

// TestInsertedOpsAgree is the property (see above).
func TestInsertedOpsAgree(t *testing.T) {
	n := uint64(400)
	if testing.Short() {
		n = 100
	}
	seeds := []uint64{}
	for s := range n {
		seeds = append(seeds, s+1)
	}
	if *insopsSeed != 0 {
		seeds = []uint64{*insopsSeed}
	}
	reached := map[string]bool{}
	survey := map[string]int{}
	for _, seed := range seeds {
		prog := insertedProgram(seed)
		impl, err := insertedOutcome(prog.implicit, prog.implSpan, prog.sites)
		if err != nil {
			t.Fatalf("seed %d: the implicit program %v\n%s", seed, err, prog.implicit)
		}
		wrot, err := insertedOutcome(prog.written, prog.wrotSpan, prog.sites)
		if err != nil {
			t.Fatalf("seed %d: the written program %v\n%s", seed, err, prog.written)
		}
		replay := fmt.Sprintf("Replay: go test -run '^TestInsertedOpsAgree$' ./internal/sketch/check -args -insops.seed=%d", seed)
		if impl.rest != wrot.rest {
			t.Errorf("seed %d: outside the sites the implicit program has %q and the written %q\n%s\n--- implicit\n%s\n--- written\n%s", seed, impl.rest, wrot.rest, replay, prog.implicit, prog.written)
		}
		for i, s := range prog.sites {
			gap := insertedGap(prog.params, s)
			agree := impl.sites[i] == wrot.sites[i]
			if agree {
				reached[s.rule] = true
			} else {
				reached[gap] = true
			}
			key := fmt.Sprintf("%s %s %+v", s.rule, s.variant, prog.params)
			if !agree {
				survey[fmt.Sprintf("%s/%s gap=%q: %s | %s", s.rule, s.variant, gap, impl.sites[i], wrot.sites[i])]++
			}
			if *insopsSurvey {
				continue
			}
			switch {
			case !agree && gap == "":
				t.Errorf("seed %d, site %s (%s): the implicit form gives %s and its written form %s\n%s\n--- implicit\n%s--- written\n%s", seed, s.name, key, impl.sites[i], wrot.sites[i], replay, s.implicit, s.written)
			case agree && gap != "":
				t.Errorf("seed %d, site %s (%s): both forms give %s, so %s no longer holds here; remove its line from insertedGap\n%s\n--- implicit\n%s--- written\n%s", seed, s.name, key, impl.sites[i], gap, replay, s.implicit, s.written)
			}
		}
	}
	if *insopsSurvey {
		keys := make([]string, 0, len(survey))
		for k := range survey {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			t.Logf("%4d  %s", survey[k], k)
		}
	}
	for _, rule := range insRules {
		if *insopsSeed == 0 && !reached[rule] {
			t.Errorf("no seed reaches a site of the rule %s whose two forms agree", rule)
		}
	}
	for _, gap := range insGaps {
		if *insopsSeed == 0 && !*insopsSurvey && !reached[gap] {
			t.Errorf("no seed reaches the known disagreement %s", gap)
		}
	}
}
