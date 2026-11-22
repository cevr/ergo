//go:build linux && (amd64 || arm64)

package screen

import (
	"fmt"
	"math/rand/v2"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// pty is a pseudo-terminal pair: the controlling side and the terminal.
func pty(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Fatal("TIOCSPTLCK:", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Fatal("TIOCGPTN:", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

// TestTermSizeIsWhatWasSet: on a pseudo-terminal pair, termSize (an ioctl
// TIOCGWINSZ into a local winsize) answers the columns and rows a
// TIOCSWINSZ set, for 40 seeded sizes of 1..65535 each way and the pixel
// sizes seeded too, on either side of the pair; a size of zero is
// refused. Linux only: the pair is opened through /dev/ptmx.
//
// Mutant (testdata/mutants.json): OTUL-term-size-swapped, the winsize's
// rows read as the columns.
func TestTermSizeIsWhatWasSet(t *testing.T) {
	m, s := pty(t)
	r := rand.New(rand.NewPCG(7, 0x0751))
	set := func(ws *struct{ Row, Col, X, Y uint16 }) {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(ws))); e != 0 {
			t.Fatal("TIOCSWINSZ:", e)
		}
	}
	for range 40 {
		ws := struct{ Row, Col, X, Y uint16 }{uint16(1 + r.IntN(65535)), uint16(1 + r.IntN(65535)), uint16(r.IntN(65536)), uint16(r.IntN(65536))}
		set(&ws)
		for _, f := range []*os.File{m, s} {
			cols, rows, err := termSize(f)
			if err != nil || cols != int(ws.Col) || rows != int(ws.Row) {
				t.Fatalf("termSize(%s) = %d, %d, %v; set %d columns, %d rows", f.Name(), cols, rows, err, ws.Col, ws.Row)
			}
		}
	}
	set(&struct{ Row, Col, X, Y uint16 }{0, 80, 0, 0})
	if _, _, err := termSize(s); err == nil {
		t.Fatal("termSize answered a size of zero rows")
	}
}
