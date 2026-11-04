package replay

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKeyFailsClosed is a property over generated trees, over spec Do 8's
// "what it reaches": for every kind of entry in every class, and for every
// kind of link target, relative and absolute, in the checkout, in a keyed
// root or outside, a change to the content reached through the entry
// either changes the whole-gate key, or leaves facts that cannot key a
// replay and whose problem names the entry (a record at their key does not
// replay, and a passed run records nothing), or names the entry as
// recently changed (so no run records under it). A key that stays the same
// and still replays never occurs.
//
// When what the entry reaches holds a FIFO, a socket or a device (KindOf),
// at the entry itself, below a directory, at a link's target or below it,
// in the checkout or in a keyed root, the facts must name the entry and
// refuse replay and record before and after the change, whatever the key
// does. Held FIFOs keep their inode while their buffer fills or drains. The
// keyed root's fingerprint is the real one (InstallationDigest), and a
// fingerprint failure makes the facts unreplayable, as collectHost does.
// (Round-2 finding B2: a FIFO in GOROOT, reached through a link in the
// checkout, replayed a pass that read its buffer.)
func TestKeyFailsClosed(t *testing.T) {
	cases := failClosedCases()
	checked := 0
	for seed := uint64(1); seed <= 6; seed++ {
		for _, c := range cases {
			for _, class := range classes {
				for _, absolute := range []bool{false, true} {
					if !c.link && absolute {
						continue
					}
					name := fmt.Sprintf("seed %d %s %s absolute=%t", seed, c.name, class, absolute)
					if err := checkFailsClosed(t, seed, c, class, absolute); err != nil {
						t.Errorf("%s: %v", name, err)
					}
					checked++
				}
			}
		}
	}
	t.Logf("%d cases over %d entry kinds and link targets", checked, len(cases))
}

// failCase builds one entry named "entry" in a tree and changes what is
// reached through it.
type failCase struct {
	name string
	link bool
	// build makes the entry; target is the path a link names, relative to
	// the checkout root when the case uses a relative link.
	build func(f *failTree) (target string)
	// change changes the content reached through the entry.
	change func(f *failTree)
	// special says that what the entry reaches holds a file KindOf
	// refuses: the facts must refuse replay and record before and after
	// the change.
	special bool
	// named, for a file a step reaches through a keyed root's tool rather
	// than through the checkout, is what a problem must name instead of
	// the entry: the file's path and kind, as the fingerprint states them.
	named func(f *failTree) string
}

type failTree struct {
	t                    *testing.T
	root, keyed, outside string
	// extra are keyed roots beside keyed, such as a device file.
	extra []string
	// held are descriptors a case keeps open until the check ends.
	held []*os.File
	// listeners are sockets a case keeps bound until the check ends.
	listeners []net.Listener
}

// holdFIFO makes a FIFO and holds it open for reading and writing, so its
// buffer can change while its inode stays.
func (f *failTree) holdFIFO(name string) *os.File {
	mkfifo(name)
	pipe, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		panic(err)
	}
	f.held = append(f.held, pipe)
	return pipe
}

// heldFIFO holds a FIFO at name, with content in its buffer.
func (f *failTree) heldFIFO(name, content string) {
	mkdir(filepath.Dir(name))
	pipe := f.holdFIFO(name)
	if content != "" {
		if _, err := pipe.Write([]byte(content)); err != nil {
			panic(err)
		}
	}
}

// drain reads the bytes the last held FIFO buffers (fewer than 64, so one
// read takes them all; a read of an empty FIFO waits in Go's poller); fill
// writes to it. Neither moves its inode or a stat field a key could hold.
func (f *failTree) drain() {
	if _, err := f.held[len(f.held)-1].Read(make([]byte, 64)); err != nil {
		panic(err)
	}
}

func (f *failTree) fill() {
	if _, err := f.held[len(f.held)-1].Write([]byte("bad\n")); err != nil {
		panic(err)
	}
}

