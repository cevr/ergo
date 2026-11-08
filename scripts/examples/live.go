package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// live is what one run owns and must undo on every exit path it controls:
// the process groups it started and has not yet stopped for the last time,
// and the directories it made and has not yet removed. mu serializes every
// start, stop and removal with the signal handler (interrupt), so the handler
// cannot miss a group that started, and nothing starts or is made after the
// handler began.
type live struct {
	mu     sync.Mutex
	closed bool // a signal stops the run: nothing more starts
	groups map[*os.Process]bool
	dirs   map[string]bool
}

func newLive() *live {
	return &live{groups: map[*os.Process]bool{}, dirs: map[string]bool{}}
}

// errStopping is why a command does not start once a signal stops the run.
var errStopping = errors.New("the runner is stopping")

// start starts command, the leader of its own process group, and registers
// the group in the same critical section.
func (l *live) start(command *exec.Cmd, memory uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errStopping
	}
	// Every command, a compiler leg too, is the first process the kernel's
	// out-of-memory killer stops; memory (0: none) bounds a built program.
	if err := proclimit.Start(command, memory); err != nil {
		return err
	}
	l.groups[command.Process] = true
	return nil
}

// stop stops the group of process while it is registered: the timeout's and
// the cap's stop. After release it does nothing, so it never signals a group
// the run no longer owns.
func (l *live) stop(process *os.Process) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.groups[process] {
		stopGroup(process)
	}
}

// release stops the group of process a last time and forgets it. A second
// release does nothing.
func (l *live) release(process *os.Process) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.groups[process] {
		stopGroup(process)
		delete(l.groups, process)
	}
}

// makeDir makes a fresh directory as os.MkdirTemp does and registers it. It
// returns the directory with its links resolved: HOME and the working
// directory of a case name the same place (on darwin TMPDIR goes through a
// link).
func (l *live) makeDir(parent, pattern string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return "", errStopping
	}
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		removeAll(dir)
		return "", err
	}
	l.dirs[resolved] = true
	return resolved, nil
}

// removeDir removes a directory makeDir made and forgets it.
func (l *live) removeDir(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.dirs, dir)
	removeAll(dir)
}

// interrupt is the first signal's work: it closes the run for good, so no
// command starts and no directory is made after it, stops every live group
// and removes every directory of the run.
func (l *live) interrupt() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for process := range l.groups {
		stopGroup(process)
	}
	for dir := range l.dirs {
		// A process the stop just killed can finish one last call, such as
		// making a file, after the first pass; so try a few times.
		for range 20 {
			removeAll(dir)
			if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		delete(l.dirs, dir)
	}
}

// handleSignals handles stopSignals until the returned function is called.
// On the first one it stops the run (interrupt), prints "examples: stopped by
// <signal>" to stderr and exits 128 plus the signal number, as `ergo run`
// does.
func (l *live) handleSignals(stderr io.Writer) (done func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, stopSignals...)
	quit := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		select {
		case sig := <-signals:
			l.interrupt()
			name, code := signalExit(sig)
			fmt.Fprintf(stderr, "examples: stopped by %s\n", name)
			os.Exit(code)
		case <-quit:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(quit)
		<-ended
	}
}
