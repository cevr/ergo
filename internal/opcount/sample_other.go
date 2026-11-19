//go:build !linux

package opcount

import "os/exec"

// Period is 0: only Linux samples here.
func Period() uint64 { return 0 }

// CountShares is Count; off Linux the parts are refused (NoSampler).
func CountShares(cmd *exec.Cmd, limit uint64, bin string) (Run, Shares, error) {
	r, err := Count(cmd, limit)
	return r, Shares{Refused: NoSampler, Why: Available().Error()}, err
}
