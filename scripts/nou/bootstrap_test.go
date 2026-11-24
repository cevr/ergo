package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapUsesAcceptedTree(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
	}
	const fixture = "fixture_test.go"
	original, err := os.ReadFile("inventory_test.go")
	if err != nil {
		t.Fatal(err)
	}
	const originalLedger = "A\tcompiler\tcompiler/existing.ergo\tfunc keep/id\t1\n"
	write := func(file, text string) {
		t.Helper()
		name := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		t.Helper()
		git("-c", "user.name=Inventory Test", "-c", "user.email=inventory@example.test",
			"-c", "commit.gpgsign=false", "commit", "-qm", "accepted")
	}
	git("init", "-q")
	write(fixture, string(original))
	write(ledger, originalLedger)
	write(".gitattributes", fixture+" export-ignore\n")
	git("add", fixture, ledger, ".gitattributes")
	commit()

	// The candidate exchanges a call for a different ID at the same count.
	// Only the already accepted version is eligible for the bootstrap.
	const old, replacement = "func added()", "func later()"
	candidate := strings.Replace(string(original), old, replacement, 1)
	if candidate == string(original) {
		t.Fatal("replacement fixture no longer matches the original control")
	}
	write(fixture, candidate)
	var report bytes.Buffer
	if err := bootstrapGo(root, "HEAD", &report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(originalLedger)) {
		t.Fatal("Go bootstrap changed the standalone ledger")
	}
	rows, err := readLedger(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	limit := map[siteID]int{}
	for id, row := range rows {
		limit[id] = row.count
	}
	got, err := embeddedSites(fixture, candidate)
	if err != nil {
		t.Fatal(err)
	}
	excess := newSites(got, limit)
	if len(excess) != 1 || !strings.Contains(excess[0].id.anchor, "func later/") {
		t.Fatalf("wanted only the candidate call rejected, got %v (%s)", excess, &report)
	}

	// After the migration has removed every legacy Go row, the seal still
	// prevents bootstrapping a later edit.
	write(ledger, goLedgerSeal+"\n"+originalLedger)
	git("add", ledger)
	commit()
	if err := bootstrapGo(root, "HEAD", &report); err == nil ||
		!strings.Contains(err.Error(), "Go scopes already active") {
		t.Fatalf("an active empty ledger must not reopen: %v", err)
	}
}
