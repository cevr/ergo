package closure

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// visitsOf walks path as testinputs' identity mode does at access time and
// returns what each lookup met.
func visitsOf(path string) []testinputs.Visit {
	var visits []testinputs.Visit
	pending := strings.Split(path, "/")
	current := "/"
	for hops := 0; len(pending) > 0; {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, name)
		v := testinputs.Look(next)
		visits = append(visits, v)
		switch v.Kind {
		case testinputs.KindLink:
			if hops++; hops > maxLinks {
				return visits
			}
			if filepath.IsAbs(v.Target) {
				current = "/"
			}
			pending = append(strings.Split(v.Target, "/"), pending...)
		case testinputs.KindDir:
			current = next
		default:
			return visits
		}
	}
	return visits
}

// identityTree is a checkout with docs/x.md, a package's testdata/in/a.txt
// and the link testdata/away to docs, and a scratch directory next to it.
func identityTree(t *testing.T) (root, scratch string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, scratch = filepath.Join(base, "m"), filepath.Join(base, "scratch")
	for _, name := range []string{"m/docs/x.md", "m/internal/p/testdata/in/a.txt", "scratch/keep"} {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "docs"), filepath.Join(root, "internal/p/testdata/away")); err != nil {
		t.Fatal(err)
	}
	return root, scratch
}

func retarget(t *testing.T, link, target string) {
	t.Helper()
	next := link + ".next"
	if err := os.Symlink(target, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, link); err != nil {
		t.Fatal(err)
	}
}

