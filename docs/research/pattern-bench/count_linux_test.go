package patternbench

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// perfAttr is struct perf_event_attr, to its fields the count uses.
type perfAttr struct {
	Type, Size             uint32
	Config, Sample         uint64
	SampleType, ReadFormat uint64
	Bits                   uint64
	Rest                   [13]uint64
}

// counter counts the user-space instructions of this thread: the bench
// locks its goroutine to the thread while it counts.
type counter struct{ fd int }

func startCount(b *testing.B) *counter {
	b.Helper()
	runtime.LockOSThread()
	attr := perfAttr{Type: 0 /* PERF_TYPE_HARDWARE */, Size: uint32(unsafe.Sizeof(perfAttr{})), Config: 1 /* INSTRUCTIONS */}
	attr.Bits = 1<<5 | 1<<6 // exclude_kernel, exclude_hv
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	if errno != 0 {
		return &counter{fd: -1}
	}
	b.ResetTimer()
	return &counter{fd: int(fd)}
}

// report stops the count and reports instructions per match, of perOp
// matches per b.N.
func (c *counter) report(b *testing.B, perOp int) {
	b.StopTimer()
	defer runtime.UnlockOSThread()
	if c.fd < 0 {
		return
	}
	defer syscall.Close(c.fd)
	var buf [8]byte
	if n, err := syscall.Read(c.fd, buf[:]); err != nil || n != 8 {
		return
	}
	b.ReportMetric(float64(binary.LittleEndian.Uint64(buf[:]))/float64(b.N*perOp), "instr/match")
}
