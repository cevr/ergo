//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

var errPTYUnsupported error

type ptyWinsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// ptyWinsizeIoctl performs TIOCGWINSZ or TIOCSWINSZ ioctl on fd.
// Safety invariant: ws points to a live ptyWinsize value kept for the call
// and converted in the Syscall call expression (unsafe.Pointer rule 4).
func ptyWinsizeIoctl(fd uintptr, req uintptr, ws *ptyWinsize) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(ws)))
	return errno
}

func ptyGetSizeOS(f *os.File) (ExecWinsize, error) {
	if f == nil {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: fsReason(err)}
	}
	var ws ptyWinsize
	var errno syscall.Errno
	if cerr := sc.Control(func(u uintptr) {
		errno = ptyWinsizeIoctl(u, syscall.TIOCGWINSZ, &ws)
	}); cerr != nil {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: fsReason(cerr)}
	}
	if errno != 0 {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	return ExecWinsize{
		Rows:    int(ws.Row),
		Cols:    int(ws.Col),
		XPixels: int(ws.Xpixel),
		YPixels: int(ws.Ypixel),
	}, nil
}

func ptySetSizeOS(f *os.File, size ExecWinsize) error {
	if f == nil {
		return ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return ExecFailed{Name: "pty", Reason: fsReason(err)}
	}
	ws := ptyWinsize{
		Row:    uint16(size.Rows),
		Col:    uint16(size.Cols),
		Xpixel: uint16(size.XPixels),
		Ypixel: uint16(size.YPixels),
	}
	var errno syscall.Errno
	if cerr := sc.Control(func(u uintptr) {
		errno = ptyWinsizeIoctl(u, syscall.TIOCSWINSZ, &ws)
	}); cerr != nil {
		return ExecFailed{Name: "pty", Reason: fsReason(cerr)}
	}
	if errno != 0 {
		return ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	return nil
}

func osFileFd(f *os.File) int {
	if f == nil {
		return -1
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return -1
	}
	out := -1
	if err := sc.Control(func(u uintptr) {
		out = int(u)
	}); err != nil {
		return -1
	}
	return out
}

func dupCloseOnExec(rawFD int, name string) (*os.File, error) {
	newFD, err := dupRawCloseOnExec(rawFD, name)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(newFD), name), nil
}

func dupRawCloseOnExec(rawFD int, name string) (int, error) {
	if rawFD < 0 {
		return -1, ExecFailed{Name: name, Reason: "invalid file descriptor"}
	}
	syscall.ForkLock.RLock()
	newFD, err := syscall.Dup(rawFD)
	if err == nil {
		syscall.CloseOnExec(newFD)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, ExecFailed{Name: name, Reason: err.Error()}
	}
	return newFD, nil
}

func osDupFileRaw(f *os.File, name string) (int, error) {
	sc, err := f.SyscallConn()
	if err != nil {
		return -1, ExecFailed{Name: name, Reason: fsReason(err)}
	}
	out := -1
	var dupErr error
	if cerr := sc.Control(func(u uintptr) {
		out, dupErr = dupRawCloseOnExec(int(u), name)
	}); cerr != nil {
		return -1, ExecFailed{Name: name, Reason: fsReason(cerr)}
	}
	if dupErr != nil {
		return -1, dupErr
	}
	return out, nil
}

