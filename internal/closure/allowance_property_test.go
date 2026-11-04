package closure

import (
	"fmt"
	"strings"
	"testing"
)

// TestGrantsCoverOnlyTheirAccesses is the allowance property: over every
// operation, every depth below a declared directory (and its parent and a
// sibling that shares its prefix), and names with and without the declared
// extension, each grant covers exactly what an independent oracle says:
//
//   - file: any access to exactly the path;
//   - listing: a lookup or a listing of exactly the directory;
//   - files D/*.ergo: an open, a lookup or a link one level below D whose
//     name ends in .ergo, and never a listing;
//   - subtree: the directory and everything below it.
//
// A declaration made of a listing and a flat pattern (the corpus shape)
// covers the union and marks the first entry that covers each access.
func TestGrantsCoverOnlyTheirAccesses(t *testing.T) {
	depthOf := func(p string) int { return strings.Count(p, "/") + 1 }
	under := func(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }
	oracle := func(a allowance, e entry) bool {
		dir := strings.TrimSuffix(a.path, "/*.ergo")
		switch a.grant {
		case file:
			return e.path == a.path
		case listing:
			return e.path == a.path && (e.op == opList || e.op == opStat)
		case files:
			return under(e.path, dir) && depthOf(e.path) == depthOf(dir)+1 && strings.HasSuffix(e.path, ".ergo") && e.op != opList
		case subtree:
			return e.path == a.path || under(e.path, a.path)
		}
		t.Fatalf("no oracle for %v", a.grant)
		return false
	}
	ops := []string{opOpen, opStat, opList, opLink}
	names := []string{"x.ergo", "x.txt", "x.ergo.bak", ".ergo", "s", "ergo"}
	cases := 0
	for _, dir := range []string{"d", "d/e", "d/e/f"} {
		var paths []string
		paths = append(paths, dir, dir+"x/x.ergo", dir+".ergo", parentOf(dir)+"x.ergo", parentOf(dir)+"s")
		for depth := 1; depth <= 4; depth++ {
			for _, name := range names {
				paths = append(paths, dir+strings.Repeat("/s", depth-1)+"/"+name)
			}
		}
		declarations := [][]allowance{
			{{dir + "/x.ergo", file, "one file"}},
			{{dir, listing, "a listing"}},
			{{dir + "/*.ergo", files, "a flat pattern"}},
			{{dir, subtree, "a subtree"}},
			{{dir, listing, "the corpus listing"}, {dir + "/*.ergo", files, "the corpus files"}},
		}
		for _, decl := range declarations {
			c := newClosure("pkg", nil, nil)
			c.declared = decl
			for _, p := range paths {
				for _, op := range ops {
					cases++
					e := entry{op, p}
					want, first := false, ""
					for _, a := range decl {
						if oracle(a, e) {
							want = true
							if first == "" {
								first = a.key()
							}
						}
					}
					used := map[string]bool{}
					got := len(c.check([]entry{e}, used)) == 0
					if got != want {
						t.Errorf("%v covers %s %s: got %v, want %v", keys(decl), op, p, got, want)
					}
					if want && (len(used) != 1 || !used[first]) {
						t.Errorf("%v, %s %s: marked %v used, want only %s", keys(decl), op, p, used, first)
					}
					if !want && len(used) != 0 {
						t.Errorf("%v, %s %s: refused but marked %v used", keys(decl), op, p, used)
					}
				}
			}
		}
	}
	if cases < 1000 {
		t.Errorf("only %d cases; the generator lost its coverage", cases)
	}

	// The checker's declaration of the migrated examples is the corpus
	// shape: its listing and its .ergo files, nothing deeper or else.
	c := newClosure("internal/sketch/check", nil, nil)
	for _, tc := range []struct {
		e      entry
		inside bool
	}{
		{entry{opList, "docs/research/ef-migrate"}, true},
		{entry{opStat, "docs/research/ef-migrate"}, true},
		{entry{opOpen, "docs/research/ef-migrate/main.ergo"}, true},
		{entry{opOpen, "docs/research/ef-migrate/notes.md"}, false},
		{entry{opOpen, "docs/research/ef-migrate/sub/main.ergo"}, false},
		{entry{opList, "docs/research/ef-migrate/sub"}, false},
		{entry{opOpen, "docs/research/ef-migrate/unused/deep/input.txt"}, false},
		{entry{opList, "docs/research"}, false},
		{entry{opOpen, "docs/research/other.ergo"}, false},
	} {
		if got := len(c.check([]entry{tc.e}, map[string]bool{})) == 0; got != tc.inside {
			t.Errorf("internal/sketch/check: %s %s inside=%v, want %v", tc.e.op, tc.e.path, got, tc.inside)
		}
	}
}

func parentOf(dir string) string {
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		return dir[:i+1]
	}
	return ""
}

func keys(decl []allowance) []string {
	var out []string
	for _, a := range decl {
		out = append(out, a.key())
	}
	return out
}

// TestGlobDeclaresWhatGlobReads pins glob: Go's filepath.Glob looks up and
// lists the directory above the first pattern element, looks up each match
// of every further element but the last and lists each directory, and the
// test reads the matches of the whole pattern.
func TestGlobDeclaresWhatGlobReads(t *testing.T) {
	for pattern, want := range map[string]string{
		"docs/research/ef-migrate/*.ergo": "listing docs/research/ef-migrate, files docs/research/ef-migrate/*.ergo",
		"examples/*/*/*.ergo":             "listing examples, listing examples/*, listing examples/*/*, files examples/*/*/*.ergo",
	} {
		if got := strings.Join(keys(glob(pattern, "r")), ", "); got != want {
			t.Errorf("glob(%q) = %s, want %s", pattern, got, want)
		}
	}
	if got := fmt.Sprint(keys(declare(glob("a/*.x", "r"), glob("a/*/*.x", "r")))); got != "[listing a files a/*.x listing a/* files a/*/*.x]" {
		t.Errorf("declare drops repeats: got %s", got)
	}
}
