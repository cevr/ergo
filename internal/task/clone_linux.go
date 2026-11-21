package task

import (
	"os"
	"syscall"
)

// ficlone is Linux's FICLONE ioctl: dst shares src's blocks (btrfs, XFS).
const ficlone = 0x40049409

// cloneFile makes dst a clone of src where the file system can.
func cloneFile(dst, src *os.File) bool {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dst.Fd(), ficlone, src.Fd())
	return errno == 0
}
