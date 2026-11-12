package lower

import (
	"fmt"
	"maps"
	"slices"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A proof that runs from a provider's constructor covers the states the
// constructor and the operations reach, and a rewrite applies to any value
// of the provider's type. So that search is a proof only when the type's
// construction is closed (types.md, Laws 14, Proved states): every value
// of P the program can hold was built by the constructor and changed only
// by the operations. Otherwise the run lists every field value.

// openConstruction is a place that builds or changes a value of a
// provider outside its constructor and operations: what it does, and
// where. A nil one means the construction is closed.
type openConstruction struct {
	what string
	node syntax.Node
	file *syntax.File
}

// at is the place as file:line:column, the file by its package name.
func (o *openConstruction) at() string {
	if o.file == nil || o.node == nil {
		return ""
	}
	line, col := o.file.Position(o.node.Pos())
	return fmt.Sprintf("%s:%d:%d", o.file.Name, line, col)
}

// constructionOpen is the first place, in source order, that makes the
// construction of prov, built by ctor and changed by conv's operations,
// open; nil when it is closed. It is closed when:
//
//   - every field of prov is unexported, so no other package builds or
//     writes one (types.md, Zero values 6), and no field holds a prov;
//   - no operation takes or returns a prov other than its receiver;
//   - in prov's package, outside ctor's and the operations' bodies, no
//     literal or record update builds a prov, no conversion makes one
//     from another type, and no expression writes a field of one: an
//     assignment to it, its address, or an argument that a function
//     takes by borrow.
//
// A record with fields has no zero value, and a record with an unexported
// field has no derived codec (types.md, Codecs 1), so those build none.
func constructionOpen(prov *check.Named, ctor *check.Func, conv check.Conversion) *openConstruction {
	st, _ := prov.Underlying().(*check.Struct)
	if ctor == nil || st == nil {
		return nil
	}
	pkg := prov.Obj.Pkg
	if pkg == nil || pkg.Info == nil {
		return &openConstruction{what: "its package has no checked source"}
	}
	isProv := func(t check.Type) bool {
		if p, ok := t.(*check.Pointer); ok {
			t = p.Elem
		}
		n, ok := t.(*check.Named)
		return ok && n.Obj == prov.Obj
	}
	mentions := func(t check.Type) bool { return slices.Contains(check.Mentions(t, pkg), prov.Obj) }
	for _, f := range st.Fields {
		if r, _ := utf8.DecodeRuneInString(f.Name); unicode.IsUpper(r) {
			return &openConstruction{what: "its field " + f.Name + " is exported, so another package writes it"}
		}
		if mentions(f.Type) {
			return &openConstruction{what: "its field " + f.Name + " holds a " + prov.Obj.Name}
		}
	}
	allowed := map[syntax.Node]bool{ctor.Decl: true}
	for _, name := range slices.Sorted(maps.Keys(conv.Table)) {
		f := conv.Table[name]
		if f == nil || f.Decl == nil {
			return &openConstruction{what: "its operation " + name + " has no Ergo body"}
		}
		allowed[f.Decl] = true
		for i, v := range f.Sig.Params {
			if i > 0 && mentions(v.Type) {
				return &openConstruction{what: "its operation " + name + " takes a " + prov.Obj.Name + " besides its receiver"}
			}
		}
		for _, r := range f.Sig.Results {
			if mentions(r) {
				return &openConstruction{what: "its operation " + name + " returns a " + prov.Obj.Name}
			}
		}
	}
	info := pkg.Info
	// writes reports whether x, a place written, is a field of a prov or
	// part of one.
	writes := func(x syntax.Expr) bool {
		for x != nil {
			switch e := x.(type) {
			case *syntax.ParenExpr:
				x = e.X
			case *syntax.SelectorExpr:
				if isProv(info.Types[e.X]) && slices.ContainsFunc(st.Fields, func(f *check.Field) bool { return f.Name == e.Sel.Name }) {
					return true
				}
				x = e.X
			case *syntax.IndexExpr:
				x = e.X
			case *syntax.SliceExpr:
				x = e.X
			case *syntax.StarExpr:
				x = e.X
			default:
				return false
			}
		}
		return false
	}
	for _, file := range pkg.Files() {
		for _, decl := range file.Decls {
			if allowed[decl] {
				continue
			}
			var open *openConstruction
			found := func(what string, n syntax.Node) {
				open = &openConstruction{what: what, node: n, file: file}
			}
			syntax.Inspect(decl, func(n syntax.Node) bool {
				if open != nil {
					return false
				}
				switch n := n.(type) {
				case *syntax.CompositeLit:
					if isProv(info.Types[n]) {
						found("a literal builds one", n)
					}
				case *syntax.AssignStmt:
					for _, x := range n.Lhs {
						if writes(x) {
							found("an assignment writes its field", x)
						}
					}
				case *syntax.UnaryExpr:
					if n.Op == syntax.AND && writes(n.X) {
						found("a borrow of its field", n)
					}
				case *syntax.CallExpr:
					c := info.Calls[n]
					if c == nil {
						return true
					}
					if c.Kind == check.CallConversion && isProv(c.Type) && len(n.Args) == 1 && !isProv(info.Types[n.Args[0]]) {
						found("a conversion makes one", n)
						return false
					}
					var params []*check.Var
					if c.Func != nil {
						params = c.Func.Sig.Params
					}
					args := n.Args
					if c.Recv != nil {
						args = append([]syntax.Expr{c.Recv}, args...)
					}
					for i, a := range args {
						borrow := c.Func == nil || i >= len(params)
						if !borrow {
							_, borrow = params[i].Type.(*check.Pointer)
						}
						if borrow && writes(a) {
							found("a call may write its field", a)
						}
					}
				}
				return open == nil
			})
			if open != nil {
				return open
			}
		}
	}
	return nil
}

// constructionWhy is why a proof of prov covers every value of its type,
// not only its constructor's states; "" when the construction is closed.
func constructionWhy(prov *check.Named, ctor *check.Func, conv check.Conversion, at func(*openConstruction) string) string {
	o := constructionOpen(prov, ctor, conv)
	if o == nil {
		return ""
	}
	where := ""
	if s := at(o); s != "" {
		where = " at " + s
	}
	return "the proof covers every value of " + prov.Obj.Name + ", not only the states " + ctor.Name + " and its operations reach: " + o.what + where
}
