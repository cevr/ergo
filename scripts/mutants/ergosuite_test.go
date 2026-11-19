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
// refused, and a directory with Go tests stays a Go suite. A killer may
// name a top-level law of DIR (hosts/mygo/desktop:FitIsBroken), which
// ergotests selects as "law Name": before, the runner refused it ("names
// no test"), so those rows had no killer (b23 fallout, ru17-fit-*).
//
// Mutants (testdata/mutants.json): st0-ergo-dir-is-go,
// st0-ergo-test-unchecked, ergo-law-killer-refused,
// ergo-law-killer-unselected.
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
	_, tools, suites, err := suitesFor(root, ledger("compiler/syntax:ScanOne", "compiler/syntax:ScanIsOne", "mixed:TestGo"))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == "compiler/syntax" })
	if i < 0 {
		t.Fatalf("suites %+v: no compiler/syntax suite", suites)
	}
	s := suites[i]
	if s.Package != "" || !slices.Equal(s.Command, []string{"{bin:ergotests}", "-run", "^(law )?{test}$", "{root}/compiler/syntax"}) || !slices.Equal(s.Named, []string{"ScanOne", "ScanIsOne"}) {
		t.Errorf("the Ergo package's suite is %+v; want ergotests running ScanOne and the law ScanIsOne", s)
	}
	if !slices.ContainsFunc(tools, func(u Tool) bool { return u.Name == "ergotests" && u.Package == "./scripts/ergotests" }) {
		t.Errorf("tools %+v: ergotests is not built", tools)
	}
	if j := slices.IndexFunc(suites, func(s Suite) bool { return s.Name == "mixed" }); j < 0 || suites[j].Package != "./mixed" {
		t.Errorf("a directory with Go tests is not a Go suite: %+v", suites)
	}
	_, _, _, err = suitesFor(root, ledger("compiler/syntax:ScanTwo"))
	if err == nil || !strings.Contains(err.Error(), "names no test") {
		t.Errorf("a killer no Ergo test declares: err %v; want it refused", err)
	}
}
