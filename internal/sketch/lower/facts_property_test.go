package lower

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The fact export's seeded property (FACT0; facts.go). A generator writes
// programs of refined types (integer bases with ranges, one-sided bounds,
// unions and exclusions; string bases refined by length), a record C, and
// functions whose parameters mix refined values, ints, strings and
// borrows of C. A body reads its refined parameters, indexes and cuts a
// string under a guard that proves the site (0 <= i && i <= len(s)-1),
// under a guard that wraps (i+1 <= len(s)) and under none, and reads and
// writes through its borrows; some functions need Console. A model in Go
// computes, from the generator's choices alone, the facts the export
// must hold: the hull of each refined parameter, each pair of borrows,
// the purity of each function with no needs and no borrow, and each
// proved site. TestAssumptionsAgree checks, for each program:
//
//   - the run build exports exactly the model's facts, at their lines;
//   - the run build's Go is the same with factExport "off" (the
//     reference, -d=ergofacts=0), which exports none: a fact changes no Go
//     until a Go pass consumes it;
//   - the test build exports none and writes each range fact and each
//     disjoint pair as a check that names the fact; each check holds for
//     the value main passes, and its Go type-checks;
//   - with a planted false range fact, the test build's check is false
//     for that value: the checking form raises the defect;
//   - admitFact admits a proved or checked fact of each kind in the run
//     build only, and never a tested one or one with no evidence.
//
// Replay: go test -run '^TestAssumptionsAgree$' ./internal/sketch/lower -args -facts.seed=N

var factsSeed = flag.Uint64("facts.seed", 0, "run TestAssumptionsAgree on this seed alone")

var factsSeeds = []uint64{1, 2, 3, 4, 5, 6}

// fdomain is a refinement base: its name and its range.
type fdomain struct {
	base   string
	lo, hi *big.Int
	isLen  bool
}

func fbig(n int64) *big.Int { return big.NewInt(n) }

var fdomains = []fdomain{
	{base: "int", lo: fbig(math.MinInt64), hi: fbig(math.MaxInt64)},
	{base: "int8", lo: fbig(math.MinInt8), hi: fbig(math.MaxInt8)},
	{base: "int16", lo: fbig(math.MinInt16), hi: fbig(math.MaxInt16)},
	{base: "uint8", lo: fbig(0), hi: fbig(math.MaxUint8)},
	{base: "string", lo: fbig(0), hi: fbig(math.MaxInt64), isLen: true},
}

// frefined is a generated refined type: its where, the model's hull (nil
// for no bound) and a value of it.
type frefined struct {
	name, where string
	dom         fdomain
	lo, hi      *big.Int
	valid       int64
}

// fparam is a generated parameter.
type fparam struct {
	name, typ string
	ref       *frefined
	borrow    bool
}

// fwant is one fact the model expects.
type fwant struct {
	kind, fn string
	line     int
	subjects []string
	lo, hi   string
	ref      *frefined // a range fact's type
}

func (w fwant) String() string {
	return fmt.Sprintf("%s %s line %d %v lo=%q hi=%q", w.kind, w.fn, w.line, w.subjects, w.lo, w.hi)
}

// fprogram is a generated program and its model.
type fprogram struct {
	src   string
	want  []fwant
	types []*frefined
	// valid is, per function and range-fact parameter, the value main
	// passes.
	valid map[string]int64
}

type fgen struct {
	r     *rand.Rand
	lines []string
}

func (g *fgen) add(s string) int {
	g.lines = append(g.lines, s)
	return len(g.lines)
}

