package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Argument text (196l; spec D8: a failure reports seed and arguments). A
// refuted property prints each argument as an Ergo literal of its type, the
// source a reader pastes into a test to replay the case:
//
//   - a record: `Point{X: 1, Y: 6}`, its fields in declaration order; a
//     record with no fields, such as an empty variant, is bare: `Wait`
//     (types.md, Structs 2); a generic instance names its type arguments:
//     `Box[int]{V: 1}`;
//   - a union: its variant's literal;
//   - a defined type over a basic, slice, Map or tuple type: a conversion,
//     `Celsius(21.5)`;
//   - a slice: `[]int{1, 2}`; a Map: `Map[string, int]{"a": 1}`, its keys
//     sorted (by value for a basic key, else by their text); a tuple:
//     `(1, "a")`;
//   - a string: quoted with the escapes the scanner reads
//     (strconv.Unquote); an integer in decimal, a rune too, because rune
//     is int32 (as in Go); a float in its shortest
//     round-trip form with a decimal point or an exponent; a duration in its
//     largest exact unit, `1500ms`; a bool: true or false.
//
// A complex value has no Ergo literal (no imaginary literal and no complex
// built-in, S001 and S104), so it prints in Go's form, `complex(1.5, -2)`.
//
// The text is written by a generated Go function per named, slice, Map and
// tuple type instance (keyed by typeKey, as the generators are), so a
// generic instance sees its own fields and no reflection is used. The
// generators draw only finite floats, so NaN and the infinities, which have
// no literal, never reach the text.
//
// Prior art: Go's fuzzing corpus writes each failing input as a Go literal
// (`int(5)`), and Elixir's StreamData prints a counterexample with inspect,
// as Elixir source.

// litKey is the package-scope key of the literal function of t, and its
// preferred name; ok is false for a basic type, whose text is inline.
func (l *lowerer) litKey(t check.Type) (key pkgKey, base string, ok bool) {
	switch u := t.(type) {
	case *check.Named:
		return pkgKey{"literal", l.typeKey(t)}, "ergoLit" + u.Obj.Name, true
	case *check.Slice:
		return pkgKey{"literal", l.typeKey(t)}, "ergoLitSlice", true
	case *check.Map:
		return pkgKey{"literal", l.typeKey(t)}, "ergoLitMap", true
	case *check.Tuple:
		return pkgKey{"literal", l.typeKey(t)}, "ergoLitTuple", true
	}
	return pkgKey{}, "", false
}

// litTypes calls visit on t and on each type t's literal reaches, each
// once, in a fixed order: the naming pass reserves their functions' names.
func (l *lowerer) litTypes(t check.Type, seen map[string]bool, visit func(check.Type)) {
	if _, _, ok := l.litKey(t); !ok {
		return
	}
	if k := l.typeKey(t); seen[k] {
		return
	} else {
		seen[k] = true
	}
	visit(t)
	switch u := t.(type) {
	case *check.Slice:
		l.litTypes(u.Elem, seen, visit)
	case *check.Map:
		l.litTypes(u.Key, seen, visit)
		l.litTypes(u.Elem, seen, visit)
	case *check.Tuple:
		for _, e := range u.Types {
			l.litTypes(e, seen, visit)
		}
	case *check.Named:
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				l.litTypes(f.Type, seen, visit)
			}
		case *check.Union:
			for _, v := range under.Variants {
				l.litTypes(v, seen, visit)
			}
		default:
			l.litTypes(under, seen, visit)
		}
	}
}

// litExpr is the Go expression of the Ergo literal of val, val of type t.
func (l *lowerer) litExpr(t check.Type, val string, site syntax.Node) string {
	if b, ok := t.(*check.Basic); ok {
		h := l.helper
		switch {
		case b.Kind == check.Bool:
			return h("strconv") + ".FormatBool(" + val + ")"
		case b.Kind == check.String:
			return h("strconv") + ".Quote(" + val + ")"
		case b.Kind == check.Duration:
			return h("LitDuration") + "(int64(" + val + "))"
		case b.Kind >= check.Int && b.Kind <= check.Int64:
			return h("strconv") + ".FormatInt(int64(" + val + "), 10)"
		case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
			return h("strconv") + ".FormatUint(uint64(" + val + "), 10)"
		case b.Kind == check.Float32:
			return h("LitFloat") + "(float64(" + val + "), 32)"
		case b.Kind == check.Float64:
			return h("LitFloat") + "(" + val + ", 64)"
		case b.Kind == check.Complex64:
			return h("LitComplex") + "(complex128(" + val + "), 32)"
		case b.Kind == check.Complex128:
			return h("LitComplex") + "(" + val + ", 64)"
		}
		ice(site, "a property argument of type %s, which has no literal; the checker admits only generatable types", t)
	}
	return l.litFunc(t, site) + "(" + val + ")"
}

