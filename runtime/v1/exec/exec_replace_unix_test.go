//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

const replaceProbeName = "ergo-exec-replace-probe"

func replaceProbeArgs() []string { return []string{"--ergo-replace-report", "an argument", ""} }

func replaceProbeEnv(dir string) map[string]string {
	return map[string]string{"A\nB": "first\nsecond", "PATH": dir, "VALUE": "two=parts"}
}

func init() {
	// Run before TestMain adds its own environment variables.
	if len(os.Args) == 4 && os.Args[1] == "--ergo-replace-report" {
		if err := json.NewEncoder(os.Stdout).Encode([][]string{os.Args, os.Environ()}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(7)
	}
	if len(os.Args) == 3 && os.Args[1] == "--ergo-replace-launch" {
		dir := os.Args[2]
		if err := ExecReplace(replaceProbeName, replaceProbeArgs(), replaceProbeEnv(dir), dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Replace returned success")
		os.Exit(2)
	}
}

// TestExecReplaceMatchesRun reuses its own test binary as the process to
// replace and as the program that reports the argv and environment it
// received. Only a subprocess can observe successful process replacement.
// Mutant replace-reads-ambient-env adds the launcher's environment back.
func TestExecReplaceMatchesRun(t *testing.T) {
	const ambient = "ERGO_REPLACE_AMBIENT"
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, replaceProbeName)); err != nil {
		t.Fatal(err)
	}
	run, err := ExecRun(rt.Root("replace control"), replaceProbeName, replaceProbeArgs(), replaceProbeEnv(dir), dir, "", 0)
	if err != nil || run.Status != 7 {
		t.Fatalf("Run: %+v, %v", run, err)
	}
	want, err := json.Marshal([][]string{
		append([]string{replaceProbeName}, replaceProbeArgs()...),
		{"A\nB=first\nsecond", "PATH=" + dir, "VALUE=two=parts"},
	})
	if err != nil || run.Stdout != string(want)+"\n" {
		t.Fatalf("Run reported %q; want exact sorted entries %s (%v)", run.Stdout, want, err)
	}
	launcher := exec.Command(exe, "--ergo-replace-launch", dir)
	launcher.Env = []string{ambient + "=parent", "PATH=/not-the-child-path"}
	got, err := launcher.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 7 || string(got) != run.Stdout || strings.Contains(string(got), ambient) {
		t.Fatalf("Replace: %s (%v); want exit 7 and the same argv and env as Run: %s", got, err, run.Stdout)
	}
}
