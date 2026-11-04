package testinputs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// Identity mode is for the closure sweep (internal/closure), which classifies
// each path a test log records by the object the path reached. A log records
// a path as text, and the sweep resolves the text after the run. A process
// outside the observed tree, which the tree can ask to act for it, can change
// what the text names in between: a read through a link, then the link
// retargeted. In identity mode the observer also keeps, for each path it
// records, what the lookup of that path met at the time of the access: every
// location the lookup passed, with the device and inode of the object there
// and the target text of each link (a Visit). The closure sweep then
// classifies a path by these objects and compares them with what the path
// names after the run.
//
// The observer makes this lookup itself, while the calling thread waits at
// its notification, before the kernel runs the call: seccomp user
// notification stops a call before it runs, so there is no descriptor of the
// object yet. To close the gap between its lookup and the kernel's, the
// observer watches (inotify) each directory it looks a name up in, before the
// lookup, when a process outside the tree can change it (identity_linux.go):
// any change of that name after the lookup, until the tree exits, is a
// changed location. It also watches every directory of the checkout, and
// the mount table.

// IdentityVariable names the file in which the observer writes the identity
// of the run's accesses (an Identity, as JSON) when the variable is set. Only
// the observing process reads it; the tests do not receive it.
const IdentityVariable = "ERGO_TESTINPUTS_IDENTITY"

// The kinds of object a Visit found.
const (
	KindAbsent = "absent" // nothing has that name
	KindDir    = "dir"
	KindFile   = "file" // a regular file
	KindLink   = "link" // a symbolic link; Target holds its text
	KindOther  = "other"
	KindError  = "error" // the lookup failed; Err says how
)

// A Visit is what a lookup of one location found: an absolute path with no
// link in it, the kind of object there, its device and inode, and a link's
// target text.
type Visit struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Dev    uint64 `json:"dev,omitempty"`
	Ino    uint64 `json:"ino,omitempty"`
	Target string `json:"target,omitempty"`
	Err    string `json:"err,omitempty"`
}

// Look returns what is at a location now, without following a link there.
func Look(at string) Visit {
	v := Visit{At: at}
	info, err := os.Lstat(at)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v.Kind = KindAbsent
		return v
	case err != nil:
		v.Kind, v.Err = KindError, errorText(err)
		return v
	}
	v.Dev, v.Ino = inode(info)
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(at)
		if err != nil {
			v.Kind, v.Err = KindError, errorText(err)
			return v
		}
		v.Kind, v.Target = KindLink, target
	case mode.IsDir():
		v.Kind = KindDir
	case mode.IsRegular():
		v.Kind = KindFile
	default:
		v.Kind = KindOther
	}
	return v
}

// errorText is the system's text for an error, without the path.
func errorText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}

// Identity is what identity mode writes: by the path a log line names
// (IdentityKey), each distinct walk an access of that path made, as the
// visits of its lookup in order. Changed holds each location whose object or
// name changed after an access looked it up, before the tree exited.
type Identity struct {
	Walks   map[string][][]Visit `json:"walks"`
	Changed []string             `json:"changed,omitempty"`
}

// IdentityKey is the key in Identity.Walks of an absolute path a log line
// names: the path with "." components and repeated or trailing slashes
// removed, unless it holds a "..", which only a walk can resolve.
func IdentityKey(path string) string { return clean(path) }

// ReadIdentity reads the file identity mode wrote.
func ReadIdentity(file string) (Identity, error) {
	var id Identity
	data, err := os.ReadFile(file)
	if err != nil {
		return id, err
	}
	if err := json.Unmarshal(data, &id); err != nil {
		return id, fmt.Errorf("%s: %w", file, err)
	}
	return id, nil
}

// A watcher sees the lookups an identity walk makes, before each one, and
// reports at the end of the run the locations that changed after a lookup.
type watcher interface {
	// see is called before the walk looks name up in the directory dir.
	see(dir, name string)
	// finish stops watching and returns the changed locations, or the
	// reason the watch cannot vouch for the run.
	finish() (changed []string, reason string)
}

