package check

import (
	"bytes"
	"crypto/sha256"
	"runtime/debug"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/sketch/std"
)

// stdFreeProgram imports no std package.
const stdFreeProgram = `func main() needs Console {
	Console.Println("hello")
}
`

// TestStdDecodeGrowsWithImports is the counted budget of an installed
// snapshot (CHK1b round 2, audit-3 F3): a process decodes the std packages
// its imports reach and no other. A check of a program without std imports
// decodes none; a check of the budget program decodes time's import
// closure; the first import of each std package decodes exactly its import
// closure (read from the import declarations of the sources). It counts
// work, never time. It kills install-decodes-eagerly: a reader that decodes
// all of std at install passes every other test.
//
// It also counts the bytes read from the snapshot (CHK1c): the install
// reads no more than the header and the base, and a check then reads the
// segments of its std closure and no other byte. It kills
// snapshot-read-whole: a reader that reads the whole file at install
// decodes the same model and passes every other test, but holds the whole
// std in memory for a program that imports none of it.
func TestStdDecodeGrowsWithImports(t *testing.T) {
	ResetStdForTest()
	defer ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := openSnapshot(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var fixed int64
	install := func() {
		t.Helper()
		ResetStdForTest()
		installBytes(t, data)
		fixed = WorkDone().StdSnapshotBytesRead
		if before := int64(snap.segs[1].off); fixed > before {
			t.Fatalf("the install read %d bytes; want at most the %d before the first package segment", fixed, before)
		}
	}
	want := func(closure []string) Work {
		w := Work{StdPackagesDecoded: int64(len(closure)), StdSnapshotBytesRead: fixed}
		for _, p := range closure {
			w.StdSnapshotBytesRead += int64(snap.segs[snap.byPath[p]].size)
		}
		return w
	}
	for _, c := range []struct {
		name, src string
		closure   []string
	}{{"std-free", stdFreeProgram, nil}, {"budget", budgetProgram, stdClosure(t, "time")}} {
		install()
		if _, ds := CheckSource(c.src); len(ds) != 0 {
			t.Fatalf("%s: %v", c.name, ds)
		}
		if got, want := WorkDone(), want(c.closure); got != want {
			t.Errorf("a check of the %s program did %+v; want %+v, the std closure %v alone", c.name, got, want, c.closure)
		}
	}
	for _, path := range std.Packages() {
		install()
		if _, err := Std.Import(path); err != nil {
			t.Fatal(err)
		}
		closure := stdClosure(t, path)
		if got, want := WorkDone(), want(closure); got != want {
			t.Errorf("importing %s did %+v; want %+v, its std closure %v alone", path, got, want, closure)
		}
	}
}

// TestInstallGrowsTheStackOnce counts the stack moves of the first check
// after an install: zero (CHK1c r2). InstallStd grows its goroutine's
// stack once, while few frames are on it (growStack). A stack that grew
// later, inside the decoder's or the checker's recursion, would be copied
// with all those frames on it, and each copy reads their stack maps:
// pages of the binary the process then holds (the receipt in
// docs/receipts/check-speed.md). The goroutine reads where its own frame
// lies after the install and after a check of the budget program, whose
// import of time decodes segments lazily; a move in between shows as two
// places. It runs on a fresh goroutine, whose stack starts small, with
// the collector off, so no stack shrink moves it. It kills
// stack-not-grown and stack-pad-small: each checks the same model and
// passes every other test.
func TestInstallGrowsTheStackOnce(t *testing.T) {
	ResetStdForTest()
	defer ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	ResetStdForTest()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	type result struct {
		err                   error
		ds                    []Diagnostic
		installed, afterCheck uintptr
	}
	done := make(chan result)
	go func() {
		var frame byte
		var r result
		r.err = InstallStd(bytes.NewReader(data), int64(len(data)))
		r.installed = uintptr(unsafe.Pointer(&frame))
		if r.err == nil {
			_, r.ds = CheckSource(budgetProgram)
		}
		r.afterCheck = uintptr(unsafe.Pointer(&frame))
		done <- r
	}()
	r := <-done
	if r.err != nil || len(r.ds) != 0 {
		t.Fatal(r.err, r.ds)
	}
	if r.installed != r.afterCheck {
		t.Errorf("the check after the install moved the stack (a frame at %#x after the install, at %#x after the check): it grew inside the recursion", r.installed, r.afterCheck)
	}
}

// installBytes installs a snapshot held in memory.
func installBytes(t *testing.T, data []byte) {
	t.Helper()
	if err := InstallStd(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
}

// TestStdSnapshotInstall guards the checked snapshot seam: after an
// export, a fresh checker installs the snapshot, imports every std package
// and checks a caller without checking any std package, and refuses a
// second install without changing its live identities. A corrupt header
// installs nothing; a corrupt package segment is checked from source,
// never decoded into a changed model. It kills install-rechecks-std,
// lazy-deps-skipped, export-corruption-accepted, header-corruption-accepted
// and install-overwrites-live;
// source-only checker tests never install bytes.
func TestStdSnapshotInstall(t *testing.T) {
	ResetStdForTest()
	defer ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	ResetStdForTest()
	snap, _, err := openSnapshot(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	// A flipped header byte that still parses (a letter of the first
	// package's path) is refused before the universe is built.
	header := append([]byte(nil), data...)
	at := len(snapshotMagic) + sha256.Size
	at += bytes.Index(header[at:], []byte(snap.segs[1].path))
	header[at] ^= 1
	if err := InstallStd(bytes.NewReader(header), int64(len(header))); err == nil {
		t.Fatal("snapshot with a corrupt header installed")
	}
	// A flipped byte at the end of the last segment: that package comes
	// from source, as if the snapshot did not hold it.
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 1
	installBytes(t, corrupt)
	last := snap.segs[len(snap.segs)-1].path
	if p, err := Std.Import(last); err != nil || p == nil || p.Path != last {
		t.Fatalf("%s from a corrupt segment: %v, %v", last, p, err)
	}
	if got := WorkDone(); got.StdPackagesChecked != 1 || got.StdPackagesDecoded != int64(len(stdClosure(t, last))-1) {
		t.Fatalf("a corrupt segment of %s did %+v; want it checked from source, its imports decoded", last, got)
	}
	ResetStdForTest()
	installBytes(t, data)
	for _, path := range std.Packages() {
		p, err := Std.Import(path)
		if err != nil || p == nil || p.Path != path {
			t.Fatalf("installed %s: %v, %v", path, p, err)
		}
	}
	if _, ds := CheckSource(budgetProgram); len(ds) != 0 {
		t.Fatal(ds)
	}
	got := WorkDone()
	if want := (Work{StdPackagesDecoded: int64(len(std.Packages())), StdSnapshotBytesRead: got.StdSnapshotBytesRead}); got != want {
		t.Fatalf("warm snapshot did %+v; want %+v, no source work", got, want)
	}
	u, p := universe(), prelude().scope
	if err := InstallStd(bytes.NewReader(data), int64(len(data))); err != ErrInstalled {
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
	data, err := encodeSnapshot(&stdSnapshot{base: &stdBase{ref: &Named{under: view}}})
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, err := openSnapshot(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	want := view.Solution().String()
	if got := decoded.ref.under.(*RowVar).Solution().String(); got != want {
		t.Fatalf("deferred row after round trip: %s; from source: %s", got, want)
	}
}
