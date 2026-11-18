package check

import (
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The normal-form shape (types.md, Maps 5; CONSTRAINTS C5b). A Hashing
// provider has it when its Equal(a, b) is n(a) == n(b) and its Hash(k) is
// hash(n(k)) for one pure, rowless expression n of the key alone: the same
// expression, up to the name of its parameter, in both bodies, reading
// neither the provider's value nor anything but the key. Then a Map with
// that provider is the prelude's keyTable, Go's own map keyed by n's
// result, each entry holding the key as first put; every other provider
// keeps hashTable. The shape is read from the two bodies, so the hash and
// the equality agree by construction, and no law verdict is needed to
// apply it. It is the same rule for every provider and reads no
// provider's name.
//
// The checker writes the normal form as a function of the provider's
// package, `func NormalKey(_ P, k K) N { n(k) }`, derived from the type
// (FuncDecl.Derived, as a refinement's parse function is), and the type
// implements the prelude's normalKeyed[K, N] with it: the package is
// checked again, as for its Map sites (mapprovider.go). A program cannot
// write a derived function, so no other provider implements normalKeyed.

// mapLowering selects the reference lowering of Maps with a provider:
// "table" makes every such Map a hashTable, a provider with a normal form
// included (-X ergo.local/prototype/internal/sketch/check.mapLowering=table,
// or SetMapLowering in a test). The default lowers by the shape.
var mapLowering = ""

// SetMapLowering selects the Map lowering for the checks that follow, and
// returns the function that restores the one before.
func SetMapLowering(v string) (restore func()) {
	old := mapLowering
	mapLowering = v
	return func() { mapLowering = old }
}

// normalKeyName is the operation of normalKeyed and the name of the
// derived function that implements it.
const normalKeyName = "NormalKey"

// normalFormOf is the normal form type N of the provider h of keys k: the
// N of the normalKeyed[k, N] that h implements, or nil.
func (c *checker) normalFormOf(h, k Type) Type {
	n, ok := h.(*Named)
	if !ok || mapLowering == "table" {
		return nil
	}
	nk := c.preludeTypeObj("normalKeyed")
	if nk == nil {
		return nil
	}
	for _, o := range c.declaredOps(n) {
		if o.origin() == nk.Type.(*Named) && len(o.TArgs) == 2 && c.identical(o.TArgs[0], k) {
			return o.TArgs[1]
		}
	}
	return nil
}

// normalKeyDecl is the derived normal-form function of the type d, or nil.
func (c *checker) normalKeyDecl(d *syntax.TypeDecl) *syntax.FuncDecl {
	for _, f := range c.files {
		for _, decl := range f.syntax.Decls {
			if fd, ok := decl.(*syntax.FuncDecl); ok && fd.Derived == d && fd.Name.Name == normalKeyName {
				return fd
			}
		}
	}
	return nil
}

// shapeClause adds normalKeyed[K, N] to n's declared operation types,
// when the checker derived its normal form fd: K is the key of the
// Hashing[K] n implements, N fd's result.
func (c *checker) shapeClause(n *Named, d *syntax.TypeDecl, ctx *typeCtx) {
	fd := c.normalKeyDecl(d)
	if fd == nil || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
		return
	}
	hashing, nk := c.hashingOf(), c.preludeTypeObj("normalKeyed")
	if hashing == nil || nk == nil {
		return
	}
	for i, op := range n.impls {
		if op.origin() != hashing.Type.(*Named) || len(op.TArgs) != 1 {
			continue
		}
		res := c.typExpr(fd.Type.Results.List[0].Type, ctx)
		if isInvalid(res) {
			return
		}
		n.impls = append(n.impls, c.instantiate(nk.Type.(*Named), []Type{op.TArgs[0], res}))
		n.implExprs = append(n.implExprs, n.implExprs[i])
		return
	}
}

