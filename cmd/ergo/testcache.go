package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/toolchain"
)

// The test result cache (g-b2), go test's cache of a passing result: the
// output of a passing `ergo test` of a package is kept under the emitted
// module's key (build.Cache.PutResult), and a later run with the same key
// replays it, with no link and no run.
//
// The key holds everything the run's verdict reads. The module key holds
// every source, the compiler and the runtime; this key adds the toolchain
// (GOROOT, target, flags, tags, the reference path and the strip
// setting), the go command that links (its path, size and modification
// time, so a reinstalled Go misses), the run's arguments (source,
// revision, seed, states, law and run names), its memory limit, the law
// ledger a full run writes, and every GO*, CGO_* and ERGO_* variable of
// the environment, which the go command and the Go runtime read, but
// ERGO_STAGE_REPORT, which only names where the counters go. A test
// reads no host: Ambient and go.Foreign have one provider, the program
// entry, and a test is no entry (runtime.md, Explored schedules 6), so the
// checker refuses a test that reaches the console, the files, the live
// clock or an `import go` binding. Nothing is cached for --receipt, whose
// file is the run's output, for --explore, or with --count 1, which always
// runs.

// testResultKey is the cache key of the run of a test binary with args
// under limit, linked by tc; "" when the run is not cached.
// testCacheable reports whether a run of opts is cached: not with --count
// 1, --receipt or --explore.
func testCacheable(opts fileOptions) bool {
	return !opts.uncached && !opts.trace && opts.receipt == "" && !opts.explore && opts.search.mode == ""
}

// ledgerPart is the law ledger at path as a key part: a full run writes it,
// so a hit finds the ledger the stored run left.
func ledgerPart(path string) string {
	var b strings.Builder
	if data, err := os.ReadFile(path); err == nil {
		b.WriteString("ledger ")
		b.Write(data)
	} else {
		b.WriteString("no ledger")
	}
	if path != "" {
		casesPath := filepath.Join(filepath.Dir(path), sketchfront.CorpusFile)
		if data, err := os.ReadFile(casesPath); err == nil {
			b.WriteString("\ncases ")
			b.Write(data)
		}
	}
	return b.String()
}

// toolchainIdentity is the Go toolchain that links tc's builds, as a key
// part: the go command's resolved path, size and time, the managed GOROOT,
// the platform, GOFLAGS, the tags and the link mode; "" when the go
// command cannot be found. A stored test result and a law verdict a test
// binary wrote (FT2) are both keyed by it, so another Go never replays
// either.
func toolchainIdentity(tc build.Toolchain) string {
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
	parts := []string{goPath, strconv.FormatInt(info.Size(), 10), strconv.FormatInt(info.ModTime().UnixNano(), 10), tc.Root, tc.GOOS + "/" + tc.GOARCH, tc.GOFLAGS, strings.Join(tc.Tags, ","), strconv.FormatBool(tc.GoBuild) + " " + strconv.FormatBool(tc.Strip) + " " + string(tc.Profile)}
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%d:%s\x00", len(p), p)
	}
	return b.String()
}

// pinnedToolchain is the toolchain key part of a law verdict a test binary
// linked by the pinned managed release (internal/toolchain) for platform
// wrote: the release, the platform and GOFLAGS; "" when ERGO_GO chooses
// another Go. It starts no process and installs nothing, so ergo check
// and the evaluator can name it too, and read the verdicts such a binary
// wrote: a native run under the pin is exactly what the key names (FT2
// r3). The release's patch is a constant of this ergo, which the store's
// compiler identity already keys.
func pinnedToolchain(platform gobind.Platform) string {
	root, err := build.DefaultRoot()
	if err != nil {
		return ""
	}
	if t, _ := toolchain.Status(os.Getenv, root); t.Mode != "managed" {
		return ""
	}
	return "pinned " + toolchain.Version + " " + platform.GOOS + "/" + platform.GOARCH + " GOFLAGS=" + os.Getenv("GOFLAGS")
}

// nativeToolchain is the toolchain key part of the law verdicts tc's test
// binary writes: the pin's when the build runs the pinned release, else
// the go command's identity (toolchainIdentity), which no evaluator reads.
func nativeToolchain(opts fileOptions, tc build.Toolchain) string {
	if p := pinnedToolchain(opts.platform); p != "" {
		return p
	}
	return toolchainIdentity(tc)
}

func testResultKey(opts fileOptions, tc build.Toolchain, args []string, limit uint64, ledger string) string {
	if !testCacheable(opts) {
		return ""
	}
	// A full run writes the law ledger; a run that finds another ledger
	// than the stored run left is no hit. Where the stage report goes
	// changes no verdict.
	return lawcache.TestResultKey(toolchainIdentity(tc), ledgerPart(ledger), limit, args, os.Environ(), sketchfront.StageReportVariable)
}

// testResult is the output of a passing run.
type testResult struct {
	stdout, stderr bytes.Buffer
	// obligations is the number of tests and law runs the binary ran.
	obligations int
}

// stored is a testResult as the cache keeps it.
type stored struct {
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
	// Obligations is the number of obligations the stored run decided; a
	// replay counts them as stored (stage.ObligationsStored).
	Obligations int `json:"obligations,omitempty"`
}

func (r *testResult) encode() []byte {
	data, _ := json.Marshal(stored{Stdout: r.stdout.Bytes(), Stderr: r.stderr.Bytes(), Obligations: r.obligations})
	return data
}

// cachedNote is the line on stderr that says a run was replayed.
const cachedNote = "ergo: cached pass replayed; --count 1 runs it again"

// replayTestResult writes a stored passing run's output, then the note,
// and judges its skips against the expected-skips of the module of path
// as they are now (skipsExit): the file is no input of the stored key.
func replayTestResult(data []byte, path string, stdout, stderr io.Writer) error {
	var s stored
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("test result cache: %w", err)
	}
	stage.Add(stage.ObligationsStored, int64(s.Obligations))
	stdout.Write(s.Stdout)
	stderr.Write(s.Stderr)
	fmt.Fprintln(stderr, cachedNote)
	return skipsExit(s.Stdout, path)
}
