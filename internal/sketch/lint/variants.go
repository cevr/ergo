package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The variant facts of option-fields-follow-variant (SR-ERGO-2): which
// variant of a union a written value is, and, for every record, which
// variant each union field takes at each write.

// variantOf is the variant declaration the value e builds: a composite
// literal's type, or a field-less variant named bare or qualified; nil
// when e is any other expression (its variant is not known here).
func variantOf(info *check.Info, e syntax.Expr) *check.TypeName {
	for {
		p, ok := e.(*syntax.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	switch x := e.(type) {
	case *syntax.CompositeLit:
		if n, ok := info.Types[x].(*check.Named); ok {
			return origin(n).Obj
		}
	case *syntax.Ident:
		tn, _ := info.Uses[x].(*check.TypeName)
		return tn
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok {
			if _, pkg := info.Uses[id].(*check.PkgName); pkg {
				if n, ok := info.Types[x].(*check.Named); ok {
					return origin(n).Obj
				}
			}
		}
	}
	return nil
}

// unionOf is the variant declarations of the union type t, or nil.
func unionOf(t check.Type) []*check.TypeName {
	n, ok := t.(*check.Named)
	if !ok {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || u.Row != nil {
		return nil
	}
	var out []*check.TypeName
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok {
			return nil
		}
		out = append(out, origin(vn).Obj)
	}
	return out
}

// emptyVariant reports a variant with no fields (None).
func emptyVariant(tn *check.TypeName) bool {
	n, ok := tn.Type.(*check.Named)
	if !ok {
		return false
	}
	s, ok := n.Underlying().(*check.Struct)
	return ok && len(s.Fields) == 0
}

// optionLike reports a union of two variants, one of them field-less:
// a value that is there or not.
func optionLike(variants []*check.TypeName) bool {
	return len(variants) == 2 && emptyVariant(variants[0]) != emptyVariant(variants[1])
}

// variantWrites is, per record field of a union type, the variant each
// write gives it, one map per write site, and the fields whose writes are
// not all known.
type variantWrites struct {
	sites   map[*check.TypeName][]map[*check.Field]*check.TypeName // by record
	unknown map[*check.Field]bool
}

func collectVariantWrites(units []Unit) *variantWrites {
	vw := &variantWrites{
		sites:   map[*check.TypeName][]map[*check.Field]*check.TypeName{},
		unknown: map[*check.Field]bool{},
	}
	for _, u := range units {
		info := u.Package.Info
		for _, file := range u.Files {
			if file.Syntax == nil {
				continue
			}
			syntax.Inspect(file.Syntax, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.CompositeLit:
					vw.literal(info, n)
				case *syntax.AssignStmt:
					for _, l := range n.Lhs {
						if sel, ok := l.(*syntax.SelectorExpr); ok {
							if g, _ := check.SelectedField(info, sel); g != nil {
								vw.unknown[g] = true // an assignment moves one field alone
							}
						}
					}
				}
				return true
			})
		}
	}
	return vw
}

func (vw *variantWrites) literal(info *check.Info, lit *syntax.CompositeLit) {
	n, ok := info.Types[lit].(*check.Named)
	if !ok {
		return
	}
	rec := origin(n).Obj
	if recordFields(n) == nil {
		return
	}
	// An update that sets one union field and not another gives a site
	// without the other's variant, so follows sees no pairing there.
	site := map[*check.Field]*check.TypeName{}
	for _, e := range lit.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*syntax.Ident)
		if !ok {
			continue
		}
		f, _ := check.KeyedField(info, lit, key)
		if f == nil || unionOf(f.Type) == nil {
			continue
		}
		if v := variantOf(info, kv.Value); v != nil {
			site[f] = v
		} else {
			vw.unknown[f] = true
		}
	}
	vw.sites[rec] = append(vw.sites[rec], site)
}

// follows is the variant of driver under which every site gives opt its
// present variant, when that is one variant and some site gives driver
// another; nil otherwise.
func (vw *variantWrites) follows(rec *check.TypeName, opt, driver *check.Field) *check.TypeName {
	if vw.unknown[opt] || vw.unknown[driver] {
		return nil
	}
	var under *check.TypeName
	other := false
	for _, site := range vw.sites[rec] {
		o, d := site[opt], site[driver]
		if o == nil || d == nil {
			return nil
		}
		if emptyVariant(o) {
			continue
		}
		if under != nil && under != d {
			return nil
		}
		under = d
	}
	if under == nil {
		return nil
	}
	for _, site := range vw.sites[rec] {
		if site[driver] != under {
			other = true
		}
	}
	if !other {
		return nil
	}
	return under
}
