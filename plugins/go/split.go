// The Go Splitter (SEMWS M1, level 1): one Go file cut into units by
// go/parser, the answer work's plugin protocol carries as UnitsAre
// (modules/work/work/language.ergo, plugin.ergo).
//
// A unit is the package clause or one top-level declaration, keyed by
// the package and the names it declares, never by its offset:
//   - a function is PKG.Name, a method PKG.Recv.Name (the receiver's type
//     name, with no pointer and no type parameters);
//   - a type, var or const declaration is keyed by its first name
//     (PKG.Name); a group `const ( ... )` too, its specs its members;
//   - the imports of a file are one unit, "import", whose members are
//     the import specs keyed by path, so imports merge as a set;
//   - a struct type's fields and an interface's methods are its members,
//     as are the elements of a composite literal a var declares (a
//     table), keyed by the key of a key: value element, else by the
//     element's text;
//   - a key taken twice (two init funcs, two `var _ = ...`) keys the
//     later ones "K #2".
//
// The texts join to the file: a unit runs from the line after the end of
// the declaration before it to the line after its own end, so the
// comment above a declaration belongs to its unit and the comment after
// it on its line stays with it; the first unit starts at the file's
// first byte and the last runs to its end. A member runs from the end of
// the member before it (or the opening brace or parenthesis) to its own
// end, so its separator leads it, and the members of a unit are one run
// of its text.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Unit is one unit of the protocol: its key, kind, text, whether its
// name is exported, and its members.
type Unit struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Exported bool   `json:"exported"`
	Members  []Unit `json:"members"`
}

// Unreadable is a file go/parser cannot read.
type Unreadable struct{ Why string }

func (u Unreadable) Error() string { return u.Why }

// Units is the units of the Go file whose text is text.
func Units(file, text string) ([]Unit, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, text, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, Unreadable{Why: err.Error()}
	}
	tf := fset.File(f.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }
	pkg := f.Name.Name
	// lineEnd is the offset just past the newline that ends the line of
	// offset at (the file's end when there is none).
	lineEnd := func(at int) int {
		if i := strings.IndexByte(text[at:], '\n'); i >= 0 {
			return at + i + 1
		}
		return len(text)
	}
	type cut struct {
		end  int
		unit Unit
	}
	var cuts []cut
	add := func(end int, u Unit) {
		// A comment on the declaration's last line stays with it: the
		// unit ends at the end of that line.
		cuts = append(cuts, cut{end: lineEnd(end), unit: u})
	}
	add(off(f.Name.End()), Unit{Key: "package", Kind: "package"})
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
				kind = "method"
			}
			add(off(d.End()), Unit{Key: pkg + "." + name, Kind: kind, Exported: d.Name.IsExported()})
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				// An import declaration is the unit "import" (a second one
				// "import #2"), its specs keyed by path.
				add(off(d.End()), Unit{Key: "import", Kind: "import", Members: specMembers(text, off, d, specKey)})
				continue
			}
			first, exported := firstName(d)
			u := Unit{Key: pkg + "." + first, Kind: d.Tok.String(), Exported: exported}
			if d.Lparen.IsValid() {
				u.Members = specMembers(text, off, d, specKey)
			} else if len(d.Specs) == 1 {
				u.Members = innerMembers(text, off, d.Specs[0])
			}
			add(off(d.End()), u)
		default:
			add(off(d.End()), Unit{Key: pkg + ".decl", Kind: "decl"})
		}
	}
	// The texts: each unit from the end of the one before it.
	out := make([]Unit, 0, len(cuts))
	seen := map[string]int{}
	start := 0
	for i, c := range cuts {
		end := max(c.end, start)
		if i == len(cuts)-1 {
			end = len(text)
		}
		u := c.unit
		u.Text = text[start:end]
		seen[u.Key]++
		if n := seen[u.Key]; n > 1 {
			u.Key = fmt.Sprintf("%s #%d", u.Key, n)
		}
		if u.Members == nil {
			u.Members = []Unit{}
		}
		out = append(out, u)
		start = end
	}
	return out, nil
}

// recvName is the type name of a receiver: T for T, *T, T[P] and *T[P].
func recvName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return "?"
		}
	}
}

// firstName is the first name a type, var or const declaration declares
// and whether it is exported.
func firstName(d *ast.GenDecl) (string, bool) {
	for _, s := range d.Specs {
		switch s := s.(type) {
		case *ast.TypeSpec:
			return s.Name.Name, s.Name.IsExported()
		case *ast.ValueSpec:
			if len(s.Names) > 0 {
				return s.Names[0].Name, s.Names[0].IsExported()
			}
		}
	}
	return d.Tok.String(), false
}

