package eval

import (
	"fmt"
	"go/constant"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ConstKind is the form of a Const.
type ConstKind int

const (
	ConstBasic  ConstKind = iota + 1 // Basic
	ConstRecord                      // Named, with its fields in Elems
	ConstSlice                       // Elems
	ConstTuple                       // Elems
	ConstHole                        // a template's hole: Hole
)

// Const is a value a build-time fold computed that the lowering can write
// as a constant: a basic value, or a record, a slice or a tuple of
// constants (CF1). A record's Named is its declared type, a union's
// variant for a union value. Nothing else is a Const: a function value,
// a pointer, a map, a cell or a capability. In a template (CF2), a part
// is a hole: the value its expression has when the program runs, by its
// index among the template's holes.
type Const struct {
	Kind  ConstKind
	Basic constant.Value
	Named *check.Named
	Elems []Const
	Hole  int
}

// NoNative reports whether why, a refusal of FoldValue, is a call of a Go
// function the evaluator has no native of ("a Go function (name), even a
// pure one"). A std change that leaves an extern without its native turns
// folds off this way, so the fold counts it apart (lower FoldCounts).
func NoNative(why string) bool { return strings.Contains(why, "), even a pure one") }

// FoldValue computes the expression x of the declaration decl of pkg at
// build time (CF1): x reads no local, a package let it reads is computed
// on its first read (foldLet), and the budget bounds the work. It returns the value as a Const, or a
// reason it could not: the budget ran out (over is set), x reads what
// the evaluator does not run, or the value is not a Const.
func FoldValue(pkg *check.Package, decl syntax.Decl, x syntax.Expr, b Budget) (c Const, why string, over bool) {
	if b.Steps <= 0 || b.Depth <= 0 {
		return Const{}, "no evaluation budget", false
	}
	foldLets.Lock()
	defer foldLets.Unlock()
	if b.Bytes == 0 {
		b.Bytes = defaultBytes
	}
	in := &interp{budget: b, root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, globals: map[*check.Var]*slot{}, fold: true}
	defer func() {
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				panic(r)
			}
			c, why, over = Const{}, ie.Error(), false
		}
	}()
	fr := in.newFrame(pkg, decl)
	got, ab := in.expr(fr, x)
	if ab != nil {
		if ab.msg != "" {
			return Const{}, ab.msg, ab.kind == ctlExhausted
		}
		return Const{}, "the expression does not end in a value", false
	}
	out, ok := toConst(got, nil)
	if !ok {
		return Const{}, "the value holds a part a constant cannot: a function, a pointer, a map or a cell", false
	}
	return out, "", false
}

// foldLetSteps bounds one package let's initializer that a fold runs
// (foldLet), apart from the fold's own budget: a let that needs more stays
// unbound, and a fold that reads it stays a call. The fluid gallery's
// short class names (fluid/web NamesOf over every look) take 10M to 20M.
var foldLetSteps int64 = 50_000_000

// SetFoldLetStepsForTest sets foldLetSteps, forgets the lets the folds
// bound under the previous bound, and returns that bound.
func SetFoldLetStepsForTest(steps int64) int64 {
	foldLets.Lock()
	defer foldLets.Unlock()
	foldLets.sites = nil
	prev := foldLetSteps
	foldLetSteps = steps
	return prev
}

// letSite is where a package let is bound: its package, its let
// declaration and the spec whose initializer binds it.
type letSite struct {
	pkg  *check.Package
	decl *syntax.VarDecl
	spec *syntax.VarSpec
}

// letOutcome is a spec's initializer as a fold ran it: the lets it bound,
// or why it bound none.
type letOutcome struct {
	lets map[*check.Var]Value
	why  string
}

// foldLets is what the folds of one compiler process know of package lets
// (LAZY-LET): where each is bound, by package as a fold first looks, and
// each spec's outcome, run once. A let's value does not depend on the
// fold that reads it, so the folds share it. The lock is held for a whole
// fold; outcomes are dropped when there are many (a long-lived process).
var foldLets struct {
	sync.Mutex
	indexed  map[*check.Package]bool
	sites    map[*check.Var]letSite
	outcomes map[*syntax.VarSpec]letOutcome
}

// foldLetLimit bounds the outcomes foldLets keeps.
const foldLetLimit = 1 << 14

