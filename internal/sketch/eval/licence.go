package eval

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/runtime/testgen"
)

// The licence run (runtime.md, Tests 7; LAWFFI r2 §3.4; owner q194 and
// q195): ergo test's run of a top-level rewrite law in scope
// (check.FuncRewrite) whose ledger entry is not current. Its verdict is
// the only licence a rewrite over functions takes short of a proof. It
// has three parts:
//
//  0. The corpus first: the cases of earlier refuted runs (licencecase.go).
//  1. Edges first, then drawn cases. Each parameter takes one value of
//     every named class of its type (testgen's class tables) before any
//     draw; two parameters take each pair of their classes, the other
//     parameters at their first class. Then licenceDraws drawn cases at
//     sizes 0 to 25, as a law's run draws them.
//  2. Each side is a function of its arguments. Each case evaluates each
//     side twice, the second time under another ambient state (a working
//     directory, an environment and a time zone); a different outcome is
//     a refutation, "not a function of its arguments". The two sides'
//     outcomes compare by key equality (their texts), not by the law's
//     ==, so a float law holds at a NaN where its sides agree.
//  3. The verdict. validated only when every class and every pair was
//     reached by a case that held, no case was skipped and none refuted.
//     A run that ran out of budget is tested, with the classes it missed;
//     a parameter type with no class table makes the law tested; a skip
//     is skipped. Only validated licenses a rewrite (lower's admitLevel,
//     by eval).
//
// The run is the check-time evaluator's, not a law-check build's: the text
// emitter is frozen (IR-ONLY, 2026-11-16), and q195 names the evaluator's
// run as a licence ("a law the evaluator skips is no licence"). So the law
// runs on exactly the Go functions the evaluator has a native of; a law
// that reaches another one is skipped and licensed by nothing.
//
// Prior art: GHC's RULES take no evidence, and Lean's simp lemmas take a
// proof. QuickCheck's and Hypothesis's runs report only that the cases
// passed; Hypothesis's st.floats and Go's fuzz seed corpus take the edge
// values first, as part 1 does, and Hypothesis's `target` and
// QuickCheck's `cover` name the classes a run reached, as part 3 needs.

// licenceDraws is the count of drawn cases after the classes.
const licenceDraws = 1000

// licenceSizes is the bound of a drawn case's size, as a law's run
// draws it (testgen.Stream.Start: case 100 has size 25).
const licenceSizes = 26

// LicenceBudget is ergo test's budget of one licence run: a hundred
// times an editor evaluation's steps and four times its bytes, for the
// classes, their pairs and the drawn cases. It is a work bound, not a
// time bound.
func LicenceBudget() Budget {
	return Budget{Steps: 100_000_000, Depth: 1024, Seed: 1, Bytes: 4 * defaultBytes}
}

// ambient is the process state a side might read beside its arguments.
// A native that would read the working directory, the environment or
// the time zone reads it here, so the second evaluation of a side sees
// another (none of today's natives reads any: they are pure functions).
type ambient struct {
	dir  string
	env  []string
	zone *time.Location
	// index is the state's place in ambients: a module body's driver
	// of the same state runs it (gobodies.go).
	index int
}

// ambients are the two states each side runs under.
var ambients = [2]ambient{
	{dir: "/", zone: time.UTC},
	{dir: "/nonexistent/ergo-licence", env: []string{"ERGO_LICENCE=second"}, zone: time.FixedZone("UTC+05:45", 5*3600+45*60), index: 1},
}

// Licence is the outcome of a licence run.
type Licence struct {
	// Name is the law's name as ergo test prints it ("law L").
	Name string
	// Verdict is validated, tested, skipped or refuted.
	Verdict string
	// Reason says why a verdict is not validated.
	Reason string
	// Cases is the count of cases that held.
	Cases int
	// Classes are the count of held cases per class, in the order the
	// run takes them; Missed the classes and pairs no held case reached.
	Classes []ClassCount
	Missed  []string
	// Arguments are the refuted case's, and Case its corpus line
	// (encodeCase), "" when a value's type has no encoding.
	Arguments []Argument
	Case      string
	// Replayed is the count of the corpus's cases the run took first,
	// and held.
	Replayed int
	Site     Site
	Steps    int64
}

