package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
)

// Stopping. A gate told to stop (SIGINT, SIGTERM, or SIGHUP unless it was
// started ignoring it, as nohup does) stops every process it started before
// it exits, so none outlives it: no step goes on running reparented to
// init, holding the descriptors the gate inherited from its caller (the
// flock of gate.lock, measured 2026-11-05 and 2026-11-06). stopOnSignals,
// which main calls first, installs the handler. On the first signal it
//
//  1. refuses every step not yet started (runBudgeted);
//  2. kills every running step whole: its cgroup (cgroup.kill) in the
//     gate's systemd scope, its process group under the RSS watch, the
//     sandboxed processes of the merge tier included, since nsenter starts
//     them in the step's cgroup and process group;
//  3. waits until each of those steps has finished (the limiter saw its
//     cgroup or group empty);
//  4. runs the cleanups registered with atStop, the last first: the merge
//     tier's sandbox closes (its holder, bwrap and its run directory), and
//     in a scope the gate kills every process left in its own leaf cgroup
//     (a tool query, a go command's telemetry sidecar);
//  5. says so and exits with 128 plus the signal's number, as a shell
//     reports a command a signal ended.
//
// A signal the gate was started ignoring stays ignored. Elsewhere the gate
// exits through exitGate, which waits for a stop in progress, so a run that
// ends while the gate stops cannot exit before every process has ended.

// errStopped refuses a step that would start while the gate stops.
var errStopped = errors.New("the gate is stopping")

// gateStop is the gate's record of what it must stop.
var gateStop stopper

// stopper tracks the running steps and the cleanups of a stop.
type stopper struct {
	mu sync.Mutex
	// signal is the signal the gate stops for, or 0.
	signal syscall.Signal
	next   int
	// kills stop each running step whole, by its tracking number.
	kills map[int]func()
	// running counts the steps between track and done.
	running sync.WaitGroup
	// cleanups run after the steps have stopped, the last first.
	cleanups map[int]func()
}

// trackedStep is one step's entry in the stopper.
type trackedStep struct {
	stopper *stopper
	id      int
}

// track records a step about to start, or refuses it while the gate stops.
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
// returns the function that withdraws it. The cleanup runs at most once,
// whether the stop or the caller runs it (once).
func (s *stopper) atStop(cleanup func()) (withdraw func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	if s.cleanups == nil {
		s.cleanups = map[int]func(){}
	}
	s.cleanups[id] = cleanup
	return func() {
		s.mu.Lock()
		delete(s.cleanups, id)
		s.mu.Unlock()
	}
}

// stop stops every running step, waits until each has finished, and runs
// the cleanups, the last registered first. It returns false when a stop
// had already begun.
func (s *stopper) stop(received syscall.Signal) bool {
	s.mu.Lock()
	if s.signal != 0 {
		s.mu.Unlock()
		return false
	}
	s.signal = received
	kills := make([]func(), 0, len(s.kills))
	for _, kill := range s.kills {
		kills = append(kills, kill)
	}
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
	ids := slices.Sorted(maps.Keys(cleanups))
	for _, id := range slices.Backward(ids) {
		cleanups[id]()
	}
	return true
}

// stopping reports whether a stop has begun.
func (s *stopper) stopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signal != 0
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
	go func() {
		received := (<-signals).(syscall.Signal)
		gateStop.stop(received)
		fmt.Fprintf(os.Stdout, "gate: stopped by %s; every process it started has ended\n", signalName(received))
		os.Exit(128 + int(received))
	}()
}

// exitGate exits with code, unless a stop has begun: then the stop exits
// once every process the gate started has ended.
func exitGate(code int) {
	if gateStop.stopping() {
		select {}
	}
	os.Exit(code)
}
