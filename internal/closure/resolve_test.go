package closure

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/testinputs"
)

// The operations of an entry. A test log records open, stat and chdir; the
// check adds the links a path traverses, and marks a path it cannot resolve.
const (
	opOpen = "open" // a read of a file's content
	opStat = "stat" // a lookup: whether the path exists and its metadata
	// opList is an open of a directory: cmd/go hashes the names and the
	// metadata of its entries, so it is a listing, not a read of them.
	opList = "list"
	// opLink is a symbolic link a recorded path traverses. Its target is
	// read, so a link is an input as a file is.
	opLink = "link"
	// opLost prefixes the operation of a recorded path whose walk cannot
	// tell what it names: a ".." after an absent component or a file, a
	// loop of links, or a lookup the file system refuses. Its path is the
	// raw rest of the path after the place the walk stopped, so only a
	// declared entry that names it exactly covers it.
	opLost = "unresolved "
	// opChanged prefixes the operation of an access whose lookup met an
	// object that is not there after the run (testinputs identity mode).
	// Its path is the object the access reached at access time, which only a
	// declared entry that names it exactly covers.
	opChanged = "changed "
	// opRaced prefixes the operation of an access whose lookup met a
	// location that changed during the run, after the lookup (the observer's
	// watch). The kernel's lookup of the call may then have met another
	// object than the observer's, so no declared entry covers it. A change
	// in the checkout that no access looked up is such an entry too.
	opRaced = "changed during "
	// opUnidentified prefixes the operation of a recorded path that has no
	// access-time identity, whose walk after the run reaches the checkout.
	// Only a declared entry that names it exactly covers it.
	opUnidentified = "unidentified "
)

// failsClosed reports whether an entry's operation says the check cannot
// tell what the access reached, or that it changed, so only an exact
// declared file entry covers it.
func failsClosed(op string) bool {
	return strings.HasPrefix(op, opLost) || strings.HasPrefix(op, opChanged) || strings.HasPrefix(op, opRaced) || strings.HasPrefix(op, opUnidentified)
}

// maxLinks is the number of links the kernel follows in one lookup before
// it reports a loop (Linux's MAXSYMLINKS).
const maxLinks = 40

// resolver walks recorded paths component by component, as the kernel
// does, so a path is classified by the objects it reaches and never by its
// spelling: "testdata/link/../x" names x next to the link's target, not
// testdata/x. It reads the file system as it is when the log is read, after
// the run. That is the tree the test read only inside the checkout, which
// the sweep keeps read-only during the run (sandbox_test.go): a walk that
// enters the checkout and then leaves it, to a place outside it that the
// test could change, is lost. Lookups are memoized.
type resolver struct {
	lookups map[string]lookup
	// roots are the checkout's link-free locations; ancestors are the
	// directories above them, which a walk may pass on its way back in.
	roots, ancestors []string
	// look finds what is at a location: on the file system now
	// (testinputs.Look), or in a walk recorded at access time (replay).
	look func(at string) testinputs.Visit
}

// lookup is what an lstat of one absolute, link-free path found.
type lookup struct {
	mode   fs.FileMode
	target string // the link's target, for a link
	err    error
}

// lookupOf is the lookup a visit describes.
func lookupOf(v testinputs.Visit) lookup {
	switch v.Kind {
	case testinputs.KindAbsent:
		return lookup{err: fs.ErrNotExist}
	case testinputs.KindError:
		return lookup{err: errors.New(v.Err)}
	case testinputs.KindLink:
		return lookup{mode: fs.ModeSymlink, target: v.Target}
	case testinputs.KindDir:
		return lookup{mode: fs.ModeDir}
	case testinputs.KindFile:
		return lookup{}
	}
	return lookup{mode: fs.ModeIrregular}
}

// walk is what resolving one path found.
type walk struct {
	// links are the links the path traverses, in order, by their absolute
	// link-free location.
	links []string
	// reached is the absolute, clean, link-free location the path names. It
	// may not exist: a lookup of an absent name is an input of that name.
	reached string
	// exists reports whether reached exists, and dir whether it is a
	// directory: an open of a directory lists it.
	exists, dir bool
	// stop is where a lost walk stopped: the component it could not look
	// up, or the link that closed a loop.
	stop string
	// lost says why the walk cannot tell what the path names; reached is
	// then the place the walk stopped joined with the raw rest of the path.
	lost string
}

