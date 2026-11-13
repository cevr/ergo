package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// selectionBase is the fixture the selection law records: functions whose
// blocks tests run, a constant no block holds, a helper reached through a
// call, a data file, a directory listing and a variable.
var selectionBase = map[string]string{
	"go.mod": "module example.test/sel\n\ngo 1.24\n",
	"p/p.go": `package p

// K is a constant no block holds.
const K = 1

// A returns one.
func A() int {
	x := 1
	return x
}

// B returns two.
func B() int {
	return 2
}

func helper() int {
	return 3
}

// C returns helper's value.
func C() int {
	return helper()
}
`,
	"p/p_test.go": `package p

import (
	"os"
	"testing"
)

func TestA(t *testing.T) {
	if A() != 1 {
		t.Fatal("A")
	}
}

func TestB(t *testing.T) {
	if B() != 2 {
		t.Fatal("B")
	}
}

func TestC(t *testing.T) {
	if C() != 3 {
		t.Fatal("C")
	}
}

func TestK(t *testing.T) {
	if K != 1 {
		t.Fatal("K")
	}
}

func TestData(t *testing.T) {
	if b, _ := os.ReadFile("testdata/x.txt"); string(b) != "x\n" {
		t.Fatal("data")
	}
}

func TestList(t *testing.T) {
	if entries, _ := os.ReadDir("testdata"); len(entries) != 1 {
		t.Fatal("list")
	}
}

func TestEnv(t *testing.T) {
	if os.Getenv("SEL_X") != "" {
		t.Fatal("env")
	}
}

// TestScratch does what the go command does in GOTMPDIR: it makes a work
// directory there and lists it.
func TestScratch(t *testing.T) {
	dir := os.Getenv("GOTMPDIR")
	if dir == "" {
		t.Fatal("no GOTMPDIR")
	}
	if _, err := os.MkdirTemp(dir, "go-build"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	}
}
`,
	"p/testdata/x.txt": "x\n",
	// An embedded file the package observes as internal/sketch/std does
	// (embedObserveVariable; gate-speed GS9), in a folder below the package
	// as std's are.
	"p/e.go": `package p

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed e/e.txt
var e string

// E returns the embedded text, opened at its checkout path under a record.
func E() string {
	if root := os.Getenv("ERGO_EMBED_OBSERVE"); root != "" {
		if f, err := os.Open(filepath.Join(root, "p", "e", "e.txt")); err == nil {
			f.Close()
		}
	}
	return e
}
`,
	"p/e/e.txt": "e\n",
	// A package below p that embeds a file and does not observe it, as
	// std's gostd does: its edit runs every test.
	"p/q/q.go": `package q

import _ "embed"

//go:embed d/d.txt
var D string
`,
	"p/q/d/d.txt": "d\n",
	"p/e_test.go": `package p

import (
	"testing"

	"example.test/sel/p/q"
)

func TestE(t *testing.T) {
	if E() != "e\n" {
		t.Fatal("E")
	}
}

func TestQ(t *testing.T) {
	if q.D != "d\n" {
		t.Fatal("Q")
	}
}
`,
}

var selectionNames = []string{"TestA", "TestB", "TestC", "TestK", "TestData", "TestList", "TestEnv", "TestScratch", "TestE", "TestQ"}

// selectionEdit is one edit of the fixture: a replacement in a file (a new
// file when from is empty) or a variable; reaches is the selection it
// alone gets, nil for every test.
type selectionEdit struct {
	name, file, from, to, env string
	reaches                   []string
}

