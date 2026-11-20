package check_test

import (
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
)

// The binder-name rule (fuzz-17d6): every identifier has one meaning, and
// typing (Info) and the reference walk agree on it. A binder takes a local
// value even when it shares its name with a package-level declaration,
// unless the spec makes the name a pattern (a type or constant test) or
// refuses the binder. TestBinderNamesAgree crosses every binder form with
// every class of name, and checks three things against an oracle written
// from the spec rules, never from the checker: what Info and the walk make
// of the identifier, the codes at the binder, and the codes elsewhere (D3:
// a refused binder adds no cascade).
//
// The binder forms are derived from the syntax AST as TestLawScopeGuard
// derives them: the binder fields are lawScopeDomain less lawNoBinder.

// binderKind is the rule a binder form follows.
type binderKind int

const (
	// A parameter, a name of a multi-name let or an as name: always a
	// binder. A local may reuse a package-level name other than a prelude
	// name (D-D2, diagnostics.md); a prelude name is S108 at the binder,
	// which still binds (D-D3).
	plainBinder binderKind = iota
	// A let's or a for's pattern name in a body (rows.md Totality 6,
	// types.md Scope 1): a constant is a pattern; a package type, function
	// or context name binds (D-D2; L5); a prelude name binds with D-D3's
	// S108.
	letForPattern
	// A match pattern name (rows.md Totality 1): a type or constant is that
	// pattern; a function name is S108 and binds a placeholder; any other
	// name binds. A name inside a catch arm's record pattern follows the
	// same rule: the arms are match arms over the failure (rows.md Catch 7),
	// and rule 1's function and package-value S108 applies to catch arms
	// (Totality 6).
	matchPattern
	// A catch arm's lone name (rows.md Catch 7): the arms are match arms
	// over the failure, so Totality 1 holds, and a name that binds takes
	// the whole failure (`case err:`). Capitalisation changes nothing: a
	// capitalised name that names no type, constant or capability binds,
	// as in a match.
	catchRest
)

// binderForm is one program that puts a binder named $B in a binder field.
// The first $B is the binder; the others read it.
type binderForm struct {
	name string
	kind binderKind
	src  string
}

// binderNameDecls are the declarations every binder program shares: the
// package names a binder can reuse, in a lower-case and a capitalised
// spelling (tn and Tn, fx and Fx, pv and Pv, cx and Cx), a union to match
// on, and two fallible functions to catch: risky fails with a record of no
// field, risky2 with a record of one field.
const binderNameDecls = `type Bang {}

func risky(n int) int fails Bang {
	if n < 0 {
		return Bang
	}
	n
}

type Boom { N int }

func risky2(n int) int fails Boom {
	if n < 0 {
		return Boom{N: n}
	}
	n
}

type Dot {}
type Line { N int }
type Shape = Dot | Line
type tn {}
type Tn {}
func fx() {}
func Fx() {}
let pv = 5
let Pv = 5
let cx = context {}
let Cx = context {}
`

// binderBadDecl declares name, a package type whose declaration fails.
func binderBadDecl(name string) string { return "type " + name + "(fx)\n" }

