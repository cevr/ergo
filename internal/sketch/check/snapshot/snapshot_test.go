package snapshot

import (
	"bytes"
	"crypto/sha256"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdFreeProgram imports no std package.
const stdFreeProgram = `func main() needs Console {
	Console.Println("hello")
}
`

// budgetProgram imports one std package, calls its externs and binds a
// provider of a std operation type (check's TestCheckWorkGrowsWithImports
// checks the same program).
const budgetProgram = `import time

let Manual = context { Scheduler: time.NewManual(0) }

func main() needs Console {
	let m = time.NewManual(0)
	Console.Println("\(m.Now().UnixSeconds())")
	Console.Println(with Manual { "bound" })
}
`

// stdClosure is the std import closure of the std package root, sorted,
// read from the import declarations of its sources.
func stdClosure(t *testing.T, root string) []string {
	var out []string
	var visit func(p string)
	visit = func(p string) {
		if slices.Contains(out, p) {
			return
		}
		srcs, ok := stdlib.Package(p)
		if !ok {
			return // a Go import
		}
		out = append(out, p)
		for _, src := range srcs {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("std package %s does not parse: %v", p, err)
			}
			for _, d := range f.Decls {
				if id, ok := d.(*syntax.ImportDecl); ok {
					for _, s := range id.Specs {
						visit(strings.Trim(s.Path.Value, "\"`"))
					}
				}
			}
		}
	}
	visit(root)
	slices.Sort(out)
	return out
}

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
	check.ResetStdForTest()
	defer check.ResetStdForTest()
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
		check.ResetStdForTest()
		installBytes(t, data)
		fixed = check.WorkDone().StdSnapshotBytesRead
		if before := int64(snap.segs[1].off); fixed > before {
			t.Fatalf("the install read %d bytes; want at most the %d before the first package segment", fixed, before)
		}
	}
	want := func(closure []string) check.Work {
		w := check.Work{StdPackagesDecoded: int64(len(closure)), StdSnapshotBytesRead: fixed}
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
		if _, ds := check.CheckSource(c.src); len(ds) != 0 {
			t.Fatalf("%s: %v", c.name, ds)
		}
		if got, want := check.WorkDone(), want(c.closure); got != want {
			t.Errorf("a check of the %s program did %+v; want %+v, the std closure %v alone", c.name, got, want, c.closure)
		}
	}
	for _, path := range stdlib.Packages() {
		install()
		if _, err := check.Std.Import(path); err != nil {
			t.Fatal(err)
		}
		closure := stdClosure(t, path)
		if got, want := check.WorkDone(), want(closure); got != want {
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
	check.ResetStdForTest()
	defer check.ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	check.ResetStdForTest()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	type result struct {
		err                   error
		ds                    []check.Diagnostic
		installed, afterCheck uintptr
	}
	done := make(chan result)
	go func() {
		var frame byte
		var r result
		r.err = InstallStd(bytes.NewReader(data), int64(len(data)))
		r.installed = uintptr(unsafe.Pointer(&frame))
		if r.err == nil {
			_, r.ds = check.CheckSource(budgetProgram)
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

// TestExportIsDeterministic checks Law ExportIsDeterministic (order.go):
// std checked from source twice in one process exports the same bytes,
// and so does an export that writes each run of tied map keys in the
// reverse order. The second check starts from nothing, so the law covers
// the checker's own state as well as the encoder's map order. It kills
// cs-snap-map-order, cs-snap-memo-exported, cs-snap-tree-unreached,
// cs-snap-ties-shallow and cs-check-map-order.
func TestExportIsDeterministic(t *testing.T) {
	check.ResetStdForTest()
	defer check.ResetStdForTest()
	export := func(what string) []byte {
		t.Helper()
		data, err := ExportStd()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return data
	}
	same := func(what string, a, b []byte) {
		t.Helper()
		if !bytes.Equal(a, b) {
			at := 0
			for at < min(len(a), len(b)) && a[at] == b[at] {
				at++
			}
			t.Errorf("%s: %d and %d bytes, first difference at byte %d", what, len(a), len(b), at)
		}
	}
	first := export("the first export")
	check.ResetStdForTest()
	second := export("the export of a second check")
	same("two checks of std export different bytes", first, second)
	reverseTiesForTest = true
	defer func() { reverseTiesForTest = false }()
	same("an export with its tied map keys reversed differs", second, export("the export with ties reversed"))
}

// TestStdSnapshotInstall guards the checked snapshot seam: after an
// export, a fresh checker installs the snapshot, imports every std package
// and checks a caller without checking any std package, and refuses a
// second install without changing its live identities. A corrupt header
// installs nothing; a corrupt package segment is checked from source,
// never decoded into a changed model. It kills install-rechecks-std,
// lazy-deps-skipped, export-corruption-accepted, header-corruption-accepted,
// install-overwrites-live, gb-snap-decoded-unmarked and
// gb-snap-prelude-unmarked (a decoded std package and the installed
// prelude are std, though the snapshot carries no mark);
// source-only checker tests never install bytes.
func TestStdSnapshotInstall(t *testing.T) {
	check.ResetStdForTest()
	defer check.ResetStdForTest()
	data, err := ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	check.ResetStdForTest()
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
	if p, err := check.Std.Import(last); err != nil || p == nil || p.Path != last {
		t.Fatalf("%s from a corrupt segment: %v, %v", last, p, err)
	}
	if got := check.WorkDone(); got.StdPackagesChecked != 1 || got.StdPackagesDecoded != int64(len(stdClosure(t, last))-1) {
		t.Fatalf("a corrupt segment of %s did %+v; want it checked from source, its imports decoded", last, got)
	}
	check.ResetStdForTest()
	installBytes(t, data)
	for _, path := range stdlib.Packages() {
		p, err := check.Std.Import(path)
		if err != nil || p == nil || p.Path != path {
			t.Fatalf("installed %s: %v, %v", path, p, err)
		}
		// The snapshot carries no std mark; the importer marks what it
		// decodes (gb-snap-decoded-unmarked).
		if !p.Std() {
			t.Fatalf("installed %s is not std", path)
		}
	}
	if _, ds := check.CheckSource(budgetProgram); len(ds) != 0 {
		t.Fatal(ds)
	}
	// So does the prelude the base installs (gb-snap-prelude-unmarked):
	// Option, a prelude type, is declared by a std package.
	if pkg, ds := check.CheckSource("func none() Option[int] { None }\n"); len(ds) != 0 {
		t.Fatal(ds)
	} else if n, ok := pkg.Funcs[0].Sig.Results[0].(*check.Named); !ok || !n.Obj.Pkg.Std() {
		t.Fatalf("the installed prelude's Option is not std: %v", pkg.Funcs[0].Sig.Results[0])
	}
	got := check.WorkDone()
	if want := (check.Work{StdPackagesDecoded: int64(len(stdlib.Packages())), StdSnapshotBytesRead: got.StdSnapshotBytesRead}); got != want {
		t.Fatalf("warm snapshot did %+v; want %+v, no source work", got, want)
	}
	u, p := check.LiveScopesForTest()
	if err := InstallStd(bytes.NewReader(data), int64(len(data))); err != check.ErrInstalled {
		t.Fatalf("late install: %v; want ErrInstalled", err)
	}
	if u2, p2 := check.LiveScopesForTest(); u2 != u || p2 != p {
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
	base, solution, err := check.DeferredRowForTest()
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeSnapshot(&stdSnapshot{base: base})
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, err := openSnapshot(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	want := solution(base)
	if got := solution(decoded); got != want {
		t.Fatalf("deferred row after round trip: %s; from source: %s", got, want)
	}
}
