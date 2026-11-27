package exec

import (
	"slices"
	"sync"
	"syscall"

	"ergo.local/prototype/runtime/rt"
)

// MemSessions is the in-memory provider of exec.Sessions and exec.Spawner
// for deterministic check-time/native laws and tests. It allocates virtual
// PTYs, pipes, and simulated child sessions with zero OS side effects.
type MemSessions struct {
	mu       sync.Mutex
	programs []string
	nextFD   int
	nextPid  int
	fds      map[int]*memFDEntry
	procs    map[int]*memProcEntry
}

// runtimeOnly implements exec.memSessions's private operation.
func (*MemSessions) runtimeOnly() {}

type memFDKind int

const (
	memFDPTY memFDKind = iota + 1
	memFDPipeRead
	memFDPipeWrite
	memFDPipeDuplex
)

type memFDEntry struct {
	kind   memFDKind
	closed bool
	pty    *memPTYState
	pipe   *memPipeState
}

type memPTYState struct {
	mu      sync.Mutex
	ws      ExecWinsize
	buf     []byte
	echo    bool
	refs    int
	eof     bool
	notify  chan struct{}
	waiters []*rt.Task
	slaveOf *memProcEntry
}

func wakeTasksAndClose(waiters *[]*rt.Task, target any, notify *chan struct{}) {
	ws := *waiters
	*waiters = nil
	ch := *notify
	*notify = make(chan struct{})
	var wakers []rt.Waker
	for _, w := range ws {
		if w != nil && w.InDomain() {
			wakers = append(wakers, rt.Waking(w, target))
		}
	}
	close(ch)
	for _, k := range wakers {
		k.Done()
	}
}

func removeMemWaiter(waiters *[]*rt.Task, t *rt.Task) {
	ws := *waiters
	for i, w := range ws {
		if w == t {
			ws[i] = ws[len(ws)-1]
			ws[len(ws)-1] = nil
			*waiters = ws[:len(ws)-1]
			return
		}
	}
}

func newMemPTYState() *memPTYState {
	return &memPTYState{
		ws:     ExecWinsize{Rows: 24, Cols: 80},
		refs:   1,
		notify: make(chan struct{}),
	}
}

func (s *memPTYState) wakeLocked() {
	wakeTasksAndClose(&s.waiters, s, &s.notify)
}

type memPipeState struct {
	mu        sync.Mutex
	buf       []byte
	readRefs  int
	writeRefs int
	notify    chan struct{}
	waiters   []*rt.Task
	onWrite   func(string)
	onEOF     func()
}

func newMemPipeState(readRefs, writeRefs int) *memPipeState {
	return &memPipeState{
		readRefs:  readRefs,
		writeRefs: writeRefs,
		notify:    make(chan struct{}),
	}
}

func (s *memPipeState) wakeLocked() {
	wakeTasksAndClose(&s.waiters, s, &s.notify)
}

type memProcEntry struct {
	mu       sync.Mutex
	pid      int
	start    int64
	name     string
	alive    bool
	exited   bool
	notChild bool
	status   int
	signal   int
	done     chan struct{}
	notify   chan struct{}
	waiters  []*rt.Task
	pty      *memPTYState
	inPipe   *memPipeState
	outPipe  *memPipeState
	errPipe  *memPipeState
	onExit   func()
}

func (p *memProcEntry) wakeLocked() {
	wakeTasksAndClose(&p.waiters, p, &p.notify)
}

type memRunning struct {
	host       *MemSessions
	proc       *memProcEntry
	outPipe    *memPipeState
	errPipe    *memPipeState
	inPipe     *memPipeState
	captureOut bool
	captureErr bool
	mergeErr   bool
	r          *Running
	mu         sync.Mutex
	endOnce    sync.Once
	bufOut     string
	bufErr     string
	inClosed   bool
}

