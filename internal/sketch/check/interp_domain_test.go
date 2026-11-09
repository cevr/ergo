package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestInterpolationDomain is the text-form rule of an interpolated part
// over its whole domain (check-v1 E6a A3; D-E6; Q5): every type of
// textTypes × every position of textPositions, enumerated, not sampled.
// The verdict of each program comes from textOracle, a reference written
// from D-E6 and Q5 that reads only the class each row states by hand,
// never the checker:
//
//   - clean iff the part's underlying type is a basic type;
//   - S109 naming #49 at the part iff it is data (a record, a union, a
//     variant, a tuple, a slice, a Map) or a type parameter;
//   - S108 at the part otherwise (a function, an operation type, a Task, a
//     pointer);
//   - exactly one diagnostic for a refused part, and nothing else.
func TestInterpolationDomain(t *testing.T) {
	n := 0
	for _, tt := range textTypes() {
		for _, pos := range textPositions {
			src, line, col := textProgram(tt, pos.body)
			_, diags := check.CheckSource(src)
			want := textOracle(tt, line, col)
			have := textGot(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("%s, %s: want %v, got %v\n\t%s\n%s", tt.name, pos.name, want, have, describe(diags), src)
				continue
			}
			for _, d := range diags {
				if d.Code == check.CodeOutside && !strings.Contains(d.Message, "#49") {
					t.Errorf("%s, %s: the S109 does not name #49: %s", tt.name, pos.name, d)
				}
			}
			n++
		}
	}
	if want := len(textTypes()) * len(textPositions); n != want {
		t.Errorf("%d of %d programs passed", n, want)
	}
}

// textClass is the class of a part's type that the oracle reads.
type textClass int

const (
	textBasic     textClass = iota // the underlying type is a basic type
	textData                       // a record, union, variant, tuple, slice or Map
	textTypeParam                  // a type parameter, whatever its constraint
	textNone                       // a type that never has a text form
)

// textType is one row of the domain: the part's type as a parameter type,
// the type parameters its function declares, the part expression (v
// unless set), the class, and statements that bind v in a body with no
// parameters ("" when no such body can build a value of the type: a
// pointer is only a parameter type, and a type parameter is only in scope
// in a generic declaration).
type textType struct {
	name, typ, tparams, part string
	class                    textClass
	mk                       string
}

// textDecls are the declarations every program shares. Dk is a type
// defined over the basic type k, for every basic kind.
func textDecls() string {
	var b strings.Builder
	b.WriteString(`import task

type Fault {}
type Rec { N int }
type Empty {}
type Dot {}
type Line { N int }
type Shape = Dot | Line
type Store {
	Get() string,
}
type Mem {}
type Box[T] { V T }
type DRec Rec
type DSlice []int
type DTuple (int, string)
type DFn func(n int) int
type DDint Dint
type DDstring Dstring

func Get(_ Mem) string {
	"m"
}

func start() Task[int, Fault] {
	task.Go() { 1 }
}

func g(s string) int fails Fault {
	len(s)
}
`)
	for _, k := range check.Typ[1:] {
		fmt.Fprintf(&b, "type D%s %s\n", k.Name, k.Name)
	}
	return b.String()
}

