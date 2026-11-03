package check_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestPreludeAndStdAreClean: the embedded prelude and the standard library
// are ordinary Ergo source checked by this checker, with no diagnostic.
func TestPreludeAndStdAreClean(t *testing.T) {
	if diags := check.PreludeDiagnostics(); len(diags) > 0 {
		t.Fatalf("prelude diagnostics: %v", diags)
	}
	pkg, err := check.Std.Import("timeout")
	if err != nil || pkg == nil {
		t.Fatalf("std timeout: %v", err)
	}
	if _, err := check.Std.Import("nosuch"); err == nil {
		t.Fatal("an unknown std package imported")
	}
}

// negativeSpans pins the code and the spanned text of each negative fixture
// (the unit-test mirror of the acceptance negatives, spec Work rules).
var negativeSpans = map[string][2]string{
	"argument-mismatch":                  {"S108", "\"x\""},
	"borrow-in-field":                    {"S108", "*int64"},
	"capability-as-value":                {"S104", "Store"},
	"case-not-variant":                   {"S108", "C"},
	"catch-arm-not-in-row":               {"S108", "B"},
	"catch-not-total":                    {"S103", "catch"},
	"constant-overflow":                  {"S108", "300"},
	"constraint-unsatisfied":             {"S108", "same(xs, xs)"},
	"duplicate-function":                 {"S108", "helper"},
	"duplicate-context-entry":            {"S111", "Store"},
	"equality-mismatch":                  {"S108", "\"a\" == 1"},
	"error-method-on-error-type":         {"S108", "Error"},
	"error-as-value-exported":            {"S101", "v"},
	"error-row-non-struct":               {"S104", "Store"},
	"export-guard-machine-step":          {"S101", "try probe()"},
	"fallible-function-value":            {"S108", "boom"},
	"generic-row-parameter-instantiated": {"S101", "apply(fail)"},
	"inferred-error-reaches-exported":    {"S101", "helper()"},
	"inferred-needs-reaches-exported":    {"S102", "read()"},
	"interface-method-needs":             {"S108", "needs Other"},
	"interface-missing-method":           {"S108", "Mem"},
	"interface-wider-row":                {"S108", "Mem"},
	"keyed-literal-nondefaultable":       {"S108", "Box{Name: \"x\"}"},
	"literal-row-from-parameter-type":    {"S101", "try fail()"},
	"local-and-binding-one-frame":        {"S104", "Store"},
	"main-called":                        {"S113", "main()"},
	"main-parameter":                     {"S113", "(arg string)"},
	"main-value-result":                  {"S113", "string"},
	"missing-capability":                 {"S102", "Store.Get()"},
	"missing-return":                     {"S107", "}"},
	"parameter-kind-conflict":            {"S108", "E"},
	"partial-catch-rest":                 {"S101", "try fail() catch {"},
	"plus-on-bool":                       {"S108", "true + false"},
	"positional-literal-short":           {"S108", "P{1}"},
	"product-not-total":                  {"S103", "match"},
	"provider-method-needs":              {"S106", "Mem"},
	"provider-missing-method":            {"S106", "Mem"},
	"provider-wider-row":                 {"S106", "Mem"},
	"recursive-struct":                   {"S108", "Node"},
	"return-in-with-block":               {"S108", "return \"x\""},
	"return-mismatch":                    {"S108", "true"},
	"timeout-is-a-library-function":      {"S101", "timeout.After(1s) { \"ok\" }"},
	"try-infallible":                     {"S108", "try word()"},
	"type-argument-not-inferred":         {"S108", "zero()"},
	"type-switch-non-union":              {"S108", "Other"},
	"type-switch-not-total":              {"S103", "match"},
	"undeclared-error":                   {"S101", "try fail()"},
	"unhandled-argument":                 {"S105", "fail()"},
	"unhandled-condition":                {"S105", "check()"},
	"unhandled-define":                   {"S105", "fail()"},
	"union-variant-interface":            {"S108", "Store"},
	"unknown-field":                      {"S104", "Name"},
	"unknown-local":                      {"S104", "missing"},
	"unknown-selector":                   {"S104", "Total"},
	"unknown-type":                       {"S104", "Missing"},
	"unknown-context":                    {"S104", "Missing"},
	"unprovided-callee":                  {"S102", "read()"},
	"integer-literal-as-duration":        {"S108", "2"},
	"unused-expression":                  {"S110", "x"},
	"unused-local":                       {"S110", "x"},
	"unused-type-switch-binding":         {"S110", "v"},
	"needs-non-capability":               {"S104", "Order"},
	"var-mismatch":                       {"S108", "\"seven\""},
	"var-nondefaultable":                 {"S108", "c"},
	"context-constructor-is-37":          {"S109", "open"},
	"context-cycle":                      {"S108", "W"},
	"context-entry-non-capability":       {"S104", "Order"},
	"context-exposure-hides-entry":       {"S109", "provides Account"},
	"context-literal-outside-with":       {"S109", "context { Store: Mem }"},
}

