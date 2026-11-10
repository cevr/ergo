package check

import (
	"sort"
	"strings"
)

// Builders (tests.go rule 3, Capabilities; fu7a). A generator builds a
// value of an operation type I with I's builder, so a record that holds a
// capability (a file system, an output stream, a cell) is generated, and a
// law over it runs, with no constructor written only for the test. Each
// rule is an invariant over every operation type:
//
//  1. I's builder is its package's New<I>: a function with no error or
//     capability row whose one result is I. For a generic I, the function
//     has I's type parameters, in their order, and returns I of them
//     (sync.NewRef[T](v T) Ref[T]).
//  2. Else it is the one New<X> with no type parameters and no row, of I's
//     package or of I's test package (the package whose path is I's path
//     with "test" after it) when the package under check sees that package
//     (itself, the prelude and its imports at any depth, as law runs see
//     providers, laws.go rule 7), whose one result X is a named type that
//     provides I by its operations: fstest.NewMemory for fs.Files and
//     fs.Writes, io.NewRecorder for io.Stdout, io.NewInput for io.Stdin.
//     Two such functions are no builder: a generator does not choose.
//  3. The builder's parameters are generated, as a constructor's are.
//  4. A builder with no parameters builds the case's world: a case builds
//     one value with it, and every value of an operation type it builds in
//     that case is that value, so a record's fs.Files and fs.Writes are
//     one file system. Each case builds its own (Builder.World). A builder
//     with parameters builds a new value each time: two sync.Ref fields
//     are two cells.
//  5. A builder has no row, so a host provider (fs.NewHost and io.NewHost
//     need Ambient) is never one: a generated value never reads the disk
//     or writes the terminal.
//
// queue.Queue has no builder: its constructor is queue.New, not
// New<Queue>, and its capacity bounds what may be pushed before a Pop.
//
// Prior art: Effect provides a test layer per service (TestClock, a
// TestConsole), chosen by the test's author; Go's testing/fstest.MapFS and
// Elixir's Mox are written into each test by hand. Ergo picks the provider
// by its name and its package, as it picks a record's constructor (rule 1 is
// New<T>), and the type checker proves it has no row.

// Builder is how a generator builds a value of an operation type (rules 1
// to 4): the call of Func with generated arguments, its result converted
// to the operation type by Conv (zero when Func returns the operation
// type itself).
type Builder struct {
	Func *Func
	Conv Conversion
}

// World reports whether b builds the case's one value (rule 4).
func (b *Builder) World() bool { return len(b.Func.Sig.Params) == 0 }

// Instance is the call of b that builds a value of n: Func's type
// arguments (n's, rule 1) and the types of its parameters.
func (b *Builder) Instance(n *Named) (targs, params []Type) {
	sig := b.Func.Sig
	if len(sig.TParams) == 0 {
		for _, v := range sig.Params {
			params = append(params, v.Type)
		}
		return nil, params
	}
	m := paramMap(sig.TParams, n.TArgs)
	for _, v := range sig.Params {
		params = append(params, n.ctx.subst(v.Type, m))
	}
	return n.TArgs, params
}

// Builder is the builder of the operation type n in the test build of p,
// or nil when n has none (rules 1 and 2). The lowering reads it; the
// checker admitted a generator of n in p only when it is not nil.
func (p *Package) Builder(n *Named) *Builder {
	b, _ := builderOf(n, lawClosures(p)[p])
	return b
}

// builderOf is n's builder for a package that sees the packages sees, or
// why n has none.
func builderOf(n *Named, sees map[*Package]bool) (*Builder, string) {
	tn := n.Obj
	if tn.newOp != nil {
		return tn.newOp, ""
	}
	cands := append([]*Builder(nil), tn.provides...)
	if tn.Pkg != nil {
		for q := range sees {
			if q.Path == tn.Pkg.Path+"test" {
				cands = append(cands, q.testBuilds[tn]...)
			}
		}
	}
	switch len(cands) {
	case 1:
		return cands[0], ""
	case 0:
		pkg := ""
		if tn.Pkg != nil {
			pkg = tn.Pkg.Path
		}
		return nil, n.String() + " is an operation type with no builder: neither " + pkg + " nor " + pkg + "test, as this package sees them, declares a New<X> with no row whose X provides it"
	}
	names := make([]string, len(cands))
	for i, b := range cands {
		names[i] = b.Func.Pkg.Path + "." + b.Func.Name
	}
	sort.Strings(names)
	return nil, n.String() + " is an operation type with more than one builder (" + strings.Join(names, ", ") + "), and a generator does not choose"
}

