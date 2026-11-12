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

// TestStdSnapshotRestoresDeferredRows is the smallest round-trip repro of
// the deferred-row reconstruction defect (export-subst-row-lost). A generic
// inferred row is exported before its concrete use is first resolved.
// Existing std examples read already memoized rows, and do not catch a
// decoder that forgets the callback. The format carries the whole checker
// graph, not just the currently forced part of std.
func TestStdSnapshotRestoresDeferredRows(t *testing.T) {
	c, ds := checkFor(`type Failed {}
func apply[E](f func() fails E) { try f() }
`)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	f := c.pkg.Funcs[0]
	failed := c.pkg.Types[0].Type
	bound := &RowArg{Row: newRow().with(c.types, failed)}
	view := c.subst(f.errVar, map[*TypeParam]Type{f.Sig.TParams[0]: bound}).(*RowVar)
	// ref is a graph root; under holds the deferred row and ctx reaches
	// its owning checker. Do not force Solution before encoding it.
	data, err := encodeSnapshot(&stdSnapshot{ref: &Named{under: view}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	want := view.Solution().String()
	if got := decoded.ref.under.(*RowVar).Solution().String(); got != want {
		t.Fatalf("deferred row after round trip: %s; from source: %s", got, want)
	}
}