func (mr *memRunning) finishStreams() {
	mr.endOnce.Do(func() {
		if mr.r.chunks == nil {
			return
		}
		if mr.r.detached.Load() {
			close(mr.r.chunks)
			close(mr.r.ended)
			return
		}
		go func() {
			if mr.captureOut {
				select {
				case mr.r.chunks <- ExecChunk{Stderr: false}:
				case <-mr.r.drop:
				}
			}
			if mr.captureErr {
				select {
				case mr.r.chunks <- ExecChunk{Stderr: true}:
				case <-mr.r.drop:
				}
			}
			close(mr.r.chunks)
			close(mr.r.ended)
		}()
	})
}

type memExecProc struct {
	entry *memProcEntry
	done  chan struct{}
}

func (p *memExecProc) exited() <-chan struct{} { return p.done }

func (p *memExecProc) signal(sig syscall.Signal) {}

func (p *memExecProc) reap() int {
	p.entry.mu.Lock()
	defer p.entry.mu.Unlock()
	if !p.entry.exited {
		p.entry.alive = false
		p.entry.exited = true
		p.entry.status = 137
		p.entry.signal = 9
		close(p.entry.done)
		p.entry.wakeLocked()
	}
	return p.entry.status
}

// ExecNewMemSessions returns an in-memory Sessions/Spawner provider whose
// Spawn/Start recognize programs.
//
//ergo:wait none
func ExecNewMemSessions(programs []string) *MemSessions {
	return &MemSessions{
		programs: slices.Clone(programs),
		nextFD:   100,
		nextPid:  1000,
		fds:      map[int]*memFDEntry{},
		procs:    map[int]*memProcEntry{},
	}
}

func (m *MemSessions) hasProgram(name string) bool {
	if name == "" {
		return false
	}
	return slices.Contains(m.programs, name)
}

func (m *MemSessions) allocFDLocked(e *memFDEntry) int {
	fd := m.nextFD
	m.nextFD++
	m.fds[fd] = e
	return fd
}