// foldLet is the slot of the package let v in a fold: its initializer runs
// on the fold's first read of it (Elixir's module attributes, Zig's
// comptime globals), as the built program runs it before main, in its own
// package's frame, with InFold true, under foldLetSteps; the lets it
// reads bind the same way. Nil when v is no package let the fold can bind:
// its initializer is a Go value, uses a capability or starts a task, does
// what the evaluator does not run, or runs past its bound. (A let that
// reads itself, directly or through others, is the checker's error.)
func (in *interp) foldLet(v *check.Var) *slot {
	fl := &foldLets
	if fl.sites == nil || len(fl.outcomes) > foldLetLimit {
		fl.indexed, fl.sites = map[*check.Package]bool{}, map[*check.Var]letSite{}
		fl.outcomes = map[*syntax.VarSpec]letOutcome{}
	}
	site, ok := fl.sites[v]
	if !ok {
		in.indexLets(in.root)
		if site, ok = fl.sites[v]; !ok {
			return nil
		}
	}
	out, done := fl.outcomes[site.spec]
	if !done {
		out = in.runLet(site)
		fl.outcomes[site.spec] = out
	}
	val, bound := out.lets[v]
	if !bound {
		return nil
	}
	s := &slot{v: val}
	in.globals[v] = s
	return s
}

// foldLetWhy is why a fold could not bind the package let v (foldLet).
func foldLetWhy(v *check.Var) string {
	fl := &foldLets
	site, ok := fl.sites[v]
	if !ok {
		return "no package of the fold declares it"
	}
	out := fl.outcomes[site.spec]
	switch {
	case out.why != "":
		return "its initializer: " + out.why
	}
	return "its initializer binds it to nothing"
}

// indexLets records where each package let of p and of the packages it
// imports is bound.
func (in *interp) indexLets(p *check.Package) {
	fl := &foldLets
	if p == nil || fl.indexed[p] || p.Host() || p.Info == nil {
		return
	}
	fl.indexed[p] = true
	for _, f := range p.Files() {
		for _, d := range f.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, s := range vd.Specs {
				site := letSite{pkg: p, decl: vd, spec: s}
				for _, id := range s.Names {
					if v, ok := p.Info.Defs[id].(*check.Var); ok {
						fl.sites[v] = site
					}
				}
				if s.Pattern != nil {
					syntax.Inspect(s.Pattern, func(n syntax.Node) bool {
						if id, ok := n.(*syntax.Ident); ok {
							if v, ok := p.Info.Defs[id].(*check.Var); ok {
								fl.sites[v] = site
							}
						}
						return true
					})
				}
			}
		}
	}
	for _, dep := range imports(p) {
		in.indexLets(dep)
	}
}

// runLet runs the initializer of site's spec as initPackage does, with
// a budget of its own: the steps and bytes it spends, its depth and a
// template run's state are the reading fold's again after it.
func (in *interp) runLet(site letSite) letOutcome {
	if site.decl.Extern != nil {
		return letOutcome{why: "a Go value"}
	}
	if fp := site.pkg.Info.Footprints[site.spec]; fp.Opaque && !fp.Tasks && len(fp.Caps) == 0 {
	} else if why := in.impure(site.pkg, site.spec); why != "" {
		return letOutcome{why: why}
	}
	budget, steps, bytes, stepsAt, bytesAt, depth, sym := in.budget, in.steps, in.bytes, in.stepsAt, in.bytesAt, in.depth, in.sym
	stepsEnd, perCase := in.stepsEnd, in.perCase
	in.budget.Steps, in.stepsAt, in.bytesAt, in.depth, in.sym = foldLetSteps, in.steps, in.bytes, 0, nil
	// The let's bound is one evaluation's, never a case count's, and
	// charge's cached end is the let's own (not the obligation's, which
	// lies past it).
	in.perCase, in.stepsEnd = 0, in.steps+foldLetSteps
	defer func() {
		in.budget, in.steps, in.bytes, in.stepsAt, in.bytesAt, in.depth, in.sym = budget, steps, bytes, stepsAt, bytesAt, depth, sym
		in.stepsEnd, in.perCase = stepsEnd, perCase
	}()
	fr := in.newFrame(site.pkg, site.decl)
	if ab := in.letSpec(fr, site.spec); ab != nil {
		return letOutcome{why: ab.msg}
	}
	out := letOutcome{lets: map[*check.Var]Value{}}
	fr.vars.each(func(v *check.Var, s *slot) {
		s.lend()
		out.lets[v] = s.v
	})
	return out
}

// Rowless reports a function whose row is empty, once each inferred row
// variable is solved: it needs no capability and fails with nothing, so a
// call of it with constant arguments is a constant (CF1).
func Rowless(fn *check.Func) bool { return rowless(fn) }

