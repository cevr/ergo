package exec

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// ExecUnsupported is exec.Unsupported: an operation (PTY allocation, FD
// reattachment, or cross-process wait) is not supported on this OS or
// provider.
type ExecUnsupported struct {
	Reason string
}

func (e ExecUnsupported) Error() string {
	if e.Reason == "" {
		return "exec: operation unsupported on this OS"
	}
	return "exec: " + e.Reason
}

// ExecWinsize is exec.Winsize: terminal dimensions in character cells
// and pixels.
type ExecWinsize struct {
	Rows    int
	Cols    int
	XPixels int
	YPixels int
}

// ExecValidWinsize reports whether ws holds valid kernel winsize bounds.
func ExecValidWinsize(ws ExecWinsize) bool {
	return ws.Rows > 0 && ws.Cols > 0 &&
		ws.Rows <= 65535 && ws.Cols <= 65535 &&
		ws.XPixels >= 0 && ws.YPixels >= 0 &&
		ws.XPixels <= 65535 && ws.YPixels <= 65535
}

// PTY is exec.PTY: an owned pseudo-terminal master descriptor allocated
// by OpenPTY or adopted across server replacement by ReattachPTY.
type PTY struct {
	mu        sync.Mutex
	readMu    sync.Mutex
	closed    bool
	master    *os.File
	slave     *os.File
	slavePath string
	wakeOnce  sync.Once
	closeCh   chan struct{}
	mem       *MemSessions
	memFD     int
}

// runtimeOnly implements exec.PTY's private operation (runtime/v1/sealed.go).
func (*PTY) runtimeOnly() {}

// Close releases p and any open slave handle. A second Close does nothing.
//
//ergo:wait none
func (p *PTY) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	master := p.master
	slave := p.slave
	mem := p.mem
	memFD := p.memFD
	p.master = nil
	p.slave = nil
	p.slavePath = ""
	p.mem = nil
	p.memFD = -1
	p.wakeOnce.Do(func() {
		if p.closeCh != nil {
			close(p.closeCh)
		}
	})
	p.mu.Unlock()

	if mem != nil {
		mem.closeFD(memFD)
		return
	}
	if slave != nil {
		_ = slave.Close()
	}
	if master != nil {
		_ = master.Close()
	}
}

// CloseSlave closes the parent's slave terminal handle if still held open
// while keeping the master descriptor open.
//
//ergo:wait none
func (p *PTY) CloseSlave() {
	if p == nil {
		return
	}
	p.mu.Lock()
	slave := p.slave
	p.slave = nil
	p.mu.Unlock()
	if slave != nil {
		_ = slave.Close()
	}
}

// Fd returns p's master file descriptor number, or -1 if closed.
//
//ergo:wait none
func (p *PTY) Fd() int {
	if p == nil {
		return -1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return -1
	}
	if p.mem != nil {
		return p.memFD
	}
	if p.master != nil {
		return osFileFd(p.master)
	}
	return -1
}

// DupFD duplicates p's master descriptor with close-on-exec set and
// returns the raw descriptor number.
//
//ergo:host
//ergo:wait none
func (p *PTY) DupFD() (int, error) {
	if p == nil {
		return -1, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return -1, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	if p.mem != nil {
		return p.mem.dupPTYFD(p.memFD)
	}
	if p.master == nil {
		return -1, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	return osDupFileRaw(p.master, "pty")
}

// SlavePath returns the slave terminal device path if known, or "".
//
//ergo:wait none
func (p *PTY) SlavePath() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ""
	}
	return p.slavePath
}

