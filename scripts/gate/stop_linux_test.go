//go:build linux

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stopVariable names the tier the helper gate of TestGateStopsItsStepsOnSignal
// runs; the helper runs only when it is set.
const stopVariable = "GATE_STOP_TIER"

// stopStep is the step the stopped gates run. It passes where the
// checkout holds the ignored file runs/stale, as the host checkout does, so
// a merge tier's inner run passes. Elsewhere, as in the merge tier's
// sandbox, whose checkout is a fresh export of the head commit, it says
// ready on the descriptor it inherited from the gate's caller (3) and waits
// on a child that holds that descriptor, as go test waits on a test binary
// that inherited the caller's flock descriptor.
var stopStep = []string{"sh", "-c", `test -e runs/stale && exit 0; echo ready >&3; sleep 1000 & wait`}

// A gate told to stop (SIGTERM, SIGINT) stops every process it started and
// exits: no step process, sandboxed or not, outlives it, and so no process
// it started still holds a descriptor the gate inherited from its caller
// (the flock of gate.lock). Measured twice before (2026-11-05, 2026-11-06):
// SIGTERM to a --merge gate left its sandboxed go test processes running,
// reparented to init, holding the caller's lock. Each case starts a helper
// gate whose step holds the write end of a pipe the test made, its fd 3,
// sends the signal to the gate's process alone once the step says ready,
// and waits for the read end to reach EOF: only then has every holder
// ended. The merge cases run the step in the merge tier's sandbox (bwrap
// and nsenter); the scope cases run the gate in a systemd scope, as on
// Linux hosts with a user service manager.
func TestGateStopsItsStepsOnSignal(t *testing.T) {
	if os.Getenv(stopVariable) != "" {
		t.Skip("the helper runs as a subprocess of this test")
	}
	cases := []struct {
		name, tier string
		scope      bool
		signal     syscall.Signal
	}{
		{"merge tier in a scope, SIGTERM", "merge", true, syscall.SIGTERM},
		{"merge tier in a scope, SIGINT", "merge", true, syscall.SIGINT},
		{"merge tier under the RSS watch, SIGTERM", "merge", false, syscall.SIGTERM},
		{"inner tier in a scope, SIGTERM", "inner", true, syscall.SIGTERM},
		{"inner tier under the RSS watch, SIGTERM", "inner", false, syscall.SIGTERM},
		// A step's tool fingerprint asks a tool that leaves a child
		// running, which no step cgroup holds: the stop sweeps the gate's
		// leaf cgroup.
		{"tool query in a scope, SIGTERM", "tool", true, syscall.SIGTERM},
		// The tool itself hangs, with no descendant, under the RSS watch
		// (the darwin mechanism), where no leaf sweep runs: the stop kills
		// the query's process group (the verifier's case, round 1).
		{"hanging tool query under the RSS watch, SIGTERM", "hanging tool", false, syscall.SIGTERM},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.tier == "merge" {
				startsSandbox(t)
			}
			requireSandbox(t)
			setBudgetCaseTemp(t)
			t.Setenv(stepMemoryVariable, "512M")
			t.Setenv(gateMemoryVariable, "1G")
			t.Setenv(hostMemoryVariable, "4G")
			t.Setenv(cpuVariable, "1")
			bin := t.TempDir()
			tool := "#!/bin/sh\nsleep 1000 &\necho ready >&3\nwait\n"
			if c.tier == "hanging tool" {
				tool = "#!/bin/sh\necho ready >&3\nexec sleep 1000\n"
			}
			executable(t, bin, "probe-tool", tool)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			root, cache := fixture(t), t.TempDir()
			write(t, root, "runs/stale", "only in the host checkout\n")
			argv := []string{os.Args[0], "-test.run=^TestGateStopHelper$", "-test.count=1"}
			environment := append(os.Environ(), stopVariable+"="+c.tier, "GATE_STOP_ROOT="+root, "GATE_STOP_CACHE="+cache)
			var helper *exec.Cmd
			if c.scope {
				slice := testSlice(t)
				budget, err := currentBudget()
				if err != nil {
					t.Fatal(err)
				}
				startableScope(t, slice, budget)
				unit := scopeUnit()
				helper = exec.Command("systemd-run", scopeArguments(slice, unit, budget, 1, argv)...)
				helper.Env = append(scopeGateEnvironment(unit, 1), stopVariable+"="+c.tier, "GATE_STOP_ROOT="+root, "GATE_STOP_CACHE="+cache)
				t.Cleanup(func() {
					// Whatever the gate left in its scope ends with the test.
					command := exec.Command("systemctl", "--user", "stop", unit+".scope")
					command.Env = scopeEnvironment("")
					command.Run()
				})
			} else {
				helper = exec.Command(argv[0], argv[1:]...)
				helper.Env = append(environment, mechanismVariable+"=rss")
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			helper.ExtraFiles = []*os.File{writer}
			var output bytes.Buffer
			helper.Stdout, helper.Stderr = &output, &output
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			exited := make(chan error, 1)
			go func() { exited <- helper.Wait() }()
			lines := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(reader).ReadString('\n')
				lines <- line
			}()
			deadline := stopDeadline(t)
			select {
			case line := <-lines:
				if line != "ready\n" {
					<-exited
					t.Fatalf("the step never said ready (%q):\n%s", line, output.String())
				}
			case err := <-exited:
				t.Fatalf("the gate ended (%v) before its step said ready:\n%s", err, output.String())
			case <-deadline:
				helper.Process.Kill()
				<-exited
				t.Fatalf("the step did not say ready by the test's deadline:\n%s", output.String())
			}
			inode := pipeInode(t, reader)
			if err := helper.Process.Signal(c.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				t.Logf("the gate ended: %v", err)
			case <-deadline:
				helper.Process.Kill()
				<-exited
				t.Fatalf("the gate did not end after %v by the test's deadline:\n%s", c.signal, output.String())
			}
			// The gate exits only once every process it started has ended,
			// so when it has exited none holds the descriptor, and the read
			// end is at EOF.
			if holders := pipeHolders(inode); len(holders) > 0 {
				for _, holder := range holders {
					syscall.Kill(holder.pid, syscall.SIGKILL)
				}
				t.Fatalf("after %v the gate exited, and processes it started still hold the descriptor it inherited: %v\n%s", c.signal, holders, output.String())
			}
			eof := make(chan error, 1)
			go func() {
				_, err := io.Copy(io.Discard, reader)
				eof <- err
			}()
			select {
			case err := <-eof:
				if err != nil {
					t.Fatal(err)
				}
			case <-deadline:
				t.Fatalf("no holder is left, and the inherited pipe has no EOF by the test's deadline")
			}
			text := output.String()
			if runs, _ := os.ReadDir(filepath.Join(cache, "sandbox")); len(runs) > 0 {
				t.Errorf("the merge sandbox's run directory outlived the gate: %v", runs)
			}
			if c.tier == "merge" && !strings.Contains(text, "merge: every step uncached") {
				t.Errorf("the signal did not stop the gate in its uncached, sandboxed run:\n%s", text)
			}
			// The gate claims only what its stop proved: in a scope, the
			// sweep read its leaf cgroup back empty; under the RSS watch,
			// the stop saw every step and command end with its group.
			want := "gate: stopped by " + signalName(c.signal) + "; every process it started has ended\n"
			if !c.scope {
				want = "gate: stopped by " + signalName(c.signal) + "; every step and command it started has ended, with its process group\n"
			}
			if !strings.Contains(text, want) {
				t.Errorf("the gate does not say %q:\n%s", want, text)
			}
			if state := helper.ProcessState; state == nil || state.ExitCode() != 128+int(c.signal) {
				t.Errorf("the gate exited %v; want status %d, 128 plus the signal", state, 128+int(c.signal))
			}
		})
	}
}