var selectionEdits = []selectionEdit{
	{name: "A's body", file: "p/p.go", from: "\tx := 1\n", to: "\tx := 9\n", reaches: []string{"TestA"}},
	{name: "a comment in A's body", file: "p/p.go", from: "\treturn x\n", to: "\t// one\n\treturn x\n", reaches: []string{"TestA"}},
	{name: "helper's body", file: "p/p.go", from: "\treturn 3\n", to: "\treturn 8\n", reaches: []string{"TestC"}},
	{name: "B's body", file: "p/p.go", from: "\treturn 2\n", to: "\treturn 7\n", reaches: []string{"TestB"}},
	{name: "the constant", file: "p/p.go", from: "const K = 1", to: "const K = 5"},
	{name: "the data file", file: "p/testdata/x.txt", from: "x\n", to: "y\n", reaches: []string{"TestData"}},
	{name: "a file added to the listed directory", file: "p/testdata/y.txt", to: "y\n", reaches: []string{"TestList"}},
	{name: "a variable", env: "SEL_X=1", reaches: []string{"TestEnv"}},
	{name: "a test's body", file: "p/p_test.go", from: "B() != 2", to: "B() != 3"},
	{name: "an observed embedded file", file: "p/e/e.txt", from: "e\n", to: "f\n", reaches: []string{"TestE"}},
	{name: "a file another package embeds unobserved", file: "p/q/d/d.txt", from: "d\n", to: "x\n"},
}