// Size reads the current window size of p (TIOCGWINSZ).
//
//ergo:wait none
func (p *PTY) Size() (ExecWinsize, error) {
	if p == nil {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	master := p.master
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return ExecWinsize{}, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	if mem != nil {
		return mem.ptySize(memFD)
	}
	return ptyGetSizeOS(master)
}

// Resize sets the terminal window size of p (TIOCSWINSZ).
//
//ergo:wait none
func (p *PTY) Resize(ws ExecWinsize) error {
	if !ExecValidWinsize(ws) {
		return ExecFailed{Name: "pty", Reason: "invalid terminal window size"}
	}
	if p == nil {
		return ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	master := p.master
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	if mem != nil {
		return mem.ptyResize(memFD, ws)
	}
	return ptySetSizeOS(master, ws)
}

// Read reads available bytes from p's master descriptor, returning "" on
// EOF when the slave closes. A stop of t interrupts the wait.
//
//ergo:wait caller
func (p *PTY) Read(t *rt.Task) (string, error) {
	t.Check()
	if p == nil {
		return "", ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	p.readMu.Lock()
	defer p.readMu.Unlock()

	p.mu.Lock()
	closed := p.closed
	master := p.master
	closeCh := p.closeCh
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return "", ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	if mem != nil {
		return mem.ptyRead(t, memFD, closeCh)
	}
	return readFileInterruptible(t, master, closeCh, "pty", true)
}

// Write writes data completely to p's master descriptor.
//
//ergo:wait none
func (p *PTY) Write(data string) error {
	if p == nil {
		return ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	master := p.master
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	if mem != nil {
		return mem.ptyWrite(memFD, data)
	}
	return writeAllFile(master, "pty", data)
}

// Pipe is exec.Pipe: an owned stream file descriptor (a child's stdin,
// stdout, stderr, or a reattached pipe end).
type Pipe struct {
	mu       sync.Mutex
	readMu   sync.Mutex
	closed   bool
	file     *os.File
	wakeOnce sync.Once
	closeCh  chan struct{}
	mem      *MemSessions
	memFD    int
}

// runtimeOnly implements exec.Pipe's private operation (runtime/v1/sealed.go).
func (*Pipe) runtimeOnly() {}

// Close releases p. A second Close does nothing.
//
//ergo:wait none
func (p *Pipe) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	file := p.file
	mem := p.mem
	memFD := p.memFD
	p.file = nil
	p.mem = nil
	p.memFD = -1
	p.wakeOnce.Do(func() {
		if p.closeCh != nil {
			close(p.closeCh)
		}
	})
	p.mu.Unlock()

	if mem != nil {
		mem.closeFD(memFD)
		return
	}
	if file != nil {
		_ = file.Close()
	}
}

// Fd returns p's underlying descriptor number, or -1 if closed.
//
//ergo:wait none
func (p *Pipe) Fd() int {
	if p == nil {
		return -1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return -1
	}
	if p.mem != nil {
		return p.memFD
	}
	if p.file != nil {
		return osFileFd(p.file)
	}
	return -1
}

// DupFD duplicates p's descriptor with close-on-exec set and returns the
// raw descriptor number.
//
//ergo:host
//ergo:wait none
func (p *Pipe) DupFD() (int, error) {
	if p == nil {
		return -1, ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return -1, ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	if p.mem != nil {
		return p.mem.dupPipeFD(p.memFD)
	}
	if p.file == nil {
		return -1, ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	return osDupFileRaw(p.file, "pipe")
}

// Read reads available bytes from p, returning "" on EOF. A stop of t
// interrupts the wait.
//
//ergo:wait caller
func (p *Pipe) Read(t *rt.Task) (string, error) {
	t.Check()
	if p == nil {
		return "", ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	p.readMu.Lock()
	defer p.readMu.Unlock()

	p.mu.Lock()
	closed := p.closed
	file := p.file
	closeCh := p.closeCh
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return "", ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	if mem != nil {
		return mem.pipeRead(t, memFD, closeCh)
	}
	return readFileInterruptible(t, file, closeCh, "pipe", false)
}

// Write writes data completely to p.
//
//ergo:wait none
func (p *Pipe) Write(data string) error {
	if p == nil {
		return ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	file := p.file
	mem := p.mem
	memFD := p.memFD
	p.mu.Unlock()
	if closed {
		return ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	if mem != nil {
		return mem.pipeWrite(memFD, data)
	}
	return writeAllFile(file, "pipe", data)
}

func newOSPipe(f *os.File) *Pipe {
	return &Pipe{file: f, closeCh: make(chan struct{})}
}

// DetachedRun is exec.DetachedRun: the durable detached identity and
// any extracted stdio pipe descriptors yielded by Running.Detach.
type DetachedRun struct {
	mu          sync.Mutex
	closed      bool
	info        ExecDetached
	bufferedOut string
	bufferedErr string
	stdin       *Pipe
	stdout      *Pipe
	stderr      *Pipe
}

// runtimeOnly implements exec.DetachedRun's private operation (runtime/v1/sealed.go).
func (*DetachedRun) runtimeOnly() {}

// Close releases any untaken pipe handles without affecting the detached
// process.
//
//ergo:wait none
func (d *DetachedRun) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	in := d.stdin
	out := d.stdout
	errP := d.stderr
	d.stdin = nil
	d.stdout = nil
	d.stderr = nil
	d.mu.Unlock()

	if in != nil {
		in.Close()
	}
	if out != nil {
		out.Close()
	}
	if errP != nil {
		errP.Close()
	}
}

// Info returns the durable Detached handle for the detached process.
//
//ergo:wait none
func (d *DetachedRun) Info() ExecDetached {
	if d == nil {
		return ExecDetached{}
	}
	return d.info
}

// BufferedStdout returns stdout bytes buffered by Running prior to Detach.
//
//ergo:wait none
func (d *DetachedRun) BufferedStdout() string {
	if d == nil {
		return ""
	}
	return d.bufferedOut
}

// BufferedStderr returns stderr bytes buffered by Running prior to Detach.
//
//ergo:wait none
func (d *DetachedRun) BufferedStderr() string {
	if d == nil {
		return ""
	}
	return d.bufferedErr
}

// TakeStdin extracts the interactive stdin Pipe if present and untaken.
//
//ergo:wait none
func (d *DetachedRun) TakeStdin() (*Pipe, bool) {
	if d == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.stdin == nil {
		return nil, false
	}
	p := d.stdin
	d.stdin = nil
	return p, true
}

// TakeStdout extracts the ToCaller stdout Pipe if present and untaken.
//
//ergo:wait none
func (d *DetachedRun) TakeStdout() (*Pipe, bool) {
	if d == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.stdout == nil {
		return nil, false
	}
	p := d.stdout
	d.stdout = nil
	return p, true
}

// TakeStderr extracts the ToCaller stderr Pipe if present and untaken.
//
//ergo:wait none
func (d *DetachedRun) TakeStderr() (*Pipe, bool) {
	if d == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.stderr == nil {
		return nil, false
	}
	p := d.stderr
	d.stderr = nil
	return p, true
}

// IntoDetached closes any untaken pipes and returns the plain Detached.
//
//ergo:wait none
func (d *DetachedRun) IntoDetached() ExecDetached {
	if d == nil {
		return ExecDetached{}
	}
	info := d.info
	d.Close()
	return info
}

// Detach transitions a live supervised Running into a DetachedRun,
// preserving live stdio pipe descriptors and buffered bytes without
// signaling or terminating the process.
//
//ergo:wait none
func (r *Running) Detach() (*DetachedRun, error) {
	if r == nil {
		return nil, ExecFailed{Reason: "cannot detach nil Running"}
	}
	if r.mem != nil {
		return r.mem.detachRunning(r)
	}
	r.detachMu.Lock()
	if r.confined {
		r.detachMu.Unlock()
		return nil, ExecFailed{Name: r.name, Reason: "a test's host detaches no program"}
	}
	if r.detached.Load() || r.finished {
		r.detachMu.Unlock()
		return nil, ExecFailed{Name: r.name, Reason: "process has already finished or detached"}
	}
	hp, ok := r.proc.(*hostProc)
	if !ok {
		r.detachMu.Unlock()
		return nil, ExecFailed{Name: r.name, Reason: "cannot detach an ended process"}
	}
	select {
	case <-r.proc.exited():
		r.detachMu.Unlock()
		return nil, ExecFailed{Name: r.name, Reason: "process has already exited"}
	default:
	}
	child, ok := hp.detach()
	if !ok {
		r.detachMu.Unlock()
		return nil, ExecFailed{Name: r.name, Reason: "process has already finished or detached"}
	}
	r.detached.Store(true)
	r.finished = true
	r.stdinMu.Lock()
	stdinF := r.stdinPipe
	r.stdinPipe = nil
	r.stdinMu.Unlock()
	outF, errF := r.readFiles[0], r.readFiles[1]
	r.readFiles = [2]*os.File{}
	sinkFiles := r.files
	r.files = nil
	r.pipes = nil
	r.detachMu.Unlock()

	for _, f := range []*os.File{outF, errF} {
		if f != nil {
			_ = f.SetReadDeadline(time.Now().Add(-time.Second))
		}
	}
	close(r.drop)
	<-r.ended
	for _, f := range []*os.File{outF, errF} {
		if f != nil {
			_ = f.SetReadDeadline(time.Time{})
		}
	}
	for _, f := range sinkFiles {
		if f != nil {
			_ = f.Close()
		}
	}

	var bufOut, bufErr strings.Builder
	appendChunk := func(c ExecChunk) {
		if c.Text == "" {
			return
		}
		if c.Stderr {
			bufErr.WriteString(c.Text)
		} else {
			bufOut.WriteString(c.Text)
		}
	}
	r.mu.Lock()
	for _, c := range r.back {
		appendChunk(c)
	}
	r.back = nil
	r.mu.Unlock()
	if r.chunks != nil {
		for c := range r.chunks {
			appendChunk(c)
		}
	}
	r.mu.Lock()
	for _, c := range r.detachTail {
		appendChunk(c)
	}
	r.detachTail = nil
	r.mu.Unlock()

	var stdinP, stdoutP, stderrP *Pipe
	if stdinF != nil {
		stdinP = newOSPipe(stdinF)
	}
	if outF != nil {
		stdoutP = newOSPipe(outF)
	}
	if errF != nil {
		stderrP = newOSPipe(errF)
	}

	reg := registerDetachedChild(r.pid, r.start)
	go func() {
		status, sig := child.reapState()
		reg.complete(status, sig)
	}()

	return &DetachedRun{
		info:        ExecDetached{Pid: r.pid, Start: r.start, Name: r.name},
		bufferedOut: bufOut.String(),
		bufferedErr: bufErr.String(),
		stdin:       stdinP,
		stdout:      stdoutP,
		stderr:      stderrP,
	}, nil
}

// detachedRegistry caches exit statuses of direct child processes
// started by Spawn, SpawnWithPTY, or Detach in this process, so
// PollDetached and WaitDetached return exact exit code and signal while
// still reaping promptly so no zombie leaks.
const maxCompletedDetached = 4096

type detachedKey struct {
	pid   int
	start int64
}

type detachedEntry struct {
	key    detachedKey
	done   chan struct{}
	mu     sync.Mutex
	exited bool
	status int
	signal int
}

var detachedRegistry = struct {
	mu      sync.Mutex
	entries map[detachedKey]*detachedEntry
	doneSeq []detachedKey
}{
	entries: map[detachedKey]*detachedEntry{},
}

func registerDetachedChild(pid int, start int64) *detachedEntry {
	key := detachedKey{pid: pid, start: start}
	e := &detachedEntry{key: key, done: make(chan struct{})}
	detachedRegistry.mu.Lock()
	detachedRegistry.entries[key] = e
	detachedRegistry.mu.Unlock()
	return e
}

func recordCompletedChild(pid int, start int64, status, signal int) {
	key := detachedKey{pid: pid, start: start}
	e := &detachedEntry{key: key, done: make(chan struct{}), exited: true, status: status, signal: signal}
	close(e.done)
	detachedRegistry.mu.Lock()
	detachedRegistry.entries[key] = e
	detachedRegistry.doneSeq = append(detachedRegistry.doneSeq, key)
	pruneDetachedLocked()
	detachedRegistry.mu.Unlock()
}

func (e *detachedEntry) complete(status, signal int) {
	e.mu.Lock()
	if e.exited {
		e.mu.Unlock()
		return
	}
	e.exited = true
	e.status = status
	e.signal = signal
	close(e.done)
	e.mu.Unlock()

	detachedRegistry.mu.Lock()
	detachedRegistry.doneSeq = append(detachedRegistry.doneSeq, e.key)
	pruneDetachedLocked()
	detachedRegistry.mu.Unlock()
}

func pruneDetachedLocked() {
	for len(detachedRegistry.doneSeq) > maxCompletedDetached {
		old := detachedRegistry.doneSeq[0]
		detachedRegistry.doneSeq = detachedRegistry.doneSeq[1:]
		if cur, ok := detachedRegistry.entries[old]; ok {
			cur.mu.Lock()
			done := cur.exited
			cur.mu.Unlock()
			if done && cur.key == old {
				delete(detachedRegistry.entries, old)
			}
		}
	}
}

func lookupDetachedEntry(pid int, start int64) *detachedEntry {
	detachedRegistry.mu.Lock()
	defer detachedRegistry.mu.Unlock()
	return detachedRegistry.entries[detachedKey{pid: pid, start: start}]
}

func writeAllFile(f *os.File, name string, data string) error {
	b := []byte(data)
	for len(b) > 0 {
		n, err := f.Write(b)
		b = b[n:]
		if err != nil {
			if errors.Is(err, io.ErrShortWrite) && n > 0 {
				continue
			}
			return ExecFailed{Name: name, Reason: fsReason(err)}
		}
		if n == 0 {
			return ExecFailed{Name: name, Reason: "short write"}
		}
	}
	return nil
}

// Detached exit observation outcome kinds (mapped to ExitOutcome in std/exec).
const (
	execOutcomeRunning     = 0
	execOutcomeExited      = 1
	execOutcomeUnavailable = 2
)

// ExecOpenPTY allocates a host pseudo-terminal pair.
//
//ergo:host
//ergo:wait none
func ExecOpenPTY() (*PTY, error) {
	return execOpenPTYOS()
}

// ExecStartWithPTY starts name attached to pty's slave terminal as its
// controlling terminal in a new session, closing parent's slave handle.
//
//ergo:host
//ergo:wait caller
func ExecStartWithPTY(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration, p *PTY) (*Running, error) {
	return execStartWithPTYOS(t, name, args, env, dir, stdin, grace, p)
}

// ExecSpawnWithPTY starts name detached on pty's slave terminal in a new
// session and returns its Detached handle.
//
//ergo:host
//ergo:wait caller
func ExecSpawnWithPTY(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, p *PTY) (ExecDetached, error) {
	return execSpawnWithPTYOS(t, name, args, env, dir, stdin, p)
}

// ExecOpenPipe allocates a host OS pipe pair (read end, write end).
//
//ergo:host
//ergo:wait none
func ExecOpenPipe() (*Pipe, *Pipe, error) {
	return execOpenPipeOS()
}

// ExecReattachPTY duplicates raw fd with close-on-exec, validates it is
// an open terminal, and wraps it in a PTY.
//
//ergo:host
//ergo:wait none
func ExecReattachPTY(fd int) (*PTY, error) {
	return execReattachPTYOS(fd)
}

// ExecReattachPipe duplicates raw fd with close-on-exec, validates it is
// an open descriptor, and wraps it in a Pipe.
//
//ergo:host
//ergo:wait none
func ExecReattachPipe(fd int) (*Pipe, error) {
	return execReattachPipeOS(fd)
}

// ExecPollDetached inspects the status of detached pid with start time
// start without blocking.
//
//ergo:host
//ergo:wait none
func ExecPollDetached(pid int, start int64) (int, int, int, bool, string, error) {
	return execPollDetachedOS(pid, start)
}

// ExecWaitDetached waits on t until the detached process exits or becomes
// unavailable, returning its ExitOutcome components.
//
//ergo:host
//ergo:wait caller
func ExecWaitDetached(t *rt.Task, pid int, start int64) (int, int, int, bool, string, error) {
	return execWaitDetachedOS(t, pid, start)
}
