package testinputs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// A recorder turns the accesses a process tree made into test-log entries.
// cmd/go rechecks only paths inside the module root, so the recorder keeps
// those: paths under the root as the test process names it (logical), under
// its physical location, or reached through a link from outside it, each
// written under the logical root.
//
// The observer's workers call it concurrently. Deciding where a path lies,
// which may read the file system, happens outside the lock; only the
// entries are guarded.
type recorder struct {
	logical, physical string

	// The observed tree runs as this user with no_new_privs, so it cannot
	// change a directory that another user owns and this user cannot write:
	// it can neither add, remove or replace an entry there nor chmod the
	// directory. What a lookup finds in such a directory, on a path of such
	// directories, is remembered; everything else is looked up on each call,
	// since the tree can change it between two calls. A process that may
	// override permissions (root, or CAP_DAC_OVERRIDE, CAP_FOWNER or
	// CAP_CHOWN) remembers nothing.
	memoize bool
	sealed  sync.Map // physical directory → whether it and every directory above it are sealed
	fixed   sync.Map // physical path in a sealed directory → the entry a lookup found

	mu      sync.Mutex
	paths   map[string]entryKey
	env     map[string]bool
	execs   int
	failure string
	stopped bool // the test process has exited; later calls are not its inputs

	// ident, when not nil, keeps the identity of each recorded access
	// (identity.go). It is set before the tree starts.
	ident *identity
}

// An entryKey says how cmd/go rechecks a path: "open" hashes a file's
// metadata or a directory's entries, "stat" hashes the path's Stat and
// Lstat. A path both read and looked up gets both lines, since "open" does
// not look at the link itself.
type entryKey uint8

const (
	keyOpen entryKey = 1 << iota
	keyStat
)

func newRecorder(root string) *recorder {
	r := &recorder{
		logical: filepath.Clean(root),
		memoize: !privileged(),
		paths:   map[string]entryKey{},
		env:     map[string]bool{},
	}
	r.physical = r.logical
	if physical, err := filepath.EvalSymlinks(r.logical); err == nil {
		r.physical = physical
	}
	return r
}

// fail marks the observation incomplete. The first reason is kept. A
// failure after stop comes from a process the test left running, which is
// not the test's, and is ignored.
func (r *recorder) fail(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped && r.failure == "" {
		r.failure = reason
	}
}

// refuse marks the observation incomplete even after stop.
func (r *recorder) refuse(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure == "" {
		r.failure = reason
	}
}

// reason returns why the observation is incomplete, or "".
func (r *recorder) reason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure
}

// stop ends recording once the test process has exited.
func (r *recorder) stop() {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
}

// open records a file read or a directory listed. A file opened only for
// writing is an output, so only its existence is recorded.
func (r *recorder) open(path string, read bool) {
	if read {
		r.record(path, keyOpen)
	} else {
		r.record(path, keyStat)
	}
}

func (r *recorder) stat(path string) { r.record(path, keyStat) }

