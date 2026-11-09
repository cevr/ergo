package lower

import (
	"os"
	"path/filepath"
	"testing"
)

// TestErgoRunMachineWork runs machines whose states own work through the
// CLI (MU4; runtime.md, Machines and actors 12): the conversion of a
// machine's `work` entry gives the Machine's Work the Start of each
// state's machines.Work, and the actor runs it. maps.ergo makes a result
// and each typed failure a signal through OnSuccess and OnFailure;
// listen.ergo has a Run whose result and failure are signals (no maps)
// and a Listen whose ticks the step reads until it leaves the state.
func TestErgoRunMachineWork(t *testing.T) {
	for name, want := range map[string]string{
		"maps.ergo":   "results 2 x\nfailed none\nfailed down\n",
		"listen.ergo": "ticks 9\nunavailable\n",
		// MU5: an entry's work starts two children of one machine with
		// actor.Child; their Emits come back through the map as the
		// entry's signals.
		"children.ergo": "saves 2 edits 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", "work", name))
			if err != nil {
				t.Fatal(err)
			}
			wantRun(t, string(src), want)
		})
	}
}
