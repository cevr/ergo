package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestStdCronLawsRun runs std cron's laws (std-spike D4) as the probe's
// tests with --seed 1; lawledger.Run holds that the probe restates
// exactly the ledger's cron laws, each with the row's statement, and that
// each passed. It builds Go because each law is an Ergo test the built
// probe runs, as TestStdDecimalLawsRun's are.
func TestStdCronLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdcron", stdCronLawsProbe, stdCronLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
