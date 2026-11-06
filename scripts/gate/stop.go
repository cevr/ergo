package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Stopping. A gate told to stop (SIGINT, SIGTERM, or SIGHUP unless it was
// started ignoring it, as nohup does) stops every process it started before
// it exits, so none outlives it: no step or command goes on running
// reparented to init, holding the descriptors the gate inherited from its
// caller (the flock of gate.lock, measured 2026-11-05 and 2026-11-06).
// stopOnSignals, which main calls first, installs the handler. Every
// process the gate starts is tracked from before it starts until it has
// ended: a step through runBudgeted, any other command (git, a tool query,
// go list, systemctl) through runCommand. On the first signal the stopper
//
//  1. refuses every step and command not yet started;
//  2. kills every running one whole: a step's cgroup (cgroup.kill) in the
//     gate's systemd scope, its process group under the RSS watch, the
//     sandboxed processes of the merge tier included, since nsenter starts
//     them in the step's cgroup and process group; a command's process
//     group;
//  3. waits until each of them has ended (the limiter saw a step's cgroup
//     or group empty; a command's Wait returned);
//  4. runs the cleanups registered with atStop, the last first: the merge
//     tier's sandbox closes (its holder, bwrap and its run directory), and
//     in a scope the gate kills every process left in its own leaf cgroup
//     (a descendant that left its group, a go command's telemetry
//     sidecar) and reads the leaf back empty;
//  5. says what it proved and exits with 128 plus the signal's number, as
//     a shell reports a command a signal ended.
//
// A second stop signal during a stop exits at once, as a second Ctrl-C does
// in Bazel, and says that processes may remain: the way out of a stop that
// cannot finish. A signal the gate was started ignoring stays ignored.
// Elsewhere the gate exits through exitGate, which waits for a stop in
// progress, so a run that ends while the gate stops cannot exit before
// every process has ended.

// errStopped refuses a step or command that would start while the gate
// stops.
var errStopped = errors.New("the gate is stopping")

// gateStop is the gate's record of what it must stop.
var gateStop stopper

// stopper tracks the running steps and commands and the cleanups of a
// stop.
type stopper struct {
	mu sync.Mutex
	// signal is the signal the gate stops for, or 0.
	signal syscall.Signal
	next   int
	// kills stop each running step or command whole, by its tracking
	// number.
	kills map[int]func()
	// running counts the steps and commands between track and done.
	running sync.WaitGroup
	// cleanups run after the steps have stopped, the last first.
	cleanups map[int]stopCleanup
}

// stopCleanup is one cleanup of a stop. A sweep's success proves that no
// process the gate started is left (sweepLeaf reads its leaf back empty).
type stopCleanup struct {
	run   func() error
	sweep bool
}

// stopResult is what a stop proved.
type stopResult struct {
	// swept is set when a sweep read back that no process the gate started
	// is left, descendants that left their process group included.
	swept bool
	// err joins the cleanups' failures.
	err error
}

// trackedStep is one step's or command's entry in the stopper.
type trackedStep struct {
	stopper *stopper
	id      int
}

// track records a step or command about to start, or refuses it while the
// gate stops.
func (s *stopper) track() (*trackedStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signal != 0 {
		return nil, errStopped
	}
	s.next++
	s.running.Add(1)
	return &trackedStep{stopper: s, id: s.next}, nil
}

// arm gives the started step's kill to the stopper; a stop already in
// progress kills it at once, as a stop does every armed step.
func (t *trackedStep) arm(kill func()) {
	s := t.stopper
	s.mu.Lock()
	if s.signal != 0 {
		s.mu.Unlock()
		kill()
		return
	}
	if s.kills == nil {
		s.kills = map[int]func(){}
	}
	s.kills[t.id] = kill
	s.mu.Unlock()
}

// done says the step has finished: its processes have ended.
func (t *trackedStep) done() {
	s := t.stopper
	s.mu.Lock()
	delete(s.kills, t.id)
	s.mu.Unlock()
	s.running.Done()
}

// atStop registers a cleanup a stop runs after the steps have stopped, and
// returns the function that withdraws it.
func (s *stopper) atStop(cleanup func() error) (withdraw func()) {
	return s.register(stopCleanup{run: cleanup})
}

// atStopSweep registers a sweep: a cleanup whose success proves that no
// process the gate started is left.
func (s *stopper) atStopSweep(sweep func() error) (withdraw func()) {
	return s.register(stopCleanup{run: sweep, sweep: true})
}

func (s *stopper) register(cleanup stopCleanup) (withdraw func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	if s.cleanups == nil {
		s.cleanups = map[int]stopCleanup{}
	}
	s.cleanups[id] = cleanup
	return func() {
		s.mu.Lock()
		delete(s.cleanups, id)
		s.mu.Unlock()
	}
}

// stop kills every running step and command, waits until each has ended,
// and runs the cleanups, the last registered first. It reports false when
// a stop had already begun.
func (s *stopper) stop(received syscall.Signal) (stopResult, bool) {
	s.mu.Lock()
	if s.signal != 0 {
		s.mu.Unlock()
		return stopResult{}, false
	}
	s.signal = received
	kills := slices.Collect(maps.Values(s.kills))
	s.mu.Unlock()
	for _, kill := range kills {
		kill()
	}
	s.running.Wait()
	s.mu.Lock()
	cleanups := s.cleanups
	s.cleanups = nil
	s.mu.Unlock()
	// The last registered first: the sandbox, opened after the limiter,
	// closes before the scope's leaf is swept.
	var result stopResult
	var errs []error
	for _, id := range slices.Backward(slices.Sorted(maps.Keys(cleanups))) {
		cleanup := cleanups[id]
		err := cleanup.run()
		errs = append(errs, err)
		result.swept = result.swept || cleanup.sweep && err == nil
	}
	result.err = errors.Join(errs...)
	return result, true
}

// stopping reports whether a stop has begun.
func (s *stopper) stopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signal != 0
}