// newResolver returns a resolver for the checkout at roots (link-free); a
// resolver with no root never loses a walk for leaving one.
func newResolver(roots ...string) *resolver {
	r := &resolver{lookups: map[string]lookup{}, roots: roots, look: testinputs.Look}
	for _, root := range roots {
		for a := filepath.Dir(root); ; a = filepath.Dir(a) {
			if !slices.Contains(r.ancestors, a) {
				r.ancestors = append(r.ancestors, a)
			}
			if a == filepath.Dir(a) {
				break
			}
		}
	}
	return r
}

// inside reports whether the link-free location p lies in the checkout.
func (r *resolver) inside(p string) bool {
	for _, root := range r.roots {
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (r *resolver) lstat(name string) lookup {
	if l, ok := r.lookups[name]; ok {
		return l
	}
	l := lookupOf(r.look(name))
	r.lookups[name] = l
	return l
}

// walk resolves the absolute path name. Each component is looked up in the
// directory the walk has reached: "." stays, ".." moves to the parent of
// that directory (which has no link in it), and a link splices its target
// in front of the rest. A lookup that finds nothing, or a file where the
// path continues, ends the walk: the path names an absent object, unless a
// ".." follows, which the kernel never reaches, so the walk is lost. A walk
// that leaves the checkout after it entered it, for a place that is neither
// in it nor above it, is lost at the last location in the checkout it
// passed (left).
func (r *resolver) walk(name string) walk {
	var w walk
	if !filepath.IsAbs(name) {
		w.lost, w.stop, w.reached = "the path is not absolute", name, name
		return w
	}
	rest := strings.Split(filepath.ToSlash(name), "/")
	cur := string(filepath.Separator)
	followed := 0
	lastIn := "" // the last location in the checkout the walk passed
	// left reports whether the walk, at p, has left the checkout it
	// entered, and records p when it is in the checkout.
	left := func(p string) bool {
		if r.inside(p) {
			lastIn = p
			return false
		}
		return lastIn != "" && !slices.Contains(r.ancestors, p)
	}
	leave := func() walk {
		w.lost = "the path leaves the checkout after it entered it; the part outside is not read-only during the run, so what it read then is unknown now"
		w.stop, w.reached = lastIn, lastIn
		return w
	}
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			if left(cur) {
				return leave()
			}
			continue
		}
		next := filepath.Join(cur, c)
		if left(next) {
			return leave()
		}
		l := r.lstat(next)
		lose := func(why string) walk {
			w.lost, w.stop, w.reached = why, next, joinRaw(next, rest)
			return w
		}
		switch {
		case l.err != nil && !errors.Is(l.err, fs.ErrNotExist):
			return lose(fmt.Sprintf("the lookup failed: %v", l.err))
		case l.mode&fs.ModeSymlink != 0:
			followed++
			if followed > maxLinks {
				return lose("a loop of links")
			}
			w.links = append(w.links, next)
			if filepath.IsAbs(l.target) {
				cur = string(filepath.Separator)
			}
			rest = append(strings.Split(filepath.ToSlash(l.target), "/"), rest...)
		case l.err != nil || !l.mode.IsDir() && len(rest) > 0:
			// Absent, or a file where the path goes on.
			if slices.Contains(rest, "..") {
				return lose(`a ".." after an absent component or a file`)
			}
			w.reached = filepath.Join(append([]string{next}, rest...)...)
			return w
		default:
			cur = next
		}
	}
	w.reached, w.exists = cur, true
	w.dir = cur == string(filepath.Separator) || r.lstat(cur).mode.IsDir()
	return w
}

// joinRaw joins the rest of a path to a location without cleaning it, so a
// ".." stays.
func joinRaw(at string, rest []string) string {
	if len(rest) == 0 {
		return at
	}
	return at + "/" + strings.Join(rest, "/")
}

// parseLog reads a test log with the system's temporary directory as the
// scratch area and no access-time identity (readLog): each path is
// classified by what it names now.
func parseLog(data []byte, pwd string, roots []string) ([]entry, error) {
	return readLog(data, pwd, roots, os.TempDir(), nil)
}

