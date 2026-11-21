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
// of variants. Records are out of scope: a record is nominal on purpose
// (two typed failures with the same fields are two failures), as the
// examples' read showed. Generic unions are out of scope too. Of each
// group the one first by file and name is kept and each other is
// reported, naming it.
var duplicateShape = &Rule{
	Name:    "duplicate-shape",
	Code:    "L118",
	Doc:     "Two unions with the same variants are one type written twice; keep one.",
	Instead: "keep one",
	Default: Warning,
	Check:   checkDuplicateShape,
}

// shapeDecl is one declared union: its file, name and key.
type shapeDecl struct {
	rel  string
	name *syntax.Ident
	key  string
}

func checkDuplicateShape(p *Pass) {
	copies := p.Memo(func() any { return duplicateShapes(p) }).(map[*syntax.Ident]shapeDecl)
	for _, d := range p.File.Decls {
		if t, ok := d.(*syntax.TypeDecl); ok {
			if kept, dup := copies[t.Name]; dup {
				where := ""
				if kept.rel != p.Rel() {
					where = " in " + kept.rel
				}
				p.Report(t.Name.Pos(), t.Name.End(), fmt.Sprintf("%s has the variants of %s%s", t.Name.Name, kept.name.Name, where))
			}
		}
	}
}

// duplicateShapes maps each union of the program that repeats another's
// variants to the one kept.
func duplicateShapes(p *Pass) map[*syntax.Ident]shapeDecl {
	var all []shapeDecl
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
				union, ok := underlying(tn).(*check.Union)
				if !ok || union.Row != nil {
					continue
				}
				var keys []string
				for _, v := range union.Variants {
					keys = append(keys, typeKey(v))
				}
				slices.Sort(keys)
				all = append(all, shapeDecl{f.Rel, t.Name, fmt.Sprint(keys)})
			}
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
