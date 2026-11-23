//go:build linux && (amd64 || arm64)

package testinputs

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// A sink receives what the observer saw a process tree do. The test log's
// recorder is one; an accessLog, which ergo task exec keys a command's
// result on, is the other. Both get every call; each keeps what its key
// needs.
type sink interface {
	// open: a file read (read) or opened for writing only, a folder listed.
	open(path string, read bool)
	// stat: a lookup, a failed one included.
	stat(path string)
	// exec: a program run, with the names of the variables it received.
	exec(path string, names []string)
	// wrote: what a call does to a path's content besides its lookup,
	// told before the lookup (or, for a write that keeps the content,
	// after the read the same open makes).
	wrote(path string, how writeRole)
	// peer: an address a process binds, connects or sends to.
	peer(p peer)
	fail(reason string)
	refuse(reason string)
	reason() string
	stop()
}

// A writeRole is what a call does to a path's content.
type writeRole uint8

const (
	writeNone  writeRole = iota
	writeWhole           // makes or removes the path whatever it held: creat, O_TRUNC, mkdir, unlink, a link's or rename's new name
	writePart            // changes it and keeps the rest: O_APPEND or no O_TRUNC, chmod, utime, truncate, an xattr
	writeMove            // moves its content away: a rename's old name, which held what the new one holds
	writeRead            // reads its content without opening it: a hard link's old name
)

// The address families a peer names.
const (
	familyUnix  = 1
	familyInet  = 2
	familyInet6 = 10
)

// A peer is an address a process binds, connects or sends to: a named
// AF_UNIX socket by its absolute path, an abstract AF_UNIX name, or an
// AF_INET or AF_INET6 address and port.
type peer struct {
	family uint16
	path   string // a named AF_UNIX socket's absolute path
	name   string // the Unix name as supplied to the kernel
	addr   netip.Addr
	port   int
	bind   bool
	socket socketRef
}

// The test log keys no write and no peer: a write's lookup is its stat
// line, and a socket file's path is a stat line too.
func (r *recorder) wrote(string, writeRole) {}
func (r *recorder) peer(peer)               {}

// An accessLog keeps, for a command (Exec), every input: the state of each
// path at the first access that depends on what the path held before the
// run. An access after the tree replaced the path (writeWhole) depends on
// the tree's own write, so it is no input; a write that keeps the content
// (writePart, writeMove) takes the content as an input first. It also
// keeps the paths the tree wrote, and refuses the run (fail) when the tree
// talks to a process outside it: a socket it did not bind, the network.
//
// The observer workers serialize the hook, while the calling thread waits
// at its notification. A second access cannot skip a still-pending snapshot.
type accessLog struct {
	snap func(path string, kind SnapKind) string

	mu      sync.Mutex
	inputs  map[inputKey]string
	order   []inputKey
	read    map[string]pathVersion
	written map[string]pathVersion
	bound   map[socketRef]peer
	failure string
	stopped bool
}

type inputKey struct {
	path string
	kind SnapKind
}

func newAccessLog(snap func(string, SnapKind) string) *accessLog {
	return &accessLog{
		snap: snap, inputs: map[inputKey]string{}, read: map[string]pathVersion{},
		written: map[string]pathVersion{}, bound: map[socketRef]peer{},
	}
}

// normal returns the path an access log keeps for a path the observer
// decoded, or false for one that is no input: /proc, /dev and /sys, whose
// files are the kernel's view of the moment (declared not inputs, as the
// clock is). A path with ".." is resolved by its folder, as virtual does.
func normal(path string) (string, bool) {
	if virtual(path) {
		return "", false
	}
	if hasDotDot(path) {
		dir, base := filepath.Split(path)
		if base == ".." || base == "." {
			dir, base = path, ""
		}
		if physical, ok := physicalDir(dir); ok {
			return filepath.Join(physical, base), true
		}
		return path, true // cleaning would turn a failed lookup into a different path
	}
	return filepath.Clean(path), true
}

