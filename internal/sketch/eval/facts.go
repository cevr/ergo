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
	cval     Value // val as a Value, once made outside a generic body
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
	pattern  *check.Pattern
	def      check.Object    // an identifier's Defs entry
	releases []check.Release // what the node releases when it ends
	match    *check.Match
	// op is the expression's form; plain says its value goes out as it is
	// made: no context value, no covered failure, no owned temporary, no
	// conversion (ORACLE O33).
	op    exprOp
	plain bool
	// kids are the facts of the operands the evaluator reads through kid,
	// by position, filled as they are first read (ORACLE O33).
	kids []*facts
	// field is a selector's record field: t is the record type its
	// operand was checked to have outside a generic body, and at is the
	// field's index in the record type rt last read (ORACLE O33).
	field struct {
		t, rt *check.Named
		at    int
	}
}

// fieldOf is the index of the field name in t, a record type, cached for
// the selector whose facts f are.
func (f *facts) fieldOf(t *check.Named, name string) int {
	if f.field.rt != t {
		f.field.rt, f.field.at = t, fieldIndex(t, name)
	}
	return f.field.at
}

// factTable holds the facts of one package's expressions, by node: the
// pointer an expression's interface holds (nodeKey).
type factTable = *nodeTable

// nodeTable is an open-addressed table from a node's pointer to its facts
// (ORACLE O32): one multiply to hash, keys and values in two flat arrays,
// a linear probe. The Go map it replaces spent a seventh of the
// evaluator's instructions per step.
type nodeTable struct {
	keys  []unsafe.Pointer
	vals  []*facts
	n     int
	shift uint
}

func newNodeTable() *nodeTable {
	return &nodeTable{keys: make([]unsafe.Pointer, 1024), vals: make([]*facts, 1024), shift: 64 - 10}
}

func (t *nodeTable) slot(k unsafe.Pointer) int {
	return int((uint64(uintptr(k)) >> 3) * 0x9E3779B97F4A7C15 >> t.shift)
}

// get is k's facts, nil when none are gathered.
func (t *nodeTable) get(k unsafe.Pointer) *facts {
	mask := len(t.keys) - 1
	for i := t.slot(k); ; i = (i + 1) & mask {
		switch t.keys[i] {
		case k:
			return t.vals[i]
		case nil:
			return nil
		}
	}
}

// put sets k's facts; k has none yet.
func (t *nodeTable) put(k unsafe.Pointer, f *facts) {
	if 2*(t.n+1) > len(t.keys) {
		old, oldVals := t.keys, t.vals
		t.keys, t.vals, t.n, t.shift = make([]unsafe.Pointer, 2*len(old)), make([]*facts, 2*len(old)), 0, t.shift-1
		for i, ok := range old {
			if ok != nil {
				t.put(ok, oldVals[i])
			}
		}
	}
	mask := len(t.keys) - 1
	i := t.slot(k)
	for t.keys[i] != nil {
		i = (i + 1) & mask
	}
	t.keys[i], t.vals[i] = k, f
	t.n++
}

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
			t = newNodeTable()
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
	if f := t.get(k); f != nil {
		return f
	}
	f := &facts{typ: info.Types[x], ctx: info.ContextValues[x], elems: info.ImplicitElems[x], covered: info.TryCovers[x] != nil, owned: info.OwnedTemps[x], instance: info.Instances[x], pattern: info.Patterns[x]}
	f.val, f.hasVal = info.Values[x]
	f.conv, f.hasConv = info.Implicit[x]
	switch h := x.(type) {
	case *syntax.Ident:
		f.use = info.Uses[h]
		f.def = info.Defs[h]
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
	f.releases, f.match = info.Releases[x], info.Matches[x]
	f.op = opOf(x, f)
	f.plain = f.ctx == nil && !f.covered && f.owned == nil && len(f.elems) == 0 &&
		(!f.hasConv || f.conv.Kind == check.ConvVariant || f.conv.Kind == check.ConvRow)
	t.put(k, f)
	return f
}

// kid is the facts of e, the operand at position i of the expression or
// statement whose facts are f: read through f once its first read found
// them, with no lookup (ORACLE O33). An operand belongs to its parent's
// package, so the parent's table holds it.
func (in *interp) kid(fr *frame, f *facts, i int, e syntax.Expr) *facts {
	if i < len(f.kids) {
		if k := f.kids[i]; k != nil {
			return k
		}
	} else {
		kids := make([]*facts, i+1)
		copy(kids, f.kids)
		f.kids = kids
	}
	k := in.fx(fr, e)
	f.kids[i] = k
	return k
}

// An exprOp is the form of an expression, decided once from its facts and
// its syntax (ORACLE O33): exprFacts switches on it, a jump, where a type
// switch over the syntax compared one type after another.
type exprOp uint8

const (
	opOther exprOp = iota
	opConst
	opBare
	opCtx
	opParen
	opIdent
	opBasicLit
	opPlace // a selector or an index
	opSlice
	opBinary
	opUnary
	opComposite
	opCall
	opTry
	opCatch
	opStmt // an if, a switch or a match in a value position
	opInterpolated
	opTuple
	opFuncLit
	opStar
)

func opOf(x syntax.Expr, f *facts) exprOp {
	switch {
	case f.hasVal:
		return opConst
	case f.bare != nil:
		return opBare
	case f.ctx != nil:
		return opCtx
	}
	switch x.(type) {
	case *syntax.ParenExpr:
		return opParen
	case *syntax.Ident:
		return opIdent
	case *syntax.BasicLit:
		return opBasicLit
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		return opPlace
	case *syntax.SliceExpr:
		return opSlice
	case *syntax.BinaryExpr:
		return opBinary
	case *syntax.UnaryExpr:
		return opUnary
	case *syntax.CompositeLit:
		return opComposite
	case *syntax.CallExpr:
		return opCall
	case *syntax.TryExpr:
		return opTry
	case *syntax.CatchExpr:
		return opCatch
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return opStmt
	case *syntax.InterpolatedString:
		return opInterpolated
	case *syntax.TupleExpr:
		return opTuple
	case *syntax.FuncLit:
		return opFuncLit
	case *syntax.StarExpr:
		return opStar
	}
	return opOther
}

// stmtOf is the facts of s, a statement of info's package: what it
// releases and the match it is.
func (t factTable) stmtOf(info *check.Info, s syntax.Node) *facts {
	if x, ok := s.(syntax.Expr); ok {
		return t.of(info, x)
	}
	k := nodeKey(s)
	if f := t.get(k); f != nil {
		return f
	}
	f := &facts{releases: info.Releases[s], match: info.Matches[s]}
	t.put(k, f)
	return f
}

// sx is the facts of s, a statement or other node of fr's package that is
// no expression.
func (in *interp) sx(fr *frame, s syntax.Node) *facts {
	if fr.tab == nil {
		fr.tab = in.table(fr.info)
	}
	return fr.tab.stmtOf(fr.info, s)
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
