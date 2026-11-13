package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// testCPUFile records, per sharded package (slowPackages), the CPU seconds
// of each top-level test: user and system time of the test process and of
// every child it waited for (the go builds and programs most tests of
// internal/sketch/lower start), each test run alone (-test.parallel=1) in
// one process of the package (`-record-durations -cpu`). The gate deals a
// package's shards by these rows when the package has them (shardCosts).
//
// durationsFile records wall time, which is not the cost of a shard: a test
// that builds Go spends several CPUs for each second of wall. Batch 16b
// gate r5 dealt internal/sketch/lower by wall time into shards that took
// 948, 458, 293 and 372 CPU-s; its first shard held
// TestCorpusCompilesWithGc, which durationsFile has no row for (it runs
// only with corpusGcEnvironment) and so weighed the median, and it passed
// go test's 15 minute timeout under load (gate16b-fix3). The size rule
// (sizeCutoff) keeps reading wall time: the fast tier's selection does not
// move when this file does.
const testCPUFile = "scripts/gate/test-cpu.json"

// readTestCPU reads testCPUFile under root; no file is no table.
func readTestCPU(root string) (map[string]map[string]float64, error) {
	content, err := os.ReadFile(filepath.Join(root, testCPUFile))
	if os.IsNotExist(err) {
		return map[string]map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var table map[string]map[string]float64
	if err := json.Unmarshal(content, &table); err != nil {
		return nil, fmt.Errorf("%s: %w", testCPUFile, err)
	}
	return table, nil
}

// shardCosts is the cost per test that deals the shards of the package
// importPath: its rows of testCPUFile, or its wall-time rows of
// durationsFile when it has none.
func shardCosts(durations, cpu map[string]map[string]float64, importPath string) map[string]float64 {
	if rows := cpu[importPath]; len(rows) > 0 {
		return rows
	}
	return durations[importPath]
}

// hangFloor is the least deadline of a gate go test step: a deadline only
// stops a hung test and names it (AGENTS.md), so no step has one near its
// cost. go test's own default, 15 minutes, was the de-facto budget of
// internal/sketch/lower's heaviest shard until gate16b-fix3.
const hangFloor = 30 * time.Minute

// hangFactor is how many times its recorded cost a gate go test step may
// take before its deadline stops it: load slows a step, it never fails it.
const hangFactor = 10

// hangDeadline is the deadline of a step or a run of recorded cost seconds:
// hangFactor times the cost, at least hangFloor, in whole minutes.
func hangDeadline(cost float64) time.Duration {
	d := time.Duration(hangFactor * cost * float64(time.Second))
	return max(hangFloor, d.Round(time.Minute))
}

// testTimeoutFor is the -timeout flag of a go test step of recorded cost
// seconds (hangDeadline).
func testTimeoutFor(cost float64) string {
	return "-timeout=" + hangDeadline(cost).String()
}

// testTimeout is the -timeout of a go test step with no recorded cost:
// hangFloor.
var testTimeout = testTimeoutFor(0)

// cpuSpans attributes a test process's CPU to its top-level tests from its
// -test.v output, one line at a time. The process runs one test at a time
// (-test.parallel=1), so the CPU between two lines belongs to the
// top-level test that was running: the one a "=== RUN" or "=== CONT" line
// of it or of its subtests named last, until its own "=== PAUSE" or its
// result line. CPU while no test runs (TestMain, a paused test's wait) is
// nobody's.
type cpuSpans struct {
	running string
	last    float64
	seconds map[string]float64
}

func newCPUSpans(start float64) *cpuSpans {
	return &cpuSpans{last: start, seconds: map[string]float64{}}
}

// line reads one output line, with now the process's CPU seconds when the
// line arrived.
func (s *cpuSpans) line(text string, now float64) {
	text = strings.TrimSpace(text)
	var action, name string
	for _, prefix := range []string{"=== RUN", "=== CONT", "=== PAUSE", "--- PASS:", "--- FAIL:", "--- SKIP:"} {
		if rest, ok := strings.CutPrefix(text, prefix); ok {
			action, name = prefix, strings.Fields(rest + " ")[0]
			break
		}
	}
	if name == "" {
		return
	}
	if s.running != "" {
		s.seconds[s.running] += now - s.last
	}
	s.last = now
	top, _, sub := strings.Cut(name, "/")
	switch {
	case action == "=== RUN" || action == "=== CONT":
		s.running = top
	case !sub && top == s.running:
		s.running = ""
	}
}

// rounded are the attributed seconds, to a tenth.
func (s *cpuSpans) rounded() map[string]float64 {
	out := map[string]float64{}
	for name, seconds := range s.seconds {
		out[name] = math.Round(seconds*10) / 10
	}
	return out
}

// cpuSpanWriter feeds a test process's output to spans. The process is a
// shell that prints its pid on the first line and then executes the test
// binary in its place, so every later line is sampled against that pid.
type cpuSpanWriter struct {
	mu      sync.Mutex
	pending []byte
	pid     int
	spans   *cpuSpans
	err     error
}

func (w *cpuSpanWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, content...)
	for {
		i := slices.Index(w.pending, '\n')
		if i < 0 {
			return len(content), nil
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		if w.pid == 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				w.err = fmt.Errorf("want the test process's pid first, got %q", line)
				continue
			}
			w.pid = pid
			continue
		}
		now, err := processCPU(w.pid)
		if err != nil {
			w.err = err
			continue
		}
		if w.spans == nil {
			w.spans = newCPUSpans(0)
		}
		w.spans.line(line, now)
	}
}

