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
// st0-ergo-test-unchecked, lawkiller-law-undeclared.
func TestErgoTestKillerIsRunByErgoTests(t *testing.T) {
	root := t.TempDir()
	for f, text := range map[string]string{
		"compiler/syntax/scan.ergo": "func scan() int { 1 }\n\ntest ScanOne {\n\tassert scan() == 1\n}\n\nlaw ScanIsOne(n int) { scan() == 1 }\n",
		"mixed/m.ergo":              "test Mixed {\n\tassert true\n}\n",
		"mixed/m_test.go":           "package mixed\n\nfunc TestGo(t *testing.T) {}\n",
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
	if s.Package != "" || !slices.Equal(s.Command, []string{"{bin:ergotests}", "-killer", "{test}", "{root}/compiler/syntax"}) || !slices.Equal(s.Named, []string{"ScanOne", "law:ScanIsOne"}) {
		t.Errorf("the Ergo package's suite is %+v; want ergotests running ScanOne and law:ScanIsOne", s)
	}
	if !slices.ContainsFunc(tools, func(u Tool) bool { return u.Name == "ergotests" && u.Package == "./scripts/ergotests" }) {
		t.Errorf("tools %+v: ergotests is not built", tools)
	}
	if j := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == "mixed" }); j < 0 || suites[j].Package != "./mixed" {
		t.Errorf("a directory with Go tests is not a Go suite: %+v", suites)
	}
	for _, id := range []string{"compiler/syntax:ScanTwo", "compiler/syntax:law:ScanIsTwo", "compiler/syntax:law:ScanOne"} {
		if _, _, _, err := suitesFor(root, ledger(id)); err == nil || !strings.Contains(err.Error(), "names no test") {
			t.Errorf("killer %s, which the package does not declare: err %v; want it refused", id, err)
		}
	}
}