// A --merge gate that gets SIGTERM in its sandboxed phase, as a terminal or
// timeout(1) sends it to the gate's whole process group, stops and leaves
// no holder of the lock its caller passed it (fu-gate U3). The test locks a
// private lock file, as flock(1) does gate.lock, and passes it as the
// gate's fd 3; the step, sandboxed, inherits it and waits on a child. The
// test starts the helper gate in a process group of its own, signals that
// group once the gate prints "merge: every step uncached", and after the
// gate exits counts the processes with the lock file open in /proc/*/fd
// (expect none), takes the lock itself, and checks that the stop closed the
// sandbox (its run directory is gone). It signals only the group it
// started; a holder it finds is reported, not signalled (the service unit
// the test runs in ends it).
func TestMergeGateStopsOnGroupSIGTERM(t *testing.T) {
	if os.Getenv(stopVariable) != "" {
		t.Skip("the helper runs as a subprocess of this test")
	}
	startsSandbox(t)
	requireSandbox(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "1G")
	t.Setenv(hostMemoryVariable, "4G")
	t.Setenv(cpuVariable, "1")
	root, cache := fixture(t), t.TempDir()
	write(t, root, "runs/stale", "only in the host checkout\n")
	lockPath := filepath.Join(t.TempDir(), "private-gate.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestGateStopHelper$", "-test.count=1")
	helper.Env = append(os.Environ(), stopVariable+"=merge", "GATE_STOP_ROOT="+root, "GATE_STOP_CACHE="+cache, mechanismVariable+"=rss")
	helper.ExtraFiles = []*os.File{lock}
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	helper.Stderr = &output
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	// The gate holds the lock now; the test's own descriptor goes.
	lock.Close()
	group := helper.Process.Pid
	uncached := make(chan struct{})
	copied := make(chan struct{})
	var text strings.Builder
	go func() {
		defer close(copied)
		scanner := bufio.NewScanner(stdout)
		said := false
		for scanner.Scan() {
			text.WriteString(scanner.Text() + "\n")
			if !said && scanner.Text() == "merge: every step uncached" {
				said = true
				close(uncached)
			}
		}
	}()
	deadline := stopDeadline(t)
	select {
	case <-uncached:
	case <-copied:
		_ = helper.Wait()
		t.Fatalf("the gate ended before its sandboxed phase:\n%s%s", text.String(), output.String())
	case <-deadline:
		_ = syscall.Kill(-group, syscall.SIGKILL)
		<-copied
		_ = helper.Wait()
		t.Fatalf("the gate did not reach its sandboxed phase by the test's deadline:\n%s%s", text.String(), output.String())
	}
	if err := syscall.Kill(-group, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() {
		<-copied
		exited <- helper.Wait()
	}()
	select {
	case err := <-exited:
		t.Logf("the gate ended: %v", err)
	case <-deadline:
		_ = syscall.Kill(-group, syscall.SIGKILL)
		t.Fatalf("the gate did not end after SIGTERM by the test's deadline:\n%s%s", text.String(), output.String())
	}
	if holders := pipeHolders(lockPath); len(holders) > 0 {
		t.Fatalf("after SIGTERM the gate exited, and processes it started still hold its lock: %v\n%s%s", holders, text.String(), output.String())
	}
	probe, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the lock is still held after the gate exited: %v", err)
	}
	// The stop closed the sandbox: its run directory is gone, which only
	// the sandbox's close removes once every process in it has ended.
	if runs, _ := os.ReadDir(filepath.Join(cache, "sandbox")); len(runs) > 0 {
		t.Errorf("the merge sandbox's run directory outlived the gate: %v", runs)
	}
	want := "gate: stopped by SIGTERM; every step and command it started has ended, with its process group\n"
	if !strings.Contains(text.String()+output.String(), want) {
		t.Errorf("the gate does not say %q:\n%s%s", want, text.String(), output.String())
	}
	if state := helper.ProcessState; state == nil || state.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Errorf("the gate exited %v; want status %d", state, 128+int(syscall.SIGTERM))
	}
}

