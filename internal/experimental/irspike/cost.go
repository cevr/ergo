package irspike

import "time"

// Cost is the counted work of one command and its descendants.
type Cost struct {
	// Instructions are the user-space instructions retired by the
	// command's process tree (perf_event_open, inherit), or -1 when the
	// host exposes no hardware counter.
	Instructions int64 `json:"instructions"`
	// UserCPU and SysCPU are the rusage CPU times of the process tree
	// (the waited-for descendants included).
	UserCPU time.Duration `json:"user_cpu_ns"`
	SysCPU  time.Duration `json:"sys_cpu_ns"`
	// MaxRSS is the largest resident set of one process of the tree, KiB.
	MaxRSS int64 `json:"max_rss_kib"`
}