func osDupFileObj(f *os.File, name string) (*os.File, error) {
	fd, err := osDupFileRaw(f, name)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

var pastReadDeadline = time.Unix(1, 0)

type execStopWatch struct {
	mu    sync.Mutex
	over  bool
	fired bool
	quit  chan struct{}
}

func execOnStop(t *rt.Task, closeCh <-chan struct{}, set func(time.Time) error) (disarm func() bool) {
	done := t.Done()
	w := &execStopWatch{quit: make(chan struct{})}
	go func() {
		select {
		case <-done:
			w.mu.Lock()
			if !w.over {
				w.fired = true
				_ = set(pastReadDeadline)
			}
			w.mu.Unlock()
		case <-closeCh:
			w.mu.Lock()
			if !w.over {
				_ = set(pastReadDeadline)
			}
			w.mu.Unlock()
		case <-w.quit:
		}
	}()
	return func() bool {
		w.mu.Lock()
		w.over = true
		fired := w.fired
		w.mu.Unlock()
		close(w.quit)
		return fired
	}
}

func readFileInterruptible(t *rt.Task, f *os.File, closeCh <-chan struct{}, name string, isPTY bool) (string, error) {
	if f == nil {
		return "", ExecFailed{Name: name, Reason: name + " is closed"}
	}
	buf := make([]byte, execChunkSize)
	if f.SetReadDeadline(time.Time{}) == nil {
		disarm := execOnStop(t, closeCh, f.SetReadDeadline)
		n, err := f.Read(buf)
		fired := disarm()
		_ = f.SetReadDeadline(time.Time{})
		if n > 0 {
			return string(buf[:n]), nil
		}
		if err == io.EOF || (isPTY && errors.Is(err, syscall.EIO)) {
			return "", nil
		}
		if fired {
			t.Check()
		}
		select {
		case <-closeCh:
			return "", ExecFailed{Name: name, Reason: name + " is closed"}
		default:
		}
		if errors.Is(err, os.ErrClosed) {
			return "", ExecFailed{Name: name, Reason: name + " is closed"}
		}
		return "", ExecFailed{Name: name, Reason: fsReason(err)}
	}
	dupF, err := osDupFileObj(f, name)
	if err != nil {
		return "", err
	}
	defer func() { _ = dupF.Close() }()
	if err := awaitFDReadableOS(t, dupF, closeCh, name); err != nil {
		return "", err
	}
	n, err := dupF.Read(buf)
	if n > 0 {
		return string(buf[:n]), nil
	}
	if err == io.EOF || (isPTY && errors.Is(err, syscall.EIO)) {
		return "", nil
	}
	select {
	case <-closeCh:
		return "", ExecFailed{Name: name, Reason: name + " is closed"}
	default:
	}
	if errors.Is(err, os.ErrClosed) {
		return "", ExecFailed{Name: name, Reason: name + " is closed"}
	}
	return "", ExecFailed{Name: name, Reason: fsReason(err)}
}

func execOpenPTYOS() (*PTY, error) {
	master, slave, slavePath, err := openOSPTY()
	if err != nil {
		return nil, err
	}
	return &PTY{
		master:    master,
		slave:     slave,
		slavePath: slavePath,
		closeCh:   make(chan struct{}),
		memFD:     -1,
	}, nil
}

func execOpenPipeOS() (*Pipe, *Pipe, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, ExecFailed{Name: "pipe", Reason: fsReason(err)}
	}
	return newOSPipe(pr), newOSPipe(pw), nil
}

func execReattachPTYOS(rawFD int) (*PTY, error) {
	f, err := dupCloseOnExec(rawFD, "pty")
	if err != nil {
		return nil, err
	}
	if _, err := ptyGetSizeOS(f); err != nil {
		_ = f.Close()
		return nil, ExecFailed{Name: "pty", Reason: "descriptor is not a terminal: " + err.Error()}
	}
	return &PTY{
		master:  f,
		closeCh: make(chan struct{}),
		memFD:   -1,
	}, nil
}

func execReattachPipeOS(rawFD int) (*Pipe, error) {
	f, err := dupCloseOnExec(rawFD, "pipe")
	if err != nil {
		return nil, err
	}
	if _, err := f.Stat(); err != nil {
		_ = f.Close()
		return nil, ExecFailed{Name: "pipe", Reason: fsReason(err)}
	}
	return newOSPipe(f), nil
}

