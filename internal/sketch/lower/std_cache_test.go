package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestStdCacheLawsRun runs the cache probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed. It builds Go because the laws run tasks, a manual clock and
// sync.Ref, which only the runtime has.
func TestStdCacheLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdcache", stdCacheLawsProbe, stdCacheLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
