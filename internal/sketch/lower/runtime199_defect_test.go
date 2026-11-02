package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fork-tree defects (runtime-199 stage 4b; runtime.md, G2).

// TestRuntime199DefectReport: a defect in a grandchild task reaches main
// through two Waits, and ergo run reports it from the failing task, not
// the waiter: the Ergo frames of the task's own stack up to its block, then
// the task chain from the failing task to main, each fork named by the
// function that called task.Go and the call's position, and the context
// values in force in the failing task when it failed. The bindings the
// parent opened after the fork are not in that snapshot. No Go frame shows,
// and main exits with status 2.
func TestRuntime199DefectReport(t *testing.T) {
	src := `import task
import trace
import log

func leaf() int64 {
	let xs = []int64{1}
	xs[3]
}

func middle() int64 {
	with log.Annotate("peer", "10.0.0.4") {
		let t = task.Go() {
			with log.Annotate("region", "inner") { leaf() }
		}
		with log.Annotate("later", "parent") { t.Wait() }
	}
}

func main() needs Console {
	with trace.Span("ingest") {
		with log.Annotate("region", "eu") {
			let t = task.Go() { middle() }
			t.Wait()
		}
	}
	Console.Println("BAD: returned")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ergo := filepath.Join(dir, "ergo")
	build := exec.Command("go", "build", "-o", ergo, "./cmd/ergo")
	build.Dir = filepath.Clean("../../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/ergo: %v\n%s", err, out)
	}
	cmd := exec.Command(ergo, "run", path)
	cmd.Dir = dir // ergo run builds into ./dist
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 {
		t.Fatalf("ergo run: %v, want exit status 2\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	want := "panic: runtime error: index out of range [3] with length 1\n" +
		"\tleaf at " + path + ":7\n" +
		"\tmiddle calls leaf at " + path + ":13:43\n" +
		"\ttask #3 middle calls task.Go at " + path + ":12:11\n" +
		"\t  context trace.span=\"ingest\" log.peer=\"10.0.0.4\" log.region=\"inner\"\n" +
		"\ttask #2 main calls task.Go at " + path + ":22:12\n" +
		"\ttask #1 main\n"
	if got := stderr.String(); got != want {
		t.Fatalf("defect report:\n%s\nwant:\n%s", got, want)
	}
}
