package lint

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The program-wide fact of derived-field (SR-ERGO-1): for every record
// field, the form of the value each write gives it, in terms of the
// record's other fields.

// fieldForms is the verdict of every record field the program writes.
type fieldForms struct {
	form    map[*check.Field]string
	uses    map[*check.Field]map[*check.Field]bool // the fields a form reads
	bad     map[*check.Field]bool
	partial map[*check.Field][]map[*check.Field]bool // writes that leave the field and set these others
}

// derived reports whether every write of f gives it one form over its
// siblings, and no write changes a sibling the form reads without f.
func (ff *fieldForms) derived(f *check.Field) bool {
	if ff.bad[f] || len(ff.uses[f]) == 0 {
		return false
	}
	for _, set := range ff.partial[f] {
		for g := range set {
			if ff.uses[f][g] {
				return false
			}
		}
	}
	return true
}

// recordFields is the fields of the record type t (its origin's), or nil.
func recordFields(t check.Type) []*check.Field {
	n, ok := t.(*check.Named)
	if !ok {
		return nil
	}
	if n.Orig != nil {
		n = n.Orig
	}
	s, ok := n.Underlying().(*check.Struct)
	if !ok {
		return nil
	}
	return s.Fields
}

// collectFieldForms reads every record write of the program's units.
func collectFieldForms(units []Unit, shared *runShared) *fieldForms {
	ff := &fieldForms{
		form:    map[*check.Field]string{},
		uses:    map[*check.Field]map[*check.Field]bool{},
		bad:     map[*check.Field]bool{},
		partial: map[*check.Field][]map[*check.Field]bool{},
	}
	for _, u := range units {
		info := u.Package.Info
		for _, file := range u.Files {
			if file.Syntax == nil {
				continue
			}
			fi := shared.fileIndexOf(file.Syntax)
			for _, n := range fi.lits {
				ff.literal(info, n)
			}
			for _, n := range fi.assigns {
				for i, l := range n.Lhs {
					sel, ok := l.(*syntax.SelectorExpr)
					if !ok || i >= len(n.Rhs) || len(n.Lhs) != len(n.Rhs) {
						continue
					}
					g, _ := check.SelectedField(info, sel)
					if g == nil {
						continue
					}
					t := info.Types[sel.X]
					if p, isPtr := t.(*check.Pointer); isPtr {
						t = p.Elem
					}
					ff.write(info, recordFields(t), map[*check.Field]syntax.Expr{g: n.Rhs[i]}, sel.X)
				}
			}
		}
	}
	return ff
}

// literal reads a composite literal of a record: a construction, or an
// update `T{..base, …}` of the same type.
func (ff *fieldForms) literal(info *check.Info, lit *syntax.CompositeLit) {
	fields := recordFields(info.Types[lit])
	if fields == nil {
		return
	}
	set := map[*check.Field]syntax.Expr{}
	for _, e := range lit.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*syntax.Ident)
		if !ok {
			continue
		}
		if f, _ := check.KeyedField(info, lit, key); f != nil {
			set[f] = kv.Value
		}
	}
	if lit.Base != nil && len(info.Spreads[lit]) > 0 {
		// A spread of another record type: the fields it copies are no
		// form of this record's fields.
		for _, f := range fields {
			if set[f] == nil {
				ff.bad[f] = true
			}
		}
		lit = &syntax.CompositeLit{Elts: lit.Elts}
	}
	ff.write(info, fields, set, lit.Base)
}

// write records a write that sets the fields in set, of a record of
// fields, over base (nil for a construction).
func (ff *fieldForms) write(info *check.Info, fields []*check.Field, set map[*check.Field]syntax.Expr, base syntax.Expr) {
	var others map[*check.Field]bool
	if len(set) < len(fields) {
		others = make(map[*check.Field]bool, len(set))
		for g := range set {
			others[g] = true
		}
	}
	baseShape := ""
	baseShaped := false
	for _, f := range fields {
		if ff.bad[f] {
			continue
		}
		e, ok := set[f]
		if !ok {
			// A construction sets every field; an update that leaves f
			// keeps it, and may move a sibling its form reads.
			ff.partial[f] = append(ff.partial[f], others)
			continue
		}
		if base != nil && !baseShaped {
			baseShape = shapeOf(info, base)
			baseShaped = true
		}
		fm := &former{info: info, self: f, base: base, baseShape: baseShape}
		if len(set) > 1 {
			whole := shapeOf(info, e)
			for g, v := range set {
				// A sibling with f's very value is a copy beside it, not
				// an input: each would read as the other's form.
				if s := shapeOf(info, v); g != f && readsVar(info, v) && s != whole {
					if fm.siblings == nil {
						fm.siblings = map[string]*check.Field{}
					}
					fm.siblings[s] = g
				}
			}
		}
		form, ok := fm.form(e)
		if !ok {
			ff.bad[f] = true
			continue
		}
		if old, seen := ff.form[f]; seen && old != form {
			ff.bad[f] = true
			continue
		}
		ff.form[f] = form
		if ff.uses[f] == nil && len(fm.uses) > 0 {
			ff.uses[f] = fm.uses
		}
	}
}

// readsVar reports whether e reads a variable: a value with none (a
// constant, a fresh empty table) is no input another field can be
// computed from, and two fields that both start empty are not one the
// other.
func readsVar(info *check.Info, e syntax.Expr) bool {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if _, isVar := info.Uses[id].(*check.Var); isVar {
				found = true
			}
		}
		return !found
	})
	return found
}

// former writes the form of one written value: its tree with each
// sibling's value, and each selection of a field of the update's base,
// as that field.
type former struct {
	info      *check.Info
	self      *check.Field
	base      syntax.Expr
	baseShape string
	siblings  map[string]*check.Field // a sibling's value, by its shape
	uses      map[*check.Field]bool
}

func (fm *former) field(g *check.Field) string {
	if fm.uses == nil {
		fm.uses = map[*check.Field]bool{}
	}
	fm.uses[g] = true
	return fmt.Sprintf("@%p", g)
}

func (fm *former) form(e syntax.Expr) (string, bool) {
	if len(fm.siblings) > 0 {
		if g, ok := fm.siblings[shapeOf(fm.info, e)]; ok {
			return fm.field(g), true
		}
	}
	if v := fm.info.Values[e]; v != nil {
		return "c:" + v.ExactString(), true
	}
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return fm.form(x.X)
	case *syntax.SelectorExpr:
		if fm.base == nil {
			return "", false
		}
		g, _ := check.SelectedField(fm.info, x)
		if g == nil || g == fm.self || shapeOf(fm.info, x.X) != fm.baseShape {
			return "", false
		}
		return fm.field(g), true
	case *syntax.UnaryExpr:
		s, ok := fm.form(x.X)
		return fmt.Sprintf("u%d(%s)", x.Op, s), ok
	case *syntax.BinaryExpr:
		a, okA := fm.form(x.X)
		b, okB := fm.form(x.Y)
		return fmt.Sprintf("b%d(%s,%s)", x.Op, a, b), okA && okB
	case *syntax.CallExpr:
		call := fm.info.Calls[x]
		if call == nil || !check.PureValueFunc(call.Func) || call.Recv != nil {
			return "", false
		}
		out := fmt.Sprintf("f%p(", call.Func)
		for _, a := range x.Args {
			s, ok := fm.form(a)
			if !ok {
				return "", false
			}
			out += s + ","
		}
		return out + ")", true
	}
	return "", false
}
