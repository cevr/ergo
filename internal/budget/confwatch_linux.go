package budget

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
)

// watchConfig wakes woke when the budget's file changes: written in place
// or replaced by a rename into its folder. A waiter so reads an edit at
// once (every decision reads the file again), with no clock. stop ends
// the watch.
func watchConfig(path string, woke chan<- struct{}) (stop func()) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return func() {}
	}
	if _, err := syscall.InotifyAddWatch(fd, filepath.Dir(path), syscall.IN_CLOSE_WRITE|syscall.IN_MOVED_TO|syscall.IN_CREATE); err != nil {
		syscall.Close(fd)
		return func() {}
	}
	f := os.NewFile(uintptr(fd), "inotify")
	name := filepath.Base(path)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			// Only the budget's file: its folder also holds busy files (the
			// log), whose writes must not wake every waiter.
			hit := false
			for off := 0; off+syscall.SizeofInotifyEvent <= n; {
				// struct inotify_event: wd, mask, cookie, len, then name.
				end := off + syscall.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buf[off+12:]))
				if end > n {
					break
				}
				raw := buf[off+syscall.SizeofInotifyEvent : end]
				if i := bytes.IndexByte(raw, 0); i >= 0 {
					raw = raw[:i]
				}
				hit = hit || string(raw) == name
				off = end
			}
			if hit {
				select {
				case woke <- struct{}{}:
				default:
				}
			}
		}
	}()
	return func() { f.Close() }
}