// specKey is the key of a spec of a group: an import's path, a type's
// name, a value spec's names.
func specKey(text string, off func(token.Pos) int, s ast.Spec) string {
	switch s := s.(type) {
	case *ast.ImportSpec:
		return s.Path.Value
	case *ast.TypeSpec:
		return s.Name.Name
	case *ast.ValueSpec:
		names := make([]string, len(s.Names))
		for i, n := range s.Names {
			names[i] = n.Name
		}
		return strings.Join(names, ",")
	}
	return text[off(s.Pos()):off(s.End())]
}

// specMembers is the specs of a parenthesized group as members.
func specMembers(text string, off func(token.Pos) int, d *ast.GenDecl, key func(string, func(token.Pos) int, ast.Spec) string) []Unit {
	if !d.Lparen.IsValid() {
		if len(d.Specs) == 1 {
			return innerMembers(text, off, d.Specs[0])
		}
		return []Unit{}
	}
	from := off(d.Lparen) + 1
	ms := make([]Unit, 0, len(d.Specs))
	for _, s := range d.Specs {
		end := off(s.End())
		// A comment on the spec's own line stays with it.
		if c := lineComment(s); c != nil && off(c.End()) > end {
			end = off(c.End())
		}
		ms = append(ms, Unit{Key: key(text, off, s), Kind: "spec", Text: text[from:end], Members: []Unit{}})
		from = end
	}
	return unique(ms)
}

func lineComment(s ast.Spec) *ast.CommentGroup {
	switch s := s.(type) {
	case *ast.ImportSpec:
		return s.Comment
	case *ast.TypeSpec:
		return s.Comment
	case *ast.ValueSpec:
		return s.Comment
	}
	return nil
}

// innerMembers is the members of one spec: a struct's fields, an
// interface's methods, a composite literal's elements.
func innerMembers(text string, off func(token.Pos) int, s ast.Spec) []Unit {
	switch s := s.(type) {
	case *ast.TypeSpec:
		switch t := s.Type.(type) {
		case *ast.StructType:
			return fieldMembers(text, off, t.Fields)
		case *ast.InterfaceType:
			return fieldMembers(text, off, t.Methods)
		}
	case *ast.ValueSpec:
		if len(s.Values) == 1 {
			if c, ok := s.Values[0].(*ast.CompositeLit); ok && len(c.Elts) > 0 {
				return eltMembers(text, off, c)
			}
		}
	}
	return []Unit{}
}

func fieldMembers(text string, off func(token.Pos) int, fl *ast.FieldList) []Unit {
	if fl == nil || len(fl.List) == 0 || !fl.Opening.IsValid() {
		return []Unit{}
	}
	from := off(fl.Opening) + 1
	ms := make([]Unit, 0, len(fl.List))
	for _, f := range fl.List {
		end := off(f.End())
		if f.Comment != nil && off(f.Comment.End()) > end {
			end = off(f.Comment.End())
		}
		key := ""
		if len(f.Names) > 0 {
			names := make([]string, len(f.Names))
			for i, n := range f.Names {
				names[i] = n.Name
			}
			key = strings.Join(names, ",")
		} else {
			key = text[off(f.Type.Pos()):off(f.Type.End())]
		}
		ms = append(ms, Unit{Key: key, Kind: "field", Text: text[from:end], Exported: len(f.Names) > 0 && f.Names[0].IsExported(), Members: []Unit{}})
		from = end
	}
	return unique(ms)
}

func eltMembers(text string, off func(token.Pos) int, c *ast.CompositeLit) []Unit {
	from := off(c.Lbrace) + 1
	ms := make([]Unit, 0, len(c.Elts))
	for i, e := range c.Elts {
		end := off(e.End())
		// The comma after an element is the separator that leads the
		// next one; the last element's trailing comma ends its member.
		if i == len(c.Elts)-1 {
			if j := strings.IndexByte(text[end:off(c.Rbrace)], ','); j >= 0 {
				end += j + 1
			}
		}
		key := ""
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			key = text[off(kv.Key.Pos()):off(kv.Key.End())]
		} else {
			key = strings.Join(strings.Fields(text[off(e.Pos()):off(e.End())]), " ")
		}
		ms = append(ms, Unit{Key: key, Kind: "element", Text: text[from:end], Members: []Unit{}})
		from = end
	}
	return unique(ms)
}

// unique keys the later of two members with one key "K #2".
func unique(ms []Unit) []Unit {
	seen := map[string]int{}
	for i := range ms {
		seen[ms[i].Key]++
		if n := seen[ms[i].Key]; n > 1 {
			ms[i].Key = fmt.Sprintf("%s #%d", ms[i].Key, n)
		}
	}
	return ms
}
