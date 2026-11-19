package sketchfront

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// harnessProgram is a test program of size Ergo packages: the root app,
// whose unexported score a test and a law run, over size-1 leaves l1 to l<size-1>.
// score and each leaf's Work are too large for Ergo's inliner, so no
// caller holds their bodies; score calls Tiny, which an inliner takes.
// root and leaf are the literals a body edit of Tiny and of l1's Work
// changes, each within its line; extra adds a
// declaration nothing uses to the root, which changes its export data and
// no test.
func harnessProgram(size int, nonce string, root, leaf int, extra bool) map[string]string {
	files := map[string]string{"ergo.mod": "module harnessed\n"}
	var imports, calls []string
	for i := 1; i < size; i++ {
		name := fmt.Sprintf("l%d", i)
		lit := 3 + i
		if i == 1 {
			lit = leaf
		}
		files[name+"/"+name+".ergo"] = bigBody(fmt.Sprintf("func Nonce() string { %q }\n\nfunc Work(n int) int {\n", nonce), lit)
		imports = append(imports, "import harnessed/"+name)
		calls = append(calls, name+".Work(n)")
	}
	head := strings.Join(imports, "\n") + fmt.Sprintf("\n\nfunc Tiny(n int) int { n * %d }\n\nfunc score(n int) int {\n", root)
	if len(calls) > 0 {
		head += "\tlet base = " + strings.Join(calls, " + ") + "\n"
	} else {
		head += "\tlet base = 0\n"
	}
	files["app/score.ergo"] = strings.Replace(bigBody(head, 5), "\tt\n}\n", "\tt + base + Tiny(n)\n}\n", 1)
	files["app/main.ergo"] = fmt.Sprintf("func main() needs Console {\n\tConsole.Println(\"\\(score(5)) %s\")\n}\n", nonce)
	files["app/tests.ergo"] = "test Scores { assert score(3) == score(3) }\n\nlaw Steady(n int) { score(n % 7) == score(n % 7) }\n"
	if extra {
		// At the end of a file, so no declaration moves; a new first
		// file would rename every Go file's header.
		files["app/score.ergo"] += "\nfunc Extra() int { 1 }\n"
	}
	return files
}

// bigBody is head followed by a loop body too large for an inliner, with
// lit on its first line, and the function's end.
func bigBody(head string, lit int) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\tlet mut t = 0\n\tfor j in 0..n {\n")
	fmt.Fprintf(&b, "\t\tt = t + (j * %d) %% 13\n", lit)
	for s := range 24 {
		fmt.Fprintf(&b, "\t\tif t %% %d == %d {\n\t\t\tt = t + j * %d - %d\n\t\t}\n", s+2, s%2, s+1, s)
	}
	b.WriteString("\t}\n\tt\n}\n")
	return b.String()
}

// TestBodyEditCompilesTheRootAlone is FT6's counter (test-speed plan §6
// row 6; the plan's name, TestBodyEditCompilesOnePackage, is CS4's test of
// the go command): the test build of a program of 1 and of 3 Ergo
// packages, on the direct path under the dev profile, keeps its harness in
// main and the root in its own package, so after a one-line body edit of
// the root's Tiny, or of the leaf l1's Work, the direct path compiles that
// one package and reuses the harness's archive (the root's export data,
// which has no bodies without inlining, holds); after a declaration is
// added to the root it compiles the root and the harness. Each build's
// test binary passes. It counts the direct path's compile actions
// (Report), never time.
//
// Mutants only it kills (testdata/mutants.json): ft-harness-key-has-bodies
// (the dev profile inlines, so a body edit compiles the harness again),
// ft-harness-key-omits-signature (a compile key without its imports'
// export data, so the harness is reused against a changed root) and
// ft-unexported-not-reachable (the root's names the harness uses keep an
// unexported spelling, so the harness does not compile). It builds Go
// because the question is what the direct path compiles and reuses, which
// only a real build does.
func TestBodyEditCompilesTheRootAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	for _, size := range []int{1, 3} {
		nonce := newNonce(t)
		dir, cache := t.TempDir(), t.TempDir()
		app := filepath.Join(dir, "app")
		step := func(what string, files map[string]string, compiled int) {
			t.Helper()
			writeFiles(t, dir, files)
			analysis, err := CheckDir(app)
			if err != nil || len(analysis.Diagnostics) > 0 {
				t.Fatalf("size %d, %s: %v %+v", size, what, err, analysis.Diagnostics)
			}
			tc := build.HostToolchain()
			tc.Profile = build.Dev
			var report build.Report
			var stderr bytes.Buffer
			output := filepath.Join(t.TempDir(), "test")
			_, err = Build(analysis, BuildRequest{Mode: build.ModeTest, Source: app, Output: output, Cache: build.Cache{Root: cache},
				Toolchain: tc, Stderr: &stderr, Report: &report})
			if err != nil || !report.Direct {
				t.Fatalf("size %d, %s: %v (%s)\n%s", size, what, err, report, stderr.String())
			}
			out, err := exec.Command(output).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "PASS (2 tests)") {
				t.Fatalf("size %d, %s: the test binary: %v\n%s", size, what, err, out)
			}
			if compiled >= 0 && (report.Compiled != compiled || !report.Linked) {
				t.Fatalf("size %d, after %s: %s, want %d compiles and a link", size, what, report, compiled)
			}
		}
		step("the first build", harnessProgram(size, nonce, 17, 17, false), -1)
		step("a body edit of the root's Tiny", harnessProgram(size, nonce, 19, 17, false), 1)
		if size > 1 {
			step("a body edit of l1's Work", harnessProgram(size, nonce, 19, 19, false), 1)
		}
		step("a declaration added to the root", harnessProgram(size, nonce, 19, 19, true), 2)
	}
}
