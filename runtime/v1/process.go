//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ergo.local/prototype/runtime/rt"
)

const (
	childStderrLimit = 32 << 10
	childDrainDelay  = 250 * time.Millisecond
	childKillTimeout = 2 * time.Second
	childGroupWait   = 500 * time.Millisecond
	childGroupPoll   = 5 * time.Millisecond
)

// ProcessStartFailed is returned when a child OS process cannot be launched.
type ProcessStartFailed struct {
	Reason string
}

func (e ProcessStartFailed) Error() string {
	return "process.StartFailed{Reason:" + e.Reason + "}"
}

// ChildLine is one line read from a child process's standard output.
type ChildLine struct {
	Text string
	Open bool
}

// ChildStatus is the exit summary of a finished child OS process.
type ChildStatus struct {
	Pid        int
	Status     int
	Signal     int
	Stderr     string
	KillFailed bool
}

// ChildProc owns a spawned child OS process, its stdio pipes, and its reaping.
type ChildProc struct {
	cmd        *exec.Cmd
	pgid       int
	grace      time.Duration
	stdinW     *os.File
	stdoutR    *os.File
	stderrR    *os.File
	stdinMu    sync.Mutex
	stdinDown  bool
	lines      chan ChildLine
	stopRead   chan struct{}
	stopOnce   sync.Once
	exited     chan struct{}
	stdoutDone chan struct{}
	stderrDone chan struct{}
	closeOnce  sync.Once
	killFailed atomic.Bool
	after      func(time.Duration) (<-chan time.Time, func() bool)

	mu     sync.Mutex
	status int
	signal int
	stderr string
}

// runtimeOnly implements ChildProc's private operation.
func (*ChildProc) runtimeOnly() {}

// ProcessStart spawns name with args in dir with environment env, optional
// memory bound limitBytes, and SIGTERM-to-SIGKILL grace period grace.
//
//ergo:host
//ergo:wait caller
func ProcessStart(t *rt.Task, name string, args []string, env map[string]string, dir string, limitBytes int, grace time.Duration) (*ChildProc, error) {
	t.Check()
	if name == "" {
		return nil, ProcessStartFailed{Reason: "empty binary"}
	}
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			reason := "not a directory"
			if err != nil {
				reason = err.Error()
			}
			return nil, ProcessStartFailed{Reason: "dir " + dir + ": " + reason}
		}
	}
	resolved, err := processLookPath(name, env["PATH"], dir)
	if err != nil {
		return nil, err
	}
	envSlice, err := buildChildEnv(env, resolved, limitBytes)
	if err != nil {
		return nil, err
	}
	cmdName := resolved
	if limitBytes > 0 {
		exe, err := processWrapperExecutable()
		if err != nil {
			return nil, ProcessStartFailed{Reason: err.Error()}
		}
		cmdName = exe
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, ProcessStartFailed{Reason: err.Error()}
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, ProcessStartFailed{Reason: err.Error()}
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, ProcessStartFailed{Reason: err.Error()}
	}

	cmd := &exec.Cmd{
		Path:        cmdName,
		Args:        append([]string{cmdName}, args...),
		Env:         envSlice,
		Dir:         dir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
		SysProcAttr: processGroupAttr(),
	}
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		return nil, ProcessStartFailed{Reason: err.Error()}
	}
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	p := &ChildProc{
		cmd:        cmd,
		pgid:       cmd.Process.Pid,
		grace:      grace,
		stdinW:     stdinW,
		stdoutR:    stdoutR,
		stderrR:    stderrR,
		lines:      make(chan ChildLine, 16),
		stopRead:   make(chan struct{}),
		exited:     make(chan struct{}),
		stdoutDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
		after: func(d time.Duration) (<-chan time.Time, func() bool) {
			timer := time.NewTimer(d)
			return timer.C, timer.Stop
		},
	}
	go p.readStderr()
	go p.readStdout()
	go p.awaitExit()
	return p, nil
}