// writeShapes derives the normal-form function of each provider of the
// package that has the shape, beside its Hash, and reports whether it
// wrote one.
func (c *checker) writeShapes() bool {
	hashing := c.hashingOf()
	if hashing == nil || c.pkg.Path == stdlib.PreludePath {
		return false
	}
	wrote := false
	for _, fi := range c.files {
		for _, decl := range fi.syntax.Decls {
			td, ok := decl.(*syntax.TypeDecl)
			if !ok || td.TypeParams != nil || td.Name == nil {
				continue
			}
			tn, _ := c.pkg.Scope.names[td.Name.Name].(*TypeName)
			if tn == nil || tn.Decl != td {
				continue
			}
			named, _ := tn.Type.(*Named)
			if named == nil || c.normalKeyDecl(td) != nil {
				continue
			}
			for _, op := range named.impls {
				if op.origin() != hashing.Type.(*Named) || len(op.TArgs) != 1 {
					continue
				}
				if fd, file := c.shapeOf(named, td); fd != nil {
					file.Decls = append(file.Decls, fd)
					wrote = true
				}
			}
		}
	}
	return wrote
}

// shapeOf is the derived normal-form function of the provider n, declared
// by td, and the file that holds its Hash; nil when n's Hash and Equal do
// not have the shape.
func (c *checker) shapeOf(n *Named, td *syntax.TypeDecl) (*syntax.FuncDecl, *syntax.File) {
	if _, own := c.methodOf(n, normalKeyName); own != nil {
		return nil, nil // the package's own function of that name stands
	}
	_, hf := c.methodOf(n, "Hash")
	_, ef := c.methodOf(n, "Equal")
	if hf == nil || ef == nil || hf.Decl == nil || ef.Decl == nil || hf.Pkg != c.pkg || ef.Pkg != c.pkg ||
		hf.Decl.Derived != nil || ef.Decl.Derived != nil {
		return nil, nil
	}
	for _, f := range []*Func{hf, ef} {
		if fp, ok := c.pkg.Info.Footprints[f.Decl]; !ok || !fp.Empty() {
			return nil, nil // n must be pure and rowless
		}
	}
	hp, ep := c.paramVars(hf.Decl), c.paramVars(ef.Decl)
	if len(hp) != 2 || len(ep) != 3 || hp[1] == nil || ep[1] == nil || ep[2] == nil {
		return nil, nil
	}
	he, ee := soleExpr(hf.Decl), soleExpr(ef.Decl)
	call, ok := unparen(he).(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, nil
	}
	if cl := c.pkg.Info.Calls[call]; cl == nil || cl.Kind != CallHash {
		return nil, nil
	}
	bin, ok := unparen(ee).(*syntax.BinaryExpr)
	if !ok || bin.Op != syntax.EQL {
		return nil, nil
	}
	// The receivers and the other parameter are not n's: n reads its
	// key alone.
	outside := map[Object]bool{}
	for _, v := range append(hp, ep...) {
		if v != nil {
			outside[v] = true
		}
	}
	ex, ey, hx := unparen(bin.X), unparen(bin.Y), unparen(call.Args[0])
	if !c.sameExpr(ex, ey, map[Object]Object{ep[1]: ep[2]}, outside) ||
		!c.sameExpr(ex, hx, map[Object]Object{ep[1]: hp[1]}, outside) {
		return nil, nil
	}
	name := c.spelling(c.pkg.Info.Types[bin.X])
	if name == "" {
		return nil, nil
	}
	var file *syntax.File
	for _, fi := range c.files {
		for _, d := range fi.syntax.Decls {
			if d == syntax.Decl(hf.Decl) {
				file = fi.syntax
			}
		}
	}
	if file == nil {
		return nil, nil
	}
	pos := hf.Decl.Name.NamePos
	fd := &syntax.FuncDecl{
		Func: hf.Decl.Func,
		Name: &syntax.Ident{NamePos: pos, Name: normalKeyName},
		Type: &syntax.FuncType{
			Func:    hf.Decl.Type.Func,
			Params:  cloneSyntax(hf.Decl.Type.Params),
			Results: &syntax.FieldList{List: []*syntax.Field{{Type: &syntax.Ident{NamePos: pos, Name: name}}}},
		},
		Body: &syntax.BlockStmt{
			Lbrace: hf.Decl.Body.Lbrace,
			List:   []syntax.Stmt{&syntax.ExprStmt{X: cloneSyntax(call.Args[0])}},
			Rbrace: hf.Decl.Body.Rbrace,
		},
		Derived: td,
	}
	return fd, file
}

