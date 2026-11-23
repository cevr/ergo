package check

import (
	"sort"
	"strings"

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
//     holding a finite value (ValueRanks). A std package's type follows the
//     same rule as a program's (CU8): json.Optional is generated as Option
//     is, and a std record with an unexported field needs std's authority. A named type is built as its package builds it: when
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
//  6. skip "reason" stands only in a test body or in a law body that is
//     not a rewrite law (its lexical owner, as in rule 4; strayAsserts),
//     and its reason, a string literal, is not blank (skipStmt). Else
//     S108.
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
// declaration by its name alone, so two of one name would be
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
	var walk func(root syntax.Node, admits, skips bool)
	walk = func(root syntax.Node, admits, skips bool) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if n == root {
				return true
			}
			switch n := n.(type) {
			case *syntax.TestDecl:
				walk(n, true, true)
				return false
			case *syntax.LawDecl:
				// A rewrite law's sides and a reachability law's search are no
				// run: no skip ends them (rule 6).
				walk(n, true, n.Rewrite == nil && n.Reach == nil)
				return false
			case *syntax.FuncLit:
				walk(n, false, false)
				return false
			case *syntax.SkipStmt:
				if !skips {
					c.errorf(n, CodeType, "skip statements are only allowed in a test or law body (not a rewrite or reachability law, nor a function literal inside one; runtime.md, Tests 4)")
				}
			case *syntax.AssertStmt:
				if admits {
					return true
				}
				if c.tests.stray == nil {
					c.tests.stray = map[*syntax.AssertStmt]bool{}
				}
				c.tests.stray[n] = true
				c.errorf(n, CodeType, "assert stands only in a test or law body")
			}
			return true
		})
	}
	for _, fi := range c.files {
		c.cur = fi
		walk(fi.syntax, false, false)
	}
	c.cur = nil
}

// skipStmt checks `skip "reason"` (rule 6): its reason is not blank. A
// misplaced skip was reported by strayAsserts.
func (c *checker) skipStmt(s *syntax.SkipStmt) {
	if text, ok := s.Text(); !ok || strings.TrimSpace(text) == "" {
		c.errorIn(s, s.ReasonPos, s.ReasonEnd, CodeType, "skip names its reason: a string literal that is not blank (runtime.md, Tests 4)")
	}
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
	// A test with no parameters may run under a harness (harness.go); a
	// body the rewrite made binds its hidden names again.
	rewritten := rewrittenHarness(d.Body)
	if rewritten != "" {
		c.bindHarnessNames(frame, rewritten)
	} else if d.Params == nil {
		tg.harness = &harnessUse{}
	}
	mark := len(c.diags)
	fc := &funcCtx{t: tg, scope: frame, test: true}
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
						c.errorf(field.Type, CodeType, "a property parameter is generated, and %s cannot be: %s", typ, why)
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
				// A parameter _ is never in the frame (Scope.insert), so a
				// second _ is no second parameter.
				if prior, ok := frame.names[id.Name].(*Var); ok && prior.param {
					c.errorf(id, CodeType, "%s is already a parameter of %s", id.Name, name)
					continue
				}
				v.refused = c.refusedBinder(id) // the parameter still binds
				c.def(id, v)
				frame.insert(id.Name, v)
			}
		}
	}
	test.Ties = c.ties(test.Params, nil)
	c.functionBody(d.Body, name)
	c.reportGoCopyWrites(d.Body)
	c.reportUnused(fc)
	c.reportNeverReassigned(fc)
	if tg.harness != nil && tg.harness.h != nil && len(c.diags) == mark {
		c.rewriteHarnessed(test, tg.harness.h)
		c.fn = prev
		c.checkTest(test)
	}
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
			// A standard library type follows the same rule (CU8): its
			// package's authority over its fields, its constructor, its
			// builder; no exception for std.
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
	builder := func(n *Named) *Builder {
		if _, ops := n.Underlying().(*Interface); !ops {
			return nil
		}
		b, _ := c.builderOf(n)
		return b
	}
	// The builder that cannot start is the cause, so it is named before a
	// record that holds its type; else the first by name, so the text does
	// not depend on map order.
	order := make([]*Named, 0, len(seen))
	for n := range seen {
		order = append(order, n)
	}
	sort.Slice(order, func(i, j int) bool {
		bi, bj := builder(order[i]) != nil, builder(order[j]) != nil
		if bi != bj {
			return bi
		}
		return order[i].String() < order[j].String()
	})
	for _, n := range order {
		if c.tests.ranks.Rank(n, builder) < 0 {
			if b := builder(n); b != nil {
				// The builder needs a value of n before it builds one,
				// directly or through a cycle, and a generator does not
				// choose another (builders.go rule 2).
				return n.String() + " has no finite value: its builder " + builderName(n, b) + " takes an argument that has none, as a builder that needs its own type does"
			}
			return n.String() + " has no finite value: each of its values holds another"
		}
	}
	return ""
}

