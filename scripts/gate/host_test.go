package main

import (
	"path/filepath"
	"testing"
)

// TestToolDigestKeysHostFacts checks that a step's tool digest, which keys
// Go's test cache for the tests the step runs, changes with the host facts
// of its temporary directory and of its GOTMPDIR (where t.TempDir makes the
// tests' directories), such as tmpfs instead of a disk.
func TestToolDigestKeysHostFacts(t *testing.T) {
	facts := "tmp 0xef53"
	original := hostFacts
	t.Cleanup(func() { hostFacts = original })
	var asked, askedGoTmp string
	hostFacts = func(tmpDir, goTmpDir string) string {
		asked, askedGoTmp = tmpDir, goTmpDir
		return facts
	}
	g := &gate{root: t.TempDir()}
	goTmpDir := filepath.Join(g.root, "gotmp")
	t.Setenv("GOTMPDIR", goTmpDir)
	step := &Step{Name: "go test probe"}
	disk := g.toolDigest(step)
	if want := stepTempDir(g.root, step.Name); asked != want {
		t.Errorf("host facts read for %q, not the step's temporary directory %q", asked, want)
	}
	if askedGoTmp != goTmpDir {
		t.Errorf("host facts read for GOTMPDIR %q, not the step's GOTMPDIR %q", askedGoTmp, goTmpDir)
	}
	facts = "tmp 0x1021994"
	if g.toolDigest(step) == disk {
		t.Error("moving the step's temporary directory from disk to tmpfs left the tool digest unchanged")
	}
	facts = "tmp 0xef53"
	if g.toolDigest(step) != disk {
		t.Error("the same host facts gave a different tool digest")
	}
}
