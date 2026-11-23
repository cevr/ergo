package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindingExcusesOnlyItsOwnAndGoesStale: an expected finding turns the
// one problem it names into a red-by-record note, and a finding no problem
// matches is a layout error, so the file empties itself as the owed units
// land.
//
// Mutants: findings-stale-never (stale reports nothing),
// findings-match-any-provider (matches drops the provider),
// findings-match-any-package (matches drops the package).
func TestFindingExcusesOnlyItsOwnAndGoesStale(t *testing.T) {
	t.Parallel()
	fs := &findings{list: []*finding{
		{example: "shop", pkg: "orders", code: "S108", law: "Store.Keeps", provider: "Disk", unit: "OWED-A", line: 1},
		{example: "shop", pkg: "orders", code: "S108", law: "Store.Gone", provider: "Disk", unit: "OWED-B", line: 2},
	}}
	at := func(pkg, law, provider string) string {
		return "examples/shop: " + pkg + lawCheck + "S108 at orders/disk.ergo:3:9: law " + law + ", declared at store.ergo:4:6, cannot run at " + provider + ": its values have no generator"
	}
	rest, notes, _ := fs.apply([]string{
		at("orders", "Store.Keeps", "Disk"),
		at("orders", "Store.Keeps", "Cloud"),
		at("billing", "Store.Keeps", "Disk"),
	})
	if len(notes) != 1 || !strings.Contains(notes[0], "owed OWED-A") {
		t.Errorf("notes = %q; want one red-by-record note owed OWED-A", notes)
	}
	if len(rest) != 2 {
		t.Errorf("rest = %q; want the problems at Cloud and in billing, which no line names", rest)
	}
	stale := fs.stale()
	if len(stale) != 1 || !strings.Contains(stale[0], "Store.Gone") {
		t.Errorf("stale = %q; want the line of Store.Gone, which no problem matched", stale)
	}
}

// A recorded S108 blocks its package and the example's executable cases,
// not an independently runnable law. The fake compiler prints a native
// refutation after check-time evaluation skips that law.
// Mutant: findings-hide-independent-tests.
func TestRecordedFindingDoesNotHideALawRefutation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const dir = "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "func main() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	f.write(dir+"/blocked/shape.ergo", `
type Shape {
	Area() int,
	law Positive() { Area() > 0 },
}
type Fn { f func(int) int } implements Shape
func Area(s Fn) int { s.f(1) }
`)
	f.write(findingsFile, "walker blocked S108 Shape.Positive Fn OWED-SHAPE\n")
	const refutation = `FAIL law Receipt (case 1 of 100, seed 1): law refuted at view.ergo:2:1: false`
	f.write(dir+"/view/view.ergo", `func Ready() bool { true }
law Receipt() { !Ready() }
// CHECKJSON {"checked":true,"evaluation":{"schemaVersion":1,"status":"skipped","obligations":[{"name":"law Receipt","status":"skipped","reason":"native fixture"}]}}
// NATIVE `+refutation+"\n")
	code, out := f.run("walker")
	if !strings.Contains(out, "red by record (owed OWED-SHAPE): examples/walker: blocked") {
		t.Fatalf("fixture did not match a recorded finding: exit %d\n%s", code, out)
	}
	if code != 1 || !strings.Contains(out, refutation) {
		t.Fatalf("recorded finding hid a refuted sibling law: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "red by record: walker: cases not run (1 cases)") ||
		!strings.Contains(out, "red by record: walker: test blocked not run") {
		t.Fatalf("runner did not report the blocked legs:\n%s", out)
	}
	for _, call := range readCalls(t, f) {
		if strings.HasPrefix(call, "build ") || strings.HasSuffix(call, "/...") ||
			strings.HasSuffix(call, "/blocked") {
			t.Errorf("runner attempted a blocked leg: %s", call)
		}
	}
}

// TestExpectedFindingsNameOwedUnits: each line of the expected findings
// reads as six fields and names an owed unit examples/README.md describes
// under "Red by record"; it pins no count.
func TestExpectedFindingsNameOwedUnits(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fs, problems := readFindings(root)
	for _, p := range problems {
		t.Error(p)
	}
	readme, err := os.ReadFile(filepath.Join(root, "examples", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, ledger, ok := strings.Cut(string(readme), "## Red by record")
	if !ok {
		t.Fatal(`examples/README.md has no "## Red by record" section, the ledger of owed units`)
	}
	ledger, _, _ = strings.Cut(ledger, "\n## ")
	for _, f := range fs.list {
		if !strings.Contains(ledger, "**"+f.unit+"**") {
			t.Errorf("%s: unit %s is not in examples/README.md's Red by record", f, f.unit)
		}
	}
}
