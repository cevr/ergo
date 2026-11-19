package lower

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// InForce (rows.md, Context values, InForce; CK-rowarg part 3). The
// prelude's InForce[R]() is the providers of R in force at the call, as a
// Context[R] value: the Go function value of every Context (contextvalues.go)
// that copies R's fields from a copy of the environment taken at the call.
// Rows are erased, so where R is a row parameter the function does not know
// R's fields: a row parameter that an InForce call names, or that a call
// binds to a held parameter of its callee, is **held**, and lowers to a Go
// type parameter constrained by the copier interface:
//
//	type ergoEnvCopier interface{ copyEnv(d *Env, s Env) }
//
// Its Go argument at a call is a type whose copyEnv copies the row's
// fields: a generated empty struct per set of capabilities, the caller's
// own held parameter, or ergoCapsJoin[A, B] of those. Go passes the type
// through its generics' dictionaries, so holding a row costs no value and
// no allocation; the copy is a call per part of the row. Held is the least
// fixed point over the program's calls (heldRows), computed in the naming
// pass, which also names each set's type.
//
// The value borrows the with blocks in force at the call. The environment
// carries the scope of the innermost owning with (envWith), which a with
// that acquires a provider with a Close sets; installing the value after
// that scope ended is a defect (v1.HeldLive), since a provider it holds
// may be closed. A with that owns no provider sets nothing: its providers
// are values, and the program carries the field only when it holds a row.
//
// Prior art: Effect's `Effect.context<R>()` reads the services in force
// as a value, which `Effect.provide` installs; ZIO's `ZIO.environment[R]`.
// Both are maps keyed by tag at run time; Ergo's rows are erased, so the
// set of fields travels as a Go type argument instead, as Go's own
// dictionaries carry a type's methods. Gleam, ReScript and Elixir have no
// rows and no ambient providers to capture.

// envWith is the field of the environment that holds the scope of the
// innermost owning with in force, for a program that holds a row.
const envWith = "with"

// heldInfo is the held analysis of the program (heldRows).
type heldInfo struct {
	rows map[*check.TypeParam]bool
	// sets are the capability sets a call binds a held parameter to, or an
	// InForce names, by key (capsKey), with their capabilities.
	sets map[string][]*check.Named
}

// heldEdge is a call of a generic function from a body: its callee and type
// arguments.
type heldEdge struct {
	callee *check.Func
	targs  []check.Type
}

// held is the program's held analysis, computed once.
func (l *lowerer) held() *heldInfo {
	if l.heldFacts != nil {
		return l.heldFacts
	}
	h := &heldInfo{rows: map[*check.TypeParam]bool{}, sets: map[string][]*check.Named{}}
	l.heldFacts = h
	var edges []heldEdge
	var forces [][]check.Type
	for _, p := range l.allPkgs() {
		for _, f := range l.canon.funcs(p) {
			if f.Decl == nil || f.Decl.Body == nil || f.Pkg == nil || f.Pkg.Info == nil {
				continue
			}
			info := f.Pkg.Info
			syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					if e, isExpr := n.(syntax.Expr); isExpr {
						if inst := info.Instances[e]; inst != nil && inst.Func != nil {
							edges = append(edges, heldEdge{inst.Func, inst.TypeArgs})
						}
					}
					return true
				}
				c := info.Calls[x]
				switch {
				case c == nil:
				case check.IsInForce(c):
					if len(c.TypeArgs) == 1 {
						forces = append(forces, rowArgElems(c.TypeArgs[0]))
					}
				case c.Kind == check.CallFunc && c.Func != nil && len(c.TypeArgs) > 0:
					edges = append(edges, heldEdge{c.Func, c.TypeArgs})
				}
				return true
			})
		}
	}
	mark := func(es []check.Type) bool {
		grew := false
		for _, e := range es {
			if tp, ok := e.(*check.TypeParam); ok && tp.Kind == check.KindUses && !h.rows[tp] {
				h.rows[tp] = true
				grew = true
			}
		}
		return grew
	}
	for _, es := range forces {
		mark(es)
	}
	for grew := true; grew; {
		grew = false
		for _, e := range edges {
			for i, tp := range e.callee.Sig.TParams {
				if h.rows[tp] && i < len(e.targs) {
					grew = mark(rowArgElems(e.targs[i])) || grew
				}
			}
		}
	}
	// The capability sets the program's Go arguments name.
	addSet := func(es []check.Type) {
		caps := capsOf(es)
		if len(caps) > 0 || len(es) == 0 {
			h.sets[l.capsKey(caps)] = caps
		}
	}
	for _, es := range forces {
		addSet(es)
	}
	for _, e := range edges {
		for i, tp := range e.callee.Sig.TParams {
			if h.rows[tp] && i < len(e.targs) {
				addSet(rowArgElems(e.targs[i]))
			}
		}
	}
	return h
}

