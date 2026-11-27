package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

const sliceLetMutSource = `import slices

func helperAppend(a []int) []int {
	let mut b = a
	b = b.Append(99)
	b
}

func helperMutate(a []int) []int {
	let mut b = a
	b[0] = 88
	b
}

func checkAll() bool {
	let a = []int{1, 2, 3}
	let mut b = a
	b = b.Append(4)
	b[0] = 9
	if !(a[0] == 1 && len(a) == 3 && b[0] == 9 && len(b) == 4 && b[3] == 4) {
		return false
	}

	let capA = slices.WithCapacity[int](4).Append(1)
	let mut capB = capA
	capB = capB.Append(2)
	let mut capC = capA
	capC = capC.Append(3)
	if !(len(capA) == 1 && capA[0] == 1 && len(capB) == 2 && capB[0] == 1 && capB[1] == 2 && len(capC) == 2 && capC[0] == 1 && capC[1] == 3) {
		return false
	}

	let mut mutA = slices.WithCapacity[int](4)
	mutA = mutA.Append(1)
	let mut mutB = mutA
	mutB = mutB.Append(2)
	mutA = mutA.Append(3)
	if !(len(mutA) == 2 && mutA[0] == 1 && mutA[1] == 3 && len(mutB) == 2 && mutB[0] == 1 && mutB[1] == 2) {
		return false
	}

	let full = []int{1, 2, 3, 4}
	let mut subB = full[0:2]
	subB = subB.Append(99)
	if !(full[2] == 3 && len(subB) == 3 && subB[0] == 1 && subB[1] == 2 && subB[2] == 99) {
		return false
	}

	let mut fullMut = []int{1, 2, 3, 4}
	fullMut[0] = 10
	let mut subMutB = fullMut[0:2]
	subMutB = subMutB.Append(77)
	if !(fullMut[0] == 10 && fullMut[2] == 3 && len(subMutB) == 3 && subMutB[2] == 77) {
		return false
	}

	let full2 = []int{1, 2, 3, 4}
	let res2 = helperAppend(full2[0:2])
	if !(full2[2] == 3 && res2[2] == 99) {
		return false
	}

	let res3 = helperMutate(a)
	if !(a[0] == 1 && res3[0] == 88) {
		return false
	}

	let loopA = slices.WithCapacity[int](4).Append(1)
	let mut out = [][]int{}
	for i in 0..3 {
		let mut loopB = loopA
		loopB = loopB.Append(i + 10)
		out = out.Append(loopB)
	}
	if !(len(loopA) == 1 && out[0][1] == 10 && out[1][1] == 11 && out[2][1] == 12) {
		return false
	}

	let appendAllA = slices.WithCapacity[int](6).Append(1)
	let mut appendAllB = appendAllA
	appendAllB = slices.AppendAll(appendAllB, []int{2, 3})
	let mut appendAllC = appendAllA
	appendAllC = slices.AppendAll(appendAllC, []int{4, 5})
	if !(len(appendAllA) == 1 && appendAllB[1] == 2 && appendAllB[2] == 3 && appendAllC[1] == 4 && appendAllC[2] == 5) {
		return false
	}

	true
}

law CheckAllHolds() {
	checkAll()
}

test CheckAllTest {
	assert checkAll()
}

func main() needs Console {
	if checkAll() {
		Console.Println("238-OK")
	} else {
		Console.Println("238-FAIL")
	}
}
`

// TestSliceLetMutAppendNoAlias verifies issue #238: copying a slice via
// `let mut b = a` and appending/mutating preserves value semantics without
// aliasing `a` across evaluator, IR lowering, and native Go execution.
// builds Go because Go backing-array aliasing on Append, spare capacity, and element mutation must be verified in compiled binaries.
func TestSliceLetMutAppendNoAlias(t *testing.T) {
	file, err := syntax.Parse(sliceLetMutSource)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	file.Name = "main.ergo"
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("check:\n%v", diags)
	}

	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	if len(rep.Results) < 2 {
		t.Fatalf("eval ran %d tests, want >= 2", len(rep.Results))
	}
	for _, r := range rep.Results {
		if r.Status != eval.Passed {
			t.Errorf("eval %s: status=%v msg=%q", r.Name, r.Status, r.Message)
		}
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(sliceLetMutSource), 0o644); err != nil {
		t.Fatal(err)
	}

	u := Unit{
		Package:    pkg,
		Files:      []*syntax.File{file},
		Sources:    []Source{{Path: srcPath, Text: sliceLetMutSource}},
		NoFallback: true,
	}
	if _, irRep, err := LowerIR(u); err != nil {
		t.Fatalf("LowerIR(NoFallback=true): %v", err)
	} else if len(irRep.Fallback) > 0 {
		t.Fatalf("LowerIR had fallbacks: %v", irRep.Fallback)
	}
	if _, testRep, err := LowerIRTests(u); err != nil {
		t.Fatalf("LowerIRTests(NoFallback=true): %v", err)
	} else if len(testRep.Fallback) > 0 {
		t.Fatalf("LowerIRTests had fallbacks: %v", testRep.Fallback)
	}

	needsBuild(t)
	cli := ergoCLI(t)
	runCmd := exec.Command(cli, "run", srcPath)
	runCmd.Dir = dir
	runCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ergo run failed (%v):\n%s", err, runOut)
	}
	if got := strings.TrimSpace(string(withoutBuildNote(runOut))); got != "238-OK" {
		t.Fatalf("ergo run output = %q, want %q", got, "238-OK")
	}

	testCmd := exec.Command(cli, "test", "--native", srcPath)
	testCmd.Dir = dir
	testCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	testOut, err := testCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ergo test --native failed (%v):\n%s", err, testOut)
	}
	if !strings.Contains(string(testOut), "CheckAllHolds") || !strings.Contains(string(testOut), "CheckAllTest") {
		t.Fatalf("native test output missing CheckAll tests:\n%s", testOut)
	}
}
