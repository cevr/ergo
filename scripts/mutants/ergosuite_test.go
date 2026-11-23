package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestErgoTestKillerIsRunByErgoTests: a killer DIR:Name whose DIR is an
// Ergo package with no Go test (compiler/syntax) is the Ergo test Name,
// run at check time by scripts/ergotests (ST-0). Before, the runner took
// DIR for a Go package and refused the killer, so a row an Ergo test alone
// kills needed a Go bridge test. A name no test of DIR declares is still
// refused, and a directory with Go tests stays a Go suite.
//
// Mutants (testdata/mutants.json): st0-ergo-dir-is-go,
// st0-ergo-test-unchecked, lawkiller-law-undeclared,
// lawkiller-ergo-tool-dropped, lawkiller-provider-law-unresolved.
func TestErgoTestKillerIsRunByErgoTests(t *testing.T) {
	root := t.TempDir()
	for f, text := range map[string]string{
		"compiler/syntax/scan.ergo": "func scan() int { 1 }\n\ntest ScanOne {\n\tassert scan() == 1\n}\n\nlaw ScanIsOne(n int) { scan() == 1 }\n",
		"mixed/m.ergo":              "test Mixed {\n\tassert true\n}\n",
		"mixed/m_test.go":           "package mixed\n\nfunc TestGo(t *testing.T) {}\n",
		// A host module's Ergo package (hosts/mygo/desktop): its law kills
		// from the repository root, decided by ergo test (LAW-KILLER).
		"hosts/m/go.mod":   "module hosts.test/m\n",
		"hosts/m/d/d.ergo": "func fit(n int) bool { n >= 0 }\n\nlaw Fits(n int) { fit(n * n) }\n",
		// A provider package runs the member laws of the operation type
		// it imports, under their qualified name (LAW-RUNS-CREDIT-PROVIDERS).
		"internal/sketch/std/w/w.ergo":        "type Store {\n\tGet() int,\n\tlaw Kept(n int) { true },\n}\n",
		"internal/sketch/std/w/memory/m.ergo": "import w\n\nfunc get() int { 1 }\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ledger := func(killers ...string) []Job {
		var ms []*Mutant
		for i, k := range killers {
			ms = append(ms, &Mutant{Name: "m" + string(rune('a'+i)), File: "compiler/syntax/scan.ergo", Killers: []string{k}})
		}
		return []Job{{Path: ledgerPath(root, filepath.Join(root, "compiler", "syntax")), Ledger: &Ledger{Mutants: ms}}}
	}
	_, tools, suites, err := suitesFor(root, ledger("compiler/syntax:ScanOne", "mixed:TestGo", "compiler/syntax:law:ScanIsOne"))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == "compiler/syntax" })
	if i < 0 {
		t.Fatalf("suites %+v: no compiler/syntax suite", suites)
	}
	s := suites[i]
	// A law is a killer too, named law:L (LAW-KILLER).
	if s.Package != "" || !slices.Equal(s.Command, []string{"{bin:ergotests}", "-ergo", "{bin:ergo}", "-killer", "{test}", "{root}/compiler/syntax"}) || !slices.Equal(s.Named, []string{"ScanOne", "law:ScanIsOne"}) {
		t.Errorf("the Ergo package's suite is %+v; want ergotests running ScanOne and law:ScanIsOne", s)
	}
	for _, want := range []Tool{{Name: "ergotests", Package: "./scripts/ergotests"}, {Name: "ergo", Package: "./cmd/ergo"}} {
		if !slices.ContainsFunc(tools, func(u Tool) bool { return u.Name == want.Name && u.Package == want.Package }) {
			t.Errorf("tools %+v: %s is not built", tools, want.Name)
		}
	}
	_, _, hosted, err := suitesFor(root, ledger("hosts/m/d:law:Fits"))
	if err != nil {
		t.Fatalf("a host module's law killer: %v", err)
	}
	if j := slices.IndexFunc(hosted, func(s Suite) bool { return s.Name == "hosts/m/d" }); j < 0 || hosted[j].Module != "" || !slices.Contains(hosted[j].Command, "{root}/hosts/m/d") || !slices.Equal(hosted[j].Named, []string{"law:Fits"}) {
		t.Errorf("a host module's law killer: suites %+v; want ergotests running law:Fits in {root}/hosts/m/d", hosted)
	}
	if j := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == "mixed" }); j < 0 || suites[j].Package != "./mixed" {
		t.Errorf("a directory with Go tests is not a Go suite: %+v", suites)
	}
	for _, id := range []string{"compiler/syntax:ScanTwo", "compiler/syntax:law:ScanIsTwo", "compiler/syntax:law:ScanOne"} {
		if _, _, _, err := suitesFor(root, ledger(id)); err == nil || !strings.Contains(err.Error(), "names no test") {
			t.Errorf("killer %s, which the package does not declare: err %v; want it refused", id, err)
		}
	}
	// pkg.T.L: the imported operation type's law, which the provider runs.
	if _, _, _, err := suitesFor(root, ledger("internal/sketch/std/w/memory:law:w.Store.Kept")); err != nil {
		t.Errorf("a provider's run of its operation type's law (law:w.Store.Kept): %v; want it resolved", err)
	}
	for _, id := range []string{"internal/sketch/std/w/memory:law:w.Store.Gone", "internal/sketch/std/w/memory:law:v.Store.Kept"} {
		if _, _, _, err := suitesFor(root, ledger(id)); err == nil || !strings.Contains(err.Error(), "names no test") {
			t.Errorf("killer %s, a law no imported package declares: err %v; want it refused", id, err)
		}
	}
}