// binderForms are the programs of each binder field. Every value the
// binder stands for is an int, so the prelude type int is the type at the
// binder in every match form.
var binderForms = map[string][]binderForm{
	"Field.Names": {
		{"a function literal's parameter", plainBinder, "func host(v int) {\n\tlet g = func($B int) { let _ = $B }\n\tg(v)\n}"},
		{"a function's parameter", plainBinder, "func host($B int) {\n\tlet _ = $B\n}"},
	},
	"VarSpec.Names": {
		{"a let", letForPattern, "func host(v int) {\n\tlet $B = v\n\tlet _ = $B\n}"},
		{"a two-name let", plainBinder, "func host(p (int, int)) {\n\tlet $B, w = p\n\tlet _ = $B\n\tlet _ = w\n}"},
	},
	"VarSpec.Pattern": {
		{"a let's record pattern", letForPattern, "func host(v int) {\n\tlet Line{N: $B} = Line{N: v}\n\tlet _ = $B\n}"},
	},
	"TupleExpr.Elts": {
		{"a let's tuple pattern", letForPattern, "func host(p (int, int)) {\n\tlet ($B, w) = p\n\tlet _ = $B\n\tlet _ = w\n}"},
		{"a match's tuple pattern", matchPattern, "func host(p (int, int)) {\n\tmatch p {\n\tcase ($B, w):\n\t\tlet _ = $B\n\t\tlet _ = w\n\t}\n}"},
	},
	"ParenExpr.X": {
		{"a let's parenthesized name", letForPattern, "func host(v int) {\n\tlet ($B) = v\n\tlet _ = $B\n}"},
		{"a match's parenthesized name", matchPattern, "func host(v int) {\n\tmatch v {\n\tcase ($B):\n\t\tlet _ = $B\n\t}\n}"},
	},
	"CompositeLit.Elts": {
		{"a record pattern's positional field", matchPattern, "func host(s Shape) {\n\tmatch s {\n\tcase Line{$B}:\n\t\tlet _ = $B\n\tcase Dot:\n\t\tlet _ = 0\n\t}\n}"},
		{"a catch record's positional field", matchPattern, "func host(v int) {\n\tlet n = risky2(v) catch {\n\tcase Boom{$B}:\n\t\tlet _ = $B\n\t\t0\n\t}\n\tlet _ = n\n}"},
	},
	"KeyValueExpr.Value": {
		{"a match's record pattern", matchPattern, "func host(s Shape) {\n\tmatch s {\n\tcase Line{N: $B}:\n\t\tlet _ = $B\n\tcase Dot:\n\t\tlet _ = 0\n\t}\n}"},
		{"a catch record's keyed field", matchPattern, "func host(v int) {\n\tlet n = risky2(v) catch {\n\tcase Boom{N: $B}:\n\t\tlet _ = $B\n\t\t0\n\t}\n\tlet _ = n\n}"},
	},
	"RangeStmt.Key": {
		{"a for's name", letForPattern, "func host(xs []int) {\n\tfor $B in xs {\n\t\tlet _ = $B\n\t}\n}"},
		{"a for's tuple pattern", letForPattern, "func host(ps [](int, int)) {\n\tfor ($B, w) in ps {\n\t\tlet _ = $B\n\t\tlet _ = w\n\t}\n}"},
	},
	"RangeStmt.Value": {
		{"a for's second name", letForPattern, "func host(xs []int) {\n\tfor i, $B in xs {\n\t\tlet _ = i\n\t\tlet _ = $B\n\t}\n}"},
	},
	"CaseClause.List": {
		{"a match's name", matchPattern, "func host(v int) {\n\tmatch v {\n\tcase $B:\n\t\tlet _ = $B\n\t}\n}"},
	},
	"AsPattern.Name": {
		{"a match's as", plainBinder, "func host(s Shape) {\n\tmatch s {\n\tcase Line as $B:\n\t\tlet _ = $B\n\tcase Dot:\n\t\tlet _ = 0\n\t}\n}"},
	},
	"AsPattern.X": {
		{"a name under as", matchPattern, "func host(v int) {\n\tmatch v {\n\tcase $B as w:\n\t\tlet _ = $B\n\t\tlet _ = w\n\t}\n}"},
	},
	"CatchArm.Types": {
		{"a catch arm's rest", catchRest, "func host(v int) {\n\tlet n = risky(v) catch {\n\tcase $B:\n\t\tlet _ = $B\n\t\t0\n\t}\n\tlet _ = n\n}"},
		{"a catch arm's as", plainBinder, "func host(v int) {\n\tlet n = risky(v) catch {\n\tcase Bang as $B:\n\t\tlet _ = $B\n\t\t0\n\t}\n\tlet _ = n\n}"},
	},
}

