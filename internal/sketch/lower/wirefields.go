package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// jsonWrapper selects the JSON representation of ordinary std unions.
// Admission, matching, generators and failure rows stay structural: there
// is no checker exception for either library type (owner q85).
func jsonWrapper(t check.Type, name string) bool {
	n, ok := t.(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg != nil && n.Obj.Pkg.Std() && n.Obj.Pkg.Path == jsonPackage && n.Obj.Name == name
}

// wrapperParts reads the empty and payload alternatives from the union.
func wrapperParts(t check.Type) (empty, present check.Type, field *check.Field) {
	u := wireUnder(t).(*check.Union)
	for _, v := range u.Variants {
		s := wireUnder(v).(*check.Struct)
		if len(s.Fields) == 0 {
			empty = v
		} else {
			present, field = v, s.Fields[0]
		}
	}
	return
}

func (tc *typeCtx) wrapperValue(present check.Type, field *check.Field, value string) string {
	n := present.(*check.Named)
	td := tc.l.typeDecl(n.Obj, tc.site)
	return tc.goType(present) + "{" + td.fieldName[field.Name] + ": " + value + "}"
}

func (l *lowerer) wireNullableEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string, scope *namer) {
	empty, present, field := wrapperParts(t)
	x := scope.alloc("value")
	td := l.typeDecl(present.(*check.Named).Obj, tc.site)
	b.WriteString("switch " + x + " := " + v + ".(type) {\ncase " + tc.goType(empty) + ":\n")
	if em.dir() == "json" {
		b.WriteString(appendConst(w, "null"))
	} else {
		b.WriteString(w + ".Null(" + task + ")\n")
	}
	b.WriteString("case " + tc.goType(present) + ":\n" + tc.wireEncodeWith(em, field.Type, env, w, x+"."+td.fieldName[field.Name]) + "\n}\n")
}

func (l *lowerer) wireNullableDecode(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	empty, present, field := wrapperParts(t)
	b.WriteString("if " + r + ".TakeNull(" + task + ") {\nreturn " + tc.goType(empty) + "{}, nil\n}\n")
	x := scope.alloc("value")
	b.WriteString(tc.wireDecode(field.Type, env, r, x, zero, scope))
	b.WriteString("return " + tc.wrapperValue(present, field, x) + ", nil\n")
}

// wireFieldCount supplies the actual number of fields to a Writer. The
// byte-appending path does not read this hint and needs no counting pass.
func (l *lowerer) wireFieldCount(em wireEvents, tc *typeCtx, b *strings.Builder, td *typeDecl, s *check.Struct, rec string, scope *namer) string {
	n := strconv.Itoa(len(s.Fields))
	if em.dir() == "json" {
		return n
	}
	count := ""
	for _, f := range s.Fields {
		if !jsonWrapper(f.Type, "Optional") {
			continue
		}
		if count == "" {
			count = scope.alloc("fields")
			b.WriteString(count + " := " + n + "\n")
		}
		empty, _, _ := wrapperParts(f.Type)
		b.WriteString("if _, absent := " + rec + "." + td.fieldName[f.Name] + ".(" + tc.goType(empty) + "); absent { " + count + "-- }\n")
	}
	if count != "" {
		return count
	}
	return n
}

// wireFields writes the fields, omitting Absent before writing its key.
// Only records with Optional fields need a dynamic comma. The normal
// fixed-field path keeps its compile-time commas.
func (l *lowerer) wireFields(em wireEvents, tc *typeCtx, b *strings.Builder, td *typeDecl, s *check.Struct, rec string, tagged bool, env, task, w string, scope *namer) {
	dynamic := false
	for _, f := range s.Fields {
		dynamic = dynamic || jsonWrapper(f.Type, "Optional")
	}
	count := ""
	if dynamic {
		count = scope.alloc("members")
		initial := "0"
		if tagged {
			initial = "1"
		}
		b.WriteString(count + " := " + initial + "\n")
	}
	for i, f := range s.Fields {
		ft, fv := f.Type, rec+"."+td.fieldName[f.Name]
		optional := jsonWrapper(ft, "Optional")
		if optional {
			_, present, field := wrapperParts(ft)
			x, ok := scope.alloc("present"), scope.alloc("ok")
			b.WriteString("if " + x + ", " + ok + " := " + fv + ".(" + tc.goType(present) + "); " + ok + " {\n")
			fv = x + "." + l.typeDecl(present.(*check.Named).Obj, tc.site).fieldName[field.Name]
			ft = field.Type
		}
		key := check.WireFieldName(f.Name)
		if dynamic {
			b.WriteString("if " + count + " == 0 {\n" + em.key(key, true, task, w) + "} else {\n" + em.key(key, false, task, w) + "}\n")
		} else {
			b.WriteString(em.key(key, i == 0 && !tagged, task, w))
		}
		b.WriteString(tc.wireEncodeWith(em, ft, env, w, fv) + "\n")
		if dynamic {
			b.WriteString(count + "++\n")
		}
		if optional {
			b.WriteString("}\n")
		}
	}
}
