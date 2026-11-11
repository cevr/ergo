package lower

import (
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Scalar replacement of loop-carried union locals (OPT6b). A union that is
// a Go interface boxes each value put into it, and Go keeps a box on the
// stack only when nothing outlives the iteration that made it: a let mut
// of a union that a loop assigns, and that is read after the loop, holds
// boxes from one iteration in the next, so each write allocates. Such a
// local lives instead as a Go struct of the variant's index and one slot
// per variant (a local type, never a parameter or a result):
//
//		type at_sr struct { tag uint8; v0 Idle; v1 Busy }
//		var at at_sr
//
//	  - a write of the union value e takes it apart by a type switch into a
//	    new struct of its tag and its variant's slot (scatter): the box e
//	    came in, if any, outlives nothing;
//	  - a match on the local switches on its tag, and each case reads its
//	    variant's slot, as a match on a flat union does (tagSwitchOf);
//	  - any other read puts the slot of its tag into a union value of its
//	    own (gather), a box that lives as long as that read's value.
//
// A local takes the form when it is a let mut of one name with a value,
// its type is a union of the Go interface form (a flat union is a struct
// already) whose variants have distinct Go types, it is written by a
// plain assignment inside a loop that its let is outside of, and every
// other use of it reads its value: no closure names it, no address or
// borrow of it is taken, it is the receiver of no call and the target of
// no op-assignment. The type is declared in the function, so the form
// needs Go types for the variants: a function with type parameters
// takes it only where they are Go types (a copy for a known record,
// record.go).
//
// The reference is scalarLowering "off", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.scalarLowering=off.
// TestMachineFlatRunAgrees and TestScalarLocalsAgree run generated
// programs under both.
//
// Prior art: LLVM's SROA and GraalVM's partial escape analysis replace an
// aggregate that does not escape with its fields; GHC's worker/wrapper and
// unboxed sums carry a known sum as a tag and fields; OCaml's flambda
// unboxes a float or a block a loop carries. Gleam, Elixir and ReScript
// keep a variant a heap value (a tagged tuple, a JavaScript object) and
// rely on the host's allocator or escape analysis.
var scalarLowering string

// scalarMutant breaks one guard or helper of the scalar locals, for the
// mutant ledger only: "scatter-tag" (a write puts every variant at tag 0),
// "gather-slot" (a read gives the first variant's slot), "closure-blind"
// (a local a closure names takes the form) and "carried-blind" (a local no
// loop carries takes the form). Empty in every build.
var scalarMutant string

// scalarLocal is a local in the scalar form: its Go name, its local type,
// the union's Go type and its variants' Go types, by tag.
type scalarLocal struct {
	name, typ, union string
	variants         []string
}

// scalarVars are the locals of body that take the scalar form.
func (l *lowerer) scalarVars(body *syntax.BlockStmt, info *check.Info) map[*check.Var]bool {
	if scalarLowering == "off" || body == nil || info == nil {
		return nil
	}
	if out, ok := l.scalarMemo[body]; ok {
		return out
	}
	if l.scalarMemo == nil {
		l.scalarMemo = map[*syntax.BlockStmt]map[*check.Var]bool{}
	}
	out := scalarCandidates(body, info)
	l.scalarMemo[body] = out
	return out
}

// scalarCandidates is the rule above, but for the local's type.
func scalarCandidates(body *syntax.BlockStmt, info *check.Info) map[*check.Var]bool {
	decls := map[*check.Var]syntax.Node{}
	var loops []syntax.Node
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false // a literal's locals are its own frame's
		case *syntax.ForStmt, *syntax.RangeStmt:
			loops = append(loops, n)
		case *syntax.VarDecl:
			if !n.Mut.IsValid() {
				return true
			}
			for _, spec := range n.Specs {
				if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
					continue
				}
				if v, ok := info.Defs[spec.Names[0]].(*check.Var); ok {
					decls[v] = n
				}
			}
		}
		return true
	})
	if len(decls) == 0 {
		return nil
	}
	within := func(n, outer syntax.Node) bool { return outer.Pos() <= n.Pos() && n.End() <= outer.End() }
	carried := map[*check.Var]bool{}
	bad := map[*check.Var]bool{}
	use := func(x syntax.Expr) *check.Var {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return nil
		}
		v, _ := info.Uses[id].(*check.Var)
		if v == nil || decls[v] == nil {
			return nil
		}
		return v
	}
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			syntax.Inspect(n, func(m syntax.Node) bool {
				if id, ok := m.(*syntax.Ident); ok {
					if v := use(id); v != nil && scalarMutant != "closure-blind" {
						bad[v] = true
					}
				}
				return true
			})
			return false
		case *syntax.AssignStmt:
			if len(n.Lhs) != 1 {
				return true
			}
			v := use(n.Lhs[0])
			if v == nil {
				return true
			}
			if n.Tok != syntax.ASSIGN {
				bad[v] = true
				return true
			}
			for _, loop := range loops {
				if within(n, loop) && (!within(decls[v], loop) || scalarMutant == "carried-blind") {
					carried[v] = true
				}
			}
		case *syntax.UnaryExpr:
			if v := use(n.X); v != nil && n.Op == syntax.AND {
				bad[v] = true
			}
		case *syntax.CallExpr:
			if c := info.Calls[n]; c != nil && c.Recv != nil {
				if v := use(c.Recv); v != nil {
					bad[v] = true
				}
			}
			if c := info.Calls[n]; c == nil || c.Kind == check.CallWith {
				for _, a := range n.Args {
					if v := use(a); v != nil {
						bad[v] = true
					}
				}
			}
		}
		if x, ok := n.(syntax.Expr); ok && info.ContextValues[x] != nil {
			if v := use(x); v != nil {
				bad[v] = true
			}
		}
		return true
	})
	out := map[*check.Var]bool{}
	for v := range decls {
		if carried[v] && !bad[v] {
			out[v] = true
		}
	}
	return out
}

