// Package replay is the gate's whole-gate replay: one key over every input
// of every step, the store that every worktree shares, the record a passing
// gate writes there, the replay of that record, and the merge tier's
// comparison of an uncached run with it.
//
// The package runs no program and builds without cgo (package hermetic
// checks it), so its tests replay from Go's test cache. Package main
// collects the facts that need programs (git, tool queries, the systemd
// probe) and passes them here as Facts. It builds on Linux and darwin, the
// platforms the gate runs on: the stat tuple of an ignored file needs each
// one's stat fields, and package hermetic refuses a file that none of its
// configurations builds.
//
// The store protocol follows the Quint model of the unit that introduced it
// (gate-incr slice 1, gatestore.qnt): content-addressed objects written once,
// a record written last by one atomic rename and only when the key at the
// end of the run equals the key at its start, and a replay that verifies
// every object through the descriptor it prints from. Any process may delete
// or damage any object or record at any time; that costs a rerun, never a
// stale replay.
package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"slices"
	"strconv"
	"strings"
)

// Version names the store's format and the meaning of its keys. It is part
// of every key and of the store's directory, so a change orphans old entries.
// v4: the whole-gate key, and step receipts that follow links (Links).
const Version = "ergo-gate-v4"

// Class is how Git sees a path of the checkout.
type Class string

const (
	Tracked   Class = "tracked"
	Untracked Class = "untracked"
	Ignored   Class = "ignored"
	// NoClass is the class of a directory: Git tracks files, not directories.
	NoClass Class = ""
)

// Kind is what a path of the checkout is on disk.
type Kind string

const (
	File      Kind = "file"
	Symlink   Kind = "symlink"
	Directory Kind = "directory"
	// Other is a FIFO, a socket or a device.
	Other Kind = "other"
)

// Entry is one path of the checkout, as the key sees it.
type Entry struct {
	// Path is relative to the checkout root, with slashes.
	Path  string
	Class Class
	Kind  Kind
	// Value is what the key holds for the path:
	//   - a regular file that Git tracks or sees untracked: its keyed
	//     permission bits and the SHA-256 of its content;
	//   - an ignored regular file, and every FIFO, socket or device: its stat
	//     tuple (size, mtime and ctime in nanoseconds, inode, mode);
	//   - a symlink: its target text;
	//   - a directory: nothing (the path is the input).
	Value string
	// Reach is where a symlink leads, as FollowLinks finds it.
	Reach string
}

// Variable is an environment variable with its set flag.
type Variable struct {
	Name  string
	Set   bool
	Value string
}

// Fact is a named host fact, such as a tool fingerprint or a file system type.
type Fact struct {
	Name, Value string
}

// Facts are every input of every step of one gate, as collected at one time.
type Facts struct {
	// Executable is the SHA-256 of the running gate executable.
	Executable string
	// Root is the checkout root's absolute path. It reaches steps through
	// TMPDIR and ERGO_REPO_ROOT.
	Root string
	// Head is the commit id of HEAD.
	Head string
	// Index holds the index entries: mode, blob id, stage and path.
	Index []string
	// Entries are every path under the root except .git, sorted by path.
	Entries []Entry
	// Environment is what a step can read from its environment.
	Environment []Variable
	// Settings are the gate's own settings: ERGO_GATE_*, XDG_CACHE_HOME, the
	// cache directory.
	Settings []Variable
	// Host is the host fingerprint.
	Host []Fact
	// Unreplayable says why these facts cannot key a replay: a link that
	// leads outside every keyed root, a fact the gate could not read. The
	// gate then neither replays nor records.
	Unreplayable []string
	// Recent names stat-keyed paths whose mtime or ctime fell within
	// RecentWindow before the collection. A change inside one timestamp tick
	// can leave a stat tuple unchanged, so the gate does not record a run
	// whose start or end facts name one.
	Recent []string
}

