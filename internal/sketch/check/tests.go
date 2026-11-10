package check

import (
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Tests and assert (196l; spec D-E4 and Q4). Each rule is an invariant over
// its whole domain:
//
//  1. A test declares no value. Test names have their own namespace, which
//     the package's top-level laws share (laws.go rule 1): a test is never
//     entered in a value scope, so a use of its name resolves as any
//     undeclared name does (S104), and a second test of one name, or a test
//     named like an earlier top-level law, is S108 at the second.
//  2. A test body is a failure target: what escapes it fails the test, so
//     its failure row is inferred and unbounded. Its service row, after its
//     own with blocks, is empty: a charging call that no with of the test
//     discharges is S102.
//  3. A property's parameters are generatable: basic types, and slices,
//     Maps, tuples, records and unions of generatable types, each named type
//     declared outside a std package (the prelude's data types, Option and
//     Range, are the language's own and are generated as a program's are;
//     a std package's type is built only by its constructor) and holding a
//     finite value
//     (ValueRanks). A named type is built as its package builds it: when
//     its package declares its constructor (TypeName.Constructor: New<T>
//     with no type parameters, the one result T, no error or capability row,
//     and parameters that package generates without reaching T), a
//     generator calls it with generated arguments, wherever T is declared
//     (std time.Time by time.NewTime); else it builds the value field by
//     field, which needs the authority of T's package. The rule is the one
//     law runs build a provider by (laws.go rule 8). A value of an
//     operation type is built by its builder (builders.go): a record that
//     holds an fs.Files holds an fstest.Memory, one per case. Anything else
//     is S108 at the parameter's type.
//  4. assert stands only in a test body or a law body (laws.go): every
//     assert whose lexical owner, the innermost test, law, function
//     declaration or function literal around it, is not a test or a law is
//     S108. A function literal inside a test is not the test's body. The
//     rule is syntactic over the whole file (strayAsserts), so no statement
//     position escapes it, whichever checker path types the statement.
//  5. assert takes a bool (S108); a fallible operand must be tried (S105,
//     from valueExpr, as for every value position).
//
// Prior art: Elixir's ExUnit `test "…" do … end` and MoonBit's
// `test "…" { … }` are declarations with no value; Gleam's gleeunit runs
// `_test` functions; Go's testing package passes a *testing.T. Property
// parameters follow QuickCheck's Arbitrary and Hypothesis's strategies,
// derived from the type: Elixir's StreamData and Gleam's qcheck build a
// generator per type the same way, a sized one for recursive data.

// testState is the checker's state for tests and assert.
type testState struct {
	// stray holds each assert outside a test or law body, already reported
	// by strayAsserts; the body check types its operand only.
	stray map[*syntax.AssertStmt]bool
	// names is the one namespace of the package's tests and top-level laws
	// (rule 1; laws.go rule 1): each name maps to the kind, "test" or
	// "law", of its first declaration in source order.
	names map[string]string
	all   []*Test // every test declaration, a duplicate name included
	ranks *ValueRanks
	// sees is the set of packages the package sees (builders.go rule 2),
	// and testPlanned marks its test builders planned.
	sees        map[*Package]bool
	testPlanned bool
}

// collectTest records a test declaration (rule 1). Its parameters are typed
// by checkTests, once every type is resolved.
func (c *checker) collectTest(d *syntax.TestDecl, fi *fileInfo) {
	t := &Test{Name: d.Name.Name, Decl: d, file: fi}
	c.ownBody(d, OwnerTest)
	// A second test of a name is checked as well, so which one is reported
	// does not decide which body's diagnostics appear (declaration order).
	c.tests.all = append(c.tests.all, t)
	if !c.claimTestName(d.Name, "test") {
		return
	}
	c.pkg.Tests = append(c.pkg.Tests, t)
}

// claimTestName claims id's name for a test or a top-level law (kind) in
// their one namespace (rule 1). A test report and a law report name the
// declaration by its name alone (#162), so two of one name would be
// ambiguous: the second in source order is S108, and claimTestName reports
// false. A member law is not in this namespace (its report names T.L).
func (c *checker) claimTestName(id *syntax.Ident, kind string) bool {
	if c.tests.names == nil {
		c.tests.names = map[string]string{}
	}
	prior, taken := c.tests.names[id.Name]
	switch {
	case !taken:
		c.tests.names[id.Name] = kind
		return true
	case prior == kind:
		// The message's form is the one the declaration-order oracle reads
		// as a duplicate (export_classh_test.go, duplicateName).
		c.errorf(id, CodeType, "%s %s is declared twice", kind, id.Name)
	default:
		c.errorf(id, CodeType, "%s %s is named like %s %s", kind, id.Name, prior, id.Name)
	}
	return false
}

// strayAsserts reports every assert that stands outside a test body and
// outside a law body (rule 4). It runs right after collect, over every file
// declaration, so it sees every statement the parser produced. What decides
// is an assert's lexical owner, the innermost body around it: a test or a
// law admits it; a function declaration, and a function literal wherever
// it stands (in a test or a law too), does not, since a literal's body runs
// when the literal is called, not as the test's or the law's own statement.
// A trailing block and a with body are blocks of their owner, not bodies.
func (c *checker) strayAsserts() {
	var walk func(root syntax.Node, admits bool)
	walk = func(root syntax.Node, admits bool) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if n == root {
				return true
			}
			switch n := n.(type) {
			case *syntax.TestDecl, *syntax.LawDecl:
				walk(n, true)
				return false
			case *syntax.FuncLit:
				walk(n, false)
				return false
			case *syntax.AssertStmt:
				if admits {
					return true
				}
				if c.tests.stray == nil {
					c.tests.stray = map[*syntax.AssertStmt]bool{}
				}
				c.tests.stray[n] = true
				c.errorf(n, CodeType, "assert stands only in a test or law body (196l)")
			}
			return true
		})
	}
	for _, fi := range c.files {
		c.cur = fi
		walk(fi.syntax, false)
	}
	c.cur = nil
}

