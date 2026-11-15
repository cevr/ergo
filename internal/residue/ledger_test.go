package residue_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: each edit of every package ledger applies once, so the mutant
// runner can judge it, and each killer and anchor test names a test the
// runner can run (killerMissing). It catches embed-duplicate-ledger-anchor
// and ledger-killer-alias; the
// runner's own tests use generated ledgers and cannot catch a stale edit in
// the repository's ledgers. It lives in residue, which reads every file by
// design, so scripts/mutants' tests keep to their closure. It walks the
// tree rather than asking git, so it runs in the mutant runner's copy too.
func TestLedgerEditsApplyOnce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no module root at %s: %v", root, err)
	}
	ledgers := 0
	testFiles := map[string][]testFile{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}
		ledgers++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var ledger struct {
			Mutants []struct {
				Name    string   `json:"name"`
				File    string   `json:"file"`
				From    string   `json:"from"`
				Killers []string `json:"killers"`
				Anchor  *struct {
					Tests []string `json:"tests"`
				} `json:"anchor"`
			} `json:"mutants"`
		}
		if err := json.Unmarshal(data, &ledger); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		for _, m := range ledger.Mutants {
			ids := m.Killers
			if m.Anchor != nil {
				ids = append(slices.Clone(ids), m.Anchor.Tests...)
			}
			for _, id := range ids {
				if why := killerMissing(root, id, testFiles); why != "" {
					t.Errorf("%s: %s: killer %q: %s", path, m.Name, id, why)
				}
			}
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.File)))
			if err != nil {
				t.Errorf("%s: %s: %v", path, m.Name, err)
				continue
			}
			if n := strings.Count(string(src), m.From); n != 1 {
				t.Errorf("%s: %s: from occurs %d times in %s, want exactly once", path, m.Name, n, m.File)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ledgers == 0 {
		t.Fatal("no testdata/mutants.json below the module root")
	}
}

// ergoSuites are the suite names of scripts/mutants' ergoSuites and their
// packages.
var ergoSuites = map[string]string{
	"check": "internal/sketch/check",
	"lower": "internal/sketch/lower",
	"std":   "internal/sketch/std",
	"api":   "internal/sketch/api",
	"ergo":  "cmd/ergo",
}

// testFile is a _test.go file of a package: its build constraint's tag (a
// single custom tag, such as ergoexplore; empty when it has none) and its
// text.
type testFile struct {
	tag  string
	text string
}

// hostWords are build constraint words the default build satisfies or
// that name a host, not a tag a suite selects.
var hostWords = []string{"linux", "darwin", "unix", "windows", "amd64", "arm64", "cgo"}

// killerMissing says why a ledger's killer id names no test the mutant
// runner can run, or "" when it does: SUITE:TEST with SUITE an ergo suite,
// DIR or DIR[TAG] (a module directory, built with -tags=TAG) holding a
// top-level TEST in a _test.go file built under that tag; or
// examples:PROGRAM. A note after the test, past a space, is the reader's.
// A DIR inside a nested Go module (selfhost's hostsyntax) is built in that
// module (scripts/mutants nestedModule; batch-18 r4 M2). An id the runner cannot resolve makes it refuse the whole run
// (fu-mutants-xpkg: 45 rows named v1:, main:, sched:, tsproj: … and were
// never run).
func killerMissing(root, id string, cache map[string][]testFile) string {
	suite, rest, ok := strings.Cut(id, ":")
	if !ok {
		return "not SUITE:TEST"
	}
	test, _, _ := strings.Cut(rest, " ")
	if suite == "examples" {
		// A program is a directory of examples with its ergo.mod; a
		// package or a case inside one is not (r3b-local-exit named
		// examples:limiter/loopback, which the examples runner refuses).
		if _, err := os.Stat(filepath.Join(root, "examples", test, "ergo.mod")); err != nil || strings.Contains(test, "/") {
			return "no example program " + test + " (want examples:PROGRAM, a directory of examples with an ergo.mod)"
		}
		return ""
	}
	dir, tag := suite, ""
	if pkg, ok := ergoSuites[suite]; ok {
		dir = pkg
	} else if d, t, tagged := strings.Cut(suite, "["); tagged {
		dir, tag = d, strings.TrimSuffix(t, "]")
	}
	files, ok := cache[dir]
	if !ok {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if err != nil || dir == "" || strings.HasPrefix(dir, ".") {
			matches = nil
		}
		for _, f := range matches {
			text, err := os.ReadFile(f)
			if err != nil {
				return err.Error()
			}
			tf := testFile{text: string(text)}
			for _, line := range strings.SplitN(tf.text, "\n", 4) {
				if word, ok := strings.CutPrefix(line, "//go:build "); ok && !strings.ContainsAny(word, " !|&()") && !slices.Contains(hostWords, word) {
					tf.tag = word
				}
			}
			files = append(files, tf)
		}
		cache[dir] = files
	}
	if len(files) == 0 {
		return "no test package " + dir + " (want DIR:Test, DIR[TAG]:Test, an ergo suite, or examples:PROGRAM)"
	}
	for _, f := range files {
		if strings.Contains(f.text, "\nfunc "+test+"(") {
			if f.tag != tag {
				return "its file is built with tag " + strconv.Quote(f.tag) + ", the id names " + strconv.Quote(tag)
			}
			return ""
		}
	}
	return "no func " + test + " in " + dir
}
