package sketchfront

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

func TestStdOverlayMutatesWorkflowFile(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(repo, "internal/sketch/std/workflow/file/file.ergo")
	orig, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	from := "\tsync.Modify(s.state, func(st workflow.State) (workflow.Loaded, workflow.State) { (workflow.LoadOf(st, run, from), st) })"
	to := "\tlet at = if s.replayed > 0 && int(from) > 0 { workflow.Cursor(0) } else { from }\n\tsync.Modify(s.state, func(st workflow.State) (workflow.Loaded, workflow.State) { (workflow.LoadOf(st, run, at), st) })"
	if !strings.Contains(string(orig), from) {
		t.Fatalf("from not found")
	}
	mut := strings.Replace(string(orig), from, to, 1)
	ov := OverlayFS(map[string]string{filepath.Clean(filePath): mut})
	durableDir := filepath.Join(repo, "examples/model-paths/durable")
	a, err := CheckDirFS(ov, durableDir, hostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %v", a.Diagnostics)
	}
	bin := filepath.Join(t.TempDir(), "durable.test")
	var stderr strings.Builder
	req := BuildRequest{
		Mode:      build.ModeTest,
		Source:    durableDir,
		Output:    bin,
		Cache:     build.Cache{Root: t.TempDir()},
		Toolchain: build.HostToolchain(),
		IR:        false,
		Stderr:    &stderr,
	}
	if _, err := Build(a, req); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, stderr.String())
	}
	out, runErr := exec.Command(bin, "--law", "DurableSession.PathsAgree", "--seed", "1").CombinedOutput()
	t.Logf("runErr=%v\noutput:\n%s", runErr, string(out))
	if runErr == nil {
		t.Fatalf("expected mutated durable binary to FAIL DurableSession.PathsAgree, but it PASSED!")
	}
}
