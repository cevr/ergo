package lower

import (
	"regexp"
	"slices"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// mappedLawStatements are std fs/mapped's laws as the ledger states them,
// by ledger key.
var mappedLawStatements = map[string]string{
	"fs/mapped Maps.BoundedIsTheText":    `BoundedIsTheText(text string) { boundedIsTheText(text) }`,
	"fs/mapped Maps.OverLimitIsTooLarge": `OverLimitIsTooLarge(text string) { overLimitIsTooLarge(text) }`,
	"fs/mapped Maps.DirIsNotRegular":     `DirIsNotRegular(limit int) { dirIsNotRegular(limit) }`,
	"fs/mapped Maps.ReplacedIsNotSame":   `ReplacedIsNotSame(a string, b string) { replacedIsNotSame(a, b) }`,
}

// TestStdMappedLawsRun runs std fs/mapped's laws as ergo test runs them:
// the package's sources are checked as a program, so its test build runs
// Maps's member laws under OnTestHost, on fs.MapBounded and fs.SameAs in
// the case's folder (CAP-FILE); lawledger.Run holds that this test runs
// exactly the ledger's rows of std fs/mapped. It builds Go because a law
// runs only in the built test binary. Mutants file-limit-unchecked,
// file-regular-unchecked, file-identity-ignored (runtime/v1 ledger).
func TestStdMappedLawsRun(t *testing.T) {
	needsBuild(t)
	sources, ok := stdlib.Package("fs/mapped")
	if !ok {
		t.Fatal("no std fs/mapped")
	}
	var names []string
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	prog := &lowered{name: "stdmapped"}
	var units []Source
	var parsed []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("fs/mapped/%s: %v", name, err)
		}
		if prog.file == nil {
			prog.file, prog.src = f, sources[name]
		}
		parsed = append(parsed, f)
		units = append(units, Source{Path: "/ergo-test/" + name, Text: sources[name]})
	}
	prog.pkg, prog.diags = check.Check("main", parsed, nil)
	if len(prog.diags) > 0 {
		t.Fatalf("std fs/mapped as a program: %v", prog.diags)
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
	stdout, stderr, code := runExeArgs(t, exes["stdmapped"], "--seed", "1")
	if code != 0 {
		t.Errorf("std fs/mapped's laws failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range mappedLawStatements {
		law := key[len("fs/mapped "):]
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			line := "ok law " + law + " under OnTestHost"
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + ` \(\d+ cases\)$`).MatchString(stdout) {
				t.Errorf("no line %q:\n%s", line, stdout)
			}
		}}
	}
	lawledger.Run(t, laws)
}
