package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// lawFiles are the files under root's law verdict store, relative to it,
// sorted.
func lawFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	dir := filepath.Join(root, lawsDir)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// putVerdicts stores n verdicts in a store of root, the first used
// longest ago, a minute apart, and returns their files, oldest first.
func putVerdicts(t *testing.T, root string, n int) []string {
	t.Helper()
	store := lawcache.Open(root, "image:"+t.Name())
	var files []string
	for i := range n {
		before := lawFiles(t, root)
		store.Put("verdict "+strconv.Itoa(i), "tested\t100\t\"100 cases\"")
		for _, f := range lawFiles(t, root) {
			if !slices.Contains(before, f) {
				files = append(files, f)
				ageBy(t, filepath.Join(root, lawsDir, f), time.Duration(n-i)*time.Minute)
			}
		}
	}
	if len(files) != n {
		t.Fatalf("%d verdicts made %d files under %s", n, len(files), lawsDir)
	}
	return files
}

// TestLawVerdictsCapped: past lawCap, a command that wrote verdicts runs
// the cap pass over the law verdict store, the age trim not due, and it
// keeps the lawCap most recently used, removing the whole excess in one
// pass. Mutant: the written verdicts not counted (lc-trim-laws-not-counted).
func TestLawVerdictsCapped(t *testing.T) {
	needLocks(t)
	defer func(old int) { lawCap = old }(lawCap)
	lawCap = 3
	root := t.TempDir()
	files := putVerdicts(t, root, 3*sweepRemovals)
	writeTrimStamp(t, root, 0) // the age trim is not due: only the cap removes
	Cache{Root: root}.LawsWritten(1)
	want := slices.Clone(files[len(files)-lawCap:])
	slices.Sort(want)
	if kept := lawFiles(t, root); !slices.Equal(kept, want) {
		t.Fatalf("after a command wrote verdicts with lawCap %d: %d kept (%q), want the %d newest %q", lawCap, len(kept), kept[:min(len(kept), 5)], lawCap, want)
	}
}

// TestDueTrimVisitsLaws: a due trim, which a build starts, removes the
// verdicts no command used for unusedLimit and a writer's temporary file
// older than stagingLifetime, and keeps a verdict used since. Mutant: the
// trim passes over Root/laws (lc-trim-skips-laws).
func TestDueTrimVisitsLaws(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	files := putVerdicts(t, root, 3)
	for _, f := range files[:2] {
		ageBy(t, filepath.Join(root, lawsDir, f), unusedLimit+time.Hour)
	}
	part := filepath.Join(filepath.Dir(files[0]), "crashed.part")
	mustWrite(t, filepath.Join(root, lawsDir, part), "tested")
	ageBy(t, filepath.Join(root, lawsDir, part), stagingLifetime+time.Minute)
	Cache{Root: root}.maybeTrim(time.Now()) // no stamp: the trim is due
	if kept, want := lawFiles(t, root), files[2:]; !slices.Equal(kept, want) {
		t.Fatalf("after a due trim: %q kept, want only the verdict used since, %q", kept, want)
	}
}

// TestReadVerdictIsKeptByTheCap: a command that reads a verdict marks it
// used, so the cap pass keeps it over one used more recently before the
// read. The second store reads the first's files through another path of
// the same root, as another process does (a store keeps what it wrote in
// memory). Mutant: a read leaves no mark (lc-get-no-mark).
func TestReadVerdictIsKeptByTheCap(t *testing.T) {
	needLocks(t)
	defer func(old int) { lawCap = old }(lawCap)
	lawCap = 1
	root := t.TempDir()
	files := putVerdicts(t, root, 2)
	for i, f := range files {
		ageBy(t, filepath.Join(root, lawsDir, f), time.Duration(3-i)*time.Hour)
	}
	other := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, other); err != nil {
		t.Fatal(err)
	}
	if _, ok := lawcache.Open(other, "image:"+t.Name()).Get("verdict 0"); !ok {
		t.Fatal("the second store does not find the first's verdict")
	}
	writeTrimStamp(t, root, 0)
	Cache{Root: root}.LawsWritten(1)
	if kept := lawFiles(t, root); !slices.Equal(kept, files[:1]) {
		t.Fatalf("kept %q, want the verdict read last, %q", kept, files[:1])
	}
}
