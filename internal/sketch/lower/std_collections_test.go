package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// The runners of std-spike D1's laws: slices' select, split and combine
// files, maps' combine file and package set. Each runs its probe's tests
// with --seed 1; lawledger.Run holds that the probe restates exactly the
// ledger's laws whose runner it is, each with the row's statement, and that
// each passed. They build Go because each law is an Ergo test the built
// probe runs, as TestStdSlicesLawsRun's are.

func TestStdSlicesMoreLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdslicesmore", stdSlicesMoreLawsProbe, stdSlicesMoreLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

func TestStdMapsMoreLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdmapsmore", stdMapsMoreLawsProbe, stdMapsMoreLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

func TestStdSetLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdset", stdSetLawsProbe, stdSetLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
