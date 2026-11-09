//go:build unix

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// stopEvents records the order of a stop's events.
type stopEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *stopEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *stopEvents) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

// A stop kills every running step, refuses every new one, waits until each
// killed step has ended, and only then runs the cleanups, the last
// registered first: the sandbox closes before the leaf sweep, and neither
// runs while a step may still hold a process. The kill here does not end
// the step, as a cgroup.kill does not end it at once: the step ends when
// the test says so.
func TestStopperOrdersAStop(t *testing.T) {
	var s stopper
	var events stopEvents
	s.atStopSweep(func() error { events.add("leaf sweep"); return nil })
	s.atStop(func() error { events.add("sandbox close"); return nil })
	step, err := s.track()
	if err != nil {
		t.Fatal(err)
	}
	killed := make(chan struct{})
	step.arm(func() { events.add("kill"); close(killed) })
	type stopped struct {
		result stopResult
		first  bool
	}
	returned := make(chan stopped, 1)
	go func() {
		result, first := s.stop(syscall.SIGTERM)
		returned <- stopped{result, first}
	}()
	<-killed
	if _, err := s.track(); !errors.Is(err, errStopped) {
		t.Errorf("a step tracked after the signal got %v; want errStopped", err)
	}
	select {
	case <-returned:
		t.Fatalf("the stop returned before its killed step ended: %v", events.list())
	case <-time.After(200 * time.Millisecond):
	}
	if got := events.list(); len(got) != 1 {
		t.Fatalf("before the killed step ended, the stop ran %v; want only its kill", got)
	}
	events.add("step ended")
	step.done()
	result := <-returned
	if !result.first {
		t.Error("the first stop reports a stop already begun")
	}
	want := []string{"kill", "step ended", "sandbox close", "leaf sweep"}
	if got := events.list(); strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the stop's order is %v; want %v", got, want)
	}
	if !result.result.swept || result.result.err != nil {
		t.Errorf("the stop proved %+v; want a clean sweep", result.result)
	}
	if _, again := s.stop(syscall.SIGINT); again {
		t.Error("a second stop ran again")
	}
}

// A run that a stop's kill made fail returns and withdraws its cleanups
// (the scope's leaf sweep) while the stop waits; the stop still runs them,
// so it still sweeps the leaf and proves it empty (round 2: measured once
// in the whole-repo run, where a killed git made the run return and the
// stop skipped the sweep).
func TestStopperRunsACleanupWithdrawnDuringTheStop(t *testing.T) {
	var s stopper
	swept := false
	withdraw := s.atStopSweep(func() error { swept = true; return nil })
	step, err := s.track()
	if err != nil {
		t.Fatal(err)
	}
	killed := make(chan struct{})
	step.arm(func() { close(killed) })
	returned := make(chan stopResult, 1)
	go func() {
		result, _ := s.stop(syscall.SIGTERM)
		returned <- result
	}()
	<-killed
	withdraw()
	step.done()
	if result := <-returned; !result.swept || !swept {
		t.Errorf("a sweep withdrawn during the stop did not run (ran %v, result %+v)", swept, result)
	}
}

// A cleanup withdrawn before any stop does not run.
func TestStopperSkipsACleanupWithdrawnBeforeTheStop(t *testing.T) {
	var s stopper
	ran := false
	s.atStop(func() error { ran = true; return nil })()
	s.stop(syscall.SIGTERM)
	if ran {
		t.Error("a withdrawn cleanup ran")
	}
}

// A step tracked before the signal and started after it (between track
// and arm) is killed the moment it is armed, and the stop waits for it.
func TestStopperKillsAStepArmedDuringTheStop(t *testing.T) {
	var s stopper
	step, err := s.track()
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() {
		s.stop(syscall.SIGTERM)
		close(returned)
	}()
	testwait.Until(t, s.stopping)
	killed := false
	step.arm(func() { killed = true })
	if !killed {
		t.Fatal("a step armed during a stop was not killed at once")
	}
	select {
	case <-returned:
		t.Fatal("the stop returned before the step armed during it ended")
	case <-time.After(100 * time.Millisecond):
	}
	step.done()
	<-returned
}

// A command (git, a tool query) that would start once a stop has begun is
// refused and never starts, so nothing the gate starts can race the leaf
// sweep (round 1's Unproven U1).
func TestStopperRefusesACommandDuringAStop(t *testing.T) {
	var s stopper
	s.stop(syscall.SIGTERM)
	started := filepath.Join(t.TempDir(), "started")
	err := s.runCommand(exec.Command("sh", "-c", `: > "$1"`, "sh", started))
	if !errors.Is(err, errStopped) {
		t.Errorf("a command run during a stop got %v; want errStopped", err)
	}
	if _, err := os.Stat(started); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused command started: %v", err)
	}
}