// refined writes a refined type over d.
func (g *fgen) refined(name string, d fdomain) *frefined {
	t := &frefined{name: name, dom: d}
	subj := "it"
	if d.isLen {
		subj = "len(it)"
	}
	// small is a value inside d, away from its ends.
	small := func() int64 {
		if d.lo.Sign() >= 0 {
			return int64(g.r.IntN(100))
		}
		return int64(g.r.IntN(200)) - 100
	}
	a, b := small(), small()
	if a > b {
		a, b = b, a
	}
	if a == b {
		b++
	}
	switch g.r.IntN(6) {
	case 0: // a range
		t.where = fmt.Sprintf("%d <= %s && %s <= %d", a, subj, subj, b)
		t.lo, t.hi, t.valid = fbig(a), fbig(b), a
	case 1: // a lower bound
		t.where = fmt.Sprintf("%s >= %d", subj, a)
		t.lo, t.valid = fbig(a), a
	case 2: // an upper bound
		t.where = fmt.Sprintf("%s <= %d", subj, b)
		t.hi, t.valid = fbig(b), b
	case 3: // a union whose hull is the domain: no fact
		t.where = fmt.Sprintf("%s <= %d || %d <= %s", subj, a, b, subj)
		t.valid = a
	case 4: // an exclusion: no fact
		t.where = fmt.Sprintf("%s != %d", subj, a)
		t.valid = a + 1
	default: // one value
		t.where = fmt.Sprintf("%s == %d", subj, a)
		t.lo, t.hi, t.valid = fbig(a), fbig(a), a
	}
	// A bound at the domain's end bounds nothing.
	if t.lo != nil && t.lo.Cmp(d.lo) <= 0 {
		t.lo = nil
	}
	if t.hi != nil && t.hi.Cmp(d.hi) >= 0 {
		t.hi = nil
	}
	if d.isLen && t.valid < 0 {
		t.valid = 0
	}
	g.add("type " + name + " " + d.base + " where " + t.where)
	return t
}

func bigText(b *big.Int) string {
	if b == nil {
		return ""
	}
	return b.String()
}

