//go:build !linux

package opcount

import (
	"errors"
	"fmt"
	"os/exec"

	"ergo.local/prototype/internal/proclimit"
)

// Available reports that only Linux counts instructions here.
func Available() error {
	return errors.New("user-space instruction counts need Linux's perf_event_open")
}

// Count runs cmd and refuses its count (NoCounter); a run that does not
// exit 0 is still RunIncomplete. No process group is kept on this system.
func Count(cmd *exec.Cmd, limit uint64) (Run, error) {
	if err := proclimit.Start(cmd, limit); err != nil {
		return Run{}, err
	}
	err := cmd.Wait()
	r := Run{Uncounted: NoCounter, Why: Available().Error(), Wait: err}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			r.Incomplete = fmt.Sprintf("exit status %d", code)
		}
	} else if err != nil {
		r.Incomplete = err.Error()
	}
	return r, nil
}
