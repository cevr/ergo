package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
)

// The test result cache (g-b2), go test's cache of a passing result: the
// output of a passing `ergo test` of a package is kept in the test
// binary's executable entry (build.Cache.PutResult), and a later run with
// the same key replays it, with no link and no run.
//
// The key holds everything the run's verdict reads. The binary is the
// entry's: the emitted module's key (every source, the compiler, the
// runtime) and the toolchain's target, flags and tags. A test reads no
// host: Ambient and go.Foreign have one provider, the program entry, and a
// test is no entry (runtime.md, Explored schedules 6), so the checker
// refuses a test that reaches the console, the files, the live clock or an
// `import go` binding. What else reaches the run is in the key: its
// arguments (source, revision, seed, states, law and run names), its memory
// limit, the go command that links it (its path, size and modification
// time, so a reinstalled Go misses) and every GO*, CGO_* and ERGO_*
// variable of the environment, which the go command and the Go runtime
// read. Nothing is cached for --receipt, whose file is the run's output,
// for --explore, or with --count 1, which always runs.

// testResultKey is the cache key of the run of a test binary with args
// under limit, linked by tc; "" when the run is not cached.
func testResultKey(opts fileOptions, tc build.Toolchain, args []string, limit uint64) string {
	if opts.uncached || opts.receipt != "" || opts.explore || opts.search.mode != "" {
		return ""
	}
	goPath, err := exec.LookPath(tc.Go)
	if err != nil {
		return ""
	}
	if goPath, err = filepath.EvalSymlinks(goPath); err != nil {
		return ""
	}
	info, err := os.Stat(goPath)
	if err != nil {
		return ""
	}
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s\x00", len(s), s) }
	write("ergo-test-result-1")
	write(goPath)
	write(strconv.FormatInt(info.Size(), 10))
	write(strconv.FormatInt(info.ModTime().UnixNano(), 10))
	write(strconv.FormatUint(limit, 10))
	write(strconv.Itoa(len(args)))
	for _, a := range args {
		write(a)
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_") || strings.HasPrefix(kv, "ERGO_") {
			env = append(env, kv)
		}
	}
	slices.Sort(env)
	write(strconv.Itoa(len(env)))
	for _, kv := range env {
		write(kv)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// testResult is the output of a passing run.
type testResult struct {
	stdout, stderr bytes.Buffer
}

// stored is a testResult as the cache keeps it.
type stored struct {
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
}

func (r *testResult) encode() []byte {
	data, _ := json.Marshal(stored{Stdout: r.stdout.Bytes(), Stderr: r.stderr.Bytes()})
	return data
}

// cachedNote is the line on stderr that says a run was replayed.
const cachedNote = "ergo: cached pass replayed; --count 1 runs it again"

// replayTestResult writes a stored passing run's output, then the note.
func replayTestResult(data []byte, stdout, stderr io.Writer) error {
	var s stored
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("test result cache: %w", err)
	}
	stdout.Write(s.Stdout)
	stderr.Write(s.Stderr)
	fmt.Fprintln(stderr, cachedNote)
	return nil
}