// genFactsProgram is the program of seed and case.
func genFactsProgram(seed uint64, n int) fprogram {
	g := &fgen{r: rand.New(rand.NewPCG(seed, uint64(n)))}
	p := fprogram{valid: map[string]int64{}}
	for i := range 1 + g.r.IntN(3) {
		p.types = append(p.types, g.refined(fmt.Sprintf("R%d", i), fdomains[g.r.IntN(len(fdomains))]))
	}
	g.add("type C { N int }")
	type ffunc struct {
		name   string
		params []fparam
		needs  bool
	}
	var funcs []ffunc
	for k := range 2 + g.r.IntN(3) {
		f := ffunc{name: fmt.Sprintf("f%d", k), needs: g.r.IntN(3) == 0}
		for j := range 1 + g.r.IntN(4) {
			pn := fmt.Sprintf("p%d", j)
			switch g.r.IntN(5) {
			case 0, 1:
				t := p.types[g.r.IntN(len(p.types))]
				f.params = append(f.params, fparam{name: pn, typ: t.name, ref: t})
			case 2:
				f.params = append(f.params, fparam{name: pn, typ: "int"})
			case 3:
				f.params = append(f.params, fparam{name: pn, typ: "string"})
			default:
				f.params = append(f.params, fparam{name: pn, typ: "*C", borrow: true})
			}
		}
		// A function whose first parameter is a borrow of the package's
		// record is that record's method (196m): the facts name it C.f.
		fn := f.name
		if f.params[0].borrow {
			fn = "C." + f.name
		}
		var list []string
		for _, q := range f.params {
			list = append(list, q.name+" "+q.typ)
		}
		head := "func " + f.name + "(" + strings.Join(list, ", ") + ") int"
		if f.needs {
			head += " needs Console"
		}
		line := g.add(head + " {")
		g.add("\tlet mut acc = 0")
		g.add("\tacc = acc + 1")
		if f.needs {
			g.add("\tConsole.Println(\"" + f.name + "\")")
		}
		borrows := 0
		var str, num string
		for i, q := range f.params {
			switch {
			case q.ref != nil:
				if q.ref.lo != nil || q.ref.hi != nil {
					p.want = append(p.want, fwant{kind: "fact-range", fn: fn, line: line, subjects: []string{q.name}, lo: bigText(q.ref.lo), hi: bigText(q.ref.hi), ref: q.ref})
					p.valid[fn+"."+q.name] = q.ref.valid
				}
				if q.ref.dom.isLen {
					g.add("\tacc = acc + len(" + q.name + ")")
				} else {
					g.add("\tacc = acc + int(" + q.ref.dom.base + "(" + q.name + "))")
				}
			case q.borrow:
				borrows++
				g.add("\tacc = acc + " + q.name + ".N")
				if g.r.IntN(2) == 0 {
					g.add("\t" + q.name + ".N = acc")
				}
				for _, o := range f.params[i+1:] {
					if o.borrow {
						p.want = append(p.want, fwant{kind: "fact-disjoint", fn: fn, line: line, subjects: []string{q.name, o.name}})
					}
				}
			case q.typ == "string" && str == "":
				str = q.name
			case q.typ == "int" && num == "":
				num = q.name
			}
		}
		if str != "" && num != "" {
			s, i := str, num
			for range 1 + g.r.IntN(3) {
				switch g.r.IntN(5) {
				case 0: // a proved index
					g.add(fmt.Sprintf("\tif 0 <= %s && %s <= len(%s)-1 {", i, i, s))
					l := g.add(fmt.Sprintf("\t\tacc = acc + int(%s[%s])", s, i))
					g.add("\t}")
					p.want = append(p.want, fwant{kind: "fact-index", fn: fn, line: l, subjects: []string{s, i}})
				case 1: // a proved cut
					g.add(fmt.Sprintf("\tif 0 <= %s && %s <= len(%s)-2 {", i, i, s))
					l := g.add(fmt.Sprintf("\t\tacc = acc + len(%s[%s:%s + 2])", s, i, i))
					g.add("\t}")
					p.want = append(p.want, fwant{kind: "fact-cut", fn: fn, line: l, subjects: []string{s, i, i + " + 2"}})
				case 2: // a guard that wraps proves nothing
					g.add(fmt.Sprintf("\tif %s + 1 <= len(%s) {", i, s))
					g.add(fmt.Sprintf("\t\tacc = acc + int(%s[%s])", s, i))
					g.add("\t}")
				case 3: // no guard
					g.add(fmt.Sprintf("\tacc = acc + int(%s[0]) + len(%s[0:1])", s, s))
				default: // a let carries the guard's facts
					g.add(fmt.Sprintf("\tif 0 <= %s && %s <= len(%s)-1 {", i, i, s))
					g.add(fmt.Sprintf("\t\tlet j = %s + 1", i))
					l := g.add(fmt.Sprintf("\t\tacc = acc + int(%s[j - 1])", s))
					g.add("\t}")
					p.want = append(p.want, fwant{kind: "fact-index", fn: fn, line: l, subjects: []string{s, "j - 1"}})
				}
			}
		}
		g.add("\tacc")
		g.add("}")
		if !f.needs && borrows == 0 {
			p.want = append(p.want, fwant{kind: "fact-pure", fn: fn, line: line, subjects: []string{f.name}})
		}
		funcs = append(funcs, f)
	}
	g.add("func main() needs Console {")
	g.add("\tlet mut total = 0")
	for _, f := range funcs {
		var args []string
		for _, q := range f.params {
			switch {
			case q.ref != nil && q.ref.dom.isLen:
				args = append(args, strconv.Quote(strings.Repeat("a", int(q.ref.valid))))
			case q.ref != nil:
				args = append(args, strconv.FormatInt(q.ref.valid, 10))
			case q.borrow:
				cell := "c" + f.name + q.name
				g.add("\tlet mut " + cell + " = C{N: 1}")
				args = append(args, "&"+cell)
			case q.typ == "int":
				args = append(args, "1")
			default:
				args = append(args, `"abcd"`)
			}
		}
		g.add("\ttotal = total + " + f.name + "(" + strings.Join(args, ", ") + ")")
	}
	g.add("\tConsole.Println(\"\\(total)\")")
	g.add("}")
	p.src = strings.Join(g.lines, "\n") + "\n"
	return p
}