// TestIdentityClassifiesByTheAccessTimeObject pins how the sweep uses the
// access-time identity of each recorded path (readLog with an
// accessIdentity): replayed, compared with the path after the run, and
// failing closed where they differ or where there is no identity.
func TestIdentityClassifiesByTheAccessTimeObject(t *testing.T) {
	root, scratch := identityTree(t)
	pkg := filepath.Join(root, "internal/p")
	away := filepath.Join(pkg, "testdata/away")
	read := filepath.Join(away, "x.md")
	own := filepath.Join(pkg, "testdata/in/a.txt")
	walks := func(paths ...string) map[string][][]testinputs.Visit {
		w := map[string][][]testinputs.Visit{}
		for _, p := range paths {
			w[testinputs.IdentityKey(p)] = append(w[testinputs.IdentityKey(p)], visitsOf(p))
		}
		return w
	}
	classify := func(id *accessIdentity, lines ...string) []entry {
		t.Helper()
		es, err := readLog([]byte("# test log\n"+strings.Join(lines, "\n")+"\n"), pkg, []string{root}, scratch, id)
		if err != nil {
			t.Fatal(err)
		}
		return es
	}
	want := func(t *testing.T, got []entry, want ...entry) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("entries\n got %v\nwant %v", got, want)
		}
	}

	t.Run("unchanged", func(t *testing.T) {
		id := &accessIdentity{walks: walks(own, read)}
		want(t, classify(id, "open testdata/in/a.txt", "open "+read),
			entry{opOpen, "internal/p/testdata/in/a.txt"}, entry{opLink, "internal/p/testdata/away"}, entry{opOpen, "docs/x.md"})
	})
	t.Run("no identity", func(t *testing.T) {
		// A line no access-time walk stands for, which reaches the checkout
		// after the run: unidentified, and only an exact declared entry
		// covers it.
		id := &accessIdentity{walks: walks(own)}
		es := classify(id, "open "+read, "stat /nonexistent/elsewhere")
		want(t, es, entry{opUnidentified + opLink, "internal/p/testdata/away"}, entry{opUnidentified + opOpen, "docs/x.md"})
		if out := newClosure("internal/p", nil, nil).check(es, map[string]bool{}); len(out) != 2 {
			t.Errorf("unidentified entries pass the closure: %v", out)
		}
	})
	t.Run("changed after the run", func(t *testing.T) {
		id := &accessIdentity{walks: walks(read)}
		retarget(t, away, "in")
		defer retarget(t, away, filepath.Join(root, "docs"))
		// The access reached docs/x.md; the path names in/x.md now.
		want(t, classify(id, "open "+read), entry{opChanged + opLink, "internal/p/testdata/away"}, entry{opChanged + opOpen, "docs/x.md"})
	})
	t.Run("changed during the run", func(t *testing.T) {
		id := &accessIdentity{walks: walks(read), changed: map[string]bool{away: true}}
		es := classify(id, "open "+read)
		want(t, es, entry{opRaced + opLink, "internal/p/testdata/away"}, entry{opRaced + opOpen, "docs/x.md"})
		c := newClosure("internal/p", nil, nil)
		c.declared = []allowance{{"docs/x.md", file, "exact"}, {"internal/p/testdata/away", file, "exact"}}
		if out := c.check(es, map[string]bool{}); len(out) != 2 {
			t.Errorf("a declared entry covers an access that may have reached another object: %v", out)
		}
	})
	t.Run("a change in the checkout no access looked up", func(t *testing.T) {
		id := &accessIdentity{walks: walks(own), changed: map[string]bool{filepath.Join(root, "docs/x.md"): true}}
		want(t, classify(id, "open "+own), entry{opRaced + opStat, "docs/x.md"}, entry{opOpen, "internal/p/testdata/in/a.txt"})
	})
	t.Run("two walks of one path", func(t *testing.T) {
		w := walks(read)
		retarget(t, away, "in")
		defer retarget(t, away, filepath.Join(root, "docs"))
		if err := os.WriteFile(filepath.Join(pkg, "testdata/in/x.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(pkg, "testdata/in/x.md"))
		w[read] = append(w[read], visitsOf(read))
		// Each access-time object is named; the first one's link is not
		// there after the run.
		want(t, classify(&accessIdentity{walks: w}, "open "+read),
			entry{opChanged + opLink, "internal/p/testdata/away"}, entry{opChanged + opOpen, "docs/x.md"}, entry{opChanged + opOpen, "internal/p/testdata/in/x.md"})
	})
	t.Run("writable places are not compared", func(t *testing.T) {
		// A link in scratch into the checkout, removed after the read: the
		// access-time object stands.
		link := filepath.Join(scratch, "l")
		if err := os.Symlink(filepath.Join(root, "docs"), link); err != nil {
			t.Fatal(err)
		}
		id := &accessIdentity{walks: walks(link + "/x.md"), writable: []string{scratch}}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		want(t, classify(id, "open "+link+"/x.md"), entry{opOpen, "docs/x.md"})
		// The same removal outside the writable places fails closed.
		id.writable = nil
		want(t, classify(id, "open "+link+"/x.md"), entry{opChanged + opOpen, "docs/x.md"})
	})
	t.Run("a lookup the observer did not make", func(t *testing.T) {
		v := visitsOf(read)
		id := &accessIdentity{walks: map[string][][]testinputs.Visit{read: {v[:len(v)-1]}}}
		want(t, classify(id, "open "+read), entry{opUnidentified + opOpen, "internal/p/testdata/away/x.md"})
	})
	t.Run("the walk after the run reaches what no access reached", func(t *testing.T) {
		// A scratch link the test read through, retargeted later into the
		// checkout: the path names after the run what the read did not.
		link := filepath.Join(scratch, "l2")
		if err := os.Symlink(filepath.Join(root, "docs"), link); err != nil {
			t.Fatal(err)
		}
		id := &accessIdentity{walks: walks(link + "/x.md"), writable: []string{scratch}}
		retarget(t, link, filepath.Join(pkg, "testdata/in"))
		if err := os.WriteFile(filepath.Join(pkg, "testdata/in/x.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(pkg, "testdata/in/x.md"))
		want(t, classify(id, "open "+link+"/x.md"), entry{opChanged + opOpen, "docs/x.md"})
	})
}
