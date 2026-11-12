//go:build linux

package opcount

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"ergo.local/prototype/internal/proclimit"
)

// perfEventAttr is the first version of the kernel's struct
// perf_event_attr (linux/perf_event.h, PERF_ATTR_SIZE_VER0: 64 bytes).
// The kernel reads Size bytes and takes later fields as zero.
type perfEventAttr struct {
	Type         uint32
	Size         uint32
	Config       uint64
	SamplePeriod uint64
	SampleType   uint64
	ReadFormat   uint64
	Flags        uint64
	WakeupEvents uint32
	BpType       uint32
	BpAddr       uint64
}

const (
	perfTypeHardware   = 0
	perfCountHWInstr   = 1
	perfFormatEnabled  = 1 << 0 // PERF_FORMAT_TOTAL_TIME_ENABLED
	perfFormatRunning  = 1 << 1 // PERF_FORMAT_TOTAL_TIME_RUNNING
	perfFlagDisabled   = 1 << 0
	perfFlagInherit    = 1 << 1
	perfFlagExclKernel = 1 << 5
	perfFlagExclHV     = 1 << 6
	perfFlagEnableExec = 1 << 12
	perfFlagFDCloexec  = 1 << 3 // perf_event_open's flags argument
)

// openCounter opens a counter of user-space instructions on the calling
// thread, which the caller has locked: disabled, inherited by the
// processes the thread starts, and enabled in each at its exec (as `perf
// stat` counts a command), so the caller's own threads are never counted.
func openCounter() (int, error) {
	attr := perfEventAttr{
		Type:       perfTypeHardware,
		Config:     perfCountHWInstr,
		ReadFormat: perfFormatEnabled | perfFormatRunning,
		Flags:      perfFlagDisabled | perfFlagInherit | perfFlagExclKernel | perfFlagExclHV | perfFlagEnableExec,
	}
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), perfFlagFDCloexec, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// Available reports whether this host counts user-space instructions, and
// why not.
func Available() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fd, err := openCounter()
	if err != nil {
		return fmt.Errorf("perf_event_open of user-space instructions: %w", err)
	}
	syscall.Close(fd)
	return nil
}

// Count starts cmd under proclimit's memory limit (0: none), in a process
// group of its own, waits for it, and measures it (Run). Its output should
// go to files, not pipes: Wait waits for a pipe that a left process holds.
// A process of the group still running after the program exits is killed
// with the group, and the run is RunIncomplete. The count also holds the
// instructions of proclimit's bound helper between its exec and the
// program's exec: the same for every run of one caller, so it cancels in a
// slope and in a comparison of two programs by one caller.
// The error is for a program that could not start.
func Count(cmd *exec.Cmd, limit uint64) (Run, error) {
	// A per-thread counter is inherited only by the processes this thread
	// starts, so the goroutine stays on the thread that opens it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	fd, openErr := openCounter()
	if fd >= 0 {
		defer syscall.Close(fd)
	}
	if err := proclimit.Start(cmd, limit); err != nil {
		return Run{}, err
	}
	waitErr := cmd.Wait()
	r := Run{Wait: waitErr}
	// The group is the program's own (Setpgid): a member left after it
	// exited is a process this run started and did not end.
	if group := cmd.Process.Pid; syscall.Kill(-group, 0) == nil {
		r.Incomplete = "a process of its group was still running when it exited (killed)"
		syscall.Kill(-group, syscall.SIGKILL)
	}
	if state := cmd.ProcessState; state != nil {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok {
			switch {
			case ws.Signaled():
				r.Incomplete = fmt.Sprintf("killed by %v", ws.Signal())
			case ws.ExitStatus() != 0:
				r.Incomplete = fmt.Sprintf("exit status %d", ws.ExitStatus())
			}
		}
		if ru, ok := state.SysUsage().(*syscall.Rusage); ok {
			r.UserCPU = time.Duration(ru.Utime.Nano())
			r.SysCPU = time.Duration(ru.Stime.Nano())
			r.MaxRSSKiB = ru.Maxrss
		}
	} else if waitErr != nil {
		r.Incomplete = waitErr.Error()
	}
	if fd < 0 {
		r.Uncounted, r.Why = NoCounter, fmt.Sprintf("perf_event_open of user-space instructions: %v", openErr)
		return r, nil
	}
	var buf [24]byte
	if n, err := syscall.Read(fd, buf[:]); err != nil || n != len(buf) {
		r.Uncounted, r.Why = NoCounter, fmt.Sprintf("read the counter: %d bytes, %v", n, err)
		return r, nil
	}
	word := func(i int) uint64 { return binary.NativeEndian.Uint64(buf[8*i:]) }
	r.Instructions, r.Uncounted, r.Why = decode(word(0), word(1), word(2))
	return r, nil
}