// bind binds a unix socket at dir/name. The address is the relative name
// from dir, since a socket's address holds at most 104 bytes on darwin and
// dir can be long; the socket stays bound until the check ends, and its
// file is left for the temporary directory's removal.
func (f *failTree) bind(dir, name string) {
	mkdir(dir)
	f.t.Chdir(dir)
	listener, err := net.Listen("unix", name)
	if err != nil {
		panic(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	f.listeners = append(f.listeners, listener)
}

func (f *failTree) write(name, content string, perm fs.FileMode) {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(name, []byte(content), perm); err != nil {
		panic(err)
	}
	if err := os.Chmod(name, perm); err != nil {
		panic(err)
	}
}

func (f *failTree) in(name string) string      { return filepath.Join(f.root, name) }
func (f *failTree) out(name string) string     { return filepath.Join(f.outside, name) }
func (f *failTree) keyedAt(name string) string { return filepath.Join(f.keyed, name) }

func failClosedCases() []failCase {
	rewrite := func(path func(f *failTree) string) func(f *failTree) {
		return func(f *failTree) {
			name := path(f)
			content, _ := os.ReadFile(name)
			// The same size, other bytes: a stat tuple sees it only by time.
			changed := []byte(strings.Repeat("y", len(content)))
			if string(changed) == string(content) {
				changed = []byte(strings.Repeat("z", len(content)))
			}
			if err := os.WriteFile(name, changed, 0o644); err != nil {
				panic(err)
			}
		}
	}
	addIn := func(dir func(f *failTree) string) func(f *failTree) {
		return func(f *failTree) { f.write(filepath.Join(dir(f), "added"), "new\n", 0o644) }
	}
	return []failCase{
		{name: "regular file", build: func(f *failTree) string { f.write(f.in("entry"), "content\n", 0o644); return "" },
			change: rewrite(func(f *failTree) string { return f.in("entry") })},
		{name: "executable", build: func(f *failTree) string { f.write(f.in("entry"), "#!/bin/sh\n", 0o755); return "" },
			change: rewrite(func(f *failTree) string { return f.in("entry") })},
		{name: "empty directory", build: func(f *failTree) string { mkdir(f.in("entry")); return "" },
			change: addIn(func(f *failTree) string { return f.in("entry") })},
		{name: "directory", build: func(f *failTree) string { f.write(f.in("entry/inner"), "x\n", 0o644); return "" },
			change: rewrite(func(f *failTree) string { return f.in("entry/inner") })},
		{name: "fifo", build: func(f *failTree) string { mkfifo(f.in("entry")); return "" },
			change: func(f *failTree) { remove(f.in("entry")); mkfifo(f.in("entry")) }},
		// The same inode, other bytes in its buffer: no stat field moves.
		{name: "fifo drained", build: func(f *failTree) string {
			if _, err := f.holdFIFO(f.in("entry")).Write([]byte("good\n")); err != nil {
				panic(err)
			}
			return ""
		}, change: func(f *failTree) {
			if _, err := f.held[0].Read(make([]byte, 16)); err != nil {
				panic(err)
			}
		}},
		{name: "fifo buffered", build: func(f *failTree) string { f.holdFIFO(f.in("entry")); return "" },
			change: func(f *failTree) {
				if _, err := f.held[0].Write([]byte("bad\n")); err != nil {
					panic(err)
				}
			}},
		{name: "link to a file in the checkout", link: true,
			build:  func(f *failTree) string { f.write(f.in("target"), "content\n", 0o644); return f.in("target") },
			change: rewrite(func(f *failTree) string { return f.in("target") })},
		{name: "link to a directory in the checkout", link: true,
			build:  func(f *failTree) string { f.write(f.in("dir/inner"), "x\n", 0o644); return f.in("dir") },
			change: addIn(func(f *failTree) string { return f.in("dir") })},
		{name: "link to a file in a keyed root", link: true,
			build:  func(f *failTree) string { return f.keyedAt("tool") },
			change: rewrite(func(f *failTree) string { return f.keyedAt("tool") })},
		{name: "link to a file outside", link: true,
			build:  func(f *failTree) string { f.write(f.out("file"), "content\n", 0o644); return f.out("file") },
			change: rewrite(func(f *failTree) string { return f.out("file") })},
		{name: "link to a directory outside", link: true,
			build:  func(f *failTree) string { f.write(f.out("dir/inner"), "x\n", 0o644); return f.out("dir") },
			change: addIn(func(f *failTree) string { return f.out("dir") })},
		{name: "dangling link inside", link: true,
			build:  func(f *failTree) string { return f.in("absent") },
			change: func(f *failTree) { f.write(f.in("absent"), "now here\n", 0o644) }},
		{name: "dangling link outside", link: true,
			build:  func(f *failTree) string { return f.out("absent") },
			change: func(f *failTree) { f.write(f.out("absent"), "now here\n", 0o644) }},
		{name: "chain out and back", link: true,
			build: func(f *failTree) string {
				f.write(f.in("target"), "content\n", 0o644)
				mkdir(f.outside)
				symlink(f.in("target"), f.out("hop"))
				return f.out("hop")
			},
			change: func(f *failTree) {
				// The hop outside now leads elsewhere.
				f.write(f.in("other"), "content\n", 0o644)
				remove(f.out("hop"))
				symlink(f.in("other"), f.out("hop"))
			}},
		{name: "loop", link: true,
			build: func(f *failTree) string {
				symlink(f.in("entry"), f.in("loop"))
				return f.in("loop")
			},
			change: func(f *failTree) { f.write(f.in("unrelated"), "x\n", 0o644) }},
		{name: "link into .git", link: true,
			build:  func(f *failTree) string { f.write(f.in(".git/HEAD"), "ref: a\n", 0o644); return f.in(".git/HEAD") },
			change: rewrite(func(f *failTree) string { return f.in(".git/HEAD") })},
		{name: "link to a file in a keyed root's directory", link: true,
			build:  func(f *failTree) string { f.write(f.keyedAt("lib/file"), "lib v1\n", 0o644); return f.keyedAt("lib") },
			change: rewrite(func(f *failTree) string { return f.keyedAt("lib/file") })},

		// What the entry reaches holds a file KindOf refuses.
		{name: "fifo in a directory, drained", special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.in("entry/inner/stream"), "good\n"); return "" },
			change: (*failTree).drain},
		{name: "fifo in a directory, filled", special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.in("entry/inner/stream"), ""); return "" },
			change: (*failTree).fill},
		{name: "socket", special: true,
			build:  func(f *failTree) string { f.bind(f.root, "entry"); return "" },
			change: func(*failTree) {}},
		{name: "socket in a directory", special: true,
			build:  func(f *failTree) string { f.bind(f.in("entry"), "sock"); return "" },
			change: func(*failTree) {}},
		{name: "link to a fifo in the checkout", link: true, special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.in("stream"), "good\n"); return f.in("stream") },
			change: (*failTree).drain},
		{name: "link to a directory in the checkout holding a fifo", link: true, special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.in("dir/stream"), ""); return f.in("dir") },
			change: (*failTree).fill},
		{name: "link to a fifo in a keyed root, drained", link: true, special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.keyedAt("stream"), "good\n"); return f.keyedAt("stream") },
			change: (*failTree).drain},
		{name: "link to a fifo in a keyed root, filled", link: true, special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.keyedAt("stream"), ""); return f.keyedAt("stream") },
			change: (*failTree).fill},
		{name: "link to a directory in a keyed root holding a fifo", link: true, special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.keyedAt("lib/deep/stream"), "good\n"); return f.keyedAt("lib") },
			change: (*failTree).drain},
		{name: "link to a socket in a keyed root", link: true, special: true,
			build:  func(f *failTree) string { f.bind(f.keyed, "sock"); return f.keyedAt("sock") },
			change: func(*failTree) {}},
		{name: "link to a device in a keyed root", link: true, special: true,
			build:  func(f *failTree) string { f.extra = append(f.extra, os.DevNull); return os.DevNull },
			change: func(*failTree) {}},
		{name: "link to a directory in a keyed root holding a link to a device", link: true, special: true,
			build: func(f *failTree) string {
				f.extra = append(f.extra, os.DevNull)
				mkdir(f.keyedAt("lib"))
				symlink(os.DevNull, f.keyedAt("lib/null"))
				return f.keyedAt("lib")
			},
			change: func(*failTree) {}},
		{name: "link to a device outside", link: true, special: true,
			build:  func(f *failTree) string { return os.DevNull },
			change: func(*failTree) {}},
		{name: "chain through a keyed root to a fifo", link: true, special: true,
			build: func(f *failTree) string {
				f.heldFIFO(f.keyedAt("stream"), "good\n")
				symlink(f.keyedAt("stream"), f.keyedAt("hop"))
				return f.keyedAt("hop")
			},
			change: (*failTree).drain},
		{name: "chain through the checkout to a fifo in a keyed root", link: true, special: true,
			build: func(f *failTree) string {
				f.heldFIFO(f.keyedAt("stream"), "")
				symlink("../goroot/stream", f.in("hop"))
				return f.in("hop")
			},
			change: (*failTree).fill},

		// A file under a keyed root that a step reaches through the tool,
		// not through the checkout: the fingerprint is the reader.
		{name: "fifo in a keyed root, drained", special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.keyedAt("stream"), "good\n"); return "" },
			change: (*failTree).drain,
			named:  func(f *failTree) string { return f.keyedAt("stream") + " is a FIFO" }},
		{name: "fifo in a keyed root's directory, filled", special: true,
			build:  func(f *failTree) string { f.heldFIFO(f.keyedAt("lib/deep/stream"), ""); return "" },
			change: (*failTree).fill,
			named:  func(f *failTree) string { return f.keyedAt("lib/deep/stream") + " is a FIFO" }},
		{name: "socket in a keyed root", special: true,
			build:  func(f *failTree) string { f.bind(f.keyed, "sock"); return "" },
			change: func(*failTree) {},
			named:  func(f *failTree) string { return f.keyedAt("sock") + " is a socket" }},
		{name: "device as a keyed root", special: true,
			build:  func(f *failTree) string { f.extra = append(f.extra, os.DevNull); return "" },
			change: func(*failTree) {},
			named:  func(*failTree) string { return os.DevNull + " is a character device" }},
		{name: "link in a keyed root to a device", special: true,
			build: func(f *failTree) string {
				mkdir(f.keyedAt("lib"))
				symlink(os.DevNull, f.keyedAt("lib/null"))
				return ""
			},
			change: func(*failTree) {},
			named:  func(f *failTree) string { return f.keyedAt("lib/null") + " is a character device" }},
	}
}

