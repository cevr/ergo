package lower

// IR builder: a capability call with a known provider (IR-FORMS IRF-5).
//
// A capability's provider is a field of the environment, a Go interface,
// so `Meter.Add(x)` in a function that `needs Meter` is an indirect call
// Go cannot inline (the ctx probe: 33 instructions a call against hand
// Go's 9). Which provider the field holds is decided by the withs that
// reach the function: when every one of them binds the capability to an
// operations conversion of the same adapter (a provider of the program
// whose functions provide the operations: `with context { Meter:
// Plain{…} }`), the field always holds that adapter. The function then
// reads the field once at its entry as the adapter's pointer type (a type
// assertion that cannot fail), and each call of the capability's
// operation that reads the function's own binding is a call of the
// adapter's method: a static call Go inlines with the provider's
// function. Everything else is today's call through the interface.
//
// The fact is whole-program (the spec's open question 3), decided per
// lowering of a program's main package, from the checker's facts alone
// (no name, path or std test):
//
//   - a site is a direct call of a function of the package inside a
//     function body of the package; its binding of a capability X is that
//     of the innermost with around it that discharges X (Info.Withs:
//     Discharged and Entries), or, with none, its function's own (it
//     inherits);
//   - a binding is known when the with's entry for X converts its provider
//     by an operations conversion the lowering makes a plain adapter for
//     (providerOf, no witness); a with of a context value, or one that
//     builds an entry for X it does not discharge, is unknown;
//   - a function's binding of X joins those of its sites (its callers'
//     own when they inherit), to a fixed point; it is unknown when the
//     function escapes: main, a method (a Go method may satisfy an
//     interface out of sight), a Close, a function in a conversion's
//     table, a call that starts a task, or any use of it that is not the
//     callee of a recorded site (a function value, a call in a function
//     literal, a test, a law, a context's provider).
//
// Two providers for one capability, an unknown site or an escape keep
// the interface call. The reference is opLowering "dynamic" (by -X, or a
// test's setting): every call reads the interface. TestIROpCallsMatchSource
// holds the two equal on generated programs, and reads the one-provider
// form on the IR.
//
// Not as the spec's §3.5 wrote it: no worker with the provider as a typed
// parameter, and no "opcall" or "with" form kind. A worker is a Go function
// the text emitter declares (its head), and the text path is frozen
// (IR-ONLY); the entry read gives the same static call with no new
// function, no new signature and no change at the callers (so the package
// split and the other variants of a body are untouched). The decision is a
// whole-program fact computed before any body is built, so a form kept
// until a pass would carry nothing the site does not know (decided by the
// simpler, reversible option).
//
// Prior art: GHC specialises a function on a known dictionary
// (SPECIALISE, and the specialiser on a call with a known instance);
// Koka's evidence passing runs a tail-resumptive operation in place;
// Go's own PGO devirtualizes a hot interface call behind a type test, and
// whole-program devirtualization (LLVM's, MLton's) does it with no test
// once the set of types is closed. Gleam, Elixir and ReScript pass a
// record of functions or a module and call through it.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// opLowering selects the capability calls: "" takes the known provider's
// adapter where the program allows it; "dynamic" is the reference.
var opLowering string

// opKey is a function's binding of one capability.
type opKey struct {
	f   *check.Func
	cap *check.TypeName
}

// opBind is a binding: none yet (zero), one known adapter, or unknown.
type opBind struct {
	set, top bool
	key      string // the adapter's key (adapterName)
	conv     check.Conversion
	site     syntax.Node
}

// join is a joined with b.
func (a opBind) join(b opBind) opBind {
	switch {
	case !b.set:
		return a
	case !a.set:
		return b
	case a.top || b.top || a.key != b.key:
		return opBind{set: true, top: true}
	}
	return a
}

var opUnknown = opBind{set: true, top: true}

// opSite is a call recorded by the walk: of a function (callee) or of a
// capability's operation (cap), in owner's body, under the withs chain
// (outermost first).
type opSite struct {
	owner  *check.Func
	callee *check.Func
	cap    *check.TypeName
	chain  []*check.With
}

// opFacts are a lowering's capability bindings.
type opFacts struct {
	// ops are the operation calls that read their function's own
	// binding, by call.
	ops map[*syntax.CallExpr]*opSite
	// bind is each function's binding of each capability its own
	// operation calls read.
	bind map[opKey]opBind
	// refs are the aliases of the adapters' pointer types, by adapter key.
	refs map[string]string
	// work counts the fixed point's visits of a site (a growth guard
	// reads it).
	work int
}