// identity holds a run's walks while the tree runs.
type identity struct {
	file  string
	watch watcher

	mu    sync.Mutex
	walks map[string]map[string][]Visit // key → fingerprint → walk
}

func newIdentityRecord(file string, watch watcher) *identity {
	return &identity{file: file, watch: watch, walks: map[string]map[string][]Visit{}}
}

// keep remembers one walk of a path.
func (id *identity) keep(key string, visits []Visit) {
	fingerprint, _ := json.Marshal(visits)
	id.mu.Lock()
	defer id.mu.Unlock()
	walks := id.walks[key]
	if walks == nil {
		walks = map[string][]Visit{}
		id.walks[key] = walks
	}
	walks[string(fingerprint)] = visits
}

// write saves the walks and the changed locations.
func (id *identity) write(changed []string) error {
	id.mu.Lock()
	out := Identity{Walks: map[string][][]Visit{}, Changed: changed}
	for key, walks := range id.walks {
		var fingerprints []string
		for f := range walks {
			fingerprints = append(fingerprints, f)
		}
		slices.Sort(fingerprints)
		for _, f := range fingerprints {
			out.Walks[key] = append(out.Walks[key], walks[f])
		}
	}
	id.mu.Unlock()
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(id.file, data, 0o644)
}

// An identityWalk is one lookup of a path, made as the kernel makes it: the
// locations it met (visits) and, once it reached the root, the path under the
// logical root as the log records it (logged). It is recorder.walk without
// memoization, continued to the end of the path.
type identityWalk struct {
	visits  []Visit
	logged  string
	entered bool
}

// identify walks path one component at a time from "/": "." stays, ".."
// moves to the parent of the link-free location reached, and a link splices
// its target in front of the rest. Each lookup is announced to the watcher
// before it is made. The walk ends at an absent component, a file, an
// error, or the kernel's limit of links.
func (r *recorder) identify(path string) identityWalk {
	var w identityWalk
	pending := strings.Split(path, "/")
	current := "/"
	for hops := 0; ; {
		if !w.entered {
			if rest, ok := r.within(current); ok {
				for _, name := range pending {
					if name != "" && name != "." {
						rest += "/" + name
					}
				}
				w.logged, w.entered = r.logical+rest, true
			}
		}
		if len(pending) == 0 {
			return w
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
		r.ident.watch.see(current, name)
		v := Look(next)
		w.visits = append(w.visits, v)
		switch v.Kind {
		case KindLink:
			if hops++; hops > 40 { // the kernel's ELOOP limit
				return w
			}
			if strings.HasPrefix(v.Target, "/") {
				current = "/"
			}
			pending = append(strings.Split(v.Target, "/"), pending...)
		case KindDir:
			current = next
		default:
			return w
		}
	}
}

// recordIdentified is record in identity mode: the walk decides whether the
// path reaches the root, and the walks of the path as the call named it and
// as the log records it are kept.
func (r *recorder) recordIdentified(path string, key entryKey) {
	if !hasDotDot(path) && virtual(path) {
		return // a ".." path is virtual only if its walk leads there, which never reaches the root
	}
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return // a process the test left running; the identity is written
	}
	p := clean(path)
	w := r.identify(p)
	if !w.entered {
		return
	}
	r.ident.keep(p, w.visits)
	if w.logged != p {
		r.ident.keep(w.logged, r.identify(w.logged).visits)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.paths[w.logged] |= key
	}
}

// finishIdentity ends identity mode once the tree has exited: it walks the
// links the log adds along recorded paths (statLinksLocked), which are lines
// of the log too, stops the watch and writes the identity file. A watch that
// cannot vouch for the run, or a file that cannot be written, refuses the
// result.
func (r *recorder) finishIdentity() {
	if r.ident == nil {
		return
	}
	r.mu.Lock()
	links := r.statLinksLocked()
	r.mu.Unlock()
	for _, link := range links {
		r.ident.keep(link, r.identify(link).visits)
	}
	changed, reason := r.ident.watch.finish()
	if reason != "" {
		r.refuse("the identity of the inputs is unknown: " + reason)
	}
	if err := r.ident.write(changed); err != nil {
		r.refuse("write the identity of the inputs: " + err.Error())
	}
}