// assertStmt checks `assert e` (rule 5). A stray assert was reported by
// strayAsserts; its operand is still typed, so Info covers it.
func (c *checker) assertStmt(s *syntax.AssertStmt) {
	op := c.valueExpr(s.X, nil)
	if c.tests.stray[s] || op.invalid() {
		return
	}
	if !isBoolean(op.typ) {
		c.errorf(s.X, CodeType, "assert takes a bool, not %s", op.typ)
	}
}

// checkTests checks each test body after every function body converged: no
// body refers to a test, so a test is a leaf of the body graph (rule 2).
func (c *checker) checkTests() {
	for _, t := range c.tests.all {
		c.checkTest(t)
	}
}

func (c *checker) checkTest(test *Test) {
	d := test.Decl
	saved := c.cur
	c.cur = test.file
	defer func() { c.cur = saved }()
	defer c.beginBodyCheck(test)()
	name := "test " + test.Name
	tg := &target{name: name, errors: newRow(), declUses: newRow(), resultKnown: true, isFunc: true}
	frame := newScope(c.pkg.Scope)
	frame.frame = true
	fc := &funcCtx{t: tg, scope: frame}
	prev := c.fn
	c.fn = fc
	defer func() { c.fn = prev }()
	if d.Params != nil {
		for _, field := range d.Params.List {
			var typ Type = invalid
			if field.Type == nil {
				for _, id := range field.Names {
					c.errorf(id, CodeType, "the parameter %s of %s needs a type", id.Name, name)
				}
			} else {
				typ = c.typExpr(field.Type, &typeCtx{param: true})
				if !isInvalid(typ) {
					if why := c.ungeneratable(typ); why != "" {
						c.errorf(field.Type, CodeType, "a property parameter is generated, and %s cannot be: %s (196l)", typ, why)
						typ = invalid
					}
				}
			}
			if len(field.Names) == 0 {
				test.Params = append(test.Params, &Var{Name: "_", Type: typ, Pos: field.Type.Pos(), param: true, used: true})
				continue
			}
			for _, id := range field.Names {
				v := &Var{Name: id.Name, Type: typ, Pos: id.Pos(), id: id, param: true, used: true}
				test.Params = append(test.Params, v)
				if id.Name == "_" {
					continue
				}
				if prior, ok := frame.names[id.Name].(*Var); ok && prior.param {
					c.errorf(id, CodeType, "%s is already a parameter of %s", id.Name, name)
					continue
				}
				v.refused = c.preludeName(id) // D-D3; the parameter still binds
				c.def(id, v)
				frame.insert(id.Name, v)
			}
		}
	}
	c.functionBody(d.Body, name)
	c.reportGoCopyWrites(d.Body)
	c.reportUnused(fc)
	c.reportNeverReassigned(fc)
}

// ungeneratable is why t is not a generatable type (rule 3), or "" when it
// is one.
func (c *checker) ungeneratable(t Type) string {
	return c.ungeneratableFrom(t, c.pkg)
}

