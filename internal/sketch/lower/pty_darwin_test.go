//go:build darwin

package lower

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

const (
	ptyGetState = syscall.TIOCGETA
)

// openPTY opens a pseudo-terminal pair: the master side the test types
// into, and the slave side a program reads as its terminal (posix_openpt,
// grantpt, unlockpt and ptsname, as ioctls).
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := ptyIoctl(master.Fd(), syscall.TIOCPTYGRANT, nil); err != nil {
		master.Close()
		return nil, nil, err
	}
	if err := ptyIoctl(master.Fd(), syscall.TIOCPTYUNLK, nil); err != nil {
		master.Close()
		return nil, nil, err
	}
	name := make([]byte, 128)
	if err := ptyIoctl(master.Fd(), syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		master.Close()
		return nil, nil, err
	}
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	slave, err = os.OpenFile(string(name), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
