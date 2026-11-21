//go:build !ergogotrace

package v1

import "ergo.local/prototype/runtime/rt"

// goTraceSink is none in a build made without --go-trace: runtime/obs,
// runtime/trace and runtime/pprof stay out of the program (gotrace_on.go).
func goTraceSink() (rt.Tracer, func()) { return nil, func() {} }
