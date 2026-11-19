package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workImport is an import line of an Ergo file: `import PATH` or
// `import NAME PATH`.
var workImport = regexp.MustCompile(`^import\s+(?:[A-Za-z_][A-Za-z0-9_]*\s+)?([^\s]+)\s*$`)

// workOwnModule is the module path of modules/work.
const workOwnModule = "github.com/cevr/ergo/modules/work/"

// TestWorkCoreReadsNoLanguage is the guard of SEMWS WG3: the core of the
// workspace tool, modules/work/work, reads a language only through the
// seam of work/language.ergo (Splitter, Indexer, Checker), so the same
// core serves an Ergo repository and any other. Each import of a tracked
// .ergo file there is a std package (a folder of internal/sketch/std)
// other than code, or a package of modules/work itself; an Ergo-language
// package (code.Reader, the checker) or any other module is reported, and
// the count must be 0.
func TestWorkCoreReadsNoLanguage(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "modules/work/work")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	std, err := os.ReadDir(filepath.Join(repo, "internal", "sketch", "std"))
	if err != nil {
		t.Fatal(err)
	}
	isStd := map[string]bool{}
	for _, e := range std {
		if e.IsDir() && e.Name() != "testdata" && e.Name() != "code" {
			isStd[e.Name()] = true
		}
	}
	read, found := 0, 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if !strings.HasSuffix(name, ".ergo") || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		read++
		for i, line := range strings.Split(string(src), "\n") {
			m := workImport.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			path := strings.Trim(m[1], `"`)
			if isStd[path] || strings.HasPrefix(path, workOwnModule) {
				continue
			}
			found++
			t.Errorf("%s:%d imports %s: the core reads a language only through work/language.ergo's Splitter, Indexer and Checker", name, i+1, path)
		}
	}
	if read < 10 {
		t.Fatalf("read %d .ergo files of modules/work/work; the listing is broken", read)
	}
	if found != 0 {
		t.Errorf("%d imports of a language package or another module in modules/work/work; want 0", found)
	}
}
