package closure

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// linkLayout is a module root with symbolic links in a package's testdata,
// in an unrelated directory, and outside the root, for the properties
// below. Paths are relative to base; root is base/root and the package is
// root/internal/a.
type linkLayout struct {
	base, root, pwd string
	links           []string // every link, relative to base
}

// brokenTarget is where the link oracle points a link to break it.
const brokenTarget = "/nonexistent/loop-probe-x"

func newLinkLayout(t *testing.T) linkLayout {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := linkLayout{base: base, root: filepath.Join(base, "root")}
	l.pwd = filepath.Join(l.root, "internal", "a")
	for _, f := range []string{"root/go.mod", "root/internal/a/a.go", "root/internal/a/testdata/own.txt", "root/internal/a/testdata/sub/x.txt", "root/internal/b/b.txt", "root/docs/tooling.md", "root/docs/research/r.md", "ext/plain.txt"} {
		name := filepath.Join(base, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []struct{ at, target string }{
		{"root/internal/a/testdata/out", "../../../docs/research"},
		{"root/internal/a/testdata/outabs", filepath.Join(l.root, "docs", "research")},
		{"root/internal/a/testdata/in", "sub"},
		{"root/internal/a/testdata/chain", "out"},
		{"root/internal/a/testdata/up", ".."},
		{"root/internal/a/testdata/upup", "../.."},
		{"root/internal/a/testdata/dangle", "../../../docs/none"},
		{"root/internal/a/testdata/loop1", "loop2"},
		{"root/internal/a/testdata/loop2", "loop1"},
		{"root/internal/a/testdata/file", "own.txt"},
		{"root/docs/research/back", "../../internal/a/testdata"},
		{"ext/into-own", filepath.Join(l.root, "internal", "a", "testdata")},
		{"ext/into-docs", "../root/docs"},
	} {
		if err := os.Symlink(filepath.FromSlash(link.target), filepath.Join(base, filepath.FromSlash(link.at))); err != nil {
			t.Fatal(err)
		}
		l.links = append(l.links, link.at)
	}
	return l
}

// paths generates raw recorded paths: a start that names the testdata
// directory (relative, absolute, through a link in testdata, through a link
// in docs, through a link outside the root), a member (a link of each kind,
// a directory, a file, an absent name) and a tail of parent traversals.
func (l linkLayout) paths() []string {
	starts := []string{
		"testdata",
		"./testdata",
		filepath.Join(l.root, "internal", "a", "testdata"),
		"../../docs/research/back",
		filepath.Join(l.base, "ext", "into-own"),
		"testdata/up/testdata",
		filepath.Join(l.base, "ext", "into-docs") + "/research/back",
	}
	members := []string{"", "/out", "/outabs", "/in", "/chain", "/up", "/upup", "/dangle", "/loop1", "/sub", "/missing", "/own.txt", "/file"}
	tails := []string{"", "/", "/..", "/../..", "/../tooling.md", "/r.md", "/x.txt", "/../own.txt", "/../research/r.md", "/../../internal/b/b.txt", "/a.go", "/testdata/own.txt", "/missing.txt", "/./r.md", "/../../go.mod"}
	var out []string
	for _, s := range starts {
		for _, m := range members {
			for _, tail := range tails {
				out = append(out, s+m+tail)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// abs is what the kernel resolves for a raw path recorded in pwd: the raw
// string, never cleaned.
func (l linkLayout) abs(raw string) string {
	if filepath.IsAbs(raw) {
		return raw
	}
	return l.pwd + "/" + raw
}

// kernel is the kernel's answer for one path: what os.Stat reached.
type kernel struct {
	info fs.FileInfo
	err  error
}

func (l linkLayout) stat(raws []string) []kernel {
	out := make([]kernel, len(raws))
	for i, raw := range raws {
		out[i].info, out[i].err = os.Stat(l.abs(raw))
	}
	return out
}

func (k kernel) same(o kernel) bool {
	if k.err != nil || o.err != nil {
		return (k.err == nil) == (o.err == nil)
	}
	return os.SameFile(k.info, o.info)
}

// dependsOn is the link oracle: for each link, the paths whose kernel
// result changes when that link alone points nowhere. A path the kernel
// resolves depends on exactly the links it traverses.
func (l linkLayout) dependsOn(t *testing.T, raws []string, before []kernel) map[string][]string {
	t.Helper()
	deps := map[string][]string{}
	for _, link := range l.links {
		at := filepath.Join(l.base, filepath.FromSlash(link))
		target, err := os.Readlink(at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(at); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(brokenTarget, at); err != nil {
			t.Fatal(err)
		}
		after := l.stat(raws)
		if err := os.Remove(at); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
		for i, raw := range raws {
			if !before[i].same(after[i]) {
				deps[raw] = append(deps[raw], at)
			}
		}
	}
	return deps
}

// TestWalkReachesWhatTheKernelReaches is the resolution property: over
// generated paths with links (relative, absolute, chained, to a file, to a
// parent, dangling, looping) and parent traversals after them, the walk
// reaches the object os.Stat reaches, and names exactly the links whose
// breaking changes the kernel's answer.
func TestWalkReachesWhatTheKernelReaches(t *testing.T) {
	l := newLinkLayout(t)
	raws := l.paths()
	before := l.stat(raws)
	deps := l.dependsOn(t, raws, before)
	res := newResolver()
	resolved, failed := 0, 0
	for i, raw := range raws {
		k := before[i]
		w := res.walk(l.abs(raw))
		if k.err != nil {
			failed++
			if w.exists {
				t.Errorf("%s: the kernel finds nothing (%v), the walk reached %s", raw, k.err, w.reached)
			}
			if errors.Is(k.err, syscall.ELOOP) && w.lost == "" {
				t.Errorf("%s: the kernel reports a loop, the walk resolved it to %s", raw, w.reached)
			}
			continue
		}
		resolved++
		if w.lost != "" || !w.exists {
			t.Errorf("%s: the kernel reaches %s, the walk is lost (%s) or absent at %s", raw, k.info.Name(), w.lost, w.reached)
			continue
		}
		info, err := os.Lstat(w.reached)
		if err != nil || !os.SameFile(info, k.info) {
			t.Errorf("%s: the walk reached %s, which is not what the kernel reaches (%v)", raw, w.reached, err)
		}
		if w.dir != k.info.IsDir() {
			t.Errorf("%s: the walk says directory=%v, the kernel %v", raw, w.dir, k.info.IsDir())
		}
		got := slices.Clone(w.links)
		slices.Sort(got)
		got = slices.Compact(got)
		want := deps[raw]
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the walk traverses links %v, the kernel depends on %v", raw, got, want)
		}
	}
	t.Logf("%d paths resolved, %d not", resolved, failed)
	if resolved < 300 || failed < 300 {
		t.Errorf("only %d resolved and %d failed paths; the generator lost its coverage", resolved, failed)
	}
}

// TestClassificationFollowsReachedObjects is the classification property:
// for each generated path the kernel resolves, recorded as an open or a
// stat, the check reports it outside the package's closure exactly when
// the object the kernel reaches is outside (by file identity, against an
// independent walk of what the package may read), or when the path
// traverses a link that lies in the checkout outside the package's
// testdata (the link oracle).
func TestClassificationFollowsReachedObjects(t *testing.T) {
	l := newLinkLayout(t)
	raws := l.paths()
	before := l.stat(raws)
	deps := l.dependsOn(t, raws, before)
	may := l.mayRead(t)
	c := newClosure("internal/a", nil, nil)
	for i, raw := range raws {
		k := before[i]
		if k.err != nil {
			continue // pinned by TestLostWalksFailClosed
		}
		for _, op := range []string{opOpen, opStat} {
			inside := may.covers(op, k.info)
			for _, link := range deps[raw] {
				rel, err := filepath.Rel(l.root, link)
				if err != nil {
					t.Fatal(err)
				}
				inRoot := rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
				if inRoot && !strings.HasPrefix(filepath.ToSlash(rel), "internal/a/testdata/") {
					inside = false
				}
			}
			entries, err := readLog([]byte("# test log\n"+op+" "+raw+"\n"), l.pwd, []string{l.root}, "")
			if err != nil {
				t.Fatal(err)
			}
			outside := c.check(entries, map[string]bool{})
			if inside != (len(outside) == 0) {
				t.Errorf("%s %s: reaches %s (inside=%v by identity and links), classified %v, outside %v", op, raw, k.info.Name(), inside, entries, outside)
			}
		}
	}
}

// readable is what the package internal/a of a linkLayout may read, found
// by walking the tree without following links: the package directory and
// its direct members, everything in its testdata, the module file; and,
// for a lookup only, the directories above the package. An object outside
// the root (not in all) is no input of the checkout.
type readable struct{ any, lookup, all []fs.FileInfo }

func (l linkLayout) mayRead(t *testing.T) readable {
	t.Helper()
	var r readable
	err := filepath.WalkDir(l.root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a link is not an object a path reaches
		}
		rel, _ := filepath.Rel(l.root, name)
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		r.all = append(r.all, info)
		parent := rel[:max(0, strings.LastIndex(rel, "/"))]
		switch {
		case rel == "internal/a", parent == "internal/a", strings.HasPrefix(rel, "internal/a/testdata/"), rel == "go.mod":
			r.any = append(r.any, info)
		case rel == ".", rel == "internal":
			r.lookup = append(r.lookup, info)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r readable) covers(op string, info fs.FileInfo) bool {
	same := func(list []fs.FileInfo) bool {
		return slices.ContainsFunc(list, func(o fs.FileInfo) bool { return os.SameFile(o, info) })
	}
	return !same(r.all) || same(r.any) || op == opStat && same(r.lookup)
}

// TestLostWalksFailClosed pins the paths the kernel cannot resolve, and a
// chdir through a link: an absent name is classified where the walk looks
// for it; a ".." after an absent component or a file, and a loop, fail
// closed, wherever lexical cleaning would put them; a walk lost in scratch
// outside the root is dropped only when the caller names that scratch and
// the walk never entered the root. A walk that leaves the root after it
// entered it fails closed, wherever it ends: the part outside the root is
// writable during the sweep, so it may have led elsewhere when the test
// read it.
func TestLostWalksFailClosed(t *testing.T) {
	l := newLinkLayout(t)
	c := newClosure("internal/a", nil, nil)
	gone := filepath.Join(l.base, "ext", "gone") + "/../../root/docs/tooling.md"
	if err := os.Symlink(filepath.Join(l.base, "ext"), filepath.Join(l.pwd, "testdata", "through-ext")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		log     string
		scratch string
		outside bool
		why     string
	}{
		{"stat testdata/missing.txt", "", false, "an absent name in testdata"},
		{"stat testdata/in/missing.txt", "", false, "an absent name in testdata through a link in testdata"},
		{"stat testdata/sub/../missing.txt", "", false, "a parent traversal over real directories"},
		{"stat testdata/out/missing.md", "", true, "an absent name in docs/research through a link"},
		{"stat testdata/out/../missing.md", "", true, "an absent name in docs, the link's parent"},
		{"stat testdata/dangle", "", true, "a dangling link into docs"},
		{"stat testdata/dangle/..", "", true, "a parent traversal after a dangling link"},
		{"open testdata/missing/../own.txt", "", true, "a parent traversal after an absent component (lexically testdata/own.txt)"},
		{"open testdata/own.txt/../own.txt", "", true, "a parent traversal after a file (lexically testdata/own.txt)"},
		{"stat testdata/loop1", "", true, "a loop of links"},
		{"open testdata/loop1/../own.txt", "", true, "a parent traversal after a loop"},
		{"stat ../../docs/research/back/missing.txt", "", true, "an absent name in testdata through a link in docs"},
		{"stat " + filepath.Join(l.base, "ext", "into-docs") + "/missing.md", "", true, "an absent name in docs through a link outside the root"},
		{"open " + gone, "", true, "a walk lost outside the root, with no scratch"},
		{"open " + gone, l.base, false, "a walk lost in scratch outside the root"},
		{"open " + filepath.Join(l.root, "internal", "a", "testdata", "missing") + "/../own.txt", l.base, true, "a walk lost in the root, even when the root is in scratch"},
		{"chdir " + filepath.Join(l.pwd, "testdata", "out") + "\nopen ../tooling.md", "", true, "a chdir through a link, then a parent traversal"},
		{"chdir " + filepath.Join(l.pwd, "testdata", "in") + "\nopen ../own.txt", "", false, "a chdir through a link in testdata"},
		{"open testdata/through-ext/into-own/own.txt", l.base, true, "out of the root through a link in testdata and back into testdata"},
		{"open testdata/through-ext/into-docs/tooling.md", l.base, true, "out of the root and back into docs"},
		{"stat testdata/through-ext/plain.txt", l.base, true, "out of the root to a file in scratch"},
		{"stat testdata/../../../../ext/plain.txt", l.base, true, "out of the root by parent traversals"},
		{"open " + filepath.Join(l.base, "ext", "into-own") + "/own.txt", l.base, false, "into the root from scratch, once"},
		{"open " + filepath.Join(l.pwd, "testdata", "outabs") + "/../../internal/a/testdata/own.txt", l.base, false, "an absolute link through the root's ancestors into docs, then up, staying in the root"},
	} {
		entries, err := readLog([]byte("# test log\n"+tc.log+"\n"), l.pwd, []string{l.root}, tc.scratch)
		if err != nil {
			t.Fatal(err)
		}
		outside := c.check(entries, map[string]bool{})
		if tc.outside != (len(outside) > 0) {
			t.Errorf("%s (%s): classified %v, outside %v; want outside=%v", tc.log, tc.why, entries, outside, tc.outside)
		}
	}
}
