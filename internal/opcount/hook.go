package opcount

// HookFile is the name of the exit hook's file in a measured build.
const HookFile = "zz_opcount_hook.go"

// HookVariable names the file the hook writes its counters to. A build
// with the hook and without the variable runs as without the hook, but for
// one Getenv at start-up.
const HookVariable = "OPCOUNT_OUT"

// Hook is the exit hook (IR-G measure's zz_irghook, reduced to what a
// receipt reads), added to a package main only in scripts/opcount's
// scratch build, through go build -overlay: never in a checkout or in the
// emission cache, never in a normal build. It must be built with
// -ldflags=-checklinkname=0. At exit (main returns or os.Exit) it writes
// "NAME VALUE" lines: the allocations, bytes and GC cycles of the run
// (MemStats, before any collection of its own), then, after one forced
// collection, the live heap, the runtime's GC CPU share, and the process's
// peak resident set (VmHWM, KiB).
const Hook = `package main

// Exit hook of scripts/opcount (internal/opcount/hook.go); a measured
// build only.

import (
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"strings"
	_ "unsafe"
)

type opcountExitHook struct {
	F            func()
	RunOnFailure bool
}

//go:linkname opcountExithookAdd internal/runtime/exithook.Add
func opcountExithookAdd(h opcountExitHook)

func init() {
	out := os.Getenv("` + HookVariable + `")
	if out == "" {
		return
	}
	opcountExithookAdd(opcountExitHook{F: func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		var b strings.Builder
		fmt.Fprintf(&b, "allocs %d\nalloc_bytes %d\ngc_cycles %d\n", m.Mallocs, m.TotalAlloc, m.NumGC)
		runtime.GC()
		samples := []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}, {Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
		metrics.Read(samples)
		fmt.Fprintf(&b, "live_heap_bytes %d\n", samples[0].Value.Uint64())
		if busy := samples[2].Value.Float64() - samples[3].Value.Float64(); busy > 0 {
			fmt.Fprintf(&b, "gc_cpu_share %g\n", samples[1].Value.Float64()/busy)
		}
		if status, err := os.ReadFile("/proc/self/status"); err == nil {
			for _, line := range strings.Split(string(status), "\n") {
				if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
					fmt.Fprintf(&b, "peak_rss_kib %s\n", strings.TrimSuffix(strings.TrimSpace(v), " kB"))
				}
			}
		}
		os.WriteFile(out, []byte(b.String()), 0o644)
	}, RunOnFailure: true})
}
`
