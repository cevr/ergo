package lower

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// graphLawStatements are std graph's and std codegraph's laws as the
// ledger states them, by ledger key.
var graphLawStatements = map[string]string{
	"graph ChainIsAPath":                     `ChainIsAPath(seed int, size int, min int) { chainIsAPath(seed, size, min) }`,
	"graph EveryCycleIsAClosedPath":          `EveryCycleIsAClosedPath(seed int, size int) { everyCycleIsAClosedPath(seed, size) }`,
	"graph FanSumsToEdges":                   `FanSumsToEdges(seed int, size int) { fanSumsToEdges(seed, size) }`,
	"graph NoCycleGivesAnOrder":              `NoCycleGivesAnOrder(seed int, size int) { noCycleGivesAnOrder(seed, size) }`,
	"graph PathIsAShortestPath":              `PathIsAShortestPath(seed int, size int) { pathIsAShortestPath(seed, size) }`,
	"graph ReachIsTheClosure":                `ReachIsTheClosure(seed int, size int) { reachIsTheClosure(seed, size) }`,
	"graph ReduceKeepsReach":                 `ReduceKeepsReach(seed int, size int) { reduceKeepsReach(seed, size) }`,
	"codegraph AffectedGrowsWithChanges":     `AffectedGrowsWithChanges(seed int, size int, x int, y int) { affectedGrowsWithChanges(seed, size, x, y) }`,
	"codegraph ChangedIsTheEdit":             `ChangedIsTheEdit(seed int, size int, x int) { changedIsTheEdit(seed, size, x) }`,
	"codegraph ChainsAreImports":             `ChainsAreImports(seed int, size int, min int) { chainsAreImports(seed, size, min) }`,
	"codegraph CohesionIsBounded":            `CohesionIsBounded(seed int, size int) { cohesionIsBounded(seed, size) }`,
	"codegraph CyclesAreImportCycles":        `CyclesAreImportCycles(seed int, size int) { cyclesAreImportCycles(seed, size) }`,
	"codegraph FanCountsImports":             `FanCountsImports(seed int, size int) { fanCountsImports(seed, size) }`,
	"codegraph HiddenHasNoImport":            `HiddenHasNoImport(seed int, size int) { hiddenHasNoImport(seed, size) }`,
	"codegraph ReducedImportsKeepReach":      `ReducedImportsKeepReach(seed int, size int) { reducedImportsKeepReach(seed, size) }`,
	"codegraph UpIsEmptyForAnOrder":          `UpIsEmptyForAnOrder(seed int, size int) { upIsEmptyForAnOrder(seed, size) }`,
	"codegraph WhyExistsIffAffected":         `WhyExistsIffAffected(seed int, size int, x int) { whyExistsIffAffected(seed, size, x) }`,
	"codegraph WithoutTestsDropsTestImports": `WithoutTestsDropsTestImports(seed int, size int) { withoutTestsDropsTestImports(seed, size) }`,
	"codegraph WhyIsAPath":                   `WhyIsAPath(seed int, size int, x int, y int) { whyIsAPath(seed, size, x, y) }`,
}

// TestStdGraphLawsRun runs std graph's and std codegraph's laws as ergo
// test runs them: each package's own sources are checked as a program and
// its test build runs its top-level laws, each a seeded property against
// a slow reference (a transitive closure, Floyd-Warshall distances, a
// scan of the rows); lawledger.Run holds that this test runs exactly the
// ledger's rows of both. It builds Go because a law runs only in the
// built test binary. Mutants graph-* and codegraph-*
// (internal/sketch/lower/testdata/mutants.json).
func TestStdGraphLawsRun(t *testing.T) {
	needsBuild(t)
	var progs []*lowered
	for _, pkg := range []string{"graph", "codegraph"} {
		progs = append(progs, stdPackageAsProgram(t, pkg))
	}
	exes := buildAll(t, progs, true)
	if exes == nil {
		return
	}
	stdout := ""
	for _, p := range progs {
		out, stderr, code := runExeArgs(t, exes[p.name], "--seed", "1")
		if code != 0 {
			t.Errorf("%s's laws failed: exit %d\n%s\n%s", p.name, code, out, firstLines(stderr, 20))
		}
		stdout += out
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range graphLawStatements {
		_, name, _ := strings.Cut(key, " ")
		line := "ok law " + name
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + ` \(\d+ cases\)$`).MatchString(stdout) {
				t.Errorf("no line %q:\n%s", line, stdout)
			}
		}}
	}
	lawledger.Run(t, laws)
}

// stdPackageAsProgram is the std package pkg's sources checked as one
// program and lowered with its tests, named "std"+pkg.
func stdPackageAsProgram(t *testing.T, pkg string) *lowered {
	t.Helper()
	sources, ok := stdlib.Package(pkg)
	if !ok {
		t.Fatalf("no std %s", pkg)
	}
	var names []string
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	prog := &lowered{name: "std" + pkg}
	var units []Source
	var parsed []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("%s/%s: %v", pkg, name, err)
		}
		if prog.file == nil {
			prog.file, prog.src = f, sources[name]
		}
		parsed = append(parsed, f)
		units = append(units, Source{Path: "/ergo-test/" + name, Text: sources[name]})
	}
	prog.pkg, prog.diags = check.Check("main", parsed, nil)
	if len(prog.diags) > 0 {
		t.Fatalf("std %s as a program: %v", pkg, prog.diags)
	}
	files, err := LowerTests(Unit{Package: prog.pkg, Files: parsed, Sources: units})
	if err != nil {
		t.Fatal(err)
	}
	prog.files = files
	return prog
}
