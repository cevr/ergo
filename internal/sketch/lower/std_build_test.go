package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdBuildLawsProbe restates each law of std build (ledger runner
// TestStdBuildLawsRun) as a property test of its own name
// (stdLawsTestName), with the law's helpers over the package's exported
// functions. A law changed in source fails TestStdBuildLawsRun until this
// restatement changes with it.
const stdBuildLawsProbe = `import build
import strings

func twice(s string) string {
	if build.InFold() && build.Known(s) {
		return foldTwice(s)
	}
	let mut b = strings.NewBuilder()
	b.WriteString(s)
	b.WriteString(s)
	b.String()
}

func foldTwice(s string) string { s + s }

func lawBranchesAgree(s string) bool { twice(s) == foldTwice(s) }

func lawBuiltKnowsNoFold(x int, s string) bool { !build.InFold() && !build.Known(x) && !build.Known(s) && !build.Hole(x) && !build.Hole(s) }

test BuildBranchesAgree(s string) {
	assert lawBranchesAgree(s)
}

test BuildBuiltKnowsNoFold(x int, s string) {
	assert lawBuiltKnowsNoFold(x, s)
}
`

// stdBuildLawStatements is the statement of each build law, as the
// ledger writes it, by ledger key.
var stdBuildLawStatements = map[string]string{
	"build BranchesAgree":    `BranchesAgree(s string) { twice(s) == foldTwice(s) }`,
	"build BuiltKnowsNoFold": `BuiltKnowsNoFold(x int, s string) { !InFold() && !Known(x) && !Known(s) && !Hole(x) && !Hole(s) }`,
}

// TestStdBuildLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's build laws of this
// runner, each with the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdBuildLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdbuild", stdBuildLawsProbe, stdBuildLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