// builderOf is builderOf for the package under check, planning n's own
// package's builders first when n is declared in it.
func (c *checker) builderOf(n *Named) (*Builder, string) {
	if tn := n.Obj; tn.Pkg == c.pkg && tn.ctorState == 0 {
		c.planConstructor(tn)
	}
	c.planTestBuilds()
	if c.tests.sees == nil {
		c.tests.sees = lawClosures(c.pkg)[c.pkg]
	}
	return builderOf(n, c.tests.sees)
}

// planBuilders sets the builders the operation type tn's own package
// declares (rules 1 and 2).
func (c *checker) planBuilders(tn *TypeName, n *Named) {
	for _, f := range tn.Pkg.Funcs {
		r := newResult(f)
		if r == nil {
			continue
		}
		if r.Obj == tn {
			if ownBuilder(f.Sig, n, r) {
				tn.newOp = &Builder{Func: f}
			}
			continue
		}
		if b := c.providerBuilder(f, r, n); b != nil {
			tn.provides = append(tn.provides, b)
		}
	}
}

// planTestBuilds sets, once, the builders a test package declares for the
// operation types of the package it tests (rule 2): fstest's for fs.
func (c *checker) planTestBuilds() {
	if c.tests.testPlanned {
		return
	}
	c.tests.testPlanned = true
	base, ok := strings.CutSuffix(c.pkg.Path, "test")
	if !ok || base == "" {
		return
	}
	var under *Package
	for _, f := range c.pkg.files {
		for _, pn := range f.imports {
			if pn.Pkg != nil && !pn.broken && pn.Pkg.Path == base {
				under = pn.Pkg
			}
		}
	}
	if under == nil {
		return
	}
	for _, f := range c.pkg.Funcs {
		r := newResult(f)
		if r == nil || r.Obj.Pkg != c.pkg {
			continue
		}
		for _, tn := range under.Types {
			n, ok := tn.Type.(*Named)
			if !ok {
				continue
			}
			if b := c.providerBuilder(f, r, n); b != nil {
				if c.pkg.testBuilds == nil {
					c.pkg.testBuilds = map[*TypeName][]*Builder{}
				}
				c.pkg.testBuilds[tn] = append(c.pkg.testBuilds[tn], b)
			}
		}
	}
}

// newResult is the named result X of f when f is New<X> with no error or
// capability row, else nil.
func newResult(f *Func) *Named {
	sig := f.Sig
	if sig == nil || len(sig.Results) != 1 || rowHolds(sig.Errors) || rowHolds(sig.Uses) {
		return nil
	}
	r, ok := sig.Results[0].(*Named)
	if !ok || r.Obj == nil || f.Name != "New"+r.Obj.Name {
		return nil
	}
	return r
}

// ownBuilder reports whether sig, whose result r is of n's declaration, is
// n's New<I> (rule 1): generic in n's type parameters, in order.
func ownBuilder(sig *Signature, n, r *Named) bool {
	if len(sig.TParams) != len(n.TParams) || len(r.TArgs) != len(n.TParams) {
		return false
	}
	for i, tp := range sig.TParams {
		if tp.Kind != KindType || r.TArgs[i] != Type(tp) {
			return false
		}
	}
	return true
}

// providerBuilder is f as a builder of the operation type n (rule 2): f
// and n are not generic, and f's result r provides n by its operations.
func (c *checker) providerBuilder(f *Func, r, n *Named) *Builder {
	if _, isOps := n.Underlying().(*Interface); !isOps || len(n.TParams) > 0 || len(f.Sig.TParams) > 0 || len(r.TArgs) > 0 || r == n {
		return nil
	}
	switch r.Underlying().(type) {
	case *Interface, *MachineType:
		return nil
	}
	a, _ := c.assignability(r, n)
	if !a.ok() || a.conv.Kind != ConvOperations {
		return nil
	}
	return &Builder{Func: f, Conv: a.conv}
}
