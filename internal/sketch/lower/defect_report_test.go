package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The defect report (#230; stage C round 2 review m1, m2, m3), through the
// public CLI: every program registers its source map, so a defect names
// its Ergo frames and never a Go frame; the frame that panicked is "F at
// file:line"; and each reported .ergo line is exact.

// defectReport runs src under ergo run and returns its standard error
// with the source path written as "F", after checking that the program
// exits 2 with no Go frame in its report.
func defectReport(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "run", path)
	cmd.Dir = filepath.Dir(path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("want exit 2 for a defect, got %v\n%s", err, stderr.String())
	}
	out := strings.ReplaceAll(stderr.String(), path, "F")
	for _, goFrame := range []string{"goroutine", ".go:", "main.", "runtime/"} {
		if strings.Contains(out, goFrame) {
			t.Fatalf("the report shows a Go frame (%q):\n%s", goFrame, out)
		}
	}
	return out
}

func wantReport(t *testing.T, src, want string) {
	t.Helper()
	if got := defectReport(t, src); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
}

// m1: a program with no call still registers its (empty) call table, so
// its report has Ergo frames only.
func TestDefectReportWithoutCalls(t *testing.T) {
	needsBuild(t)
	wantReport(t, `func main() {
	let xs = []int{1, 2, 3}
	let i = 5
	let v = xs[i]
	_ = v
}
`, "panic: runtime error: index out of range [5] with length 3\n\tmain at F:4\n")
}

// m1: a package-level let's initializer runs inside the root task, so a
// defect in it is reported like any other, with its Ergo frames.
func TestDefectReportInPackageLet(t *testing.T) {
	needsBuild(t)
	wantReport(t, `func pick(xs []int, i int) int {
	xs[i]
}

let (a, b) = (pick([]int{1}, 4), 2)

func main() needs Console {
	Console.Println("start")
	if a + b > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [4] with length 1\n\tpick at F:2\n\ta calls pick at F:5:15\n")
	wantReport(t, `let xs = []int{1}

let first = xs[3]

func main() needs Console {
	if first > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [3] with length 1\n\tfirst at F:3\n")
}

// m1: the package-level lets run in Go's initialization order, a let
// after every let it refers to, through functions too, else in
// declaration order.
func TestPackageLetsRunInDependencyOrder(t *testing.T) {
	needsBuild(t)
	wantRun(t, `let total = sum()

func sum() int { base + extra }

let extra = twice(base)

func twice(n int) int { n * 2 }

let base = 5

let (q, r) = (total / 3, total % 3)

func main() needs Console {
	if total == 15 && extra == 10 && q == 5 && r == 0 {
		Console.Println("ordered")
	}
}
`, "ordered\n")
}

// m2: the frame that panicked is the first source frame after the panic,
// written "F at file:line", even when its line also holds a call, and in a
// recursion that panics on the line of its own recursive call.
func TestDefectReportNamesThePanickingFrame(t *testing.T) {
	needsBuild(t)
	wantReport(t, `func idx() int { 9 }

func at(xs []int) int { xs[idx()] }

func main() needs Console {
	let xs = []int{1}
	let v = at(xs)
	if v > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [9] with length 1\n\tat at F:3\n\tmain calls at at F:7:10\n")
	wantReport(t, `func down(xs []int, n int) int {
	if n == 0 { xs[n + 5] } else { down(xs, n - 1) }
}

func main() needs Console {
	let v = down([]int{1}, 3)
	if v > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [5] with length 1\n\tdown at F:2\n\tdown calls down at F:2:33 (3 times)\n\tmain calls down at F:6:10\n")
}

// m3: a reported line is exact. A tuple literal lowers to an anonymous Go
// struct that go/format spreads over several lines; the defect inside it,
// on one .ergo line or on the second line of a statement, names its own
// line, and so does the call that passes a tuple.
func TestDefectReportLinesAreExact(t *testing.T) {
	needsBuild(t)
	wantReport(t, `func at(xs []int, i int) (int, int) {
	(1, xs[i])
}

func main() needs Console {
	let (a, b) = at([]int{1}, 9)
	if a + b > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [9] with length 1\n\tat at F:2\n\tmain calls at at F:6:15\n")
	wantReport(t, `func at(xs []int, i int) (int, int) {
	(1,
		xs[i])
}

func main() needs Console {
	let (a, b) = at([]int{1}, 9)
	if a + b > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [9] with length 1\n\tat at F:3\n\tmain calls at at F:7:15\n")
	wantReport(t, `func pick(p (int, int), xs []int) int {
	let (i, _) = p
	xs[i]
}

func main() needs Console {
	let v = pick((7, 8), []int{1})
	if v > 0 { Console.Println("never") }
}
`, "panic: runtime error: index out of range [7] with length 1\n\tpick at F:3\n\tmain calls pick at F:7:10\n")
	// The call starts its statement, so it shares the statement's //line
	// directive, and its Go line comes after the lowered && block: each Go
	// line names its own statement.
	wantReport(t, `func g2() bool { true }

func pick(xs []int, i int, b bool) int { xs[i] }

func main() {
	let xs = []int{1}
	pick(xs, 5, xs[0] == 1 && g2())
}
`, "panic: runtime error: index out of range [5] with length 1\n\tpick at F:3\n\tmain calls pick at F:7:2\n")
}
