package lower

import (
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The fact export (FACT0; plan IR-G §3 item 6, §2 items 1 to 8). A fact
// is a condition the checker or the lowering proved true at a point of a
// function. The run build exports each one, so that the Go compiler can
// take it as an assumption: FACT1 puts range facts into Go's prove pass,
// ALIAS1 gives disjoint borrows separate alias classes, PURE1 lets Go's
// optimizer merge, drop and hoist pure calls. Those consumers are Go
// halves that wait for the IR batch; this file is the one export they all
// read (a second export is a finding, plan §5).
//
// The kinds, each a row of the shortcut registry (shortcuts.go):
//   - fact-range: a parameter of a refined type is inside the hull of its
//     where, lo <= p <= hi, or lo <= len(p) <= hi (check/refine.go,
//     Refinement.Bounds). Proved: only a checked constant and the type's
//     derived parse function make a value of the type, and a parameter is
//     never written. It holds in the whole body.
//   - fact-index and fact-cut: an index S[i] or a cut s[a:b] is in range
//     at its site, by the bounds facts of a dominating compare (bounds.go,
//     BCE1). Checked: the compare runs. They hold at the site.
//   - fact-disjoint: two borrow parameters of one function never name
//     overlapping places, since the checker refuses every call that would
//     pass two overlapping borrows (types.md, Borrows 2). Proved. It holds
//     in the whole body.
//   - fact-pure: the function writes nothing its caller can read: it is
//     pure (pure.go: no environment, an empty footprint, pure callees) and
//     takes no borrow, so each argument is a copy. Proved. It says nothing
//     of determinism (a map's order), which PURE1 takes from MAP1.
//
// One admission function, admitFact, decides which facts leave: proved or
// checked only. A tested fact (a law that passed its cases) never selects
// a shortcut whose failure is memory corruption (q115), so it never
// leaves, and neither does a fact with no evidence. A test build exports
// nothing (takes is false there) and writes the checking form instead: a
// range fact is a check at the function's entry, and two borrows of one
// Go type are compared, each raising a defect that names the fact and its
// .ergo position. A bounds fact's checking form is Go's own index check,
// which the test build keeps. A pure fact needs none: its arguments are
// copies, so the call cannot change them. factExport "off" is the
// reference, -d=ergofacts=0: no fact leaves, and the Go is the same.
//
// Facts writes the section as JSON (the fact section of ergoir.json once
// the IR builder writes one), and `ergo inspect facts` prints it.
// TestAssumptionsAgree holds the export to a model of generated programs.
//
// Prior art: LLVM's llvm.assume, nonnull and range metadata carry facts to
// the optimizer, and Rust feeds them from its types (NonZero, niches, the
// noalias of &mut); Zig makes an assumption a checked unreachable in its
// safe builds, as the test build does here; SPARK removes a run-time check
// only when it proves it. Gleam, ReScript and Elixir pass no fact to
// their host: the BEAM and JavaScript engines learn ranges only from
// their own run-time profiling.

// factKind is a kind of fact, the name of its registry row.
type factKind string

const (
	factRange    factKind = "fact-range"
	factIndex    factKind = "fact-index"
	factCut      factKind = "fact-cut"
	factDisjoint factKind = "fact-disjoint"
	factPure     factKind = "fact-pure"
)

// The evidence labels of a fact (facts.md, Evidence 1).
const (
	evidenceProved  = "proved"
	evidenceChecked = "checked"
	evidenceTested  = "tested"
	evidenceNone    = "none"
)

// factExport "off" exports no fact: the reference of every fact kind,
// -d=ergofacts=0 (plan IR-G §2 item 8).
var factExport string

// factPlantForTest is a test seam, nil outside tests: it edits each fact
// before the build exports it or writes its checking form, so a test can
// plant a false one.
var factPlantForTest func(*Fact)

// Fact is one exported fact.
type Fact struct {
	// Kind is the fact's kind: fact-range, fact-index, fact-cut,
	// fact-disjoint or fact-pure.
	Kind string `json:"kind"`
	// Func is the Ergo function (Type.Method for a method) it holds in.
	Func string `json:"func"`
	// At is the .ergo position it holds at, file:line:column: the
	// parameter of an entry fact, the site of a bounds fact, the function
	// of a pure fact.
	At string `json:"at"`
	// Subjects are the places it names: parameters and locals.
	Subjects []string `json:"subjects"`
	// Holds is the condition, in Ergo's syntax.
	Holds string `json:"holds"`
	// Lo and Hi are a range fact's bounds, decimal; empty for no bound.
	Lo string `json:"lo,omitempty"`
	Hi string `json:"hi,omitempty"`
	// Evidence is proved or checked: no other label leaves admitFact.
	Evidence string `json:"evidence"`
	// Reason is what proves it.
	Reason string `json:"reason"`
}

// factSet is the facts a build exported, once each, in the order met.
type factSet struct {
	list []placedFact
	seen map[string]bool
}

type placedFact struct {
	Fact
	file      string
	line, col int
}

// admitFact is the admission of every fact kind: it reports whether this
// build exports a fact of the kind with the evidence. Only a proved or a
// checked fact leaves; a test build and factExport "off" export none.
func (l *lowerer) admitFact(kind factKind, evidence string) bool {
	if evidence != evidenceProved && evidence != evidenceChecked {
		return false
	}
	switch kind {
	case factRange:
		return l.takes("fact-range")
	case factIndex:
		return l.takes("fact-index")
	case factCut:
		return l.takes("fact-cut")
	case factDisjoint:
		return l.takes("fact-disjoint")
	case factPure:
		return l.takes("fact-pure")
	}
	return false
}

// addFact exports f, which holds at n, when the build admits it, and
// reports whether it did.
func (fc *funcCtx) addFact(n syntax.Node, f Fact) bool {
	l := fc.l
	if !l.admitFact(factKind(f.Kind), f.Evidence) {
		return false
	}
	file, line, col := l.locate(n, n.Pos())
	f.At = l.siteText(n)
	f.Func = fc.owner
	key := f.Kind + "\x00" + f.At + "\x00" + strings.Join(f.Subjects, "\x00")
	if l.facts.seen[key] {
		return true
	}
	if l.facts.seen == nil {
		l.facts.seen = map[string]bool{}
	}
	l.facts.seen[key] = true
	l.facts.list = append(l.facts.list, placedFact{Fact: f, file: file, line: line, col: col})
	l.took(f.Kind)
	return true
}

// factList is the exported facts by position, then kind and subjects.
func (l *lowerer) factList() []Fact {
	ps := append([]placedFact(nil), l.facts.list...)
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		switch {
		case a.file != b.file:
			return a.file < b.file
		case a.line != b.line:
			return a.line < b.line
		case a.col != b.col:
			return a.col < b.col
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		}
		return strings.Join(a.Subjects, ",") < strings.Join(b.Subjects, ",")
	})
	out := make([]Fact, len(ps))
	for i, p := range ps {
		out[i] = p.Fact
	}
	return out
}

