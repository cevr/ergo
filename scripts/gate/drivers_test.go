package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGateRefusesALaneWithoutItsDrivers: the gate's first check refuses a
// work workspace whose configuration lacks a merge driver its
// .gitattributes names, warns and runs on in any other checkout, and
// names the command that writes the drivers in both; Git's built-in
// drivers and commented lines ask for nothing, and once the driver is set
// the check is silent. Mutants: the refusal cut in a workspace
// (gate-drivers-refusal-cut) and raised outside one
// (gate-drivers-refusal-outside).
func TestGateRefusesALaneWithoutItsDrivers(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"--no-optional-locks", "-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	attributes := "# x merge=ergo-commented\n**/mutants.json merge=ergo-rows\n*.txt merge=union\n"
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(attributes), 0o644); err != nil {
		t.Fatal(err)
	}
	names := func(what, text string) {
		t.Helper()
		if !strings.Contains(text, "ergo-rows") || strings.Contains(text, "ergo-commented") || strings.Contains(text, "union") ||
			!strings.Contains(text, "lanework.sh drivers") {
			t.Errorf("the %s %q should name ergo-rows alone and the lanework.sh drivers command", what, text)
		}
	}

	warning, err := driverCheck(root)
	if err != nil {
		t.Errorf("a checkout that is no lane was refused: %v", err)
	}
	names("warning", warning)

	if err := os.WriteFile(filepath.Join(root, ".git", "work-id"), []byte("id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warning, err = driverCheck(root)
	if err == nil {
		t.Fatalf("a lane without merge.ergo-rows.driver passed the check (warning %q)", warning)
	}
	names("refusal", err.Error())

	git("config", "merge.ergo-rows.driver", "work merge-file -path %P rows %O %A %B >&2")
	if warning, err := driverCheck(root); err != nil || warning != "" {
		t.Errorf("a lane with its driver set got %q, %v; want nothing", warning, err)
	}
}
