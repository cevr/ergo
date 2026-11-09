package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefectReportNamesErgoCallSites: a defect that escapes main is
// reported with its Ergo call chain from the call table, innermost first:
// the frame that panicked, then each call, by Ergo function, callee and
// .ergo position; no Go frame (#230; runtime.md, Exit causes). The pure
// call into pick is named as well as the capability call into middle.
func TestDefectReportNamesErgoCallSites(t *testing.T) {
	needsBuild(t)
	src := `func pick(xs []int, i int) int {
	xs[i]
}

func middle(n int) int needs Console {
	Console.Println("middle")
	pick([]int{1, 2}, n)
}

func main() needs Console {
	let v = middle(5)
	if v == 0 {
		Console.Println("zero")
	}
}
`
	path := filepath.Join(t.TempDir(), "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "run", path)
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the program did not fail:\n%s", out)
	}
	want := "middle\npanic: runtime error: index out of range [5] with length 2\n" +
		"\tpick at " + path + ":2\n" +
		"\tmiddle calls pick at " + path + ":7:2\n" +
		"\tmain calls middle at " + path + ":11:10\n"
	if !strings.HasPrefix(string(out), want) {
		t.Fatalf("defect report:\n%s\nwant it to start with:\n%s", out, want)
	}
	if strings.Contains(string(out), "goroutine") || strings.Contains(string(out), ".go:") {
		t.Fatalf("the report shows Go frames:\n%s", out)
	}
}