// Facts is the fact section of the run build of u: every fact the build
// exports, by position. It lowers u as Lower does and writes no file.
func Facts(u Unit) ([]Fact, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	l := newLowerer(u)
	if _, err := l.program(); err != nil {
		return nil, err
	}
	return l.factList(), nil
}

// FactsText is facts as text, one line each: position, kind, function,
// condition, evidence and reason.
func FactsText(facts []Fact) string {
	var b strings.Builder
	for _, f := range facts {
		b.WriteString(f.At + "  " + f.Kind + "  " + f.Func + "  " + f.Holds + "  (" + f.Evidence + ": " + f.Reason + ")\n")
	}
	return b.String()
}

// entryParam is a named parameter of the function being lowered.
type entryParam struct {
	v      *check.Var
	id     *syntax.Ident
	goName string
}

// entryFacts exports the facts that hold in the whole body of f, whose
// parameters are bound, or writes their checking forms in a test build:
// the range of each refined parameter, the disjointness of each two
// borrow parameters, and f's purity (pure: f is lowered pure).
func (fc *funcCtx) entryFacts(f *check.Func, pure bool) {
	if f.Decl == nil || f.Decl.Type == nil || f.Decl.Type.Params == nil {
		return
	}
	var ps []entryParam
	for _, field := range f.Decl.Type.Params.List {
		for _, id := range field.Names {
			v, ok := f.Pkg.Info.Defs[id].(*check.Var)
			if !ok || v == nil || fc.locals[v] == "" {
				continue
			}
			ps = append(ps, entryParam{v: v, id: id, goName: fc.locals[v]})
		}
	}
	borrows := 0
	for _, p := range ps {
		if _, ok := p.v.Type.(*check.Pointer); ok {
			borrows++
		}
		if fact, ok := rangeFact(p.v); ok {
			fc.factOrCheck(p.id, fact, func(f Fact) string {
				subj := p.goName
				if check.RefinementOf(p.v.Type).Len {
					subj = "len(" + subj + ")"
				}
				return boundsText(subj, f.Lo, f.Hi)
			})
		}
	}
	for i, a := range ps {
		pa, ok := a.v.Type.(*check.Pointer)
		if !ok {
			continue
		}
		for _, b := range ps[i+1:] {
			pb, ok := b.v.Type.(*check.Pointer)
			if !ok {
				continue
			}
			fact := Fact{
				Kind:     string(factDisjoint),
				Subjects: []string{a.v.Name, b.v.Name},
				Holds:    a.v.Name + " and " + b.v.Name + " do not overlap",
				Evidence: evidenceProved,
				Reason:   "every call passes two borrows that do not overlap (types.md, Borrows 2)",
			}
			// Two places of one Go type that do not overlap have two
			// addresses, unless the type has no size. A borrow of a
			// field and one of its record have two types: no cheap
			// check sees their overlap, so none is written.
			same := fc.goType(pa) == fc.goType(pb) && !mayBeEmpty(pa.Elem)
			fc.factOrCheck(b.id, fact, func(Fact) string {
				if !same {
					return ""
				}
				return a.goName + " != " + b.goName
			})
		}
	}
	if pure && borrows == 0 && f.Decl.Name != nil {
		fc.addFact(f.Decl.Name, Fact{
			Kind:     string(factPure),
			Subjects: []string{f.Name},
			Holds:    f.Name + " writes nothing its caller can read",
			Evidence: evidenceProved,
			Reason:   "pure, with no borrow parameter (pure.go)",
		})
	}
}

