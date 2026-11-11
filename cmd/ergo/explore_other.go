//go:build !unix

package main

import (
	"errors"
	"os"

	"ergo.local/prototype/runtime/explore/sched"
)

// startWorker refuses: a worker runs in a process group of its own, which
// only a Unix host gives here.
func startWorker(binary string, args []string, out *os.File, limit uint64) (sched.Conn, error) {
	return nil, errors.New("ergo test --explore MODE runs its workers on a Unix host only")
}