func mkdir(name string) {
	if err := os.MkdirAll(name, 0o755); err != nil {
		panic(err)
	}
}

func remove(name string) {
	if err := os.Remove(name); err != nil {
		panic(err)
	}
}

func symlink(target, name string) {
	if err := os.Symlink(target, name); err != nil {
		panic(err)
	}
}

func mkfifo(name string) {
	if err := syscall.Mkfifo(name, 0o644); err != nil {
		panic(err)
	}
}

func checkFailsClosed(t *testing.T, seed uint64, c failCase, class Class, absolute bool) error {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		return err
	}
	f := &failTree{t: t, root: filepath.Join(base, "checkout"), keyed: filepath.Join(base, "goroot"), outside: filepath.Join(base, "outside")}
	defer func() {
		for _, held := range f.held {
			held.Close()
		}
		for _, listener := range f.listeners {
			listener.Close()
		}
	}()
	mkdir(f.root)
	mkdir(f.outside)
	f.write(f.keyedAt("tool"), "tool v1\n", 0o755)
	// A generated tree around the entry.
	rng := rand.New(rand.NewPCG(seed, 7))
	for i := range 1 + rng.IntN(5) {
		f.write(f.in(fmt.Sprintf("%s/file%d", pick(rng, []string{"src", "docs", "dist", "."}), i)), pick(rng, contents), pick(rng, []fs.FileMode{0o644, 0o755, 0o600}))
	}
	target := c.build(f)
	if c.link {
		if !absolute {
			relative, err := filepath.Rel(f.root, target)
			if err != nil {
				return err
			}
			target = relative
		}
		symlink(target, f.in("entry"))
	}
	// Every path takes the generated class; the entry and what it reaches
	// are what the property is about.
	classify := func(string) Class { return class }
	// Old stat times: a stat-keyed file then changes by its times too.
	old := time.Now().Add(-time.Hour)
	_ = filepath.WalkDir(f.root, func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type()&fs.ModeSymlink == 0 {
			_ = os.Chtimes(name, old, old)
		}
		return nil
	})
	// The facts as collectFacts composes them: the checkout, the keyed
	// roots' fingerprints (a failure is a problem, as collectHost makes it),
	// then the links against those roots.
	collect := func() (*Facts, error) {
		entries, recent, unreplayable, err := Walk(f.root, classify, time.Now())
		if err != nil {
			return nil, err
		}
		roots := append([]string{f.keyed}, f.extra...)
		var host []Fact
		for i, root := range roots {
			digest, err := InstallationDigest(root, f.root)
			if err != nil {
				unreplayable = append(unreplayable, fmt.Sprintf("program tool%d cannot be fingerprinted: %v", i, err))
				digest = "unlocated"
			}
			host = append(host, Fact{fmt.Sprintf("program tool%d", i), digest})
		}
		links, err := NewLinks(f.root, roots)
		if err != nil {
			return nil, err
		}
		unreplayable = append(unreplayable, links.FollowLinks(entries)...)
		return &Facts{Root: f.root, Entries: entries, Host: host, Unreplayable: unreplayable, Recent: recent}, nil
	}
	// names reports whether a problem names the entry, by its path in the
	// checkout: the entry, a path below it, or the link it is.
	names := func(facts *Facts) bool {
		return slices.ContainsFunc(facts.Unreplayable, func(p string) bool {
			if c.named != nil {
				return strings.Contains(p, c.named(f))
			}
			return strings.HasPrefix(p, "entry ") || strings.HasPrefix(p, "entry/")
		})
	}
	before, err := collect()
	if err != nil {
		return err
	}
	if c.special {
		if !names(before) {
			return fmt.Errorf("before the change, no problem names the entry (problems %q)", before.Unreplayable)
		}
		if err := refuses(t, before, collect); err != nil {
			return fmt.Errorf("before the change: %v", err)
		}
	}
	c.change(f)
	after, err := collect()
	if err != nil {
		return err
	}
	if c.special {
		if !names(after) {
			return fmt.Errorf("after the change, no problem names the entry (problems %q)", after.Unreplayable)
		}
		if err := refuses(t, after, collect); err != nil {
			return fmt.Errorf("after the change: %v", err)
		}
		return nil
	}
	if after.Key() != before.Key() {
		return nil
	}
	if names(after) && names(before) {
		return refuses(t, after, collect)
	}
	if slices.ContainsFunc(after.Recent, func(p string) bool { return p == "entry" || strings.HasPrefix(p, "entry/") }) {
		return nil
	}
	return fmt.Errorf("the key stayed %s and the facts still replay (problems %q, recent %q)", after.Key()[:12], after.Unreplayable, after.Recent)
}

