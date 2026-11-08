package testinputs

import (
	"os"
	"runtime"
)

// oomScore is internal/proclimit's OOMScore. This package imports nothing
// of the module outside its own tree, since its end-to-end fixture copies
// it alone; proclimit's TestStartedProgramsDieFirst holds the two equal.
const oomScore = "1000"

// raiseOOMScore makes this test process, and every process it starts, go
// build included, the first the kernel's out-of-memory killer stops,
// whatever score the shell that ran go test passed on (-900 under the
// workbox's SSH service; docs/tooling.md, "Test memory"). Raising needs no
// privilege; where it fails the process keeps its score, and proclimit's
// test reports it.
func raiseOOMScore() {
	if runtime.GOOS == "linux" {
		_ = os.WriteFile("/proc/self/oom_score_adj", []byte(oomScore), 0)
	}
}
