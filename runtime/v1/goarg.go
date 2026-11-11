package v1

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
var GoArgClauses = map[string]map[int]GoArgClause{
	"bufio.NewReader":      {0: {false, "bufio/bufio.go: NewReaderSize stores rd in Reader.rd, and Reader has no Close"}},
	"bufio.NewReaderSize":  {0: {false, "bufio/bufio.go: stores rd in Reader.rd; later reads call it"}},
	"bufio.NewWriter":      {0: {false, "bufio/bufio.go: NewWriterSize stores w in Writer.wr; later Flush calls it"}},
	"bufio.NewWriterSize":  {0: {false, "bufio/bufio.go: stores w in Writer.wr; later Flush calls it"}},
	"io.ReadAll":           {0: {true, "io/io.go: reads in a loop, returns only collected bytes"}},
	"io.ReadAtLeast":       {0: {true, "io/io.go: reads in a loop and stores no reader"}},
	"io.ReadFull":          {0: {true, "io/io.go: returns ReadAtLeast's result and keeps no reader"}},
	"io.WriteString":       {0: {true, "io/io.go: one synchronous WriteString or Write call, no retained writer"}},
	"os.File.Name":         {0: {true, "os/file.go: reads the file's name, retains no receiver"}},
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
}

// GoArgLent reports a reviewed, reasoned resource lend, never a default.
func GoArgLent(target string, index int) bool {
	c := GoArgClauses[target][index]
	return c.Lent && c.Reason != ""
}
