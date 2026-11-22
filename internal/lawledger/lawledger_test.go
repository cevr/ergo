package lawledger

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const file = "# Std laws that a Go test still runs.\n\n" +
	"os Process.GetenvIsStable\tTestOther\n" +
	"queue CloseKeepsValues\tTestRunner\n" +
	"strings RepeatLength\tTestRunner\n" +
	"time TimeRoundTrips\tTestRunner\n"

// statements are the source statements of file's laws; time
// TimeRoundTrips is a law the parser derives, with no source text.
var statements = map[string]string{
	"os Process.GetenvIsStable": "GetenvIsStable(key string) { Getenv(key) == Getenv(key) }",
	"queue CloseKeepsValues":    "CloseKeepsValues(v int) { Pop() == v }",
	"strings RepeatLength":      "RepeatLength(n int) { n < 0 || n > 64 }",
}

// Parse reads each line that is not a comment as a key and a runner; a
// line without two tab-separated cells is an error that names its line.
// Mutant it kills (testdata/mutants.json): runners-short-line-read.
func TestParse(t *testing.T) {
	rows, err := Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %v", rows)
	}
	if r := rows[0]; r.Key() != "os Process.GetenvIsStable" || r.Runner != "TestOther" || r.Line != 3 {
		t.Errorf("row = %+v", r)
	}
	for _, bad := range []string{"queue CloseKeepsValues TestRunner", "queue CloseKeepsValues", "queue\tTestRunner", "queue A\tTestRunner\tTestOther"} {
		if _, err := Parse(file + bad + "\n"); err == nil || !strings.Contains(err.Error(), "runners.txt:7") {
			t.Errorf("the line %q: err = %v", bad, err)
		}
	}
}

// fakeRunner records what run reports and the subtests it starts, without
// running them.
type fakeRunner struct {
	name     string
	errors   []string
	subtests []string
}

func (f *fakeRunner) Helper()      {}
func (f *fakeRunner) Name() string { return f.name }
func (f *fakeRunner) Error(args ...any) {
	f.errors = append(f.errors, fmt.Sprint(args...))
}
func (f *fakeRunner) Run(name string, _ func(*testing.T)) bool {
	f.subtests = append(f.subtests, name)
	return true
}

// run fails the runner on each disagreement with its lines and the
// source, fails a law with no source text by naming parsed.Run, and starts
// one subtest per law it restates, named after the key, in key order.
// Mutant it kills: run-unknown-key-passes.
func TestRunReportsAndRunsEachLaw(t *testing.T) {
	rows, err := Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	source := func(key string) (string, bool) {
		s, ok := statements[key]
		return s, ok
	}
	check := func(*testing.T) {}
	laws := map[string]Law{
		"strings RepeatLength":   {Statement: "RepeatLength(n int) { n < 0 || n > 64 }", Check: check},
		"queue CloseKeepsValues": {Statement: "CloseKeepsValues(v int) { Pop() == v }", Check: check},
		"time TimeRoundTrips":    {Statement: "TimeRoundTrips(v Time) { true }", Check: check},
	}
	derived := &fakeRunner{name: "TestRunner"}
	run(derived, rows, source, laws)
	if len(derived.errors) != 1 || !strings.Contains(derived.errors[0], "parsed.Run") || !slices.Equal(derived.subtests, []string{"queue.CloseKeepsValues", "strings.RepeatLength", "time.TimeRoundTrips"}) {
		t.Errorf("a derived law through Run: errors %v, subtests %v", derived.errors, derived.subtests)
	}
	given := map[string]string{"time TimeRoundTrips": "TimeRoundTrips(v Time) { true }"}
	agree := &fakeRunner{name: "TestRunner"}
	run(agree, rows, func(key string) (string, bool) {
		if s, ok := given[key]; ok {
			return s, true
		}
		return source(key)
	}, laws)
	if len(agree.errors) != 0 {
		t.Errorf("the derived law's statement given (RunWith): errors %v", agree.errors)
	}
	laws["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() != v }", Check: check}
	differ := &fakeRunner{name: "TestRunner"}
	run(differ, rows, source, laws)
	if len(differ.errors) != 2 {
		t.Errorf("a changed statement: errors %v, want 2", differ.errors)
	}
	lawless := &fakeRunner{name: "TestLawless"}
	run(lawless, rows, source, map[string]Law{})
	if len(lawless.errors) != 1 || len(lawless.subtests) != 0 {
		t.Errorf("a runner no line names: errors %v, subtests %v", lawless.errors, lawless.subtests)
	}
}

