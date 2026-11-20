package check

import (
	"reflect"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The normal-form shape (types.md, Maps 6; CONSTRAINTS C5b, C5d). Every
// Hashing provider has the operations NormalForm() bool and NormalKey(k K)
// K: a Map with a provider stores its keys in Go's own map keyed by
// NormalKey(k) when NormalForm() is true, and in an open-addressing table
// that calls Hash and Equal when it is false (the prelude's hashTable).
// A provider may write both; when it writes neither, the checker writes
// them beside its Hash, derived from the type (FuncDecl.Derived, as a
// refinement's parse function is), and checks the package again, as for
// its Map sites (mapprovider.go):
//
//   - when its Equal(a, b) is n(a) == n(b) and its Hash(k) is hash(n(k))
//     for one pure, rowless expression n of the key alone, of the key's
//     own type (the same expression, up to the name of its parameter, in
//     both bodies, reading neither the provider's value nor anything but
//     the key): NormalForm true and NormalKey n(k);
//   - else NormalForm false and NormalKey k.
//
// The shape is read from the two bodies, so the hash, the equality and the
// normal form agree by construction; Hashing's laws check a written one.
// It is the same rule for every provider and reads no provider's name.

// mapLowering selects the reference lowering of Maps with a provider:
// "table" makes every derived NormalForm false, so every such Map is the
// table (-X ergo.local/prototype/internal/sketch/check.mapLowering=table,
// or SetMapLowering in a test). A provider's written NormalForm stands.
// The default derives it by the shape.
var mapLowering = ""

// SetMapLowering selects the Map lowering for the checks that follow, and
// returns the function that restores the one before.
func SetMapLowering(v string) (restore func()) {
	old := mapLowering
	mapLowering = v
	return func() { mapLowering = old }
}

// The operations of Hashing that the checker derives.
const (
	normalFormName = "NormalForm"
	normalKeyName  = "NormalKey"
)

// derivedNormal reports whether the checker derived d's normal form.
func (c *checker) derivedNormal(d *syntax.TypeDecl) bool {
	for _, f := range c.files {
		for _, decl := range f.syntax.Decls {
			if fd, ok := decl.(*syntax.FuncDecl); ok && fd.Derived == d && fd.Name.Name == normalFormName {
				return true
			}
		}
	}
	return false
}

// writeShapes derives NormalForm and NormalKey for each provider of the
// package that writes neither, beside its Hash, and reports whether it
// wrote any.
func (c *checker) writeShapes() bool {
	hashing := c.hashingOf()
	if hashing == nil || c.pkg.Path == stdlib.PreludePath {
		return false
	}
	wrote := false
	for _, fi := range c.files {
		for _, decl := range fi.syntax.Decls {
			td, ok := decl.(*syntax.TypeDecl)
			if !ok || td.Name == nil {
				continue
			}
			tn, _ := c.pkg.Scope.names[td.Name.Name].(*TypeName)
			if tn == nil || tn.Decl != td {
				continue
			}
			named, _ := tn.Type.(*Named)
			if named == nil || c.derivedNormal(td) {
				continue
			}
			for _, op := range named.impls {
				if op.origin() != hashing.Type.(*Named) || len(op.TArgs) != 1 {
					continue
				}
				if fds, file := c.normalDecls(named, td); fds != nil {
					file.Decls = append(file.Decls, fds...)
					wrote = true
				}
				break
			}
		}
	}
	return wrote
}

// normalDecls are the derived NormalForm and NormalKey of the provider n,
// declared by td, and the file that holds its Hash; nil when n writes
// either or has no Hash of its package's own.
func (c *checker) normalDecls(n *Named, td *syntax.TypeDecl) ([]syntax.Decl, *syntax.File) {
	for _, name := range []string{normalFormName, normalKeyName} {
		if _, own := c.methodOf(n, name); own != nil {
			return nil, nil // the package's own functions stand
		}
	}
	_, hf := c.methodOf(n, "Hash")
	if hf == nil || hf.Decl == nil || hf.Pkg != c.pkg || hf.Decl.Type == nil || hf.Decl.Type.Params == nil || hf.Decl.Body == nil {
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
	// The receiver and the key of Hash(p P, k K), as written.
	ps := hf.Decl.Type.Params.List
	var recvType, keyType syntax.Expr
	keyName := ""
	switch {
	case len(ps) == 1 && len(ps[0].Names) == 2:
		recvType, keyType, keyName = ps[0].Type, ps[0].Type, ps[0].Names[1].Name
	case len(ps) == 2 && len(ps[0].Names) <= 1 && len(ps[1].Names) <= 1:
		recvType, keyType = ps[0].Type, ps[1].Type
		if len(ps[1].Names) == 1 {
			keyName = ps[1].Names[0].Name
		}
	}
	if file == nil || recvType == nil {
		return nil, nil
	}
	form, key := false, syntax.Expr(nil)
	if nx := c.shapeOf(n, hf); nx != nil && mapLowering != "table" {
		form, key = true, cloneSyntax(nx)
	}
	if key == nil {
		if keyName == "" || keyName == "_" {
			keyName = "key"
		}
		key = &syntax.Ident{NamePos: hf.Decl.Name.NamePos, Name: keyName}
	} else if keyName == "" || keyName == "_" {
		return nil, nil // n reads a key Hash does not name
	}
	pos := hf.Decl.Name.NamePos
	recv := func() *syntax.Field {
		return &syntax.Field{Names: []*syntax.Ident{{NamePos: pos, Name: "_"}}, Type: cloneSyntax(recvType)}
	}
	decl := func(name string, params []*syntax.Field, result syntax.Expr, body syntax.Expr) *syntax.FuncDecl {
		return &syntax.FuncDecl{
			Func:       hf.Decl.Func,
			Recv:       cloneSyntax(hf.Decl.Recv),
			Name:       &syntax.Ident{NamePos: pos, Name: name},
			TypeParams: cloneSyntax(hf.Decl.TypeParams),
			Type: &syntax.FuncType{
				Func:    hf.Decl.Type.Func,
				Params:  &syntax.FieldList{List: params},
				Results: &syntax.FieldList{List: []*syntax.Field{{Type: result}}},
			},
			Body: &syntax.BlockStmt{
				Lbrace: hf.Decl.Body.Lbrace,
				List:   []syntax.Stmt{&syntax.ExprStmt{X: body}},
				Rbrace: hf.Decl.Body.Rbrace,
			},
			Derived: td,
		}
	}
	formName := "false"
	if form {
		formName = "true"
	}
	keyField := &syntax.Field{Names: []*syntax.Ident{{NamePos: pos, Name: keyName}}, Type: cloneSyntax(keyType)}
	return []syntax.Decl{
		decl(normalFormName, []*syntax.Field{recv()}, &syntax.Ident{NamePos: pos, Name: "bool"}, &syntax.Ident{NamePos: pos, Name: formName}),
		decl(normalKeyName, []*syntax.Field{recv(), keyField}, cloneSyntax(keyType), key),
	}, file
}

// shapeOf is n of the provider's shape, Hash(k) = hash(n(k)) and Equal(a,
// b) = n(a) == n(b), as Hash writes it, or nil.
func (c *checker) shapeOf(n *Named, hf *Func) syntax.Expr {
	_, ef := c.methodOf(n, "Equal")
	if ef == nil || ef.Decl == nil || ef.Pkg != c.pkg || hf.Decl.Derived != nil || ef.Decl.Derived != nil {
		return nil
	}
	for _, f := range []*Func{hf, ef} {
		if fp, ok := c.pkg.Info.Footprints[f.Decl]; !ok || !fp.Empty() {
			return nil // n must be pure and rowless
		}
	}
	hp, ep := c.paramVars(hf.Decl), c.paramVars(ef.Decl)
	if len(hp) != 2 || len(ep) != 3 || hp[1] == nil || ep[1] == nil || ep[2] == nil {
		return nil
	}
	he, ee := soleExpr(hf.Decl), soleExpr(ef.Decl)
	call, ok := unparen(he).(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil
	}
	if cl := c.pkg.Info.Calls[call]; cl == nil || cl.Kind != CallHash {
		return nil
	}
	bin, ok := unparen(ee).(*syntax.BinaryExpr)
	if !ok || bin.Op != syntax.EQL {
		return nil
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
		return nil
	}
	// n maps a key to a key: its result is Equal's key type.
	if v, ok := ep[1].(*Var); !ok || !c.identical(c.pkg.Info.Types[bin.X], v.Type) {
		return nil
	}
	return call.Args[0]
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
