//go:build ergogotrace

package v1

import (
	"os"
	"runtime/trace"
	"strings"

	"ergo.local/prototype/runtime/obs"
	"ergo.local/prototype/runtime/rt"
)

// goTraceSink is the Go-trace sink of a build made with --go-trace
// (ergogotrace; RT-OBS), chosen by GoTraceEnv: unset, none; "flight:FILE",
// a flight recorder whose window a defect writes to FILE; any other value,
// a full Go execution trace written to that file while main runs. done
// stops the trace and closes the file.
func goTraceSink() (sink rt.Tracer, done func()) {
	spec := os.Getenv(GoTraceEnv)
	if spec == "" {
		return nil, func() {}
	}
	if path, ok := strings.CutPrefix(spec, "flight:"); ok {
		f, err := obs.StartFlight()
		if err != nil {
			goTraceFailed(err)
			return nil, func() {}
		}
		return obs.New(func() { dumpFlight(f, path) }), f.Stop
	}
	out, err := os.Create(spec)
	if err != nil {
		goTraceFailed(err)
		return nil, func() {}
	}
	if err := trace.Start(out); err != nil {
		goTraceFailed(err)
		out.Close()
		return nil, func() {}
	}
	return obs.New(nil), func() {
		trace.Stop()
		if err := out.Close(); err != nil {
			goTraceFailed(err)
		}
	}
}

// dumpFlight writes f's window to path, once.
func dumpFlight(f *obs.Flight, path string) {
	out, err := os.Create(path)
	if err != nil {
		goTraceFailed(err)
		return
	}
	if _, err := f.Dump(out); err != nil {
		goTraceFailed(err)
	}
	if err := out.Close(); err != nil {
		goTraceFailed(err)
	}
}

// goTraceFailed reports a failure of the Go trace on standard error, one
// line, written as the panic writer writes: no fmt, whose init roots and
// size a --go-trace build, still an Ergo program, does not take on
// (TestCoreHasNoInitRoots).
func goTraceFailed(err error) {
	os.Stderr.WriteString("ergo: go trace: " + err.Error() + "\n")
}
