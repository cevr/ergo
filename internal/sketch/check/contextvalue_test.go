package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

const contextShadowPrelude = `type A { Use() }
type Quiet {}
func Use(_ Quiet) {}
let W = context { A: Quiet }
type Tag { Text string }
func contextKey(_ Tag) string { "k" }
func contextText(t Tag) string { t.Text }
`

// valueContextHeader is seven lines; the two declarations under test follow
// on lines 8 and 9, and a with of W on line 10.
const valueContextHeader = `type A { Use() }
type Quiet {}
func Use(_ Quiet) {}
type Tag { Text string }
func contextKey(_ Tag) string { "k" }
func contextText(t Tag) string { t.Text }
type Outer { In Tag }
`

// withKinds renders what each with of pkg binds: "services" (a named
// or inline context), "value" (a context value) or "none", sorted.
func withKinds(pkg *check.Package) string {
	var out []string
	for _, w := range pkg.Info.Withs {
		switch {
		case len(w.Contexts) > 0:
			out = append(out, "services")
		case w.Value != nil:
			out = append(out, "value")
		default:
			out = append(out, "none")
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// A package value and a named context may not share a name: the later of the
// two is S108, so `with W` never chooses between two package bindings of
// W. A record with no fields is a value too (written bare), as a unit
// struct is in Rust (E0428). A type with fields or operations and a context
// still share a name. The with binds the earlier value, or the context.
func TestPackageValueNamedLikeContext(t *testing.T) {
	const context = `let W = context { A: Quiet }`
	values := map[string]struct {
		decl string
		// valueFirst is what the with binds when the value is first ("" is
		// not asserted: W is no context value there).
		valueFirst string
	}{
		"let":            {`let W = Tag{Text: "pkg"}`, "value"},
		"func":           {`func W() int { 1 }`, ""},
		"pattern let":    {`let Tag{Text: W} = Tag{Text: "pkg"}`, ""},
		"nested pattern": {`let Outer{In: W} = Outer{In: Tag{Text: "pkg"}}`, "value"},
		"tuple let":      {`let q, W = (1, Tag{Text: "pkg"})`, "value"},
		"unit record":    {`type W {}`, "services"},
		"unit variant":   {`type S = W{} | Paid{ n int }`, "services"},
	}
	for name, value := range values {
		for _, order := range []struct {
			name  string
			first string
			later string
			want  string
		}{{"context first", context, value.decl, "services"}, {"value first", value.decl, context, value.valueFirst}} {
			for _, operand := range []string{"W", "(W)"} {
				t.Run(name+"/"+order.name+"/"+operand, func(t *testing.T) {
					src := valueContextHeader + order.first + "\n" + order.later + "\n" + fmt.Sprintf("func g() { with %s { A.Use() } }\n", operand)
					pkg, ds := check.CheckSource(src)
					var at []int
					for _, d := range ds {
						if d.Code == "S108" && d.Message == "W is already declared in this package" {
							at = append(at, d.Line)
						}
					}
					if len(at) != 1 || at[0] != 9 {
						t.Fatalf("want one S108 at the later declaration (line 9), got %v in %v", at, ds)
					}
					if got := withKinds(pkg); order.want != "" && got != order.want {
						t.Fatalf("with W binds %s, want %s", got, order.want)
					}
				})
			}
		}
	}
	for _, decl := range []string{"type W { n int }", "type W { Use() }"} {
		for _, src := range []string{
			valueContextHeader + decl + "\n" + context + "\nfunc g() { with W { A.Use() } }\n",
			valueContextHeader + context + "\n" + decl + "\nfunc g() { with (W) { A.Use() } }\n",
		} {
			if _, ds := check.CheckSource(src); len(ds) != 0 {
				t.Errorf("a type and a context share a name: %v", ds)
			}
		}
	}
}

// The duplicate rule across files: the later declaration is the one in the
// later file, whichever kind it is (runtime-199 stage 4a r3 m4). The later
// file is the later by name (check/canon.go; pkg-lower round 9), as in
// Go, not the later as listed, so both listings of one pair of files blame
// the same file at the same place.
func TestPackageValueNamedLikeContextFiles(t *testing.T) {
	const context = "let W = context { A: Quiet }\n"
	for _, value := range []string{
		"let Outer{In: W} = Outer{In: Tag{Text: \"pkg\"}}\n",
		"let W = Tag{Text: \"pkg\"}\n",
		"type W {}\n",
	} {
		for _, tc := range []struct {
			name  string
			files [2]string
		}{
			{"value with the header", [2]string{valueContextHeader + value, context}},
			{"context with the header", [2]string{valueContextHeader + context, value}},
		} {
			t.Run(strings.Fields(value)[1]+"/"+tc.name, func(t *testing.T) {
				var texts [2]string
				for i, src := range tc.files {
					texts[i] = src + "func g" + fmt.Sprint(i) + "() { with W { A.Use() } }\n"
				}
				names := [2]string{"f0.ergo", "f1.ergo"}
				const later = 1 // f1.ergo sorts after f0.ergo
				decl := context
				if !strings.Contains(texts[later], context) {
					decl = value
				}
				offset := strings.Index(texts[later], decl) + strings.Index(decl, "W")
				var first check.Diagnostic
				for order := range 2 {
					listed := [2]string{texts[order], texts[1-order]}
					var files []*syntax.File
					for i, src := range listed {
						f, err := syntax.Parse(src)
						if err != nil {
							t.Fatal(err)
						}
						f.Name = names[i^order]
						files = append(files, f)
					}
					_, ds := check.Check("main", files, nil)
					var at []check.Diagnostic
					for _, d := range ds {
						if d.Code == "S108" && d.Message == "W is already declared in this package" {
							at = append(at, d)
						}
					}
					if len(at) != 1 || listed[at[0].File] != texts[later] || at[0].Offset != offset {
						t.Fatalf("listing %d: want one S108 in the later file by name at offset %d, got %v in %v", order, offset, at, ds)
					}
					d := at[0]
					d.File = 0
					if order == 0 {
						first = d
					} else if d != first {
						t.Fatalf("the two listings report %v and %v", first, d)
					}
				}
			})
		}
	}
}

// An import name and a named context may not share a name either: the import
// is refused as it is for a let of its name, so `with log` never binds a
// context beside the package log (runtime-199 stage 4a r3 m1).
func TestImportNamedLikeContext(t *testing.T) {
	for _, tc := range []struct{ imp, src, want string }{
		{"import log\n", "let log = context { A: Quiet }\nfunc g() { with log { A.Use() } }\n",
			"log clashes with context log; alias the import (import t log)"},
		{"import l log\n", "let l = context { A: Quiet }\nfunc g() { with l { A.Use() } }\n",
			"l clashes with context l; choose another alias"},
	} {
		_, ds := check.CheckSource(tc.imp + valueContextHeader + tc.src)
		var got []string
		for _, d := range ds {
			if strings.Contains(d.Message, "clashes with") {
				got = append(got, d.Message)
			}
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%q: want one %q, got %v in %v", tc.src, tc.want, got, ds)
		}
	}
}

// The binder table (runtime-199 stage 4a r2 o2): every form that binds a
// local W next to the package context W. A value binder makes W a context
// operand; a type parameter or a needs-row name hides the context and is no
// operand (r3 m3, r2 m1). Typing and the reference walk read one resolved
// binding kind, and RefWalkProblems checks the operand both ways.
func TestWithOperandBinders(t *testing.T) {
	const prelude = contextShadowPrelude + `type Boom { Tag Tag }
type Box { W Tag }
func mk() Tag fails Boom { Tag{Text: "x"} }
func two[T](a T, b T) []T { []T{a, b} }
`
	const hides = "which hides the package context"
	for _, tc := range []struct{ name, body, want, diag string }{
		{"let", `func f() int { let W = Tag{Text: "l"}; with W { 1 } }`, "value", ""},
		{"param", `func f(W Tag) int { with W { 1 } }`, "value", ""},
		{"for-in", `func f() int { let mut n = 0; for W in two(Tag{Text: "a"}, Tag{Text: "b"}) { n = n + with W { 1 } }; n }`, "value", ""},
		{"closure", `func f() int { let W = Tag{Text: "c"}; let g = func() int { with W { 1 } }; g() }`, "value", ""},
		{"literal-param", `func f() int { let g = func(W Tag) int { with W { 1 } }; g(Tag{Text: "p"}) }`, "value", ""},
		{"record-pattern", `func f(b Box) int { match b { case Box{W: W}: with W { 1 } } }`, "value", ""},
		{"let-pattern", `func f(b Box) int { let Box{W: W} = b; with W { 1 } }`, "value", ""},
		{"catch-record", `func f() int { let t = mk() catch { case Boom{Tag: W}: with W { Tag{Text: "r"} } }; with t { 1 } }`, "value,value", ""},
		{"after", `func f() int { let a = with W { A.Use(); 1 }; let W = Tag{Text: "z"}; a + with W { 1 } }`, "services,value", ""},
		{"inner-block", `func f() int { if true { let W = Tag{Text: "i"}; return with W { 1 } }; with W { A.Use(); 2 } }`, "services,value", ""},
		{"type-param", `func f[W](x W) { with W { A.Use() } }`, "none", "W names a type parameter here, " + hides},
		{"type-param-paren", `func f[W](x W) { with (W) { A.Use() } }`, "none", "W names a type parameter here, " + hides},
		{"needs-row", "let A = context { A: Quiet }\nfunc f() needs A { with A { A.Use() } }", "none", "A names the capability of this function's needs row here, " + hides},
		{"type-param-literal", `func f[W](x W) { let g = func() { with W { A.Use() } }; g() }`, "none", "W names a type parameter here, " + hides},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := prelude + tc.body + "\n"
			pkg, ds := check.CheckSource(src)
			var got []string
			for _, d := range ds {
				got = append(got, d.Message)
			}
			switch {
			case tc.diag == "" && len(ds) != 0:
				t.Fatalf("diagnostics: %v", ds)
			case tc.diag != "" && (len(ds) != 1 || !strings.HasPrefix(ds[0].Message, tc.diag)):
				t.Fatalf("want one %q, got %v", tc.diag, got)
			}
			if k := withKinds(pkg); k != tc.want {
				t.Fatalf("withs bind %s, want %s", k, tc.want)
			}
			if p := check.RefWalkProblems(src); len(p) > 0 {
				t.Errorf("reference walk: %s", strings.Join(p, "; "))
			}
		})
	}
}

// contextPrelude declares a user type that satisfies the context-binding
// protocol (runtime.md, G1) and a capability for the blocks.
const contextValuePrelude = `type Log { Say(s string) }
type Tag { Key string, Text string }
func contextKey(t Tag) string { t.Key }
func contextText(t Tag) string { t.Text }
func tag(k string, v string) Tag { Tag{Key: k, Text: v} }
`

// checkContext checks contextPrelude and src as package main.
func checkContext(t *testing.T, src string) (*check.Package, *syntax.File, []check.Diagnostic) {
	t.Helper()
	var imports, rest []string
	for _, line := range strings.SplitAfter(src, "\n") {
		if strings.HasPrefix(line, "import ") {
			imports = append(imports, line)
		} else {
			rest = append(rest, line)
		}
	}
	f, err := syntax.Parse(strings.Join(imports, "") + contextValuePrelude + strings.Join(rest, ""))
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	return pkg, f, ds
}

// A same-shaped type that breaks the protocol is not a context binding:
// admission is the protocol, never the spelling of the type or its
// functions. Each case is one located diagnostic at the operand.
func TestContextBindingRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, code, text string }{
		{"key needs a capability", `type Bad { K string }
func contextKey(b Bad) string needs Log { Log.Say("k"); b.K }
func contextText(b Bad) string { b.K }
func bad() Bad { Bad{K: "k"} }
func f() int needs Log { with bad() { 1 } }
`, "S108", "requires {Log}"},
		{"text can fail", `type Fault {}
type Bad { K string }
func contextKey(b Bad) string { b.K }
func contextText(b Bad) string fails Fault { if b.K == "" { return Fault }; b.K }
func bad() Bad { Bad{K: "k"} }
func f() int { with bad() { 1 } }
`, "S108", "can fail with"},
		{"key returns another type", `type Bad { K string }
func contextKey(b Bad) int { 1 }
func contextText(b Bad) string { b.K }
func bad() Bad { Bad{K: "k"} }
func f() int { with bad() { 1 } }
`, "S108", "has signature"},
		{"no contextText", `type Bad { K string }
func contextKey(b Bad) string { b.K }
func bad() Bad { Bad{K: "k"} }
func f() int { with bad() { 1 } }
`, "S108", "has no method contextText"},
		{"fields, not functions", `type Bad { contextKey string, contextText string }
func bad() Bad { Bad{contextKey: "k", contextText: "v"} }
func f() int { with bad() { 1 } }
`, "S108", "has no method contextKey"},
		{"a basic value", `func f() int { with "k" { 1 } }
`, "S108", "string is not a named type"},
		{"an operation type", `type Keyed { contextKey() string, contextText() string }
func f(k Keyed) int { with k { 1 } }
`, "S108", "are operations, not functions on the type"},
		{"exported names", `type Bad { K string }
func ContextKey(b Bad) string { b.K }
func ContextText(b Bad) string { b.K }
func bad() Bad { Bad{K: "k"} }
func f() int { with bad() { 1 } }
`, "S108", "has no method contextKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ds := checkContext(t, tc.src)
			if len(ds) != 1 || ds[0].Code != tc.code || !strings.Contains(ds[0].Message, tc.text) {
				t.Fatalf("diagnostics: %v, want one %s containing %q", ds, tc.code, tc.text)
			}
		})
	}
}
