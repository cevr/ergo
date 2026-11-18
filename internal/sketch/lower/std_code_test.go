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

// codeLawStatements are std code's laws as the ledger states them, by
// ledger key.
var codeLawStatements = map[string]string{
	"code ApiDiffBreaksOnANewTake":           `ApiDiffBreaksOnANewTake(seed int, pick int, how int) { apiDiffBreaksOnANewTake(seed, pick, how) }`,
	"code ApiDiffOfItselfIsEmpty":            `ApiDiffOfItselfIsEmpty(seed int) { len(ApiDiff(allContracts(seed), allContracts(seed))) == 0 }`,
	"code AppliedIsPlanned":                  `AppliedIsPlanned(seed int, pick int, word int) { appliedIsPlanned(seed, pick, word) }`,
	"code ApplyIsSafe":                       `ApplyIsSafe(seed int, mode int, at int, out int) { applyIsSafe(seed, mode, at, out) }`,
	"code CanonicalStaysCanonical":           `CanonicalStaysCanonical(seed int, pick int, word int) { canonicalStaysCanonical(seed, pick, word) }`,
	"code Checker.CheckIsDeterministic":      `CheckIsDeterministic(seed int) { checkIsDeterministic(seed) }`,
	"code Checker.FormatIsIdempotent":        `FormatIsIdempotent(text string) { formatIsIdempotent(text) }`,
	"code DescribeParsesBack":                `DescribeParsesBack(pick int, word int, text string) { describeParsesBack(pick, word, text) }`,
	"code EqualEditsOnce":                    `EqualEditsOnce(text string, at int) { equalEditsOnce(text, at) }`,
	"code FixesCycleIsRefused":               `FixesCycleIsRefused(seed int) { fixesCycleIsRefused(seed) }`,
	"code Files.ExchangeSwaps":               `ExchangeSwaps(x string, y string) { exchangeSwaps(x, y) }`,
	"code Files.WriteThenRead":               `WriteThenRead(x string) { writeThenRead(x) }`,
	"code ImpactIsTheChangedContracts":       `ImpactIsTheChangedContracts(seed int, pick int, word int) { impactIsTheChangedContracts(seed, pick, word) }`,
	"code IndexIsScan":                       `IndexIsScan(seed int, size int) { indexIsScan(seed, size) }`,
	"code OverlapIsRefused":                  `OverlapIsRefused(seed int) { overlapIsRefused(seed) }`,
	"code OverlayDiagnosticBlocks":           `OverlayDiagnosticBlocks(seed int, pick int) { overlayDiagnosticBlocks(seed, pick) }`,
	"code Planner.PlanIsDeterministic":       `PlanIsDeterministic(pick int, word int) { planIsDeterministic(pick, word) }`,
	"code Planner.PlanOfIsThePlan":           `PlanOfIsThePlan(pick int, word int) { planOfIsThePlan(pick, word) }`,
	"code PlanOfAppliedIsEmpty":              `PlanOfAppliedIsEmpty(seed int, pick int, word int) { planOfAppliedIsEmpty(seed, pick, word) }`,
	"code PlanWritesNothing":                 `PlanWritesNothing(seed int, pick int, word int) { planWritesNothing(seed, pick, word) }`,
	"code Reader.CallsAreSteps":              `CallsAreSteps(pick int, depth int, inbound bool) { callsAreSteps(pick, depth, inbound) }`,
	"code Reader.HandleRoundTrips":           `HandleRoundTrips(pick int) { handleRoundTrips(pick) }`,
	"code Reader.MachineArmsStopAtAWholeArm": `MachineArmsStopAtAWholeArm(pick int) { machineArmsStopAtAWholeArm(pick) }`,
	"code Reader.PagesPartition":             `PagesPartition(pick int, size int) { pagesPartition(pick, size) }`,
	"code Reader.ReachChainsAreCalls":        `ReachChainsAreCalls(pick int) { reachChainsAreCalls(pick) }`,
	"code Reader.ReferencesAreComplete":      `ReferencesAreComplete() { referencesAreComplete() }`,
	"code Reader.ReferencesAreSound":         `ReferencesAreSound(pick int, kinds []string) { referencesAreSound(pick, kinds) }`,
	"code ReformatIsSameCode":                `ReformatIsSameCode(seed int, pick int) { reformatIsSameCode(seed, pick) }`,
	"code RenameIsAlpha":                     `RenameIsAlpha(seed int, pick int, to int) { renameIsAlpha(seed, pick, to) }`,
	"code SecondApplyIsStale":                `SecondApplyIsStale(seed int, pick int, word int) { secondApplyIsStale(seed, pick, word) }`,
	"code StaleChangesNothing":               `StaleChangesNothing(seed int, pick int, word int, which int) { staleChangesNothing(seed, pick, word, which) }`,
	"code UntouchedKeepsBytes":               `UntouchedKeepsBytes(seed int, pick int, word int) { untouchedKeepsBytes(seed, pick, word) }`,
	"code Writer.UnknownPlanIsNoPlan":        `UnknownPlanIsNoPlan(id string) { unknownPlanIsNoPlan(id) }`,
}

