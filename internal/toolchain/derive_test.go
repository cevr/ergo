package toolchain

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestIRPatchIsCurrent checks that toolchain/ir/go1.27.0.patch is the
// patch its source tree toolchain/ir/_go makes (DerivePatch), byte for
// byte, and that toolchain/ir/lock names its digest (PATCH-MERGE): the
// patch is derived, so a lane edits the files under _go and regenerates,
// and a hand-edited patch or a stale one after an _go edit fails here.
// Each new file's `index 0000000..<id>` line is the git blob id of its
// content, computed: an id that does not match its file (four did, from
// hand edits, before PATCH-MERGE) can no longer arise, and is never fixed
// by hand.
//
// Mutants: patch-derive-stale (an _go file edited, the patch not
// regenerated), patch-lock-digest-wrong (LockDigest writes another
// digest), patch-gofiles-hunk-dropped (a hook hunk of gofiles.patch
// changed), patch-derive-unsorted (DerivePatch writes its files out of
// path order).
func TestIRPatchIsCurrent(t *testing.T) {
	ir := filepath.Join("..", "..", "toolchain", "ir")
	fix := "regenerate it with `go run ./scripts/irport -write` from the repository root; edit the files under toolchain/ir/_go (and _go/" + GoFilesPatch + " for a hunk in Go's own files), never the patch"
	want, err := DerivePatch(filepath.Join(ir, "_go"))
	if err != nil {
		t.Fatalf("toolchain/ir/_go makes no patch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ir, "go1.27.0.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("toolchain/ir/go1.27.0.patch is not the patch toolchain/ir/_go makes (first difference at byte %d); %s", firstDiff(got, want), fix)
	}
	lock, err := os.ReadFile(filepath.Join(ir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	wantLock, err := LockDigest(lock, "go1.27.0.patch", want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lock, wantLock) {
		t.Errorf("toolchain/ir/lock does not name the digest of the patch toolchain/ir/_go makes; %s", fix)
	}
}

// firstDiff is the offset of the first byte where a and b differ.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
