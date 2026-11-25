//go:build linux || darwin

package v1

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

var (
	cKept []byte
	cSink byte
)

func TestCGuardInProcessFaults(t *testing.T) {
	in := []byte("abcdefgh")
	for _, tc := range []struct {
		name     string
		writable bool
		call     func(p []byte)
		after    func()
		verb     string
	}{
		{"const-write", false, func(p []byte) { p[2] = 'z' }, nil, "wrote to"},
		{"over-read-1-byte", false, func(p []byte) {
			cSink = *(*byte)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(p)), len(p)))
		}, nil, "read past the end of"},
		{"post-return-retain", false, func(p []byte) { cKept = p }, func() { cSink = cKept[1] }, "kept"},
		{"out-overflow-1-byte", true, func(p []byte) {
			*(*byte)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(p)), len(p))) = 0x7f
		}, nil, "wrote past the end of"},
		{"out-post-return-retain", true, func(p []byte) { cKept = p }, func() { cSink = cKept[0] }, "kept"},
		{"valid-read", false, func(p []byte) {
			if !bytes.Equal(p, in) {
				t.Errorf("valid-read got %q, want %q", p, in)
			}
		}, nil, ""},
		{"valid-write", true, func(p []byte) {
			copy(p, "WXYZ1234")
		}, nil, ""},
	} {
		dst := append([]byte(nil), in...)
		got := func() (verb string) {
			defer func() {
				if p := recover(); p != nil {
					d, ok := rt.FaultValue(p).(*ViewDefect)
					if !ok {
						t.Errorf("%s: panic %v is not *ViewDefect", tc.name, p)
						return
					}
					verb = d.Verb
				}
			}()
			var g *CBufferGuard
			if tc.writable {
				g = GuardCWrite(dst, "fixture.F", "buf", "cffi")
			} else {
				g = GuardCRead(dst, "fixture.F", "buf", "cffi")
			}
			if cap(g.Bytes()) != len(dst) {
				t.Fatalf("%s: cap=%d, want %d", tc.name, cap(g.Bytes()), len(dst))
			}
			tc.call(g.Bytes())
			g.Done()
			if tc.after != nil {
				tc.after()
			}
			return ""
		}()
		if got != tc.verb {
			t.Errorf("%s: verb=%q, want %q", tc.name, got, tc.verb)
		}
		if tc.name == "valid-write" && string(dst) != "WXYZ1234" {
			t.Errorf("valid-write did not copy back: got %q", dst)
		}
	}
	cKept = nil

	// Typed slice wrappers: GuardCInSlice and GuardCOutSlice.
	inU32 := []uint32{10, 20, 30, 40}
	gi := GuardCInSlice(inU32, "fixture.In", "nums", "cffi")
	gotIn := unsafe.Slice((*uint32)(gi.Pointer()), len(inU32))
	for i, want := range inU32 {
		if gotIn[i] != want {
			t.Fatalf("GuardCInSlice[%d] = %d, want %d", i, gotIn[i], want)
		}
	}
	gi.Done()

	outU32 := make([]uint32, 4)
	goGuard := GuardCOutSlice(outU32, "fixture.Out", "nums", "cffi")
	dstU32 := unsafe.Slice((*uint32)(goGuard.Pointer()), len(outU32))
	for i := range dstU32 {
		dstU32[i] = uint32(100 + i)
	}
	goGuard.Done()
	for i, got := range outU32 {
		if got != uint32(100+i) {
			t.Fatalf("GuardCOutSlice[%d] = %d, want %d", i, got, 100+i)
		}
	}

	// Empty slices are no-ops.
	if g := GuardCRead(nil, "f", "p", "at"); g.Bytes() != nil || g.Pointer() != nil {
		t.Fatalf("empty GuardCRead should return nil slice and pointer")
	}
	if g := GuardCWrite(nil, "f", "p", "at"); g.Bytes() != nil || g.Pointer() != nil {
		t.Fatalf("empty GuardCWrite should return nil slice and pointer")
	}

	// Guard-ring memory bound law: cGuardsLive - unfinished <= guardRingLen.
	unfinished := cGuardsLive() - len(cGuardRing)
	for range 3 * guardRingLen {
		g := GuardCRead(in, "fixture.F", "buf", "cffi")
		g.Done()
	}
	if live := cGuardsLive() - unfinished; live > guardRingLen {
		t.Fatalf("%d live C guard mappings after calls, want at most %d", live, guardRingLen)
	}
}

func TestCGuardSubprocessSIGSEGV(t *testing.T) {
	if mode := os.Getenv("ERGO_CGUARD_SUBPROC"); mode != "" {
		buf := []byte("01234567")
		switch mode {
		case "over-read":
			g := GuardCRead(buf, "c", "p", "at")
			debug.SetPanicOnFault(false)
			cSink = *(*byte)(unsafe.Add(g.Pointer(), len(buf)))
		case "const-write":
			g := GuardCRead(buf, "c", "p", "at")
			debug.SetPanicOnFault(false)
			*(*byte)(g.Pointer()) = 'X'
		case "post-return":
			g := GuardCRead(buf, "c", "p", "at")
			ptr := g.Pointer()
			g.Done()
			debug.SetPanicOnFault(false)
			cSink = *(*byte)(ptr)
		case "out-overflow":
			g := GuardCWrite(buf, "c", "p", "at")
			debug.SetPanicOnFault(false)
			*(*byte)(unsafe.Add(g.Pointer(), len(buf))) = 0x7f
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}

	for _, mode := range []string{"over-read", "const-write", "post-return", "out-overflow"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run", "^TestCGuardSubprocessSIGSEGV$")
			cmd.Env = append(os.Environ(), "ERGO_CGUARD_SUBPROC="+mode, "GOTRACEBACK=none")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s: subprocess exited 0, expected SIGSEGV\n%s", mode, out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("%s: unexpected error %v", mode, err)
			}
			ws, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatalf("%s: no WaitStatus: %v", mode, err)
			}
			sigged := ws.Signaled() && (ws.Signal() == syscall.SIGSEGV || ws.Signal() == syscall.SIGBUS)
			runtimeFault := strings.Contains(string(out), "SIGSEGV") || strings.Contains(string(out), "SIGBUS") || strings.Contains(string(out), "segmentation violation")
			if !sigged && !runtimeFault {
				t.Fatalf("%s: expected SIGSEGV/SIGBUS, got exit=%v signaled=%v sig=%v\n%s", mode, exitErr.ExitCode(), ws.Signaled(), ws.Signal(), out)
			}
		})
	}
}
