package build

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CoverVariable names a directory that turns on the coverage build (GATE2
// GT7). The mutant runner's coverage pass (scripts/mutants) sets it, so
// that a program the ergo CLI builds while a test runs records which of
// its lines run, and those lines map back to the .ergo source through the
// lowered Go's //line directives.
//
// With it set, Build copies the emitted module's own files (main.go, its
// assets, its own packages, go.mod and go.sum; not a nested module such
// as the runtime, which a replace reaches) into DIR/KEY, under the
// module path coverModule/KEY, so that the blocks of two programs, which
// both are ergo.local/program, are never merged under one file name, and
// links that copy with go build -cover of the program's package. DIR/KEY
// stays: the runner reads its Go files to map a block to .ergo lines. The
// copy adds one package, covdir, whose init gives a program started with
// no GOCOVERDIR (a case command the examples runner starts with a clean
// environment) the GOCOVERDIR of its nearest ancestor that has one, so the
// counters land in the running test's directory and the program never
// prints the cover runtime's "GOCOVERDIR not set" warning; the main
// package then unsets it again, so the program sees the environment it
// was given. covdir also writes the counters when SIGINT or SIGTERM ends
// the program (a server a test interrupts, GT7b) and then dies of that
// signal. A module whose own Go registers a handler for signals
// (registersHandler) gets no flush and keeps its handler. Without the variable nothing here runs, and the emitted
// module, the cache and the direct path are as before.
const CoverVariable = "ERGO_COVER_GO"

// CoverFlags are the go build flags of a coverage build, before its
// -coverpkg. The mode is atomic: covdir writes the counters itself when a
// signal ends the program (runtime/coverage.WriteCountersDir needs atomic
// counters), and the mutant runner builds its tests in the same mode, as
// go tool covdata refuses two modes in one directory.
const CoverFlags = "-cover -covermode=atomic"

// coverModule is the module path prefix of a coverage build's copy.
const coverModule = "ergo.local/cover"

// coverBuild links the coverage copy of module to output.
func (t Toolchain) coverBuild(module Module, output string, stderr io.Writer) (Report, error) {
	report := Report{Refused: "coverage build (" + CoverVariable + ")"}
	dir, importPath, err := coverCopy(t.Cover, module)
	if err != nil {
		return report, fmt.Errorf("coverage build: %w", err)
	}
	covered := t
	covered.GOFLAGS = strings.TrimSpace(CoverFlags + " -coverpkg=" + importPath + " " + t.GOFLAGS)
	child := covered.Command(Module{Dir: dir}, output)
	child.Stdout, child.Stderr = stderr, stderr
	if err := child.Run(); err != nil {
		return report, fmt.Errorf("Go build failed: %w", err)
	}
	return report, nil
}

// CoverCopy makes the coverage copy of the Go module at dir under root, as
// Build does with CoverVariable set (GATE2 GT7b): a test harness that
// links lowered programs itself (internal/sketch/lower's dispatcher) links
// the copy with -cover, so the mutant runner maps its blocks as it maps a
// coverage build's. It returns the copy's directory and import path; the
// copy holds the covdir package at IMPORT/covdir.
func CoverCopy(root, dir string) (string, string, error) {
	return coverCopy(root, Module{Dir: dir})
}

// coverKey names a module's coverage copy: its emission key, or the digest
// of its directory when it has none.
func coverKey(module Module) string {
	key := module.Key
	if key == "" {
		sum := sha256.Sum256([]byte(module.Dir))
		key = hex.EncodeToString(sum[:])
	}
	if len(key) > 16 {
		key = key[:16]
	}
	return key
}

// coverCopy makes (once per key) the coverage copy of module under root
// and returns its directory and import path.
func coverCopy(root string, module Module) (string, string, error) {
	if !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("%s=%q is not an absolute path", CoverVariable, root)
	}
	key := coverKey(module)
	importPath := coverModule + "/" + key
	dir := filepath.Join(root, key)
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return dir, importPath, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	staging, err := os.MkdirTemp(root, key+".staging-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(staging)
	if err := fillCoverCopy(staging, module.Dir, importPath); err != nil {
		return "", "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		// Another build of the same key published it first.
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, importPath, nil
		}
		return "", "", err
	}
	return dir, importPath, nil
}

