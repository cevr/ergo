//go:build linux

package v1

import (
	"encoding/binary"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"unsafe"
)

// TestBuilderAppendFromCounts is the counted guard of WriteFrom's speed
// (SB2; stdlib.md strings 11): the user-space instructions per run of
// StringsBuilderAppendFrom against hand Go on one thread, counted by the
// hardware counter, never timed. A run the buffer holds already must cost
// no more than hand Go's one append (at most appendFromHeld of it), and a
// run of 60 bytes at distance 3, which overlaps its own output, at most
// appendFromOverlap of hand Go's byte loop (Go LZ decoders copy an
// overlapping match byte by byte). Each side is counted over 1,000 and
// 11,000 runs into a buffer with room for all of them (no allocation, so
// no collector work on the thread), and the difference over 10,000 runs
// leaves the counter's own cost out; the median of 5 rounds is compared.
// It skips where the host does not count user-space instructions.
//
// Mutant (runtime/v1 ledger): sb2-appendfrom-bytewise (one byte a pass)
// puts the overlapping run above its bound.
func TestBuilderAppendFromCounts(t *testing.T) {
	const appendFromHeld, appendFromOverlap = 1.1, 0.5
	base := []byte("the quick brown fox jumps over the lazy dog, 0123456789")
	base = slices.Repeat(base, 64)
	buf := make([]byte, len(base), len(base)+11_000*60)
	copy(buf, base)
	runs := func(n int, held bool, f func(b []byte, start, k int) []byte) func() {
		return func() {
			b := buf[:len(base)]
			for i := range n {
				if held {
					b = f(b, (i*37)%(len(base)-40), 40)
				} else {
					b = f(b, len(b)-3, 60)
				}
			}
			textSink = len(b)
		}
	}
	ergo := func(b []byte, start, k int) []byte { return StringsBuilderAppendFrom(b, start, k) }
	hand := func(b []byte, start, k int) []byte {
		if start+k <= len(b) {
			return append(b, b[start:start+k]...)
		}
		for j := range k {
			b = append(b, b[start+j])
		}
		return b
	}
	for _, c := range []struct {
		name  string
		held  bool
		bound float64
	}{{"held", true, appendFromHeld}, {"overlapping", false, appendFromOverlap}} {
		var e, h []float64
		for range 5 {
			for _, side := range []struct {
				f   func(b []byte, start, k int) []byte
				per *[]float64
			}{{ergo, &e}, {hand, &h}} {
				lo, err := countInstructions(runs(1_000, c.held, side.f))
				if err != nil {
					t.Skipf("no counter of user-space instructions: %v", err)
				}
				hi, _ := countInstructions(runs(11_000, c.held, side.f))
				*side.per = append(*side.per, float64(hi-lo)/10_000)
			}
		}
		ratio := median(e) / median(h)
		t.Logf("%s run: AppendFrom %.1f instructions, hand Go %.1f; ratio %.3f", c.name, median(e), median(h), ratio)
		if ratio > c.bound {
			t.Errorf("a %s run costs %.3f of hand Go's instructions; want at most %.2f", c.name, ratio, c.bound)
		}
	}
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

// countInstructions is the user-space instructions the calling thread runs
// in f, from a hardware counter opened on the thread (perf_event_open,
// as internal/opcount counts a child; perf_event_attr version 0).
func countInstructions(f func()) (uint64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	attr := struct {
		Type, Size                                      uint32
		Config, SamplePeriod, SampleType, Format, Flags uint64
		WakeupEvents, BpType                            uint32
		BpAddr                                          uint64
	}{Type: 0, Config: 1, Flags: 1<<0 | 1<<5 | 1<<6} // hardware instructions; disabled, no kernel, no hypervisor
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 1<<3, 0)
	if errno != 0 {
		return 0, errno
	}
	defer syscall.Close(int(fd))
	const enable, disable = 0x2400, 0x2401
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, enable, 0); errno != 0 {
		return 0, errno
	}
	f()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, disable, 0); errno != 0 {
		return 0, errno
	}
	var out [8]byte
	if _, err := syscall.Read(int(fd), out[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(out[:]), nil
}