// ClassCount is a class and the count of the held cases that took it.
type ClassCount struct {
	Name  string
	Cases int
}

// Line is the run's report line, as ergo test prints it: the verdict, its
// reason, the cases and the count per class.
func (l Licence) Line() string {
	s := "licence " + l.Name + ": " + l.Verdict
	if l.Reason != "" {
		s += ": " + l.Reason
	}
	if len(l.Arguments) > 0 {
		args := make([]string, len(l.Arguments))
		for i, a := range l.Arguments {
			args[i] = a.Name + " = " + a.Value
		}
		s += " at (" + strings.Join(args, ", ") + ")"
	}
	s += "; " + strconv.Itoa(l.Cases) + " cases"
	if l.Replayed > 0 {
		s += " (" + strconv.Itoa(l.Replayed) + " from the corpus first)"
	}
	if len(l.Classes) > 0 {
		parts := make([]string, len(l.Classes))
		for i, c := range l.Classes {
			parts[i] = c.Name + " " + strconv.Itoa(c.Cases)
		}
		s += "; classes " + strings.Join(parts, ", ")
	}
	return s
}

// LicenceRun runs the licence run of law, a top-level law of pkg in the
// rewrite's scope, under b (its Steps and Bytes bound the whole run). An
// error is an internal error of the evaluator.
//
// goBodies runs the module's own Go bodies the law reaches (LAWFFI LF2m;
// gobodies.go); nil runs none, and a law that reaches one is skipped.
func LicenceRun(pkg *check.Package, law *check.Law, b Budget, corpus []string, goBodies GoBodies) (Licence, error) {
	if b.Steps <= 0 || b.Depth <= 0 || b.Bytes < 0 {
		return Licence{}, ErrBudget
	}
	if b.Bytes == 0 {
		b.Bytes = defaultBytes
	}
	if b.States == 0 {
		b.States = defaultStates
	}
	if pkg == nil || pkg.Info == nil || law == nil || law.TopRewrite() == nil {
		return Licence{}, fmt.Errorf("eval: law %v is no rewrite in scope", law)
	}
	defer raiseGC()()
	l, ranged, err := licenceOrdered(pkg, law, b, corpus, mapAscending, goBodies)
	if err != nil || !ranged {
		return l, err
	}
	// A for over a Map ran: only an outcome the other order also gives
	// is a verdict (maporder.go).
	other, _, err := licenceOrdered(pkg, law, b, corpus, mapDescending, goBodies)
	if err != nil {
		return l, err
	}
	if other.Verdict != l.Verdict || other.Reason != l.Reason {
		l.Verdict, l.Reason, l.Arguments = "skipped", orderNote, nil
	}
	return l, nil
}

// licenceClass is one class a parameter takes: its report name and its
// value.
type licenceClass struct {
	name  string
	value func(in *interp, fr *frame) Value
}

func licenceOrdered(pkg *check.Package, law *check.Law, b Budget, corpus []string, order mapOrder, goBodies GoBodies) (l Licence, ranged bool, err error) {
	in := &interp{budget: b, root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, mapOrder: order, goBodies: goBodies}
	l.Name = lawName(law)
	if claim := law.ClaimRewriteBinding(); claim != nil {
		l.Name = "law " + law.Title(pkg) + " for " + claim.Type.Obj.Name
		if claim.At != nil && claim.At != claim.Type {
			l.Name += " at " + claim.InstanceTitle()
		}
		if claim.Unrun != "" {
			l.Verdict, l.Reason = "skipped", claim.Unrun+claim.ClaimBlame()
			return l, false, nil
		}
		defer func() {
			if l.Verdict == "refuted" || l.Verdict == "skipped" {
				l.Reason += claim.ClaimBlame()
			}
		}()
	}
	defer func() {
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				panic(r)
			}
			err = ie
		}
	}()
	if fail := in.initialize(pkg); fail != nil {
		l.Verdict, l.Reason = "skipped", fail.reason
		if fail.status != Skipped {
			l.Verdict = "tested"
		}
		return l, false, nil
	}
	in.begin()
	in.licence(law, &l, corpus)
	l.Steps = in.steps - in.stepsAt
	return l, in.mapRanged, nil
}

