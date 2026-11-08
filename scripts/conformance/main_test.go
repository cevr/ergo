package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// cliEnv makes the test binary act as the conformance command, so process
// tests drive the real CLI without building a separate binary.
const cliEnv = "ERGO_CONFORMANCE_TEST_CLI"

func TestMain(m *testing.M) {
	if os.Getenv(cliEnv) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	// Keep every git process, fixture and verifier alike, independent of the
	// developer's global and system configuration.
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(testinputs.Main(m))
}

// repoRoot is the Ergo repository holding this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// runCLI runs the conformance command in dir with env as its whole
// environment and returns its exit code, stdout and stderr.
func runCLI(t *testing.T, dir string, env []string, args ...string) (int, string, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, args...)
	command.Dir = dir
	command.Env = append(env, cliEnv+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if _, ok := err.(*exec.ExitError); err != nil && !ok {
		t.Fatalf("run conformance CLI: %v", err)
	}
	return command.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

var fixtureGitConfig = []string{
	"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "init.templateDir=",
	"-c", "protocol.file.allow=always", "-c", "user.name=Snapshot Test", "-c", "user.email=test@example.invalid",
}

// hermeticGitEnv is the fixture environment: the process environment without
// global or system git configuration.
func hermeticGitEnv(extra ...string) []string {
	return append(os.Environ(), append([]string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}, extra...)...)
}

func fixtureGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"--no-optional-locks", "-C", repository}, append(slices.Clone(fixtureGitConfig), args...)...)...)
	command.Env = hermeticGitEnv()
	output, err := command.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(output))
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for relative, contents := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertContains(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("%q\ndoes not contain\n%q", text, fragment)
		}
	}
}

func readJSONObject(t *testing.T, path string) map[string]any {
	t.Helper()
	text, err := readText(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}