// The gate re-executes itself in its scope only while no stop has begun
// (fu-gate round 2's Unproven U4): a stop that began while it probed or
// replayed refuses the exec, and a stop that a signal starts while the
// exec is under way waits for the stopper's lock, so it cannot begin
// between the check and the exec.
func TestStopperRefusesTheScopeExecDuringAStop(t *testing.T) {
	var s stopper
	execs := 0
	if err := s.execUnlessStopping(func() error { execs++; return errors.New("exec failed") }); err == nil || errors.Is(err, errStopped) || execs != 1 {
		t.Fatalf("before a stop: err %v, %d execs; want the exec's own error after one exec", err, execs)
	}
	s.stop(syscall.SIGTERM)
	if err := s.execUnlessStopping(func() error { execs++; return nil }); !errors.Is(err, errStopped) || execs != 1 {
		t.Errorf("during a stop: err %v, %d execs; want errStopped and no exec", err, execs)
	}

	// A stop that begins during the exec waits until the exec returns
	// (here it fails, as a real one returns only on failure).
	var during stopper
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- during.execUnlessStopping(func() error {
			close(entered)
			<-release
			return errors.New("exec failed")
		})
	}()
	<-entered
	stopped := make(chan struct{})
	go func() {
		during.stop(syscall.SIGTERM)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("a stop began while the exec was under way")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-result; err == nil || errors.Is(err, errStopped) {
		t.Errorf("the exec returned %v; want its own error", err)
	}
	<-stopped
}

// A stop kills a running command's whole process group, a descendant
// included, and waits for it before the cleanups run: every holder of the
// command's output has ended by then (round 1's M1, under any watch).
func TestStopperKillsARunningCommandWhole(t *testing.T) {
	var s stopper
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.Command("sh", "-c", "sleep 1000 & echo ready; wait")
	command.Stdout = writer
	ran := make(chan error, 1)
	go func() { ran <- s.runCommand(command) }()
	line := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(reader, line); err != nil {
		t.Fatal(err)
	}
	// The command is armed once Start has returned; only then may the
	// test close its copy of the write end.
	testwait.Until(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.kills) == 1
	})
	writer.Close()
	eof := make(chan error, 1)
	s.atStop(func() error {
		if end, ok := testwait.End(t); !ok {
		} else if err := reader.SetReadDeadline(end); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, reader)
		eof <- err
		return err
	})
	if result, _ := s.stop(syscall.SIGTERM); result.err != nil {
		t.Errorf("when the cleanup ran, a process of the command still held its output: %v", result.err)
	}
	if err := <-ran; err == nil {
		t.Error("the stopped command reports success")
	}
	<-eof
}

// serve exits with 128 plus the signal's number and claims only what the
// stop proved.
func TestStopperServeSaysWhatTheStopProved(t *testing.T) {
	cases := []struct {
		name     string
		register func(*stopper)
		want     string
	}{
		{"no sweep", func(*stopper) {}, "gate: stopped by SIGTERM; every step and command it started has ended, with its process group\n"},
		{"a clean sweep", func(s *stopper) { s.atStopSweep(func() error { return nil }) }, "gate: stopped by SIGTERM; every process it started has ended\n"},
		{"a failed sweep", func(s *stopper) {
			s.atStopSweep(func() error { return errors.New("processes 7 stay in the gate's leaf cgroup") })
		}, "gate: stopped by SIGTERM; not every process it started has ended: processes 7 stay in the gate's leaf cgroup\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s stopper
			c.register(&s)
			signals := make(chan os.Signal, 1)
			codes := make(chan int, 1)
			var out bytes.Buffer
			go s.serve(signals, &out, func(code int) { codes <- code })
			signals <- syscall.SIGTERM
			if code := <-codes; code != 128+int(syscall.SIGTERM) {
				t.Errorf("serve exited %d; want %d", code, 128+int(syscall.SIGTERM))
			}
			if out.String() != c.want {
				t.Errorf("serve said %q; want %q", out.String(), c.want)
			}
		})
	}
}

// A second signal during a stop that cannot finish exits at once with 128
// plus its number, and says that processes may remain (round 1's Unproven
// U2).
func TestStopperServeExitsOnASecondSignal(t *testing.T) {
	var s stopper
	step, err := s.track()
	if err != nil {
		t.Fatal(err)
	}
	killed := make(chan struct{})
	step.arm(func() { close(killed) })
	defer step.done()
	signals := make(chan os.Signal, 1)
	codes := make(chan int, 1)
	var out bytes.Buffer
	go s.serve(signals, &out, func(code int) { codes <- code })
	signals <- syscall.SIGINT
	<-killed
	signals <- syscall.SIGTERM
	if code := <-codes; code != 128+int(syscall.SIGTERM) {
		t.Errorf("serve exited %d; want %d", code, 128+int(syscall.SIGTERM))
	}
	if want := "gate: stopped by SIGINT, then SIGTERM before the stop ended; processes it started may remain\n"; out.String() != want {
		t.Errorf("serve said %q; want %q", out.String(), want)
	}
}
