package residue_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/scripts/mutants/anchor"
	"ergo.local/prototype/scripts/mutants/suitename"
)

// ledgerFile is the schema of a package's testdata/mutants.json, as
// scripts/mutants (ledger.go, Ledger and Mutant) reads it. A field the
// runner does not know is an error there, so it is one here.
type ledgerFile struct {
	About string `json:"about"`
	// Env is the NAME=VALUE switches the runner gives the ledger's own
	// suite (scripts/mutants Ledger.Env).
	Env []string `json:"env"`
	// Commands are the killers that are not a go test, command:NAME
	// (scripts/mutants Ledger.Commands).
	Commands map[string][]string `json:"commands"`
	Mutants  []struct {
		Name    string   `json:"name"`
		Origin  string   `json:"origin"`
		File    string   `json:"file"`
		Decl    string   `json:"decl"` // scripts/mutants Mutant.Decl (MN0)
		From    string   `json:"from"`
		To      string   `json:"to"`
		Rule    string   `json:"rule"`
		Killers []string `json:"killers"`
		Gap     string   `json:"gap"`
		Refused string   `json:"refused"` // scripts/mutants Mutant.Refused
		// Regenerate is scripts/mutants Mutant.Regenerate (MUT-REGEN).
		Regenerate *struct {
			Dir  string   `json:"dir"`
			Argv []string `json:"argv"`
		} `json:"regenerate"`
		Anchor *struct {
			File  string   `json:"file"`
			Text  string   `json:"text"`
			Tests []string `json:"tests"`
		} `json:"anchor"`
	} `json:"mutants"`
}

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: every package ledger has the runner's schema (readLedger: no
// unknown field, a unique name, a file and a from, a to that differs from
// the from, each test SUITE:NAME), each edit applies once, so the mutant
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
	names := suitename.At(root)
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
		var ledger ledgerFile
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&ledger); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		seen := map[string]bool{}
		for i, m := range ledger.Mutants {
			var tests []string
			tests = append(tests, m.Killers...)
			if m.Anchor != nil {
				tests = append(tests, m.Anchor.Tests...)
			}
			switch {
			case m.Name == "":
				t.Errorf("%s: mutant %d has no name", path, i)
			case seen[m.Name]:
				t.Errorf("%s: two mutants are named %q", path, m.Name)
			case m.File == "" || m.From == "":
				t.Errorf("%s: mutant %q needs a file and a from", path, m.Name)
			case m.From == m.To:
				t.Errorf("%s: mutant %q changes nothing", path, m.Name)
			}
			for _, k := range tests {
				if !strings.Contains(k, ":") {
					t.Errorf("%s: mutant %q: test %q is not SUITE:NAME", path, m.Name, k)
				} else if name, ok := strings.CutPrefix(k, "command:"); ok {
					if name, _, _ = strings.Cut(name, " "); ledger.Commands[name] == nil {
						t.Errorf("%s: %s: killer %q: the ledger has no command %q", path, m.Name, k, name)
					}
				} else {
					id, err := names.Resolve(k)
					if err != nil {
						t.Errorf("%s: %s: %v", path, m.Name, err)
					} else if why := killerMissing(root, id, testFiles); why != "" {
						t.Errorf("%s: %s: killer %q: %s", path, m.Name, k, why)
					}
				}
			}
			seen[m.Name] = true
			if m.File == "" || m.From == "" {
				continue
			}
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.File)))
			if err != nil {
				t.Errorf("%s: %s: %v", path, m.Name, err)
				continue
			}
			if _, err := anchor.Locate(m.File, src, m.Decl, m.From); err != nil {
				t.Errorf("%s: %s: %v, want exactly once", path, m.Name, err)
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

// testFile is a _test.go file of a package: its build constraint's tag (a
// single custom tag, such as ergoexplore; empty when it has none) and its
// text.
type testFile struct {
	tag  string
	text string
	// constrained: the file has a build constraint of any form.
	constrained bool
}

// hostWords are build constraint words the default build satisfies or
// that name a host, not a tag a suite selects.
var hostWords = []string{"linux", "darwin", "unix", "windows", "amd64", "arm64", "cgo"}

// killerMissing says why a ledger's killer id names no test the mutant
// runner can run, or "" when it does: SUITE:TEST with SUITE an ergo suite,
// DIR or DIR[TAG] (a module directory, built with -tags=TAG) holding a
// top-level TEST in a _test.go file built under that tag (a file with no constraint is built under every tag: EXPLORE-rt runs all of runtime/v1 under ergoexplore); or
// examples:PROGRAM. TEST may be a family whose parts are TESTPart0 … (a
// test split to fit the gate's step limit). A note after the test, past a
// space, is the reader's.
// A DIR inside a nested Go module (selfhost's hostsyntax) is built in that
// module (scripts/mutants nestedModule; batch-18 r4 M2). An id the runner cannot resolve makes it refuse the whole run
// (fu-mutants-xpkg: 45 rows named v1:, main:, sched:, tsproj: … and were
// never run).
// ergoKillerMissing judges a killer of an Ergo package with no Go test
// (scripts/mutants runs it with scripts/ergotests, ST-0): Name is an Ergo
// test of the package, law:L (law:T.L) a law it declares (LAW-KILLER).
func ergoKillerMissing(root, dir, test string, files []string) string {
	if law, ok := strings.CutPrefix(test, "law:"); ok {
		// law:L, law:T.L, or law:pkg.T.L, a member law an imported
		// operation type declares that dir's providers run.
		if lawledger.LawDeclared(root, dir, law) {
			return ""
		}
		return "no Ergo law " + law + " declared for " + dir
	}
	decl := regexp.MustCompile(`(?m)^test ` + regexp.QuoteMeta(test) + `\s*\{`)
	for _, f := range files {
		if text, err := os.ReadFile(f); err == nil && decl.Match(text) {
			return ""
		}
	}
	return "no Ergo test or law " + test + " in " + dir
}

func killerMissing(root, id string, cache map[string][]testFile) string {
	if !strings.Contains(id, ":") {
		return "not SUITE:TEST"
	}
	suite, test := suitename.Split(id)
	if suite == "examples" {
		// A program is a directory of examples with its ergo.mod; a
		// package or a case inside one is not (r3b-local-exit named
		// examples:limiter/loopback, which the examples runner refuses).
		dir, name := filepath.Join(root, "examples", test), test
		if mod, ok := strings.CutPrefix(test, "modules/"); ok {
			dir, name = filepath.Join(root, "modules", mod), mod // a module of modules/ (MK0)
		}
		if _, err := os.Stat(filepath.Join(dir, "ergo.mod")); err != nil || strings.Contains(name, "/") {
			return "no example program " + test + " (want examples:PROGRAM or examples:modules/MODULE, a directory with an ergo.mod)"
		}
		return ""
	}
	dir, tag := suite, ""
	if d, t, tagged := strings.Cut(suite, "["); tagged {
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
				word, ok := strings.CutPrefix(line, "//go:build ")
				tf.constrained = tf.constrained || ok
				if ok && !strings.ContainsAny(word, " !|&()") && !slices.Contains(hostWords, word) {
					tf.tag = word
				}
			}
			files = append(files, tf)
		}
		cache[dir] = files
	}
	if len(files) == 0 {
		if ergo, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.ergo")); len(ergo) > 0 && tag == "" {
			return ergoKillerMissing(root, dir, test, ergo)
		}
		return "no test package " + dir + " (want DIR:Test, DIR[TAG]:Test, DIR:ErgoTest, DIR:law:L, an ergo suite, or examples:PROGRAM)"
	}
	// A family split into parts (TestXPart0 …) is run as all its parts
	// (scripts/mutants Suite.Families): a row names the family, so a split
	// leaves its rows as they are.
	parts := regexp.MustCompile(`\nfunc ` + regexp.QuoteMeta(test) + `(Part[0-9]+)?\(`)
	for _, f := range files {
		if parts.MatchString(f.text) {
			if f.tag != tag && f.constrained {
				return "its file is built with tag " + strconv.Quote(f.tag) + ", the id names " + strconv.Quote(tag)
			}
			return ""
		}
	}
	return "no func " + test + " in " + dir
}
