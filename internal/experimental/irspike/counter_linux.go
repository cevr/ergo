//go:build linux

package irspike

import (
	"encoding/binary"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// perfEventAttr is the prefix of struct perf_event_attr this file sets
// (linux/perf_event.h, PERF_ATTR_SIZE_VER0 is 64 bytes).
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
	perfTypeHardware      = 0
	perfCountHWInstr      = 1
	perfFlagDisabled      = 1 << 0
	perfFlagInherit       = 1 << 1
	perfFlagExcludeKernel = 1 << 5
	perfFlagExcludeHV     = 1 << 6
	perfIOCEnable         = 0x2400
	perfIOCDisable        = 0x2401
)

// Cost is the counted work of one command and its descendants.
type Cost struct {
	// Instructions are the user-space instructions retired by the
	// command's process tree (perf_event_open, inherit), or -1 when the
	// host exposes no hardware counter.
	Instructions int64 `json:"instructions"`
	// UserCPU and SysCPU are the rusage CPU times of the process tree
	// (the waited-for descendants included).
	UserCPU time.Duration `json:"user_cpu_ns"`
	SysCPU  time.Duration `json:"sys_cpu_ns"`
	// MaxRSS is the largest resident set of one process of the tree, KiB.
	MaxRSS int64 `json:"max_rss_kib"`
}

// Measure runs cmd and counts its process tree's work. The counter is
// opened on this process with inherit set and enabled just before the
// start: it also counts this process's own threads while they wait, a
// bounded overhead that is the same for every command measured.
func Measure(cmd *exec.Cmd) (Cost, error) {
	// A per-thread counter: the child inherits it only from the thread that
	// forks it, so the goroutine stays on the thread that opened it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	attr := perfEventAttr{Type: perfTypeHardware, Config: perfCountHWInstr, Flags: perfFlagDisabled | perfFlagInherit | perfFlagExcludeKernel | perfFlagExcludeHV}
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	counted := errno == 0
	if counted {
		defer syscall.Close(int(fd))
		syscall.Syscall(syscall.SYS_IOCTL, fd, perfIOCEnable, 0)
	}
	err := cmd.Run()
	c := Cost{Instructions: -1}
	if counted {
		syscall.Syscall(syscall.SYS_IOCTL, fd, perfIOCDisable, 0)
		var buf [8]byte
		if n, _ := syscall.Read(int(fd), buf[:]); n == 8 {
			c.Instructions = int64(binary.LittleEndian.Uint64(buf[:]))
		}
	}
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			c.UserCPU = time.Duration(ru.Utime.Nano())
			c.SysCPU = time.Duration(ru.Stime.Nano())
			c.MaxRSS = ru.Maxrss
		}
	}
	return c, err
}