// factOrCheck exports fact, which holds at n, or, in a test build, writes
// its checking form: the Go condition check gives, raising a defect that
// names the fact where it is false. check returns "" when no cheap check
// exists.
func (fc *funcCtx) factOrCheck(n syntax.Node, fact Fact, check func(Fact) string) {
	if factPlantForTest != nil {
		factPlantForTest(&fact)
	}
	if fc.addFact(n, fact) || fc.l.tests == nil {
		return
	}
	cond := check(fact)
	if cond == "" {
		return
	}
	reason := fact.Kind + ": " + fact.Holds + " is false in " + fc.owner + " (" + fact.Reason + ")"
	fc.line("if !(" + cond + ") {")
	fc.line("\tpanic(" + fc.l.rt() + ".UnreachableAt(" + strconv.Quote(reason) + ", " + strconv.Quote(fc.l.siteText(n)) + "))")
	fc.line("}")
}

// rangeFact is the range fact of v, a parameter, when its type is
// refined and its where bounds it.
func rangeFact(v *check.Var) (Fact, bool) {
	r := check.RefinementOf(v.Type)
	if r == nil {
		return Fact{}, false
	}
	lo, hi := r.Bounds(v.Type)
	if lo == nil && hi == nil {
		return Fact{}, false
	}
	f := Fact{
		Kind:     string(factRange),
		Subjects: []string{v.Name},
		Evidence: evidenceProved,
		Reason:   "type " + v.Type.String() + " where " + r.Text,
	}
	if lo != nil {
		f.Lo = lo.String()
	}
	if hi != nil {
		f.Hi = hi.String()
	}
	subj := v.Name
	if r.Len {
		subj = "len(" + subj + ")"
	}
	f.Holds = boundsText(subj, f.Lo, f.Hi)
	return f, true
}