// Compare passes a runner only when it restates exactly its lines' laws,
// each with the source's statement (up to the formatter's spacing, Same)
// and a check. Mutants it kills: compare-missing-restatement-passes,
// compare-statement-unchecked.
func TestCompare(t *testing.T) {
	rows, err := Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	rows = slices.DeleteFunc(rows, func(r Row) bool { return r.Key() == "time TimeRoundTrips" })
	check := func(*testing.T) {}
	good := func() map[string]Law {
		return map[string]Law{
			"queue CloseKeepsValues": {Statement: "CloseKeepsValues(v int) {\n\tPop() == v\n}", Check: check},
			"strings RepeatLength":   {Statement: "RepeatLength(n int) { n < 0 || n > 64 }", Check: check},
		}
	}
	if problems := Compare("TestRunner", rows, statements, good()); len(problems) != 0 {
		t.Fatalf("the agreeing runner: %q", problems)
	}
	for _, c := range []struct {
		name   string
		runner string
		edit   func(map[string]Law)
		want   string
	}{
		{"missing", "TestRunner", func(l map[string]Law) { delete(l, "strings RepeatLength") }, "strings RepeatLength is listed to run in TestRunner, which does not restate it"},
		{"extra", "TestRunner", func(l map[string]Law) { l["os Process.GetenvIsStable"] = Law{Statement: "x", Check: check} }, "TestRunner restates os Process.GetenvIsStable, which no line of"},
		{"statement", "TestRunner", func(l map[string]Law) {
			l["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() != v }", Check: check}
		}, "TestRunner restates queue CloseKeepsValues as"},
		{"no check", "TestRunner", func(l map[string]Law) {
			l["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() == v }"}
		}, "restates queue CloseKeepsValues with no check"},
		{"unnamed runner", "TestLawless", func(map[string]Law) {}, "names the runner TestLawless"},
	} {
		laws := good()
		c.edit(laws)
		problems := Compare(c.runner, rows, statements, laws)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
			t.Errorf("%s: problems %q, want one with %q", c.name, problems, c.want)
		}
	}
}

// statementSource holds a law in a comment, two member laws of one name,
// brackets inside literals, an interpolation and a comment, nested blocks
// and a rewrite law.
const statementSource = "// law Hidden() { true }\n" +
	"type Box {\n\tGet() int,\n\n\tlaw Stable() { Get() == Get() },\n" +
	"\tlaw Braces(s string, m Map[string, string]) { s + \"}\" + `{` + \"\\(m[\"}\"])\" + string('{') != \"\" },\n}\n\n" +
	"type Other[T comparable] {\n\tGet() T,\n\n\tlaw Stable() {\n\t\tlet x = Get()\n\t\tx == Get()\n\t},\n}\n\n" +
	"type Level int\n\n" +
	"law Nested(xs []int) { for x in xs { if x > 0 { return true } } false }\n\n" +
	"law Swap(a int, b int) { a + b } == { b + a }\n\n" +
	"law Guarded(n int) /* { */ { n == n }\n"

// Statement cuts a law's text from its name to the brace that closes its
// body, counting brackets outside comments and literals, and finds a
// member law in its own type. Mutants it kills:
// statement-stops-at-first-brace, statement-counts-braces-in-strings,
// statement-ignores-the-type.
func TestStatement(t *testing.T) {
	for _, c := range []struct{ law, want string }{
		{"Box.Stable", "Stable() { Get() == Get() }"},
		{"Box.Braces", "Braces(s string, m Map[string, string]) { s + \"}\" + `{` + \"\\(m[\"}\"])\" + string('{') != \"\" }"},
		{"Other.Stable", "Stable() { let x = Get() x == Get() }"},
		{"Nested", "Nested(xs []int) { for x in xs { if x > 0 { return true } } false }"},
		{"Swap", "Swap(a int, b int) { a + b } == { b + a }"},
		{"Guarded", "Guarded(n int) /* { */ { n == n }"},
		{"Hidden", ""},
		{"Stable", ""},
		{"Level.Stable", ""},
	} {
		got, ok := Statement(statementSource, c.law)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("Statement(%s) = %q, %v, want %q", c.law, got, ok, c.want)
		}
	}
}

// Same holds a law and its reformatted text the same, and a law whose
// tokens or literals changed different. Mutants it kills
// (internal/lawledger/testdata/mutants.json): same-counts-all-space,
// same-joins-tokens, same-drops-op-space, same-strips-literals,
// same-ends-literal-at-escape.
func TestSame(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"F(xs []int) { len(xs) + 1 > 0 }", "F(xs []int) {\n\tlen(xs)+1 > 0\n}", true},
		{"F(s string) { raw + \"\\n\" + s }", "F(s string) { raw+\"\\n\"+s }", true},
		{"F() { let it = 1 it }", "F() { letit = 1 it }", false},
		{"F(a int, b int) { a - -b }", "F(a int, b int) { a --b }", false},
		{"F() { \"a, b\" == x }", "F() { \"a,b\" == x }", false},
		{"F() { \"a\\\" b\" == x }", "F() { \"a\\\"b\" == x }", false},
	} {
		if got := Same(c.a, c.b); got != c.same {
			t.Errorf("Same(%q, %q) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}
