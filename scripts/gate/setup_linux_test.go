//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// setupVariable names the fixture of TestScopeSetupPrecedesDiscovery's
// helper; the helper runs only when it is set.
const setupVariable = "GATE_SETUP_ROOT"

// The gate sets up its scope before it lists the Go packages: go list
// starts Go's telemetry sidecar, which can outlive it, and a process left in
// the scope's own cgroup makes the kernel refuse the scope's controllers
// (EBUSY), so the gate exited 2 before any step (gate-cpu round 5's
// verifier, discovery-sidecar.log). The helper gate runs setUp, as main
// does, in a real scope, over a module whose go list starts a real sidecar:
// a local-only telemetry directory (TEST_TELEMETRY_DIR, which the go
// command reads) whose counter file held.v1.count is a FIFO the sidecar
// blocks on, so it stays for the whole run, and no report is uploaded.
// The sidecar must be in the gate's leaf cgroup, and the gate past its
// setup with its step passed.
func TestScopeSetupPrecedesDiscovery(t *testing.T) {
	if os.Getenv(setupVariable) != "" {
		t.Skip("the helper runs as a subprocess of this test")
	}
	if realTools["go"] == "" {
		t.Skip("no go on PATH")
	}
	slice := testSlice(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "1G")
	t.Setenv(hostMemoryVariable, "4G")
	t.Setenv(cpuVariable, "1")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	root := fixture(t)
	write(t, root, "go.mod", "module example.test/discovery\n\ngo 1.24\n")
	write(t, root, "main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "scripts/authored_examples.txt", "main.go\n")
	telemetry := t.TempDir()
	write(t, telemetry, "mode", "local")
	if err := os.MkdirAll(filepath.Join(telemetry, "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(telemetry, "local", "held.v1.count"), 0o600); err != nil {
		t.Fatal(err)
	}
	unit := scopeUnit()
	command := exec.Command("systemd-run", scopeArguments(slice, unit, budget, 1, []string{os.Args[0], "-test.run=^TestScopeSetupHelper$", "-test.count=1"})...)
	command.Dir = root
	// The real go first, as the gate's caller has it.
	command.Env = append(scopeGateEnvironment(unit, 1), setupVariable+"="+root, "TEST_TELEMETRY_DIR="+telemetry, "GATE_SETUP_CACHE="+t.TempDir(),
		"PATH="+filepath.Dir(realTools["go"])+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		// The sidecar stays on its FIFO until its scope stops.
		stop := exec.Command("systemctl", "--user", "stop", unit+".scope")
		stop.Env = scopeEnvironment("")
		stop.Run()
	})
	output, err := command.CombinedOutput()
	text := string(output)
	t.Logf("helper: %v\n%s", err, text)
	wantAll(t, text, "sidecar in the gate's leaf: ", "== probe: ok", "setup-exit=0\n")
}

// TestScopeSetupHelper is the gate TestScopeSetupPrecedesDiscovery starts in
// a scope: it runs setUp, as main does, finds go list's sidecar, then runs
// one step.
func TestScopeSetupHelper(t *testing.T) {
	root := os.Getenv(setupVariable)
	if root == "" {
		t.Skip("run by TestScopeSetupPrecedesDiscovery")
	}
	scope, err := takeScope()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERGO_GATE_CACHE", os.Getenv("GATE_SETUP_CACHE"))
	g, err := setUp(scope, "", true)
	if err != nil {
		fmt.Printf("setup: %v\nsetup-exit=2\n", err)
		return
	}
	if sidecar := telemetrySidecar(scope); sidecar != "" {
		fmt.Printf("sidecar in the gate's leaf: %s\n", sidecar)
	} else {
		fmt.Printf("no telemetry sidecar in the gate's leaf: the case is vacuous\n")
	}
	g.steps = []*Step{{Name: "probe", Argv: []string{"true"}}}
	var out bytes.Buffer
	code := g.run(&out)
	fmt.Printf("%ssetup-exit=%d\n", out.String(), code)
}

// telemetrySidecar names the go telemetry sidecar in the leaf cgroup of
// the gate's scope, or "".
func telemetrySidecar(scope string) string {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	_, path, _ := strings.Cut(strings.TrimSpace(string(membership)), "0::")
	procs, err := os.ReadFile(filepath.Join(cgroupRoot, path, "cgroup.procs"))
	if err != nil || filepath.Base(filepath.Dir(path)) != scope+".scope" {
		return ""
	}
	for _, pid := range strings.Fields(string(procs)) {
		command, _ := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if strings.Contains(string(command), "** telemetry **") {
			return pid
		}
	}
	return ""
}
