package gofacts

// The memory-only effect (np4f-plan GBX6; interop.md, Purity and authority
// and Foreign facts 7). A Go function or method of an Effects row is not
// pure, since it reads or writes memory its arguments reach (an atomic
// cell, a generator's state), but it reads no host state, needs no
// capability and returns without waiting on another goroutine (its wait
// kind is none). `ergo bind go` writes it with no `needs go.Foreign` and a
// `// ergo:effect none (vouched: <reason>)` line; the checker charges it
// nothing and keeps it out of the pure tier (its call is opaque in a
// footprint), and only on its guard line current and validated
// (check.EffectActs). Its guard (internal/gofacts/hostguard, effect.go)
// runs the same drawn calls on the same drawn state under two host states:
// the results and the state after must agree.

// Effect is one row: the extern target, why the reviewer holds it, and the
// hash of its Go declaration at the pinned toolchain (SourceHash), which
// TestVouchedRowsReviewed holds as it holds Rows'.
type Effect struct {
	Target, Reason, Source string
}

// atomicWord, pcgState, fileModeBits and timeInstant are the reasons of
// the rows below.
const (
	atomicWord   = "its body is one atomic instruction on x's own word: it touches no memory but x's and never waits"
	pcgState     = "it reads and writes only p's two state words: a multiply and an add, or the seeds"
	fileModeBits = "it reads only m's bits: a mask, a compare or their text"
	timeInstant  = "it reads only the instants of t and its argument (wall, ext), never a location's zone or the host's"
)

// Effects is the table of memory-only Go functions and methods.
var Effects = []Effect{
	{Target: "sync/atomic.Bool.CompareAndSwap", Reason: atomicWord, Source: "a232c5cae2f0"},
	{Target: "sync/atomic.Bool.Load", Reason: atomicWord, Source: "43ee7c56ffde"},
	{Target: "sync/atomic.Bool.Store", Reason: atomicWord, Source: "6ca84c04cfcd"},
	{Target: "sync/atomic.Bool.Swap", Reason: atomicWord, Source: "8394e0edb911"},
	{Target: "sync/atomic.Int64.Add", Reason: atomicWord, Source: "f55e2110ad02"},
	{Target: "sync/atomic.Int64.And", Reason: atomicWord, Source: "459ef6af2c0e"},
	{Target: "sync/atomic.Int64.CompareAndSwap", Reason: atomicWord, Source: "a8e69380f253"},
	{Target: "sync/atomic.Int64.Load", Reason: atomicWord, Source: "9960558b852d"},
	{Target: "sync/atomic.Int64.Or", Reason: atomicWord, Source: "c9881d6b3c12"},
	{Target: "sync/atomic.Int64.Store", Reason: atomicWord, Source: "df817dd4c1bb"},
	{Target: "sync/atomic.Int64.Swap", Reason: atomicWord, Source: "1182ea4cd802"},
	{Target: "math/rand/v2.NewPCG", Reason: "it returns a new PCG whose two state words are its arguments", Source: "07dc6a6412f8"},
	{Target: "math/rand/v2.PCG.Seed", Reason: pcgState, Source: "7a7521723a07"},
	{Target: "math/rand/v2.PCG.Uint64", Reason: pcgState, Source: "82ade025cb4d"},
	// The values of a file's metadata (HOST-ROW-3b): hostfs.Info's Mode
	// and ModTime are an fs.FileMode and a time.Time, whose methods below
	// compute on the value alone, so a user reads them with no go.Foreign.
	// A time.Time method that reads its location's zone (Year, Hour,
	// Format, Local, In, ...) is not a row: the zone is host state for a
	// time in time.Local, which os's times are.
	{Target: "io/fs.FileMode.IsDir", Reason: fileModeBits, Source: "09c52a7f6974"},
	{Target: "io/fs.FileMode.IsRegular", Reason: fileModeBits, Source: "adeb948447f8"},
	{Target: "io/fs.FileMode.Perm", Reason: fileModeBits, Source: "892d9c86cda5"},
	{Target: "io/fs.FileMode.Type", Reason: fileModeBits, Source: "002a34f0fec4"},
	{Target: "io/fs.FileMode.String", Reason: fileModeBits, Source: "ec31917348c5"},
	{Target: "time.Time.Add", Reason: timeInstant, Source: "0363db7ec041"},
	{Target: "time.Time.After", Reason: timeInstant, Source: "f66cc9c7d00c"},
	{Target: "time.Time.Before", Reason: timeInstant, Source: "6b1fb40f80d2"},
	{Target: "time.Time.Compare", Reason: timeInstant, Source: "ec502b96477d"},
	{Target: "time.Time.Equal", Reason: timeInstant, Source: "6e90fda3fff1"},
	{Target: "time.Time.IsZero", Reason: timeInstant, Source: "48e36ccb8bf8"},
	{Target: "time.Time.Sub", Reason: timeInstant, Source: "e1b6a752c5b6"},
	{Target: "time.Time.UTC", Reason: timeInstant, Source: "a37ff6506242"},
	{Target: "time.Time.Unix", Reason: timeInstant, Source: "f38f0efa4da0"},
	{Target: "time.Time.UnixMilli", Reason: timeInstant, Source: "0e69a893e6c6"},
	{Target: "time.Time.UnixNano", Reason: timeInstant, Source: "87ce5948816a"},
}

