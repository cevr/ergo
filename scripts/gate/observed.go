package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Observed test times (gate-speed GS33b). A fast-tier test step, and a
// full-tier step that runs package tests uncached, runs `go test -json`;
// the gate keeps each top-level test's wall time in the gate cache, the
// last observedKeep times per test, and the size rule (sizeCutoff) reads
// the largest of them. durationsFile is only the seed:
// a test with no observation keeps its recorded time, and a test with
// neither runs in the fast tier once and is observed. So a test that takes
// sizeCutoff or longer in a fast run leaves the fast tier at the next run,
// with no file a lane has to re-record by hand (the record had no time for
// 37 of internal/sketchfront's 58 tests, and TestStdSnapshotAgrees, 147 s,
// ran in every fast run). A large test is not run by a fast run again, but
// each full run observes it (without -short, so its time is the larger
// one): once its last observedKeep times are all under the cutoff it
// comes back to the fast tier. One time over the cutoff keeps it out for
// observedKeep runs, so a test near the cutoff does not flap.

// observedKeep is how many times of one test the store keeps.
const observedKeep = 3

// observedVersion names the store's format.
const observedVersion = 1

// observedRecord is one package's file in the store.
type observedRecord struct {
	Version int                  `json:"version"`
	Tests   map[string][]float64 `json:"tests"`
}

// observedPath is the store's file of the package importPath.
func observedPath(cacheDir, importPath string) string {
	return filepath.Join(cacheDir, "test-seconds", strings.ReplaceAll(importPath, "/", "_")+".json")
}

// loadObserved reads a package's observed times; a missing or unreadable
// file is no observation.
func loadObserved(cacheDir, importPath string) map[string][]float64 {
	if cacheDir == "" {
		return nil
	}
	data, err := os.ReadFile(observedPath(cacheDir, importPath))
	if err != nil {
		return nil
	}
	var record observedRecord
	if json.Unmarshal(data, &record) != nil || record.Version != observedVersion {
		return nil
	}
	return record.Tests
}

// observeTests adds the times of one run of a package's tests to its file
// in the store, keeping the last observedKeep of each test.
func observeTests(cacheDir, importPath string, seconds map[string]float64) error {
	if cacheDir == "" || len(seconds) == 0 {
		return nil
	}
	tests := loadObserved(cacheDir, importPath)
	if tests == nil {
		tests = map[string][]float64{}
	}
	for name, s := range seconds {
		kept := append(tests[name], s)
		if len(kept) > observedKeep {
			kept = kept[len(kept)-observedKeep:]
		}
		tests[name] = kept
	}
	data, err := json.Marshal(observedRecord{Version: observedVersion, Tests: tests})
	if err != nil {
		return err
	}
	path := observedPath(cacheDir, importPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "seconds-")
	if err != nil {
		return err
	}
	_, err = temporary.Write(data)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), path)
	}
	if err != nil {
		_ = os.Remove(temporary.Name())
	}
	return err
}

// testSeconds is the size rule's time of each test: durationsFile's rows,
// with each test the store has observed taking the largest of its kept
// times instead.
func testSeconds(root, cacheDir string) (map[string]map[string]float64, error) {
	durations, err := readDurations(root)
	if err != nil {
		return nil, err
	}
	if cacheDir == "" {
		return durations, nil
	}
	packages, _, err := testPackages(root)
	if err != nil {
		return nil, err
	}
	for _, p := range packages {
		durations[p.importPath] = observedRow(cacheDir, p.importPath, durations[p.importPath])
	}
	return durations, nil
}

// observedRow is the package's row of recorded times with each test the
// store observed taking the largest of its kept times instead.
func observedRow(cacheDir, importPath string, recorded map[string]float64) map[string]float64 {
	row := maps.Clone(recorded)
	if row == nil {
		row = map[string]float64{}
	}
	for name, times := range loadObserved(cacheDir, importPath) {
		if len(times) > 0 {
			row[name] = slices.Max(times)
		}
	}
	return row
}

