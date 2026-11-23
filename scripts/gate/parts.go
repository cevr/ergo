package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Parts (LOWER-SHORT-FITS). A lane cannot run a large package's -short
// tests in one go test process inside its timeout (internal/sketch/lower:
// 292 tests one after another under load), and so cannot compare it with
// main by failure line. `-parts N PKG` deals the package's tests into N
// fixed parts, one go test selection each; the integrator runs each part
// on main and keeps its failure lines, and a lane runs the same parts at
// its head and compares part by part.
//
// The deal is a function of the committed record alone (testWorkFile): the
// names it has rows for, dealt by shard's rule (longest first onto the
// lightest part) over their counted work. A test the record lacks (a
// lane's new test) runs in the last part, the complement of the others'
// names (shardArgs), so it never moves a recorded test to another part,
// and every test of the package runs in exactly one part. With no rows for
// the package, the deal is over the source's test names, one unit each.
//
// The weight of a test is counted work, never time (AGENTS.md, Tests): one
// for the test, plus each item of work lower's probe log counts for it in
// the recording run (ERGO_SPLIT_PROBE, internal/sketch/lower split_test.go):
// lowerers made and bodies lowered, programs built or run, functions
// type-checked, source files go/types reads. `-record-work PROBE PKG`
// rewrites the package's rows from one such log.

// testWorkFile records, per package, the counted work of each top-level
// test in a -short run: kind ("lower", "build", "run", …) to count.
const testWorkFile = "scripts/gate/test-work.json"

// readTestWork reads testWorkFile under root; no file is no table.
func readTestWork(root string) (map[string]map[string]map[string]int, error) {
	content, err := os.ReadFile(filepath.Join(root, testWorkFile))
	if os.IsNotExist(err) {
		return map[string]map[string]map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	var table map[string]map[string]map[string]int
	if err := json.Unmarshal(content, &table); err != nil {
		return nil, fmt.Errorf("%s: %w", testWorkFile, err)
	}
	return table, nil
}

// workWeight is a test's weight: one for the test and one for each counted
// item of work.
func workWeight(kinds map[string]int) float64 {
	w := 1
	for _, n := range kinds {
		w += n
	}
	return float64(w)
}

// partsOf is the N go test selections of a package whose record rows are
// rows and whose source defines names: each before the last runs exactly
// its names, the last skips every name the others run. The recorded names
// are dealt when there are any, else the source's; n is at most the number
// dealt.
func partsOf(rows map[string]map[string]int, names []string, n int) [][]string {
	dealt := slices.Sorted(maps.Keys(rows))
	weights := map[string]float64{}
	for name, kinds := range rows {
		weights[name] = workWeight(kinds)
	}
	if len(dealt) == 0 {
		dealt = slices.Clone(names)
		for _, name := range dealt {
			weights[name] = 1
		}
	}
	n = max(1, min(n, len(dealt)))
	return shardArgs(shard(dealt, weights, n))
}

// printParts writes the selections of `-parts n dir`, one line each:
// "<part> -run=<pattern>" or "<part> -skip=<pattern>", the last part with
// nothing to skip "<part>" alone.
func printParts(root, dir string, n int, out io.Writer) error {
	if n < 1 {
		return fmt.Errorf("-parts %d: at least one part", n)
	}
	importPath, names, err := packageTests(root, dir)
	if err != nil {
		return err
	}
	table, err := readTestWork(root)
	if err != nil {
		return err
	}
	for k, selection := range partsOf(table[importPath], names, n) {
		line := fmt.Sprint(k)
		if selection != nil {
			line += " " + selection[0] + "=" + selection[1]
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

// packageTests is the import path of the package in dir (relative to root)
// and its test names (testNames).
func packageTests(root, dir string) (string, []string, error) {
	rel := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(dir)), "./")
	if rel == "." || strings.HasPrefix(rel, "../") || filepath.IsAbs(dir) {
		return "", nil, fmt.Errorf("%s: name a package directory of the module", dir)
	}
	names, err := testNames(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", nil, err
	}
	if len(names) == 0 {
		return "", nil, fmt.Errorf("%s has no tests", dir)
	}
	return modulePath(root) + "/" + rel, names, nil
}

// probeWork counts a probe log's work by top-level test and kind: each
// line is "<kind> <sum> <test> [<n>]", n items of work (one when left
// out); a subtest's name counts for its top-level test. A line without a
// test is counted in unnamed.
func probeWork(r io.Reader) (work map[string]map[string]int, unnamed int, err error) {
	work = map[string]map[string]int{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			unnamed++
			continue
		}
		test, _, _ := strings.Cut(fields[2], "/")
		if work[test] == nil {
			work[test] = map[string]int{}
		}
		n := 1
		if len(fields) > 3 {
			if _, err := fmt.Sscan(fields[3], &n); err != nil {
				return nil, 0, fmt.Errorf("probe line %q: %w", scanner.Text(), err)
			}
		}
		work[test][fields[0]] += n
	}
	return work, unnamed, scanner.Err()
}

// recordWork rewrites the rows of the package in dir in testWorkFile from
// the probe log at probe: a row for each test the source defines (one that
// logged nothing has no kinds) and each the log names.
func recordWork(root, dir, probe string, out io.Writer) error {
	importPath, names, err := packageTests(root, dir)
	if err != nil {
		return err
	}
	f, err := os.Open(probe)
	if err != nil {
		return err
	}
	defer f.Close()
	work, unnamed, err := probeWork(f)
	if err != nil {
		return err
	}
	rows := map[string]map[string]int{}
	for _, name := range names {
		rows[name] = map[string]int{}
	}
	for name, kinds := range work {
		rows[name] = kinds
	}
	table, err := readTestWork(root)
	if err != nil {
		return err
	}
	table[importPath] = rows
	content, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, testWorkFile), append(content, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "gate: %s: %d tests recorded in %s (%d probe lines named no test)\n", importPath, len(rows), testWorkFile, unnamed)
	return nil
}
