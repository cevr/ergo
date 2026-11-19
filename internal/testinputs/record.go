package testinputs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RecordVariable names a directory in which a test process that cmd/go
// does not cache (go test -count=1, the gate's merge tier) records its run
// for the closure sweep, which then reads the record instead of running the
// package's tests again (internal/closure, ReuseVariable; HEAVY-COST-2).
// The process gives itself a test log, so it is observed as a swept run is,
// the observer's access-time identity file, and a Go build cache of its
// package's own for the programs it starts (the sweep's shared cache:
// TestSharedCacheKeepsEachPackagesInputs). Without the sweep's view (the
// root read-only, no user namespace), it first executes itself again in a
// bwrap box that gives it (boxRecording). It writes a Record of what the
// sweep's guarantees need, measured in the process, never assumed: the
// sweep reuses a run only when every guarantee holds, and runs the tests
// itself otherwise.
const RecordVariable = "ERGO_CLOSURE_RECORD"

// RecordFile is the name of a recorded run's manifest; its test log and
// identity file lie beside it (RecordLog, RecordIdentity).
const (
	RecordFile     = "record.json"
	RecordLog      = "test.log"
	RecordIdentity = "identity.json"
)

// Record is one recorded test process.
type Record struct {
	// Package is the package directory relative to the module root, with
	// forward slashes; Dir is its absolute directory and Root the module's.
	Package, Dir, Root string
	// Args are the test binary's arguments, without the test log flag.
	Args []string
	// Env is the process's environment as it started, sorted.
	Env []string
	// Cache is the Go build cache of the package's programs (GOCACHE).
	Cache string
	// Writable are the run's own writable places: its temporary and cache
	// directories and /dev/shm. The sweep does not compare their state
	// after the run (accessIdentity.writable).
	Writable []string
	// Scratch is the run's temporary directory (readLog's scratch).
	Scratch string
	// TreeReadOnly says a file could not be created in the module root
	// because the file system is read-only (EROFS), the sweep's S1;
	// TreeProbe says what the probe met.
	TreeReadOnly bool
	TreeProbe    string
	// UserNamespaces is what starting a process in a new user namespace
	// met (probeUserNamespaces): UserNamespacesRefused, then the error, is
	// the sweep's S3 (no namespace a test could make a mount in).
	UserNamespaces string
	// Exit is the test binary's exit code.
	Exit int
}

// recording is a run being recorded: where its files go.
type recording struct {
	dir string
	rec Record
}

// startRecording prepares the record of this test process under the
// directory base: the test log flag, the identity variable and the
// package's own build cache. It returns the test log's path.
func startRecording(base string) (*recording, string, error) {
	env := slices.Clone(os.Environ())
	slices.Sort(env)
	root, err := moduleRoot()
	if err != nil {
		return nil, "", err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(root, pwd)
	if err != nil {
		return nil, "", err
	}
	pkg := filepath.ToSlash(rel)
	name := strings.ReplaceAll(pkg, "/", "_")
	dir := filepath.Join(base, "runs", name, strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	cache := filepath.Join(base, "cache", name)
	for _, d := range []string{dir, cache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, "", err
		}
	}
	r := &recording{dir: dir, rec: Record{Package: pkg, Dir: pwd, Root: root, Args: slices.Clone(os.Args[1:]), Env: env, Cache: cache}}
	r.rec.Scratch = os.TempDir()
	r.rec.Writable = writablePlaces(cache, dir)
	r.rec.TreeReadOnly, r.rec.TreeProbe = probeTree(root)
	r.rec.UserNamespaces = probeUserNamespaces()
	log := filepath.Join(dir, RecordLog)
	os.Args = append(os.Args, "-test.testlogfile="+log)
	if err := os.Setenv(IdentityVariable, filepath.Join(dir, RecordIdentity)); err != nil {
		return nil, "", err
	}
	if err := os.Setenv("GOCACHE", cache); err != nil {
		return nil, "", err
	}
	return r, log, nil
}

// writablePlaces are the run's own writable places: its temporary
// directories, its caches, the record itself and /dev/shm.
func writablePlaces(extra ...string) []string {
	places := []string{"/dev/shm", os.TempDir()}
	for _, name := range []string{"GOTMPDIR", "XDG_CACHE_HOME"} {
		if v := os.Getenv(name); v != "" {
			places = append(places, v)
		}
	}
	places = append(places, extra...)
	slices.Sort(places)
	return slices.Compact(places)
}

// probeTree tries to create a file in the module root: the sweep's runs
// see the checkout read-only, so a recorded run must too.
func probeTree(root string) (bool, string) {
	probe := filepath.Join(root, fmt.Sprintf(".ergo-closure-probe-%d", os.Getpid()))
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		f.Close()
		os.Remove(probe)
		return false, "the module root is writable"
	}
	if errors.Is(err, syscall.EROFS) {
		return true, "read-only file system"
	}
	return false, err.Error()
}

// finish writes the record with the run's exit code. A record it cannot
// write turns a pass into a failure, as an unmarked log does.
func (r *recording) finish(code int) int {
	r.rec.Exit = code
	data, err := json.MarshalIndent(r.rec, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.dir, RecordFile), data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testinputs: write the run's record:", err)
		return failed(code)
	}
	return code
}

// ReadRecords returns the recorded runs of the package pkg (relative to
// the module root, forward slashes) under base, each with its directory.
func ReadRecords(base, pkg string) ([]Record, []string, error) {
	runs := filepath.Join(base, "runs", strings.ReplaceAll(pkg, "/", "_"))
	entries, err := os.ReadDir(runs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var recs []Record
	var dirs []string
	for _, e := range entries {
		dir := filepath.Join(runs, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, RecordFile))
		if err != nil {
			return nil, nil, fmt.Errorf("recorded run %s: %w", dir, err)
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, nil, fmt.Errorf("recorded run %s: %w", dir, err)
		}
		recs = append(recs, rec)
		dirs = append(dirs, dir)
	}
	return recs, dirs, nil
}