func processLookPath(name, pathEnv, dir string) (string, error) {
	if strings.Contains(name, "/") {
		checkPath := name
		if dir != "" && !strings.HasPrefix(name, "/") {
			checkPath = dir + "/" + name
		}
		fi, err := os.Stat(checkPath)
		if err != nil {
			return "", ProcessStartFailed{Reason: err.Error()}
		}
		if fi.IsDir() {
			return "", ProcessStartFailed{Reason: name + " is a directory"}
		}
		if syscall.Access(checkPath, 0x1) != nil {
			return "", ProcessStartFailed{Reason: name + ": permission denied"}
		}
		return name, nil
	}
	denied := false
	for _, entry := range strings.Split(pathEnv, ":") {
		if entry == "" {
			continue
		}
		file := entry + "/" + name
		fi, err := os.Stat(file)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if syscall.Access(file, 0x1) != nil {
			denied = true
			continue
		}
		return file, nil
	}
	if denied {
		return "", ProcessStartFailed{Reason: name + ": permission denied"}
	}
	return "", ProcessStartFailed{Reason: name + ": executable not found"}
}

func buildChildEnv(env map[string]string, target string, limitBytes int) ([]string, error) {
	merged := make(map[string]string, len(env)+4)
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.IndexByte(v, 0) >= 0 {
			return nil, ProcessStartFailed{Reason: "invalid environment entry"}
		}
		merged[k] = v
	}
	delete(merged, processWrapEnv)
	delete(merged, processTargetEnv)
	delete(merged, processBytesEnv)
	if limitBytes > 0 {
		b := strconv.FormatInt(int64(limitBytes), 10)
		merged["GOMEMLIMIT"] = b + "B"
		merged[processBytesEnv] = b
		merged[processWrapEnv] = "1"
		merged[processTargetEnv] = target
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out, nil
}

func (p *ChildProc) readStdout() {
	defer close(p.stdoutDone)
	defer close(p.lines)
	r := bufio.NewReaderSize(p.stdoutR, 4096)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			select {
			case p.lines <- ChildLine{Text: line, Open: true}:
			case <-p.stopRead:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *ChildProc) readStderr() {
	defer close(p.stderrDone)
	var head bytes.Buffer
	var tail []byte
	buf := make([]byte, 4096)
	const half = childStderrLimit / 2
	for {
		n, err := p.stderrR.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if head.Len() < half {
				take := min(len(chunk), half-head.Len())
				head.Write(chunk[:take])
				chunk = chunk[take:]
			}
			if len(chunk) > 0 {
				if len(chunk) >= half {
					tail = append(tail[:0], chunk[len(chunk)-half:]...)
				} else {
					total := len(tail) + len(chunk)
					if total > half {
						drop := total - half
						copy(tail, tail[drop:])
						tail = tail[:len(tail)-drop]
					}
					tail = append(tail, chunk...)
				}
			}
		}
		if err != nil {
			break
		}
	}
	var text string
	if len(tail) > 0 {
		text = head.String() + "\n... [stderr truncated] ...\n" + string(tail)
	} else {
		text = head.String()
	}
	p.mu.Lock()
	p.stderr = text
	p.mu.Unlock()
}

func (p *ChildProc) awaitExit() {
	defer close(p.exited)
	status, sig := p.reap()
	p.mu.Lock()
	p.status = status
	p.signal = sig
	p.mu.Unlock()
}

func (p *ChildProc) reap() (int, int) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := int(ws.Signal())
			return 128 + sig, sig
		}
		return ee.ExitCode(), 0
	}
	return 1, 0
}

// WriteLine writes one line terminated by a newline to the child's stdin.
//
//ergo:wait caller
func (p *ChildProc) WriteLine(t *rt.Task, line string) bool {
	t.Check()
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdinDown || p.stdinW == nil {
		return false
	}
	_, err := io.WriteString(p.stdinW, line+"\n")
	return err == nil
}

// CloseStdin closes the parent's write end of the child's stdin pipe.
//
//ergo:wait none
func (p *ChildProc) CloseStdin() {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if !p.stdinDown && p.stdinW != nil {
		p.stdinDown = true
		_ = p.stdinW.Close()
	}
}