// irOpBody is the function whose body a builder builds, its environment's
// local and the entry locals of the adapters read so far.
type irOpBody struct {
	f      *check.Func
	env    int
	locals map[*check.TypeName]int
}

// opFacts are st's lowering's bindings, computed once.
func (st *irState) opFacts(l *lowerer) *opFacts {
	if st.ops == nil {
		st.ops = l.capBindings()
	}
	return st.ops
}

// capBindings computes the bindings of l's package (none unless it is a
// program's main package).
func (l *lowerer) capBindings() *opFacts {
	facts := &opFacts{ops: map[*syntax.CallExpr]*opSite{}, bind: map[opKey]opBind{}}
	pkg := l.pkg
	if opLowering == "dynamic" || pkg == nil || pkg.Info == nil {
		return facts
	}
	info := pkg.Info
	program := false
	for _, f := range pkg.Funcs {
		if f.Name == "main" && f.Decl != nil && f.Decl.Recv == nil {
			program = true
		}
	}
	if !program {
		return facts
	}
	calls := map[*check.Func][]*opSite{}
	explained := map[*syntax.Ident]bool{}
	var ops []*opSite
	for _, f := range pkg.Funcs {
		if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil {
			continue
		}
		var walk func(n syntax.Node, chain []*check.With)
		walk = func(n syntax.Node, chain []*check.With) {
			syntax.Inspect(n, func(m syntax.Node) bool {
				switch x := m.(type) {
				case *syntax.FuncLit:
					// A literal runs against its caller's environment:
					// nothing in it is a site.
					return false
				case *syntax.CallExpr:
					c := info.Calls[x]
					if c == nil {
						return true
					}
					switch c.Kind {
					case check.CallWith:
						w := info.Withs[x]
						for _, a := range x.Args {
							walk(a, chain) // providers run outside the with
						}
						inner := append([]*check.With{}, chain...)
						for ; w != nil; w = w.Next {
							inner = append(inner, w)
						}
						if x.Trailing != nil {
							walk(x.Trailing, inner)
						}
						return false
					case check.CallFunc:
						if c.Func != nil && !c.StartsTask {
							if id := calleeIdent(x.Fun); id != nil {
								explained[id] = true
								calls[c.Func] = append(calls[c.Func], &opSite{owner: f, callee: c.Func, chain: chain})
							}
						}
					case check.CallProviderOp:
						if capT := capOf(info, x); capT != nil && c.Op != nil {
							ops = append(ops, &opSite{owner: f, cap: capT, chain: chain})
							facts.ops[x] = ops[len(ops)-1]
						}
					}
				}
				return true
			})
		}
		walk(f.Decl.Body, nil)
	}
	escapes := map[*check.Func]bool{}
	for id, obj := range info.Uses {
		if fn, ok := obj.(*check.Func); ok && !explained[id] {
			escapes[fn] = true
		}
	}
	table := func(c check.Conversion) {
		for _, fn := range c.Table {
			escapes[fn] = true
		}
	}
	for _, c := range info.Implicit {
		table(c)
	}
	for _, w := range info.Withs {
		for ; w != nil; w = w.Next {
			for _, e := range w.Entries {
				for _, c := range e.Conversions {
					table(c)
				}
			}
		}
	}
	for _, ctx := range pkg.Contexts {
		for _, e := range ctx.Entries {
			for _, c := range e.Conversions {
				table(c)
			}
		}
	}
	escaped := func(f *check.Func) bool {
		return escapes[f] || f.Pkg != pkg || f.Name == "main" || f.Decl == nil || f.Decl.Recv != nil || isClose(f) || l.funcDecl(f).method != nil
	}
	// The pairs a binding is needed for: each operation call's that
	// inherits, and, as the fixed point goes, each inheriting caller's.
	need := map[opKey]bool{}
	var order []opKey
	add := func(k opKey) {
		if !need[k] {
			need[k] = true
			order = append(order, k)
		}
	}
	for x, s := range facts.ops {
		if b, inherits := l.binding(s.chain, s.cap); inherits {
			add(opKey{s.owner, s.cap})
		} else {
			_ = b
			delete(facts.ops, x) // a with of its function binds it
		}
	}
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(order); i++ {
			k := order[i]
			b := facts.bind[k]
			if b.top {
				continue
			}
			nb := b
			if escaped(k.f) {
				nb = opUnknown
			} else {
				for _, s := range calls[k.f] {
					facts.work++
					sb, inherits := l.binding(s.chain, k.cap)
					if inherits {
						add(opKey{s.owner, k.cap})
						sb = facts.bind[opKey{s.owner, k.cap}]
					}
					nb = nb.join(sb)
				}
			}
			if nb.set != b.set || nb.top != b.top || nb.key != b.key {
				facts.bind[k] = nb
				changed = true
			}
		}
	}
	return facts
}