// allPkgs are the program's package and the libraries it reaches.
func (l *lowerer) allPkgs() []*check.Package {
	return append([]*check.Package{l.pkg}, l.libraries()...)
}

// rowArgElems are the elements of a row argument, solved.
func rowArgElems(t check.Type) []check.Type {
	if ra, ok := t.(*check.RowArg); ok {
		return rowElems(ra.Row)
	}
	return nil
}

// capsOf are the capabilities among a row's elements.
func capsOf(es []check.Type) []*check.Named {
	var out []*check.Named
	for _, e := range es {
		if n, ok := e.(*check.Named); ok {
			out = append(out, n)
		}
	}
	return out
}

// capsKey is the key of a set of capabilities: their type keys, sorted
// and without repeats.
func (l *lowerer) capsKey(caps []*check.Named) string {
	var keys []string
	for _, c := range caps {
		keys = append(keys, l.typeKey(c))
	}
	sort.Strings(keys)
	return strings.Join(slices.Compact(keys), ",")
}

// holdsRows reports a program that holds a row: its environment carries
// envWith.
func (l *lowerer) holdsRows() bool { return len(l.held().rows) > 0 || len(l.held().sets) > 0 }

// isHeld reports a held row parameter.
func (l *lowerer) isHeld(tp *check.TypeParam) bool { return l.held().rows[tp] }

// reserveHeld names the copier interface, the join and each capability
// set's type, in key order (the naming pass).
func (l *lowerer) reserveHeld() {
	h := l.held()
	if !l.holdsRows() {
		return
	}
	l.pkgName(pkgKey{"env copier", nil}, "ergoEnvCopier")
	l.pkgName(pkgKey{"caps join", nil}, "ergoCapsJoin")
	for _, k := range slices.Sorted(maps.Keys(h.sets)) {
		l.pkgName(pkgKey{"caps", k}, "ergoCaps")
	}
}

// envCopier is the copier interface's Go name, emitted once with the join.
func (l *lowerer) envCopier() string {
	name := l.pkgName(pkgKey{"env copier", nil}, "ergoEnvCopier")
	if l.heldEmitted == nil {
		l.heldEmitted = map[string]bool{}
	}
	if !l.heldEmitted["copier"] {
		l.heldEmitted["copier"] = true
		env := l.env()
		join := l.pkgName(pkgKey{"caps join", nil}, "ergoCapsJoin")
		l.gen.WriteString("// " + name + " copies the fields of a held row from s to d (inforce.go).\n")
		l.gen.WriteString("type " + name + " interface{ copyEnv(d *" + env + ", s " + env + ") }\n\n")
		l.gen.WriteString("// " + join + " copies the fields of A's row, then B's.\n")
		l.gen.WriteString("type " + join + "[A, B " + name + "] struct{}\n\n")
		l.gen.WriteString("func (" + join + "[A, B]) copyEnv(d *" + env + ", s " + env + ") {\n\tvar a A\n\ta.copyEnv(d, s)\n\tvar b B\n\tb.copyEnv(d, s)\n}\n\n")
	}
	return name
}

