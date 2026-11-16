package lower

// IR builder: the witnesses a call passes (F7 lane B). See ir_call.go.
//
// A call of a function whose type parameter a method set (or the codec
// constraint) constrains passes, after the environment, one witness per
// such parameter (witness.go; a codec's in the codec lowering): a struct of function fields the
// emitter writes as a literal at the call. A witness whose type arguments
// name no type parameter of the caller holds only closures that take
// their environment as a parameter (envClosure, callerEnv) and package
// functions, so it is a constant: the IR passes it as a package variable,
// `var ergoWitness = <the emitter's literal>`, written once, and the call
// reads it ("global"). The naming pass reserves one name per witness of
// each instance of the root package and of every library it reaches
// (reserveWitnessValues), in package then source order, only in an IR
// lowering.
//
// A call whose witness is built from the caller's own (a generic caller
// passing its type parameter on) keeps the gate "call that passes the
// caller's witness". The callee side, a body with witness parameters,
// keeps "witness parameter".
//
// Prior art: Swift emits a protocol witness table for each concrete
// conformance once, as a global constant, and passes its address; GHC
// floats a dictionary for a known instance to the top level (a CAF), so a
// call passes a pointer to it; Go's own GC-shape stenciling reads a
// per-instantiation dictionary that the linker lays out statically.

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// witnessSite is the i-th witness that the use x of a generic function
// passes.
type witnessSite struct {
	x syntax.Expr
	i int
}

// reserveWitnessValues names the package variable of every witness an
// instance of the root package and its libraries passes, in package then
// source order.
func (l *lowerer) reserveWitnessValues() {
	type site struct {
		x      syntax.Expr
		n      int
		path   string
		line   int
		col    int
		pkgIdx int
	}
	var sites []site
	for pi, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
		if p.Info == nil {
			continue
		}
		for x, inst := range p.Info.Instances {
			if inst == nil || inst.Func == nil {
				continue
			}
			n := len(witnessParams(inst.Func.Sig))
			if n == 0 {
				continue
			}
			path, line, col := l.locate(x, x.Pos())
			sites = append(sites, site{x: x, n: n, path: path, line: line, col: col, pkgIdx: pi})
		}
	}
	slices.SortFunc(sites, func(a, b site) int {
		switch {
		case a.pkgIdx != b.pkgIdx:
			return a.pkgIdx - b.pkgIdx
		case a.path != b.path:
			if a.path < b.path {
				return -1
			}
			return 1
		case a.line != b.line:
			return a.line - b.line
		case a.col != b.col:
			return a.col - b.col
		}
		// A call and its callee start at one place: the longer first.
		return int(b.x.End()) - int(a.x.End())
	})
	for _, s := range sites {
		for i := 0; i < s.n; i++ {
			l.pkgName(pkgKey{"witness value", witnessSite{s.x, i}}, "ergoWitness")
		}
	}
}

// witnessArgs are the witnesses the call x of f passes, as package
// variables, in f's witness parameter order; nil when f takes none.
func (b *irBuilder) witnessArgs(f *check.Func, x *syntax.CallExpr, p irPos) []*irExpr {
	params := witnessParams(f.Sig)
	if len(params) == 0 {
		return nil
	}
	inst := b.l.info.Instances[x]
	if inst == nil || inst.Func != f {
		irNo("call that passes witnesses without its instance")
	}
	for _, t := range inst.TypeArgs {
		if len(freeTypeParams(t)) > 0 {
			irNo("call that passes the caller's witness")
		}
	}
	texts := b.fc.witnessArgs(f, inst, x)
	out := make([]*irExpr, len(texts))
	for i, text := range texts {
		key := pkgKey{"witness value", witnessSite{x, i}}
		name, ok := b.l.pkgNames[key]
		if !ok {
			irNo("witness without its reserved name")
		}
		if b.st.census == nil && !b.l.texts[key] {
			b.l.texts[key] = true
			b.l.gen.WriteString("var " + name + " = " + text + "\n\n")
		}
		out[i] = &irExpr{K: "global", Pos: p, Name: name}
	}
	return out
}