// EffectOf is the row of target, if it has one.
func EffectOf(target string) (Effect, bool) {
	for _, e := range Effects {
		if e.Target == target {
			return e, true
		}
	}
	return Effect{}, false
}

// The host effect (HOST-ROW, from std-priv NP4f-6; interop.md, Purity and
// authority and Foreign facts 7). A Go function of a Hosts row reads or
// changes the host process's own state (its environment, its working
// directory, its clock, its file system at a path it resolves against the
// working directory) and nothing else: no memory its arguments reach, no
// goroutine it leaves behind, no callback, no handle it keeps open but the
// one it returns to its caller (an *os.File, a resource its owner must
// close; HOST-ROW-3 slice 2). Ambient is the authority to
// choose the host (stdlib.md, Host packages), so `ergo bind go` writes it
// with `needs Ambient`, not go.Foreign, and a `// ergo:effect ambient
// (vouched: <reason>)` line; the checker admits that row only on its guard
// line current and validated (check.HostActs), and a provider whose
// operations call it captures Ambient where it is built, as one that calls
// unreviewed Go captures go.Foreign. The row is Go's function's, not std's:
// every binding of it, std's or a user's, gets the same row. Its guard
// (internal/gofacts/hostguard, host.go) runs the call under the
// determinism guard's two host states: it must follow each state and keep
// nothing of the caller's. A file row's state is the files of each state's
// own working directory (HOST-ROW-2).