func (l *accessLog) depend(raw string, kind SnapKind) {
	path, ok := normal(raw)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.capture(path, kind)
}

// capture runs with the lock held. An attempted write is not evidence of a
// replacement: a failed operation can leave the old input where it was.
func (l *accessLog) capture(path string, kind SnapKind) {
	k := inputKey{path, kind}
	if _, have := l.inputs[k]; have || l.stopped {
		return
	}
	var v pathVersion
	if before, written := l.written[path]; written || kind == SnapRead {
		v = version(path)
		if written && before != v {
			return
		}
	}
	if _, have := l.read[path]; kind == SnapRead && !have {
		l.read[path] = v
	}
	var state string
	if l.snap != nil {
		state = l.snap(path, kind)
	}
	l.inputs[k] = state
	l.order = append(l.order, k)
}

func (l *accessLog) open(path string, read bool) {
	if read {
		l.depend(path, SnapRead)
	}
}

func (l *accessLog) stat(path string) {
	l.depend(path, SnapLookup)
}

// exec takes the program's content and its #! interpreters' as inputs.
// The variables need no key: the command's environment is the caller's,
// and every later one derives from it.
func (l *accessLog) exec(path string, _ []string) {
	for range 5 {
		l.depend(path, SnapRead)
		if path = shebang(path); path == "" {
			return
		}
	}
}

func (l *accessLog) wrote(path string, how writeRole) {
	p, ok := normal(path)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	before := version(p)
	switch how {
	case writeWhole:
		l.capture(p, SnapExists)
	case writePart, writeMove, writeRead:
		l.capture(p, SnapRead)
	}
	if how == writeRead {
		return
	}
	if _, wrote := l.written[p]; !wrote {
		l.written[p] = before
	}
}

