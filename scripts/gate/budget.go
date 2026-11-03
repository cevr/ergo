package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The memory budget (see the package comment). Every step runs under a step
// budget, and the steps of one gate share a gate budget.
const (
	// stepMemoryVariable overrides the step budget, defaultStepMemory.
	stepMemoryVariable = "ERGO_GATE_STEP_MEMORY"
	// gateMemoryVariable overrides the gate budget, half of physical memory.
	gateMemoryVariable = "ERGO_GATE_MEMORY"
	// hostMemoryVariable overrides this gate's host budget, three quarters
	// of physical memory. It lowers only this gate's own budgets: the bound
	// on every gate of the host together is the bound of the gates' slice,
	// which belongs to the host, and no gate's setting changes it (see
	// boundHost).
	hostMemoryVariable = "ERGO_GATE_HOST_MEMORY"
	// mechanismVariable set to "rss" forces the RSS watch where the systemd
	// scope would work, so its tests run on Linux too.
	mechanismVariable = "ERGO_GATE_MEMORY_MECHANISM"
	// scopeVariable names, to the gate re-executed in its systemd scope, the
	// scope's unit. The gate reads it once and removes it from its
	// environment, so no step inherits it.
	scopeVariable = "ERGO_GATE_SCOPE"
)

// defaultStepMemory is the step budget unless stepMemoryVariable sets one:
// the owner's figure (2026-10-30). An uncached gate on main e36c9471 with
// -count=1 measured these step peaks: "go test (other packages)" 2.5G (it
// holds internal/sketch/check, which durations.json does not shard),
// "go test ./cmd/ergo" 602M, "go test build" 560M (1.0G with a cold build
// cache), every other step under 110M. So 8G leaves the largest step more
// than 3x headroom, and a runaway test, such as the checker test that
// reached 44 GB, stops at it. The gate budget, half of the workbox's 62G,
// is 31G: room for the concurrent steps of one gate (about 4G together at
// these peaks) and for one runaway step, while one gate never takes more
// than half the host. The default host bound, three quarters of it (47G),
// bounds the gates that run at once (up to three on the workbox) and leaves
// the rest of the host a quarter.
const defaultStepMemory int64 = 8 << 30

// defaultHostMemory is the default host budget, three quarters of physical
// memory rounded down, or 0 when physical memory is unknown. It is also the
// bound a gate gives the gates' slice when the slice has none.
func defaultHostMemory() int64 {
	total := physicalMemory()
	if total <= 0 {
		return 0
	}
	return roundedDown(total / 4 * 3)
}

// memoryBudget is the hard cap, in bytes, of each step, of all the steps of
// one gate together, and of all the gates of the host together.
type memoryBudget struct{ step, gate, host int64 }

// currentBudget reads the budget from the environment. The gate budget
// defaults to half of physical memory, so one gate cannot take the host, and
// the host budget to three quarters (defaultHostMemory). A budget above the
// one that holds it is lowered to it.
func currentBudget() (memoryBudget, error) {
	budget := memoryBudget{step: defaultStepMemory}
	for _, setting := range []struct {
		variable string
		size     *int64
		// fallback is the default when the variable is unset, or nil for the
		// fixed default already in size; 0 means physical memory is unknown.
		fallback func() int64
	}{
		{stepMemoryVariable, &budget.step, nil},
		{gateMemoryVariable, &budget.gate, func() int64 {
			if total := physicalMemory(); total > 0 {
				return roundedDown(total / 4 * 2)
			}
			return 0
		}},
		{hostMemoryVariable, &budget.host, defaultHostMemory},
	} {
		if value := os.Getenv(setting.variable); value != "" {
			parsed, err := parseBytes(value)
			if err != nil {
				return memoryBudget{}, fmt.Errorf("%s: %w", setting.variable, err)
			}
			*setting.size = parsed
			continue
		}
		if setting.fallback == nil {
			continue
		}
		if *setting.size = setting.fallback(); *setting.size > 0 {
			continue
		}
		if setting.variable == hostMemoryVariable {
			// The host budget is then the gate budget: one gate's bound.
			budget.host = budget.gate
			continue
		}
		return memoryBudget{}, fmt.Errorf("physical memory unknown on this host: set %s", setting.variable)
	}
	budget.gate = min(budget.gate, budget.host)
	budget.step = min(budget.step, budget.gate)
	return budget, nil
}