// cpuRecordFlags run a test binary for writeTestCPU: verbose, once, one
// test at a time, with a deadline for a hang only.
var cpuRecordFlags = []string{"-test.v=true", "-test.count=1", "-test.parallel=1", "-test.timeout=" + (10 * hangFloor).String()}

// writeTestCPU measures the CPU seconds of each test of the named packages
// and rewrites their rows of testCPUFile. Each package's test binary runs
// with the variables of its full-tier steps, once each step it needs (the
// CLI's build) has run, under the memory budget of limit.
func writeTestCPU(root string, limit limiter, packages []string) error {
	table, err := readTestCPU(root)
	if err != nil {
		return err
	}
	steps, err := gateSteps(root)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "gate-test-cpu-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	module := modulePath(root)
	prepared := map[string]bool{"go test build": true}
	for _, pkg := range packages {
		importPath := module + "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(pkg)), "./")
		i := slices.IndexFunc(steps, func(s *Step) bool {
			return strings.HasPrefix(s.Name, "go test ") && s.Argv[len(s.Argv)-1] == importPath
		})
		if i < 0 {
			return fmt.Errorf("%s: no full-tier go test step runs it", pkg)
		}
		step := steps[i]
		for _, need := range step.Needs {
			if prepared[need] {
				continue
			}
			prepared[need] = true
			j := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == need })
			if j < 0 {
				return fmt.Errorf("no full-tier step %q", need)
			}
			// The build is a go build or the staged build's reviewed sh
			// script (stagedBuild), as in writeFastCPU.
			var command *exec.Cmd
			switch argv := steps[j].Argv; argv[0] {
			case "go":
				command = exec.Command("go", argv[1:]...)
			case "sh":
				command = exec.Command("sh", argv[1:]...)
			default:
				return fmt.Errorf("%s: not a go build or a reviewed script: %q", need, argv)
			}
			command.Env, command.Dir = append(stepEnvironment(), steps[j].Env...), root
			command.Stdout, command.Stderr = io.Discard, os.Stderr
			if used, err := runBudgeted(limit, need, command); used.verdict != "" || err != nil {
				return fmt.Errorf("%s: %s %v", need, used.verdict, err)
			}
		}
		binary := filepath.Join(scratch, filepath.Base(importPath)+".test")
		build := exec.Command("go", "test", "-c", "-o", binary, importPath)
		build.Env, build.Dir = stepEnvironment(), root
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("go test -c %s: %w", importPath, err)
		}
		// The variables name files from the module root (cliTestEnvironment);
		// go test runs a binary in its package's directory, and so does this.
		env := append(stepEnvironment(), step.Env...)
		for k, v := range env {
			if name, value, ok := strings.Cut(v, "="); ok && slices.Contains(cliTestEnvironment, v) {
				env[k] = name + "=" + filepath.Join(root, value)
			}
		}
		temporary := filepath.Join(scratch, "tmp")
		if err := os.MkdirAll(temporary, 0o755); err != nil {
			return err
		}
		command := exec.Command("sh", append([]string{"-c", `echo $$; exec "$0" "$@"`, binary}, cpuRecordFlags...)...)
		command.Env = setEnvironmentVariable(env, "TMPDIR", temporary)
		command.Dir = filepath.Join(root, strings.TrimPrefix(importPath, module+"/"))
		writer := &cpuSpanWriter{}
		command.Stdout, command.Stderr = writer, os.Stderr
		used, err := runBudgeted(limit, "test cpu "+pkg, command)
		if used.verdict != "" {
			return fmt.Errorf("%s: %s", pkg, used.verdict)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", pkg, err)
		}
		if writer.err != nil {
			return fmt.Errorf("%s: %w", pkg, writer.err)
		}
		if writer.spans == nil {
			return fmt.Errorf("%s: no test ran", pkg)
		}
		table[importPath] = writer.spans.rounded()
	}
	data, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, testCPUFile), append(data, '\n'), 0o644)
}

// processCPUFrom reads the CPU seconds of a process from its /proc stat
// line: user and system time, its own and its waited-for children's
// (fields 14 to 17), in clock ticks of 1/100 s (USER_HZ).
func processCPUFrom(stat string) (float64, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, fmt.Errorf("not a stat line: %q", stat)
	}
	// After the command: state is field 3, so field n is index n-3.
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 15 {
		return 0, fmt.Errorf("short stat line: %q", stat)
	}
	ticks := 0.0
	for _, field := range fields[11:15] {
		n, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return 0, fmt.Errorf("stat field %q: %w", field, err)
		}
		ticks += n
	}
	return ticks / 100, nil
}
