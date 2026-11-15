package std_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/embedread"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/std/gostd"
)

// embedChildVariable makes TestEmbeddedReadsAreInTheTestLog the child
// that reads the sources.
const embedChildVariable = "ERGO_STD_EMBED_READS_CHILD"

// TestEmbeddedReadsAreInTheTestLog: in the mutant runner's coverage pass
// (embedread.Variable set) each embedded file std and gostd read is opened
// at its checkout path, so the go command's test log names it and the
// runner selects the test for that file's rows (scripts/mutants, reads.go,
// GATE2 GT8b); without the variable nothing is opened. The test runs its
// own binary again as the child, with -test.testlogfile, because a test
// process cannot read its own log before it exits; no Go is built.
// Mutants (testdata/mutants.json): gt8b-note-off, gt8b-prelude-unnoted,
// gt8b-package-unnoted, gt8b-binding-unnoted.
func TestEmbeddedReadsAreInTheTestLog(t *testing.T) {
	binding := gostd.Paths()[0]
	if os.Getenv(embedChildVariable) != "" {
		_ = std.Prelude()
		if _, ok := std.Package("strings"); !ok {
			t.Fatal("no std strings")
		}
		if _, ok := gostd.Binding(binding); !ok {
			t.Fatalf("no binding of %s", binding)
		}
		return
	}
	if testing.Short() {
		t.Skip("full tier: runs this test binary again")
	}
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	want := []string{
		filepath.Join(dir, "prelude.ergo"),
		filepath.Join(dir, "strings", "strings.ergo"),
		filepath.Join(dir, "gostd", "src", filepath.FromSlash(binding), filepath.Base(binding)+".ergo"),
	}
	for _, on := range []bool{true, false} {
		logFile := filepath.Join(t.TempDir(), "test.log")
		cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedReadsAreInTheTestLog$", "-test.count=1", "-test.testlogfile="+logFile)
		cmd.Env = append(os.Environ(), embedChildVariable+"=1")
		if on {
			cmd.Env = append(cmd.Env, embedread.Variable+"=1")
		}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := proclimit.Run(cmd, proclimit.DefaultLimit); err != nil {
			t.Fatalf("child: %v\n%s", err, out.String())
		}
		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		opened := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			if name, ok := strings.CutPrefix(line, "open "); ok {
				opened[name] = true
			}
		}
		for _, w := range want {
			if opened[w] != on {
				t.Errorf("with %s set %v: the test log names %s: %v, want %v", embedread.Variable, on, w, opened[w], on)
			}
		}
	}
}
