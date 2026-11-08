//go:build linux

package closure

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// The batch-10 gate failed TestRunCollectsADescendantThatLeftItsSession
// with "pidfd_open <pid>: invalid argument": processesWith listed /proc,
// the listed process ended, and a thread of another process took its id
// before the open. These tests give procSource a listing that races in each
// way and check that no step of it is an error or a wrong process.

// procMarker is the environment entry the injected sources match.
const procMarker = "ERGO_CLOSURE_PROCS_TEST=1"

// markedRead reads every id as a process that carries procMarker.
func markedRead(int, string) []byte { return []byte("A=1\x00" + procMarker + "\x00") }

// A listed id that is gone (ESRCH) or now names a thread that leads no
// process (EINVAL) is no process of the listing: with returns no error and
// no process for it.
func TestProcessesWithSkipsAGoneOrThreadID(t *testing.T) {
	maxData, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Fatal(err)
	}
	pidMax, err := strconv.Atoi(string(maxData[:len(maxData)-1]))
	if err != nil {
		t.Fatal(err)
	}
	// A thread of this process that is not its leader: Go's runtime runs
	// several threads, so /proc/self/task lists more than the leader.
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatal(err)
	}
	thread := 0
	for _, task := range tasks {
		if id, err := strconv.Atoi(task.Name()); err == nil && id != os.Getpid() {
			thread = id
			break
		}
	}
	if thread == 0 {
		t.Fatal("this process has no thread besides its leader")
	}
	for _, c := range []struct {
		name string
		pid  int
	}{
		{"an id no process can have (above pid_max)", pidMax + 1},
		{"a thread that leads no process", thread},
	} {
		source := procSource{
			list: func() ([]int, error) { return []int{c.pid}, nil },
			open: openProcess,
			read: markedRead,
		}
		found, err := source.with(procMarker)
		if err != nil || len(found) != 0 {
			t.Errorf("%s (%d): found %v, error %v; want no process and no error", c.name, c.pid, found, err)
		}
		for _, p := range found {
			syscall.Close(p.fd)
		}
	}
}

// A listed id whose process ends between the open and the read can name a
// successor by the read: identify does not take the read for the ended
// process, opens the id again, and returns the successor's pidfd. An id
// whose process ends at each open is not returned.
func TestProcessesWithReopensAnIDWhoseProcessEnded(t *testing.T) {
	// ended is a pidfd of a child that has ended and been reaped.
	done := exec.Command("true")
	if err := done.Start(); err != nil {
		t.Fatal(err)
	}
	ended, ok, err := openProcess(done.Process.Pid)
	if err != nil || !ok {
		t.Fatalf("pidfd of the ended child: %v", err)
	}
	defer syscall.Close(ended)
	if err := done.Wait(); err != nil {
		t.Fatal(err)
	}
	// running is a pidfd of a child that runs until its input closes.
	child := exec.Command("cat")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		child.Wait()
	}()
	running, ok, err := openProcess(child.Process.Pid)
	if err != nil || !ok {
		t.Fatalf("pidfd of the running child: %v", err)
	}
	defer syscall.Close(running)

	const listed = 4242
	opens := func(fds ...int) func(int) (int, bool, error) {
		return func(pid int) (int, bool, error) {
			if pid != listed || len(fds) == 0 {
				t.Fatalf("open %d after the injected opens", pid)
			}
			fd, err := syscall.Dup(fds[0])
			fds = fds[1:]
			return fd, err == nil, err
		}
	}
	list := func() ([]int, error) { return []int{listed}, nil }

	source := procSource{list: list, open: opens(ended, running), read: markedRead}
	found, err := source.with(procMarker)
	if err != nil || len(found) != 1 {
		t.Fatalf("found %v, error %v; want the successor alone", found, err)
	}
	defer syscall.Close(found[0].fd)
	if stopped, err := readableWithin(found[0].fd, 0); err != nil || stopped {
		t.Errorf("the pidfd returned for id %d names an ended process (ended %t, %v); want the running successor", listed, stopped, err)
	}

	source = procSource{list: list, open: opens(ended, ended, ended), read: markedRead}
	if found, err := source.with(procMarker); err != nil || len(found) != 0 {
		t.Errorf("an id whose process ended at each open: found %v, error %v; want none", found, err)
	}
}
