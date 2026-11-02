package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

const contextShadowPrelude = `type A { Use() }
type Quiet {}
func Use(_ Quiet) {}
let W = wire { A: Quiet }
type Tag { Text string }
func contextKey(_ Tag) string { "k" }
func contextText(t Tag) string { t.Text }
`

// A with operand obeys the same lexical scope as a value expression.
// A local or parameter must be used even when a package wire has its name.
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

// contextPrelude declares a user type that satisfies the context-binding
// protocol (runtime.md, G1) and a capability for the blocks.
const contextPrelude = `type Log { Say(s string) }
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
	f, err := syntax.Parse(strings.Join(imports, "") + contextPrelude + strings.Join(rest, ""))
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	return pkg, f, ds
}

// contextWiths renders each with of a function body in source order: the
// binding's type and its Key and Text functions, or "wire" for a with of a
// wire.
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
			case w.Context == nil:
				out = append(out, fd.Name.Name+": wire")
			default:
				cb := w.Context
				out = append(out, fd.Name.Name+": "+cb.Type.String()+" "+cb.Key.Name+"@"+cb.Key.Pkg.Path+" "+cb.Text.Name+"@"+cb.Text.Pkg.Path)
				if len(w.Wires) != 0 || len(w.Discharged) != 0 || len(w.Acquired) != 0 {
					out = append(out, fd.Name.Name+": a context with holds wire facts")
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
		{"inside a wire with", `type Quiet {}
func Say(_ Quiet, s string) {}
let W = wire { Log: Quiet }
func f() { with W { with tag("k", "v") { Log.Say("x") } } }
`, "f: wire\nf: main.Tag contextKey@main contextText@main"},
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
`, "S109", "with over a value"},
		{"an operand list", `func f() int { with tag("a", "1"), tag("b", "2") { 1 } }
`, "S109", "nest one with per operand"},
		{"exported names", `type Bad { K string }
func ContextKey(b Bad) string { b.K }
func ContextText(b Bad) string { b.K }
func bad() Bad { Bad{K: "k"} }
func f() int { with bad() { 1 } }
`, "S108", "has no method contextKey"},
		{"a std operand list", `import log
func f() int { with log.Annotate("a", "1"), log.Annotate("b", "2") { 1 } }
`, "S109", "nest one with per operand"},
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
	checkFootprints(t, contextPrelude+`type Opaque { K string }
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