// ReadLine waits for the next line from the child's stdout or EOF.
//
//ergo:wait caller
func (p *ChildProc) ReadLine(t *rt.Task) ChildLine {
	t.Check()
	select {
	case item, ok := <-p.lines:
		if !ok {
			return ChildLine{Open: false}
		}
		return item
	case <-t.Done():
		t.Check()
		return ChildLine{Open: false}
	}
}

// ReadLineWithin waits up to d for the next stdout line from p.
//
//ergo:wait none
func (p *ChildProc) ReadLineWithin(d time.Duration) ChildLine {
	if d <= 0 {
		select {
		case item, ok := <-p.lines:
			if !ok {
				return ChildLine{Open: false}
			}
			return item
		default:
			return ChildLine{Open: false}
		}
	}
	if d > time.Second {
		d = time.Second
	}
	ch, stop := p.after(d)
	defer stop()
	select {
	case item, ok := <-p.lines:
		if !ok {
			return ChildLine{Open: false}
		}
		return item
	case <-ch:
		return ChildLine{Open: false}
	}
}

// Kill forcibly terminates the child's process group with SIGKILL and reaps it.
//
//ergo:wait caller
func (p *ChildProc) Kill(t *rt.Task) {
	t.Check()
	p.stopAndReap(true)
}

// Wait waits for the child process to exit and be reaped, returning its summary.
//
//ergo:wait caller
func (p *ChildProc) Wait(t *rt.Task) ChildStatus {
	t.Check()
	if !p.killFailed.Load() {
		select {
		case <-p.exited:
		case <-t.Done():
			t.Check()
		}
	}
	if p.killFailed.Load() {
		p.stopOnce.Do(func() { close(p.stopRead) })
		_ = p.stdoutR.Close()
		_ = p.stderrR.Close()
	} else {
		p.drainStreams()
	}
	p.mu.Lock()
	st := ChildStatus{
		Pid:        p.pgid,
		Status:     p.status,
		Signal:     p.signal,
		Stderr:     p.stderr,
		KillFailed: p.killFailed.Load(),
	}
	p.mu.Unlock()
	return st
}

// Close releases p: if the child is still running, it closes stdin, optionally
// sends SIGTERM for Grace, then sends SIGKILL to the process group and reaps it.
//
//ergo:wait none
//ergo:release caller
func (p *ChildProc) Close(ct *rt.Task) {
	Release(ct, func() {
		p.stopAndReap(false)
	})
}

func (p *ChildProc) stopAndReap(forceImmediate bool) {
	p.closeOnce.Do(func() {
		p.CloseStdin()
		alreadyDead := false
		select {
		case <-p.exited:
			alreadyDead = true
		default:
		}
		if !alreadyDead {
			if !forceImmediate && p.grace > 0 {
				_ = signalProcessGroup(p.pgid, syscall.SIGTERM)
				t := time.NewTimer(p.grace)
				select {
				case <-p.exited:
					t.Stop()
					alreadyDead = true
				case <-t.C:
				}
			}
			if !alreadyDead {
				_ = signalProcessGroup(p.pgid, syscall.SIGKILL)
				t := time.NewTimer(childKillTimeout)
				select {
				case <-p.exited:
					t.Stop()
				case <-t.C:
					p.killFailed.Store(true)
					return
				}
			}
		}
		_ = signalProcessGroup(p.pgid, syscall.SIGKILL)
		awaitProcessGroupEnd(p.pgid)
		p.drainStreams()
	})
}

func (p *ChildProc) drainStreams() {
	timer := time.NewTimer(childDrainDelay)
	defer timer.Stop()
	select {
	case <-p.stderrDone:
	case <-timer.C:
		_ = p.stderrR.Close()
		<-p.stderrDone
	}
	p.stopOnce.Do(func() { close(p.stopRead) })
	timer2 := time.NewTimer(childDrainDelay)
	defer timer2.Stop()
	select {
	case <-p.stdoutDone:
	case <-timer2.C:
		_ = p.stdoutR.Close()
		<-p.stdoutDone
	}
	_ = p.stdoutR.Close()
	_ = p.stderrR.Close()
}

func awaitProcessGroupEnd(pgid int) {
	if pgid <= 0 {
		return
	}
	deadline := time.Now().Add(childGroupWait)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(childGroupPoll)
	}
}
