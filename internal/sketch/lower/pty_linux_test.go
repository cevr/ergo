//go:build linux

package lower

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	ptyGetState = syscall.TCGETS
)

// openPTY opens a pseudo-terminal pair: the master side the test types
// into, and the slave side a program reads as its terminal.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err := ptyIoctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		return nil, nil, err
	}
	var n uint32
	if err := ptyIoctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
