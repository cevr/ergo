//go:build !unix

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

func openRSSWatch(memoryBudget, string) (limiter, error) {
	return nil, errors.New("memory budget: no mechanism on " + runtime.GOOS)
}

func lockStepTemp(string) (*os.File, error) {
	return nil, fmt.Errorf("step temp locking is unsupported on %s", runtime.GOOS)
}

func unlockStepTemp(*os.File) error { return nil }

func makePipeReaderNonblocking(*os.File) error {
	return fmt.Errorf("nonblocking step output is unsupported on %s", runtime.GOOS)
}

func readPipeNonblocking(*os.File, []byte) (int, error) {
	return 0, fmt.Errorf("nonblocking step output is unsupported on %s", runtime.GOOS)
}

func pipeReadWouldBlock(error) bool { return false }

func pipeReadInterrupted(error) bool { return false }
