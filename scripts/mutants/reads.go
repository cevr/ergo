package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/scripts/mutants/covfile"
)

// Source reads (GATE2 GT8). A test that reads a file of the checkout, as
// a test that parses its package's source does (check's
// TestMintFormNamesEveryKind reads ownership.go), runs none of that
// file's blocks, so line coverage never selects it for a mutant of the
// file. In the coverage pass each Go test binary writes the go command's
// test log (-test.testlogfile: every os.Open, os.Stat and os.Chdir of the
// test process), and every regular file of the checkout the test opened
// is recorded as a read: a block of the whole file (readBlock) whose tests
// are the readers, which coverage.covering adds for every line of the
// file. A file a test reads through go:embed is in the log when its
// package observes it (embedObserveVariable: std's .ergo sources and
// gostd's bindings); a file a program the test starts reads is not.

// embedObserveVariable is internal/sketch/stdlib's ObserveVariable (the
// gate's coverage record sets it too, gate-speed GS9): set to the root of
// the coverage pass's copy, a package that embeds its sources (stdlib for
// std, std/gostd) opens the copy's file of each embedded file it reads, so
// the test log names it (GT8b).
const embedObserveVariable = "ERGO_EMBED_OBSERVE"

// readBlock is the block line of a read: no block of the cover tool
// starts at line 0.
const readBlock = covfile.ReadBlock

// testLogName is the file a coverage run's test log goes to, beside its
// counters directory.
func testLogName(dir string) string { return dir + ".testlog" }

// reads records, for test, the files of root its test log (the testing
// package's -test.testlogfile format: "open NAME", "chdir NAME", …) names.
// A relative name is relative to the process's working directory, which
// starts at cwd and moves with each chdir.
func (k *collector) reads(test int, log io.Reader, cwd, root string) error {
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	seen := map[string]bool{}
	for scanner.Scan() {
		op, name, ok := strings.Cut(scanner.Text(), " ")
		if !ok || name == "" {
			continue
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		switch op {
		case "chdir":
			cwd = name
			continue
		case "open":
		default:
			continue
		}
		rel, err := filepath.Rel(root, filepath.Clean(name))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] || rel == ".git" || strings.HasPrefix(rel, ".git/") {
			continue
		}
		seen[rel] = true
		if info, err := os.Stat(name); err != nil || !info.Mode().IsRegular() {
			continue
		}
		k.record(blockKey{file: rel, start: readBlock, end: readBlock}, test)
	}
	return scanner.Err()
}
