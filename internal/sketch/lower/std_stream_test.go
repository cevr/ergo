package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestStdStreamLawsRun runs the stream probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed. It builds Go because the laws move a time.Manual, whose
// timeline only the runtime has.
func TestStdStreamLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdstream", stdStreamLawsProbe, stdStreamLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