// scalarOf is the scalar form of v, declared now, when v takes it in the
// body being written; nil when it does not.
func (fc *funcCtx) scalarOf(v *check.Var) *scalarLocal {
	if v == nil || !fc.scalarSet[v] || fc.goGeneric {
		return nil
	}
	t := fc.boundType(v.Type)
	if mentionsTypeParam(t) {
		return nil
	}
	n, ok := t.(*check.Named)
	if !ok || fc.l.flatUnionOf(t) != nil {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || len(u.Variants) > 255 {
		return nil
	}
	sl := &scalarLocal{union: fc.goType(t)}
	for _, t := range u.Variants {
		g := fc.goType(t)
		if slices.Contains(sl.variants, g) {
			return nil
		}
		sl.variants = append(sl.variants, g)
	}
	return sl
}

// scalarLet writes the let of v, a local in the scalar form sl, with the
// value val: its local type, its variable and the value taken apart.
func (fc *funcCtx) scalarLet(v *check.Var, sl *scalarLocal, val string) {
	sl.name = fc.declare(v)
	sl.typ = fc.temp(v.Name + "_sr")
	var b strings.Builder
	b.WriteString("type " + sl.typ + " struct {\n")
	b.WriteString("\ttag uint8\n")
	for k, g := range sl.variants {
		b.WriteString("\t" + flatField(k) + " " + g + "\n")
	}
	b.WriteString("}")
	for _, ln := range strings.Split(b.String(), "\n") {
		fc.line(ln)
	}
	fc.line("var " + sl.name + " " + sl.typ)
	if fc.scalars == nil {
		fc.scalars = map[*check.Var]*scalarLocal{}
	}
	fc.scalars[v] = sl
	fc.scatter(sl, val)
	fc.line("_ = " + sl.name)
}

// scatter writes the union value val into the scalar local sl.
func (fc *funcCtx) scatter(sl *scalarLocal, val string) {
	x := fc.temp("x")
	fc.line("switch " + x + " := " + val + ".(type) {")
	for k, g := range sl.variants {
		tag := strconv.Itoa(k)
		if scalarMutant == "scatter-tag" {
			tag = "0"
		}
		fc.line("case " + g + ":")
		fc.line("\t" + sl.name + " = " + sl.typ + "{tag: " + tag + ", " + flatField(k) + ": " + x + "}")
	}
	fc.line("default:")
	fc.line("\t_ = " + x)
	fc.line("\tpanic(\"ergo: defect: a value of " + sl.union + " that is none of its variants\")")
	fc.line("}")
}

// gather is the union value of the scalar local sl, in a Go local of its
// own: the slot of its tag.
func (fc *funcCtx) gather(sl *scalarLocal) string {
	g := fc.temp("g")
	fc.line("var " + g + " " + sl.union)
	fc.line("switch " + sl.name + ".tag {")
	for k := range sl.variants {
		slot := k
		if scalarMutant == "gather-slot" {
			slot = 0
		}
		fc.line("case " + strconv.Itoa(k) + ":")
		fc.line("\t" + g + " = " + sl.name + "." + flatField(slot))
	}
	fc.line("}")
	return g
}

// scalarIdent is the scalar local x names, if it names one.
func (fc *funcCtx) scalarIdent(x syntax.Expr) *scalarLocal {
	if fc.scalars == nil {
		return nil
	}
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil
	}
	return fc.scalars[v]
}

// scalarTags is the tag switch of a match on the scalar local subject
// (its Go name) over the Go types types; nil when subject names none.
func (fc *funcCtx) scalarTags(subject string, types []string) *tagSwitch {
	var sl *scalarLocal
	for _, s := range fc.scalars {
		if s.name == subject {
			sl = s
		}
	}
	if sl == nil {
		return nil
	}
	tags := make([]int, len(types))
	for i, t := range types {
		k := slices.Index(sl.variants, t)
		if k < 0 {
			return nil
		}
		tags[i] = k
	}
	return &tagSwitch{tags: tags, head: "switch " + subject + ".tag {", variant: func(k int) string { return subject + "." + flatField(k) }}
}
