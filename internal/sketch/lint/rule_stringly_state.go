package lint

import (
	"cmp"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stringlyState is the indirection rule of the strict preset (catalog
// EF34, status strings that encode modes; anti-slop repeated-switches,
// primitive-obsession): a string field of a record the program declares,
// which the program sets, outside its tests and laws, to a string
// literal it also compares it with, and compares with three or more
// different string literals, by == or != or as a match's cases. The field holds a state the program
// names itself, as text the type system cannot see: a misspelt value is
// a new state, and a match on it is never exhaustive. A field the program
// only reads (a token's text, a key event's name, a request's method) is
// input, and a local or a parameter compared with literals is a parser at
// its boundary: both are out of scope. One finding per field, at its
// first comparison by file and offset, so a reorder keeps it.
var stringlyState = &Rule{
	Name:    "stringly-state",
	Code:    "L124",
	Doc:     "A string field the program sets from literals and compares with three or more is a union written as text; declare the union.",
	Instead: "declare a union with one variant per value, and match on it",
	Default: Warning,
	Check:   checkStringlyState,
}

// comparison is one place a field is compared with a string literal.
type comparison struct {
	rel     string
	at, end syntax.Pos
	field   *check.Field
	value   string
}

// fieldStates is a field's first comparison and its distinct literals.
type fieldStates struct {
	first  comparison
	values []string
}

func checkStringlyState(p *Pass) {
	states := p.Memo(func() any { return stringStates(p) }).(map[*check.Field]fieldStates)
	for _, s := range states {
		if s.first.rel == p.Rel() {
			p.Report(s.first.at, s.first.end, fmt.Sprintf("the field %s is compared with %d string literals: %v", s.first.field.Name, len(s.values), s.values))
		}
	}
}

// stringStates is every field of the program that is set from a literal
// outside tests and laws and compared with three or more literals.
func stringStates(p *Pass) map[*check.Field]fieldStates {
	own := map[*check.Package]bool{}
	for _, u := range p.Program() {
		own[u.Package] = true
	}
	var all []comparison
	set := map[*check.Field]map[string]bool{}
	for _, u := range p.Program() {
		info := u.Package.Info
		// literal reports whether x is a string literal, and its value.
		literal := func(x syntax.Expr) (string, bool) {
			b, ok := unparen(x).(*syntax.BasicLit)
			if !ok || b.Kind != syntax.STRING {
				return "", false
			}
			v := info.Values[b]
			if v == nil {
				return "", false
			}
			return v.ExactString(), true
		}
		field := func(x syntax.Expr) *check.Field {
			s, ok := unparen(x).(*syntax.SelectorExpr)
			if !ok {
				return nil
			}
			f, pkg := check.SelectedField(info, s)
			if f == nil || !own[pkg] || !check.IsString(f.Type) {
				return nil
			}
			return f
		}
		for _, file := range u.Files {
			if file.Syntax == nil {
				continue
			}
			compare := func(sel, lit syntax.Expr, at, end syntax.Pos) {
				f := field(sel)
				v, ok := literal(lit)
				if f != nil && ok {
					all = append(all, comparison{file.Rel, at, end, f, v})
				}
			}
			for _, d := range file.Syntax.Decls {
				switch d.(type) {
				case *syntax.TestDecl, *syntax.LawDecl:
				default:
					markSet(info, d, own, set, literal, field)
				}
			}
			fi := p.run.shared.fileIndexOf(file.Syntax)
			for _, x := range fi.binaries {
				if x.Op == syntax.EQL || x.Op == syntax.NEQ {
					compare(x.X, x.Y, x.Pos(), x.End())
					compare(x.Y, x.X, x.Pos(), x.End())
				}
			}
			for _, n := range fi.switches {
				switch x := n.(type) {
				case *syntax.SwitchStmt:
					if x.Tag == nil {
						break
					}
					for _, c := range x.Clauses {
						for _, e := range c.List {
							compare(x.Tag, e, x.Tag.Pos(), x.Tag.End())
						}
					}
				}
			}
		}
	}
	slices.SortStableFunc(all, func(a, b comparison) int {
		return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.at, b.at))
	})
	out := map[*check.Field]fieldStates{}
	for _, c := range all {
		s, seen := out[c.field]
		if !seen {
			s.first = c
		}
		if !slices.Contains(s.values, c.value) {
			s.values = append(s.values, c.value)
		}
		out[c.field] = s
	}
	for f, s := range out {
		if len(s.values) < 3 || !setsCompared(set[f], s.values) {
			delete(out, f)
		} else {
			slices.Sort(s.values)
			out[f] = s
		}
	}
	return out
}

// markSet marks the fields d sets from a string literal: a keyed element
// of a record literal, or an assignment to a selected field.
func markSet(info *check.Info, d syntax.Node, own map[*check.Package]bool, set map[*check.Field]map[string]bool,
	literal func(syntax.Expr) (string, bool), field func(syntax.Expr) *check.Field) {
	walkNode(d, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CompositeLit:
			for _, e := range x.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*syntax.Ident)
				if !ok {
					continue
				}
				v, isLit := literal(kv.Value)
				if !isLit {
					continue
				}
				if f, pkg := check.KeyedField(info, x, key); f != nil && own[pkg] {
					addSet(set, f, v)
				}
			}
		case *syntax.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i, l := range x.Lhs {
					if v, isLit := literal(x.Rhs[i]); isLit {
						if f := field(l); f != nil {
							addSet(set, f, v)
						}
					}
				}
			}
		}
		return true
	})
}

// addSet records that f is set to the literal v.
func addSet(set map[*check.Field]map[string]bool, f *check.Field, v string) {
	if set[f] == nil {
		set[f] = map[string]bool{}
	}
	set[f][v] = true
}

// setsCompared reports whether the program sets the field to one of the
// literals it compares it with: a state it names, not input it reads.
func setsCompared(set map[string]bool, compared []string) bool {
	for _, v := range compared {
		if set[v] {
			return true
		}
	}
	return false
}

// unparen is x without its parentheses.
func unparen(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}
