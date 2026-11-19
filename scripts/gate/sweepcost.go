package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// sweepCostFile records what the closure sweep of each package really
// took, by package directory (gate-speed GS23, compounding GT3): the wall
// of its slowest run and of its whole sweep, the build of its test binary
// included. internal/closure reads it for each run's deadline, and the gate
// for the sweep step's weight and timeout (sweepWeight). A swept run starts
// from an empty Go build cache, so a test that builds Go takes far more
// there than in the warm go test durations.json records:
// internal/sketchfront recorded 2 s of test time and swept in 16 to 30
// minutes, so its runs' deadline was the 30 minute floor, which three
// sweeps of batch 18 passed under load. `-record-durations -sweeps`
// writes it (writeSweepCosts).
const sweepCostFile = "scripts/gate/sweep-cost.json"

// sweepCostOutVariable names the file a sweep writes its packages' real
// costs to (internal/closure, the same name).
const sweepCostOutVariable = "ERGO_CLOSURE_COST_OUT"

// sweepRecordDeadline bounds each run of a sweep that records its cost:
// the record replaces the deadline the old record set, so it must not stop
// the run it measures. It still stops a run that hangs.
const sweepRecordDeadline = "4h"

// sweepCost is one package's recorded sweep, in seconds of wall.
type sweepCost struct {
	Run   float64 `json:"run"`
	Sweep float64 `json:"sweep"`
}

// readSweepCosts reads sweepCostFile under root; no file is no table.
func readSweepCosts(root string) (map[string]sweepCost, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sweepCostFile)))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]sweepCost{}, nil
	}
	if err != nil {
		return nil, err
	}
	var costs map[string]sweepCost
	if err := json.Unmarshal(data, &costs); err != nil {
		return nil, fmt.Errorf("%s: %w", sweepCostFile, err)
	}
	return costs, nil
}

// sweepStaleFactor is how far a sweep may outrun its recorded cost before
// the gate says the record is stale.
const sweepStaleFactor = 3

// sweepCostStale is the check of a sweep's record against its last real
// run, seconds of wall: "" while the run took at most sweepStaleFactor
// times the recorded sweep, else what to do. A package with no record is
// stale once its run took more than the hang floor's worth of a step
// with no record. Under load a sweep runs longer, and the record only
// sets bounds, so the line is a note on the step, not a failure: the
// failure a stale record causes is the deadline it sets.
func sweepCostStale(dir string, recorded sweepCost, ran float64) string {
	if recorded.Sweep <= 0 {
		if ran <= hangFloor.Seconds()/sweepStaleFactor {
			return ""
		}
		return fmt.Sprintf("gate: closure sweep ./%s ran %.0fs and has no row in %s, so its runs' deadline is the floor; the gate writes its row at the end of this run (or record it: ./scripts/gate.sh -record-durations -sweeps %s)", dir, ran, sweepCostFile, dir)
	}
	if ran <= sweepStaleFactor*recorded.Sweep {
		return ""
	}
	return fmt.Sprintf("gate: closure sweep ./%s ran %.0fs, more than %d times its recorded %.0fs (%s); the gate rewrites its row at the end of this run (or record it: ./scripts/gate.sh -record-durations -sweeps %s)", dir, ran, sweepStaleFactor, recorded.Sweep, sweepCostFile, dir)
}

// rewriteStaleSweeps rewrites the rows of sweepCostFile under root whose
// sweep this run found stale (sweepCostStale), dir to the seconds it ran,
// so the record corrects itself without a separate record run: the sweep
// becomes what it ran, and its slowest run grows in the same proportion (a
// package with no row: the whole run). The gate calls it after every step
// has ended and its records are written, since a sweep fails on any change
// of the checkout while it runs. It returns the directories it rewrote.
func rewriteStaleSweeps(root string, stale map[string]float64) ([]string, error) {
	if len(stale) == 0 {
		return nil, nil
	}
	table, err := readSweepCosts(root)
	if err != nil {
		return nil, err
	}
	dirs := slices.Sorted(maps.Keys(stale))
	for _, dir := range dirs {
		ran := math.Ceil(stale[dir])
		row := table[dir]
		run := ran
		if row.Sweep > 0 {
			run = max(row.Run, math.Ceil(row.Run*ran/row.Sweep))
		}
		table[dir] = sweepCost{Run: run, Sweep: ran}
	}
	return dirs, writeSweepTable(root, table)
}

// writeSweepTable writes sweepCostFile under root, its rows in order.
func writeSweepTable(root string, table map[string]sweepCost) error {
	data, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, sweepCostFile), append(data, '\n'), 0o644)
}

// writeSweepCosts runs the closure sweep of each named package directory
// (every swept package when none is named), one after another, each under
// the memory budget of limit and with a deadline that does not stop it
// (sweepRecordDeadline), and rewrites their rows of sweepCostFile, keeping
// the other rows. A package whose sweep fails keeps its old row, and the
// record fails.
func writeSweepCosts(root string, limit limiter, out io.Writer, dirs []string) error {
	if !sweepSupported() {
		return errors.New("-sweeps: the closure sweep runs on Linux only")
	}
	table, err := readSweepCosts(root)
	if err != nil {
		return err
	}
	steps, err := gateSteps(root)
	if err != nil {
		return err
	}
	var sweeps []*Step
	for _, step := range steps {
		if step.Sweep != "" && (len(dirs) == 0 || slices.Contains(dirs, step.Sweep) || slices.Contains(dirs, "./"+step.Sweep)) {
			sweeps = append(sweeps, step)
		}
	}
	if len(sweeps) == 0 {
		return fmt.Errorf("-sweeps: no closure sweep step sweeps %q", dirs)
	}
	scratch, err := os.MkdirTemp("", "gate-sweep-cost-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	var failures []error
	for _, step := range sweeps {
		file := filepath.Join(scratch, strings.ReplaceAll(step.Sweep, "/", "_")+".json")
		argv := slices.Clone(step.Argv)
		for i, a := range argv {
			if strings.HasPrefix(a, "-timeout=") {
				argv[i] = "-timeout=" + sweepRecordDeadline
			}
		}
		command := exec.Command(argv[0], argv[1:]...)
		command.Dir = root
		command.Env = append(append(stepEnvironment(), step.Env...), sweepCostOutVariable+"="+file, "ERGO_CLOSURE_RUN_DEADLINE="+sweepRecordDeadline)
		command.Stdout, command.Stderr = out, out
		fmt.Fprintf(out, "gate: recording %s\n", step.Name)
		used, err := runBudgeted(limit, step.Name, command)
		if used.verdict != "" || err != nil {
			failures = append(failures, fmt.Errorf("%s: %s %v", step.Name, used.verdict, err))
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: no cost written: %w", step.Name, err))
			continue
		}
		var measured map[string]sweepCost
		if err := json.Unmarshal(data, &measured); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", step.Name, err))
			continue
		}
		cost, ok := measured[step.Sweep]
		if !ok {
			failures = append(failures, fmt.Errorf("%s: the sweep wrote no cost for %s", step.Name, step.Sweep))
			continue
		}
		table[step.Sweep] = cost
		fmt.Fprintf(out, "gate: %s: slowest run %.0fs, sweep %.0fs\n", step.Name, cost.Run, cost.Sweep)
	}
	if err := writeSweepTable(root, table); err != nil {
		return err
	}
	return errors.Join(failures...)
}