// spelling is the name the package writes the comparable type t with:
// a basic type, or a type of the package with no type arguments; "" for
// any other, which keeps its provider on the table.
func (c *checker) spelling(t Type) string {
	if t == nil || !comparable(t) {
		return ""
	}
	switch t := t.(type) {
	case *Basic:
		if int(t.Kind) < len(Typ) && Typ[t.Kind] == t && !strings.HasPrefix(t.Name, "untyped") {
			if o, _ := c.universe.lookup(t.Name); o != nil {
				if tn, ok := o.(*TypeName); ok && tn.Type == t && c.pkg.Scope.names[t.Name] == nil {
					return t.Name
				}
			}
		}
	case *Named:
		if t.Obj != nil && t.Obj.Pkg == c.pkg && len(t.TArgs) == 0 {
			if tn, ok := c.pkg.Scope.names[t.Obj.Name].(*TypeName); ok && tn == t.Obj {
				return t.Obj.Name
			}
		}
	}
	return ""
}

// paramVars are the variables of d's parameters, in order, nil for an
// unnamed or `_` one.
func (c *checker) paramVars(d *syntax.FuncDecl) []Object {
	var out []Object
	if d.Type == nil || d.Type.Params == nil {
		return nil
	}
	for _, f := range d.Type.Params.List {
		if len(f.Names) == 0 {
			out = append(out, nil)
		}
		for _, id := range f.Names {
			o := c.pkg.Info.Defs[id]
			if id.Name == "_" {
				o = nil
			}
			out = append(out, o)
		}
	}
	return out
}

// soleExpr is the one expression d's body is, or nil.
func soleExpr(d *syntax.FuncDecl) syntax.Expr {
	if d.Body == nil || len(d.Body.List) != 1 {
		return nil
	}
	switch s := d.Body.List[0].(type) {
	case *syntax.ExprStmt:
		return s.X
	case *syntax.ReturnStmt:
		if len(s.Results) == 1 {
			return s.Results[0]
		}
	}
	return nil
}

// sameExpr reports whether y is x with each variable of sub replaced by
// its image: the same syntax, every other name the same object and none
// of outside. A function literal is never the same: n is an expression of
// the key alone.
func (c *checker) sameExpr(x, y syntax.Node, sub map[Object]Object, outside map[Object]bool) bool {
	return c.sameValue(reflect.ValueOf(x), reflect.ValueOf(y), sub, outside)
}

var posType = reflect.TypeOf(syntax.Pos(0))

func (c *checker) sameValue(x, y reflect.Value, sub map[Object]Object, outside map[Object]bool) bool {
	if x.Kind() != y.Kind() || x.Type() != y.Type() {
		return false
	}
	switch x.Kind() {
	case reflect.Interface, reflect.Pointer:
		if x.IsNil() || y.IsNil() {
			return x.IsNil() == y.IsNil()
		}
		if x.Kind() == reflect.Pointer {
			switch xn := x.Interface().(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.Ident:
				yn := y.Interface().(*syntax.Ident)
				ox, oy := c.pkg.Info.Uses[xn], c.pkg.Info.Uses[yn]
				if img, ok := sub[ox]; ok {
					return img == oy
				}
				return xn.Name == yn.Name && ox == oy && (ox == nil || !outside[ox])
			}
		}
		return c.sameValue(x.Elem(), y.Elem(), sub, outside)
	case reflect.Struct:
		for i := 0; i < x.NumField(); i++ {
			if x.Type().Field(i).Type == posType {
				continue
			}
			if !c.sameValue(x.Field(i), y.Field(i), sub, outside) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if x.Len() != y.Len() {
			return false
		}
		for i := 0; i < x.Len(); i++ {
			if !c.sameValue(x.Index(i), y.Index(i), sub, outside) {
				return false
			}
		}
		return true
	case reflect.String:
		return x.String() == y.String()
	case reflect.Bool:
		return x.Bool() == y.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return x.Int() == y.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return x.Uint() == y.Uint()
	}
	return false
}

// cloneSyntax is a deep copy of the syntax x: fresh nodes with the same
// positions, so the derived function's nodes are its own.
func cloneSyntax[T any](x T) T {
	return cloneValue(reflect.ValueOf(x)).Interface().(T)
}

func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneValue(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneValue(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneValue(v.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i)))
		}
		return out
	}
	return v
}