// testEvent is the part of a `go test -json` event the gate reads.
type testEvent struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Output  string
}

// testWallSeconds are the top-level tests' wall times of a `go test
// -json` stream, by package, as writeDurations measures them: Elapsed
// omits time a test spends waiting for its parallel subtests, so a test's
// time runs from its start or, for a parallel test, from when it resumes,
// to its end.
func testWallSeconds(stream io.Reader) (map[string]map[string]float64, error) {
	measured := map[string]map[string]float64{}
	started := map[[2]string]time.Time{}
	err := eachTestEvent(stream, func(event testEvent) {
		if event.Test == "" || strings.Contains(event.Test, "/") {
			return
		}
		key := [2]string{event.Package, event.Test}
		switch event.Action {
		case "run", "cont":
			started[key] = event.Time
		case "pass", "fail", "skip":
			if at, ok := started[key]; ok {
				if measured[event.Package] == nil {
					measured[event.Package] = map[string]float64{}
				}
				measured[event.Package][event.Test] = math.Round(event.Time.Sub(at).Seconds()*100) / 100
			}
		}
	}, nil)
	return measured, err
}

// eachTestEvent calls event for each JSON line of stream and other, when
// set, for each line that is not one (a build error on stderr).
func eachTestEvent(stream io.Reader, event func(testEvent), other func(string)) error {
	reader := bufio.NewReader(stream)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			var e testEvent
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &e) == nil && e.Action != "" {
				event(e)
			} else if other != nil {
				other(line)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// renderTestText writes what plain `go test` prints for a `go test -json`
// stream: the package's own lines, and a top-level test's lines (its
// subtests' included) only when it failed or never ended (a timeout or a
// crash), without the lines -json's implied -v adds. A line that is not
// JSON (stderr) passes as it is.
func renderTestText(stream io.Reader, out io.Writer) error {
	held := map[[2]string]*bytes.Buffer{}
	var order [][2]string
	var werr error
	write := func(text string) {
		if werr == nil {
			_, werr = io.WriteString(out, text)
		}
	}
	verbose := func(text string) bool {
		for _, prefix := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- PASS:", "--- SKIP:"} {
			if strings.HasPrefix(strings.TrimLeft(text, " "), prefix) {
				return true
			}
		}
		return false
	}
	err := eachTestEvent(stream, func(event testEvent) {
		name, _, _ := strings.Cut(event.Test, "/")
		top := [2]string{event.Package, name}
		switch {
		case event.Action == "output" && name == "":
			if event.Output != "PASS\n" {
				write(event.Output)
			}
		case event.Action == "output":
			if verbose(event.Output) {
				return
			}
			if held[top] == nil {
				held[top] = &bytes.Buffer{}
				order = append(order, top)
			}
			held[top].WriteString(event.Output)
		case event.Test == name && (event.Action == "pass" || event.Action == "skip"):
			delete(held, top)
		case event.Test == name && event.Action == "fail":
			if b := held[top]; b != nil {
				write(b.String())
				delete(held, top)
			}
		}
	}, write)
	for _, top := range order {
		if b := held[top]; b != nil {
			write(b.String())
		}
	}
	if err != nil {
		return err
	}
	return werr
}

// observeStep reads the -json output in spool of a test step: it adds the
// tests' times to the store, by package, and returns a spool of the text
// plain go test prints, which the gate prints and records instead. spool
// is closed.
func observeStep(cacheDir, spoolDir string, spool *os.File) (*os.File, error) {
	defer spool.Close()
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	seconds, err := testWallSeconds(spool)
	if err != nil {
		return nil, err
	}
	for importPath, times := range seconds {
		if err := observeTests(cacheDir, importPath, times); err != nil {
			return nil, fmt.Errorf("observed test times: %w", err)
		}
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	text, err := newOutputSpool(spoolDir)
	if err != nil {
		return nil, err
	}
	if err := renderTestText(spool, text); err != nil {
		_ = text.Close()
		return nil, err
	}
	return text, nil
}