// ungeneratableFrom is ungeneratable with the construction authority of the
// package from: a law run generates each value with the authority of the
// package that owns it (laws.go rule 7), a provider with its own package's.
func (c *checker) ungeneratableFrom(t Type, from *Package) string {
	seen := map[*Named]bool{}
	var why func(t Type) string
	why = func(t Type) string {
		switch u := t.(type) {
		case *Basic:
			return ""
		case *Slice:
			return why(u.Elem)
		case *Map:
			if s := why(u.Key); s != "" {
				return s
			}
			return why(u.Elem)
		case *Tuple:
			for _, e := range u.Types {
				if s := why(e); s != "" {
					return s
				}
			}
			return ""
		case *Named:
			if seen[u] {
				return "" // assumed while it is checked: a greatest fixpoint
			}
			seen[u] = true
			if _, isOps := u.Underlying().(*Interface); isOps {
				// A capability is built by its builder (builders.go).
				b, s := c.builderOf(u)
				if b == nil {
					return s
				}
				_, params := b.Instance(u)
				for _, p := range params {
					if s := why(p); s != "" {
						return s
					}
				}
				return ""
			}
			if s, built := c.constructorWhy(u); built {
				return s // built as its package builds it (rule 3)
			}
			if u.Obj.Pkg != nil {
				if _, isStd := std.Package(u.Obj.Pkg.Path); isStd {
					return u.String() + " is a standard library type"
				}
			}
			switch under := u.Underlying().(type) {
			case *Struct:
				if obj := literalAuthority(u, from); obj != nil {
					// A generator builds the record field by field: only
					// its package may (types.md, Zero values 6).
					return authorityText(obj)
				}
				for _, f := range under.Fields {
					if s := why(f.Type); s != "" {
						return s
					}
				}
				return ""
			case *Union:
				for _, v := range under.Variants {
					if s := why(v); s != "" {
						return s
					}
				}
				return ""
			case *Basic, *Slice, *Map, *Tuple:
				return why(under)
			case *Interface:
				return u.String() + " is an operation type"
			case *MachineType:
				return u.String() + " is a machine"
			}
			return u.String() + " is not a record, a union or a type over a generatable type"
		case *Interface:
			return t.String() + " is an operation type"
		case *Union:
			// A row value (q47): generated as one of its members, as a
			// union is as one of its variants.
			if len(u.Variants) == 0 {
				return "the empty row has no value"
			}
			for _, v := range u.Variants {
				if tp, ok := v.(*TypeParam); ok {
					return tp.Name + " is an error row parameter, whose members are not known"
				}
				if s := why(v); s != "" {
					return s
				}
			}
			return ""
		case *Signature:
			return t.String() + " is a function type"
		case *Pointer:
			return t.String() + " is a borrow"
		}
		return t.String() + " is not a generatable type"
	}
	if s := why(t); s != "" {
		return s
	}
	if c.tests.ranks == nil {
		c.tests.ranks = NewValueRanks()
	}
	for n := range seen {
		if c.tests.ranks.Rank(n) < 0 {
			return n.String() + " has no finite value: each of its values holds another"
		}
	}
	return ""
}

// constructorWhy reports whether a generator builds n by its constructor
// (rule 3), and why it cannot when that constructor's parameters reach n
// itself. A type of another package was planned by its own check.
func (c *checker) constructorWhy(n *Named) (string, bool) {
	tn := n.Obj
	if tn == nil || tn.Pkg == nil || n.Orig != nil || len(n.TArgs) > 0 || len(n.TParams) > 0 {
		return "", false
	}
	if tn.Pkg != c.pkg {
		return "", tn.ctor != nil
	}
	switch tn.ctorState {
	case 1:
		return n.String() + "'s constructor New" + tn.Name + " takes a value that holds a " + n.String(), true
	case 0:
		c.planConstructor(tn)
	}
	return "", tn.ctor != nil
}

// planConstructors plans the constructor of each type of the package
// (rule 3), once every body has converged: a constructor's rows are
// inferred.
func (c *checker) planConstructors() {
	c.planTestBuilds()
	for _, tn := range c.pkg.Types {
		if tn.ctorState == 0 {
			c.planConstructor(tn)
		}
	}
}

