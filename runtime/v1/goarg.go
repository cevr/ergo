package v1

import "sync"

// GoArgClause is a reviewed exception to the conservative import-go
// argument policy. Indexes are the binding's parameters, including a
// concrete method's receiver at index 0. An interface operation's implicit
// receiver is -1, with its explicit parameters starting at 0. The hidden
// context is not counted.
// A missing entry is kept: Go's type does not prove a lend. Function
// arguments use GoSyncCallbacks; these entries concern resource arguments.
type GoArgClause struct {
	Lent   bool
	Reason string
}

// GoArgClauses records source facts, not type-shape guesses (go1.27).
// Keeping a resource transfers it to Go; it never borrows an Ergo owner.
// New narrow exceptions require a per-function entry and a reason here.
var GoArgClauses = sync.OnceValue(func() map[string]map[int]GoArgClause {
	return map[string]map[int]GoArgClause{
		"bufio.NewReader":      {0: {false, "bufio/bufio.go: NewReaderSize stores rd in Reader.rd, and Reader has no Close"}},
		"bufio.NewReaderSize":  {0: {false, "bufio/bufio.go: stores rd in Reader.rd; later reads call it"}},
		"bufio.NewWriter":      {0: {false, "bufio/bufio.go: NewWriterSize stores w in Writer.wr; later Flush calls it"}},
		"bufio.NewWriterSize":  {0: {false, "bufio/bufio.go: stores w in Writer.wr; later Flush calls it"}},
		"io.ReadAll":           {0: {true, "io/io.go: reads in a loop, returns only collected bytes"}},
		"io.ReadAtLeast":       {0: {true, "io/io.go: reads in a loop and stores no reader"}},
		"io.ReadFull":          {0: {true, "io/io.go: returns ReadAtLeast's result and keeps no reader"}},
		"io.WriteString":       {0: {true, "io/io.go: one synchronous WriteString or Write call, no retained writer"}},
		"os.File.Name":         {0: {true, "os/file.go: reads the file's name, retains no receiver"}},
		"os.StartProcess":      {2: {true, "os/exec_posix.go: startProcess copies attr's Dir, Env, Sys (ensurePidfd copies it when it changes it) and each file's Fd into a syscall.ProcAttr; it keeps no attr past its return (GBX5)"}},
		"os.File.Read":         {0: {true, "os/file.go: read and error conversion finish before return"}},
		"os.File.ReadAt":       {0: {true, "os/file.go: pread loop finishes before return"}},
		"os.File.Write":        {0: {true, "os/file.go: write and error conversion finish before return"}},
		"os.File.WriteAt":      {0: {true, "os/file.go: pwrite loop finishes before return"}},
		"os.File.WriteString":  {0: {true, "os/file.go: calls Write synchronously and keeps no receiver"}},
		"os.File.Seek":         {0: {true, "os/file.go: seek completes before return and retains no receiver"}},
		"os.File.Sync":         {0: {true, "os/file_posix.go: fsync completes before return and retains no receiver"}},
		"os.File.Stat":         {0: {true, "os/stat_*.go: fstat returns metadata, not a reference to the File"}},
		"os.File.Truncate":     {0: {true, "os/file_posix.go: truncate completes before return and retains no receiver"}},
		"os.File.Readdir":      {0: {true, "os/dir.go: returns file info values after the directory read"}},
		"os.File.Readdirnames": {0: {true, "os/dir.go: returns names after the directory read"}},
		"os.File.ReadDir":      {0: {true, "os/dir.go: returns entries after the directory read"}},
		// The targets of (*os.File).Stat, ReadDir and Readdir's bindings
		// (S2-FILE-B; gobind.ValueResults): each calls the method once and
		// copies its values into a new struct or slice.
		"ergo.local/prototype/runtime/v1/hostfs.FileStat":    {0: {true, "runtime/v1/hostfs: calls f.Stat once and copies its values; keeps no f"}},
		"ergo.local/prototype/runtime/v1/hostfs.FileReadDir": {0: {true, "runtime/v1/hostfs: calls f.ReadDir once and copies its entries; keeps no f"}},
		"ergo.local/prototype/runtime/v1/hostfs.FileReaddir": {0: {true, "runtime/v1/hostfs: calls f.Readdir once and copies its values; keeps no f"}},
	}
})

