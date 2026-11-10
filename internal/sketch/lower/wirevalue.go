package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// json.Value (G4) is an ordinary closed union of std json; its codec writes
// each kind as the bare JSON value, not as a tagged record: null, a bool,
// a number's spelling as it was read, a string, an array of values and an
// object of members in order, a repeated key included. Decode reads by the
// Reader's Peek. Prior art: Go's json.RawMessage keeps the bytes;
// ReScript's JSON.t and Gleam's Dynamic are untyped trees the host's parser
// builds; Elixir Jason keeps member order only for ordered objects.

// valuePart is the variant of json.Value named name, its record and the
// Go name of its field (field may be "").
func (tc *typeCtx) valuePart(t check.Type, name, field string) (variant check.Type, goField string) {
	for _, v := range wireUnder(t).(*check.Union).Variants {
		n := v.(*check.Named)
		if n.Obj.Name != name {
			continue
		}
		if field != "" {
			goField = tc.l.typeDecl(n.Obj, tc.site).fieldName[field]
		}
		return v, goField
	}
	ice(tc.site, "json.Value has no variant %s", name)
	return nil, ""
}

// valueFieldType is the type of the field of the variant name.
func valueFieldType(t check.Type, name, field string) check.Type {
	for _, v := range wireUnder(t).(*check.Union).Variants {
		if v.(*check.Named).Obj.Name != name {
			continue
		}
		for _, f := range wireUnder(v).(*check.Struct).Fields {
			if f.Name == field {
				return f.Type
			}
		}
	}
	return nil
}

// numEncode writes a json.Num's spelling, f, as the number itself.
func numEncode(em wireEvents, task, w, f string) string {
	if em.dir() == "json" {
		return w + ".B = append(" + w + ".B, " + f + "...)\n"
	}
	return w + ".Number(" + task + ", " + f + ")\n"
}

// numDecode reads a number's spelling into the json.Num t and returns it.
func numDecode(tc *typeCtx, t check.Type, field, task, r, zero string, scope *namer) string {
	s, err := scope.alloc("s"), scope.alloc("err")
	return s + ", " + err + " := " + r + ".Number(" + task + ")\nif " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\nreturn " + tc.goType(t) + "{" + field + ": " + s + "}, nil\n"
}

// json.Num alone is a number too: its by-name pair (ParseNum, Wire) admits
// it as a wire type and keeps its spelling valid, and the codec writes the
// number, not Wire's string.
func (l *lowerer) wireNumEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, task, w, v string) {
	b.WriteString(numEncode(em, task, w, v+"."+l.typeDecl(t.(*check.Named).Obj, tc.site).fieldName["text"]))
}

func (l *lowerer) wireNumDecode(tc *typeCtx, b *strings.Builder, t check.Type, task, r, zero string, scope *namer) {
	b.WriteString(numDecode(tc, t, l.typeDecl(t.(*check.Named).Obj, tc.site).fieldName["text"], task, r, zero, scope))
}

func (l *lowerer) wireValueEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string, scope *namer) {
	x := scope.alloc("value")
	null, _ := tc.valuePart(t, "Null", "")
	boolean, bf := tc.valuePart(t, "Boolean", "Value")
	number, nf := tc.valuePart(t, "Num", "text")
	str, sf := tc.valuePart(t, "Str", "Value")
	array, af := tc.valuePart(t, "Array", "Items")
	object, of := tc.valuePart(t, "Object", "Members")
	member := wireUnder(valueFieldType(t, "Object", "Members")).(*check.Slice).Elem
	mtd := l.typeDecl(member.(*check.Named).Obj, tc.site)
	b.WriteString("switch " + x + " := " + v + ".(type) {\ncase " + tc.goType(null) + ":\n_ = " + x + "\n")
	if em.dir() == "json" {
		b.WriteString(appendConst(w, "null"))
	} else {
		b.WriteString(w + ".Null(" + task + ")\n")
	}
	b.WriteString("case " + tc.goType(boolean) + ":\n" + tc.wireEncodeWith(em, valueFieldType(t, "Boolean", "Value"), env, w, x+"."+bf) + "\n")
	b.WriteString("case " + tc.goType(number) + ":\n" + numEncode(em, task, w, x+"."+nf))
	b.WriteString("case " + tc.goType(str) + ":\n" + tc.wireEncodeWith(em, valueFieldType(t, "Str", "Value"), env, w, x+"."+sf) + "\n")
	b.WriteString("case " + tc.goType(array) + ":\n" + tc.wireEncodeWith(em, valueFieldType(t, "Array", "Items"), env, w, x+"."+af) + "\n")
	b.WriteString("case " + tc.goType(object) + ":\n")
	i, m := "_", scope.alloc("m")
	if em.indexed() {
		i = scope.alloc("i")
	}
	b.WriteString(em.open(false, "len("+x+"."+of+")", task, w))
	b.WriteString("for " + i + ", " + m + " := range " + x + "." + of + " {\n")
	b.WriteString(em.mapKey(tc, m+"."+mtd.fieldName["Key"], i, task, w) + tc.wireEncodeWith(em, t, env, w, m+"."+mtd.fieldName["Value"]) + "\n}\n")
	b.WriteString(em.close(false, task, w))
	b.WriteString("}\n")
}