// accessIdentity is what a run's observer saw at access time (testinputs
// identity mode): each walk an access of a path made, by the path's key, and
// the locations that changed after a walk looked them up, before the tree
// exited. writable are the run's writable places (its scratch directories,
// its own /dev/shm, a declared cgroup2 mount): the tree changes them itself,
// and the sweep's view of them after the run is not the run's, so their
// state after the run is not compared.
type accessIdentity struct {
	walks    map[string][][]testinputs.Visit
	changed  map[string]bool
	writable []string
}

func newAccessIdentity(id testinputs.Identity, writable []string) *accessIdentity {
	a := &accessIdentity{walks: id.Walks, changed: map[string]bool{}, writable: writable}
	for _, at := range id.Changed {
		a.changed[at] = true
	}
	return a
}

func (a *accessIdentity) isWritable(at string) bool {
	for _, w := range a.writable {
		if at == w || strings.HasPrefix(at, w+"/") {
			return true
		}
	}
	return false
}

// visited reports whether some walk looked the location up.
func (a *accessIdentity) visited(at string) bool {
	for _, walks := range a.walks {
		for _, visits := range walks {
			for _, v := range visits {
				if v.At == at {
					return true
				}
			}
		}
	}
	return false
}

// readLog reads a test log (cmd/go's -test.testlogfile format, with the
// lines testinputs.Main appends) and returns its inputs inside the module
// root, relative to it. pwd is the directory the test started in, which
// relative paths and chdir lines resolve against.
//
// Each path is resolved on the file system (resolver): the entry is the
// object it reaches, an open of a directory is a listing (opList), and
// each link the path traverses inside the root is an opLink entry. The
// sweep runs the tests with the checkout read-only, so the walk inside it
// sees the tree the test read; a path that reached the checkout from
// outside it was also recorded resolved at access time by testinputs
// (recorder.walk). A path the walk cannot resolve fails closed: it is an
// opLost entry, unless the walk stopped in scratch, outside the root,
// before it ever entered the root. Scratch is where tests make and remove
// their temporary trees (t.TempDir): a walk lost there passed a tree the
// test has since removed, which cmd/go does not hash either. A walk that
// leaves the root after it entered it stops at the last place in the root
// it passed, so it is never dropped.
//
// A log without its final newline was left uncacheable on purpose
// (testinputs.Main), so its inputs are unknown and readLog returns an
// error that says why.
//
// With an access-time identity (id; the sweep), each path is classified by
// the objects its accesses reached, replayed from the walks the observer
// recorded at access time, and that classification is compared with the
// path's walk after the run. Any difference fails closed, named by the
// access-time objects: a location an access looked up changed during the
// run (opRaced, which nothing covers, since the kernel's lookup may then
// have met another object than the observer's), or is not after the run
// what it was (outside the writable places), or the walk after the run
// reaches something in the checkout no access reached (opChanged, which only
// an exact declared entry covers). A path with no access-time walk whose
// walk after the run reaches the checkout fails closed too
// (opUnidentified), and so does a replay that needs a lookup the observer
// did not make. A change in the checkout that no access looked up is an
// opRaced entry of its own: the tree may have read the changed object by
// another name, which no log line shows.
func readLog(data []byte, pwd string, roots []string, scratch string, id *accessIdentity) ([]entry, error) {
	const header = "# test log\n"
	if !bytes.HasPrefix(data, []byte(header)) {
		return nil, errors.New("no test log header; the test binary did not write a log")
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		last := data[bytes.LastIndexByte(data, '\n')+1:]
		return nil, fmt.Errorf("the log is marked uncacheable, so its inputs are unknown: %q", last)
	}
	// The walk yields link-free locations, so compare them with the
	// link-free location of each root.
	linkFree := func(dirs ...string) []string {
		var out []string
		for _, d := range dirs {
			if d == "" {
				continue
			}
			if r, err := filepath.EvalSymlinks(d); err == nil {
				d = r
			}
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
		return out
	}
	real, scratches := linkFree(roots...), linkFree(scratch)
	var entries []entry
	seen := map[entry]bool{}
	add := func(e entry) {
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	res := newResolver(real...)
	if id != nil {
		// A change in the checkout that no access looked up: the tree may
		// have read the object by another name (a hard link made during the
		// run), which no line shows. It fails closed as an input of its own.
		var unseen []string
		for at := range id.changed {
			if rel, inside := relative(at, real); inside && !id.visited(at) {
				unseen = append(unseen, rel)
			}
		}
		slices.Sort(unseen)
		for _, rel := range unseen {
			add(entry{opRaced + opStat, rel})
		}
	}
	// entriesOf classifies one walk of a path the log records with op.
	entriesOf := func(w walk, op string) []entry {
		var es []entry
		for _, link := range w.links {
			if rel, inside := relative(link, real); inside {
				es = append(es, entry{opLink, rel})
			}
		}
		if w.lost != "" {
			// Fail closed: the path may have reached anything, here or
			// in the checkout, so it is an input no closure covers unless
			// a declared entry names it exactly.
			if rel, inside := relative(w.stop, real); inside {
				es = append(es, entry{opLost + op, rel + filepath.ToSlash(strings.TrimPrefix(w.reached, w.stop))})
			} else if _, inScratch := relative(w.stop, scratches); !inScratch {
				es = append(es, entry{opLost + op, w.reached})
			}
			return es
		}
		if w.dir && op == opOpen {
			op = opList
		}
		if rel, inside := relative(w.reached, real); inside {
			es = append(es, entry{op, rel})
		}
		return es
	}
	now := map[string]testinputs.Visit{} // each location's object after the run
	lookNow := func(at string) testinputs.Visit {
		v, ok := now[at]
		if !ok {
			v = testinputs.Look(at)
			now[at] = v
		}
		return v
	}
	mark := func(prefix string, es []entry) {
		for _, e := range es {
			if !strings.HasPrefix(e.op, opLost) {
				e.op = prefix + e.op
			}
			add(e)
		}
	}
	body := string(data[len(header):])
	if body == "" {
		return entries, nil // the tests read nothing
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		op, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("log line %q", line)
		}
		switch op {
		case "getenv":
			continue
		case "chdir", opOpen, opStat:
		default:
			return nil, fmt.Errorf("log line %q: unknown operation", line)
		}
		if !filepath.IsAbs(name) {
			name = joinRaw(pwd, []string{name})
		}
		if op == "chdir" {
			// The kernel resolves the directory at the chdir; the walk of
			// each later path resolves it again from the raw name.
			pwd = name
			continue
		}
		post := entriesOf(res.walk(name), op)
		if id == nil {
			mark("", post)
			continue
		}
		walks := id.walks[testinputs.IdentityKey(name)]
		if len(walks) == 0 {
			// No access of this path reached the checkout at access time.
			mark(opUnidentified, post)
			continue
		}
		var at []entry
		changed, raced, unidentified := false, false, false
		for _, visits := range walks {
			recorded := map[string]testinputs.Visit{}
			for _, v := range visits {
				if _, ok := recorded[v.At]; !ok {
					recorded[v.At] = v
				}
				switch {
				case id.changed[v.At]:
					raced = true
				case !id.isWritable(v.At) && lookNow(v.At) != v:
					changed = true
				}
			}
			replay := newResolver(real...)
			replay.look = func(at string) testinputs.Visit {
				if v, ok := recorded[at]; ok {
					return v
				}
				unidentified = true
				return testinputs.Visit{At: at, Kind: testinputs.KindError, Err: "the observer did not look this location up"}
			}
			at = append(at, entriesOf(replay.walk(name), op)...)
		}
		if unidentified {
			p := name
			if rel, inside := relative(name, real); inside {
				p = rel
			}
			mark(opUnidentified, []entry{{op, p}})
			continue
		}
		for _, e := range post {
			if !slices.Contains(at, e) {
				changed = true // the path reaches after the run what no access reached
			}
		}
		switch {
		case raced:
			mark(opRaced, at)
		case changed:
			mark(opChanged, at)
		default:
			mark("", at)
		}
	}
	return entries, nil
}

// relative returns name relative to the first root it lies in, with forward
// slashes. A name with a ".." component is never inside.
func relative(name string, roots []string) (string, bool) {
	for _, root := range roots {
		rest, ok := strings.CutPrefix(name, root)
		switch {
		case !ok:
			continue
		case rest == "":
			return ".", true
		case strings.HasPrefix(rest, string(filepath.Separator)):
			rel := filepath.ToSlash(rest[1:])
			if slices.Contains(strings.Split(rel, "/"), "..") {
				return "", false
			}
			return rel, true
		}
	}
	return "", false
}