// licence runs law's cases into l (LicenceRun).
func (in *interp) licence(law *check.Law, l *Licence, corpus []string) {
	rw := law.TopRewrite()
	fr := in.newFrame(law.Pkg, law.Decl)
	claim := law.ClaimRewriteBinding()
	if claim != nil {
		// The rewrite view also names the claimant's declaration
		// parameters for matching; only the law's own parameters need
		// instantiation in the concrete licence body.
		original := *law
		original.TParams = law.TParams[len(claim.Type.TParams):]
		info, why := claim.ClaimInfo(&original)
		if why != "" {
			l.Verdict, l.Reason = "skipped", why
			return
		}
		fr.info, fr.tab = info, in.table(info)
	}
	in.atInt = map[*check.TypeParam]bool{}
	for _, tp := range law.TParams {
		in.atInt[tp] = true
	}
	r := testgen.New(in.budget.Seed, l.Name)
	r.Charge = func(k int64) { mustCharge(in, fr, k) }
	draw := func(t check.Type) Value {
		if claim != nil && claim.ClaimCtor != nil && t == claim.At {
			var args []Value
			for _, tp := range claim.ClaimCtorParams {
				args = append(args, in.generate(fr, r, tp, testplan.Depth, nil))
			}
			return mustValue(in.call(fr, nil, claim.ClaimCtor, claim.ClaimCtorArgs, args))
		}
		return in.generate(fr, r, t, testplan.Depth, nil)
	}
	// types are the parameters' types as the run reads them, and families
	// the pure functions a function parameter takes (licencefuncs.go).
	n := len(law.Params)
	types := make([]check.Type, n)
	families := make([][]licenceClass, n)
	for i, v := range law.Params {
		types[i] = lawParamType(law, v.Type)
		paramType := v.Type
		if claim != nil {
			paramType = claim.Params[i]
			types[i] = paramType
		}
		if _, isFunc := paramType.(*check.Signature); isFunc {
			families[i] = funcFamily(law, paramType)
			if families[i] == nil && claim != nil {
				families[i] = claimResultFamily(paramType, claim.At, draw)
			}
			if families[i] == nil {
				l.Verdict, l.Reason = "skipped", "the run has no family of pure functions for "+v.Name+" "+v.Type.String()
				return
			}
		}
	}
	pre := preflight{in: in, seen: map[syntax.Node]bool{}}
	if !rw.OverFuncs {
		// A law over function parameters has no plan: testplan generates
		// no function, and its Unrun says so.
		p := testplan.Law(law, nil)
		if p.Reason != "" {
			l.Verdict, l.Reason = "skipped", p.Reason
			return
		}
	}
	bodies := []*syntax.BlockStmt{law.Decl.Body}
	if claim != nil {
		left, right := law.Decl.RewriteSides()
		bodies = []*syntax.BlockStmt{left, right}
	}
	for _, body := range bodies {
		delete(pre.seen, fr.decl)
		if why := pre.body(fr, fr.decl, body, nil, map[*check.Var]check.Conversion{}); why != "" {
			l.Verdict, l.Reason = "skipped", why
			return
		}
	}
	for i := range law.Params {
		if families[i] != nil {
			continue
		}
		if why := pre.generator(fr, types[i], nil, map[check.Type]bool{}); why != "" {
			l.Verdict, l.Reason = "skipped", why
			return
		}
	}
	tables := make([][]licenceClass, n)
	var noClasses []string
	for i, v := range law.Params {
		tables[i] = families[i]
		if tables[i] == nil {
			tables[i] = classesOf(types[i])
		}
		if tables[i] == nil {
			noClasses = append(noClasses, types[i].String())
		}
		label := func(c licenceClass) licenceClass {
			if n > 1 {
				c.name = v.Name + ": " + c.name
			}
			return c
		}
		for k := range tables[i] {
			tables[i][k] = label(tables[i][k])
		}
	}
	// The edge cases: a class index per parameter, -1 for a drawn value;
	// each with the classes (and the pair) it stands for.
	type edge struct {
		at   []int
		hits []string
	}
	var edges []edge
	required := map[string]bool{}
	var order []string
	need := func(name string) {
		if !required[name] {
			required[name] = true
			order = append(order, name)
		}
	}
	first := func() []int {
		at := make([]int, n)
		for i := range at {
			if len(tables[i]) == 0 {
				at[i] = -1
			}
		}
		return at
	}
	switch {
	case n == 1:
		for k, c := range tables[0] {
			at := first()
			at[0] = k
			need(c.name)
			edges = append(edges, edge{at, []string{c.name}})
		}
	case n > 1:
		for i := 0; i < n; i++ {
			for _, c := range tables[i] {
				need(c.name)
			}
		}
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				for ki, ci := range tables[i] {
					for kj, cj := range tables[j] {
						at := first()
						at[i], at[j] = ki, kj
						pair := ci.name + " × " + cj.name
						need(pair)
						edges = append(edges, edge{at, []string{ci.name, cj.name, pair}})
					}
				}
			}
		}
	}
	counts := map[string]int{}
	finish := func() {
		for _, i := range classNames(tables) {
			l.Classes = append(l.Classes, ClassCount{Name: i, Cases: counts[i]})
		}
		for _, name := range order {
			if counts[name] == 0 {
				l.Missed = append(l.Missed, name)
			}
		}
	}
	defer finish()
	// current is the case being run, for the corpus line of a refuted one.
	var current []Value
	defer func() {
		// A function argument has no corpus text: the refutation names its
		// family member instead.
		if l.Verdict == "refuted" && current != nil && !rw.OverFuncs {
			l.Case = in.encodeCase(fr, law, current)
		}
	}()
	// run is one case: each side under both states, compared by key
	// equality (a value's text: floats by their bits' text, so a NaN is a
	// NaN and -0 is not +0; a failure by its value; a defect by its text),
	// with given as its values when it is not nil; it reports whether the
	// case held, having set the verdict when it did not.
	run := func(at []int, k int, given []Value) (held bool) {
		defer func() {
			if rec := recover(); rec != nil {
				ab, ok := rec.(*abrupt)
				if !ok {
					panic(rec)
				}
				held = false
				in.licenceStop(l, statusOf(ab), ab.msg, in.site(ab.pkg, ab.decl, ab.at), nil)
			}
		}()
		mustCharge(in, fr, 1)
		r.Start(k)
		r.SetSize(min(k*licenceSizes/(licenceDraws+1), licenceSizes-1))
		vals := make([]Value, n)
		current = vals
		for i := range law.Params {
			if given != nil {
				vals[i] = given[i]
			} else if at != nil && at[i] >= 0 {
				vals[i] = tables[i][at[i]].value(in, fr)
			} else if families[i] != nil {
				vals[i] = families[i][r.Intn(len(families[i]))].value(in, fr)
			} else {
				vals[i] = draw(types[i])
			}
		}
		args := func() []Argument {
			out := make([]Argument, n)
			for i, v := range law.Params {
				if f, isFamily := vals[i].(*nativeFunc); isFamily && families[i] != nil {
					out[i] = Argument{Name: v.Name, Value: f.name}
					continue
				}
				out[i] = Argument{Name: v.Name, Value: in.argument(fr, types[i], vals[i])}
			}
			return out
		}
		frameAt := func() *frame {
			cf := in.newFrame(law.Pkg, law.Decl)
			cf.info, cf.tab = fr.info, fr.tab
			for i, v := range law.Params {
				cf.bind(v, vals[i])
			}
			return cf
		}
		var outcome [2]string
		for i, side := range []syntax.Expr{rw.Lhs, rw.Rhs} {
			var outs [2]string
			for s := range ambients {
				in.ambient = ambients[s]
				o, st, why := in.sideOutcome(frameAt(), l.Name, side)
				if st != Passed {
					in.licenceStop(l, st, why, in.site(law.Pkg, law.Decl, side.Pos()), args())
					return false
				}
				outs[s] = o
			}
			if outs[0] != outs[1] {
				in.licenceStop(l, Refuted, fmt.Sprintf("not a function of its arguments: %s gave %s, and under another working directory, environment and time zone %s", in.exprText(law, side), outs[0], outs[1]), in.site(law.Pkg, law.Decl, side.Pos()), args())
				return false
			}
			outcome[i] = outs[0]
		}
		in.ambient = ambients[0]
		if rw.OverFuncs {
			// The build places the right side under v1.FuseOr: its defect
			// runs the left side again (licencefuncs.go).
			outcome[1] = fusedOutcome(outcome[0], outcome[1])
		}
		if outcome[0] != outcome[1] {
			in.licenceStop(l, Refuted, fmt.Sprintf("the law is false: %s gave %s, and %s gives %s", in.exprText(law, rw.Lhs), outcome[0], in.exprText(law, rw.Rhs), outcome[1]), in.site(law.Pkg, law.Decl, law.Decl.Pos()), args())
			return false
		}
		l.Cases++
		return true
	}
	// The corpus's cases first (a refuted case of an earlier run, as Go's
	// fuzzer keeps a crasher): a line whose values no longer fit the
	// parameters is passed over.
	for _, line := range corpus {
		if rw.OverFuncs {
			break // no corpus line holds a function
		}
		vals, ok := in.safeDecode(fr, law, line)
		if !ok {
			continue
		}
		if !run(nil, 0, vals) {
			return
		}
		l.Replayed++
	}
	for k, e := range edges {
		if !run(e.at, k+1, nil) {
			return
		}
		for _, h := range e.hits {
			counts[h]++
		}
	}
	if n == 0 {
		if !run(nil, 1, nil) {
			return
		}
	} else {
		for k := 1; k <= licenceDraws; k++ {
			if !run(nil, k, nil) {
				return
			}
		}
	}
	switch {
	case len(noClasses) > 0:
		l.Verdict, l.Reason = "tested", "no classes for "+strings.Join(noClasses, ", ")
	default:
		l.Verdict = "validated"
	}
}

