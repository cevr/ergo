package lint

import (
	"cmp"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// duplicateShape is the rule of the strict preset for one shape written
// twice (catalog CR7, CR11): two unions of the program with the same set
// of variants, or two records with the same fields (name and type), when
// neither record is a variant of a union (a variant's fields are its
// case's, and two cases may carry the same ones). Records need two fields
// or more. Generic types are out of scope. Of each group the one first by
// file and name is kept and each other is reported, naming it.
var duplicateShape = &Rule{
	Name:    "duplicate-shape",
	Code:    "L118",
	Doc:     "Two unions with the same variants, or two records with the same fields, are one type written twice; keep one.",
	Default: Warning,
	Check:   checkDuplicateShape,
}

// shapeDecl is one declared union or record: its file, name and key.
type shapeDecl struct {
	rel  string
	name *syntax.Ident
	key  string
}

func checkDuplicateShape(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	copies := p.Memo(func() any { return duplicateShapes(p) }).(map[*syntax.Ident]shapeDecl)
	for _, d := range p.File.Decls {
		if t, ok := d.(*syntax.TypeDecl); ok {
			if kept, dup := copies[t.Name]; dup {
				p.Report(t.Name.Pos(), t.Name.End(), fmt.Sprintf("%s has the shape of %s (%s); keep one", t.Name.Name, kept.name.Name, kept.rel))
			}
		}
	}
}

// duplicateShapes maps each union or record of the program that repeats
// another's shape to the one kept.
func duplicateShapes(p *Pass) map[*syntax.Ident]shapeDecl {
	type decl struct {
		rel  string
		name *syntax.Ident
		tn   *check.TypeName
	}
	var decls []decl
	variants := map[*check.TypeName]bool{}
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				t, ok := d.(*syntax.TypeDecl)
				if !ok || t.TypeParams != nil || t.Extern != nil || t.Origin != nil {
					continue
				}
				tn, ok := u.Package.Info.Defs[t.Name].(*check.TypeName)
				if !ok {
					continue
				}
				decls = append(decls, decl{f.Rel, t.Name, tn})
				if union, ok := underlying(tn).(*check.Union); ok {
					for _, v := range union.Variants {
						if n, ok := v.(*check.Named); ok {
							variants[origin(n).Obj] = true
						}
					}
				}
			}
		}
	}
	var all []shapeDecl
	for _, d := range decls {
		switch t := underlying(d.tn).(type) {
		case *check.Union:
			var keys []string
			for _, v := range t.Variants {
				keys = append(keys, typeKey(v))
			}
			slices.Sort(keys)
			all = append(all, shapeDecl{d.rel, d.name, fmt.Sprint("union ", keys)})
		case *check.Struct:
			if variants[d.tn] || len(t.Fields) < 2 {
				continue
			}
			var keys []string
			for _, f := range t.Fields {
				keys = append(keys, fmt.Sprintf("%q %s", f.Name, typeKey(f.Type)))
			}
			slices.Sort(keys)
			all = append(all, shapeDecl{d.rel, d.name, fmt.Sprint("record ", keys)})
		}
	}
	slices.SortStableFunc(all, func(a, b shapeDecl) int {
		return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.name.Name, b.name.Name))
	})
	kept := map[string]shapeDecl{}
	out := map[*syntax.Ident]shapeDecl{}
	for _, s := range all {
		if k, ok := kept[s.key]; ok {
			out[s.name] = k
			continue
		}
		kept[s.key] = s
	}
	return out
}

func underlying(tn *check.TypeName) check.Type {
	if n, ok := tn.Type.(*check.Named); ok {
		return n.Underlying()
	}
	return nil
}

func origin(n *check.Named) *check.Named {
	if n.Orig != nil {
		return n.Orig
	}
	return n
}

// typeKey is a type's identity for a key: a named type by its
// declaration, with its type arguments; any other by its text.
func typeKey(t check.Type) string {
	n, ok := t.(*check.Named)
	if !ok {
		return t.String()
	}
	key := fmt.Sprintf("%p", origin(n).Obj)
	for _, a := range n.TArgs {
		key += "," + typeKey(a)
	}
	return key
}