// TestSelectionRunsEveryTestAnEditReaches is the law of the fast tier's
// test-impact selection (gate-speed GS4): after any edit of a recorded
// tree, every test whose verdict the edit changes is among those the
// selection runs. It records a fixture package, then for each edit alone,
// two function bodies of one file together, and seeded random sets of
// edits (ERGO_GATE_SELECTION_SEED replays one), it runs the package's
// tests on the edited tree and holds the selection to their verdicts. An
// edit alone also gets exactly the selection its row names: the tests whose
// blocks or reads it touches, or every test when it is outside a function
// body or in a test file. An edit of an embedded file the package observes
// (observedEmbeds, as internal/sketch/std does; gate-speed GS9) reaches its
// readers only, and a shared GOTMPDIR that the tests change reaches no test
// (GS10).
//
// It builds Go because the law is about what go test runs: the record is
// a cover build run test by test, and each verdict is a go test run of the
// edited tree. scripts/gate's tests run in the full tier only.
func TestSelectionRunsEveryTestAnEditReaches(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go")
	}
	// The fixture's p/ observes its embedded file as std does.
	observed := observedEmbeds
	observedEmbeds = append(slices.Clone(observedEmbeds), "p")
	defer func() { observedEmbeds = observed }()
	root := t.TempDir()
	for name, content := range selectionBase {
		write(t, root, name, content)
	}
	// A shared GOTMPDIR, as scripts/gate.sh sets one: TestScratch and every
	// go test of the verdicts change its listing (gate-speed GS10).
	env := append(stepEnvironment(), "GOWORK=off", "GOTMPDIR="+t.TempDir())
	listed, _, err := testPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(listed, func(p testPackage) bool { return p.dir == "p" })
	if at < 0 {
		t.Fatalf("no test package p in %v", listed)
	}
	p := listed[at]
	cache := t.TempDir()
	record, err := recordCoverage(root, cache, passThroughLimiter{}, p, env, env, selectionNames, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range record.Tests {
		if test.Always != "" {
			t.Fatalf("%s runs on every edit at the record: %s", test.Name, test.Always)
		}
	}
	if err := saveCoverageRecord(coveragePath(cache, p.dir), record); err != nil {
		t.Fatal(err)
	}
	check := func(applied []int, exact []string) {
		t.Helper()
		edited := slices.Clone(env)
		var added []string
		for _, i := range applied {
			e := selectionEdits[i]
			if e.env != "" {
				edited = append(edited, e.env)
				continue
			}
			name := filepath.Join(root, filepath.FromSlash(e.file))
			content, err := os.ReadFile(name)
			if e.from == "" {
				added = append(added, name)
			} else if err != nil || !bytes.Contains(content, []byte(e.from)) {
				t.Fatalf("edit %q does not apply: %v", e.name, err)
			}
			write(t, root, e.file, strings.Replace(string(content), e.from, e.to, 1))
		}
		chosen := selectTests(root, cache, p, edited, selectionNames)
		var run []string
		for _, name := range selectionNames {
			if !slices.Contains(chosen.skip, name) {
				run = append(run, name)
			}
		}
		verdicts := selectionVerdicts(t, root, edited)
		var names []string
		for _, i := range applied {
			names = append(names, selectionEdits[i].name)
		}
		for _, name := range selectionNames {
			if verdicts[name] != "pass" && !slices.Contains(run, name) {
				t.Errorf("after %q, %s %s, and the selection skips it (%s)", names, name, verdicts[name], chosen.note)
			}
		}
		if exact != nil && !slices.Equal(run, exact) {
			t.Errorf("after %q the selection runs %v (%s); want %v", names, run, chosen.note, exact)
		}
		for name, content := range selectionBase {
			write(t, root, name, content)
		}
		for _, name := range added {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, e := range selectionEdits {
		want := e.reaches
		if want == nil {
			want = selectionNames
		}
		check([]int{i}, want)
	}
	// Two bodies of one file are two hunks, each in its function.
	check([]int{0, 2}, []string{"TestA", "TestC"})
	seed := uint64(1)
	if s := os.Getenv("ERGO_GATE_SELECTION_SEED"); s != "" {
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	r := rand.New(rand.NewPCG(seed, 0x5e1ec7))
	for range 4 {
		var applied []int
		for i := range selectionEdits {
			if r.IntN(3) == 0 {
				applied = append(applied, i)
			}
		}
		check(applied, nil)
	}
	if t.Failed() {
		t.Logf("replay: ERGO_GATE_SELECTION_SEED=%d", seed)
	}
}

// selectionVerdicts runs the fixture's tests and returns each top-level
// test's verdict: pass, fail, or none when it did not run (a build
// failure).
func selectionVerdicts(t *testing.T, root string, env []string) map[string]string {
	t.Helper()
	command := exec.Command("go", "test", "-json", "-short", "-count=1", "./p")
	command.Env, command.Dir = env, root
	output, _ := command.Output()
	verdicts := map[string]string{}
	for _, name := range selectionNames {
		verdicts[name] = "none"
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var event struct{ Action, Test string }
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test == "" || strings.Contains(event.Test, "/") {
			continue
		}
		if event.Action == "pass" || event.Action == "fail" {
			verdicts[event.Test] = event.Action
		}
	}
	return verdicts
}

// The fast tier narrows a selection package's tests by the selector: a
// skipped test joins the names its shards skip, another package keeps its
// steps, and the step carries the selector's note.
func TestFastTierSkipsWhatTheSelectorSkips(t *testing.T) {
	root := checkoutRoot(t)
	skipped := "TestNoSuchTestTheSelectorSkips"
	steps, err := fastStepsSelected(root, func(p testPackage, env, names []string) selection {
		return selection{skip: []string{skipped}, note: "selected for " + p.dir}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range selectPackages {
		var shards []*Step
		for _, step := range steps {
			if strings.HasPrefix(step.Name, "go test ./"+dir+" ") {
				shards = append(shards, step)
			}
		}
		if len(shards) == 0 {
			t.Fatalf("no fast step tests %s", dir)
		}
		last := shards[len(shards)-1]
		if !strings.Contains(strings.Join(last.Argv, " "), skipped) || last.Note != "selected for "+dir {
			t.Errorf("%s: argv %q, note %q; want %s skipped and the selector's note", last.Name, last.Argv, last.Note, skipped)
		}
	}
	for _, step := range steps {
		if step.Note != "" && !slices.ContainsFunc(selectPackages, func(dir string) bool { return strings.HasPrefix(step.Name, "go test ./"+dir+" ") }) {
			t.Errorf("%s is not a selection package's step, yet it has the note %q", step.Name, step.Note)
		}
	}
}
