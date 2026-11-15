package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Known providers (DT4). A value of a program's operation type is a Go
// interface, so each operation call on it is an indirect call Go cannot
// inline. When a call passes, for an operation-type parameter of its
// callee, a value an operations conversion builds at the call (the
// provider is known there: `PlayOn(&s, input)` with s a *screen.Screen),
// the call goes to a copy of the callee whose parameter has the
// conversion's adapter type, a Go pointer to a struct, in place of the
// interface. In the copy
//   - each operation call on the parameter is a call of the adapter's
//     method, which Go resolves statically and inlines with the
//     provider's function;
//   - a call that passes the parameter on, at a parameter of the same
//     operation type, goes to that function's copy for the same adapter.
//
// The copy differs from its function only in the Go type of that one
// parameter: its body is the function's, its polls, failures and
// defects its own. A parameter has copies only when every use of it in
// the body is the receiver of an operation call or an argument at a
// parameter of the same operation type, so the concrete Go type reaches
// no assignment, comparison, return or type inference; where it reaches
// a function with no copy, Go converts it to the interface at the call.
// A function has copies when it has an Ergo body, is no method, no
// extern, no Close and not main, and has no witness; the copies' Go
// names are reserved in the naming pass, for every such function and
// every operations conversion of the program to its parameter's type
// that an adapter lowers with no type parameter, no witness of its own
// and no script or sealed std source.
//
// The reference is providerLowering "dynamic", set by a test or by a
// build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.providerLowering=dynamic:
// every call takes the function itself. TestKnownProvidersAgree runs
// generated programs under both.
//
// Prior art: devirtualization of a known receiver in C++ (GCC's
// -fdevirtualize, LLVM's whole-program devirtualization) and in the JVM's
// JITs (class hierarchy analysis, inline caches); Go's own PGO
// devirtualizes a hot interface call behind a type test, and its inliner
// devirtualizes a call on a value whose concrete type it sees; MLton
// specializes every function on its argument's known representation;
// Rust monomorphizes a generic over its trait implementation. Gleam,
// Elixir and ReScript have no interfaces of their own: a record of
// functions or a protocol dispatch stays a call through the host's value.
var providerLowering string

// knownProv is a provider the lowering knows at a call: the operations
// conversion's adapter (adapterName) and the operation type it provides.
type knownProv struct {
	key, base string
	to        *check.TypeName
}

// providerOf is the known provider of the operations conversion c, or
// nil when c is not one a copy is made for: its adapter is the plain
// one of operations (calls.go), with no type parameter.
func (l *lowerer) providerOf(c check.Conversion) *knownProv {
	if providerLowering == "dynamic" || c.Kind != check.ConvOperations {
		return nil
	}
	to, ok := c.To.(*check.Named)
	if !ok || !l.provType(to) || check.ScriptOf(c.From) != nil || len(convParams(c)) > 0 {
		return nil
	}
	from, _, ok := opsSource(c.From)
	if !ok || l.runtimeType(from, nil) != "" || hostSourced(c.From) {
		return nil
	}
	key, base, ok := l.adapterName(c)
	if !ok {
		return nil
	}
	return &knownProv{key: key, base: base, to: to.Obj}
}

// provType reports an operation type of the program (a Go interface the
// lowering emits) with no type arguments.
func (l *lowerer) provType(n *check.Named) bool {
	return n.Obj.Pkg != nil && !n.Obj.Pkg.Host() && len(n.TArgs) == 0 && isOps(n) && l.runtimeType(n, nil) == ""
}

// provParam is the index of f's operation-type parameter a copy of f is
// made for, or -1 (memoized).
func (l *lowerer) provParam(f *check.Func) int {
	if k, ok := l.provParams[f]; ok {
		return k
	}
	if l.provParams == nil {
		l.provParams = map[*check.Func]int{}
	}
	k := l.findProvParam(f)
	l.provParams[f] = k
	return k
}

func (l *lowerer) findProvParam(f *check.Func) int {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" || isClose(f) {
		return -1
	}
	if len(witnessParams(f.Sig)) > 0 {
		return -1
	}
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		return -1
	}
	info := f.Pkg.Info
	index := map[*check.Var]int{}
	for i, p := range f.Sig.Params {
		if n, ok := p.Type.(*check.Named); ok && vars[i] != nil && l.provType(n) {
			index[vars[i]] = i
		}
	}
	if len(index) == 0 {
		return -1
	}
	param := func(x syntax.Expr) (*syntax.Ident, int) {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return nil, -1
		}
		v, ok := info.Uses[id].(*check.Var)
		if !ok {
			return nil, -1
		}
		if i, ok := index[v]; ok {
			return id, i
		}
		return nil, -1
	}
	// allowed are the uses that keep the parameter's Go type: an
	// operation call's receiver, an argument at the same operation type.
	allowed := map[*syntax.Ident]bool{}
	used := map[int]bool{}
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		c := info.Calls[x]
		if c == nil {
			return true
		}
		switch c.Kind {
		case check.CallOp:
			if id, i := param(c.Recv); id != nil {
				allowed[id], used[i] = true, true
			}
		case check.CallFunc:
			if c.Func == nil || c.Recv != nil {
				return true
			}
			for j, a := range x.Args {
				id, i := param(a)
				if id == nil || j >= len(c.Func.Sig.Params) {
					continue
				}
				if n, ok := c.Func.Sig.Params[j].Type.(*check.Named); ok && n.Obj == f.Sig.Params[i].Type.(*check.Named).Obj && len(n.TArgs) == 0 {
					allowed[id], used[i] = true, true
				}
			}
		}
		return true
	})
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && !allowed[id] {
			if v, ok := info.Uses[id].(*check.Var); ok {
				if i, ok := index[v]; ok {
					used[i] = false
					delete(index, v)
				}
			}
		}
		return true
	})
	found := -1
	for i := range f.Sig.Params {
		if used[i] {
			found = i
			break
		}
	}
	return found
}

// provFuncs are the functions of pkgs with a copy for a provider of each
// operation type, in the canonical order.
func (l *lowerer) provFuncs(pkgs []*check.Package) map[*check.TypeName][]*check.Func {
	out := map[*check.TypeName][]*check.Func{}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if k := l.provParam(f); k >= 0 {
				to := f.Sig.Params[k].Type.(*check.Named).Obj
				out[to] = append(out[to], f)
			}
		}
	}
	return out
}

// provSpecKey is the key of f's copy for prov.
func provSpecKey(f *check.Func, prov *knownProv) pkgKey {
	return pkgKey{"spec", specKey{f: f, prov: prov.key}}
}

// provSpecBase is the base Go name of f's copy for prov: f's name and
// the adapter's, opsScreenAsScreen giving PlayOnScreenAsScreen.
func provSpecBase(f *check.Func, prov *knownProv) string {
	return f.Name + strings.TrimPrefix(prov.base, "ops")
}

// knownProvider is the provider x gives, when the lowering knows it: an
// operations conversion of x's value made here, or a parameter of the
// copy being written. Else nil.
func (fc *funcCtx) knownProvider(x syntax.Expr) *knownProv {
	if x == nil {
		return nil
	}
	if id, ok := unparen(x).(*syntax.Ident); ok && fc.provs != nil {
		if v, ok := fc.l.info.Uses[id].(*check.Var); ok && fc.provs[v] != nil {
			return fc.provs[v]
		}
	}
	conv, ok := fc.l.info.Implicit[x]
	if !ok {
		return nil
	}
	return fc.l.providerOf(conv)
}