// nameClass is what a binder's name is where the binder stands.
type nameClass int

const (
	freshName     nameClass = iota // no declaration
	packageType                    // tn
	badType                        // tbad, whose declaration is refused
	packageFunc                    // fx
	packageValue                   // pv, a package-level let
	packageCtx                     // cx: a named context is no package value (diagnostics.md, declared twice)
	preludeInt                     // the prelude type int
	preludeFunc                    // the prelude function len
	preludeConst                   // the prelude constant true
	preludeType                    // the prelude capability type Console
	preludeRecord                  // the prelude record type None
)

// binderNames are the names of every class. A package class has a
// lower-case and a capitalised spelling (rows.md Totality 1 and Catch 7
// give the case of a name no meaning). The prelude declares int, len and
// true in lower case and its types, Console and None among them, in
// capitals, with no capitalised function or constant; Int, Len and True
// are fresh names.
var binderNames = []struct {
	name  string
	class nameClass
}{
	{"got", freshName}, {"Got", freshName}, {"Int", freshName}, {"Len", freshName}, {"True", freshName},
	{"tn", packageType}, {"Tn", packageType},
	{"tbad", badType}, {"Tbad", badType},
	{"fx", packageFunc}, {"Fx", packageFunc},
	{"pv", packageValue}, {"Pv", packageValue},
	{"cx", packageCtx}, {"Cx", packageCtx},
	{"int", preludeInt}, {"len", preludeFunc}, {"true", preludeConst}, {"Console", preludeType}, {"None", preludeRecord},
}

func (c nameClass) prelude() bool { return c >= preludeInt }

// binderVerdict is the oracle's answer for one program: what the
// identifier means ("binds" a local or "names" the package or prelude
// object), the codes at the binder, and the codes elsewhere in the
// program, besides tbad's own declaration.
type binderVerdict struct {
	meaning   string
	at, other []string
}

func binds(codes ...string) binderVerdict { return binderVerdict{meaning: "binds", at: codes} }
func names(codes ...string) binderVerdict { return binderVerdict{meaning: "names", at: codes} }

// binderRule is the spec's rule table for a binder of kind k named by a
// name of class c.
func binderRule(k binderKind, c nameClass) binderVerdict {
	if c == freshName || c == packageCtx {
		// No type, constant, value or function of the name is in scope.
		return binds()
	}
	if c == badType && k != plainBinder && k != letForPattern {
		// The name still names the refused type, and the test of it is
		// refused silently: D3, no cascade (the seed's class).
		return names()
	}
	switch k {
	case plainBinder:
		if c.prelude() {
			return binds("S108") // D-D3: the refused binder still binds
		}
		return binds() // D-D2: a local may reuse a package-level name
	case letForPattern:
		switch {
		case c.prelude():
			return binds("S108") // D-D3: a prelude type or constant there is a binder
		}
		// Totality 6: binds over a package function, value or type, a
		// refused one included (types.md, Scope 1; L5).
		return binds()
	case matchPattern:
		switch c {
		case packageType, preludeType, preludeRecord:
			return names("S108") // Totality 1: a type other than the subject's
		case preludeInt:
			// Totality 1: the subject's own type, so the test is legal and
			// binds nothing; the arm's read of int is a type used as a value.
			return binderVerdict{meaning: "names", other: []string{"S108"}}
		case preludeConst:
			return names("S108") // Totality 1: a bool constant typed by an int subject
		case packageFunc, packageValue, preludeFunc:
			return binds("S108") // Totality 1: a function or package value name is S108 and binds a placeholder
		}
	case catchRest:
		switch c {
		case packageType, preludeInt, preludeType, preludeRecord:
			// Catch 2: the type is not in risky's row, so Bang is left
			// unhandled: Catch 4's S103 at the catch.
			return binderVerdict{meaning: "names", at: []string{"S108"}, other: []string{"S103"}}
		case preludeConst:
			return names("S108") // Totality 1: a constant is a pattern, never a failure
		case packageFunc, packageValue, preludeFunc:
			return binds("S108") // Totality 1 (rows.md Totality 6: applies to catch arms)
		}
	}
	panic("binderRule: no rule")
}

