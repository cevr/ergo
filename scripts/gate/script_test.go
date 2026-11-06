package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gateScriptFixture stands in for the gate behind scripts/gate.sh: it says
// its pid, holds the descriptor it inherited (the caller's lock), and on
// SIGTERM says so and exits 143, as the gate's stop does.
const gateScriptFixture = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	fmt.Printf("pid %d\n", os.Getpid())
	<-stop
	fmt.Println("stopped by SIGTERM")
	os.Exit(143)
}
`

// scripts/gate.sh executes the gate in its own process: the process a
// caller starts and signals is the gate, so SIGTERM reaches the gate's stop
// code, and once the gate has exited nothing holds a descriptor the caller
// gave it, such as the lock of `flock gate.lock ./scripts/gate.sh`. With
// `go run`, the gate was a child of the go command, which does not pass
// SIGTERM on (fu-gate): the signal ended the go command and left the gate
// running with the lock. TestGateStopsItsStepsOnSignal proves the gate's
// own stop ends every step.
func TestGateScriptExecsTheGate(t *testing.T) {
	script, err := os.ReadFile("../gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/gatescript\n\ngo 1.24\n")
	write(t, root, "scripts/gate.sh", string(script))
	write(t, root, "scripts/gate/main.go", gateScriptFixture)
	lockPath := filepath.Join(t.TempDir(), "gate.lock")
	lock, err := os.Create(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("sh", filepath.Join(root, "scripts/gate.sh"))
	command.Env = append(os.Environ(), "ERGO_GATE_CACHE="+t.TempDir(), "GOTMPDIR="+t.TempDir())
	command.ExtraFiles = []*os.File{lock}
	command.Stderr = os.Stderr
	// The gate's output reaches a pipe this test reads line by line, so a
	// gate that outlives the script (holding the pipe) cannot hang it.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command.Stdout = writer
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	lock.Close() // only the script holds the lock now
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		for scanner := bufio.NewScanner(reader); scanner.Scan(); {
			lines <- scanner.Text()
		}
	}()
	gatePID := 0
	select {
	case line := <-lines:
		gatePID, _ = strconv.Atoi(strings.TrimPrefix(line, "pid "))
	case <-time.After(5 * time.Minute): // a hang guard: the build is seconds
	}
	if gatePID == 0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("the gate did not start")
	}
	t.Cleanup(func() {
		// A gate the script left behind (the mutant) is this test's child's
		// child: stop it by the pid it printed.
		if gatePID != command.Process.Pid {
			_ = syscall.Kill(gatePID, syscall.SIGKILL)
		}
	})
	if gatePID != command.Process.Pid {
		t.Errorf("the gate runs as pid %d, not as the script's process %d: a signal to the script does not reach it", gatePID, command.Process.Pid)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err = <-exited:
	case <-time.After(5 * time.Minute): // a hang guard
		t.Fatal("the script did not exit after SIGTERM")
	}
	var rest strings.Builder
	if gatePID == command.Process.Pid {
		// The gate was the script's process: it has exited, so the pipe
		// ends.
		for line := range lines {
			rest.WriteString(line + "\n")
		}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 || !strings.Contains(rest.String(), "stopped by SIGTERM") {
		t.Errorf("after SIGTERM to the script: %v, output %q; want the gate's stop, exit 143", err, rest.String())
	}
	relock, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer relock.Close()
	if err := syscall.Flock(int(relock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("the lock is still held after the script exited (%v): the gate outlived it", err)
	}
}
