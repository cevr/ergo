package lawledger

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const doc = "# std\n\n## Std laws\n\n| Package | Law | Statement | Runner | State |\n| --- | --- | --- | --- | --- |\n" +
	"| `queue` | `CloseKeepsValues` | `CloseKeepsValues(v int) { Pop() == v }` | `TestRunner` | tested |\n" +
	"| `strings` | `RepeatLength` | `RepeatLength(n int) { n < 0 \\|\\| n > 64 }` | `TestRunner` | tested |\n" +
	"| `os` | `Process.GetenvIsStable` | `GetenvIsStable(key string) { Getenv(key) == Getenv(key) }` | `TestOther` | tested |\n" +
	"\n## Next\n\n| a | b |\n"

// Parse reads the Std laws section only, unquotes each cell and unescapes
// \|; a wrong header or a short row is an error.
func TestParse(t *testing.T) {
	rows, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	if r := rows[1]; r.Key() != "strings RepeatLength" || r.Statement != "RepeatLength(n int) { n < 0 || n > 64 }" || r.Runner != "TestRunner" || r.State != "tested" || r.Line != 8 {
		t.Errorf("row = %+v", r)
	}
	if rows[2].Key() != "os Process.GetenvIsStable" {
		t.Errorf("member key = %q", rows[2].Key())
	}
	if _, err := Parse(strings.Replace(doc, "| Runner |", "| Test |", 1)); err == nil {
		t.Error("a wrong header was read")
	}
	if _, err := Parse(strings.Replace(doc, " | `TestOther` | tested |", " |", 1)); err == nil || !strings.Contains(err.Error(), "stdlib.md:9") {
		t.Errorf("a short row: err = %v", err)
	}
	if _, err := Parse("# no table\n"); err == nil {
		t.Error("a text with no ledger was read")
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

// run fails the runner on each disagreement with the ledger, and starts
// one subtest per law it restates, named after the key, in key order.
func TestRunReportsAndRunsEachLaw(t *testing.T) {
	rows, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	check := func(*testing.T) {}
	laws := map[string]Law{
		"strings RepeatLength":   {Statement: "RepeatLength(n int) { n < 0 || n > 64 }", Check: check},
		"queue CloseKeepsValues": {Statement: "CloseKeepsValues(v int) { Pop() == v }", Check: check},
	}
	agree := &fakeRunner{name: "TestRunner"}
	run(agree, rows, laws)
	if len(agree.errors) != 0 || !slices.Equal(agree.subtests, []string{"queue.CloseKeepsValues", "strings.RepeatLength"}) {
		t.Errorf("agreeing runner: errors %v, subtests %v", agree.errors, agree.subtests)
	}
	laws["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() != v }", Check: check}
	differ := &fakeRunner{name: "TestRunner"}
	run(differ, rows, laws)
	if len(differ.errors) != 1 {
		t.Errorf("a changed statement: errors %v, want 1", differ.errors)
	}
	lawless := &fakeRunner{name: "TestLawless"}
	run(lawless, rows, map[string]Law{})
	if len(lawless.errors) != 1 || len(lawless.subtests) != 0 {
		t.Errorf("a runner no row names: errors %v, subtests %v", lawless.errors, lawless.subtests)
	}
}

// Compare passes a runner only when it restates exactly its rows, each
// with the row's statement (white space free) and a check.
func TestCompare(t *testing.T) {
	rows, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	check := func(*testing.T) {}
	good := func() map[string]Law {
		return map[string]Law{
			"queue CloseKeepsValues": {Statement: "CloseKeepsValues(v int) {\n\tPop() == v\n}", Check: check},
			"strings RepeatLength":   {Statement: "RepeatLength(n int) { n < 0 || n > 64 }", Check: check},
		}
	}
	if problems := Compare("TestRunner", rows, good()); len(problems) != 0 {
		t.Fatalf("the agreeing runner: %q", problems)
	}
	for _, c := range []struct {
		name   string
		runner string
		edit   func(map[string]Law)
		want   string
	}{
		{"missing", "TestRunner", func(l map[string]Law) { delete(l, "strings RepeatLength") }, "the ledger runs strings RepeatLength in TestRunner, which does not restate it"},
		{"extra", "TestRunner", func(l map[string]Law) { l["os Process.GetenvIsStable"] = Law{Statement: "x", Check: check} }, "TestRunner restates os Process.GetenvIsStable, which no row runs in it"},
		{"statement", "TestRunner", func(l map[string]Law) {
			l["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() != v }", Check: check}
		}, "TestRunner restates queue CloseKeepsValues as"},
		{"no check", "TestRunner", func(l map[string]Law) {
			l["queue CloseKeepsValues"] = Law{Statement: "CloseKeepsValues(v int) { Pop() == v }"}
		}, "restates queue CloseKeepsValues with no check"},
		{"unnamed runner", "TestLawless", func(map[string]Law) {}, "no row of the Std laws table names the runner TestLawless"},
	} {
		laws := good()
		c.edit(laws)
		problems := Compare(c.runner, rows, laws)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
			t.Errorf("%s: problems %q, want one with %q", c.name, problems, c.want)
		}
	}
}