// roundedDown rounds a default budget down to whole gibibytes (mebibytes on
// a tiny host), which read well in the budget line.
func roundedDown(size int64) int64 {
	unit := int64(1 << 30)
	if size < unit {
		unit = 1 << 20
	}
	return size / unit * unit
}

// softLimit is the GOMEMLIMIT each step's Go processes get unless the
// caller sets one: three quarters of the step budget, so the collector
// works harder before the kernel or the watch stops the step.
func softLimit(budget memoryBudget) string {
	return strconv.FormatInt(budget.step/4*3, 10)
}

// stepSoftLimit is softLimit for the budget in the environment, or for the
// default budget when the environment's does not parse (the gate refuses
// to start then, so no step runs with it).
func stepSoftLimit() string {
	budget, err := currentBudget()
	if err != nil {
		budget = memoryBudget{step: defaultStepMemory, gate: defaultStepMemory, host: defaultStepMemory}
	}
	return softLimit(budget)
}

var physicalMemoryOnce = sync.OnceValue(totalMemory)

// physicalMemory is the host's memory in bytes, or 0 if unknown.
func physicalMemory() int64 { return physicalMemoryOnce() }

// parseBytes reads a size as systemd's MemoryMax does: a decimal number of
// bytes with an optional binary suffix K, M, G or T.
func parseBytes(value string) (int64, error) {
	digits, shift := value, 0
	if n := len(value); n > 0 {
		switch value[n-1] {
		case 'K', 'k':
			shift = 10
		case 'M', 'm':
			shift = 20
		case 'G', 'g':
			shift = 30
		case 'T', 't':
			shift = 40
		}
		if shift > 0 {
			digits = value[:n-1]
		}
	}
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || number <= 0 || number > (1<<62)>>shift {
		return 0, fmt.Errorf("%q is not a positive size such as 512M or 8G", value)
	}
	return number << shift, nil
}

// formatBytes writes a size the way parseBytes reads it, exactly where a
// suffix divides it, and to a tenth otherwise.
func formatBytes(size int64) string {
	for _, unit := range []struct {
		shift  uint
		suffix string
	}{{40, "T"}, {30, "G"}, {20, "M"}, {10, "K"}} {
		if size >= 1<<unit.shift {
			if size%(1<<unit.shift) == 0 {
				return strconv.FormatInt(size>>unit.shift, 10) + unit.suffix
			}
			return strconv.FormatFloat(float64(size)/float64(int64(1)<<unit.shift), 'f', 1, 64) + unit.suffix
		}
	}
	return strconv.FormatInt(size, 10)
}

// A limiter runs steps under the memory budget.
type limiter interface {
	// describe names the mechanism and the budget, for the gate's log.
	describe() string
	// prepare readies command, not yet started, to run the named step under
	// the step budget.
	prepare(name string, command *exec.Cmd) (stepWatch, error)
	close()
}

// stepWatch follows one step.
type stepWatch interface {
	// started is called once the command has started.
	started(pid int)
	// finish is called once the command has ended or failed to start.
	finish() usage
}

// usage is what a step used of its budget.
type usage struct {
	// peak is the most memory the step held, in bytes, or 0 if unknown.
	peak int64
	// verdict is empty, or says why the step fails whatever its exit
	// status: its budget or the gate's was exceeded, or the budget could not
	// be enforced.
	verdict string
	// processIssue fails a step that left processes alive at leader exit,
	// separately from its memory-budget verdict.
	processIssue string
	// stopFailed means the limiter could not prove every step process stopped.
	// In that case output readers are closed instead of waiting on descendants.
	stopFailed bool
}

