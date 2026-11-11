package check

import (
	"testing"

	"ergo.local/prototype/internal/sketch/std"
)

// TestStdSnapshotInstall guards the checked snapshot seam: after an
// export, a fresh checker installs the graph with all std entries complete,
// checks a caller without checking any std package, and refuses a second
// install without changing its live identities. It kills install-rechecks-std
// and install-overwrites-live; source-only checker tests never install bytes.
func TestStdSnapshotInstall(t *testing.T) {
	ResetStdForTest()
	defer ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	ResetStdForTest()
	// Flip a byte near the end of an otherwise valid graph (rather than
	// only its magic), and refuse it without initializing the universe.
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 1
	if err := InstallStd(corrupt); err == nil {
		t.Fatal("corrupt snapshot installed")
	}
	if err := InstallStd(data); err != nil {
		t.Fatal(err)
	}
	for _, path := range std.Packages() {
		p, err := Std.Import(path)
		if err != nil || p == nil || p.Path != path {
			t.Fatalf("installed %s: %v, %v", path, p, err)
		}
	}
	if _, ds := CheckSource(budgetProgram); len(ds) != 0 {
		t.Fatal(ds)
	}
	if got := WorkDone(); got != (Work{}) {
		t.Fatalf("warm snapshot did %+v; want no source work", got)
	}
	u, p := universe(), prelude().scope
	if err := InstallStd(data); err != ErrInstalled {
		t.Fatalf("late install: %v; want ErrInstalled", err)
	}
	if universe() != u || prelude().scope != p {
		t.Fatal("late install replaced live identities")
	}
}