// runCommand runs command, a process the gate starts outside a step, so a
// stop reaches it: it is refused once a stop has begun, it runs in a
// process group of its own, which a stop kills, and a stop waits until it
// has ended. A descendant that leaves the group is out of the stop's reach
// under the RSS watch; in the scope the leaf sweep finds it.
func (s *stopper) runCommand(command *exec.Cmd) error {
	tracked, err := s.track()
	if err != nil {
		return fmt.Errorf("%s: %w", command.Path, err)
	}
	defer tracked.done()
	ownProcessGroup(command)
	// A descendant that left the group and holds the output pipes cannot
	// hold the stop's wait: Wait gives up on the pipes a second after the
	// command ended.
	if command.WaitDelay == 0 {
		command.WaitDelay = time.Second
	}
	if err := command.Start(); err != nil {
		return err
	}
	pid := command.Process.Pid
	tracked.arm(func() { killProcessGroup(pid) })
	return command.Wait()
}

// runOutput is command's standard output, as exec.Cmd.Output, run by
// runCommand.
func runOutput(command *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	if command.Stderr == nil {
		command.Stderr = &stderr
	}
	err := gateStop.runCommand(command)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// runCombinedOutput is command's standard output and error, as
// exec.Cmd.CombinedOutput, run by runCommand.
func runCombinedOutput(command *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := gateStop.runCommand(command)
	return output.Bytes(), err
}

// once runs f at most once, however often the function it returns is
// called.
func once(f func() error) func() error {
	var run sync.Once
	var err error
	return func() error {
		run.Do(func() { err = f() })
		return err
	}
}

// stopSignals are the signals that stop the gate.
var stopSignals = []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// signalName names a stop signal as kill(1) does.
func signalName(received syscall.Signal) string {
	switch received {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return received.String()
}

// stopOnSignals makes the stop signals the gate was not started ignoring
// stop it (gateStop.stop), then exit with 128 plus the signal's number.
func stopOnSignals() {
	var caught []os.Signal
	for _, candidate := range stopSignals {
		if !signal.Ignored(candidate) {
			caught = append(caught, candidate)
		}
	}
	if len(caught) == 0 {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, caught...)
	go gateStop.serve(signals, os.Stdout, os.Exit)
}

// serve stops the gate on the first signal and exits with 128 plus its
// number, saying what the stop proved. A second signal during the stop
// exits at once.
func (s *stopper) serve(signals <-chan os.Signal, out io.Writer, exit func(int)) {
	received := (<-signals).(syscall.Signal)
	stopped := make(chan stopResult, 1)
	go func() {
		result, _ := s.stop(received)
		stopped <- result
	}()
	select {
	case result := <-stopped:
		fmt.Fprintln(out, stopLine(received, result))
		exit(128 + int(received))
	case again := <-signals:
		fmt.Fprintf(out, "gate: stopped by %s, then %s before the stop ended; processes it started may remain\n", signalName(received), signalName(again.(syscall.Signal)))
		exit(128 + int(again.(syscall.Signal)))
	}
}

// stopLine is the gate's last line after a stop: it claims only what the
// stop proved.
func stopLine(received syscall.Signal, result stopResult) string {
	prefix := "gate: stopped by " + signalName(received) + "; "
	switch {
	case result.err != nil:
		return prefix + "not every process it started has ended: " + result.err.Error()
	case result.swept:
		return prefix + "every process it started has ended"
	default:
		return prefix + "every step and command it started has ended, with its process group"
	}
}

// exitGate exits with code, unless a stop has begun: then the stop exits
// once every process the gate started has ended.
func exitGate(code int) {
	if gateStop.stopping() {
		select {}
	}
	os.Exit(code)
}