// gotWant is the model's form of an exported fact.
func gotWant(f Fact) fwant {
	at := strings.Split(f.At, ":")
	line, _ := strconv.Atoi(at[len(at)-2])
	return fwant{kind: f.Kind, fn: f.Func, line: line, subjects: f.Subjects, lo: f.Lo, hi: f.Hi}
}

// factsUnit checks src as the package main.
func factsUnit(t *testing.T, src string) Unit {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("the generated program does not parse: %v\n%s", err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("the generated program does not check: %v\n%s", diags, src)
	}
	return Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
}

func TestAssumptionsAgree(t *testing.T) {
	seeds := factsSeeds
	if *factsSeed != 0 {
		seeds = []uint64{*factsSeed}
	}
	for _, seed := range seeds {
		for n := range 4 {
			p := genFactsProgram(seed, n)
			fail := func(format string, args ...any) {
				t.Helper()
				t.Errorf("seed %d case %d: %s\n%s\nreplay: go test -run '^TestAssumptionsAgree$' ./internal/sketch/lower -args -facts.seed=%d", seed, n, fmt.Sprintf(format, args...), p.src, seed)
			}
			u := factsUnit(t, p.src)
			agreeFacts(t, p, u, fail)
		}
	}
	admissionLaw(t, factsUnit(t, genFactsProgram(1, 0).src))
}