// fillCoverCopy writes the coverage copy of the module at src into dst.
func fillCoverCopy(dst, src, importPath string) error {
	mod, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return err
	}
	old := modulePathOf(mod)
	if old == "" {
		return errors.New("go.mod names no module")
	}
	handles := false
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == manifestName:
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				// A nested module (the runtime at ./ergo) is reached
				// through the copy's absolute replace.
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case rel == "go.mod":
			if data, err = coverGoMod(data, src, importPath); err != nil {
				return err
			}
		case strings.HasSuffix(rel, ".go"):
			handles = handles || registersHandler(string(data))
			// An import of the module's own packages names the copy's
			// path; the line count does not change.
			text := strings.ReplaceAll(string(data), `"`+old+`/`, `"`+importPath+`/`)
			data = []byte(strings.ReplaceAll(text, `"`+old+`"`, `"`+importPath+`"`))
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dst, "covdir"), 0o755); err != nil {
		return err
	}
	covdir := covdirSource
	if handles {
		// The program handles SIGINT or SIGTERM itself: covdir leaves
		// them alone, so the program keeps its handler (its counters are
		// written when that handler lets it exit).
		covdir = strings.Replace(covdir, flushCall, "\t// The program handles SIGINT or SIGTERM itself (internal/sketch/build, cover.go).\n", 1)
	}
	if err := os.WriteFile(filepath.Join(dst, "covdir", "covdir.go"), []byte(covdir), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, "ergo_cover.go"), []byte(strings.ReplaceAll(coverMainSource, "IMPORT", importPath)), 0o644)
}

// flushCall is covdir's call that flushes the counters on a signal.
const flushCall = "\tflushOnSignal(dir)\n"

// registersHandler reports Go source that registers a handler of its own
// for signals (os/signal's Notify or NotifyContext). A copy of such a
// module gets no signal flush: the flush resets the signal after it
// writes the counters, which would drop the program's handler. It reads
// the module's own files only; a handler a required module registers is
// not seen.
func registersHandler(src string) bool {
	return strings.Contains(src, "signal.Notify")
}

// manifestName is the emitted module's manifest, which the coverage copy
// does not hold.
const manifestName = "ergo-manifest.json"

// modulePathOf is the module path go.mod names, or "".
func modulePathOf(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// coverGoMod is the emitted go.mod with the module path importPath and
// every relative replace made absolute against src.
func coverGoMod(data []byte, src, importPath string) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	named := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") && !named {
			// The emitted module is one main package: no file imports
			// the module's own path, so it can be renamed.
			lines[i] = "module " + importPath
			named = true
			continue
		}
		at := strings.Index(line, "=> ")
		if at < 0 {
			continue
		}
		target := strings.TrimSpace(line[at+3:])
		if strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") {
			lines[i] = line[:at+3] + filepath.ToSlash(filepath.Join(src, filepath.FromSlash(target)))
		}
	}
	if !named {
		return nil, errors.New("go.mod names no module")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// covdirSource is the coverage copy's covdir package (CoverVariable).
const covdirSource = `// Package covdir gives a coverage build started with no GOCOVERDIR the
// GOCOVERDIR of its nearest ancestor process that has one (written by
// internal/sketch/build, coverage builds only). A program an interrupt or a
// termination ends (a server a test stops) dies of the signal before the
// cover runtime's exit hook runs, so covdir writes the counters first and
// then dies of the same signal, as the program would have.
package covdir

import (
	"os"
	"os/signal"
	"runtime/coverage"
	"strconv"
	"strings"
	"syscall"
)

// Set reports that init set GOCOVERDIR; the main package unsets it.
var Set bool

func init() {
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		dir = ancestorDir()
		if dir == "" {
			dir = os.TempDir() + "/ergo-cover-orphans"
			os.MkdirAll(dir, 0o755)
		}
		os.Setenv("GOCOVERDIR", dir)
		Set = true
	}
	flushOnSignal(dir)
}

// flushOnSignal writes the counters into dir when SIGINT or SIGTERM
// arrives, then resets the signal and raises it again. A signal the
// program inherited ignored stays ignored: Notify would end that.
func flushOnSignal(dir string) {
	var sigs []os.Signal
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !signal.Ignored(s) {
			sigs = append(sigs, s)
		}
	}
	if len(sigs) == 0 {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	go func() {
		s := <-ch
		coverage.WriteMetaDir(dir)
		coverage.WriteCountersDir(dir)
		signal.Reset(s)
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			p.Signal(s)
		}
	}()
}

func ancestorDir() string {
	pid := os.Getppid()
	for range 64 {
		if pid <= 1 {
			return ""
		}
		id := strconv.Itoa(pid)
		if env, err := os.ReadFile("/proc/" + id + "/environ"); err == nil {
			for _, kv := range strings.Split(string(env), "\x00") {
				if v, ok := strings.CutPrefix(kv, "GOCOVERDIR="); ok && v != "" {
					return v
				}
			}
		}
		stat, err := os.ReadFile("/proc/" + id + "/stat")
		if err != nil {
			return ""
		}
		s := string(stat)
		fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(fields) < 2 {
			return ""
		}
		if pid, err = strconv.Atoi(fields[1]); err != nil {
			return ""
		}
	}
	return ""
}
`

// coverMainSource is the coverage copy's file of the main package: it
// imports covdir (whose init runs before the cover runtime reads
// GOCOVERDIR) and unsets what covdir set.
const coverMainSource = `package main

import (
	"os"

	"IMPORT/covdir"
)

func init() {
	if covdir.Set {
		os.Unsetenv("GOCOVERDIR")
	}
}
`