// litFunc is the literal function of t, emitted once.
func (l *lowerer) litFunc(t check.Type, site syntax.Node) string {
	key, base, ok := l.litKey(t)
	if !ok {
		ice(site, "a property argument of type %s, which has no literal function", t)
	}
	name := l.pkgName(key, base)
	if l.tests.lits[key] {
		return name
	}
	l.tests.lits[key] = true
	tc := &typeCtx{l: l, site: site}
	scope := newNamer(l.names)
	v := scope.alloc("v")
	q := strconv.Quote
	var body strings.Builder
	switch u := t.(type) {
	case *check.Slice:
		parts, e := scope.alloc("parts"), scope.alloc("e")
		body.WriteString("\t" + parts + " := make([]string, 0, len(" + v + "))\n")
		body.WriteString("\tfor _, " + e + " := range " + v + " {\n\t\t" + parts + " = append(" + parts + ", " + l.litExpr(u.Elem, e, site) + ")\n\t}\n")
		body.WriteString("\treturn " + q(l.ergoType(t)+"{") + " + " + l.helper("strings") + ".Join(" + parts + ", \", \") + \"}\"\n")
	case *check.Map:
		less := "nil"
		if b, basic := u.Key.(*check.Basic); basic && b.Kind != check.Bool && b.Kind != check.Complex64 && b.Kind != check.Complex128 {
			kt := tc.goType(u.Key)
			less = "func(a, b " + kt + ") bool { return a < b }"
		}
		k, e := scope.alloc("k"), scope.alloc("e")
		keyText := "func(" + k + " " + tc.goType(u.Key) + ") string { return " + l.litExpr(u.Key, k, site) + " }"
		valText := "func(" + e + " " + tc.goType(u.Elem) + ") string { return " + l.litExpr(u.Elem, e, site) + " }"
		body.WriteString("\treturn " + l.helper("LitEntries") + "(" + q(l.ergoType(t)) + ", " + v + ", " + keyText + ", " + valText + ", " + less + ")\n")
	case *check.Tuple:
		parts := make([]string, len(u.Types))
		for i, e := range u.Types {
			parts[i] = l.litExpr(e, v+"."+tupleField(i), site)
		}
		body.WriteString("\treturn \"(\" + " + strings.Join(parts, " + \", \" + ") + " + \")\"\n")
	case *check.Named:
		switch under := u.Underlying().(type) {
		case *check.Struct:
			if len(under.Fields) == 0 {
				body.WriteString("\treturn " + q(l.ergoType(t)) + "\n")
				break
			}
			td := l.typeDecl(u.Obj, site)
			parts := make([]string, len(under.Fields))
			for i, f := range under.Fields {
				field, ok := td.fieldName[f.Name]
				if !ok {
					ice(site, "%s has no field %s", td.name, f.Name)
				}
				sep := ", "
				if i == 0 {
					sep = "{"
				}
				parts[i] = q(sep+f.Name+": ") + " + " + l.litExpr(f.Type, v+"."+field, site)
			}
			body.WriteString("\treturn " + q(l.ergoType(t)) + " + " + strings.Join(parts, " + ") + " + \"}\"\n")
		case *check.Union:
			x := scope.alloc("x")
			body.WriteString("\tswitch " + x + " := " + v + ".(type) {\n")
			for _, vt := range under.Variants {
				body.WriteString("\tcase " + tc.goType(vt) + ":\n\t\treturn " + l.litExpr(vt, x, site) + "\n")
			}
			body.WriteString("\t}\n\tpanic(" + q("a value of "+l.ergoType(t)+" with no variant") + ")\n")
		default:
			body.WriteString("\treturn " + q(l.ergoType(t)+"(") + " + " + l.litExpr(under, tc.goType(under)+"("+v+")", site) + " + \")\"\n")
		}
	default:
		ice(site, "a property argument of type %s, which has no literal function", t)
	}
	l.gen.WriteString("func " + name + "(" + v + " " + tc.goType(t) + ") string {\n" + body.String() + "}\n\n")
	return name
}

// ergoType is t's Ergo spelling in the lowered package: a type of the
// package or of the prelude by its name, a type of an imported package
// qualified by the name the file imports it under.
func (l *lowerer) ergoType(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		return u.Name
	case *check.Slice:
		return "[]" + l.ergoType(u.Elem)
	case *check.Map:
		return "Map[" + l.ergoType(u.Key) + ", " + l.ergoType(u.Elem) + "]"
	case *check.Tuple:
		parts := make([]string, len(u.Types))
		for i, e := range u.Types {
			parts[i] = l.ergoType(e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *check.Named:
		name := u.Obj.Name
		if p := u.Obj.Pkg; p != nil && p != l.pkg && p.Path != std.PreludePath {
			name = l.importName(p) + "." + name
		}
		if len(u.TArgs) > 0 {
			parts := make([]string, len(u.TArgs))
			for i, a := range u.TArgs {
				parts[i] = l.ergoType(a)
			}
			name += "[" + strings.Join(parts, ", ") + "]"
		}
		return name
	}
	return t.String()
}

// importName is the name the root package imports p under: the import's
// own name, in the first of the root's files that gives one, else the
// package's name. It reads the root's files, not the current one, so the
// name does not depend on which declaration is being lowered (196b).
func (l *lowerer) importName(p *check.Package) string {
	for _, f := range l.srcs {
		if f.pkg != l.pkg {
			continue
		}
		for _, d := range f.file.Decls {
			g, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				if s.Path == nil || s.Name == nil {
					continue
				}
				if path, err := strconv.Unquote(s.Path.Value); err != nil || path != p.Path {
					continue
				}
				return s.Name.Name
			}
		}
	}
	return p.Name
}
