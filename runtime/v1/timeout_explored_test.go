//go:build ergoexplore

package v1

import "ergo.local/prototype/runtime/rt"

// abortExplored stops the active explored run (rt.AbortExplore), so a
// watchdog (within) frees the explorer for the next test; it reports
// whether a run was active. The live build's is in timeout_live_test.go.
func abortExplored(why error) bool { return rt.AbortExplore(why) }
