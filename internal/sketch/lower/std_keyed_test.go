package lower

import (
	"maps"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdKeyedLawsProbe is the probe of std hmac's and uuid's laws (std-spike
// D3), one program: hmac's restatement holds the imports and helpers of
// both.
const stdKeyedLawsProbe = stdHmacLawsProbe + stdUuidLawsProbe

// TestStdKeyedLawsRun runs std hmac's and uuid's laws (std-spike D3) as
// the probe's tests with --seed 1; lawledger.Run holds that the probe
// restates exactly the ledger's laws whose runner is this test, each with
// the row's statement, and that each passed. It builds Go because each
// law is an Ergo test the built probe runs, as TestStdDecimalLawsRun's
// are.
func TestStdKeyedLawsRun(t *testing.T) {
	needsBuild(t)
	statements := maps.Clone(stdHmacLawStatements)
	maps.Copy(statements, stdUuidLawStatements)
	if laws := lawProbe(t, "stdkeyed", stdKeyedLawsProbe, statements); laws != nil {
		lawledger.Run(t, laws)
	}
}
