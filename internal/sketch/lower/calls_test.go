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
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestDefectReportNamesErgoCallSites(t *testing.T) {
	needsBuild(t)
	src := `func main() needs Console {
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
	// Two files are the smallest repro of bs6-file-index: a compact
	// site's file index must name its own source, not the first file.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "pick.ergo"), []byte("func pick(xs []int, i int) int {\n\txs[i]\n}\n\nfunc middle(n int) int needs Console {\n\tConsole.Println(\"middle\")\n\tpick([]int{1, 2}, n)\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "run", filepath.Dir(path))
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the program did not fail:\n%s", out)
	}
	want := "middle\npanic: runtime error: index out of range [5] with length 2\n" +
		"\tpick at pick.ergo:2\n" +
		"\tmiddle calls pick at pick.ergo:7:2\n" +
		"\tmain calls middle at main.ergo:2:10\n"
	if !strings.HasPrefix(string(out), want) {
		t.Fatalf("defect report:\n%s\nwant it to start with:\n%s", out, want)
	}
	if strings.Contains(string(out), "goroutine") || strings.Contains(string(out), ".go:") {
		t.Fatalf("the report shows Go frames:\n%s", out)
	}
}