// OpenPTY allocates a virtual PTY in m.
//
//ergo:wait none
func (m *MemSessions) OpenPTY() (*PTY, error) {
	if m == nil {
		return nil, ExecFailed{Name: "pty", Reason: "nil memory sessions"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := newMemPTYState()
	fd := m.allocFDLocked(&memFDEntry{kind: memFDPTY, pty: st})
	return &PTY{
		slavePath: "/dev/mempty/" + itoaSmall(fd),
		closeCh:   make(chan struct{}),
		mem:       m,
		memFD:     fd,
	}, nil
}

// OpenPipe allocates a connected virtual pipe pair (read end, write end).
//
//ergo:wait none
func (m *MemSessions) OpenPipe() (*Pipe, *Pipe, error) {
	if m == nil {
		return nil, nil, ExecFailed{Name: "pipe", Reason: "nil memory sessions"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := newMemPipeState(1, 1)
	rfd := m.allocFDLocked(&memFDEntry{kind: memFDPipeRead, pipe: st})
	wfd := m.allocFDLocked(&memFDEntry{kind: memFDPipeWrite, pipe: st})
	return &Pipe{closeCh: make(chan struct{}), mem: m, memFD: rfd},
		&Pipe{closeCh: make(chan struct{}), mem: m, memFD: wfd}, nil
}

// ReattachPTY duplicates virtual PTY descriptor fd.
//
//ergo:wait none
func (m *MemSessions) ReattachPTY(fd int) (*PTY, error) {
	if m == nil {
		return nil, ExecFailed{Name: "pty", Reason: "nil memory sessions"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind != memFDPTY || e.pty == nil {
		return nil, ExecFailed{Name: "pty", Reason: "invalid or closed PTY descriptor"}
	}
	e.pty.mu.Lock()
	e.pty.refs++
	e.pty.mu.Unlock()
	newFD := m.allocFDLocked(&memFDEntry{kind: memFDPTY, pty: e.pty})
	return &PTY{
		closeCh: make(chan struct{}),
		mem:     m,
		memFD:   newFD,
	}, nil
}

// ReattachPipe duplicates virtual pipe descriptor fd.
//
//ergo:wait none
func (m *MemSessions) ReattachPipe(fd int) (*Pipe, error) {
	if m == nil {
		return nil, ExecFailed{Name: "pipe", Reason: "nil memory sessions"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind == memFDPTY || e.pipe == nil {
		return nil, ExecFailed{Name: "pipe", Reason: "invalid or closed pipe descriptor"}
	}
	e.pipe.mu.Lock()
	switch e.kind {
	case memFDPipeRead:
		e.pipe.readRefs++
	case memFDPipeWrite:
		e.pipe.writeRefs++
	case memFDPipeDuplex:
		e.pipe.readRefs++
		e.pipe.writeRefs++
	}
	e.pipe.mu.Unlock()
	newFD := m.allocFDLocked(&memFDEntry{kind: e.kind, pipe: e.pipe})
	return &Pipe{
		closeCh: make(chan struct{}),
		mem:     m,
		memFD:   newFD,
	}, nil
}

func (m *MemSessions) dupPTYFD(fd int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind != memFDPTY || e.pty == nil {
		return -1, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	e.pty.mu.Lock()
	e.pty.refs++
	e.pty.mu.Unlock()
	return m.allocFDLocked(&memFDEntry{kind: memFDPTY, pty: e.pty}), nil
}

func (m *MemSessions) dupPipeFD(fd int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind == memFDPTY || e.pipe == nil {
		return -1, ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	e.pipe.mu.Lock()
	switch e.kind {
	case memFDPipeRead:
		e.pipe.readRefs++
	case memFDPipeWrite:
		e.pipe.writeRefs++
	case memFDPipeDuplex:
		e.pipe.readRefs++
		e.pipe.writeRefs++
	}
	e.pipe.mu.Unlock()
	return m.allocFDLocked(&memFDEntry{kind: e.kind, pipe: e.pipe}), nil
}

func (m *MemSessions) closeFD(fd int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	e, ok := m.fds[fd]
	if !ok || e.closed {
		m.mu.Unlock()
		return
	}
	e.closed = true
	delete(m.fds, fd)
	m.mu.Unlock()

	if e.kind == memFDPTY && e.pty != nil {
		e.pty.mu.Lock()
		e.pty.refs--
		if e.pty.refs <= 0 {
			e.pty.eof = true
		}
		e.pty.wakeLocked()
		e.pty.mu.Unlock()
		return
	}
	if e.pipe != nil {
		var triggerEOF func()
		e.pipe.mu.Lock()
		switch e.kind {
		case memFDPipeRead:
			e.pipe.readRefs--
		case memFDPipeWrite:
			e.pipe.writeRefs--
		case memFDPipeDuplex:
			e.pipe.readRefs--
			e.pipe.writeRefs--
		}
		if e.pipe.writeRefs <= 0 && e.pipe.onEOF != nil {
			triggerEOF = e.pipe.onEOF
			e.pipe.onEOF = nil
		}
		e.pipe.wakeLocked()
		e.pipe.mu.Unlock()
		if triggerEOF != nil {
			triggerEOF()
		}
	}
}

func (m *MemSessions) ptySize(fd int) (ExecWinsize, error) {
	st, err := m.getPTY(fd)
	if err != nil {
		return ExecWinsize{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ws, nil
}

func (m *MemSessions) ptyResize(fd int, ws ExecWinsize) error {
	st, err := m.getPTY(fd)
	if err != nil {
		return err
	}
	st.mu.Lock()
	st.ws = ws
	st.mu.Unlock()
	return nil
}

func (m *MemSessions) ptyWrite(fd int, data string) error {
	st, err := m.getPTY(fd)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.eof {
		return ExecFailed{Name: "pty", Reason: "PTY slave has closed"}
	}
	if st.echo || st.slaveOf == nil {
		st.buf = append(st.buf, data...)
		st.wakeLocked()
	}
	return nil
}

func (m *MemSessions) ptyRead(t *rt.Task, fd int, closeCh <-chan struct{}) (string, error) {
	st, err := m.getPTY(fd)
	if err != nil {
		return "", err
	}
	for {
		t.Check()
		select {
		case <-closeCh:
			return "", ExecFailed{Name: "pty", Reason: "PTY is closed"}
		default:
		}
		st.mu.Lock()
		if len(st.buf) > 0 {
			out := string(st.buf)
			st.buf = nil
			st.mu.Unlock()
			return out, nil
		}
		if st.eof {
			st.mu.Unlock()
			return "", nil
		}
		notify := st.notify
		var p rt.Parked
		if t.InDomain() {
			st.waiters = append(st.waiters, t)
			p = rt.Park(t, st, t.Done(), func() bool {
				return len(st.buf) > 0 || st.eof
			})
		}
		st.mu.Unlock()
		select {
		case <-notify:
		case <-closeCh:
			p.Unpark()
			st.mu.Lock()
			removeMemWaiter(&st.waiters, t)
			st.mu.Unlock()
			return "", ExecFailed{Name: "pty", Reason: "PTY is closed"}
		case <-t.Done():
			p.Unpark()
			st.mu.Lock()
			removeMemWaiter(&st.waiters, t)
			st.mu.Unlock()
			t.Check()
		}
		p.Unpark()
		st.mu.Lock()
		removeMemWaiter(&st.waiters, t)
		st.mu.Unlock()
	}
}

func (m *MemSessions) pipeWrite(fd int, data string) error {
	e, st, err := m.getPipe(fd)
	if err != nil {
		return err
	}
	if e.kind == memFDPipeRead {
		return ExecFailed{Name: "pipe", Reason: "pipe descriptor is read-only"}
	}
	st.mu.Lock()
	if st.readRefs <= 0 {
		st.mu.Unlock()
		return ExecFailed{Name: "pipe", Reason: "broken pipe"}
	}
	onWrite := st.onWrite
	if onWrite == nil {
		st.buf = append(st.buf, data...)
		st.wakeLocked()
		st.mu.Unlock()
		return nil
	}
	st.mu.Unlock()
	onWrite(data)
	return nil
}

func (m *MemSessions) pipeRead(t *rt.Task, fd int, closeCh <-chan struct{}) (string, error) {
	e, st, err := m.getPipe(fd)
	if err != nil {
		return "", err
	}
	if e.kind == memFDPipeWrite {
		return "", ExecFailed{Name: "pipe", Reason: "pipe descriptor is write-only"}
	}
	for {
		t.Check()
		select {
		case <-closeCh:
			return "", ExecFailed{Name: "pipe", Reason: "pipe is closed"}
		default:
		}
		st.mu.Lock()
		if len(st.buf) > 0 {
			out := string(st.buf)
			st.buf = nil
			st.mu.Unlock()
			return out, nil
		}
		if st.writeRefs <= 0 {
			st.mu.Unlock()
			return "", nil
		}
		notify := st.notify
		var p rt.Parked
		if t.InDomain() {
			st.waiters = append(st.waiters, t)
			p = rt.Park(t, st, t.Done(), func() bool {
				return len(st.buf) > 0 || st.writeRefs <= 0
			})
		}
		st.mu.Unlock()
		select {
		case <-notify:
		case <-closeCh:
			p.Unpark()
			if t.InDomain() {
				st.mu.Lock()
				removeMemWaiter(&st.waiters, t)
				st.mu.Unlock()
			}
			return "", ExecFailed{Name: "pipe", Reason: "pipe is closed"}
		case <-t.Done():
			p.Unpark()
			if t.InDomain() {
				st.mu.Lock()
				removeMemWaiter(&st.waiters, t)
				st.mu.Unlock()
			}
			t.Check()
		}
		p.Unpark()
		if t.InDomain() {
			st.mu.Lock()
			removeMemWaiter(&st.waiters, t)
			st.mu.Unlock()
		}
	}
}

func (m *MemSessions) getPTY(fd int) (*memPTYState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind != memFDPTY || e.pty == nil {
		return nil, ExecFailed{Name: "pty", Reason: "PTY is closed"}
	}
	return e.pty, nil
}

func (m *MemSessions) getPipe(fd int) (*memFDEntry, *memPipeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fds[fd]
	if !ok || e.closed || e.kind == memFDPTY || e.pipe == nil {
		return nil, nil, ExecFailed{Name: "pipe", Reason: "pipe is closed"}
	}
	return e, e.pipe, nil
}

func (m *MemSessions) newProcLocked(name string, pty *memPTYState, inPipe, outPipe, errPipe *memPipeState) *memProcEntry {
	pid := m.nextPid
	m.nextPid++
	e := &memProcEntry{
		pid:     pid,
		start:   int64(pid),
		name:    name,
		alive:   true,
		done:    make(chan struct{}),
		notify:  make(chan struct{}),
		pty:     pty,
		inPipe:  inPipe,
		outPipe: outPipe,
		errPipe: errPipe,
	}
	m.procs[pid] = e
	return e
}

func (m *MemSessions) completeEntry(e *memProcEntry, status, signal int) {
	var pty *memPTYState
	var outP, errP *memPipeState
	var onExit func()
	e.mu.Lock()
	if !e.exited {
		e.alive = false
		e.exited = true
		e.status = status
		e.signal = signal
		pty = e.pty
		outP = e.outPipe
		errP = e.errPipe
		onExit = e.onExit
		close(e.done)
		e.wakeLocked()
	}
	e.mu.Unlock()

	if onExit != nil {
		onExit()
	}
	if pty != nil {
		pty.mu.Lock()
		pty.eof = true
		pty.wakeLocked()
		pty.mu.Unlock()
	}
	if outP != nil {
		outP.mu.Lock()
		outP.writeRefs--
		outP.wakeLocked()
		outP.mu.Unlock()
	}
	if errP != nil {
		errP.mu.Lock()
		errP.writeRefs--
		errP.wakeLocked()
		errP.mu.Unlock()
	}
}

// Spawn creates a simulated detached process in m (matching Processes).
//
//ergo:wait none
func (m *MemSessions) Spawn(name string, outKind, errKind int) (ExecDetached, error) {
	if !m.hasProgram(name) {
		return ExecDetached{}, ExecNotFound{Name: name}
	}
	if outKind == execToCaller || errKind == execToCaller {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "a detached program has no caller to read it (ToCaller)"}
	}
	if outKind == execToStdout {
		return ExecDetached{}, execToItself(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.newProcLocked(name, nil, nil, nil, nil)
	return ExecDetached{Pid: e.pid, Start: e.start, Name: name}, nil
}

// Alive reports whether d names a live process in m.
//
//ergo:wait none
func (m *MemSessions) Alive(pid int, start int64) bool {
	if m == nil || pid <= 0 {
		return false
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.alive && e.start == start
}

// Kill ends d in m with status 137 (signal 9) if alive.
//
//ergo:wait none
func (m *MemSessions) Kill(pid int, start int64) {
	if m == nil || pid <= 0 {
		return
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	match := e.alive && e.start == start
	e.mu.Unlock()
	if match {
		m.completeEntry(e, 137, 9)
	}
}

// Complete marks a simulated detached process exited with (status, signal).
//
//ergo:wait none
func (m *MemSessions) Complete(pid int, start int64, status, signal int) {
	if m == nil || pid <= 0 {
		return
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	match := e.start == start
	e.mu.Unlock()
	if match {
		m.completeEntry(e, status, signal)
	}
}

// MarkNotChild clears OS-collectible exit status for d to simulate an
// unrelated parent / reparented PID whose exit status is unavailable.
//
//ergo:wait none
func (m *MemSessions) MarkNotChild(pid int, start int64) {
	if m == nil || pid <= 0 {
		return
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.start == start {
		first := !e.exited
		e.alive = false
		e.exited = true
		e.notChild = true
		if first {
			close(e.done)
		}
		e.wakeLocked()
	}
	e.mu.Unlock()
}

// StartWithPTY starts a simulated child attached to p.
//
//ergo:wait none
func (m *MemSessions) StartWithPTY(name string, stdin string, p *PTY) (*Running, error) {
	if !m.hasProgram(name) {
		return nil, ExecNotFound{Name: name}
	}
	if stdin != "" {
		return nil, ExecFailed{Name: name, Reason: "PTY command cannot specify buffered Stdin; write to PTY directly"}
	}
	if p == nil {
		return nil, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	mem := p.mem
	fd := p.memFD
	p.mu.Unlock()
	if closed || mem != m {
		return nil, ExecFailed{Name: name, Reason: "PTY does not belong to this provider or is closed"}
	}
	st, err := m.getPTY(fd)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	e := m.newProcLocked(name, st, nil, nil, nil)
	m.mu.Unlock()
	st.mu.Lock()
	st.echo = true
	st.slaveOf = e
	st.mu.Unlock()

	mr := &memRunning{host: m, proc: e}
	r := &Running{
		proc:  &memProcBridge{host: m, entry: e},
		after: nil,
		name:  name,
		pid:   e.pid,
		start: e.start,
		mem:   mr,
		drop:  make(chan struct{}),
		ended: make(chan struct{}),
	}
	close(r.ended)
	mr.r = r
	return r, nil
}

// SpawnWithPTY starts a simulated detached child attached to p.
//
//ergo:wait none
func (m *MemSessions) SpawnWithPTY(name string, stdin string, p *PTY) (ExecDetached, error) {
	if !m.hasProgram(name) {
		return ExecDetached{}, ExecNotFound{Name: name}
	}
	if stdin != "" {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY command cannot specify buffered Stdin; write to PTY directly"}
	}
	if p == nil {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY is closed"}
	}
	p.mu.Lock()
	closed := p.closed
	mem := p.mem
	fd := p.memFD
	p.mu.Unlock()
	if closed || mem != m {
		return ExecDetached{}, ExecFailed{Name: name, Reason: "PTY does not belong to this provider or is closed"}
	}
	st, err := m.getPTY(fd)
	if err != nil {
		return ExecDetached{}, err
	}
	m.mu.Lock()
	e := m.newProcLocked(name, st, nil, nil, nil)
	m.mu.Unlock()
	st.mu.Lock()
	st.echo = true
	st.slaveOf = e
	st.mu.Unlock()
	return ExecDetached{Pid: e.pid, Start: e.start, Name: name}, nil
}

// StartInteractive starts a simulated interactive pipe child in m.
//
//ergo:wait none
func (m *MemSessions) StartInteractive(name string, stdin string, outKind, errKind int) (*Running, error) {
	if !m.hasProgram(name) {
		return nil, ExecNotFound{Name: name}
	}
	if stdin != "" {
		return nil, ExecFailed{Name: name, Reason: "interactive start cannot specify buffered Stdin; use WriteStdin"}
	}
	if outKind == execToStdout {
		return nil, execToItself(name)
	}
	inPipe := newMemPipeState(1, 1)
	var outPipe, errPipe *memPipeState
	if outKind == execToCaller {
		outPipe = newMemPipeState(1, 1)
	}
	if errKind == execToCaller {
		errPipe = newMemPipeState(1, 1)
	}
	m.mu.Lock()
	e := m.newProcLocked(name, nil, inPipe, outPipe, errPipe)
	m.mu.Unlock()

	mr := &memRunning{
		host:       m,
		proc:       e,
		inPipe:     inPipe,
		outPipe:    outPipe,
		errPipe:    errPipe,
		captureOut: outKind == execToCaller,
		captureErr: errKind == execToCaller,
		mergeErr:   errKind == execToStdout,
	}
	r := &Running{
		proc:  &memProcBridge{host: m, entry: e},
		after: nil,
		name:  name,
		pid:   e.pid,
		start: e.start,
		mem:   mr,
		drop:  make(chan struct{}),
		ended: make(chan struct{}),
	}
	if outPipe != nil || errPipe != nil {
		r.chunks = make(chan ExecChunk, execChunkBuffer)
	} else {
		close(r.ended)
	}
	mr.r = r
	e.mu.Lock()
	e.onExit = mr.finishStreams
	e.mu.Unlock()
	inPipe.mu.Lock()
	inPipe.onWrite = func(data string) {
		mr.forwardInput(data)
	}
	inPipe.onEOF = func() {
		mr.finishInput()
	}
	inPipe.mu.Unlock()
	return r, nil
}

func (mr *memRunning) forwardInput(data string) {
	if mr.outPipe == nil {
		return
	}
	if !mr.r.detached.Load() {
		mr.mu.Lock()
		if !mr.r.detached.Load() {
			if mr.r.chunks != nil && mr.bufOut == "" {
				select {
				case mr.r.chunks <- ExecChunk{Text: data, Stderr: false}:
					mr.mu.Unlock()
					return
				case <-mr.r.drop:
				default:
				}
			}
			mr.bufOut += data
			mr.mu.Unlock()
			return
		}
		mr.mu.Unlock()
	}
	mr.outPipe.mu.Lock()
	mr.outPipe.buf = append(mr.outPipe.buf, data...)
	mr.outPipe.wakeLocked()
	mr.outPipe.mu.Unlock()
}

func (mr *memRunning) finishInput() {
	mr.host.completeEntry(mr.proc, 0, 0)
}

func (mr *memRunning) writeStdin(data string) error {
	mr.mu.Lock()
	closed := mr.inClosed || mr.r.detached.Load() || mr.inPipe == nil
	mr.mu.Unlock()
	if closed {
		return ExecFailed{Name: mr.proc.name, Reason: "process has no writable stdin pipe"}
	}
	mr.forwardInput(data)
	return nil
}

func (mr *memRunning) closeStdin() error {
	mr.mu.Lock()
	if mr.inClosed || mr.r.detached.Load() || mr.inPipe == nil {
		mr.mu.Unlock()
		return ExecFailed{Name: mr.proc.name, Reason: "process has no open stdin pipe"}
	}
	mr.inClosed = true
	mr.mu.Unlock()
	mr.finishInput()
	return nil
}

func (mr *memRunning) detachRunning(r *Running) (*DetachedRun, error) {
	r.detachMu.Lock()
	defer r.detachMu.Unlock()
	if r.detached.Load() || r.finished {
		return nil, ExecFailed{Name: r.name, Reason: "process has already finished or detached"}
	}
	mr.proc.mu.Lock()
	exited := mr.proc.exited
	mr.proc.mu.Unlock()
	if exited {
		return nil, ExecFailed{Name: r.name, Reason: "process has already exited"}
	}
	r.detached.Store(true)
	r.finished = true
	close(r.drop)
	mr.finishStreams()
	<-r.ended

	var preOut string
	r.mu.Lock()
	for _, c := range r.back {
		if !c.Stderr {
			preOut += c.Text
		}
	}
	r.back = nil
	r.mu.Unlock()
	if r.chunks != nil {
		for c := range r.chunks {
			if !c.Stderr {
				preOut += c.Text
			}
		}
	}

	mr.mu.Lock()
	bufOut := preOut + mr.bufOut
	bufErr := mr.bufErr
	mr.bufOut = ""
	mr.bufErr = ""
	inClosed := mr.inClosed
	mr.mu.Unlock()

	var stdinP, stdoutP, stderrP *Pipe
	mr.host.mu.Lock()
	if mr.inPipe != nil && !inClosed {
		inFD := mr.host.allocFDLocked(&memFDEntry{kind: memFDPipeWrite, pipe: mr.inPipe})
		stdinP = &Pipe{closeCh: make(chan struct{}), mem: mr.host, memFD: inFD}
	}
	if mr.outPipe != nil {
		outFD := mr.host.allocFDLocked(&memFDEntry{kind: memFDPipeRead, pipe: mr.outPipe})
		stdoutP = &Pipe{closeCh: make(chan struct{}), mem: mr.host, memFD: outFD}
	}
	if mr.errPipe != nil {
		errFD := mr.host.allocFDLocked(&memFDEntry{kind: memFDPipeRead, pipe: mr.errPipe})
		stderrP = &Pipe{closeCh: make(chan struct{}), mem: mr.host, memFD: errFD}
	}
	mr.host.mu.Unlock()

	return &DetachedRun{
		info:        ExecDetached{Pid: mr.proc.pid, Start: mr.proc.start, Name: mr.proc.name},
		bufferedOut: bufOut,
		bufferedErr: bufErr,
		stdin:       stdinP,
		stdout:      stdoutP,
		stderr:      stderrP,
	}, nil
}

type memProcBridge struct {
	host  *MemSessions
	entry *memProcEntry
}

func (p *memProcBridge) exited() <-chan struct{} { return p.entry.done }

func (p *memProcBridge) signal(s syscall.Signal) {
	sig := int(s)
	if sig <= 0 {
		sig = 9
	}
	p.host.completeEntry(p.entry, 128+sig, sig)
}

func (p *memProcBridge) reap() int {
	p.entry.mu.Lock()
	ex := p.entry.exited
	st := p.entry.status
	p.entry.mu.Unlock()
	if !ex {
		p.host.completeEntry(p.entry, 137, 9)
		return 137
	}
	return st
}

// PollDetached checks the state of detached pid with start time start in m.
//
//ergo:wait none
func (m *MemSessions) PollDetached(pid int, start int64) (int, int, int, bool, string) {
	if m == nil || pid <= 0 {
		return execOutcomeUnavailable, 0, 0, true, "invalid pid"
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return execOutcomeUnavailable, 0, 0, true, "unknown detached process"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.start != start {
		return execOutcomeUnavailable, 0, 0, true, "start time mismatch"
	}
	if !e.exited {
		return execOutcomeRunning, 0, 0, false, ""
	}
	if e.notChild {
		return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process"
	}
	return execOutcomeExited, e.status, e.signal, false, ""
}

// WaitDetached waits parked on t until the simulated process exits.
//
//ergo:wait caller
func (m *MemSessions) WaitDetached(t *rt.Task, pid int, start int64) (int, int, int, bool, string) {
	t.Check()
	if m == nil || pid <= 0 {
		return execOutcomeUnavailable, 0, 0, true, "invalid pid"
	}
	m.mu.Lock()
	e := m.procs[pid]
	m.mu.Unlock()
	if e == nil {
		return execOutcomeUnavailable, 0, 0, true, "unknown detached process"
	}
	for {
		t.Check()
		e.mu.Lock()
		if e.start != start {
			e.mu.Unlock()
			return execOutcomeUnavailable, 0, 0, true, "start time mismatch"
		}
		if e.exited {
			notChild := e.notChild
			st, sg := e.status, e.signal
			e.mu.Unlock()
			if notChild {
				return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process"
			}
			return execOutcomeExited, st, sg, false, ""
		}
		notify := e.notify
		var p rt.Parked
		if t.InDomain() {
			e.waiters = append(e.waiters, t)
			p = rt.Park(t, e, t.Done(), func() bool { return e.exited })
		}
		e.mu.Unlock()
		select {
		case <-notify:
		case <-t.Done():
			p.Unpark()
			if t.InDomain() {
				e.mu.Lock()
				removeMemWaiter(&e.waiters, t)
				e.mu.Unlock()
			}
			t.Check()
		}
		p.Unpark()
		if t.InDomain() {
			e.mu.Lock()
			removeMemWaiter(&e.waiters, t)
			e.mu.Unlock()
		}
	}
}

// ExecMemOpenPipe allocates a virtual pipe pair in m (bound by Ergo
// MemorySessions.OpenPipe without multi-result method lowering).
//
//ergo:wait none
func ExecMemOpenPipe(m *MemSessions) (*Pipe, *Pipe, error) {
	if m == nil {
		return nil, nil, ExecFailed{Name: "pipe", Reason: "memory sessions unavailable"}
	}
	return m.OpenPipe()
}

// ExecMemPollDetached inspects detached process state in m without blocking.
//
//ergo:wait none
func ExecMemPollDetached(m *MemSessions, pid int, start int64) (int, int, int, bool, string) {
	if m == nil {
		return execOutcomeUnavailable, 0, 0, true, "memory sessions unavailable"
	}
	return m.PollDetached(pid, start)
}

// ExecMemWaitDetached waits parked on t until simulated process exits.
//
//ergo:wait caller
func ExecMemWaitDetached(t *rt.Task, m *MemSessions, pid int, start int64) (int, int, int, bool, string) {
	t.Check()
	if m == nil {
		return execOutcomeUnavailable, 0, 0, true, "memory sessions unavailable"
	}
	return m.WaitDetached(t, pid, start)
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
