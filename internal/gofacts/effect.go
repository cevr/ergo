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

// atomicWord and pcgState are the reasons of the rows below.
const (
	atomicWord = "its body is one atomic instruction on x's own word: it touches no memory but x's and never waits"
	pcgState   = "it reads and writes only p's two state words: a multiply and an add, or the seeds"
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
// goroutine it leaves behind, no callback, no handle it keeps open. Ambient is the authority to
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
	// The metadata rows (HOST-ROW-3): os's Stat, Lstat and ReadDir are
	// bound to these (gobind.ValueResults), which read Go's interface
	// result once, on the implementation os returns, into a struct the
	// caller owns; so no row vouches for an interface method.
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Stat", Reason: "it calls os.Stat on one path, following a symbolic link, and copies the five values of its result into a new struct", Source: "07a2c004939a"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.Lstat", Reason: "it calls os.Lstat on one path, not following a symbolic link, and copies the five values of its result into a new struct", Source: "b90166fec086"},
	{Target: "ergo.local/prototype/runtime/v1/hostfs.ReadDir", Reason: "it calls os.ReadDir on one directory and copies each entry's name, kind and type into a new, sorted slice", Source: "66ec26573280"},
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