// codeLawLines are the lines ergo test prints when each law holds: one
// per provider for a member law.
var codeLawLines = map[string][]string{
	"code ApiDiffBreaksOnANewTake":           {"ok law ApiDiffBreaksOnANewTake"},
	"code ApiDiffOfItselfIsEmpty":            {"ok law ApiDiffOfItselfIsEmpty"},
	"code AppliedIsPlanned":                  {"ok law AppliedIsPlanned"},
	"code ApplyIsSafe":                       {"ok law ApplyIsSafe"},
	"code CanonicalStaysCanonical":           {"ok law CanonicalStaysCanonical"},
	"code Checker.CheckIsDeterministic":      {"ok law Checker.CheckIsDeterministic for Model"},
	"code Checker.FormatIsIdempotent":        {"ok law Checker.FormatIsIdempotent for Model"},
	"code DescribeParsesBack":                {"ok law DescribeParsesBack"},
	"code EqualEditsOnce":                    {"ok law EqualEditsOnce"},
	"code FixesCycleIsRefused":               {"ok law FixesCycleIsRefused"},
	"code Files.ExchangeSwaps":               {"ok law Files.ExchangeSwaps for Disk", "ok law Files.ExchangeSwaps under OnTestHost"},
	"code Files.WriteThenRead":               {"ok law Files.WriteThenRead for Disk", "ok law Files.WriteThenRead under OnTestHost"},
	"code ImpactIsTheChangedContracts":       {"ok law ImpactIsTheChangedContracts"},
	"code IndexIsScan":                       {"ok law IndexIsScan"},
	"code OverlapIsRefused":                  {"ok law OverlapIsRefused"},
	"code OverlayDiagnosticBlocks":           {"ok law OverlayDiagnosticBlocks"},
	"code Planner.PlanIsDeterministic":       {"ok law Planner.PlanIsDeterministic for Session"},
	"code Planner.PlanOfIsThePlan":           {"ok law Planner.PlanOfIsThePlan for Session"},
	"code PlanOfAppliedIsEmpty":              {"ok law PlanOfAppliedIsEmpty"},
	"code PlanWritesNothing":                 {"ok law PlanWritesNothing"},
	"code Reader.CallsAreSteps":              {"ok law Reader.CallsAreSteps for Index", "ok law Reader.CallsAreSteps for Scan"},
	"code Reader.HandleRoundTrips":           {"ok law Reader.HandleRoundTrips for Index", "ok law Reader.HandleRoundTrips for Scan"},
	"code Reader.MachineArmsStopAtAWholeArm": {"ok law Reader.MachineArmsStopAtAWholeArm for Index", "ok law Reader.MachineArmsStopAtAWholeArm for Scan"},
	"code Reader.PagesPartition":             {"ok law Reader.PagesPartition for Index", "ok law Reader.PagesPartition for Scan"},
	"code Reader.ReachChainsAreCalls":        {"ok law Reader.ReachChainsAreCalls for Index", "ok law Reader.ReachChainsAreCalls for Scan"},
	"code Reader.ReferencesAreComplete":      {"ok law Reader.ReferencesAreComplete for Index", "ok law Reader.ReferencesAreComplete for Scan"},
	"code Reader.ReferencesAreSound":         {"ok law Reader.ReferencesAreSound for Index", "ok law Reader.ReferencesAreSound for Scan"},
	"code ReformatIsSameCode":                {"ok law ReformatIsSameCode"},
	"code RenameIsAlpha":                     {"ok law RenameIsAlpha"},
	"code SecondApplyIsStale":                {"ok law SecondApplyIsStale"},
	"code StaleChangesNothing":               {"ok law StaleChangesNothing"},
	"code UntouchedKeepsBytes":               {"ok law UntouchedKeepsBytes"},
	"code Writer.UnknownPlanIsNoPlan":        {"ok law Writer.UnknownPlanIsNoPlan for Applier"},
}

// TestStdCodeLawsRun runs std code's laws as ergo test runs them: the
// package's own sources are checked as a program, so its test build runs
// the member laws of each operation type against its providers (Reader
// against Index and Scan, Checker against Model, Planner against Session,
// Files against Disk and, under OnTestHost, the product HostFiles in the
// case's folder, Writer against Applier) and the top-level laws; lawledger.Run holds that this test runs
// exactly the ledger's rows of std code. It builds Go because a law runs
// only in the built test binary. Mutants code-short-first,
// code-page-repeats-a-row, code-kinds-ignored
// (internal/sketch/lower/testdata/mutants.json).
func TestStdCodeLawsRun(t *testing.T) {
	needsBuild(t)
	sources, ok := stdlib.Package("code")
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
		lines := codeLawLines[key]
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
