//go:build !linux && !darwin

package main

import (
	"fmt"
	"runtime"
)

// The gate runs on Linux and Darwin (hostfacts_linux.go,
// hostfacts_darwin.go); elsewhere it reads no host facts and says so.
func notRead() error { return fmt.Errorf("host facts are not read on %s", runtime.GOOS) }

func kernelFacts() (release, version string, err error) { return "", "", notRead() }

func fileSystemType(string) (string, error) { return "", notRead() }

func openFileLimit() (string, error) { return "", notRead() }

func cgroupFacts() (facts [][2]string, problems []string) {
	return [][2]string{{"cgroup file system", "none on " + runtime.GOOS}}, nil
}