// Key is the whole-gate key: SHA-256, in hex, over every fact but
// Unreplayable and Recent, which decide whether the key may be used.
func (f *Facts) Key() string {
	f.sort()
	h := sha256.New()
	put(h, "version", Version)
	put(h, "executable", f.Executable)
	put(h, "root", f.Root)
	put(h, "head", f.Head)
	for _, entry := range f.Index {
		put(h, "index", entry)
	}
	for _, e := range f.Entries {
		put(h, "entry", e.Path, string(e.Class), string(e.Kind), e.Value, e.Reach)
	}
	for _, v := range f.Environment {
		put(h, "environment", v.Name, strconv.FormatBool(v.Set), v.Value)
	}
	for _, v := range f.Settings {
		put(h, "setting", v.Name, strconv.FormatBool(v.Set), v.Value)
	}
	for _, fact := range f.Host {
		put(h, "host", fact.Name, fact.Value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sources are the facts without the paths a step can write and Git does not
// see: ignored files, FIFOs, sockets and devices (the stat-keyed paths), and
// directories. They keep every tracked and untracked file and link, HEAD,
// the index, the environment, the settings and the host.
func (f *Facts) sources() *Facts {
	kept := *f
	kept.Entries = nil
	for _, e := range f.Entries {
		if e.Kind != Directory && e.Kind != Other && e.Class != Ignored {
			kept.Entries = append(kept.Entries, e)
		}
	}
	return &kept
}

// put writes one record of the key: a label and length-prefixed parts, so no
// two different fact lists encode alike.
func put(h hash.Hash, parts ...string) {
	var buffer []byte
	for _, part := range parts {
		buffer = strconv.AppendInt(buffer, int64(len(part)), 10)
		buffer = append(buffer, ':')
		buffer = append(buffer, part...)
	}
	h.Write(append(buffer, '\n'))
}

func (f *Facts) sort() {
	slices.Sort(f.Index)
	slices.SortFunc(f.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	byName := func(a, b Variable) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(f.Environment, byName)
	slices.SortFunc(f.Settings, byName)
	slices.SortFunc(f.Host, func(a, b Fact) int { return strings.Compare(a.Name, b.Name) })
}

// Problem is the first reason the facts cannot key a replay or a record, or
// "" when there is none.
func (f *Facts) Problem() string {
	if len(f.Unreplayable) > 0 {
		return f.Unreplayable[0]
	}
	return ""
}

// Diff names, for a log, up to limit inputs that differ between a and b.
func Diff(a, b *Facts, limit int) []string {
	var differ []string
	note := func(format string, args ...any) {
		if len(differ) < limit {
			differ = append(differ, fmt.Sprintf(format, args...))
		}
	}
	scalar := func(name, x, y string) {
		if x != y {
			note("%s", name)
		}
	}
	scalar("the gate executable", a.Executable, b.Executable)
	scalar("the checkout root", a.Root, b.Root)
	scalar("HEAD", a.Head, b.Head)
	if !slices.Equal(a.Index, b.Index) {
		note("the index")
	}
	entries := map[string]Entry{}
	for _, e := range a.Entries {
		entries[e.Path] = e
	}
	seen := map[string]bool{}
	for _, e := range b.Entries {
		seen[e.Path] = true
		if old, ok := entries[e.Path]; !ok {
			note("%s (added)", e.Path)
		} else if old != e {
			note("%s", e.Path)
		}
	}
	for _, e := range a.Entries {
		if !seen[e.Path] {
			note("%s (removed)", e.Path)
		}
	}
	variables := func(kind string, x, y []Variable) {
		values := map[string]Variable{}
		for _, v := range x {
			values[v.Name] = v
		}
		names := map[string]bool{}
		for _, v := range y {
			names[v.Name] = true
			if values[v.Name] != v {
				note("%s %s", kind, v.Name)
			}
		}
		for _, v := range x {
			if !names[v.Name] {
				note("%s %s", kind, v.Name)
			}
		}
	}
	variables("variable", a.Environment, b.Environment)
	variables("setting", a.Settings, b.Settings)
	facts := map[string]string{}
	for _, fact := range a.Host {
		facts[fact.Name] = fact.Value
	}
	names := map[string]bool{}
	for _, fact := range b.Host {
		names[fact.Name] = true
		if value, ok := facts[fact.Name]; !ok || value != fact.Value {
			note("host fact %s", fact.Name)
		}
	}
	for _, fact := range a.Host {
		if !names[fact.Name] {
			note("host fact %s", fact.Name)
		}
	}
	return differ
}