// capsType is the Go name of the type that copies the fields of caps,
// emitted once.
func (l *lowerer) capsType(caps []*check.Named, site syntax.Node) string {
	key := l.capsKey(caps)
	name, ok := l.pkgNames[pkgKey{"caps", key}]
	if !ok {
		ice(site, "the capability set %q of a held row has no name from the naming pass", key)
	}
	l.envCopier()
	if !l.heldEmitted["caps "+key] {
		l.heldEmitted["caps "+key] = true
		var fields []string
		for _, c := range caps {
			fields = append(fields, l.envField(c))
		}
		sort.Strings(fields)
		fields = slices.Compact(fields)
		var b strings.Builder
		b.WriteString("type " + name + " struct{}\n\n")
		b.WriteString("func (" + name + ") copyEnv(d *" + l.env() + ", s " + l.env() + ") {\n")
		if len(fields) == 0 {
			b.WriteString("\t_, _ = d, s\n")
		}
		for _, f := range fields {
			b.WriteString("\td." + f + " = s." + f + "\n")
		}
		b.WriteString("}\n\n")
		l.gen.WriteString(b.String())
	}
	return name
}

// heldArg is the Go type argument of a held row parameter bound to the row
// argument a: the type that copies its fields.
func (tc *typeCtx) heldArg(a check.Type) string {
	es := rowArgElems(a)
	caps := capsOf(es)
	var parts []string
	if len(caps) > 0 {
		parts = append(parts, tc.l.capsType(caps, tc.site))
	}
	for _, e := range es {
		switch e := e.(type) {
		case *check.Named:
		case *check.TypeParam:
			name, ok := tc.params[e]
			if !ok || !tc.l.isHeld(e) {
				ice(tc.site, "the row parameter %s in a held row has no Go type parameter", e.Name)
			}
			parts = append(parts, name)
		default:
			ice(tc.site, "a held row's element %s is no capability and no row parameter", e)
		}
	}
	if len(parts) == 0 {
		return tc.l.capsType(nil, tc.site)
	}
	t := parts[0]
	join := tc.l.pkgName(pkgKey{"caps join", nil}, "ergoCapsJoin")
	for _, p := range parts[1:] {
		tc.l.envCopier()
		t = join + "[" + t + ", " + p + "]"
	}
	return t
}

// inForce lowers a call of InForce[R](): the Context[R] value that copies
// R's fields from a copy of the environment at the call, after checking
// that the innermost owning with in force there has not ended. The closure
// holds the copy by value and passes it to copyEnv by value, so no pointer
// to it is taken: Go then stores it in the closure, one allocation, when
// it is at most 128 bytes (the task, the with and up to seven
// capabilities), and in a variable of its own past that.
func (fc *funcCtx) inForce(x *syntax.CallExpr, c *check.Call) operand {
	if len(c.TypeArgs) != 1 {
		ice(x, "InForce with %d type arguments", len(c.TypeArgs))
	}
	copier := fc.heldArg(c.TypeArgs[0])
	snap := fc.temp("held")
	fc.line(snap + " := *" + fc.envExpr(x))
	return operand{val: fc.envClosure(callerEnv, "e", x, func(e string) ([]string, string, string) {
		ind := strings.Repeat("\t", fc.depth+1)
		r := fc.scope.alloc("r")
		var b strings.Builder
		b.WriteString(ind + fc.l.rt() + ".HeldLive(" + snap + "." + envWith + ")\n")
		b.WriteString(ind + "var " + r + " " + copier + "\n")
		b.WriteString(ind + r + ".copyEnv(" + e + ", " + snap + ")\n")
		b.WriteString(ind + "return struct{}{}, nil\n")
		return nil, fc.valueResults(nil), b.String()
	})}
}