// A command a step's watch runs inside the step (runInStep: the ps that
// sees the step's group end) runs while the gate stops, where runCommand
// refuses, is not killed by the stop, and holds the stop's wait until it
// has ended. The test ends the command through its stdin; it signals
// nothing.
func TestStopWaitsForACommandInAStep(t *testing.T) {
	var s stopper
	step, err := s.track()
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan stopResult, 1)
	go func() {
		result, _ := s.stop(syscall.SIGTERM)
		stopped <- result
	}()
	for !s.stopping() {
		time.Sleep(time.Millisecond)
	}
	if err := s.runCommand(exec.Command("true")); !errors.Is(err, errStopped) {
		t.Fatalf("runCommand during a stop: %v, want errStopped", err)
	}
	releaseReader, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseReader.Close()
	readyReader, ready, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	command := exec.Command("sh", "-c", "echo ready; read -r _; exit 0")
	command.Stdin, command.Stdout = releaseReader, ready
	ran := make(chan error, 1)
	go func() { ran <- s.runInStep(command) }()
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(readyReader).ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		if line != "ready\n" {
			_ = release.Close()
			t.Fatalf("the command in the step did not start during the stop: %q, %v", line, <-ran)
		}
	case err := <-ran:
		t.Fatalf("the command in the step ended (%v) before it said ready", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the command in the step did not say ready")
	}
	ready.Close()
	// The step's finish returns; only the command holds the stop now.
	step.done()
	select {
	case <-stopped:
		t.Fatal("the stop ended while a command in a step ran")
	case <-time.After(200 * time.Millisecond):
	}
	_ = release.Close()
	if err := <-ran; err != nil {
		t.Errorf("the command in the step: %v, want it to run to its end", err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop did not end after the command in the step ended")
	}
}