// peer refuses the run when the tree talks to something it did not start:
// a socket file it did not bind, any abstract Unix address, or a network
// address other than a loopback endpoint it bound.
func (l *accessLog) peer(p peer) {
	if p.path != "" {
		if path, ok := normal(p.path); ok {
			p.path = path
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	if p.family == familyUnix && p.path == "" {
		l.failLocked("the command used an abstract Unix socket")
		return
	}
	if p.bind {
		if p.socket != (socketRef{}) {
			l.bound[p.socket] = p
		}
		return
	}
	if l.failure != "" {
		return
	}
	if p.family == familyUnix || p.addr.Unmap().IsLoopback() {
		for _, binding := range l.bound {
			if binding.matches(p) {
				return
			}
		}
	}
	if p.family == familyUnix {
		l.failure = "the command talked to a process outside it through socket " + p.path
	} else {
		l.failure = "the command used the network (" + netip.AddrPortFrom(p.addr, uint16(p.port)).String() + ")"
	}
}

// A bind notification says what was attempted, not whether it worked.
// Check the same live socket's address before trusting a later peer.
type socketRef struct {
	pid, fd  int
	dev, ino uint64
}

func identifySocket(tid, fd int) socketRef {
	var st syscall.Stat_t
	if err := syscall.Stat("/proc/"+strconv.Itoa(tid)+"/fd/"+strconv.Itoa(fd), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return socketRef{}
	}
	proc, _ := os.ReadFile("/proc/" + strconv.Itoa(tid) + "/status")
	for line := range strings.SplitSeq(string(proc), "\n") {
		if value, ok := strings.CutPrefix(line, "Tgid:"); ok {
			pid, _ := strconv.Atoi(strings.TrimSpace(value))
			return socketRef{pid: pid, fd: fd, dev: st.Dev, ino: st.Ino}
		}
	}
	return socketRef{}
}

func (r socketRef) duplicate() (int, bool) {
	if r.pid == 0 {
		return -1, false
	}
	pidfd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(r.pid), 0, 0)
	if errno != 0 {
		return -1, false
	}
	defer syscall.Close(int(pidfd))
	fd, err := takeDescriptor(int(pidfd), r.fd)
	if err != nil {
		return -1, false
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Dev != r.dev || st.Ino != r.ino {
		syscall.Close(fd)
		return -1, false
	}
	return fd, true
}

func (bound peer) matches(to peer) bool {
	if bound.family != to.family || bound.family == familyUnix && bound.path != to.path || bound.port != 0 && bound.port != to.port {
		return false
	}
	fromFD, ok := bound.socket.duplicate()
	if !ok {
		return false
	}
	defer syscall.Close(fromFD)
	toFD, ok := to.socket.duplicate()
	if !ok {
		return false
	}
	defer syscall.Close(toFD)
	a, errA := syscall.GetsockoptInt(fromFD, syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
	b, errB := syscall.GetsockoptInt(toFD, syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
	if errA != nil || errB != nil || a != b {
		return false
	}
	a, errA = syscall.GetsockoptInt(fromFD, syscall.SOL_SOCKET, syscall.SO_TYPE)
	b, errB = syscall.GetsockoptInt(toFD, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if errA != nil || errB != nil || a != b {
		return false
	}
	addr, err := syscall.Getsockname(fromFD)
	if err != nil {
		return false
	}
	switch addr := addr.(type) {
	case *syscall.SockaddrUnix:
		return addr.Name != "" && addr.Name == bound.name
	case *syscall.SockaddrInet4:
		return addressMatches(netip.AddrFrom4(addr.Addr), addr.Port, to)
	case *syscall.SockaddrInet6:
		return addressMatches(netip.AddrFrom16(addr.Addr), addr.Port, to)
	}
	return false
}

func addressMatches(ip netip.Addr, port int, to peer) bool {
	return port != 0 && port == to.port && (ip.IsUnspecified() || ip.Unmap() == to.addr.Unmap())
}

func (l *accessLog) fail(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLocked(reason)
}

func (l *accessLog) failLocked(reason string) {
	if !l.stopped && l.failure == "" {
		l.failure = reason
	}
}

func (l *accessLog) refuse(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failure == "" {
		l.failure = reason
	}
}

func (l *accessLog) reason() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

func (l *accessLog) stop() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
}

// validate compares the accessed object, not just its timestamp. ctime
// catches an edit whose writer rolled mtime back; atime is not an input.
func (l *accessLog) validate() {
	for _, k := range l.order {
		if k.kind != SnapRead {
			continue
		}
		if _, wrote := l.written[k.path]; !wrote && l.read[k.path] != version(k.path) {
			l.refuse("a read input changed during the command: " + k.path)
			return
		}
	}
}

type pathVersion struct {
	link, target statVersion
}

type statVersion struct {
	dev, ino     uint64
	size         int64
	mode         uint32
	mtime, ctime syscall.Timespec
	err          string
}

func version(path string) pathVersion {
	link, err := os.Lstat(path)
	out := pathVersion{link: statStamp(link, err)}
	if err == nil && link.Mode()&os.ModeSymlink != 0 {
		target, err := os.Stat(path)
		out.target = statStamp(target, err)
	}
	return out
}

func statStamp(info os.FileInfo, err error) statVersion {
	if err != nil {
		if e, ok := err.(*os.PathError); ok {
			err = e.Err
		}
		return statVersion{err: err.Error()}
	}
	st := info.Sys().(*syscall.Stat_t)
	return statVersion{dev: st.Dev, ino: st.Ino, size: st.Size, mode: st.Mode, mtime: st.Mtim, ctime: st.Ctim}
}

// result returns the inputs, in the order the run first depended on them,
// and the paths it wrote, sorted.
func (l *accessLog) result() ([]Input, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	inputs := make([]Input, 0, len(l.order))
	for _, k := range l.order {
		inputs = append(inputs, Input{Path: k.path, Kind: k.kind, State: l.inputs[k]})
	}
	written := make([]string, 0, len(l.written))
	for p := range l.written {
		written = append(written, p)
	}
	slices.Sort(written)
	return inputs, written
}