func acquireSlaveForSpawn(p *PTY, cmdName string) (slave *os.File, tempOpened bool, err error) {
	if p == nil {
		return nil, false, ExecFailed{Name: cmdName, Reason: "PTY is closed"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false, ExecFailed{Name: cmdName, Reason: "PTY is closed"}
	}
	if p.mem != nil {
		return nil, false, nil
	}
	if p.slave != nil {
		return p.slave, false, nil
	}
	if p.slavePath == "" {
		return nil, false, ExecFailed{Name: cmdName, Reason: "reattached PTY has no slave device path"}
	}
	f, err := os.OpenFile(p.slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, false, ExecFailed{Name: cmdName, Reason: fsReason(err)}
	}
	return f, true, nil
}

func releaseParentSlave(p *PTY, tempOpened bool, tempFile *os.File) {
	if tempOpened {
		if tempFile != nil {
			_ = tempFile.Close()
		}
		return
	}
	p.CloseSlave()
}

func execStartWithPTYOS(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration, p *PTY) (*Running, error) {
	t.Check()
	if stdin != "" {
		return nil, ExecFailed{Name: name, Reason: "PTY command cannot specify buffered Stdin; write to PTY directly"}
	}
	if p == nil {
		return nil, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	mem := p.mem
	p.mu.Unlock()
	if closed {
		return nil, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	if mem != nil {
		return nil, ExecFailed{Name: name, Reason: "cannot start OS process on memory PTY"}
	}
	cmd, err := ExecCommand(name, args, env, dir, "")
	if err != nil {
		return nil, err
	}
	slave, tempOpened, err := acquireSlaveForSpawn(p, name)
	if err != nil {
		return nil, err
	}
	exit, err := execWatchExit()
	if err != nil {
		if tempOpened && slave != nil {
			_ = slave.Close()
		}
		return nil, execErr(name, err)
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	cmd.WaitDelay = execWaitDelay
	v1.FlushStdout()
	child, err := execBegin(cmd, "")
	releaseParentSlave(p, tempOpened, slave)
	if err != nil {
		exit.close()
		return nil, execStartErr(name, err)
	}
	start := execStartTime(child.pid)
	hp := &hostProc{child: child, group: child.pid, exit: exit, done: make(chan struct{})}
	go func() {
		exit.await(hp.group)
		close(hp.done)
		if hp.isDetached() {
			exit.close()
		}
	}()
	return newRunningHost(hp, name, child.pid, start, false, grace, nil, nil, nil, nil), nil
}

func execSpawnWithPTYOS(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, p *PTY) (ExecDetached, error) {
	t.Check()
	if stdin != "" {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY command cannot specify buffered Stdin; write to PTY directly"}
	}
	if p == nil {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	mem := p.mem
	p.mu.Unlock()
	if closed {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	if mem != nil {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "cannot spawn OS process on memory PTY"}
	}
	cmd, err := ExecCommand(name, args, env, dir, "")
	if err != nil {
		return ExecDetached{}, err
	}
	slave, tempOpened, err := acquireSlaveForSpawn(p, name)
	if err != nil {
		return ExecDetached{}, err
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	v1.FlushStdout()
	err = cmd.Start()
	releaseParentSlave(p, tempOpened, slave)
	if err != nil {
		return ExecDetached{}, execErr(name, err)
	}
	pid := cmd.Process.Pid
	start := execStartTime(pid)
	reg := registerDetachedChild(pid, start)
	go func() {
		_ = cmd.Wait()
		status, sig := execStatusSignal(cmd.ProcessState)
		reg.complete(status, sig)
	}()
	return ExecDetached{Pid: pid, Start: start, Name: name}, nil
}

func waitStatusSignal(ws syscall.WaitStatus) (status, sig int) {
	if ws.Signaled() {
		s := int(ws.Signal())
		return 128 + s, s
	}
	return ws.ExitStatus(), 0
}

func tryReapChildIfMatch(pid int, start int64) (status, sig int, reaped bool) {
	if pid <= 0 {
		return 0, 0, false
	}
	if start != 0 {
		if curStart := execStartTime(pid); curStart != 0 && curStart != start {
			return 0, 0, false
		}
	}
	var ws syscall.WaitStatus
	for {
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || wpid <= 0 {
			return 0, 0, false
		}
		st, sg := waitStatusSignal(ws)
		recordCompletedChild(pid, start, st, sg)
		return st, sg, true
	}
}

func execPollDetachedOS(pid int, start int64) (int, int, int, bool, string, error) {
	if pid <= 0 {
		return execOutcomeUnavailable, 0, 0, true, "invalid pid", nil
	}
	if entry := lookupDetachedEntry(pid, start); entry != nil {
		entry.mu.Lock()
		exited := entry.exited
		st, sg := entry.status, entry.signal
		entry.mu.Unlock()
		if exited {
			return execOutcomeExited, st, sg, false, "", nil
		}
		if execRunning(pid, start) {
			return execOutcomeRunning, 0, 0, false, "", nil
		}
		return execOutcomeRunning, 0, 0, false, "", nil
	}
	if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
		return execOutcomeExited, st, sg, false, "", nil
	}
	if execRunning(pid, start) {
		return execOutcomeRunning, 0, 0, false, "", nil
	}
	return execOutcomeUnavailable, 0, 0, true, "process exited or start time mismatch", nil
}

func execWaitDetachedOS(t *rt.Task, pid int, start int64) (int, int, int, bool, string, error) {
	t.Check()
	if pid <= 0 {
		return execOutcomeUnavailable, 0, 0, true, "invalid pid", nil
	}
	if entry := lookupDetachedEntry(pid, start); entry != nil {
		select {
		case <-entry.done:
			entry.mu.Lock()
			st, sg := entry.status, entry.signal
			entry.mu.Unlock()
			return execOutcomeExited, st, sg, false, "", nil
		case <-t.Done():
			t.Check()
			<-entry.done
			entry.mu.Lock()
			st, sg := entry.status, entry.signal
			entry.mu.Unlock()
			return execOutcomeExited, st, sg, false, "", nil
		}
	}
	if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
		return execOutcomeExited, st, sg, false, "", nil
	}
	if !execRunning(pid, start) {
		return execOutcomeUnavailable, 0, 0, true, "process exited or start time mismatch", nil
	}
	return awaitDetachedNonChildOS(t, pid, start)
}
