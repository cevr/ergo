//go:build darwin

package exec

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// ptyGrantUnlockIoctl executes a zero-argument Darwin PTY ioctl (TIOCPTYGRANT or TIOCPTYUNLK).
func ptyGrantUnlockIoctl(fd uintptr, req uintptr) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, 0)
	return errno
}

// ptyGnameIoctl executes Darwin TIOCPTYGNAME into a 128-byte kernel name buffer.
// Safety invariant: buf points to the first element of a live [128]byte buffer
// kept for the call and converted in the Syscall call expression (unsafe.Pointer rule 4).
func ptyGnameIoctl(fd uintptr, buf *[128]byte) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0])))
	return errno
}

func openOSPTY() (master, slave *os.File, slavePath string, err error) {
	syscall.ForkLock.RLock()
	mfd, serr := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if serr == nil {
		syscall.CloseOnExec(mfd)
	}
	syscall.ForkLock.RUnlock()
	if serr != nil {
		return nil, nil, "", ExecFailed{Name: "pty", Reason: serr.Error()}
	}
	master = os.NewFile(uintptr(mfd), "pty-master")
	if errno := ptyGrantUnlockIoctl(uintptr(mfd), syscall.TIOCPTYGRANT); errno != 0 {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	if errno := ptyGrantUnlockIoctl(uintptr(mfd), syscall.TIOCPTYUNLK); errno != 0 {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	var nameBuf [128]byte
	if errno := ptyGnameIoctl(uintptr(mfd), &nameBuf); errno != 0 {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	n := bytes.IndexByte(nameBuf[:], 0)
	if n < 0 {
		n = len(nameBuf)
	}
	slavePath = string(nameBuf[:n])
	syscall.ForkLock.RLock()
	sfd, serr := syscall.Open(slavePath, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if serr == nil {
		syscall.CloseOnExec(sfd)
	}
	syscall.ForkLock.RUnlock()
	if serr != nil {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: serr.Error()}
	}
	slave = os.NewFile(uintptr(sfd), slavePath)
	return master, slave, slavePath, nil
}

func execNoteExitFlags() uint32 {
	return syscall.NOTE_EXIT | syscall.NOTE_EXITSTATUS
}

func execKqueueExitStatus(fflags uint32, data int64) (status, sig int, ok bool) {
	if fflags&syscall.NOTE_EXITSTATUS == 0 {
		return 0, 0, false
	}
	ws := syscall.WaitStatus(data)
	st, sg := waitStatusSignal(ws)
	return st, sg, true
}