// TestRecentChangesAreNotRecorded: a stat-keyed path changed within
// RecentWindow of a collection is named recent, and a run whose facts name
// one records nothing.
func TestRecentChangesAreNotRecorded(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "ignored")
	if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := func(string) Class { return Ignored }
	_, recent, _, err := Walk(root, ignored, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recent, []string{"ignored"}) {
		t.Fatalf("recent %q right after a write, want [ignored]", recent)
	}
	_, recent, _, err = Walk(root, ignored, time.Now().Add(RecentWindow+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("recent %q after the window, want none", recent)
	}
	store := Open(t.TempDir())
	collect := func() (*Facts, error) {
		entries, recent, _, err := Walk(root, ignored, time.Now())
		return &Facts{Root: root, Entries: entries, Recent: recent}, err
	}
	start, err := collect()
	if err != nil {
		t.Fatal(err)
	}
	run := store.Begin(start)
	object, err := run.Output()
	if err != nil {
		t.Fatal(err)
	}
	run.Passed("step", object, "ran", 1, time.Now())
	line := run.Finish(true, collect, Meta{Now: time.Now()})
	if !strings.Contains(line, "no whole-gate record: ignored changed within 2s") {
		t.Fatalf("finish said %q", line)
	}
	if _, err := store.Load(start.Key()); err == nil {
		t.Fatal("a run with a recent path recorded")
	}
}

