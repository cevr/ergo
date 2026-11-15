package toolchain

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testinputs"
)

// TestMain observes what the tests read (testinputs), so Go's test cache
// and the closure sweep see every input.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// probeVariable makes TestTestsBuildWithSystemGo's child print its ERGO_GO.
const probeVariable = "ERGO_TOOLCHAIN_TEST_PROBE"

// TestTestsBuildWithSystemGo is the smallest repro of batch-18's M4: with
// ERGO_GO unset, a plain go test of a package that runs the ergo CLI
// installed the pinned Go from the network. testinputs.Main, every test
// package's TestMain, sets ERGO_GO=system when it is unset (mutant
// tc-tests-managed-go). It runs this test binary again, because the
// question is the environment a test process starts with, which this
// process has already passed: no in-process check sees it.
func TestTestsBuildWithSystemGo(t *testing.T) {
	if os.Getenv(probeVariable) == "1" {
		os.Stdout.WriteString("ERGO_GO=" + os.Getenv("ERGO_GO") + "\n")
		return
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ERGO_GO=") && !strings.HasPrefix(kv, "ERGO_TESTINPUTS_") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestsBuildWithSystemGo$", "-test.count=1")
	cmd.Env = append(env, probeVariable+"=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := proclimit.Run(cmd, 1<<30); err != nil {
		t.Fatalf("the probe: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ERGO_GO=system\n") {
		t.Errorf("a test process started with ERGO_GO unset sees %q; want ERGO_GO=system", out.String())
	}
}
