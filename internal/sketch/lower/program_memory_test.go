package lower

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestRunProgramBoundsAGrowingProgram: a generated program that keeps
// allocating stops at the test kit's memory limit, early, with an error
// that names the program and the limit; it never takes the host (the
// workbox OOM of 2026-11-08, docs/tooling.md "Test memory"). The program
// reserves 1 GiB in 16 MiB slices it never touches, so even the mutant
// below costs no resident memory. A built program is needed: the limit is
// a kernel property of the process the kit starts, which no in-process
// check sees. Mutant: runBounded runs the program without proclimit (or
// with limit 0); the program then prints "uncapped".
//
// It builds Go because its question is the kernel memory bound on a running
// program.
func TestRunProgramBoundsAGrowingProgram(t *testing.T) {
	needsBuild(t)
	if runtime.GOOS != "linux" {
		t.Skip("only Linux bounds a started program (internal/proclimit)")
	}
	t.Setenv(proclimit.Variable, "256MiB")
	const src = `import slices

func main() needs Console {
	let mut held = [][]int{}
	for i in 0..64 {
		let _ = i
		held = held.Append(slices.WithCapacity[int](2097152))
	}
	Console.Println("uncapped \(len(held))")
}
`
	o := mustLower(t, "growing", src)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return // buildAll reported the Go diagnostics
	}
	cmd := exec.Command(exes["growing"])
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err := runBounded(t, cmd)
	var limited *proclimit.LimitError
	if !errors.As(err, &limited) {
		t.Fatalf("a program reserving 1 GiB under a 256 MiB limit: err %v, stdout %q; want a LimitError", err, stdout.String())
	}
	for _, want := range []string{exes["growing"], "256 MiB", proclimit.Variable} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