// toConst is v as a Const, or false when a part of it is none: holes
// index the symbols that are a template's holes.
func toConst(v Value, holes map[*symbol]int) (Const, bool) {
	if s, ok := v.(*symbol); ok {
		i, isHole := holes[s]
		return Const{Kind: ConstHole, Hole: i}, isHole
	}
	if c := toConstant(v); c != nil {
		return Const{Kind: ConstBasic, Basic: c}, true
	}
	switch v := v.(type) {
	case *record:
		if v.t == nil || (v.key != "" && len(v.fields) > 0) {
			// A generic record's fields are typed by its type arguments,
			// which the Const does not carry: not folded (CF1 r1).
			return Const{}, false
		}
		out := Const{Kind: ConstRecord, Named: v.t}
		for _, f := range v.fields {
			c, ok := toConst(f, holes)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	case sliceV:
		if v.array {
			return Const{}, false
		}
		out := Const{Kind: ConstSlice, Elems: []Const{}}
		for _, e := range v.elems() {
			c, ok := toConst(e, holes)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	case tuple:
		out := Const{Kind: ConstTuple}
		for _, e := range v.elems {
			c, ok := toConst(e, holes)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	}
	return Const{}, false
}

// FoldTemplate computes the call x of the declaration decl of pkg at build
// time as FoldValue does, with each expression of holes (subexpressions
// of x, none inside another) standing for the value it has when the
// program runs: a template (CF2). A hole is a symbol of its expression's
// type; the run moves it (a binding, an argument, a result, a record, a
// list or a tuple part) and stops at anything that reads it: a condition,
// an operator other than + on strings, a match that its type cannot
// decide, a field, an index, an extern go function's argument (build's
// Known and Hole excepted). The value is a Const whose parts may be holes.
// On a stop, culprits are the holes it read, by index, for the caller to
// widen (each to the argument or element around it) and run again; none
// when the run stopped for another reason.
//
// Prior art: partial evaluation with the data opaque (Futamura; Marko's
// and Solid's SSR compilers split a template's static markup from its
// dynamic expressions the same way, by syntax, where this run decides it
// by evaluation).
func FoldTemplate(pkg *check.Package, decl syntax.Decl, x syntax.Expr, holes []syntax.Expr, b Budget) (c Const, culprits []int, why string, over bool) {
	if b.Steps <= 0 || b.Depth <= 0 {
		return Const{}, nil, "no evaluation budget", false
	}
	foldLets.Lock()
	defer foldLets.Unlock()
	if b.Bytes == 0 {
		b.Bytes = defaultBytes
	}
	run := newSymRun()
	run.tmpl = true
	run.holes = map[syntax.Expr]*symbol{}
	index := map[*symbol]int{}
	for i, h := range holes {
		// A hole has its own type, before the variant goes into its
		// union, so a match can decide it (symMatch).
		t := pkg.Info.Types[h]
		if conv, ok := pkg.Info.Implicit[h]; ok && conv.Kind == check.ConvVariant && conv.From != nil {
			t = conv.From
		}
		s := &symbol{name: fmt.Sprintf("hole%d", i), t: t}
		run.holes[h] = s
		index[s] = i
	}
	in := &interp{budget: b, root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, globals: map[*check.Var]*slot{}, sym: run, fold: true, walk: true}
	blamed := func() []int {
		seen := map[int]bool{}
		var out []int
		for _, s := range run.culprits {
			if i, ok := index[s]; ok && !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
		return out
	}
	defer func() {
		if r := recover(); r != nil {
			// A form the run does not take a hole into: no template.
			c, culprits, why, over = Const{}, blamed(), fmt.Sprint(r), false
		}
	}()
	run.mark(pkg.Info, x)
	run.ok[x] = true
	fr := in.newFrame(pkg, decl)
	got, ab := in.expr(fr, x)
	if ab != nil {
		if ab.msg != "" {
			return Const{}, blamed(), ab.msg, ab.kind == ctlExhausted
		}
		return Const{}, nil, "the expression does not end in a value", false
	}
	out, ok := toConst(got, index)
	if !ok {
		run.culprits = nil
		run.blameUnwritten(got)
		return Const{}, blamed(), "the value holds a part a template cannot: a function, a pointer, a map, a cell or a computed hole", false
	}
	return out, nil, "", false
}

// blame records the holes in vs as the culprits of a template run's stop;
// nothing in any other run.
func (s *symRun) blame(vs ...Value) {
	if !s.tmpl {
		return
	}
	for _, v := range vs {
		s.culprits = appendSymbols(s.culprits, v)
	}
}

// blameUnwritten blames the holes inside the parts of v a template cannot
// write: a term computed from holes.
func (s *symRun) blameUnwritten(v Value) {
	switch v := v.(type) {
	case *symbol:
	case term:
		s.blame(v)
	case *record:
		for _, f := range v.fields {
			s.blameUnwritten(f)
		}
	case tuple:
		for _, e := range v.elems {
			s.blameUnwritten(e)
		}
	case sliceV:
		for _, e := range v.elems() {
			s.blameUnwritten(e)
		}
	}
}

// appendSymbols appends the symbols v holds to out.
func appendSymbols(out []*symbol, v Value) []*symbol {
	switch v := v.(type) {
	case *symbol:
		return append(out, v)
	case *extTerm:
		for _, a := range v.args {
			out = appendSymbols(out, a)
		}
	case *catTerm:
		for _, p := range v.parts {
			out = appendSymbols(out, p)
		}
	case chunk:
		for _, e := range v.elems {
			out = appendSymbols(out, e)
		}
	case *record:
		for _, f := range v.fields {
			out = appendSymbols(out, f)
		}
	case tuple:
		for _, e := range v.elems {
			out = appendSymbols(out, e)
		}
	case sliceV:
		for _, e := range v.elems() {
			out = appendSymbols(out, e)
		}
	}
	return out
}

// holeNative registers in t f as the native of the function named name,
// which a template run passes a hole (std build's questions about what the
// build knows): f answers for a symbol itself.
func holeNative(t nativeTables, name string, f native) {
	t.std[name] = f
	t.holes[name] = true
}

// symExtern is a template run's rule for a call of the function fn with
// no Ergo body: its native runs on known arguments, and an argument that
// holds a hole stops the run, except for a native holeNative registered.
func (in *interp) symExtern(fr *frame, at *syntax.CallExpr, fn *check.Func, args []Value) *abrupt {
	if nativeOf(fn) != nil && loadedNatives().holes[fn.Pkg.Path+"."+fn.Name] {
		return nil
	}
	for _, a := range args {
		if symbolic(a) {
			in.sym.blame(args...)
			return fr.abrupt(ctlUnsupported, at, nil, fn.Name+" reads a hole at "+in.siteText(fr, at.Pos()))
		}
	}
	return nil
}

// symMatch is a template run's test of the pattern p on v, a term or a
// value a constant pattern compares: a hole whose type is a record takes
// a type test of its type or of a union of it, and refuses one of
// another record; anything else stops the run.
func (in *interp) symMatch(fr *frame, at syntax.Node, p *check.Pattern, v Value) (bool, *abrupt) {
	if s, ok := v.(*symbol); ok && (p.Kind == check.PatTypeTest || p.Kind == check.PatRecord && len(p.Fields) == 0) {
		if yes, decided := symHasType(s, fr.rt(p.Type)); decided {
			return yes, nil
		}
	}
	in.sym.blame(v)
	what := describe(v)
	if s, ok := v.(*symbol); ok {
		what = fmt.Sprintf("%s, a hole of %s,", s.name, s.t)
	}
	return false, fr.abrupt(ctlUnsupported, at, nil, frameName(fr)+" matches on "+what+" at "+in.siteText(fr, at.Pos()))
}

// symHasType is whether a value of s's type has the type t, and whether
// the types decide it: s's type is a record that is no instance of a
// generic one, and t is a record or a union.
func symHasType(s *symbol, t rtype) (yes, decided bool) {
	n, ok := s.t.(*check.Named)
	if !ok || len(n.TArgs) > 0 || structOf(n) == nil {
		return false, false
	}
	t = t.env.resolve(t.t)
	var variants []check.Type
	switch tt := t.t.(type) {
	case *check.Union:
		variants = tt.Variants
	case *check.Named:
		if u, isUnion := tt.Underlying().(*check.Union); isUnion {
			variants = u.Variants
		} else {
			variants = []check.Type{tt}
		}
	default:
		return false, false
	}
	for _, vt := range variants {
		vn, ok := vt.(*check.Named)
		if !ok {
			return false, false
		}
		if vn.Obj == n.Obj {
			return len(vn.TArgs) == 0, len(vn.TArgs) == 0
		}
	}
	return false, true
}

// isSliceV reports a slice value.
func isSliceV(v Value) bool {
	_, ok := v.(sliceV)
	return ok
}

// isTerm reports a term: a hole or a value computed from one.
func isTerm(v Value) bool {
	_, ok := v.(term)
	return ok
}