// stopDeadline fires a little before the test's own deadline, so a failure
// is reported, and the helper stopped, before go test kills the binary.
func stopDeadline(t *testing.T) <-chan time.Time {
	deadline, ok := t.Deadline()
	if !ok {
		return nil
	}
	return time.After(time.Until(deadline) * 9 / 10)
}

// pipeInode is the inode of the pipe reader reads.
func pipeInode(t *testing.T, reader *os.File) string {
	t.Helper()
	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// pipeHolder is a process with a descriptor on the test's pipe.
type pipeHolder struct {
	pid     int
	command string
}

func (h pipeHolder) String() string { return fmt.Sprintf("%s (pid %d)", h.command, h.pid) }

// pipeHolders are the processes other than this one with a descriptor on
// the pipe named inode.
func pipeHolders(inode string) []pipeHolder {
	entries, _ := os.ReadDir("/proc")
	var holders []pipeHolder
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		descriptors, _ := filepath.Glob(filepath.Join("/proc", entry.Name(), "fd", "*"))
		for _, descriptor := range descriptors {
			if target, err := os.Readlink(descriptor); err == nil && target == inode {
				command, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
				holders = append(holders, pipeHolder{pid, strings.ReplaceAll(strings.TrimSpace(string(command)), "\x00", " ")})
				break
			}
		}
	}
	return holders
}

// TestGateStopHelper is the gate TestGateStopsItsStepsOnSignal stops: as
// main does, it takes its scope and stops on a signal, then runs stopStep
// in the tier the test names, in the real sandbox for the merge tier.
func TestGateStopHelper(t *testing.T) {
	tier := os.Getenv(stopVariable)
	if tier == "" {
		t.Skip("run by TestGateStopsItsStepsOnSignal")
	}
	requireSandbox(t)
	scope, err := takeScope()
	if err != nil {
		t.Fatal(err)
	}
	stopOnSignals()
	g := wholeFixture(t, os.Getenv("GATE_STOP_ROOT"), []*Step{{Name: "holder", Argv: stopStep}})
	g.scope, g.cacheDir = scope, os.Getenv("GATE_STOP_CACHE")
	var code int
	switch tier {
	case "merge":
		code = g.mergeTier(os.Stdout)
	case "inner", "tool", "hanging tool":
		if err := os.Remove(filepath.Join(g.root, "runs", "stale")); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if tier != "inner" {
			g.steps = []*Step{{Name: "fingerprinted", Argv: []string{"true"}, ToolEnv: []string{"probe-tool"}}}
		}
		code = g.run(os.Stdout)
	}
	fmt.Printf("gate-exit=%d\n", code)
	exitGate(code)
}