// binding is the binding of capT at a site under chain: the innermost
// with's that discharges it, or inherits when none does.
func (l *lowerer) binding(chain []*check.With, capT *check.TypeName) (opBind, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		w := chain[i]
		if w.Value != nil || w.Installs != nil {
			return opUnknown, false
		}
		for j, d := range w.Discharged {
			if n, ok := d.(*check.Named); ok && n.Obj == capT {
				if j >= len(w.Entries) {
					return opUnknown, false
				}
				return l.entryBinding(w.Entries[j], capT), false
			}
		}
		for _, a := range w.Acquired {
			for _, k := range a.Caps {
				if n, ok := a.Entry.Caps[k].(*check.Named); ok && n.Obj == capT {
					return opUnknown, false
				}
			}
		}
	}
	return opBind{}, true
}

// entryBinding is the binding the with entry e gives capT.
func (l *lowerer) entryBinding(e *check.ContextEntry, capT *check.TypeName) opBind {
	if e == nil || e.Provider == nil {
		return opUnknown
	}
	for i, t := range e.Caps {
		n, ok := t.(*check.Named)
		if !ok || n.Obj != capT || i >= len(e.Conversions) {
			continue
		}
		conv := e.Conversions[i]
		prov := l.providerOf(conv)
		if prov == nil {
			return opUnknown
		}
		for _, f := range conv.Table {
			if f == nil || len(witnessParams(f.Sig)) > 0 {
				return opUnknown
			}
		}
		return opBind{set: true, key: prov.key, conv: conv, site: e.Provider}
	}
	return opUnknown
}

// capOf is the capability a call of its operation names: Cap.Op(…).
func capOf(info *check.Info, x *syntax.CallExpr) *check.TypeName {
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		return nil
	}
	n, ok := info.Types[sel.X].(*check.Named)
	if !ok {
		return nil
	}
	return n.Obj
}

// knownOp is the receiver of the capability operation call x of named: the
// entry local holding the known adapter when x reads its function's own
// binding of named and that binding is one adapter, else recv (the
// environment's field).
func (b *irBuilder) knownOp(x *syntax.CallExpr, named *check.Named, recv *irExpr) *irExpr {
	if opLowering == "dynamic" || b.ops == nil || b.law != nil || b.fn == nil {
		return recv
	}
	facts := b.st.opFacts(b.l)
	s := facts.ops[x]
	if s == nil || s.owner != b.ops.f || s.cap != named.Obj {
		return recv
	}
	bind := facts.bind[opKey{s.owner, s.cap}]
	if !bind.set || bind.top {
		return recv
	}
	h, ok := b.ops.locals[named.Obj]
	if !ok {
		// The emitter declares the adapter, as for the with's own
		// conversion (opsConv).
		b.fc.operations(bind.conv, "_", bind.site)
		name := b.l.adapters[bind.key]
		if name == "" {
			return recv
		}
		// The reader asserts to a type name: an alias of the adapter's
		// pointer type, declared beside the IR bodies' imports.
		ref, ok := facts.refs[bind.key]
		if !ok {
			ref = b.l.names.alloc(name + "Ref")
			if facts.refs == nil {
				facts.refs = map[string]string{}
			}
			facts.refs[bind.key] = ref
			b.keepImport("type " + ref + " = *" + name)
		}
		p := b.fn.Pos
		h = b.temp("op"+named.Obj.Name, "*"+name, p)
		env := &irExpr{K: "local", Pos: p, I: b.ops.env}
		field := &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}
		b.entry = append(b.entry, &irStmt{K: "def", Pos: p, Local: h, X: &irExpr{K: "assert", Pos: p, T: ref, X: field}})
		if b.ops.locals == nil {
			b.ops.locals = map[*check.TypeName]int{}
		}
		b.ops.locals[named.Obj] = h
	}
	return &irExpr{K: "local", Pos: recv.Pos, I: h}
}