// agreeFacts checks one program's export, erasure and checking forms.
func agreeFacts(t *testing.T, p fprogram, u Unit, fail func(string, ...any)) {
	t.Helper()
	facts, err := Facts(u)
	if err != nil {
		fail("Facts: %v", err)
		return
	}
	var got, want []string
	for _, f := range facts {
		if f.Evidence != evidenceProved && f.Evidence != evidenceChecked {
			fail("the fact %+v left with the evidence %q", f, f.Evidence)
		}
		got = append(got, gotWant(f).String())
	}
	for _, w := range p.want {
		want = append(want, w.String())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		fail("the export and the model differ\nexport:\n  %s\nmodel:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// Erasure: no fact changes the run build's Go.
	run := goText(mustLowerUnit(t, "facts-run", u, false))
	saved := factExport
	factExport = "off"
	off := goText(mustLowerUnit(t, "facts-off", u, false))
	offFacts, err := Facts(u)
	factExport = saved
	if err != nil || len(offFacts) > 0 {
		fail("factExport off exports %d facts (%v)", len(offFacts), err)
	}
	if run != off {
		fail("the run build's Go differs with factExport off: a fact changed the Go\n%s", firstDiff(run, off))
	}

	// The checking form, then with a planted false range fact.
	test := mustLowerUnit(t, "facts-test", u, true)
	typecheckGo(t, []*lowered{test})
	checkForms(goText(test), p, false, fail)
	factPlantForTest = func(f *Fact) {
		if f.Kind == string(factRange) {
			f.Lo, f.Hi = "7777", "7777"
		}
	}
	planted := goText(mustLowerUnit(t, "facts-planted", u, true))
	factPlantForTest = nil
	checkForms(planted, p, true, fail)
}

// checkForms finds, in the test build's Go, the check of each range and
// disjoint fact of the model, and evaluates each range check on the value
// main passes: it holds, or, planted, it fails.
func checkForms(goSrc string, p fprogram, planted bool, fail func(string, ...any)) {
	lines := strings.Split(goSrc, "\n")
	find := func(reason string) (string, bool) {
		for i, l := range lines {
			if i > 0 && strings.Contains(l, reason) {
				c := strings.TrimSpace(lines[i-1])
				if strings.HasPrefix(c, "/*line ") {
					_, c, _ = strings.Cut(c, "*/")
				}
				if strings.HasPrefix(c, "if !(") && strings.HasSuffix(c, ") {") {
					return c[len("if !(") : len(c)-len(") {")], true
				}
			}
		}
		return "", false
	}
	for _, w := range p.want {
		switch w.kind {
		case "fact-range":
			holds := boundsText(w.subjects[0], w.lo, w.hi)
			if w.ref.dom.isLen {
				holds = boundsText("len("+w.subjects[0]+")", w.lo, w.hi)
			}
			cond, ok := find("fact-range: " + holds + " is false in " + w.fn + " (")
			if !ok {
				fail("the test build has no check of %s", w)
				continue
			}
			v := p.valid[w.fn+"."+w.subjects[0]]
			holdsNow, err := evalCheck(cond, v)
			switch {
			case err != nil:
				fail("the check %q of %s: %v", cond, w, err)
			case holdsNow == planted:
				fail("the check %q of %s is %v for %d, planted %v: a check that holds raises no defect, and a planted false fact must", cond, w, holdsNow, v, planted)
			}
		case "fact-disjoint":
			if _, ok := find("fact-disjoint: " + w.subjects[0] + " and " + w.subjects[1] + " do not overlap is false in " + w.fn + " ("); !ok {
				fail("the test build has no check of %s", w)
			}
		}
	}
}

// evalCheck evaluates a check's Go condition with every name and every
// len(name) bound to v.
func evalCheck(cond string, v int64) (bool, error) {
	x, err := parser.ParseExpr(cond)
	if err != nil {
		return false, err
	}
	var num func(ast.Expr) (*big.Int, error)
	num = func(e ast.Expr) (*big.Int, error) {
		switch e := e.(type) {
		case *ast.ParenExpr:
			return num(e.X)
		case *ast.Ident:
			return fbig(v), nil
		case *ast.CallExpr:
			return fbig(v), nil
		case *ast.BasicLit:
			b, ok := new(big.Int).SetString(e.Value, 10)
			if !ok {
				return nil, fmt.Errorf("the literal %s", e.Value)
			}
			return b, nil
		case *ast.UnaryExpr:
			if e.Op == token.SUB {
				b, err := num(e.X)
				if err != nil {
					return nil, err
				}
				return b.Neg(b), nil
			}
		}
		return nil, fmt.Errorf("the operand %T", e)
	}
	var boolean func(ast.Expr) (bool, error)
	boolean = func(e ast.Expr) (bool, error) {
		switch e := e.(type) {
		case *ast.ParenExpr:
			return boolean(e.X)
		case *ast.BinaryExpr:
			if e.Op == token.LAND {
				a, err := boolean(e.X)
				if err != nil || !a {
					return false, err
				}
				return boolean(e.Y)
			}
			a, err := num(e.X)
			if err != nil {
				return false, err
			}
			b, err := num(e.Y)
			if err != nil {
				return false, err
			}
			c := a.Cmp(b)
			switch e.Op {
			case token.LEQ:
				return c <= 0, nil
			case token.GEQ:
				return c >= 0, nil
			}
		}
		return false, fmt.Errorf("the condition %T", e)
	}
	return boolean(x)
}

// admissionLaw checks admitFact over every kind, evidence label and
// build: a proved or checked fact leaves the run build, nothing leaves a
// test build or the reference, and a tested fact or one with no evidence
// never leaves.
func admissionLaw(t *testing.T, u Unit) {
	t.Helper()
	kinds := []factKind{factRange, factIndex, factCut, factDisjoint, factPure}
	for _, build := range []string{"run", "test", "off"} {
		var tests *testLowering
		if build == "test" {
			tests = &testLowering{}
		}
		saved := factExport
		if build == "off" {
			factExport = "off"
		}
		l := newBuildLowerer(u, tests)
		for _, k := range kinds {
			for _, ev := range []string{evidenceProved, evidenceChecked, evidenceTested, evidenceNone} {
				want := build == "run" && (ev == evidenceProved || ev == evidenceChecked)
				if got := l.admitFact(k, ev); got != want {
					t.Errorf("admitFact(%s, %s) in the %s build = %v, want %v", k, ev, build, got, want)
				}
			}
		}
		factExport = saved
	}
}
