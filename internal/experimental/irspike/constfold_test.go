package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// constFoldUnit writes GenerateConstFold(seed) into dir and loads it.
func constFoldUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string, bool) {
	t.Helper()
	text, defect := GenerateConstFold(seed)
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
	}
	return u, text, defect
}

// lowerFold is u's IR lowering with the fold set to mode ("" or "off").
func lowerFold(t *testing.T, u lower.Unit, mode string) ([]lower.File, lower.IRReport) {
	t.Helper()
	prev := lower.SetConstFoldLoweringForTest(mode)
	defer lower.SetConstFoldLoweringForTest(prev)
	files, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	return files, rep
}

// foldCalls reports whether an IR body other than name's own names name: a
// call of name stays. name's own declaration is no call: its body takes IR
// whether or not its callers' calls of it fold.
func foldCalls(t *testing.T, files []lower.File, name string) bool {
	var ir struct {
		Funcs []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"funcs"`
	}
	if err := json.Unmarshal([]byte(foldIRText(files)), &ir); err != nil {
		t.Fatal(err)
	}
	for _, f := range ir.Funcs {
		if f.Name != name && strings.Contains(string(f.Body), `"name":"`+name+`"`) {
			return true
		}
	}
	return false
}

// foldIRText is the IR file's text among files.
// foldIRBody is the JSON of the IR body of the function name, if any.
func foldIRBody(t *testing.T, files []lower.File, name string) (string, bool) {
	var ir struct {
		Funcs []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"funcs"`
	}
	if err := json.Unmarshal([]byte(foldIRText(files)), &ir); err != nil {
		t.Fatal(err)
	}
	for _, f := range ir.Funcs {
		if f.Name == name {
			return string(f.Body), true
		}
	}
	return "", false
}

func foldIRText(files []lower.File) string {
	for _, f := range files {
		if filepath.Base(f.Path) == lower.IRFileName {
			return string(f.Data)
		}
	}
	return ""
}

// TestConstFoldCounts is the fold's guard and budget, in process (CF1).
// On a build whose Go and whose compiler's Go are the pinned release, a
// generated program folds its static pages (each staticN body takes IR,
// the call folded into it), and spin(2000000), over the fold's budget,
// stays a call in the IR. A build by another Go release or an unknown one,
// a compiler on another release, and the reference (the fold off) fold
// nothing. With a budget of a few steps, every candidate that needs more
// stays as written: fewer folds, more over the budget. lits and recs, whose
// list and record literals hold impure calls, take IR bodies (the spill).
// fromLets, which reads package lets, folds (LAZY-LET: the fold runs each
// let's initializer on its first read, under the let's bound, and pays
// none of it: heavy costs more than the fold's budget); with no steps for
// a let, it stays a call.
func TestConstFoldCounts(t *testing.T) {
	defer lower.SetConstFoldBuildGoForTest(toolchain.Version)()
	defer lower.SetConstFoldCompilerGoForTest(toolchain.Version)()
	u, text, _ := constFoldUnit(t, 1, t.TempDir())
	files, rep := lowerFold(t, u, "")
	if rep.Folds.Folded == 0 || rep.Folds.OverBudget == 0 {
		t.Errorf("folds %+v: want folded calls and spin over the budget\n%s", rep.Folds, text)
	}
	if rep.Folds.NoNative != 0 {
		t.Errorf("folds %+v: a fold called a Go function the evaluator has no native of (b25)", rep.Folds)
	}
	for _, fn := range []string{"static0", "lits", "recs"} {
		if !slices.Contains(rep.IR, fn) {
			t.Errorf("%s takes no IR body (fallback %v)", fn, rep.Fallback)
		}
	}
	// A folded html subtree is its markup: each staticN body calls no
	// function, and one holds a tag as a string (JSON writes < as \u003c).
	tags := false
	for i := 0; ; i++ {
		body, ok := foldIRBody(t, files, fmt.Sprintf("static%d", i))
		if !ok {
			break
		}
		if strings.Contains(body, `"k":"call"`) {
			t.Errorf("static%d is not one constant of markup: a folded element is not its markup\n%s", i, body)
		}
		tags = tags || strings.Contains(body, `\u003c`)
	}
	if !tags {
		t.Errorf("no static page holds a tag: a folded element is not its markup")
	}
	if foldCalls(t, files, "fromLets") {
		t.Errorf("fromLets is still called: a fold that reads package lets did not bind them\n%s", text)
	}
	for _, fn := range []string{"foldRowJSON", "foldRowTOML", "foldRowGeneric"} {
		if foldCalls(t, files, fn) {
			t.Errorf("%s is still called: the error-row agreement must exercise a fold\n%s", fn, text)
		}
	}
	prevLet := eval.SetFoldLetStepsForTest(0)
	unbound, _ := lowerFold(t, u, "")
	eval.SetFoldLetStepsForTest(prevLet)
	if !foldCalls(t, unbound, "fromLets") {
		t.Errorf("with no steps for a let, fromLets folded: a let's initializer ran past its bound")
	}
	// main takes IR once its html calls fold, and calls spin there.
	if !slices.Contains(rep.IR, "main_1") || !strings.Contains(foldIRText(files), `"name":"spin"`) {
		t.Errorf("the folded IR keeps no call of spin (IR bodies %v): the call over the budget was not kept", rep.IR)
	}
	for _, v := range []string{"go1.26.0", ""} {
		restore := lower.SetConstFoldBuildGoForTest(v)
		_, other := lowerFold(t, u, "")
		restore()
		if other.Folds != (lower.FoldCounts{}) {
			t.Errorf("built by %q: folds %+v, want none", v, other.Folds)
		}
	}
	restore := lower.SetConstFoldCompilerGoForTest("go1.26.0")
	_, other := lowerFold(t, u, "")
	restore()
	if other.Folds != (lower.FoldCounts{}) {
		t.Errorf("a compiler on go1.26.0: folds %+v, want none", other.Folds)
	}
	if _, off := lowerFold(t, u, "off"); off.Folds != (lower.FoldCounts{}) {
		t.Errorf("the reference folds: %+v", off.Folds)
	}
	prev := lower.SetConstFoldBudgetForTest(20)
	_, small := lowerFold(t, u, "")
	lower.SetConstFoldBudgetForTest(prev)
	if small.Folds.Folded >= rep.Folds.Folded || small.Folds.OverBudget <= rep.Folds.OverBudget {
		t.Errorf("a budget of 20 steps: folds %+v, at the default %+v", small.Folds, rep.Folds)
	}
}