// builderName is b's function as a diagnostic about n names it: with n's
// own qualifier when n's package declares it, so llm's text says
// NewMetered beside Model and llm.NewMetered beside llm.Model, else by
// its package's path (fstest.NewMemory).
func builderName(n *Named, b *Builder) string {
	if b.Func.Pkg != n.Obj.Pkg {
		return b.Func.Pkg.Path + "." + b.Func.Name
	}
	q, _, _ := strings.Cut(n.String(), "[")
	return strings.TrimSuffix(q, n.Obj.Name) + b.Func.Name
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
		return n.String() + "'s constructor " + syntax.ConstructorName(tn.Name) + " takes a value that holds a " + n.String(), true
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
		if f.Name != syntax.ConstructorName(tn.Name) || sig == nil || len(sig.TParams) > 0 || len(sig.Results) != 1 || !c.identical(sig.Results[0], n) {
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
// union one more than its least variant. A named type is ranked as a
// generator builds it (rule 3): an operation type one more than the
// greatest parameter of its builder, and none with no builder; any other
// by its underlying type. So a builder that needs a value of its own
// type, directly or through a cycle (llm's NewMetered(inner Model) is
// Model's), gives it rank -1. A type with no finite value has rank -1.
// Every generator reads it to stop at a finite value: at the depth limit a
// union takes a variant of least rank, and every other step at that limit
// goes to a lower rank, so generation ends. The checker refuses a property
// parameter or a provider that reaches a type of rank -1 (rule 3), so the
// generators never meet one.
type ValueRanks struct {
	rank map[*Named]int
}

// NewValueRanks returns an empty memo. One memo ranks with one package's
// builders: the builder argument of each Rank is that package's
// (Package.Builder), since a test package may give an operation type its
// builder.
func NewValueRanks() *ValueRanks { return &ValueRanks{rank: map[*Named]int{}} }

// infinite is the rank of a type with no finite value while it is solved.
const infinite = int(^uint(0) >> 1)

// genInputs is what a generator draws to build a value of the operation
// type n: its builder's parameters, or none built when it has no builder
// (eval genBuilt, lower genFunc). built is false for any other type, which
// is ranked by its underlying type, a type with a constructor too: the
// constructor is chosen only when its parameters are generatable without
// reaching the type (planConstructor), which this same rank decides.
func genInputs(n *Named, builder func(*Named) *Builder) (params []Type, built, finite bool) {
	if _, ops := n.Underlying().(*Interface); ops {
		b := builder(n)
		if b == nil {
			return nil, true, false
		}
		_, params = b.Instance(n)
		return params, true, true
	}
	return nil, false, true
}

// Rank is t's rank, or -1 when t has no finite value, with builder the
// builder of an operation type (nil when it has none). It solves the least
// fixpoint over the named types t reaches that are not solved yet, each
// round lowering at least one rank, so the work is quadratic in that set at
// worst and each named type is solved once per memo.
func (r *ValueRanks) Rank(t Type, builder func(*Named) *Builder) int {
	var open []*Named
	inOpen := map[*Named]bool{}
	type input struct {
		params        []Type
		built, finite bool
	}
	inputs := map[*Named]input{}
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
			params, built, finite := genInputs(u, builder)
			inputs[u] = input{params, built, finite}
			if !built {
				reach(u.Underlying())
				return
			}
			for _, p := range params {
				reach(p)
			}
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
			k := infinite
			switch in := inputs[n]; {
			case !in.built:
				k = of(n.Underlying())
			case in.finite:
				k = 1 + maxRank(in.params, of)
			}
			if k < cur[n] {
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