// licenceStop sets the verdict of a run that stopped at a case whose
// status is st: a skip is skipped, an exhausted budget tested with the
// classes missed, a refutation or a defect refuted.
func (in *interp) licenceStop(l *Licence, st Status, why string, site Site, args []Argument) {
	switch st {
	case Skipped:
		l.Verdict, l.Reason = "skipped", why
	case Incomplete:
		l.Verdict, l.Reason = "tested", why+"; classes missed"
	default:
		l.Verdict, l.Reason, l.Site, l.Arguments = "refuted", why, site, args
	}
}

// sideOutcome evaluates side in fr: its value's text, or the failure or
// defect it ended in, as the outcome two runs compare; or the status of a
// run that could not tell (skipped, out of budget).
func (in *interp) sideOutcome(fr *frame, name string, side syntax.Expr) (string, Status, string) {
	var v Value
	_, ab := in.rootTask(fr, name, func() *abrupt {
		var ab *abrupt
		v, ab = in.expr(fr, side)
		return ab
	})
	if ab == nil {
		return "the value " + describe(v), Passed, ""
	}
	switch ab.kind {
	case ctlFail, ctlEscape:
		return "the failure " + describe(ab.val), Passed, ""
	case ctlDefect, ctlInterrupt:
		return "the defect " + strconv.Quote(ab.msg), Passed, ""
	}
	return "", statusOf(ab), ab.msg
}