// TestNegativeSpans: each negative fixture's one diagnostic has the pinned
// code and covers the pinned text, on its marked line.
func TestNegativeSpans(t *testing.T) {
	for name, want := range negativeSpans {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "negative", name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			_, diags := check.CheckSource(src)
			if len(diags) != 1 {
				t.Fatalf("want one diagnostic, got %v", diags)
			}
			d := diags[0]
			got := src[d.Offset : d.Offset+d.Length]
			if d.Code != want[0] || got != want[1] {
				t.Fatalf("got %s covering %q, want %s covering %q", d.Code, got, want[0], want[1])
			}
			line := strings.Count(src[:d.Offset], "\n") + 1
			col := d.Offset - strings.LastIndexByte(src[:d.Offset], '\n')
			if d.Line != line || d.Column != col {
				t.Fatalf("position %d:%d, want %d:%d", d.Line, d.Column, line, col)
			}
		})
	}
}

// TestRegistry: the code table, docs/spec/diagnostics.md's code rows and
// rule headings, and the parser's code agree (spec D11).
func TestRegistry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "spec", "diagnostics.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	row := regexp.MustCompile(`(?m)^\| (S\d{3})( \(designed\))? \|`)
	heading := regexp.MustCompile(`(?m)^### (S\d{3}): (.+)$`)
	docRows := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(text, -1) {
		status := "implemented"
		if m[2] != "" {
			status = "designed"
		}
		docRows[m[1]] = status
	}
	anchors := map[string]string{}
	for _, m := range heading.FindAllStringSubmatch(text, -1) {
		anchors[m[1]] = strings.ToLower(m[1]) + "-" + strings.ReplaceAll(strings.ToLower(m[2]), " ", "-")
	}
	table := map[string]bool{}
	for _, c := range check.Codes {
		table[c.Code] = true
		if docRows[c.Code] != c.Status {
			t.Errorf("%s: table status %q, diagnostics.md %q", c.Code, c.Status, docRows[c.Code])
		}
		if anchors[c.Code] != c.Anchor {
			t.Errorf("%s: table anchor %q, diagnostics.md heading anchor %q", c.Code, c.Anchor, anchors[c.Code])
		}
	}
	for code := range docRows {
		if !table[code] {
			t.Errorf("diagnostics.md lists %s, which the code table lacks", code)
		}
	}
	if !check.Registered(syntax.CodeSyntax) {
		t.Errorf("the parser's code %s is not registered", syntax.CodeSyntax)
	}
}

// corpus returns every source the checker's tests and the fuzz seeds use:
// the fixture mirror, the acceptance rule fixtures copied into testdata/v1,
// the prototype's fixtures and the parser's corpus, as translated into the
// decided syntax (#196; the frozen old .sketch copies no longer parse).
func corpus(t testing.TB) map[string]string {
	out := map[string]string{}
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/v1/*.ergo",
		"../proto/testdata/*.ergo",
		"../proto/testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
	} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out[f] = string(data)
		}
	}
	return out
}

// TestDeterministic: checking one source twice gives the same diagnostics,
// in position order, each located and registered.
func TestDeterministic(t *testing.T) {
	sources := corpus(t)
	if len(sources) < 150 {
		t.Fatalf("corpus has %d sources", len(sources))
	}
	for name, src := range sources {
		_, a := check.CheckSource(src)
		_, b := check.CheckSource(src)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: two runs differ", name)
		}
		assertWellFormed(t, name, src, a)
	}
}

func assertWellFormed(t testing.TB, name, src string, diags []check.Diagnostic) {
	t.Helper()
	for i, d := range diags {
		if !check.Registered(d.Code) {
			t.Errorf("%s: unregistered code %s", name, d.Code)
		}
		if d.Offset < 0 || d.Length < 0 || d.Offset+d.Length > len(src) {
			t.Errorf("%s: span %d+%d outside the source", name, d.Offset, d.Length)
		}
		if d.Code != syntax.CodeSyntax && d.Line < 1 {
			t.Errorf("%s: unlocated diagnostic %v", name, d)
		}
		if i > 0 && diags[i-1].Offset > d.Offset {
			t.Errorf("%s: diagnostics out of order", name)
		}
	}
}

// FuzzCheck: no input panics the checker, its output is deterministic,
// located and registered, and an accepted program's Info is complete and
// final (spec Work rules; D-A4 invariants 1 and 2).
func FuzzCheck(f *testing.F) {
	for _, src := range corpus(f) {
		f.Add(src)
	}
	f.Add(contextShadowPrelude + `func f() int { let W = Tag{Text: "local"}; with W { 1 } }`)
	f.Add(contextShadowPrelude + `func f(W Tag) int { with ((W)) { 1 } }`)
	f.Add(valueContextHeader + "let W = Tag{Text: \"pkg\"}\nlet W = context { A: Quiet }\nfunc g() { with W { A.Use() } }\n")
	f.Add(valueContextHeader + "let W = context { A: Quiet }\nlet Tag{Text: W} = Tag{Text: \"pkg\"}\nfunc g() { with (W) { A.Use() } }\n")
	f.Fuzz(func(t *testing.T, src string) {
		_, a := check.CheckSource(src)
		_, b := check.CheckSource(src)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("two runs differ")
		}
		assertWellFormed(t, "input", src, a)
		// Class H (round 16): the reference walk visits every body node and
		// resolves as typing does, and declaration order changes no verdict.
		if p := check.RefWalkProblems(src); len(p) > 0 {
			t.Fatalf("reference walk: %s", strings.Join(p, "; "))
		}
		if p := check.OrderProblems(src, 2); len(p) > 0 {
			t.Fatalf("declaration order: %s", p[0])
		}
		if gaps := check.InfoGaps(src); len(gaps) > 0 {
			t.Fatalf("Info of an accepted program:\n\t%s", strings.Join(gaps, "\n\t"))
		}
	})
}
