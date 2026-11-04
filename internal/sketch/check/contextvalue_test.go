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

// A with operand obeys the same lexical scope as a value expression.
// A local or parameter must be used even when a package context has its name.
func TestContextBindingLexicalOperand(t *testing.T) {
	for _, operand := range []string{"W", "(W)", "((W))"} {
		for _, tc := range []struct{ name, fn string }{
			{"local", `func f() int { let W = Tag{Text: "local"}; with %s { 1 } }`},
			{"parameter", `func f(W Tag) int { with %s { 1 } }`},
		} {
			t.Run(tc.name+"/"+operand, func(t *testing.T) {
				src := contextShadowPrelude + fmt.Sprintf(tc.fn, operand)
				f, err := syntax.Parse(src)
				if err != nil {
					t.Fatal(err)
				}
				pkg, ds := check.Check("main", []*syntax.File{f}, nil)
				if len(ds) != 0 {
					t.Fatalf("diagnostics: %v", ds)
				}
				if got := strings.Join(contextWiths(pkg, f), "\n"); got != "f: main.Tag contextKey@main contextText@main" {
					t.Fatalf("with resolves %s, want the lexical Tag", got)
				}
			})
		}
	}
}

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
// later file, whichever kind it is (runtime-199 stage 4a r3 m4).
func TestPackageValueNamedLikeContextFiles(t *testing.T) {
	const context = "let W = context { A: Quiet }\n"
	for _, value := range []string{
		"let Outer{In: W} = Outer{In: Tag{Text: \"pkg\"}}\n",
		"let W = Tag{Text: \"pkg\"}\n",
		"type W {}\n",
	} {
		for _, tc := range []struct {
			name   string
			files  [2]string
			at     int // the file of the later declaration
			column int
		}{
			{"value in file 0", [2]string{valueContextHeader + value, context}, 1, 5},
			{"context in file 0", [2]string{valueContextHeader + context, value}, 1, strings.Index(value, "W") + 1},
		} {
			t.Run(strings.Fields(value)[1]+"/"+tc.name, func(t *testing.T) {
				var files []*syntax.File
				for _, src := range tc.files {
					f, err := syntax.Parse(src + "func g" + fmt.Sprint(len(files)) + "() { with W { A.Use() } }\n")
					if err != nil {
						t.Fatal(err)
					}
					files = append(files, f)
				}
				_, ds := check.Check("main", files, nil)
				var at []check.Diagnostic
				for _, d := range ds {
					if d.Code == "S108" && d.Message == "W is already declared in this package" {
						at = append(at, d)
					}
				}
				if len(at) != 1 || at[0].File != tc.at || at[0].Line != 1 || at[0].Column != tc.column {
					t.Fatalf("want one S108 in file %d at 1:%d, got %v in %v", tc.at, tc.column, at, ds)
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

func TestContextOperandReferenceWalk(t *testing.T) {
	for _, body := range []string{
		`func f() int { let W = Tag{Text: "local"}; with W { 1 } }`,
		`func f(W Tag) int { with ((W)) { 1 } }`,
		`func f() { with W { A.Use() } }`,
	} {
		if p := check.RefWalkProblems(contextShadowPrelude + body); len(p) > 0 {
			t.Errorf("reference walk: %s", strings.Join(p, "; "))
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

// contextWiths renders each with of a function body in source order: the
// binding's type and its Key and Text functions, or "services" for a with of a
// named or inline context.
func contextWiths(pkg *check.Package, f *syntax.File) []string {
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := pkg.Info.Withs[x]
			switch {
			case w == nil:
			case w.Value == nil:
				out = append(out, fd.Name.Name+": services")
			default:
				cb := w.Value
				out = append(out, fd.Name.Name+": "+cb.Type.String()+" "+cb.Key.Name+"@"+cb.Key.Pkg.Path+" "+cb.Text.Name+"@"+cb.Text.Pkg.Path)
				if len(w.Contexts) != 0 || len(w.Discharged) != 0 || len(w.Acquired) != 0 {
					out = append(out, fd.Name.Name+": a context with holds context facts")
				}
			}
			return true
		})
	}
	return out
}

// A value whose type satisfies the protocol is a context binding, however
// it is spelled: a call, a local, a nested binding. The binding discharges
// nothing, so the block's needs stay the enclosing function's.
func TestContextBindingAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"user type", `func f() needs Log { with tag("k", "v") { Log.Say("x") } }
`, "f: main.Tag contextKey@main contextText@main"},
		{"local value", `func f() int { let a = tag("k", "v"); with a { 1 } }
`, "f: main.Tag contextKey@main contextText@main"},
		{"nested shadow", `func f() int { with tag("k", "outer") { with tag("k", "inner") { 1 } } }
`, "f: main.Tag contextKey@main contextText@main\nf: main.Tag contextKey@main contextText@main"},
		{"inside a context with", `type Quiet {}
func Say(_ Quiet, s string) {}
let W = context { Log: Quiet }
func f() { with W { with tag("k", "v") { Log.Say("x") } } }
`, "f: services\nf: main.Tag contextKey@main contextText@main"},
		{"block value", `func f() string { with tag("k", "v") { "x" } }
`, "f: main.Tag contextKey@main contextText@main"},
		// std's values bind by the same protocol: the checker reads the
		// functions on their unexported types, never the package's name.
		{"std values", `import log
import trace
func f() int { with log.Annotate("region", "eu") { with trace.Span("work") { 1 } } }
`, "f: log.annotation contextKey@log contextText@log\nf: trace.span contextKey@trace contextText@trace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg, f, ds := checkContext(t, tc.src)
			if len(ds) != 0 {
				t.Fatalf("diagnostics: %v", ds)
			}
			if got := strings.Join(contextWiths(pkg, f), "\n"); got != tc.want {
				t.Fatalf("withs:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// A with of a context value discharges no capability: a block that needs
// Log under a function that does not declare it is the function's need.
func TestContextBindingDischargesNothing(t *testing.T) {
	_, _, ds := checkContext(t, `type Other { Do() }
func f() needs Other { with tag("k", "v") { Log.Say("x") } }
`)
	if len(ds) != 1 || ds[0].Code != "S102" {
		t.Fatalf("diagnostics: %v, want one S102 for the undischarged Log", ds)
	}
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
`, "S108", "requires {main.Log}"},
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

// The operand is evaluated in the enclosing frame, as an argument is: an
// unhandled failure is S105, and a tried one is the function's failure.
func TestContextBindingOperandFailure(t *testing.T) {
	src := `type Fault {}
func key(ok bool) string fails Fault { if ok { return "k" }; Fault }
func tried(ok bool) int fails Fault { with tag(try key(ok), "v") { 1 } }
`
	pkg, _, ds := checkContext(t, src)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	for _, f := range pkg.Funcs {
		if f.Name == "tried" && !strings.Contains(f.Sig.Errors.String(), "Fault") {
			t.Fatalf("tried's row %s lacks the operand's Fault", f.Sig.Errors)
		}
	}
	_, _, ds = checkContext(t, `type Fault {}
func key(ok bool) string fails Fault { if ok { return "k" }; Fault }
func untried() int { with tag(key(true), "v") { 1 } }
`)
	if len(ds) != 1 || ds[0].Code != "S105" {
		t.Fatalf("diagnostics: %v, want one S105", ds)
	}
}

// A with of a context value runs the protocol's functions in its frame
// (Footprints): a pure binding keeps a pure frame pure, and a binding whose
// function runs unknown code makes the frame opaque.
func TestContextBindingFootprints(t *testing.T) {
	checkFootprints(t, contextValuePrelude+`type Opaque { K string }
func contextKey(o Opaque) string { let f = func() string { o.K }; f() }
func contextText(o Opaque) string { o.K }
func mk() Opaque { Opaque{K: "k"} }
func pure() int { with tag("k", "v") { 1 } }
func opaque() int { with mk() { 1 } }
func block() needs Log { with tag("k", "v") { Log.Say("x") } }
`, map[string]string{"pure": "pure", "opaque": "opaque", "block": "Log"})
}

// A context read is effectful even with an empty capability row (approval
// item 4): log.Annotation and trace.Current read the running task's
// context, so a frame that reads one is never pure. A std binding's own
// functions are pure, so a with of one keeps a pure frame pure.
func TestContextReadFootprints(t *testing.T) {
	checkFootprints(t, `import log
import trace
func annotation() bool { match log.Annotation("k") { case Some[string]: true; case None: false } }
func current() bool { match trace.Current() { case Some[string]: true; case None: false } }
func bound() int { with log.Annotate("k", "v") { with trace.Span("s") { 1 } } }
`, map[string]string{"annotation": "opaque", "current": "opaque", "bound": "pure"})
}
