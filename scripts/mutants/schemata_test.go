package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// schemataRows are the rows of the schemata law's module: three that fold
// and die (one whose first named test never reaches its site), one that
// folds and survives, and one that changes a type (isolated) and survives.
const schemataRows = `{"about":"schemata law","mutants":[
{"name":"add-minus","file":"one/one.go","from":"return a + b","to":"return a - b","killers":["one:TestAdd"]},
{"name":"neg-dropped","file":"one/one.go","from":"return -n","to":"return n","killers":["one:TestAdd","one:TestNeg"]},
{"name":"twice-thrice","file":"one/one.go","from":"b.N * 2","to":"b.N * 3","killers":["one:TestTwice"]},
{"name":"loose-survives","file":"one/one.go","from":"return 9","to":"return 8","killers":["one:TestLoose"]},
{"name":"box-grows","file":"one/one.go","from":"type Box struct{ N int }","to":"type Box struct{ N, M int }","killers":["one:TestTwice"]}
]}`

const schemataSource = `package one

type Box struct{ N int }

func Add(a, b int) int { return a + b }

func Neg(n int) int { return -n }

func (b Box) Twice() int { return b.N * 2 }

func Loose() int { return 9 }
`

const schemataTests = `package one

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add")
	}
}

func TestNeg(t *testing.T) {
	if Neg(4) != -4 {
		t.Fatal("Neg")
	}
}

func TestTwice(t *testing.T) {
	if (Box{N: 4}).Twice() != 8 {
		t.Fatal("Twice")
	}
}

func TestLoose(t *testing.T) {
	if Loose() < 0 {
		t.Fatal("Loose")
	}
}
`

// TestSchemataVerdictMatchesIsolated is the schemata law: on the same
// rows, the run that folds them into one tree gives every row the verdict
// and the killers the isolated run gives it, and builds the killers' suite
// once for every folded row it kills, where the isolated run builds it once
// per row. It builds Go because the law is about the builds of the runner
// and the processes they run. Mutants: schemata-mutant-env-dropped,
// schemata-survivor-not-confirmed (testdata/mutants.json).
func TestSchemataVerdictMatchesIsolated(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":                    "module fake.test/m\n\ngo 1.27\n",
		"one/one.go":                schemataSource,
		"one/one_test.go":           schemataTests,
		"one/testdata/mutants.json": schemataRows,
	} {
		writeFile(t, filepath.Join(root, name), text)
	}
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "fake")
	isolated, isoBuilds := schemataRun(t, root, false)
	folded, treeBuilds := schemataRun(t, root, true)
	want := map[string]string{"add-minus": Killed, "neg-dropped": Killed, "twice-thrice": Killed, "loose-survives": Survived, "box-grows": Survived}
	for name, status := range want {
		iso, fold := isolated[name], folded[name]
		if iso.Status != status {
			t.Fatalf("%s: the isolated run says %s, the module means %s", name, iso.Status, status)
		}
		if fold.Status != iso.Status || fold.Killer != iso.Killer || !slices.Equal(fold.Killers, iso.Killers) {
			t.Errorf("%s: schemata %s %s %v, isolated %s %s %v", name, fold.Status, fold.Killer, fold.Killers, iso.Status, iso.Killer, iso.Killers)
		}
	}
	if isoBuilds != [2]int{5, 0} {
		t.Errorf("the isolated run built %d mutated copies and %d trees, want one copy per row", isoBuilds[0], isoBuilds[1])
	}
	// The tree kills three rows with one build; the two survivors are
	// confirmed on their own copies, as any doubt is.
	if treeBuilds != [2]int{2, 1} {
		t.Errorf("the schemata run built %d mutated copies and %d trees, want 2 and 1", treeBuilds[0], treeBuilds[1])
	}
}

var buildsLine = regexp.MustCompile(`builds: (\d+) of mutated copies, (\d+) of the schemata tree`)

func schemataRun(t *testing.T, root string, fold bool) (map[string]Result, [2]int) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "results.json")
	var stdout, stderr strings.Builder
	code := run([]string{"-root", root, "-work", t.TempDir(), "-fresh", "-resume=false", "-workers", "1", "-tests", "1",
		"-schemata=" + strconv.FormatBool(fold), "-json", out, filepath.Join(root, "one")}, &stdout, &stderr)
	if code == 2 {
		t.Fatalf("schemata=%v: exit %d\n%s\n%s", fold, code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Result{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	for dec.More() {
		var r Result
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		by[r.Name] = r
	}
	m := buildsLine.FindStringSubmatch(stderr.String())
	if m == nil {
		t.Fatalf("schemata=%v: no builds line\n%s", fold, stderr.String())
	}
	rows, _ := strconv.Atoi(m[1])
	tree, _ := strconv.Atoi(m[2])
	return by, [2]int{rows, tree}
}
