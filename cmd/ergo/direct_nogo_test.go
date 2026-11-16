package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDirectBuildStartsNoGoCommand: on the IR path, a program that the
// direct path builds without a refusal, built again after a body edit,
// starts no go command: the toolchain plan, the release's GOROOT and the
// std archives are all known, so the build runs only the patched compile
// of what changed and the link (compile-speed CS10). The test counts go
// processes by name twice: the go command on PATH is a script that logs
// each run before it runs the real one (the first build runs it, the
// second must not), and the stage report's go_commands counts every go
// command the build starts, the patched release's own included. The
// program has IR bodies in two Go packages (its root and geo), and its
// output shows the edit.
//
// It builds Go because the question is which processes a real build
// starts, and the patched compile reads the split's IR files; no
// in-process check sees either. Each program run has a 1 GiB memory bound
// the kernel enforces. Mutants: the release's GOROOT asked of the go
// command on every build (cs-ir-goroot-probe-each-build); go list run on
// a plan hit (cs-direct-go-list-on-hit).
func TestDirectBuildStartsNoGoCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go with the patched compile")
	}
	binary := buildTestCLI(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	bin, log := filepath.Join(tmp, "bin"), filepath.Join(tmp, "go.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", log, realGo)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "src")
	program := func(k int) {
		t.Helper()
		for rel, text := range map[string]string{
			"ergo.mod":          "module nogo\n",
			"geo/geo.ergo":      fmt.Sprintf("func scale(n int) int { n * %d + 1 }\n\nfunc Scale(n int) int { scale(n) }\n", k),
			"cmd/app/main.ergo": "import nogo/geo\n\nfunc twice(n int) int { n * 2 + 1 }\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(geo.Scale(5)) \\(twice(4))\")\n}\n",
		} {
			path := filepath.Join(src, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	xdg := filepath.Join(tmp, "xdg")
	out := filepath.Join(tmp, "app")
	goRuns := func() []string {
		data, _ := os.ReadFile(log)
		return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "_"))
	}
	report := filepath.Join(tmp, "stage.json")
	build := func(label string) string {
		t.Helper()
		os.Remove(report)
		command := exec.Command(binary, "build", "-work", "-o", out, "./cmd/app")
		command.Dir = src
		command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ERGO_STAGE_REPORT="+report)
		stdout, stderr, code := runSmokeCommand(t, command)
		if code != 0 {
			t.Fatalf("%s: ergo build: exit %d %s\n%s", label, code, stdout, stderr)
		}
		link := regexp.MustCompile(`(?m)^LINK=(.*)$`).FindStringSubmatch(string(stderr))
		if link == nil || !strings.HasPrefix(link[1], "direct") {
			t.Fatalf("%s: ergo build did not take the direct path: %s", label, stderr)
		}
		return workDir(t, string(stderr))
	}
	program(3)
	work := build("first build")
	if len(goRuns()) == 0 {
		t.Fatalf("the first build ran no go command: the script on PATH is not the one the build runs")
	}
	withIR := 0
	filepath.WalkDir(work, func(p string, e os.DirEntry, err error) error {
		if err == nil && e.Name() == "ergoir.json" {
			if data, err := os.ReadFile(p); err == nil && strings.Contains(string(data), `"name"`) {
				withIR++
			}
		}
		return nil
	})
	if withIR < 2 {
		t.Fatalf("%d packages of the module have IR bodies, want the root and geo", withIR)
	}
	if got := runBounded(t, out, irRun{}); got.stdout != "16 9\n" {
		t.Fatalf("first build prints %q, want \"16 9\\n\"", got.stdout)
	}
	before := len(goRuns())
	program(4)
	build("after a body edit")
	if runs := goRuns()[before:]; len(runs) > 0 {
		t.Errorf("after a body edit the build ran the go command on PATH %d times: %v", len(runs), runs)
	}
	var counts map[string]int64
	if data, err := os.ReadFile(report); err != nil || json.Unmarshal(data, &counts) != nil {
		t.Fatalf("no stage report: %v", err)
	}
	if counts["go_commands"] != 0 || counts["go_compiles"] == 0 {
		t.Errorf("after a body edit: %d go commands (the patched release's included) and %d compiles, want none and some", counts["go_commands"], counts["go_compiles"])
	}
	if got := runBounded(t, out, irRun{}); got.stdout != "21 9\n" {
		t.Errorf("after the edit the program prints %q, want \"21 9\\n\"", got.stdout)
	}
}
