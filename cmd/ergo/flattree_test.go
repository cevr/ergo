package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestFlatTreeAgreesWithControl is the correctness property of
// examples/flattree (JSON read into a flat tree on the dense stores):
// the Ergo program over its flat tree, the same program over its plain
// twin (`-tree`, package tree) and the hand Go control in dense form
// (testdata/flattree/control) print the same bytes, and what the
// reference of TestJSONParseAgreesWithControl (encoding/json's tokens,
// printed by the test's own printers) computes. The texts are every file
// of JSONTestSuite (pinned in the example's testdata), seeded generated
// documents (every escape, lone surrogates, bytes that are not UTF-8,
// repeated keys, every number form, nesting at encoding/json's depth
// limit), each again with one to three bytes changed, and the
// `flattree gen` workload at two sizes; the commands are `print`,
// `stats` (counts by kind, depth and distinct keys, against the
// reference's own count) and `path` at the reference's pointers. A
// failure's line and column are the reference's; its reason, and every
// byte of standard error and the status, are the same on the three
// sides. A failing seed prints the document and its replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads its file through the host and ends with os.Exit,
// and the control is Go; no in-process check runs either. Each run is
// bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/flattree/
// testdata/mutants.json).
func TestFlatTreeAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusExample(t, "flattree")
	control := buildHandControl(t, "flattree", work)
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	a := flatAgreement{t: t, ergo: ergoBin, control: control, path: path}

	suite, err := filepath.Glob(filepath.Join("..", "..", "examples", "flattree", "testdata", "JSONTestSuite-*", "test_parsing", "*.json"))
	if err != nil || len(suite) == 0 {
		t.Fatalf("JSONTestSuite is not in examples/flattree/testdata: %v", err)
	}
	for _, file := range suite {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(file)
		ref := jsonReference(data)
		if (strings.HasPrefix(name, "y_") && ref.bad >= 0) || (strings.HasPrefix(name, "n_") && ref.bad < 0) {
			t.Fatalf("%s: encoding/json disagrees with the suite's verdict", name)
		}
		writeFile(t, path, string(data))
		a.agree(ref, name, "", "print")
		a.agree(ref, name, "", "stats")
	}

	seeds := 200
	first := uint64(1)
	if s := os.Getenv("FLATTREE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("FLATTREE_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x150))
		doc := jsonGenDoc(r, seed)
		what := "seed " + strconv.FormatUint(seed, 10)
		replay := fmt.Sprintf("replay: FLATTREE_SEED=%d go test -count=1 -run '^TestFlatTreeAgreesWithControl$' ./cmd/ergo", seed)
		ref := jsonReference([]byte(doc))
		writeFile(t, path, doc)
		if ref.bad >= 0 {
			if !strings.HasPrefix(doc, "[[[") {
				t.Fatalf("seed %d: the generator wrote a text encoding/json rejects at %d:\n%q", seed, ref.bad, clip(doc))
			}
			a.agree(ref, what+", nested too deep", replay, "stats")
			continue
		}
		a.agree(ref, what, replay, "print")
		a.agree(ref, what, replay, "stats")
		if jsonDepth(ref.v) <= jsonDeep {
			for _, ptr := range jsonPointers(r, ref.v) {
				a.agree(ref, what, replay, "path", ptr)
			}
		}
		broken := jsonBreak(r, doc)
		bref := jsonReference([]byte(broken))
		writeFile(t, path, broken)
		a.agree(bref, what+", with bytes changed", replay, "print")
		a.agree(bref, what+", with bytes changed", replay, "stats")
	}

	for _, n := range []string{"2", "300"} {
		gen := exec.Command(ergoBin, "gen", "-seed", strconv.FormatUint(first, 10), "-n", n)
		var out bytes.Buffer
		gen.Stdout = &out
		if err := proclimit.Run(gen, 1<<30); err != nil {
			t.Fatalf("flattree gen: %v", err)
		}
		ref := jsonReference(out.Bytes())
		if ref.bad >= 0 {
			t.Fatalf("the workload of %s records is not JSON at byte %d", n, ref.bad)
		}
		writeFile(t, path, out.String())
		a.agree(ref, "the workload of "+n+" records", "", "print")
		a.agree(ref, "the workload of "+n+" records", "", "stats")
		a.agree(ref, "the workload of "+n+" records", "", "path", "/0/at")
	}
}

// flatAgreement is the three programs and the file they read.
type flatAgreement struct {
	t             *testing.T
	ergo, control string
	path          string
}

// agree runs cmd on the file (with ptr after it for path) on the three
// sides, and fails unless each prints what ref says and all print the
// same.
func (a flatAgreement) agree(ref jsonRef, what, replay, cmd string, ptr ...string) {
	t := a.t
	t.Helper()
	file := filepath.Base(a.path)
	want := flatOutcome(ref, file, cmd, ptr)
	runs := [][]string{
		append([]string{a.ergo, cmd, file}, ptr...),
		append([]string{a.ergo, cmd, "-tree", file}, ptr...),
		append([]string{a.control, cmd, file}, ptr...),
	}
	var first string
	for i, run := range runs {
		got := flatRun(t, filepath.Dir(a.path), run)
		if !want.MatchString(got) || (i > 0 && got != first) {
			src, _ := os.ReadFile(a.path)
			t.Fatalf("%s: %s %s printed\n%s\nthe reference wants\n%s\nthe first run printed\n%s\nthe document:\n%q\n%s",
				what, filepath.Base(run[0]), strings.Join(run[1:], " "), clip(got), want, clip(first), clip(string(src)), replay)
		}
		first = got
	}
}

// flatOutcome is what flattree prints for cmd on file, as a pattern: the
// reference's outcome for the jsonparse command of the same output (print
// is min, path is get), or for stats the reference's own counts.
func flatOutcome(ref jsonRef, file, cmd string, ptr []string) *regexp.Regexp {
	var args []string
	switch cmd {
	case "print":
		args = []string{"min"}
	case "path":
		args = append([]string{"get"}, ptr...)
	default:
		args = []string{"stats"}
	}
	if cmd == "stats" && ref.bad < 0 {
		return regexp.MustCompile("^" + regexp.QuoteMeta("stdout:\n"+flatStats(ref.v)+"\nstderr:\n\nstatus 0\n") + "$")
	}
	pattern := ref.outcome(args, file).String()
	return regexp.MustCompile(strings.Replace(pattern, "jsonparse: ", "flattree: ", 1))
}

// flatStats is `flattree stats` of v, counted on the reference's tree.
func flatStats(v *jsonValue) string {
	counts := map[byte]int{}
	keys := map[string]bool{}
	var walk func(v *jsonValue)
	walk = func(v *jsonValue) {
		counts[v.kind]++
		for _, k := range v.keys {
			keys[k] = true
		}
		for _, x := range v.items {
			walk(x)
		}
	}
	walk(v)
	return fmt.Sprintf("values %d\ndepth %d\nnull %d\nbool %d\nnumber %d\nstring %d\narray %d\nobject %d\nkeys %d\n",
		jsonCount(v), jsonDepth(v), counts['n'], counts['b'], counts['#'], counts['s'], counts['['], counts['{'], len(keys))
}

// flatRun runs run (the binary and its arguments) in dir, and is its
// stdout, stderr and exit status as one text.
func flatRun(t *testing.T, dir string, run []string) string {
	t.Helper()
	command := exec.Command(run[0], run[1:]...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", run[0], err)
		}
		code = exit.ExitCode()
	}
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout.String(), stderr.String(), code)
}