// planConstructor sets tn's constructor: the function New<T> of its
// package whose one result is T, with no type parameters, no error or
// capability row and parameters the package generates without reaching T.
// An operation type has its builders instead (builders.go), and a machine
// none: it is built by its own declaration.
func (c *checker) planConstructor(tn *TypeName) {
	tn.ctorState = 1
	defer func() { tn.ctorState = 2 }()
	n, ok := tn.Type.(*Named)
	if !ok {
		return
	}
	switch n.Underlying().(type) {
	case *Interface:
		c.planBuilders(tn, n)
		return
	case *MachineType:
		return
	}
	if len(n.TParams) > 0 {
		return
	}
	for _, f := range tn.Pkg.Funcs {
		sig := f.Sig
		if f.Name != "New"+tn.Name || sig == nil || len(sig.TParams) > 0 || len(sig.Results) != 1 || !c.identical(sig.Results[0], n) {
			continue
		}
		if rowHolds(sig.Errors) || rowHolds(sig.Uses) {
			continue
		}
		ok := true
		for _, v := range sig.Params {
			if isInvalid(v.Type) || c.ungeneratableFrom(v.Type, tn.Pkg) != "" {
				ok = false
			}
		}
		if ok {
			tn.ctor = f
			return
		}
	}
}

// ValueRanks computes the rank of generatable types: the least depth of a
// value of the type, where a basic type, a slice and a Map (empty) have
// rank 0, a record or a tuple one more than its greatest element, and a
// union one more than its least variant. A type with no finite value has
// rank -1. Lowering's generators read it to stop at a finite value: at the
// depth limit a union takes a variant of least rank. The checker refuses a
// property parameter of rank -1 (rule 3), so the two never disagree.
type ValueRanks struct {
	rank map[*Named]int
}

// NewValueRanks returns an empty memo.
func NewValueRanks() *ValueRanks { return &ValueRanks{rank: map[*Named]int{}} }

// infinite is the rank of a type with no finite value while it is solved.
const infinite = int(^uint(0) >> 1)

// Rank is t's rank, or -1 when t has no finite value. It solves the least
// fixpoint over the named types t reaches that are not solved yet, each
// round lowering at least one rank, so the work is quadratic in that set at
// worst and each named type is solved once per memo.
func (r *ValueRanks) Rank(t Type) int {
	var open []*Named
	inOpen := map[*Named]bool{}
	var reach func(t Type)
	reach = func(t Type) {
		switch u := t.(type) {
		case *Slice:
			reach(u.Elem)
		case *Map:
			reach(u.Key)
			reach(u.Elem)
		case *Tuple:
			for _, e := range u.Types {
				reach(e)
			}
		case *Named:
			if _, done := r.rank[u]; done || inOpen[u] {
				return
			}
			inOpen[u] = true
			open = append(open, u)
			reach(u.Underlying())
		case *Struct:
			for _, f := range u.Fields {
				reach(f.Type)
			}
		case *Union:
			for _, v := range u.Variants {
				reach(v)
			}
		}
	}
	reach(t)
	cur := make(map[*Named]int, len(open))
	for _, n := range open {
		cur[n] = infinite
	}
	var of func(t Type) int
	of = func(t Type) int {
		switch u := t.(type) {
		case *Named:
			if k, ok := cur[u]; ok {
				return k
			}
			if k, ok := r.rank[u]; ok && k >= 0 {
				return k
			}
			return infinite // solved with no finite value
		case *Tuple:
			return 1 + maxRank(u.Types, of)
		case *Struct:
			ts := make([]Type, len(u.Fields))
			for i, f := range u.Fields {
				ts[i] = f.Type
			}
			return 1 + maxRank(ts, of)
		case *Union:
			least := infinite
			for _, v := range u.Variants {
				if k := of(v); k < least {
					least = k
				}
			}
			if least == infinite {
				return infinite
			}
			return least + 1
		}
		return 0 // a basic type, a slice and a Map have an empty value
	}
	for changed := true; changed; {
		changed = false
		for _, n := range open {
			if k := of(n.Underlying()); k < cur[n] {
				cur[n], changed = k, true
			}
		}
	}
	for _, n := range open {
		if cur[n] == infinite {
			r.rank[n] = -1
		} else {
			r.rank[n] = cur[n]
		}
	}
	k := of(t)
	if k == infinite {
		return -1
	}
	return k
}

func maxRank(ts []Type, of func(Type) int) int {
	most := -1
	for _, t := range ts {
		k := of(t)
		if k == infinite {
			return infinite - 1
		}
		if k > most {
			most = k
		}
	}
	return most
}