// exprText is the source text of x, a side of law.
func (in *interp) exprText(law *check.Law, x syntax.Expr) string {
	if f := in.fileOf(law.Pkg, law.Decl); f != nil && x.Pos() >= 0 && int(x.End()) <= len(f.Src) {
		return f.Src[x.Pos():x.End()]
	}
	return "a side"
}

// underOf is t's underlying type, through every defined type.
func underOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return underOf(n.Underlying())
	}
	return t
}

// classNames are the classes of tables, in order, once each.
func classNames(tables [][]licenceClass) []string {
	var out []string
	for _, t := range tables {
		for _, c := range t {
			out = append(out, c.name)
		}
	}
	return out
}

// classesOf is t's class table, nil when t has none: a listable type's
// every value; a string's, an integer's and a float's classes; a slice's
// length classes, its elements at their first class.
func classesOf(t check.Type) []licenceClass {
	name := t.String()
	label := func(c string) string {
		if strings.HasPrefix(c, "=") {
			return name + c
		}
		return name + "." + c
	}
	if vs, ok := testplan.Domain(t); ok && len(vs) > 0 {
		out := make([]licenceClass, len(vs))
		for i, v := range vs {
			out[i] = licenceClass{name: name + "=" + describeListed(v), value: func(in *interp, fr *frame) Value { return in.listValue(fr, v) }}
		}
		return out
	}
	if n, ok := t.(*check.Named); ok && check.RefinementOf(n) != nil {
		return nil
	}
	switch u := underOf(t).(type) {
	case *check.Basic:
		switch u.Kind {
		case check.String:
			var out []licenceClass
			for _, c := range testgen.StringClasses() {
				s := c.Value
				out = append(out, licenceClass{name: label(c.Name), value: func(in *interp, fr *frame) Value {
					mustCharge(in, fr, int64(len(s)))
					return s
				}})
			}
			return out
		case check.Float32, check.Float64:
			bits := 64
			if u.Kind == check.Float32 {
				bits = 32
			}
			var out []licenceClass
			for _, c := range testgen.FloatClasses(bits) {
				f := c.Value
				out = append(out, licenceClass{name: label(c.Name), value: func(*interp, *frame) Value {
					if bits == 32 {
						return float32(f)
					}
					return f
				}})
			}
			return out
		}
		width, signed, ok := intWidth(u.Kind)
		if !ok {
			return nil
		}
		var out []licenceClass
		for _, c := range testgen.IntClasses(width, signed) {
			v, ok := fromUint64(u.Kind, c.Value)
			if !ok {
				return nil
			}
			out = append(out, licenceClass{name: label(c.Name), value: func(*interp, *frame) Value { return v }})
		}
		return out
	case *check.Slice:
		elem := classesOf(u.Elem)
		if len(elem) == 0 {
			return nil
		}
		var out []licenceClass
		for _, c := range testgen.SliceLengths() {
			k := c.Value
			out = append(out, licenceClass{name: label(c.Name), value: func(in *interp, fr *frame) Value {
				mustCharge(in, fr, int64(k))
				xs := make([]Value, k)
				for i := range xs {
					xs[i] = elem[0].value(in, fr)
				}
				return newSlice(xs)
			}})
		}
		return out
	case *check.Union:
		// A union's classes are its variants (LICENCE-ADT): each is a value
		// of that variant, drawn at full size from a stream of its own, so
		// a recursive union's class of a node holds nodes.
		var out []licenceClass
		for _, v := range u.Variants {
			name := label(v.String())
			out = append(out, licenceClass{name: name, value: func(in *interp, fr *frame) Value {
				r := testgen.New(in.budget.Seed, name)
				r.Charge = func(k int64) { mustCharge(in, fr, k) }
				r.Start(4 * licenceSizes)
				return in.generate(fr, r, v, testplan.Depth, nil)
			}})
		}
		return out
	}
	return nil
}

// describeListed is a listed value's text in a class name.
func describeListed(v testplan.Value) string {
	if v.Scalar != nil {
		return fmt.Sprint(v.Scalar)
	}
	parts := make([]string, len(v.Children))
	for i, c := range v.Children {
		parts[i] = describeListed(c)
	}
	return v.Type.String() + "(" + strings.Join(parts, ", ") + ")"
}

// intWidth is an integer kind's width in bits and its sign.
func intWidth(k check.BasicKind) (uint, bool, bool) {
	switch k {
	case check.Int, check.Int64, check.Duration:
		return 64, true, true
	case check.Int8:
		return 8, true, true
	case check.Int16:
		return 16, true, true
	case check.Int32:
		return 32, true, true
	case check.Uint, check.Uint64, check.Uintptr:
		return 64, false, true
	case check.Uint8:
		return 8, false, true
	case check.Uint16:
		return 16, false, true
	case check.Uint32:
		return 32, false, true
	}
	return 0, false, false
}