func (l *lowerer) wireValueDecode(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	kind := func(name string) string { return tc.goType(check.PreludeType(name)) }
	null, _ := tc.valuePart(t, "Null", "")
	boolean, bf := tc.valuePart(t, "Boolean", "Value")
	number, nf := tc.valuePart(t, "Num", "text")
	str, sf := tc.valuePart(t, "Str", "Value")
	array, af := tc.valuePart(t, "Array", "Items")
	object, of := tc.valuePart(t, "Object", "Members")
	members := valueFieldType(t, "Object", "Members")
	member := wireUnder(members).(*check.Slice).Elem
	mtd := l.typeDecl(member.(*check.Named).Obj, tc.site)
	b.WriteString("switch " + r + ".Peek(" + task + ").(type) {\n")
	b.WriteString("case " + kind("WireNull") + ":\n" + r + ".TakeNull(" + task + ")\nreturn " + tc.goType(null) + "{}, nil\n")
	x := scope.alloc("x")
	b.WriteString("case " + kind("WireBool") + ":\n" + tc.wireDecode(valueFieldType(t, "Boolean", "Value"), env, r, x, zero, scope))
	b.WriteString("return " + tc.goType(boolean) + "{" + bf + ": " + x + "}, nil\n")
	b.WriteString("case " + kind("WireNumber") + ":\n" + numDecode(tc, number, nf, task, r, zero, scope))
	x = scope.alloc("x")
	b.WriteString("case " + kind("WireString") + ":\n" + tc.wireDecode(valueFieldType(t, "Str", "Value"), env, r, x, zero, scope))
	b.WriteString("return " + tc.goType(str) + "{" + sf + ": " + x + "}, nil\n")
	x = scope.alloc("x")
	b.WriteString("case " + kind("WireList") + ":\n" + tc.wireDecode(valueFieldType(t, "Array", "Items"), env, r, x, zero, scope))
	b.WriteString("return " + tc.goType(array) + "{" + af + ": " + x + "}, nil\n")
	out, e := scope.alloc("out"), scope.alloc("err")
	b.WriteString("case " + kind("WireObject") + ":\nif " + e + " := " + r + ".BeginObject(" + task + "); " + e + " != nil {\nreturn " + zero + ", " + e + "\n}\n")
	b.WriteString(out + " := " + tc.goType(members) + "{}\nfor " + r + ".More(" + task + ") {\n")
	k, x := scope.alloc("k"), scope.alloc("x")
	b.WriteString(k + " := " + r + ".Key(" + task + ")\n" + tc.wireDecode(t, env, r, x, zero, scope))
	b.WriteString(out + " = append(" + out + ", " + tc.goType(member) + "{" + mtd.fieldName["Key"] + ": " + k + ", " + mtd.fieldName["Value"] + ": " + x + "})\n}\n" + r + ".End(" + task + ")\n")
	b.WriteString("return " + tc.goType(object) + "{" + of + ": " + out + "}, nil\n}\n")
	b.WriteString("return " + zero + ", " + tc.wireKind(env, r, "a JSON value") + "\n")
}
