package sketchfront

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/sketchtest"
)

// The determinism property (wf10 NONDET1): what the checker, the lowering,
// InspectGo and the evaluator give for a program is a function of the
// program's files. Run again, in the same process or in another, with any
// GOMAXPROCS, they give the same bytes. The domain is the generated
// programs of sketchtest.GenerateContextProgram (contexts that embed
// imported contexts beside local providers) and every package folder of
// examples/. internal/sketch/lower's TestLoweringIsAFunctionOfInput is the
// fast tier's part over the generated programs alone.

// outputOf is everything the checker and the lowering give for the
// program in dir, in the order they give it: the diagnostics, then, for a
// program that checks, the build lowering, the test lowering, InspectGo
// and the evaluation. Nothing is sorted: an order is part of the output.
func outputOf(dir string) string {
	var b strings.Builder
	a, err := CheckDir(dir)
	if err != nil {
		return "read: " + err.Error()
	}
	b.WriteString(diagnosticLines(a))
	if len(a.Diagnostics) > 0 || a.program == nil {
		return b.String()
	}
	files := func(kind string, fs []lower.File, err error) {
		fmt.Fprintf(&b, "== %s\n", kind)
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n", err)
		}
		for _, f := range fs {
			fmt.Fprintf(&b, "-- %s\n%s\n", f.Path, f.Data)
		}
	}
	fs, err := lower.Lower(a.program.unit())
	files("Lower", fs, err)
	fs, err = lower.LowerTests(a.program.unit())
	files("LowerTests", fs, err)
	inspect, err := json.Marshal(InspectGo(a.Checked()))
	fmt.Fprintf(&b, "== InspectGo %v\n%s\n", err, inspect)
	ev, err := a.Evaluation(eval.DefaultBudget(), "main.ergo")
	report, _ := json.Marshal(ev)
	fmt.Fprintf(&b, "== Evaluation %v\n%s\n", err, report)
	return b.String()
}

// determinismProgram is one program of the domain: a folder to check, and
// for a generated one the program, which a failure prints.
type determinismProgram struct {
	name      string
	dir       string
	generated *sketchtest.ContextProgram
}

