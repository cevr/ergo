package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// tsBasic is a basic type's class in the reference model of type sets.
type tsBasic struct {
	integer, float, str, boolean, duration bool
}

var tsBasics = map[string]tsBasic{
	"int": {integer: true}, "int8": {integer: true}, "int16": {integer: true}, "int32": {integer: true}, "int64": {integer: true},
	"uint": {integer: true}, "uint8": {integer: true}, "uint16": {integer: true}, "uint32": {integer: true}, "uint64": {integer: true},
	"float32": {float: true}, "float64": {float: true},
	"string": {str: true}, "bool": {boolean: true}, "duration": {duration: true},
}

var tsBasicNames = func() []string {
	var out []string
	for k := range tsBasics {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}()

// tsArg is an argument type of the property: a basic type, a type defined
// over one (refined or not), or a record, union or tuple.
type tsArg struct {
	name       string
	base       string // its basic underlying type, "" for none
	defined    bool
	refined    bool
	comparable bool
}

const tsDecls = `type DInt int
type DI64 int64
type DU8 uint8
type DF64 float64
type DStr string
type DBool bool
type DDur duration
type Port int where 0 <= it && it <= 9
type Rec { N int }
type RecS { Xs []int }
type Un = UA{} | UB{}
type Tup (int, string)
`

var tsArgs = func() []tsArg {
	var out []tsArg
	for _, b := range tsBasicNames {
		out = append(out, tsArg{name: b, base: b, comparable: true})
	}
	return append(out,
		tsArg{name: "DInt", base: "int", defined: true, comparable: true},
		tsArg{name: "DI64", base: "int64", defined: true, comparable: true},
		tsArg{name: "DU8", base: "uint8", defined: true, comparable: true},
		tsArg{name: "DF64", base: "float64", defined: true, comparable: true},
		tsArg{name: "DStr", base: "string", defined: true, comparable: true},
		tsArg{name: "DBool", base: "bool", defined: true, comparable: true},
		tsArg{name: "DDur", base: "duration", defined: true, comparable: true},
		tsArg{name: "Port", base: "int", defined: true, refined: true, comparable: true},
		tsArg{name: "Rec", comparable: true},
		tsArg{name: "RecS"},
		tsArg{name: "Un", comparable: true},
		tsArg{name: "Tup", comparable: true},
	)
}()

// tsTerm is a term of a generated set: a basic type, ~ or bare, or the
// term comparable (base "").
type tsTerm struct {
	tilde bool
	base  string
}

func (t tsTerm) String() string {
	switch {
	case t.base == "":
		return "comparable"
	case t.tilde:
		return "~" + t.base
	}
	return t.base
}

// tsAdmits is the reference's membership: ~K admits a type over K that is
// not refined, a bare K admits K alone, comparable admits the comparable
// types.
func tsAdmits(set []tsTerm, a tsArg) bool {
	for _, t := range set {
		switch {
		case t.base == "":
			if a.comparable {
				return true
			}
		case t.tilde:
			if a.base == t.base && !a.refined {
				return true
			}
		default:
			if a.base == t.base && !a.defined {
				return true
			}
		}
	}
	return false
}

// tsOps are the operators the property applies to a type parameter, each
// with the reference's rule for one basic type and the body it checks.
var tsOps = []struct {
	name string
	has  func(tsBasic) bool
	body string // over a and b of type T
	res  string
}{
	{"+", func(b tsBasic) bool { return b.integer || b.float || b.str || b.duration }, "a + b", "T"},
	{"-", func(b tsBasic) bool { return b.integer || b.float || b.duration }, "a - b", "T"},
	{"*", func(b tsBasic) bool { return b.integer || b.float }, "a * b", "T"},
	{"/", func(b tsBasic) bool { return b.integer || b.float }, "a / b", "T"},
	{"%", func(b tsBasic) bool { return b.integer }, "a % b", "T"},
	{"&", func(b tsBasic) bool { return b.integer }, "a & b", "T"},
	{"<", func(b tsBasic) bool { return b.integer || b.float || b.str || b.duration }, "a < b", "bool"},
	{"==", func(tsBasic) bool { return true }, "a == b", "bool"},
	{"unary -", func(b tsBasic) bool { return b.integer || b.float || b.duration }, "-a", "T"},
}

// tsOpAllowed is the reference's operator rule: every term has it, and
// the term comparable gives == alone.
func tsOpAllowed(set []tsTerm, op int) bool {
	for _, t := range set {
		if t.base == "" {
			if tsOps[op].name != "==" {
				return false
			}
			continue
		}
		if !tsOps[op].has(tsBasics[t.base]) {
			return false
		}
	}
	return true
}

// The prelude's sets as the deleted built-in table had them (check/
// typeops.go inSet before C1): the reference the prelude's declarations
// must agree with.
var (
	tsOrderedOld = func(a tsArg) bool {
		b := tsBasics[a.base]
		return a.base != "" && !a.refined && (b.integer || b.float || b.str || b.duration)
	}
	tsNumberOld = func(a tsArg) bool {
		b := tsBasics[a.base]
		return a.base != "" && !a.refined && (b.integer || b.float)
	}
	tsOrderedOps = map[string]bool{"+": true, "<": true, "==": true}
	tsNumberOps  = map[string]bool{"+": true, "-": true, "*": true, "/": true, "<": true, "==": true, "unary -": true}
)

// tsCoversOrdered reports whether the set covers every term of ordered
// (each ~K, uintptr among them), the reference's rule for a type
// parameter under ordered as an argument.
func tsCoversOrdered(set []tsTerm) bool {
	kinds := []string{"uintptr"}
	for _, k := range tsBasicNames {
		if b := tsBasics[k]; b.integer || b.float || b.str || b.duration {
			kinds = append(kinds, k)
		}
	}
	for _, k := range kinds {
		covered := false
		for _, t := range set {
			if t.base == "" || t.tilde && t.base == k {
				covered = true
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// tsRow is one checked line of a generated program and the verdict the
// reference gives it: true when the line must check.
type tsRow struct {
	what string
	ok   bool
}

// tsProgram is the seeded program: a generated type set, a membership row
// per argument type against it and against ordered and number, a row per
// operator under it, and a row that writes it as a value's type.
func tsProgram(r *rand.Rand) (string, map[int]tsRow, []tsTerm) {
	n := 1 + r.Intn(6)
	var set []tsTerm
	for range n {
		if r.Intn(8) == 0 {
			set = append(set, tsTerm{})
			continue
		}
		set = append(set, tsTerm{tilde: r.Intn(2) == 0, base: tsBasicNames[r.Intn(len(tsBasicNames))]})
	}
	terms := make([]string, len(set))
	for i, t := range set {
		terms[i] = t.String()
	}
	var b strings.Builder
	rows := map[int]tsRow{}
	line := 1
	emit := func(s string, row *tsRow) {
		if row != nil {
			rows[line] = *row
		}
		b.WriteString(s)
		b.WriteString("\n")
		line += strings.Count(s, "\n") + 1
	}
	emit("type Set { "+strings.Join(terms, " | ")+" }", nil)
	emit(strings.TrimSuffix(tsDecls, "\n"), nil)
	emit("func id[T Set](x T) T { x }", nil)
	emit("func idO[T ordered](x T) T { x }", nil)
	emit("func idN[T number](x T) T { x }", nil)
	for i, a := range tsArgs {
		emit(fmt.Sprintf("func m%d(v %s) %s { id(v) }", i, a.name, a.name), &tsRow{"Set admits " + a.name, tsAdmits(set, a)})
		emit(fmt.Sprintf("func o%d(v %s) %s { idO(v) }", i, a.name, a.name), &tsRow{"ordered admits " + a.name, tsOrderedOld(a)})
		emit(fmt.Sprintf("func n%d(v %s) %s { idN(v) }", i, a.name, a.name), &tsRow{"number admits " + a.name, tsNumberOld(a)})
	}
	emit("func p0[U ordered](v U) U { id(v) }", &tsRow{"Set admits a type parameter under ordered", tsCoversOrdered(set)})
	emit("func p1[U Set](v U) U { id(v) }", &tsRow{"Set admits a type parameter under Set", true})
	for i, op := range tsOps {
		params := "a T, b T"
		if op.name == "unary -" {
			params = "a T"
		}
		emit(fmt.Sprintf("func s%d[T Set](%s) %s { %s }", i, params, op.res, op.body), &tsRow{"Set gives " + op.name, tsOpAllowed(set, i)})
		emit(fmt.Sprintf("func so%d[T ordered](%s) %s { %s }", i, params, op.res, op.body), &tsRow{"ordered gives " + op.name, tsOrderedOps[op.name]})
		emit(fmt.Sprintf("func sn%d[T number](%s) %s { %s }", i, params, op.res, op.body), &tsRow{"number gives " + op.name, tsNumberOps[op.name]})
	}
	emit("func hold(c Set) {}", &tsRow{"Set as a value's type", false})
	emit("func main() {}", nil)
	return b.String(), rows, set
}

// TestTypeSetsAgree is the type-set rule's property (types.md, Generics 2;
// CONSTRAINTS C1): for seeded sets of 1 to 6 terms over the basic types,
// ~ or bare, with the term comparable, the checker's verdict for each
// argument type's membership (basic, defined, refined, record, union,
// tuple), for each operator applied under the set, for a type parameter
// as an argument and for the set as a value's type equals a small
// reference model; and the prelude's ordered and number give the verdicts
// the deleted built-in table gave, for every argument type and operator.
// A failing seed prints the Ergo source, both verdicts and the replay
// command (ERGO_TYPESET_SEED).
func TestTypeSetsAgree(t *testing.T) {
	seeds, first := 200, int64(1)
	if s := os.Getenv("ERGO_TYPESET_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	refused, admitted := 0, 0
	for seed := first; seed < first+int64(seeds); seed++ {
		src, rows, set := tsProgram(rand.New(rand.NewSource(seed)))
		_, ds := check.CheckSource(src)
		bad := map[int][]string{}
		for _, d := range ds {
			if _, ok := rows[d.Line]; !ok {
				t.Fatalf("seed %d (ERGO_TYPESET_SEED=%d go test -run TestTypeSetsAgree ./internal/sketch/check): a diagnostic on a line no row owns: %s %d:%d %s\n%s", seed, seed, d.Code, d.Line, d.Column, d.Message, src)
			}
			bad[d.Line] = append(bad[d.Line], d.Code+" "+d.Message)
		}
		for line, row := range rows {
			got := len(bad[line]) == 0
			if got != row.ok {
				t.Fatalf("seed %d (ERGO_TYPESET_SEED=%d go test -run TestTypeSetsAgree ./internal/sketch/check): set %v, line %d, %s: the checker says %v (%v), the reference %v\n%s", seed, seed, set, line, row.what, got, bad[line], row.ok, src)
			}
			if row.ok {
				admitted++
			} else {
				refused++
			}
		}
	}
	if seeds > 1 && (refused == 0 || admitted == 0) {
		t.Fatalf("the property compared %d refusals and %d admissions: it must see both", refused, admitted)
	}
}
