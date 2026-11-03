package main

import "testing"

// TestToolDigestKeysHostFacts checks that a step's tool digest, which keys
// Go's test cache for the tests the step runs, changes with the host facts
// of its temporary directory, such as tmpfs instead of a disk.
func TestToolDigestKeysHostFacts(t *testing.T) {
	facts := "tmp 0xef53"
	original := hostFacts
	t.Cleanup(func() { hostFacts = original })
	var asked string
	hostFacts = func(tmpDir string) string {
		asked = tmpDir
		return facts
	}
	g := &gate{root: t.TempDir()}
	step := &Step{Name: "go test probe"}
	disk := g.toolDigest(step)
	if want := stepTempDir(g.root, step.Name); asked != want {
		t.Errorf("host facts read for %q, not the step's temporary directory %q", asked, want)
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