// Hosts is the table of host Go functions.
var Hosts = []Effect{
	{Target: "os.Getenv", Reason: "it reads one variable of the process's environment", Source: "9bd559b80670"},
	{Target: "os.LookupEnv", Reason: "it reads one variable of the process's environment", Source: "5949a9958c29"},
	{Target: "os.Environ", Reason: "it copies the process's environment into a new slice", Source: "a26f3fa0bdc1"},
	{Target: "os.Getwd", Reason: "it reads the process's working directory", Source: "e751aa1d3b9f"},
	{Target: "time.Now", Reason: "it reads the host's clock and the process's local zone", Source: "c9ea77d5e6b9"},
	// The file rows (HOST-ROW-2): each takes paths and values, resolves a
	// relative path against the working directory, and returns values the
	// caller owns; none returns or keeps an open file.
	{Target: "os.ReadFile", Reason: "it reads one file into a new byte slice and closes it", Source: "bb04a2d6e68b"},
	{Target: "os.WriteFile", Reason: "it writes its bytes to one file, creating or truncating it, and closes it", Source: "c971c9ff32f1"},
	{Target: "os.Mkdir", Reason: "it makes one directory", Source: "054e02d4d835"},
	{Target: "os.MkdirAll", Reason: "it makes a directory and its missing parents", Source: "387cad4262ed"},
	{Target: "os.Remove", Reason: "it removes one file or empty directory", Source: "c04948848952"},
	{Target: "os.RemoveAll", Reason: "it removes a path and everything under it", Source: "98f8afe7adc2"},
	{Target: "os.Rename", Reason: "it renames one path", Source: "2e89ed8a4b75"},
	{Target: "os.Readlink", Reason: "it reads one symbolic link's target", Source: "6a065e85af7a"},
	{Target: "os.Chmod", Reason: "it changes one file's mode", Source: "354bb99d651b"},
	// The path forms of (*os.File).Chdir and Chown (S2-FILE's follow-up).
	{Target: "os.Chdir", Reason: "it makes one directory the process's working directory", Source: "5efc41e7d8dd"},
	{Target: "os.Chown", Reason: "it changes one file's owner and group, following a symbolic link, a negative id leaving that one", Source: "1249ef2db82e"},
	{Target: "os.Lchown", Reason: "it changes one path's owner and group, not following a symbolic link, a negative id leaving that one", Source: "c09cd95ce984"},
	// The metadata rows (HOST-ROW-3): os's Stat, Lstat and ReadDir are
	// bound to these (gobind.ValueResults), which read Go's interface
	// result once, on the implementation os returns, into a struct the
	// caller owns; so no row vouches for an interface method.
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Stat", Reason: "it calls os.Stat on one path, following a symbolic link, and copies the five values of its result into a new struct", Source: "07a2c004939a"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Lstat", Reason: "it calls os.Lstat on one path, not following a symbolic link, and copies the five values of its result into a new struct", Source: "b90166fec086"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.ReadDir", Reason: "it calls os.ReadDir on one directory and copies each entry's name, kind and type into a new, sorted slice", Source: "66ec26573280"},
	// The targets of (*os.File).Stat, ReadDir and Readdir's bindings
	// (S2-FILE-B): each calls the method once on the caller's handle and
	// copies its values; none keeps the handle.
	{Target: "ergo.local/prototype/runtime/v1/hostfs.FileStat", Reason: "it calls Stat on its open file's handle and copies the five values of its result into a new struct", Source: "9d2c981cc12d"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.FileReadDir", Reason: "it calls ReadDir(n) on its open directory's handle, reading on from the handle's position, and copies each entry's name, kind and type into a new slice", Source: "3fb32a005db0"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.FileReaddir", Reason: "it calls Readdir(n) on its open directory's handle, reading on from the handle's position, and copies each entry's five values into a new slice", Source: "334a0ad29d37"},
	// The open files (HOST-ROW-3 slice 2): Open, Create and OpenFile
	// return the one handle they open, which the caller owns (a go.Ref
	// resource: its Close takes it); each method reads or changes the
	// file of its handle and keeps nothing of its arguments. Read and
	// ReadAt write only b[:n] (v1.GoWrites), so their binding returns b.
	{Target: "os.Open", Reason: "it opens one file for reading and returns its new handle to the caller", Source: "f9e1351c88a4"},
	{Target: "os.Create", Reason: "it creates or truncates one file, opens it for reading and writing, and returns its new handle to the caller", Source: "2f7de022449f"},
	{Target: "os.OpenFile", Reason: "it opens one file with the caller's flags and mode and returns its new handle to the caller", Source: "ff9a64cc1436"},
	{Target: "os.File.Read", Reason: "it reads up to len(b) bytes of its file at the handle's offset into b[:n], keeping no reference to b", Source: "80a3a9532f27"},
	{Target: "os.File.ReadAt", Reason: "it reads len(b) bytes of its file at off into b, keeping no reference to b, and leaves the handle's offset", Source: "7c804d1ca0d7"},
	{Target: "os.File.Write", Reason: "it writes b to its file at the handle's offset, keeping no reference to b", Source: "623de74b5313"},
	{Target: "os.File.WriteString", Reason: "it writes s to its file at the handle's offset", Source: "da1e5bc07acd"},
	{Target: "os.File.Seek", Reason: "it moves the handle's offset and returns the new one", Source: "6dc50422060a"},
	{Target: "os.File.Close", Reason: "it closes its handle; a later call on it fails with os.ErrClosed", Source: "61bc2647f666"},
	{Target: "os.File.Name", Reason: "it returns the name its handle was opened with", Source: "0099ffe09700"},
	{Target: "os.File.Sync", Reason: "it commits its file's contents to stable storage", Source: "1ef07de7120d"},
	{Target: "os.File.Truncate", Reason: "it changes its file's size", Source: "316577c38894"},
	// The rest of *os.File's methods (S2-FILE). Chdir changes the
	// process's working directory, a host state Getwd reads; the
	// deadlines set the handle's own poller deadlines, which arm a runtime
	// timer and start no goroutine.
	{Target: "os.File.WriteAt", Reason: "it writes b to its file at off, keeping no reference to b, and leaves the handle's offset", Source: "adab3799e15d"},
	{Target: "os.File.Chmod", Reason: "it changes its file's mode", Source: "9d9b9e00558b"},
	{Target: "os.File.Chown", Reason: "it changes its file's owner and group, a negative id leaving that one", Source: "63cd3451504c"},
	{Target: "os.File.Chdir", Reason: "it makes its directory the process's working directory", Source: "2c7cab4822f3"},
	{Target: "os.File.Readdirnames", Reason: "it reads up to n names of its directory from the handle's position into a new slice", Source: "9564848e855e"},
	{Target: "os.File.SetDeadline", Reason: "it sets its handle's read and write deadlines, or fails with os.ErrNoDeadline on a file with none", Source: "04da8420ccbd"},
	{Target: "os.File.SetReadDeadline", Reason: "it sets its handle's read deadline, or fails with os.ErrNoDeadline on a file with none", Source: "9c34436651b5"},
	{Target: "os.File.SetWriteDeadline", Reason: "it sets its handle's write deadline, or fails with os.ErrNoDeadline on a file with none", Source: "dad17b485b6d"},
	// Host process and filesystem metrics (std/host).
	{Target: "runtime.NumCPU", Reason: "it returns the number of logical CPUs reported by the operating system at process startup", Source: "9ef49458017b"},
	{Target: "os.Getpagesize", Reason: "it returns the operating system's memory page size", Source: "e147c7c13ec8"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Statfs", Reason: "it calls syscall.Statfs on one path and copies the volume total, free and available byte counts into a new struct", Source: "73314a4cd754"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Getfsstat", Reason: "it calls Darwin syscall.Getfsstat(MNT_NOWAIT) to enumerate mounted filesystems and their byte capacities (returning ENOSYS elsewhere)", Source: "072e63cf1ed8"},
}

// HostOf is the host row of target, if it has one.
func HostOf(target string) (Effect, bool) {
	for _, e := range Hosts {
		if e.Target == target {
			return e, true
		}
	}
	return Effect{}, false
}

// HostKey is the ledger key of target's host row.
func HostKey(target string) string { return "go " + target + " effect ambient" }

// HostDigest is the digest of the host row's guard line at toolchain.
func (e Effect) HostDigest(toolchain string) string {
	return lineDigest("fact " + HostKey(e.Target) + "\nsource " + e.Source + "\nreason " + e.Reason + "\ngo " + toolchain + "\n")
}

// EffectKey is the ledger key of target's memory-only row.
func EffectKey(target string) string { return "go " + target + " effect none" }

// Digest is the digest of the row's guard line at toolchain: the key, the
// reviewed source hash, the reason and the toolchain.
func (e Effect) Digest(toolchain string) string {
	return lineDigest("fact " + EffectKey(e.Target) + "\nsource " + e.Source + "\nreason " + e.Reason + "\ngo " + toolchain + "\n")
}
