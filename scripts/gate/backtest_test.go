package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/covfile"
)

// backtestSource is the fixture's package: two functions whose body blocks
// tests run, and a constant no block holds.
const backtestSource = `package p

const K = 1

func A() int {
	x := 1
	return x
}

func B() int {
	return 2
}
`

// The backtest (backtest.go) holds, for every Go row of every ledger at
// the store's commit, each killer of a suite the store holds in the round
// tier's selection of the row's edit, and reports any it misses. It runs
// on a fixture built here, a Git repository with a ledger and a store with
// one indexed segment, so it runs the same on every host and never passes
// empty. The store-backed run over the real ledgers is the full tier's
// step "coverage store backtest" on the workbox.
//
// Mutants (testdata/mutants.json): round-missing-store-passes,
// round-backtest-empty-passes, round-backtest-miss-unreported.
func TestSelectionHoldsEveryLedgerKiller(t *testing.T) {
	if _, err := backtestCommit(covfile.Store{Dir: t.TempDir()}, "."); err == nil || !strings.Contains(err.Error(), "no coverage store") {
		t.Fatalf("an empty coverage store: backtestCommit = %v, want the no coverage store error", err)
	}
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.test/bt\n\ngo 1.24\n",
		"p/p.go":      backtestSource,
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n\nfunc TestB(t *testing.T) { _ = B() }\n\nfunc TestC(t *testing.T) { _ = K }\n",
		"p/testdata/mutants.json": `{"mutants": [
  {"name": "a-body", "file": "p/p.go", "from": "\tx := 1\n", "to": "\tx := 2\n", "killers": ["p:TestA"]},
  {"name": "b-body", "file": "p/p.go", "from": "\treturn 2\n", "to": "\treturn 3\n", "killers": ["p:TestB"]},
  {"name": "k-declaration", "file": "p/p.go", "from": "const K = 1", "to": "const K = 2", "killers": ["p:TestC"]},
  {"name": "other-suite", "file": "p/p.go", "from": "\tx := 1\n", "to": "\tx := 3\n", "killers": ["q:TestZ"]},
  {"name": "stale", "file": "p/p.go", "from": "nowhere", "to": "", "killers": ["p:TestA"]}
]}
`,
	}
	for name, text := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"--no-optional-locks", "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		command.Dir = repo
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn("init", "-q")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "fixture")
	head := gitIn("rev-parse", "HEAD")

	store := covfile.Store{Dir: t.TempDir()}
	closures := map[string]*replay.Closure{"./p": {Dir: "p", Deps: []string{"p"}}}
	if _, err := backtestLedgers(store, repo, head, closures); err == nil || !strings.Contains(err.Error(), "no backtest") {
		t.Fatalf("a store with no segment of a ledger's suite: err = %v, want no backtest", err)
	}
	// index records a segment of suite p at head whose A body (lines 5-8)
	// is run by test a and B body (lines 10-12) by test b.
	index := func(key string, a, b int) {
		t.Helper()
		seg := &covfile.Coverage{
			Tests: []*covfile.Test{{ID: "p:TestA"}, {ID: "p:TestB"}, {ID: "p:TestC"}},
			Files: map[string][]covfile.Block{"p/p.go": {{Start: 5, End: 8, Tests: []int{a}}, {Start: 10, End: 12, Tests: []int{b}}}},
		}
		if err := store.Save(key, seg); err != nil {
			t.Fatal(err)
		}
		if err := store.Record(covfile.Entry{Suite: "p", Package: "./p", Cover: []string{"./p"}, Key: key, Mode: covfile.Mode, Commit: head}); err != nil {
			t.Fatal(err)
		}
	}
	index("aa01", 0, 1)
	if got, err := backtestCommit(store, repo); err != nil || got != head {
		t.Fatalf("backtestCommit = %q, %v; want the indexed HEAD %s", got, err, head)
	}
	report, err := backtestLedgers(store, repo, head, closures)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows != 3 || report.Killers != 3 || report.Stale != 1 || len(report.Missed) != 0 {
		t.Fatalf("%s, missed %v; want 3 rows, 3 killers, 1 stale, none missed", report, report.Missed)
	}
	// A record that has test A run B's body misses b-body's killer.
	index("aa02", 0, 0)
	report, err = backtestLedgers(store, repo, head, closures)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Missed, []string{"b-body p:TestB"}) {
		t.Fatalf("%s, missed %v; want b-body p:TestB missed", report, report.Missed)
	}
}