// GoWrite is a reviewed Go function or method that writes its slice
// parameter Index (counted after the receiver) in place (GBX2;
// docs/spec/interop.md, Go writes in place). Its binding returns that
// slice in Ergo, first when Go has results of its own (HOST-ROW-3 slice
// 2: `(*os.File).Read` is `([]byte, int) fails go.Error`), and the
// call's value is the slice Go wrote.
type GoWrite struct {
	Index  int
	Reason string
}

// GoWrites records source facts (go1.27): each function writes only its
// slice argument's elements and keeps no reference to it.
var GoWrites = sync.OnceValue(func() map[string]GoWrite {
	return map[string]GoWrite{
		"os.File.Read":          {0, "os/file.go: f.read(b) fills b[:n] through poll.FD.Read, keeps no reference"},
		"os.File.ReadAt":        {0, "os/file.go: f.pread(b, off) fills b in a loop through poll.FD.Pread, keeps no reference"},
		"slices.Sort":           {0, "slices/sort.go: pdqsortOrdered sorts x's elements in place, keeps no reference"},
		"slices.SortFunc":       {0, "slices/sort.go: pdqsortCmpFunc sorts x's elements in place, keeps no reference"},
		"slices.SortStableFunc": {0, "slices/sort.go: stableCmpFunc sorts x's elements in place, keeps no reference"},
		"slices.Reverse":        {0, "slices/slices.go: swaps s's elements in place, keeps no reference"},
		"sort.Ints":             {0, "sort/sort.go: slices.Sort(x)"},
		"sort.Float64s":         {0, "sort/sort.go: slices.Sort(x)"},
		"sort.Strings":          {0, "sort/sort.go: slices.Sort(x)"},
	}
})

// GoReadOnlyVars are the reviewed Go package variables of Go's standard
// library that no Go code of their package writes after its init, each
// with its source reason (go1.27; GBX3, docs/spec/interop.md, Go package
// variables). A binding declares each as a read-only let; a pointer one is
// never nil, so it is a go.Ref. No entry is of an interface type (os.Interrupt,
// io.Discard): Ergo holds a Go interface value only through an operation.
var GoReadOnlyVars = sync.OnceValue(func() map[string]string {
	return map[string]string{
		"encoding/base64.StdEncoding":    "encoding/base64/base64.go: = NewEncoding(…) at init; the package never assigns it",
		"encoding/base64.URLEncoding":    "encoding/base64/base64.go: = NewEncoding(…) at init; the package never assigns it",
		"encoding/base64.RawStdEncoding": "encoding/base64/base64.go: = StdEncoding.WithPadding(NoPadding) at init; never assigned",
		"encoding/base64.RawURLEncoding": "encoding/base64/base64.go: = URLEncoding.WithPadding(NoPadding) at init; never assigned",
		"hash/crc32.IEEETable":           "hash/crc32/crc32.go: = simpleMakeTable(IEEE) at init; the package reads it only",
		"time.UTC":                       "time/zoneinfo.go: = &utcLoc; the package never assigns it",
		"time.Local":                     "time/zoneinfo.go: = &localLoc; the package never assigns it (it fills localLoc lazily, behind a sync.Once)",
		"os.Args":                        "os/proc.go: set once by the runtime's init (runtime_args); the package never assigns it",
	}
})

// GoConcurrentTypes are the reviewed Go types of Go's standard library
// whose memory-only methods (gofacts.Effects) are safe for concurrent use,
// each with its source reason (go1.27; GB-RACE, docs/spec/interop.md, Go
// values by address 8). A go.Ref to a Go type with a memory-only method
// and no entry here is confined to the task that holds it.
var GoConcurrentTypes = sync.OnceValue(func() map[string]string {
	return map[string]string{
		"sync/atomic.Bool":  "sync/atomic/type.go: each method is one atomic operation on the value's word",
		"sync/atomic.Int64": "sync/atomic/type.go: each method is one atomic operation on the value's word",
	}
})

// GoArgLent reports a reviewed, reasoned resource lend, never a default.
func GoArgLent(target string, index int) bool {
	c := GoArgClauses()[target][index]
	return c.Lent && c.Reason != ""
}
