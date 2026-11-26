package selfhostcheck

import "testing"

func TestCompilerLoadLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/load")
}
