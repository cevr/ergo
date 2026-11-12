package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// Bare unions and ordered members implement the public wire forms.
// No variant, field or package name selects their event representation.
// Prior art: serde's untagged enums require disjoint representations;
// Jason's ordered objects retain member order and repeated keys.

// numEncode writes a number spelling supplied by a NumberText form.
func numEncode(em wireEvents, task, w, f string) string {
	if em.dir() == "json" {
		return w + ".B = append(" + w + ".B, " + f + "...)\n"
	}
	return w + ".Number(" + task + ", " + f + ")\n"
}

func (l *lowerer) wireNumEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string) {
	p := l.info.WirePairOf(t)
	spelling := "string(" + l.wireCallFunc(p.Wire, env, tc.site, v) + ")"
	b.WriteString(numEncode(em, task, w, spelling))
}

func (l *lowerer) wireNumDecode(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	p := l.info.WirePairOf(t)
	x, err := scope.alloc("number"), scope.alloc("err")
	b.WriteString(x + ", " + err + " := " + r + ".Number(" + task + ")\nif " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n")
	b.WriteString(l.wireParseReturn(p.Parse, env, tc.site, tc.goType(p.Base)+"("+x+")"))
}

// barePart returns a variant's transparent payload, or nil when the
// variant supplies its own wire form or is the empty/null alternative.
func barePart(v check.Type) *check.Field {
	if check.WireFormOf(v) != check.WireDefault {
		return nil
	}
	s := wireUnder(v).(*check.Struct)
	if len(s.Fields) == 1 {
		return s.Fields[0]
	}
	return nil
}

func (l *lowerer) wireValueEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string, scope *namer) {
	if fu := l.flatUnionOf(t); fu != nil {
		flatBail(tc.site, fu)
	}
	x := scope.alloc("value")
	b.WriteString("switch " + x + " := " + v + ".(type) {\n")
	for _, variant := range wireUnder(t).(*check.Union).Variants {
		b.WriteString("case " + tc.goType(variant) + ":\n")
		if check.WireVariantKind(variant) == "WireNull" {
			b.WriteString("_ = " + x + "\n")
			if em.dir() == "json" {
				b.WriteString(appendConst(w, "null"))
			} else {
				b.WriteString(w + ".Null(" + task + ")\n")
			}
		} else if f := barePart(variant); f != nil {
			field := l.typeDecl(variant.(*check.Named).Obj, tc.site).fieldName[f.Name]
			b.WriteString(tc.wireEncodeWith(em, f.Type, env, w, x+"."+field) + "\n")
		} else {
			b.WriteString(tc.wireEncodeWith(em, variant, env, w, x) + "\n")
		}
	}
	b.WriteString("}\n")
}

func (l *lowerer) wireValueDecode(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	if fu := l.flatUnionOf(t); fu != nil {
		flatBail(tc.site, fu)
	}
	b.WriteString("switch " + r + ".Peek(" + task + ").(type) {\n")
	for _, variant := range wireUnder(t).(*check.Union).Variants {
		kind := check.WireVariantKind(variant)
		b.WriteString("case " + tc.goType(check.PreludeType(kind)) + ":\n")
		if kind == "WireNull" {
			b.WriteString(r + ".TakeNull(" + task + ")\nreturn " + tc.goType(variant) + "{}, nil\n")
			continue
		}
		x := scope.alloc("x")
		if f := barePart(variant); f != nil {
			b.WriteString(tc.wireDecode(f.Type, env, r, x, zero, scope))
			b.WriteString("return " + tc.wrapperValue(variant, f, x) + ", nil\n")
		} else {
			b.WriteString(tc.wireDecode(variant, env, r, x, zero, scope))
			b.WriteString("return " + x + ", nil\n")
		}
	}
	b.WriteString("}\nreturn " + zero + ", " + tc.wireKind(env, r, "a value of the bare union") + "\n")
}

// memberParts reads the admitted single-slice / key-value entry structure.
func (tc *typeCtx) memberParts(t check.Type) (outer *check.Field, list *check.Slice, entry *check.Struct, td *typeDecl) {
	outer = wireUnder(t).(*check.Struct).Fields[0]
	list = wireUnder(outer.Type).(*check.Slice)
	entry = wireUnder(list.Elem).(*check.Struct)
	td = tc.l.typeDecl(list.Elem.(*check.Named).Obj, tc.site)
	return
}

func (l *lowerer) wireMembersEncode(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string, scope *namer) {
	outer, _, entry, td := tc.memberParts(t)
	rec := v + "." + l.typeDecl(t.(*check.Named).Obj, tc.site).fieldName[outer.Name]
	i, m := "_", scope.alloc("member")
	if em.indexed() {
		i = scope.alloc("i")
	}
	b.WriteString(em.open(false, "len("+rec+")", task, w))
	b.WriteString("for " + i + ", " + m + " := range " + rec + " {\n")
	b.WriteString(em.mapKey(tc, "string("+m+"."+td.fieldName[entry.Fields[0].Name]+")", i, task, w))
	b.WriteString(tc.wireEncodeWith(em, entry.Fields[1].Type, env, w, m+"."+td.fieldName[entry.Fields[1].Name]) + "\n}\n")
	b.WriteString(em.close(false, task, w))
}

func (l *lowerer) wireMembersDecode(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	outer, list, entry, td := tc.memberParts(t)
	e, out, key, x := scope.alloc("err"), scope.alloc("members"), scope.alloc("key"), scope.alloc("value")
	b.WriteString("if " + e + " := " + r + ".BeginObject(" + task + "); " + e + " != nil {\nreturn " + zero + ", " + e + "\n}\n")
	b.WriteString(out + " := " + tc.goType(outer.Type) + "{}\nfor " + r + ".More(" + task + ") {\n")
	b.WriteString(key + " := " + r + ".Key(" + task + ")\n")
	b.WriteString(tc.wireDecode(entry.Fields[1].Type, env, r, x, zero, scope))
	b.WriteString(out + " = append(" + out + ", " + tc.goType(list.Elem) + "{" + td.fieldName[entry.Fields[0].Name] + ": " + tc.goType(entry.Fields[0].Type) + "(" + key + "), " + td.fieldName[entry.Fields[1].Name] + ": " + x + "})\n}\n" + r + ".End(" + task + ")\n")
	b.WriteString("return " + tc.wrapperValue(t, outer, out) + ", nil\n")
}
