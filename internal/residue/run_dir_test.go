package residue_test

import (
	"os/exec"
	"testing"
)

// TestNoRunDirFilesTracked catches the batch-15 r7 request accidentally
// committed under tmp/. Run receipts are not source, even when forced past
// the ignore rule. Historical mutant: d786905a's tracked round-7 request.
func TestNoRunDirFilesTracked(t *testing.T) {
	tracked, err := exec.Command("git", "--no-optional-locks", "ls-files", "--full-name", "--", ":(top)tmp/").Output()
	if err != nil {
		t.Fatalf("list tracked run artifacts: %v", err)
	}
	if len(tracked) != 0 {
		t.Fatalf("run artifacts must not be tracked:\n%s", tracked)
	}
}
