//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package rt

import "syscall"

// readSmall reads a small file whole with the system calls themselves.
// It is not os.ReadFile, which sizes its buffer with File.Stat: a Stat's
// FileInfo links time's formatting and zone loading into every program
// (internal/sizes, fact "^time\.Time\.(String|Format|GoString)$"), and a
// cgroup or proc file reports size 0 anyway; nor os.File.Read, whose
// poller path a program that reads no file does not link otherwise
// (SIZE-HELLO). It runs once, before the root task starts.
func readSmall(name string) ([]byte, error) {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	for err == syscall.EINTR {
		fd, err = syscall.Open(name, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	var data []byte
	buf := make([]byte, 512)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return data, nil
		}
		data = append(data, buf[:n]...)
	}
}
