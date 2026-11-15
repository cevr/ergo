package lower

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// codeLawStatements are std code's laws as the ledger states them, by
// ledger key.
var codeLawStatements = map[string]string{
	"code IndexIsScan":                  `IndexIsScan(seed int, size int) { indexIsScan(seed, size) }`,
	"code Reader.HandleRoundTrips":      `HandleRoundTrips(pick int) { handleRoundTrips(pick) }`,
	"code Reader.ReferencesAreSound":    `ReferencesAreSound(pick int, kinds []string) { referencesAreSound(pick, kinds) }`,
	"code Reader.ReferencesAreComplete": `ReferencesAreComplete() { referencesAreComplete() }`,
	"code Reader.PagesPartition":        `PagesPartition(pick int, size int) { pagesPartition(pick, size) }`,
}

// TestStdCodeLawsRun runs std code's laws as ergo test runs them: the
// package's own sources are checked as a program, so its test build runs
// the member laws of Reader against both of its providers, Index and Scan
// (each built by NewIndex or NewScan from generated tables), and the
// top-level law IndexIsScan; lawledger.Run holds that this test runs
// exactly the ledger's rows of std code. It builds Go because a law runs
// only in the built test binary. Mutants code-short-first,
// code-page-repeats-a-row, code-kinds-ignored
// (internal/sketch/lower/testdata/mutants.json).
func TestStdCodeLawsRun(t *testing.T) {
	needsBuild(t)
	sources, ok := std.Package("code")
	if !ok {
		t.Fatal("no std code")
	}
	var names []string
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	prog := &lowered{name: "stdcode"}
	var units []Source
	var parsed []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("code/%s: %v", name, err)
		}
		if prog.file == nil {
			prog.file, prog.src = f, sources[name]
		}
		parsed = append(parsed, f)
		units = append(units, Source{Path: "/ergo-test/" + name, Text: sources[name]})
	}
	prog.pkg, prog.diags = check.Check("main", parsed, nil)
	if len(prog.diags) > 0 {
		t.Fatalf("std code as a program: %v", prog.diags)
	}
	files, err := LowerTests(Unit{Package: prog.pkg, Files: parsed, Sources: units})
	if err != nil {
		t.Fatal(err)
	}
	prog.files = files
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdcode"], "--seed", "1")
	if code != 0 {
		t.Errorf("std code's laws failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range codeLawStatements {
		_, law, _ := strings.Cut(key, " ")
		lines := []string{"ok law " + law}
		if strings.Contains(law, ".") {
			lines = []string{"ok law " + law + " for Index", "ok law " + law + " for Scan"}
		}
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			for _, line := range lines {
				if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + ` \(\d+ cases\)$`).MatchString(stdout) {
					t.Errorf("no line %q:\n%s", line, stdout)
				}
			}
		}}
	}
	lawledger.Run(t, laws)
}