// exec records a program run with the names of the variables it received:
// its file, and the interpreters its #! lines name, which the kernel opens
// without a system call.
func (r *recorder) exec(path string, names []string) {
	files := []string{path}
	for range 4 {
		interpreter := shebang(path)
		if interpreter == "" {
			break
		}
		files = append(files, interpreter)
		path = interpreter
	}
	for _, file := range files {
		r.record(file, keyOpen)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.execs++
	for _, name := range names {
		if strings.ContainsRune(name, '\n') {
			if r.failure == "" {
				r.failure = "an environment variable name holds a newline"
			}
			continue
		}
		r.env[name] = true
	}
}

// shebang returns the absolute interpreter a script's #! line names, or "".
// It opens without blocking and reads only a regular file, so a FIFO named
// by an exec never stalls the observer. A relative interpreter, which the
// kernel resolves against the working directory, is not followed.
func shebang(path string) string {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	head := make([]byte, 256)
	n, _ := io.ReadFull(file, head)
	line, ok := strings.CutPrefix(string(head[:n]), "#!")
	if !ok {
		return ""
	}
	line, _, _ = strings.Cut(line, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	return fields[0]
}

// record keeps one access to a path inside the module root.
func (r *recorder) record(path string, key entryKey) {
	if r.ident == nil && virtual(path) {
		return
	}
	if strings.ContainsRune(path, '\n') {
		r.fail("a path holds a newline, which the test log cannot carry")
		return
	}
	if r.ident != nil {
		r.recordIdentified(path, key)
		return
	}
	inside, ok := r.inside(clean(path))
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.paths[inside] |= key
	}
}

// virtual reports whether a path lies in /proc, /dev or /sys, which cmd/go
// cannot recheck and whose links resolve differently in each process. A
// ".." is resolved first, as the kernel would, so /proc/../root/file is
// recorded: the directory a path ends in is looked up where it exists, and
// a path whose directory is missing is cleaned by name. That directory is
// cut off the path as written, not cleaned: cleaning would drop a link
// before a "..", such as /tmp/../proc/<pid>/fd/<n>/../go.mod, which leads
// wherever descriptor n's parent is.
func virtual(path string) bool {
	if hasDotDot(path) {
		cut := strings.LastIndexByte(path, '/') + 1
		dir, base := path[:cut], path[cut:]
		if base == ".." || base == "." {
			dir, base = path, ""
		}
		if physical, ok := physicalDir(dir); ok {
			path = filepath.Join(physical, base)
		} else {
			path = filepath.Clean(path)
		}
	}
	for _, prefix := range []string{"/proc", "/dev", "/sys"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// hasDotDot reports whether a path has a ".." component.
func hasDotDot(path string) bool {
	for element := range strings.SplitSeq(path, "/") {
		if element == ".." {
			return true
		}
	}
	return false
}

// inside returns path under the logical root, if the kernel's lookup of it
// reaches the root.
func (r *recorder) inside(path string) (string, bool) {
	if r.ident != nil {
		w := r.identify(path)
		return w.logged, w.entered
	}
	if rest, ok := r.within(path); ok {
		return r.logical + rest, true
	}
	// Most paths outside the root (the Go toolchain's, system libraries)
	// pass through no link. If the parent's physical location is its name,
	// only the last component could link into the root.
	if dir := filepath.Dir(path); !strings.Contains(path, "/..") && r.direct(dir) {
		if e, err := r.lookup(dir, path); err != nil || !e.link {
			return "", false
		}
	}
	return r.walk(path)
}

// direct reports whether a directory outside the root exists under its own
// name, with no link on the way: its physical location is its name. It costs
// an open and a readlink instead of a lookup per component. A sealed
// directory's answer comes from the memo.
func (r *recorder) direct(dir string) bool {
	if r.memoize {
		if sealed, ok := r.sealed.Load(dir); ok && sealed.(bool) {
			return true // only a physical directory is ever sealed
		}
	}
	if physical, ok := physicalDir(dir); !ok || physical != dir {
		return false
	}
	if r.memoize {
		r.sealedDir(dir)
	}
	return true
}

// An entry is what a lookup of one path component found.
type entry struct {
	missing bool
	link    bool
	target  string
}

// walk resolves path one component at a time, as the kernel does, and
// returns the logical root joined with what remains of the path once the
// lookup reaches the root, through a link or by name. What remains is not
// resolved: cmd/go's recheck follows the links in it, so a link inside the
// root that is later retargeted, or a directory there that is missing now
// and added later, changes the key. A lookup that stops outside the root, at
// a missing component or one that is not a directory, is not an input
// cmd/go can recheck.
func (r *recorder) walk(path string) (string, bool) {
	pending := strings.Split(path, "/")
	current := "/"
	for hops := 0; ; {
		if rest, ok := r.within(current); ok {
			for _, name := range pending {
				if name != "" && name != "." {
					rest += "/" + name
				}
			}
			return r.logical + rest, true
		}
		if len(pending) == 0 {
			return "", false
		}
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := current + "/" + name
		if current == "/" {
			next = "/" + name
		}
		e, err := r.lookup(current, next)
		if err != nil || e.missing {
			return "", false
		}
		if !e.link {
			current = next
			continue
		}
		if hops++; hops > 40 { // the kernel's ELOOP limit
			return "", false
		}
		if strings.HasPrefix(e.target, "/") {
			current = "/"
		}
		pending = append(strings.Split(e.target, "/"), pending...)
	}
}

// lookup finds what a physical path names, from the memo when its directory
// is sealed.
func (r *recorder) lookup(dir, path string) (entry, error) {
	if r.memoize {
		if e, ok := r.fixed.Load(path); ok {
			return e.(entry), nil
		}
	}
	var e entry
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		e.missing = true
	case err != nil:
		return entry{}, err
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return entry{}, err
		}
		e = entry{link: true, target: target}
	}
	if r.memoize && r.sealedDir(dir) {
		r.fixed.Store(path, e)
	}
	return e, nil
}

// sealedDir reports whether the tree cannot change a physical directory or
// any directory above it. A directory that is not sealed is remembered too:
// if it became sealed later, only the memo's saving is lost.
func (r *recorder) sealedDir(dir string) bool {
	if sealed, ok := r.sealed.Load(dir); ok {
		return sealed.(bool)
	}
	sealed := false
	if foreignDir(dir) && !writable(dir) {
		sealed = dir == "/" || r.sealedDir(filepath.Dir(dir))
	}
	r.sealed.Store(dir, sealed)
	return sealed
}

// within reports whether path is the root or below it, by its logical or
// physical name, and the rest after the root.
func (r *recorder) within(path string) (string, bool) {
	if rest, ok := under(path, r.logical); ok {
		return rest, true
	}
	return under(path, r.physical)
}

// under reports whether path is root or below it, and the rest after root.
func under(path, root string) (string, bool) {
	if path == root {
		return "", true
	}
	if root == "/" {
		return path, strings.HasPrefix(path, "/")
	}
	if rest, ok := strings.CutPrefix(path, root); ok && strings.HasPrefix(rest, "/") {
		return rest, true
	}
	return "", false
}

// clean removes "." components and repeated or trailing slashes. It keeps
// ".." components, which the kernel resolves after following links.
func clean(path string) string {
	if hasDotDot(path) {
		return path
	}
	return filepath.Clean(path)
}

// lines returns the test-log entries, sorted: the environment variables the
// observed processes received, then the paths. A process run from outside
// the root reads executables and libraries cmd/go cannot recheck, and the
// gate's tool digest stands for them.
func (r *recorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	if r.execs > 0 {
		r.env[ToolDigest] = true
	}
	r.statLinksLocked()
	for name := range r.env {
		lines = append(lines, "getenv "+name+"\n")
	}
	for path, key := range r.paths {
		if key&keyOpen != 0 {
			lines = append(lines, "open "+path+"\n")
		}
		if key&keyStat != 0 {
			lines = append(lines, "stat "+path+"\n")
		}
	}
	slices.Sort(lines)
	return lines
}

// statLinksLocked adds a lookup of every link inside the root along a
// recorded path. cmd/go rechecks an "open" path by what it leads to, and
// the file a retargeted link now leads to may have the same metadata as the
// old one; the link's own Lstat changes with it. The links are read once
// the tree has exited, as they are when cmd/go rechecks the key. It returns
// the links.
func (r *recorder) statLinksLocked() []string {
	const (
		plain = iota + 1
		link
		missing
	)
	seen := map[string]int{} // prefix → what it is
	var found []string
	for path := range r.paths {
		rest, ok := under(path, r.logical)
		if !ok {
			continue
		}
		prefix := r.logical
		for element := range strings.SplitSeq(rest, "/") {
			if element == "" {
				continue
			}
			prefix = strings.TrimSuffix(prefix, "/") + "/" + element
			what := seen[prefix]
			if what == 0 {
				info, err := os.Lstat(prefix)
				switch {
				case err != nil:
					what = missing
				case info.Mode()&fs.ModeSymlink != 0:
					what = link
					found = append(found, prefix)
				default:
					what = plain
				}
				seen[prefix] = what
			}
			if what == missing {
				break // nothing below it exists
			}
		}
	}
	for _, link := range found {
		r.paths[link] |= keyStat
	}
	return found
}