// TestConstFoldAgrees is CF1's law: a program whose calls of constants
// fold at build time prints what the program built as written prints (the
// fold off, the reference), exits the same and fails with the same
// message, for generated std html pages and the program's own pure
// functions of records, lists, union variants, floats and strings; even
// seeds end in a defect a call of constants raises, which stays a call.
// Error-row values returned by generic code (the JSON and TOML decoders
// included) are data, unlike an actual typed failure. This distinguishes
// fold-return-rechecks-instantiated-type from a correct evaluator.
// Both print what the source path (the Go text) prints, so the impure
// calls inside list and record literals run in the source's order, and a
// node built from run-time data is not static (no call of an extern, such
// as html's inFold, is folded to the evaluator's answer). Every seed
// folds. A failing seed prints the Ergo source, both outcomes
// and its replay command. It builds Go for the reason needsBuild gives:
// the question is what the built program prints.
func TestConstFoldAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	release, err := os.ReadFile(filepath.Join(ir.GOROOT, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	v, _, _ := strings.Cut(string(release), "\n")
	defer lower.SetConstFoldBuildGoForTest(strings.Fields(v + " ")[0])()
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text, defect := constFoldUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestConstFoldAgrees ./internal/experimental/irspike"
		var outs [2]outcome
		for i, mode := range []string{"", "off"} {
			rep, err := EmitFold(u, filepath.Join(dir, "ir"+mode), mode)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "" && rep.Folds.Folded == 0 {
				t.Fatalf("seed %d: nothing folds (folds %+v)\n%s\n%s", seed, rep.Folds, replay, text)
			}
			bin := filepath.Join(dir, "ir"+mode+".bin")
			if _, err := Build(ir, IR, filepath.Join(dir, "ir"+mode), bin, BuildOptions{}); err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, text)
			}
			outs[i] = run(t, bin)
		}
		if _, err := Emit(u, Source, filepath.Join(dir, "source")); err != nil {
			t.Fatal(err)
		}
		sbin := filepath.Join(dir, "source.bin")
		if _, err := Build(src, Source, filepath.Join(dir, "source"), sbin, BuildOptions{}); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		source := run(t, sbin)
		if outs[0] != outs[1] || outs[1] != source || (outs[0].exit != 0) != defect {
			t.Fatalf("seed %d: the fold, the reference and the source path differ, or the program did not end as written\nfold:      %+v\nreference: %+v\nsource:    %+v\n%s\n%s", seed, outs[0], outs[1], source, replay, text)
		}
	}
}
