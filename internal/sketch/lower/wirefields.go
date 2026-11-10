package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
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

// wireFieldWitness is the Field of t's codec witness: for json.Optional,
// the record-field form, so a record field typed by a type parameter takes
// the form of its argument as a field declared Optional does; nil
// otherwise. The rule is one: the field's type after instantiation selects
// the form.
func (fc *funcCtx) wireFieldWitness(tc *typeCtx, t check.Type, site syntax.Node) string {
	if !jsonWrapper(t, "Optional") {
		return "nil"
	}
	empty, present, field := wrapperParts(t)
	gt, pt := tc.goType(t), tc.goType(present)
	payload := func(v string) string {
		return v + ".(" + pt + ")." + fc.l.typeDecl(present.(*check.Named).Obj, site).fieldName[field.Name]
	}
	encSig, decSig := wireCodecSigs(t)
	enc := fc.envClosure(callerEnv, "e", site, func(e string) ([]string, string, string) {
		w, v := fc.scope.alloc("w"), fc.scope.alloc("v")
		params := []string{w + " " + tc.goType(encSig.Params[0].Type), v + " " + gt}
		return params, tc.valueResults(nil), tc.wireEncode(field.Type, e, w, payload(v)) + "\nreturn struct{}{}, nil\n"
	})
	dec := fc.envClosure(callerEnv, "e", site, func(e string) ([]string, string, string) {
		r, x := fc.scope.alloc("r"), fc.scope.alloc("x")
		params := []string{r + " " + tc.goType(decSig.Params[0].Type)}
		return params, tc.valueResults(t), tc.wireDecode(field.Type, e, r, x, "*new("+gt+")", fc.scope) + "return " + tc.wrapperValue(present, field, x) + ", nil\n"
	})
	fast := "nil"
	if wireFast() {
		e, b, v := fc.scope.alloc("e"), fc.scope.alloc("b"), fc.scope.alloc("v")
		fast = "func(" + e + " *" + fc.l.pkgName(pkgKey{"env", nil}, "Env") + ", " + b + " *" + fc.l.rt() + ".Text, " + v + " " + gt + ") {\n" + tc.wireEncodeWith(jsonEvents{}, field.Type, e, b, payload(v)) + "\n}"
	}
	v := fc.scope.alloc("v")
	absent := "func(" + v + " " + gt + ") bool { _, ok := " + v + ".(" + tc.goType(empty) + "); return ok }"
	return "&" + tc.wireFieldType(t) + "{Enc: " + enc + ", Dec: " + dec + ", JSON: " + fast + ", Absent: " + absent + ", Empty: " + tc.goType(empty) + "{}}"
}

// paramField is a record field typed by the wire type parameter tp: its
// witness, and a context whose witness for tp is the record-field form.
func (tc *typeCtx) paramField(t check.Type) (wit string, field *typeCtx, ok bool) {
	tp, ok := t.(*check.TypeParam)
	if !ok {
		return "", nil, false
	}
	wit = tc.wireOf(tp)
	f := *tc
	f.wires = map[*check.TypeParam]string{}
	for k, v := range tc.wires {
		f.wires[k] = v
	}
	f.wires[tp] = wit + ".Field"
	return wit, &f, true
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
		wit, _, param := tc.paramField(f.Type)
		if !param && !jsonWrapper(f.Type, "Optional") {
			continue
		}
		if count == "" {
			count = scope.alloc("fields")
			b.WriteString(count + " := " + n + "\n")
		}
		fv := rec + "." + td.fieldName[f.Name]
		if param {
			b.WriteString("if " + wit + ".Field != nil && " + wit + ".Field.Absent(" + fv + ") { " + count + "-- }\n")
			continue
		}
		empty, _, _ := wrapperParts(f.Type)
		b.WriteString("if _, absent := " + fv + ".(" + tc.goType(empty) + "); absent { " + count + "-- }\n")
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
		_, _, param := tc.paramField(f.Type)
		dynamic = dynamic || param || jsonWrapper(f.Type, "Optional")
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
		key := check.WireFieldName(f.Name)
		if wit, ftc, ok := tc.paramField(ft); ok {
			// The argument decides at run time: its witness's Field is
			// set only for json.Optional.
			b.WriteString("if " + wit + ".Field == nil || !" + wit + ".Field.Absent(" + fv + ") {\n")
			b.WriteString("if " + count + " == 0 {\n" + em.key(key, true, task, w) + "} else {\n" + em.key(key, false, task, w) + "}\n")
			b.WriteString("if " + wit + ".Field != nil {\n" + ftc.wireEncodeWith(em, ft, env, w, fv) + "\n} else {\n" + tc.wireEncodeWith(em, ft, env, w, fv) + "\n}\n")
			b.WriteString(count + "++\n}\n")
			continue
		}
		optional := jsonWrapper(ft, "Optional")
		if optional {
			_, present, field := wrapperParts(ft)
			x, ok := scope.alloc("present"), scope.alloc("ok")
			b.WriteString("if " + x + ", " + ok + " := " + fv + ".(" + tc.goType(present) + "); " + ok + " {\n")
			fv = x + "." + l.typeDecl(present.(*check.Named).Obj, tc.site).fieldName[field.Name]
			ft = field.Type
		}
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
