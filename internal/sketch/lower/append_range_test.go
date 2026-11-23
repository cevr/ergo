package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestAppendDuringSliceRangeAgrees reduces vt's DeleteChars: take a
// mutable prefix, append each element of the same source's suffix, then
// fill the vacated cells. Neither the suffix nor its source may change.
// The evaluator's old backing-identity guard skipped this program
// (mutant append-refuses-shared-range in the eval ledger).
//
// It builds Go because only executing both lowerings shows which values
// the loop visits and which values remain in the source after the append.
func TestAppendDuringSliceRangeAgrees(t *testing.T) {
	const src = `
type Cell { Code int }

func main() needs Console {
	let old = []Cell{Cell{Code: 1}, Cell{Code: 2}, Cell{Code: 3}, Cell{Code: 4}}
	let col = 1
	let k = 1
	let mut cells = old[0:col]
	for c in old[col+k : len(old)] {
		cells = cells.Append(c)
	}
	for _ in 0..k {
		cells = cells.Append(Cell{Code: 0})
	}
	Console.Println("\(len(cells)): \(cells[0].Code) \(cells[1].Code) \(cells[2].Code) \(cells[3].Code)")
	Console.Println("\(len(old)): \(old[0].Code) \(old[1].Code) \(old[2].Code) \(old[3].Code)")
}
`
	const want = "4: 1 3 4 0\n4: 1 2 3 4\n"
	prog := mustLower(t, "append_during_range", src)
	t.Run("evaluator", func(t *testing.T) {
		got, err := eval.RunMain(prog.pkg, eval.DefaultBudget(), eval.Process{})
		if err != nil || got.Skipped != "" || got.Incomplete != "" || got.Status != 0 || got.Stderr != "" || got.Stdout != want {
			t.Fatalf("evaluator: %+v, error %v; want stdout %q and a completed clean exit\n%s", got, err, want, src)
		}
	})
	t.Run("native", func(t *testing.T) {
		needsBuild(t)
		exes := buildAll(t, []*lowered{prog}, true)
		if exes == nil {
			return // the harness reported a build failure
		}
		if stdout, stderr, code := runExe(t, exes[prog.name]); code != 0 || stderr != "" || stdout != want {
			t.Errorf("text: exit %d, stderr %q, stdout %q; want %q\n%s", code, stderr, stdout, want, src)
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "run", path)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := runProgram(t, cmd); err != nil || len(withoutBuildNote(stderr.Bytes())) != 0 || stdout.String() != want {
			t.Errorf("IR: %v, stderr %q, stdout %q; want %q\n%s", err, stderr.String(), stdout.String(), want, src)
		}
	})
}