// boundsText is lo <= x && x <= hi, without an empty bound.
func boundsText(x, lo, hi string) string {
	var parts []string
	if lo != "" {
		parts = append(parts, lo+" <= "+x)
	}
	if hi != "" {
		parts = append(parts, x+" <= "+hi)
	}
	return strings.Join(parts, " && ")
}

// mayBeEmpty reports a type that may have no size in Go, so that two of
// its values may share an address: a record or tuple of such types, an
// array of none of them, a type it cannot tell.
func mayBeEmpty(t check.Type) bool {
	switch t := t.(type) {
	case *check.Named:
		if t.Defined() != nil {
			return mayBeEmpty(t.Defined())
		}
		return mayBeEmpty(t.Underlying())
	case *check.Basic, *check.Slice, *check.Map, *check.Pointer, *check.Signature:
		return false
	case *check.Array:
		return t.Len == 0 || mayBeEmpty(t.Elem)
	case *check.Struct:
		for _, f := range t.Fields {
			if !mayBeEmpty(f.Type) {
				return false
			}
		}
		return true
	case *check.Tuple:
		for _, e := range t.Types {
			if !mayBeEmpty(e) {
				return false
			}
		}
		return true
	}
	return true
}

// indexFact exports the fact of x, S[i], whose bounds facts prove it in
// range.
func (fc *funcCtx) indexFact(x *syntax.IndexExpr) {
	s, i := factText(x.X), factText(x.Indices[0])
	fc.addFact(x, Fact{
		Kind:     string(factIndex),
		Subjects: []string{s, i},
		Holds:    "0 <= " + i + " && " + i + " < len(" + s + ")",
		Evidence: evidenceChecked,
		Reason:   "a dominating compare (bounds.go)",
	})
}

// cutFact exports the fact of x, s[a:b], whose bounds facts prove it in
// range.
func (fc *funcCtx) cutFact(x *syntax.SliceExpr) {
	s := factText(x.X)
	lo, hi := "0", "len("+s+")"
	subjects := []string{s}
	if x.Low != nil {
		lo = factText(x.Low)
		subjects = append(subjects, lo)
	}
	if x.High != nil {
		hi = factText(x.High)
		subjects = append(subjects, hi)
	}
	fc.addFact(x, Fact{
		Kind:     string(factCut),
		Subjects: subjects,
		Holds:    "0 <= " + lo + " && " + lo + " <= " + hi + " && " + hi + " <= len(" + s + ")",
		Evidence: evidenceChecked,
		Reason:   "a dominating compare (bounds.go)",
	})
}

// factText is x in Ergo's syntax, for the forms a bounds fact reads: a
// name, a constant, len, a sum, a mask; any other form is "…".
func factText(x syntax.Expr) string {
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return "(" + factText(x.X) + ")"
	case *syntax.Ident:
		return x.Name
	case *syntax.BasicLit:
		return x.Value
	case *syntax.SelectorExpr:
		return factText(x.X) + "." + x.Sel.Name
	case *syntax.BinaryExpr:
		return factText(x.X) + " " + x.Op.String() + " " + factText(x.Y)
	case *syntax.UnaryExpr:
		return x.Op.String() + factText(x.X)
	case *syntax.CallExpr:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = factText(a)
		}
		return factText(x.Fun) + "(" + strings.Join(args, ", ") + ")"
	}
	return "…"
}
