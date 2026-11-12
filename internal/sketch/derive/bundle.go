package derive

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The bundle (patterns.md, The pattern bundle; Coherence 1). A derive's
// declarations are generated as Ergo text, parsed by the ordinary parser
// and placed after the derived type, so the checker, the lowering and
// inspect read them as written declarations. A refinement plan generates
// `type N B where lo <= it && it <= hi`, so its bundle is the parser's one
// refinement derivation (syntax/refine.go).

// Names of a derived type's functions (Derivation 4). The matcher, find
// driver and builder are internal: lower-case, after the type.
func parseName(t string) string { return prefixed("parse", t) }
func findName(t string) string  { return prefixed("find", t) }
func matchName(t string) string { return "match" + upperFirst(t) }
func scanName(t string) string  { return "scan" + upperFirst(t) }
func buildName(t string) string { return "build" + upperFirst(t) }

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// prefixed is ParseT for an upper-case T and parseT for a lower-case t.
func prefixed(verb, t string) string {
	r, _ := utf8.DecodeRuneInString(t)
	if unicode.IsUpper(r) {
		return upperFirst(verb) + t
	}
	return verb + upperFirst(t)
}

// patternBundle is the text of a pattern's declarations: its types, its
// parse and find functions, the builder of its value from a match, and
// the two matcher functions whose Go the lowering emits from the plan
// (lower/pattern.go); their Ergo bodies are placeholders it never emits.
func patternBundle(t string, shape *Shape, slots int) string {
	var b strings.Builder
	result := fmt.Sprintf("[%d]int", slots)
	if shape.Repeated {
		result = "[]int"
	}
	// Types.
	if shape.Union {
		var vs []string
		for _, v := range shape.Variants {
			if v.Record == nil {
				vs = append(vs, v.Name+" { Value string }")
			} else {
				vs = append(vs, v.Name+" "+fieldsText(v.Record))
				nestedTypes(&b, v.Record, false)
			}
		}
		fmt.Fprintf(&b, "type %s = %s\n\n", t, strings.Join(vs, " | "))
	} else {
		nestedTypes(&b, shape.Record, true)
	}
	fmt.Fprintf(&b, "func %s(s string) %s fails NoMatch {\n\tlet r = %s(s)\n\tif r[0] == 0 {\n\t\tNoMatch{Type: %q, At: r[1]}\n\t} else {\n\t\t%s(s, r)\n\t}\n}\n\n",
		parseName(t), t, matchName(t), t, buildName(t))
	fmt.Fprintf(&b, "func %s(s string, from int) Option[(%s, int, int)] {\n\tlet r = %s(s, from)\n\tif r[0] == 0 {\n\t\tNone\n\t} else {\n\t\tSome[(%s, int, int)]{Value: (%s(s, r), r[1], r[2])}\n\t}\n}\n\n",
		findName(t), t, scanName(t), t, buildName(t))
	fmt.Fprintf(&b, "func %s(s string) %s { %s{} }\n\n", matchName(t), result, result)
	fmt.Fprintf(&b, "func %s(s string, from int) %s { %s{} }\n\n", scanName(t), result, result)
	fmt.Fprintf(&b, "func %s(s string, r %s) %s {\n", buildName(t), result, t)
	if shape.Repeated {
		eventBuilder(&b, shape)
	} else {
		b.WriteString("\t" + fixedValue(shape) + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func fieldsText(r *Record) string {
	var fs []string
	for _, f := range r.Fields {
		fs = append(fs, f.Name+" "+fieldType(f))
	}
	return "{ " + strings.Join(fs, ", ") + " }"
}

func elemType(f *Field) string {
	if f.Record == nil {
		return "string"
	}
	return f.Record.Type
}

func fieldType(f *Field) string {
	switch f.Count {
	case Opt:
		return "Option[" + elemType(f) + "]"
	case Many:
		return "[]" + elemType(f)
	}
	return elemType(f)
}

// nestedTypes writes the record types of r's nested captures, and r's own
// type when self is set.
func nestedTypes(b *strings.Builder, r *Record, self bool) {
	for _, f := range r.Fields {
		if f.Record != nil {
			nestedTypes(b, f.Record, true)
		}
	}
	if self {
		fmt.Fprintf(b, "type %s %s\n\n", r.Type, fieldsText(r))
	}
}

// Slots of the capture with tag t in a fixed result (regexpplan, slots.go).
func open(t int) string  { return fmt.Sprintf("r[%d]", 1+2*t) }
func close(t int) string { return fmt.Sprintf("r[%d]", 2+2*t) }

func fixedValue(shape *Shape) string {
	if !shape.Union {
		return recordValue(shape.Record, fixedField)
	}
	var b strings.Builder
	for i, v := range shape.Variants {
		value := v.Name + "{Value: s[" + open(v.Tag) + ":" + close(v.Tag) + "]}"
		if v.Record != nil {
			value = recordValue(&Record{Type: v.Name, Fields: v.Record.Fields}, fixedField)
		}
		switch {
		case len(shape.Variants) == 1:
			return value
		case i == 0:
			fmt.Fprintf(&b, "if %s >= 0 { %s }", open(v.Tag), value)
		case i == len(shape.Variants)-1:
			fmt.Fprintf(&b, " else { %s }", value)
		default:
			fmt.Fprintf(&b, " else if %s >= 0 { %s }", open(v.Tag), value)
		}
	}
	return b.String()
}

func recordValue(r *Record, field func(*Field) string) string {
	if len(r.Fields) == 0 {
		return r.Type // a record with no fields is a value by its name alone
	}
	var fs []string
	for _, f := range r.Fields {
		fs = append(fs, f.Name+": "+field(f))
	}
	return r.Type + "{" + strings.Join(fs, ", ") + "}"
}

func fixedField(f *Field) string {
	value := "s[" + open(f.Tag) + ":" + close(f.Tag) + "]"
	if f.Record != nil {
		value = recordValue(f.Record, fixedField)
	}
	if f.Count == Opt {
		return fmt.Sprintf("if %s < 0 { None } else { Some[%s]{Value: %s} }", open(f.Tag), elemType(f), value)
	}
	return value
}

// The event builder keeps, for each capture with tag t, its open and close
// positions o<t> and c<t> and, for a repeated capture, the list l<t> of
// its occurrences. A capture's open resets the captures of its scope; a
// repeated capture's close appends its value.
func eventField(f *Field) string {
	o, c := fmt.Sprintf("o%d", f.Tag), fmt.Sprintf("c%d", f.Tag)
	if f.Count == Many {
		return fmt.Sprintf("l%d", f.Tag)
	}
	value := "s[" + o + ":" + c + "]"
	if f.Record != nil {
		value = recordValue(f.Record, eventField)
	}
	if f.Count == Opt {
		return fmt.Sprintf("if %s < 0 { None } else { Some[%s]{Value: %s} }", o, elemType(f), value)
	}
	return value
}

func eventBuilder(b *strings.Builder, shape *Shape) {
	var all []*Field
	scopes := map[int][]*Field{} // a capture's tag -> the fields of its scope
	var visit func(f *Field)
	visit = func(f *Field) {
		all = append(all, f)
		if f.Record != nil {
			scopes[f.Tag] = f.Record.Fields
			for _, g := range f.Record.Fields {
				visit(g)
			}
		}
	}
	top := shape.Variants
	if !shape.Union {
		top = shape.Record.Fields
	}
	for _, f := range top {
		visit(f)
	}
	// A capture's open position is read for a string value, an Option or
	// a union variant; its close position for a string value.
	variant := map[int]bool{}
	if shape.Union {
		for _, v := range shape.Variants {
			variant[v.Tag] = true
		}
	}
	needO := func(f *Field) bool { return f.Record == nil || f.Count == Opt || variant[f.Tag] }
	needC := func(f *Field) bool { return f.Record == nil }
	for _, f := range all {
		if needO(f) {
			fmt.Fprintf(b, "\tlet mut o%d = -1\n", f.Tag)
		}
		if needC(f) {
			fmt.Fprintf(b, "\tlet mut c%d = -1\n", f.Tag)
		}
		if f.Count == Many {
			fmt.Fprintf(b, "\tlet mut l%d = []%s{}\n", f.Tag, elemType(f))
		}
	}
	b.WriteString("\tlet mut i = 3\n\tfor i < len(r) {\n\t\tlet tag = r[i]\n\t\tlet pos = r[i + 1]\n")
	for k, f := range all {
		kw := "} else if"
		if k == 0 {
			kw = "if"
		}
		fmt.Fprintf(b, "\t\t%s tag == %d {\n", kw, f.Tag)
		if needO(f) {
			fmt.Fprintf(b, "\t\t\to%d = pos\n", f.Tag)
		}
		for _, g := range scopes[f.Tag] {
			if needO(g) {
				fmt.Fprintf(b, "\t\t\to%d = -1\n", g.Tag)
			}
			if g.Count == Many {
				fmt.Fprintf(b, "\t\t\tl%d = []%s{}\n", g.Tag, elemType(g))
			}
		}
		fmt.Fprintf(b, "\t\t} else if tag == %d {\n", -f.Tag)
		if needC(f) {
			fmt.Fprintf(b, "\t\t\tc%d = pos\n", f.Tag)
		}
		if f.Count == Many {
			one := *f
			one.Count = One
			fmt.Fprintf(b, "\t\t\tl%d = l%d.Append(%s)\n", f.Tag, f.Tag, eventField(&one))
		}
	}
	b.WriteString("\t\t}\n\t\ti = i + 2\n\t}\n")
	if !shape.Union {
		b.WriteString("\t" + recordValue(shape.Record, eventField) + "\n")
		return
	}
	for i, v := range shape.Variants {
		one := *v
		value := v.Name + "{Value: s[" + fmt.Sprintf("o%d:c%d", v.Tag, v.Tag) + "]}"
		if v.Record != nil {
			value = recordValue(&Record{Type: v.Name, Fields: one.Record.Fields}, eventField)
		}
		switch {
		case i == 0 && len(shape.Variants) == 1:
			b.WriteString("\t" + value + "\n")
		case i == 0:
			fmt.Fprintf(b, "\tif o%d >= 0 {\n\t\t%s\n", v.Tag, value)
		case i == len(shape.Variants)-1:
			fmt.Fprintf(b, "\t} else {\n\t\t%s\n\t}\n", value)
		default:
			fmt.Fprintf(b, "\t} else if o%d >= 0 {\n\t\t%s\n", v.Tag, value)
		}
	}
}

// refineBundle is the refinement a Refine plan derives: the written
// `where` form, so both share one derivation (Coherence 1).
func refineBundle(t, base string, length bool, lo, hi int64) string {
	it := "it"
	if length {
		it = "len(it)"
	}
	return fmt.Sprintf("type %s %s where %d <= %s && %s <= %d\n", t, base, lo, it, it, hi)
}
