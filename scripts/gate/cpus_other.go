//go:build !linux

package main

import "runtime"

// affinityCPUs is the number of CPUs the process may run on. Off Linux the
// gate reads no affinity mask (darwin has none to bind a process).
func affinityCPUs() int { return runtime.NumCPU() }

// cgroupCPULimits: there are no cgroups off Linux.
func cgroupCPULimits() []string { return nil }