func addProcessIssue(used *usage, message string) {
	if message == "" {
		return
	}
	if used.processIssue == "" {
		used.processIssue = message
		return
	}
	used.processIssue += "; " + message
}

func stepExceeded(budget memoryBudget) string {
	return fmt.Sprintf("memory budget exceeded (%s)", formatBytes(budget.step))
}

func gateExceeded(budget memoryBudget) string {
	return fmt.Sprintf("gate memory budget exceeded (%s); the step was the largest when the gate's steps together passed it", formatBytes(budget.gate))
}

func hostExceeded(host int64) string {
	return fmt.Sprintf("host memory budget exceeded (%s); the step was the largest when the host's gates together passed it", formatBytes(host))
}

// runBudgeted runs command, not yet started, as the named step under the
// step budget, and returns what it used. Every process the gate starts for
// a step runs through it.
func runBudgeted(limit limiter, name string, command *exec.Cmd) (usage, error) {
	output, err := captureCommandOutput(command)
	if err != nil {
		return usage{}, err
	}
	defer output.restore()

	watch, err := limit.prepare(name, command)
	if err != nil {
		output.closeWriters()
		_ = output.wait()
		return usage{}, err
	}
	if err = command.Start(); err == nil {
		watch.started(command.Process.Pid)
	}
	output.closeWriters()
	if err == nil {
		err = command.Wait()
	}
	used := watch.finish()
	if used.stopFailed {
		output.abort()
	} else {
		_, heldOpen, drainErr := output.finish()
		if heldOpen {
			addProcessIssue(&used, "step output held open by a process outside the step's process group or cgroup")
		}
		if err == nil && drainErr != nil {
			err = fmt.Errorf("drain step output: %w", drainErr)
		}
	}
	return used, err
}

// commandOutput replaces Go-managed stdout/stderr copy pipes with explicit
// pipes. Cmd.Wait then waits for the leader alone; runBudgeted stops the rest
// of the step before waiting for these readers to reach EOF.
type commandOutput struct {
	command        *exec.Cmd
	originalStdout io.Writer
	originalStderr io.Writer
	readers        []*os.File
	writers        []*os.File
	drained        []<-chan pipeResult
	stopReaders    chan struct{}
	stopOnce       sync.Once
	mu             sync.Mutex
	aborted        bool
}

type pipeResult struct {
	eof      bool
	heldOpen bool
	err      error
}

type synchronizedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w synchronizedWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(content)
}

func captureCommandOutput(command *exec.Cmd) (*commandOutput, error) {
	output := &commandOutput{
		command:        command,
		originalStdout: command.Stdout,
		originalStderr: command.Stderr,
		stopReaders:    make(chan struct{}),
	}
	shared := command.Stdout != nil && command.Stderr != nil && interfaceEqual(command.Stdout, command.Stderr)
	if command.Stdout != nil {
		writer, err := output.pipe(command.Stdout)
		if err != nil {
			output.closeWriters()
			_ = output.wait()
			return nil, err
		}
		command.Stdout = writer
	}
	if shared {
		command.Stderr = command.Stdout
	} else if command.Stderr != nil {
		writer, err := output.pipe(command.Stderr)
		if err != nil {
			output.closeWriters()
			_ = output.wait()
			output.restore()
			return nil, err
		}
		command.Stderr = writer
	}
	return output, nil
}

func (o *commandOutput) pipe(destination io.Writer) (*os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if err := makePipeReaderNonblocking(reader); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	o.readers = append(o.readers, reader)
	drained := make(chan pipeResult, 1)
	o.writers = append(o.writers, writer)
	o.drained = append(o.drained, drained)
	go drainPipe(reader, destination, &o.mu, o.stopReaders, drained)
	return writer, nil
}