// binderFields are the binder fields: lawScopeDomain less lawNoBinder.
func binderFields(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range lawScopeDomain(t) {
		if lawNoBinder[f] == "" {
			out = append(out, f)
		}
	}
	return out
}

func TestBinderNamesAgree(t *testing.T) {
	fields := binderFields(t)
	if len(fields) < 10 {
		t.Fatalf("found %d binder fields; the guard reads the syntax package", len(fields))
	}
	inDomain := map[string]bool{}
	for _, f := range fields {
		inDomain[f] = true
		if len(binderForms[f]) == 0 {
			t.Errorf("the binder field %s has no binder program", f)
		}
	}
	for f := range binderForms {
		if !inDomain[f] {
			t.Errorf("binderForms names %s, no binder field of the domain", f)
		}
	}
	// Every class outside the prelude has both spellings.
	spellings := map[nameClass][2]bool{}
	for _, bn := range binderNames {
		s := spellings[bn.class]
		if r, _ := utf8.DecodeRuneInString(bn.name); unicode.IsUpper(r) {
			s[1] = true
		} else {
			s[0] = true
		}
		spellings[bn.class] = s
	}
	for c := freshName; c < preludeInt; c++ {
		if s := spellings[c]; !s[0] || !s[1] {
			t.Errorf("name class %d lacks a lower-case or a capitalised spelling", c)
		}
	}
	keys := make([]string, 0, len(binderForms))
	for f := range binderForms {
		keys = append(keys, f)
	}
	sort.Strings(keys)
	programs := 0
	for _, field := range keys {
		for _, form := range binderForms[field] {
			for _, bn := range binderNames {
				programs++
				row := form.name + "/" + bn.name
				decls := binderNameDecls
				if bn.class == badType {
					decls += binderBadDecl(bn.name)
				}
				decls += "\n"
				src := decls + strings.ReplaceAll(form.src, "$B", bn.name)
				at := len(decls) + strings.Index(form.src, "$B")
				want := binderRule(form.kind, bn.class)
				label := field + ", " + row

				info, walk, ok := check.BinderMeaning(src, at)
				if !ok {
					t.Errorf("%s: no identifier at the binder\n%s", label, src)
					continue
				}
				if info != want.meaning || walk != want.meaning {
					t.Errorf("%s: want %s, Info says %s and the walk says %s\n%s", label, want.meaning, info, walk, src)
				}
				if p := check.RefWalkProblems(src); len(p) > 0 {
					t.Errorf("%s: reference walk problems %v\n%s", label, p, src)
				}

				_, diags := check.CheckSource(src)
				var atBinder, other []string
				for _, d := range diags {
					switch {
					case d.Offset == at:
						atBinder = append(atBinder, d.Code)
					case bn.class == badType && d.Offset < len(decls) && d.Offset >= len(binderNameDecls):
						// tbad's own declaration, checked below.
					default:
						other = append(other, d.Code)
					}
				}
				sort.Strings(atBinder)
				sort.Strings(other)
				if strings.Join(atBinder, " ") != strings.Join(want.at, " ") {
					t.Errorf("%s: want %v at the binder, got %v\n\t%s\n%s", label, want.at, atBinder, describe(diags), src)
				}
				if strings.Join(other, " ") != strings.Join(want.other, " ") {
					t.Errorf("%s: want %v elsewhere, got %v\n\t%s\n%s", label, want.other, other, describe(diags), src)
				}
				if bn.class == badType && len(diags)-len(atBinder)-len(other) != 1 {
					t.Errorf("%s: want one diagnostic at tbad's declaration\n\t%s\n%s", label, describe(diags), src)
				}
			}
		}
	}
	t.Logf("%d binder fields, %d programs (%d names each)", len(fields), programs, len(binderNames))
}