// textTypes is the domain: every basic kind, a type defined over each, a
// type defined over a defined type over int and over string, a basic
// field of a generic instance, then every kind of type with no text form.
func textTypes() []textType {
	var out []textType
	for _, k := range check.Typ[1:] {
		out = append(out, textType{name: k.Name, typ: k.Name, class: textBasic, mk: "let v " + k.Name})
	}
	for _, k := range check.Typ[1:] {
		d := "D" + k.Name
		out = append(out, textType{name: "a type over " + k.Name, typ: d, class: textBasic, mk: "let v " + d})
	}
	return append(out,
		textType{name: "a type over a type over int", typ: "DDint", class: textBasic, mk: "let v DDint"},
		textType{name: "a type over a type over string", typ: "DDstring", class: textBasic, mk: "let v DDstring"},
		textType{name: "an int field of Box[int]", typ: "Box[int]", part: "v.V", class: textBasic, mk: "let v = Box[int]{V: 1}"},
		textType{name: "a record", typ: "Rec", class: textData, mk: "let v = Rec{N: 1}"},
		textType{name: "an empty record", typ: "Empty", class: textData, mk: "let v Empty"},
		textType{name: "a union", typ: "Shape", class: textData, mk: "let v Shape = Dot"},
		textType{name: "a variant", typ: "Line", class: textData, mk: "let v = Line{N: 1}"},
		textType{name: "a tuple", typ: "(int, string)", class: textData, mk: "let v (int, string)"},
		textType{name: "a slice", typ: "[]int", class: textData, mk: "let v []int"},
		textType{name: "a Map", typ: "Map[string, int]", class: textData, mk: "let v Map[string, int]"},
		textType{name: "Option[int]", typ: "Option[int]", class: textData, mk: "let v Option[int] = None"},
		textType{name: "a type over a record", typ: "DRec", class: textData, mk: "let v = DRec{N: 1}"},
		textType{name: "a type over a slice", typ: "DSlice", class: textData, mk: "let v DSlice"},
		textType{name: "a type over a tuple", typ: "DTuple", class: textData, mk: "let v DTuple"},
		textType{name: "a function type", typ: "escaping func(n int) int", class: textNone, mk: "let v = func(n int) int { n }"},
		// A non-escaping parameter: the text-form S108 alone, with no
		// second, non-escaping S108 for the same part (escaping.go).
		textType{name: "a non-escaping function type", typ: "func(n int) int", class: textNone},
		textType{name: "a type over a function type", typ: "DFn", class: textNone, mk: "let v DFn = func(n int) int { n }"},
		textType{name: "an operation-type value", typ: "Store", class: textNone, mk: "let v Store = Mem"},
		textType{name: "a Task", typ: "Task[int, Fault]", class: textNone, mk: "let v = start()"},
		textType{name: "a pointer to a record", typ: "*Rec", class: textNone},
		textType{name: "a type parameter", typ: "T", tparams: "[T]", class: textTypeParam},
		textType{name: "an ordered type parameter", typ: "T", tparams: "[T ordered]", class: textTypeParam},
		textType{name: "a comparable type parameter", typ: "T", tparams: "[T comparable]", class: textTypeParam},
	)
}

// textPositions are the places of the part: $TP is the type parameter
// list, $T the part's type and $V the part. The part is the only part,
// a middle part between text, a part of a nested interpolation, and a
// part of a string that a try expression's call takes.
var textPositions = []struct{ name, body string }{
	{"the only part", "func f$TP(v $T) string {\n\t\"\\($V)\"\n}\n"},
	{"a middle part", "func f$TP(v $T) string {\n\t\"a \\($V) b\"\n}\n"},
	{"a nested interpolation", "func f$TP(v $T) string {\n\t\"x \\(\"y \\($V) z\") w\"\n}\n"},
	{"a string under try", "func f$TP(v $T) int fails Fault {\n\ttry g(\"a \\($V) b\")\n}\n"},
}

// textProgram is the shared declarations and body with tt substituted,
// and the line and column (1-based) of the part.
func textProgram(tt textType, body string) (src string, line, col int) {
	part := tt.part
	if part == "" {
		part = "v"
	}
	body = strings.NewReplacer("$TP", tt.tparams, "$T", tt.typ).Replace(body)
	src = textDecls() + "\n" + body
	at := strings.Index(src, "\\($V)")
	if at < 0 {
		panic("no part in " + body)
	}
	at += len(`\(`)
	src = src[:at] + part + src[at+len("$V"):]
	line = strings.Count(src[:at], "\n") + 1
	col = at - strings.LastIndex(src[:at], "\n")
	return src, line, col
}

// textOracle is the reference verdict of D-E6 and Q5 for a part of type tt
// at line:col, as CODE@LINE:COL.
func textOracle(tt textType, line, col int) []string {
	switch tt.class {
	case textData, textTypeParam:
		return []string{fmt.Sprintf("S109@%d:%d", line, col)}
	case textNone:
		return []string{fmt.Sprintf("S108@%d:%d", line, col)}
	}
	return nil
}

// textGot is the diagnostics as CODE@LINE:COL, sorted.
func textGot(diags []check.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%s@%d:%d", d.Code, d.Line, d.Column))
	}
	sort.Strings(out)
	return out
}
