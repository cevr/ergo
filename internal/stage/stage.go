// Package stage counts the compiler's work by stage (compile-speed plan
// CS0): files read and parsed, packages checked, modules lowered, Go
// actions run and reused, processes started, bytes hashed and written.
// The counters are process-wide and only ever grow; a command writes them,
// with the checker's std work (check.WorkDone), to the file that
// ERGO_STAGE_REPORT names (sketchfront.WriteStageReport). They count work,
// never time: a receipt or a test compares counts, which are the same on
// any host.
package stage

import "sync/atomic"

// Counter is one stage counter.
type Counter int

const (
	// FilesRead and BytesRead count the source inputs read from disk:
	// .ergo files, ergo.mod, go.mod, bindings and embedded assets.
	FilesRead Counter = iota
	BytesRead
	// FilesParsed and BytesParsed count the program's own .ergo files
	// parsed; std's are in check.Work.
	FilesParsed
	BytesParsed
	// PackagesChecked counts the program's own packages checked, and
	// DeclsChecked their top-level declarations; std's are in check.Work.
	PackagesChecked
	DeclsChecked
	// ModulesLowered counts the emitted Go modules lowered, GoBytesLowered
	// their lowered Go bytes, and ModulesReused the emitted modules taken
	// from the emission cache.
	ModulesLowered
	GoBytesLowered
	ModulesReused
	// BytesHashed counts the bytes hashed into an emission key.
	BytesHashed
	// GoCompiles and GoCompilesReused count the direct path's package
	// compile actions run and taken from its action store; Links and
	// LinksReused its link actions.
	GoCompiles
	GoCompilesReused
	Links
	LinksReused
	// GoCommands counts go command runs (go build, go env, go list).
	GoCommands
	// Processes counts every process the compiler started: Go tools, go
	// commands and the programs it runs.
	Processes
	// BytesWritten counts the bytes of executables and emitted files
	// written.
	BytesWritten
	// OutputsPlaced counts the executables placed at an output path (a hard
	// link or a copy of the store's blob).
	OutputsPlaced
	numCounters
)

// names are the counters' names in a report, in Counter order.
var names = [numCounters]string{
	"files_read", "bytes_read", "files_parsed", "bytes_parsed",
	"packages_checked", "decls_checked",
	"modules_lowered", "go_bytes_lowered", "modules_reused",
	"bytes_hashed",
	"go_compiles", "go_compiles_reused", "links", "links_reused",
	"go_commands", "processes", "bytes_written", "outputs_placed",
}

var counts [numCounters]atomic.Int64

// Add adds n to counter c.
func Add(c Counter, n int64) { counts[c].Add(n) }

// Get is counter c's value.
func Get(c Counter) int64 { return counts[c].Load() }

// Snapshot is every counter by its report name.
func Snapshot() map[string]int64 {
	out := make(map[string]int64, numCounters)
	for c := range numCounters {
		out[names[c]] = counts[c].Load()
	}
	return out
}

// Reset sets every counter to zero: a test's fresh process.
func Reset() {
	for c := range numCounters {
		counts[c].Store(0)
	}
}