func drainPipe(reader *os.File, destination io.Writer, mu *sync.Mutex, stop <-chan struct{}, drained chan<- pipeResult) {
	result := pipeResult{}
	defer func() { drained <- result }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	buffer := make([]byte, 32<<10)
	stopping := false
	for {
		read, err := readPipeNonblocking(reader, buffer)
		if read > 0 {
			mu.Lock()
			written, writeErr := destination.Write(buffer[:read])
			mu.Unlock()
			if writeErr != nil {
				result.err = writeErr
				return
			}
			if written != read {
				result.err = io.ErrShortWrite
				return
			}
		}
		if err == nil {
			if read == 0 {
				result.eof = true
				return
			}
			continue
		}
		if pipeReadInterrupted(err) {
			continue
		}
		if pipeReadWouldBlock(err) {
			if stopping {
				result.heldOpen = true
				return
			}
			select {
			case <-stop:
				stopping = true
			case <-ticker.C:
			}
			continue
		}
		result.err = err
		return
	}
}

func (o *commandOutput) closeWriters() {
	for _, writer := range o.writers {
		_ = writer.Close()
	}
}

func (o *commandOutput) wait() error {
	var first error
	for _, drained := range o.drained {
		if result := <-drained; first == nil && result.err != nil && !o.aborted {
			first = result.err
		}
	}
	o.closeReaders()
	return first
}

func (o *commandOutput) finish() (eof, heldOpen bool, err error) {
	o.stopOnce.Do(func() { close(o.stopReaders) })
	eof = true
	var first error
	for _, drained := range o.drained {
		result := <-drained
		eof = eof && result.eof
		heldOpen = heldOpen || result.heldOpen
		if first == nil && result.err != nil && !o.aborted {
			first = result.err
		}
	}
	o.closeReaders()
	return eof, heldOpen, first
}

func (o *commandOutput) closeReaders() {
	for _, reader := range o.readers {
		_ = reader.Close()
	}
}

func (o *commandOutput) abort() {
	o.aborted = true
	o.stopOnce.Do(func() { close(o.stopReaders) })
	o.closeReaders()
}

func (o *commandOutput) restore() {
	o.command.Stdout = o.originalStdout
	o.command.Stderr = o.originalStderr
}

// interfaceEqual matches os/exec's comparison: equal comparable writers can
// share one pipe, while an uncomparable writer simply uses separate pipes.
func interfaceEqual(a, b any) (equal bool) {
	defer func() { _ = recover() }()
	return a == b
}

// recordDurations measures the slow packages (writeDurations) under the
// memory budget, as the gate runs its steps.
func (g *gate) recordDurations(out io.Writer) error {
	budget, err := currentBudget()
	if err != nil {
		return err
	}
	limit, err := g.openLimiter(budget)
	if err != nil {
		return err
	}
	defer limit.close()
	fmt.Fprintf(out, "gate: memory budget: %s\n", limit.describe())
	return writeDurations(g.root, limit)
}

// openLimiter chooses the mechanism: the systemd scope the gate runs in, or
// else the RSS watch, saying why.
func (g *gate) openLimiter(budget memoryBudget) (limiter, error) {
	switch mechanism := os.Getenv(mechanismVariable); mechanism {
	case "":
	case "rss":
		return openRSSWatch(budget, "forced by "+mechanismVariable+"=rss")
	default:
		return nil, fmt.Errorf("%s=%q: the only value is rss", mechanismVariable, mechanism)
	}
	reason := g.scopeNote
	if g.scope != "" {
		scope, err := openScope(g.scope, budget)
		if err == nil {
			return scope, nil
		}
		reason = "systemd scope unusable: " + err.Error()
	}
	if reason == "" {
		reason = "not started in a systemd scope"
	}
	return openRSSWatch(budget, reason)
}

// describeBudget is the budget part of the gate's mechanism line.
func describeBudget(budget memoryBudget) string {
	return fmt.Sprintf("%s per step, %s per gate", formatBytes(budget.step), formatBytes(budget.gate))
}

// firstLine is the first line of a command's output, for a reason.
func firstLine(output []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return line
}