// determinismDomain is the generated programs, each written under root,
// then the example package folders in the order a walk meets them.
// ERGO_DETERMINISM_SEED=<n> replays one generated program, with no example.
func determinismDomain(t *testing.T, root string) []determinismProgram {
	t.Helper()
	seeds := make([]int64, 16)
	for i := range seeds {
		seeds[i] = int64(9100 + i)
	}
	replay := os.Getenv("ERGO_DETERMINISM_SEED")
	if replay != "" {
		n, err := strconv.ParseInt(replay, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{n}
	}
	var programs []determinismProgram
	for _, seed := range seeds {
		p := sketchtest.GenerateContextProgram(seed)
		dir := filepath.Join(root, fmt.Sprintf("gen%d", seed))
		for name, text := range p.Files() {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		programs = append(programs, determinismProgram{name: fmt.Sprintf("generated seed %d", seed), dir: dir, generated: &p})
	}
	if replay != "" {
		return programs
	}
	folder, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	generated := len(programs)
	err = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case "cases", "testdata", "ergo.bind", "ts":
			return filepath.SkipDir
		}
		if matches, _ := filepath.Glob(filepath.Join(path, "*.ergo")); len(matches) > 0 {
			rel, _ := filepath.Rel(folder, path)
			programs = append(programs, determinismProgram{name: "examples/" + filepath.ToSlash(rel), dir: path})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(programs) == generated {
		t.Fatal("no example package folder under examples/")
	}
	return programs
}

// describe names the program; a generated one with its files and replay
// command.
func (p determinismProgram) describe() string {
	if p.generated == nil {
		return p.name
	}
	return fmt.Sprintf("%s; replay: ERGO_DETERMINISM_SEED=%d go test ./internal/sketchfront -run TestExamplesOutputIsAFunctionOfInput\n%s", p.name, p.generated.Seed, p.generated)
}

// dumpVariable names the file TestDeterminismDump writes the domain's
// outputs to; the test is a no-op without it.
const dumpVariable = "ERGO_DETERMINISM_DUMP"

// rootVariable names the folder the parent wrote the generated programs
// in, which a child checks at the same paths.
const rootVariable = "ERGO_DETERMINISM_ROOT"

// TestDeterminismDump writes each program's output, as JSON by name, for
// TestExamplesOutputIsAFunctionOfInput's child processes.
func TestDeterminismDump(t *testing.T) {
	path := os.Getenv(dumpVariable)
	if path == "" {
		t.Skip("a child of TestExamplesOutputIsAFunctionOfInput")
	}
	// The parent's root: a program's folder is part of its input (the
	// lowering's //line directives name it).
	root := os.Getenv(rootVariable)
	if root == "" {
		t.Fatalf("%s is unset", rootVariable)
	}
	out := map[string]string{}
	for _, p := range determinismDomain(t, root) {
		out[p.name] = outputOf(p.dir)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExamplesOutputIsAFunctionOfInput: for each program of the domain, a
// second pass in this process and two child processes, at GOMAXPROCS 1 and
// 4, give the bytes this process's first pass gives, and a generated
// program checks clean. It runs this test binary because an in-process
// rerun keeps what one process fixes for its life (the map hash seed,
// addresses, the std packages checked once); only a new process varies
// them. Each child is bounded by proclimit (ERGO_PROGRAM_MEMORY, 4 GiB by
// default). Full tier only: it checks every example four times.
// Mutant eval/nondet1-argument-keys: the evaluator's charged sort of a map
// argument's keys starts from the map's order, so its steps differ per run
// (examples/ssg).
func TestExamplesOutputIsAFunctionOfInput(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: checks every example four times, in three processes")
	}
	limit, err := proclimit.Limit(os.Environ(), 4<<30)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	programs := determinismDomain(t, root)
	first := make([]string, len(programs))
	for i, p := range programs {
		first[i] = outputOf(p.dir)
		if p.generated != nil && !strings.Contains(first[i], "== Lower\n") {
			t.Errorf("%s does not check:\n%s", p.describe(), firstLines(first[i], 20))
		}
	}
	compare := func(who string, outputs map[string]string) {
		for i, p := range programs {
			if got, ok := outputs[p.name]; !ok {
				t.Errorf("%s has no output for %s", who, p.name)
			} else if got != first[i] {
				t.Errorf("%s: %s differs from this process's first pass:\n%s", p.describe(), who, firstDiff(first[i], got))
			}
		}
	}
	for _, procs := range []int{1, 4} {
		path := filepath.Join(t.TempDir(), "dump.json")
		cmd := exec.Command(os.Args[0], "-test.run=^TestDeterminismDump$", "-test.count=1")
		cmd.Env = append(os.Environ(), dumpVariable+"="+path, rootVariable+"="+root, "GOMAXPROCS="+strconv.Itoa(procs))
		if out, err := proclimitOutput(cmd, limit); err != nil {
			t.Fatalf("GOMAXPROCS=%d child: %v\n%s", procs, err, firstLines(out, 40))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var theirs map[string]string
		if err := json.Unmarshal(data, &theirs); err != nil {
			t.Fatal(err)
		}
		compare(fmt.Sprintf("the GOMAXPROCS=%d child", procs), theirs)
	}
	second := map[string]string{}
	for _, p := range programs {
		second[p.name] = outputOf(p.dir)
	}
	compare("this process's second pass", second)
}

// proclimitOutput runs cmd under its memory bound and returns its combined
// output.
func proclimitOutput(cmd *exec.Cmd, limit uint64) (string, error) {
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := proclimit.Run(cmd, limit)
	return out.String(), err
}

// firstLines is the first n lines of s.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	return strings.Join(lines[:min(n, len(lines))], "\n")
}

// firstDiff shows the lines around the first line where a and b differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			lo, hi := max(0, i-3), i+4
			return fmt.Sprintf("line %d\nwant:\n%s\ngot:\n%s", i+1, strings.Join(al[lo:min(hi, len(al))], "\n"), strings.Join(bl[lo:min(hi, len(bl))], "\n"))
		}
	}
	return fmt.Sprintf("one is a prefix of the other: %d and %d lines", len(al), len(bl))
}