// TestMergeTierAdmitsOnlyItsOwnWrites: the uncached run's steps may write
// ignored files and directories into the checkout, and the merge tier still
// compares; a source, the index, HEAD, the environment or the host that
// changed during the run fails it, and leaves the record in place.
func TestMergeTierAdmitsOnlyItsOwnWrites(t *testing.T) {
	base := func() *Facts {
		return &Facts{
			Root: "/checkout", Head: "h", Index: []string{"100644 b 0\tsrc/a.go"},
			Entries: []Entry{
				{Path: "src", Kind: Directory},
				{Path: "src/a.go", Class: Tracked, Kind: File, Value: "-r--r--r--:a"},
				{Path: "notes.txt", Class: Untracked, Kind: File, Value: "-r--r--r--:n"},
			},
			Environment: []Variable{{"GOFLAGS", false, ""}},
			Host:        []Fact{{"cpus", "16"}},
		}
	}
	wrote := func(f *Facts) {
		f.Entries = append(f.Entries, Entry{Path: "dist", Kind: Directory}, Entry{Path: "dist/main", Class: Ignored, Kind: File, Value: "1:2:3:4:-rwxr-xr-x"})
	}
	for _, c := range []struct {
		name   string
		change func(*Facts)
		code   int
		line   string
	}{
		{"no change", func(*Facts) {}, 0, "merge: verdicts equal (1 steps)\n"},
		{"the run wrote ignored files", wrote, 0, "merge: the run wrote into the checkout: dist (added), dist/main (added)\n"},
		{"a tracked file changed beside the writes", func(f *Facts) { wrote(f); f.Entries[1].Value = "-r--r--r--:b" }, 1, "merge: FAILED: the checkout or the host changed during the merge run: dist (added), dist/main (added), src/a.go\n"},
		{"an untracked file appeared", func(f *Facts) {
			f.Entries = append(f.Entries, Entry{Path: "src/b.go", Class: Untracked, Kind: File, Value: "-r--r--r--:c"})
		}, 1, "merge: FAILED: the checkout or the host changed during the merge run: src/b.go (added)"},
		{"the index changed", func(f *Facts) { f.Index = append(f.Index, "100644 c 0\tsrc/b.go") }, 1, "merge: FAILED: the checkout or the host changed during the merge run: the index"},
		{"HEAD moved", func(f *Facts) { f.Head = "h2" }, 1, "merge: FAILED: the checkout or the host changed during the merge run: HEAD"},
		{"a variable changed", func(f *Facts) { f.Environment[0] = Variable{"GOFLAGS", true, "-count=1"} }, 1, "merge: FAILED: the checkout or the host changed during the merge run: variable GOFLAGS"},
		{"a host fact changed", func(f *Facts) { f.Host[0].Value = "8" }, 1, "merge: FAILED: the checkout or the host changed during the merge run: host fact cpus"},
	} {
		store := Open(t.TempDir())
		start := base()
		key := start.Key()
		run := store.Begin(start)
		object, err := run.Output()
		if err != nil {
			t.Fatal(err)
		}
		run.Passed("step", object, "ran", 1, time.Now())
		if line := run.Finish(true, func() (*Facts, error) { return base(), nil }, Meta{Now: time.Now()}); !strings.HasPrefix(line, "gate: recorded") {
			t.Fatalf("%s: the inner run did not record: %q", c.name, line)
		}
		collected := 0
		tier := MergeTier{
			Store: store,
			Collect: func() (*Facts, error) {
				collected++
				f := base()
				if collected > 1 {
					c.change(f)
				}
				return f, nil
			},
			Inner:    func() bool { t.Fatalf("%s: the inner tier ran with a record", c.name); return false },
			Uncached: func() ([]string, map[string]Verdict) { return []string{"step"}, map[string]Verdict{"step": Pass} },
		}
		var out strings.Builder
		code := tier.Run(&out)
		if code != c.code || !strings.Contains(out.String(), c.line) {
			t.Errorf("%s: exit %d, want %d with %q:\n%s", c.name, code, c.code, c.line, out.String())
		}
		if _, err := store.Load(key); err != nil {
			t.Errorf("%s: the record at the start key is gone: %v", c.name, err)
		}
	}
}

// TestMergeComparisonCoversEveryStep: a step the record holds that is not
// declared, or declared and not in the record, is a differing row.
func TestMergeComparisonCoversEveryStep(t *testing.T) {
	record := &Record{Steps: []Step{{Name: "a"}, {Name: "gone"}}}
	c := Compare([]string{"a", "new"}, map[string]Verdict{"a": Pass, "new": Pass}, record)
	want := []string{
		"merge: a: inner pass, uncached pass",
		"merge: new: inner none, uncached pass",
		"merge: gone: inner pass, uncached not run",
	}
	if !slices.Equal(c.Rows, want) || !slices.Equal(c.Differ, []string{"new", "gone"}) {
		t.Fatalf("rows %q differ %q", c.Rows, c.Differ)
	}
	if c.Summary() != "merge: FAILED: verdicts differ: new, gone" {
		t.Fatalf("summary %q", c.Summary())
	}
	equal := Compare([]string{"a"}, map[string]Verdict{"a": Pass}, &Record{Steps: []Step{{Name: "a"}}})
	if equal.Summary() != "merge: verdicts equal (1 steps)" {
		t.Fatalf("summary %q", equal.Summary())
	}
}
