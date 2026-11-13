package eval

import (
	"go/constant"
	"unsafe"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// facts are the checker's facts about one expression that every
// evaluation of it reads (ORACLE O29). The checker keeps each kind of
// fact in a map of its own, keyed by the expression, so evaluating a node
// looked it up in up to eight maps, most of them misses, each hashing an
// interface key; that was a fifth of the evaluator's instructions per
// step. The first evaluation of a node gathers them here, and every later
// one reads them from one lookup. The facts are the checker's, unchanged:
// nothing here decides anything the maps did not.
type facts struct {
	typ      check.Type
	val      constant.Value
	hasVal   bool
	ctx      *check.With
	conv     check.Conversion
	hasConv  bool
	elems    []check.Conversion
	covered  bool
	owned    *check.Drop
	bare     *check.TypeName // the record a bare name or pkg.Name stands for
	use      check.Object    // an identifier's
	call     *check.Call     // a call's
	instance *check.Instance
}

// factTable holds the facts of one package's expressions, by node: the
// pointer an expression's interface holds (nodeKey).
type factTable map[unsafe.Pointer]*facts

// nodeKey is the pointer x holds. Every syntax node is a pointer type
// (its Pos method has a pointer receiver), so the pointer names the node,
// and a map keyed by it hashes one word, not an interface.
func nodeKey(x syntax.Node) unsafe.Pointer {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&x))[1]
}

// fx is the facts of x, an expression of fr's package. A frame keeps its
// package's table once it has asked.
func (in *interp) fx(fr *frame, x syntax.Expr) *facts {
	if fr.tab == nil {
		fr.tab = in.table(fr.info)
	}
	return fr.tab.of(fr.info, x)
}

// table is the fact table of info's package.
func (in *interp) table(info *check.Info) factTable {
	if in.factInfo != info {
		if in.factTables == nil {
			in.factTables = map[*check.Info]factTable{}
		}
		t := in.factTables[info]
		if t == nil {
			t = factTable{}
			in.factTables[info] = t
		}
		in.factInfo, in.facts = info, t
	}
	return in.facts
}

// of is the facts of x, an expression of info's package, gathered at its
// first use.
func (t factTable) of(info *check.Info, x syntax.Expr) *facts {
	k := nodeKey(x)
	if f := t[k]; f != nil {
		return f
	}
	f := &facts{typ: info.Types[x], ctx: info.ContextValues[x], elems: info.ImplicitElems[x], covered: info.TryCovers[x] != nil, owned: info.OwnedTemps[x], instance: info.Instances[x]}
	f.val, f.hasVal = info.Values[x]
	f.conv, f.hasConv = info.Implicit[x]
	switch h := x.(type) {
	case *syntax.Ident:
		f.use = info.Uses[h]
		f.bare, _ = f.use.(*check.TypeName)
	case *syntax.SelectorExpr:
		if id, ok := unparen(h.X).(*syntax.Ident); ok {
			pn, ok := info.Uses[id].(*check.PkgName)
			if named, isNamed := f.typ.(*check.Named); ok && isNamed && named.Obj.Pkg == pn.Pkg && named.Obj.Name == h.Sel.Name {
				f.bare = named.Obj
			}
		}
	case *syntax.CallExpr:
		f.call = info.Calls[h]
	}
	t[k] = f
	return f
}

// paramsOf is the parameters of fn's declaration, in order, a nil for an
// unnamed one, read from its package's Defs once.
func (in *interp) paramsOf(fn *check.Func) []*check.Var {
	if ps, ok := in.params[fn]; ok {
		return ps
	}
	var ps []*check.Var
	for _, field := range fn.Decl.Type.Params.List {
		if len(field.Names) == 0 {
			ps = append(ps, nil)
		}
		for _, id := range field.Names {
			v, _ := fn.Pkg.Info.Defs[id].(*check.Var)
			ps = append(ps, v)
		}
	}
	if in.params == nil {
		in.params = map[*check.Func][]*check.Var{}
	}
	in.params[fn] = ps
	return ps
}

// ownerOf is pkg.OwnerOf(decl), read once per declaration.
func (in *interp) ownerOf(pkg *check.Package, decl syntax.Decl) check.FactOwner {
	if decl == nil {
		return pkg.OwnerOf(decl)
	}
	k := nodeKey(decl)
	if o, ok := in.owners[k]; ok {
		return o
	}
	o := pkg.OwnerOf(decl)
	if in.owners == nil {
		in.owners = map[unsafe.Pointer]check.FactOwner{}
	}
	in.owners[k] = o
	return o
}
