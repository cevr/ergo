package stdlib_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
)

const fsEntryNativeTestSrc = `import fs
import fs/mapped
import fstest
import time

type hostBox {
	Host() fs.Host,

	law HostEntryWorks() {
		let h = Host()
		let e = fs.ReadEntry(h, ".") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			return false
		}
		match e.Kind {
		case fs.DirEntry:
			true
		case fs.FileEntry, fs.SymlinkEntry, fs.OtherEntry:
			false
		}
	},
}

type realHostBox { h fs.Host } implements hostBox

func Host(b realHostBox) fs.Host { b.h }

let onDiskEntries = context needs fstest.TestAmbient {
	fs.Files:   fs.NewTestHost(),
	fs.Writes:  fs.NewTestHost(),
	fs.Entries: fs.NewTestHost(),
	hostBox:    realHostBox{h: fs.NewTestHost()},
}

type probeProvider { h fs.Host } implements fs.Entries

func ReadFile(p probeProvider, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.ReadFile(p.h, path)
}

func ReadDir(p probeProvider, path string) []string fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.ReadDir(p.h, path)
}

func Stat(p probeProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.Stat(p.h, path)
}

func Lstat(p probeProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.Lstat(p.h, path)
}

func ReadEntry(p probeProvider, path string) fs.Entry fails fs.NotFound | fs.Denied | fs.Failed {
	try fs.ReadEntry(p.h, path)
}

func checkEntryAgrees(path string) bool needs fs.Entries {
	let l = fs.Entries.Lstat(path) catch {
	case fs.NotFound as err:
		let _ = fs.Entries.ReadEntry(path) catch {
		case fs.NotFound as got:
			return got == err
		case fs.Denied, fs.Failed:
			return false
		}
		return false
	case fs.Denied as err:
		let _ = fs.Entries.ReadEntry(path) catch {
		case fs.Denied as got:
			return got == err
		case fs.NotFound, fs.Failed:
			return false
		}
		return false
	case fs.Failed as err:
		let _ = fs.Entries.ReadEntry(path) catch {
		case fs.Failed as got:
			return got == err
		case fs.NotFound, fs.Denied:
			return false
		}
		return false
	}
	let e = fs.Entries.ReadEntry(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return false
	}
	let isDir = match e.Kind {
	case fs.DirEntry:
		true
	case fs.FileEntry, fs.SymlinkEntry, fs.OtherEntry:
		false
	}
	isDir == l.Dir && e.Size == l.Size && e.AllocatedBytes >= 0 && e.AllocatedBytes%512 == 0 && e.Nlink >= 1
}

func isKindFile(k fs.EntryKind) bool {
	match k {
	case fs.FileEntry:
		true
	case fs.DirEntry, fs.SymlinkEntry, fs.OtherEntry:
		false
	}
}

func isKindDir(k fs.EntryKind) bool {
	match k {
	case fs.DirEntry:
		true
	case fs.FileEntry, fs.SymlinkEntry, fs.OtherEntry:
		false
	}
}

func isKindSymlink(k fs.EntryKind) bool {
	match k {
	case fs.SymlinkEntry:
		true
	case fs.FileEntry, fs.DirEntry, fs.OtherEntry:
		false
	}
}

func entrySummary(path string) string needs fs.Entries {
	let e = fs.Entries.ReadEntry(path) catch {
	case fs.NotFound:
		return "notfound"
	case fs.Denied:
		return "denied"
	case fs.Failed:
		return "failed"
	}
	let kind = match e.Kind {
	case fs.FileEntry:
		"file:\(e.Size)"
	case fs.DirEntry:
		"dir"
	case fs.SymlinkEntry:
		"symlink:\(e.Size)"
	case fs.OtherEntry:
		"other"
	}
	"\(kind):\(e.Nlink >= 1):\(e.AllocatedBytes >= 0 && e.AllocatedBytes%512 == 0)"
}

func populateAgreementTree() fails fs.NotFound | fs.Denied | fs.Failed | fs.CrossDevice needs fs.Writes {
	try fs.Writes.MkdirAll("sub/nested")
	try fs.Writes.WriteFile("sub/empty", "")
	try fs.Writes.WriteFile("sub/small", "hello")
	try fs.Writes.AppendFile("sub/small", "-world")
	let mut big = ""
	for _ in 0..80 {
		big = big + "abcdefghij"
	}
	try fs.Writes.WriteFile("sub/big", big)
	try fs.Writes.WriteFile("sub/src", "move-me")
	try fs.Writes.Rename("sub/src", "sub/nested/moved")
}

test MemoryEntrySemantics {
	let mem = fstest.NewVolumes([]string{"v1", "v2"})
	with context { fs.Files: mem, fs.Writes: mem, fs.Entries: mem } {
		try populateAgreementTree()
		try fs.Writes.WriteFile("v1/a", "one")
		try fs.Writes.WriteFile("v2/a", "one")

		for p in []string{"", "missing", "sub", "sub/empty", "sub/small", "sub/big", "sub/nested/moved", "sub/small/child", "v1/a", "v2/a"} {
			assert checkEntryAgrees(p)
		}

		let dirEntry = try fs.Entries.ReadEntry("sub")
		assert isKindDir(dirEntry.Kind) && dirEntry.Size == 0 && dirEntry.AllocatedBytes == 0 && dirEntry.Nlink == 1

		let emptyEntry = try fs.Entries.ReadEntry("sub/empty")
		assert isKindFile(emptyEntry.Kind) && emptyEntry.Size == 0 && emptyEntry.AllocatedBytes == 0 && emptyEntry.Nlink == 1

		let smallBefore = try fs.Entries.ReadEntry("sub/small")
		assert isKindFile(smallBefore.Kind) && smallBefore.Size == 11 && smallBefore.AllocatedBytes == 512 && smallBefore.Nlink == 1
		assert time.Equal(smallBefore.ModTime, smallBefore.ModTime) && time.Equal(smallBefore.AccessTime, smallBefore.AccessTime)

		let bigEntry = try fs.Entries.ReadEntry("sub/big")
		assert isKindFile(bigEntry.Kind) && bigEntry.Size == 800 && bigEntry.AllocatedBytes == 1024
		assert emptyEntry.ID != dirEntry.ID && emptyEntry.ID != smallBefore.ID && smallBefore.ID != bigEntry.ID && emptyEntry.ID != bigEntry.ID

		let movedBefore = try fs.Entries.ReadEntry("sub/nested/moved")
		try fs.Writes.AppendFile("sub/nested/moved", "-app")
		let movedAppended = try fs.Entries.ReadEntry("sub/nested/moved")
		assert movedAppended.ID == movedBefore.ID
		assert movedAppended.Size == 11 && movedAppended.AllocatedBytes == 512
		assert time.After(movedAppended.ModTime, movedBefore.ModTime)

		try fs.Writes.WriteFile("sub/nested/moved", "updated-content")
		let movedAfter = try fs.Entries.ReadEntry("sub/nested/moved")
		assert movedAfter.ID != movedAppended.ID
		assert movedAfter.Size == 15 && movedAfter.AllocatedBytes == 512
		assert time.After(movedAfter.ModTime, movedAppended.ModTime)

		try fs.Writes.Rename("sub/nested/moved", "sub/moved2")
		let movedRenamed = try fs.Entries.ReadEntry("sub/moved2")
		assert movedRenamed.ID == movedAfter.ID
		assert movedRenamed.ID != emptyEntry.ID

		let v1a = try fs.Entries.ReadEntry("v1/a")
		let v2a = try fs.Entries.ReadEntry("v2/a")
		assert v1a.ID != v2a.ID

		let seen = Map[fs.FileID, string]{v1a.ID: "v1/a", v2a.ID: "v2/a"}
		assert seen.Len() == 2
		assert try mem.ReadEntry("sub/empty") == emptyEntry
	}
}

test HostEntryAndAgreement {
	let mem = fstest.NewMemory()
	with context { fs.Files: mem, fs.Writes: mem, fs.Entries: mem } {
		try populateAgreementTree()
	}

	with onDiskEntries {
		let h = hostBox.Host()
		let prov = probeProvider{h: h}
		let mh = mapped.NewHost(h)
		with context { fs.Files: h, fs.Writes: h, fs.Entries: prov } {
			try populateAgreementTree()
			let mut big = ""
			for _ in 0..80 {
				big = big + "abcdefghij"
			}
			let beforeWrite = try fs.Entries.ReadEntry("sub/nested/moved")
			try fs.Writes.AppendFile("sub/nested/moved", "-app")
			let afterAppend = try fs.Entries.ReadEntry("sub/nested/moved")
			assert afterAppend.ID == beforeWrite.ID

			try fs.Writes.WriteFile("sub/nested/moved", "updated-content")
			let afterWrite = try fs.Entries.ReadEntry("sub/nested/moved")
			assert afterWrite.ID != afterAppend.ID

			try fs.Writes.Rename("sub/nested/moved", "sub/moved2")
			let afterRename = try fs.Entries.ReadEntry("sub/moved2")
			assert afterRename.ID == afterWrite.ID

			try fs.Link(h, "sub/big", "sub/big-hardlink")
			try fs.Symlink(h, "big", "sub/sym-file")
			try fs.Symlink(h, "nested", "sub/sym-dir")
			try fs.Symlink(h, "does-not-exist", "sub/sym-dangling")

			for p in []string{"", "missing", "../outside", "/etc/passwd", "sub", "sub/empty", "sub/small", "sub/big", "sub/big-hardlink", "sub/sym-file", "sub/sym-dir", "sub/sym-dangling", "sub/small/child"} {
				assert checkEntryAgrees(p)
			}
			assert entrySummary("../outside") == "denied"
			assert entrySummary("/etc/passwd") == "denied"

			let symFile = try fs.Entries.ReadEntry("sub/sym-file")
			let symDir = try fs.Entries.ReadEntry("sub/sym-dir")
			let dangling = try fs.Entries.ReadEntry("sub/sym-dangling")
			assert isKindSymlink(symFile.Kind) && symFile.Size == 3
			assert isKindSymlink(symDir.Kind) && symDir.Size == 6
			assert isKindSymlink(dangling.Kind) && dangling.Size == 14

			let hardA = try fs.Entries.ReadEntry("sub/big")
			let hardB = try fs.Entries.ReadEntry("sub/big-hardlink")
			let other = try fs.Entries.ReadEntry("sub/small")
			assert isKindFile(hardA.Kind) && isKindFile(hardB.Kind)
			assert hardA.ID == hardB.ID && hardA.Nlink >= 2 && hardB.Nlink >= 2
			assert hardA.ID != other.ID && other.Nlink == 1
			assert hardA.Size == big.Len()
			assert hardA.AllocatedBytes >= 0 && hardA.AllocatedBytes%512 == 0

			assert try fs.ReadEntry(h, "sub/sym-dir") == symDir
			assert try mapped.ReadEntry(mh, "sub/big-hardlink") == hardB

			let hostFile = entrySummary("sub/big")
			let hostDir = entrySummary("sub")
			let hostMissing = entrySummary("missing")
			let hostUnderFile = entrySummary("sub/small/child")
			with context { fs.Files: mem, fs.Entries: mem } {
				assert entrySummary("sub/big") == hostFile
				assert entrySummary("sub") == hostDir
				assert entrySummary("missing") == hostMissing
				assert entrySummary("sub/small/child") == hostUnderFile
			}
		}

		with context { fs.Entries: mh } {
			assert checkEntryAgrees("sub/sym-dir")
			assert checkEntryAgrees("sub/big-hardlink")
		}

		with context { fs.Files: h, fs.Writes: h, fs.Entries: h } {
			try fs.Writes.WriteFile("disk.txt", "abc")
			assert checkEntryAgrees("disk.txt")
			assert checkEntryAgrees("no-such")
		}
	}
}
`

const fsEntryOracleSrc = `import fs
import fs/mapped
import io
import os
import time

func kindName(k fs.EntryKind) string {
	match k {
	case fs.FileEntry:
		"file"
	case fs.DirEntry:
		"dir"
	case fs.SymlinkEntry:
		"symlink"
	case fs.OtherEntry:
		"other"
	}
}

func formatLine(path string, e fs.Entry) string {
	"\(path)\t\(kindName(e.Kind))\t\(e.Size)\t\(e.AllocatedBytes)\t\(time.UnixSeconds(e.ModTime))\t\(time.Nanosecond(e.ModTime))\t\(time.UnixSeconds(e.AccessTime))\t\(time.Nanosecond(e.AccessTime))\t\(e.Nlink)"
}

func probeLine(h fs.Host, p string) string needs fs.Entries {
	let viaCap = fs.Entries.ReadEntry(p) catch {
	case fs.NotFound:
		return "\(p)\tnotfound"
	case fs.Denied:
		return "\(p)\tdenied"
	case fs.Failed:
		return "\(p)\tfailed"
	}
	let viaHost = fs.ReadEntry(h, p) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "ERR host mismatch"
	}
	if viaCap != viaHost {
		return "ERR cap mismatch"
	}
	formatLine(p, viaCap)
}

func main() needs Ambient | Console {
	let h = fs.NewHost()
	let mh = mapped.NewHost(h)
	let args = os.Args(os.NewHost())
	if args.Len() < 3 {
		io.Console.Println("usage")
		return
	}
	let hardA = args[0]
	let hardB = args[1]
	let other = args[2]
	let eA = fs.ReadEntry(h, hardA) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		io.Console.Println("ERR hardA")
		return
	}
	let eB = mapped.ReadEntry(mh, hardB) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		io.Console.Println("ERR hardB")
		return
	}
	let eO = with context { fs.Entries: h } {
		fs.Entries.ReadEntry(other) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("ERR other")
			return
		}
	}
	if eA.ID != eB.ID || eA.ID == eO.ID {
		io.Console.Println("ERR id")
		return
	}
	let idMap = Map[fs.FileID, string]{eA.ID: "hard", eO.ID: "other"}
	if idMap.Len() != 2 {
		io.Console.Println("ERR map")
		return
	}
	with context { fs.Entries: h } {
		for i in 0..args.Len() {
			io.Console.Println(probeLine(h, args[i]))
		}
	}
}
`

func buildErgoForFSEntry(t *testing.T, dir string) string {
	t.Helper()
	root := moduleRootDir(t)
	ergoBin := filepath.Join(dir, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	return ergoBin
}

// TestStdFSEntryLawsRun verifies fs.Entries.EntryAgreesWithLstat and
// host/memory Entry semantics via native Ergo compilation (ERGO_FALLBACK=off),
// recording the law ledger entry via lawledger.Run.
func TestStdFSEntryLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	var entryRow lawledger.Row
	found := false
	for _, row := range rows {
		if row.Key() == "fs Entries.EntryAgreesWithLstat" {
			entryRow = row
			found = true
			break
		}
	}
	if !found {
		t.Fatal(`runners.txt missing "fs Entries.EntryAgreesWithLstat"`)
	}
	statement, ok := lawledger.StdStatement(entryRow.Key())
	if !ok || statement == "" {
		t.Fatalf("lawledger.StdStatement(%q) returned empty", entryRow.Key())
	}

	laws := map[string]lawledger.Law{
		entryRow.Key(): {
			Statement: statement,
			Check: func(t *testing.T) {
				tmp := t.TempDir()
				ergoBin := buildErgoForFSEntry(t, tmp)
				modDir := filepath.Join(tmp, "entrymod")
				if err := os.MkdirAll(modDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module entrymod\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(modDir, "fs_entry_native_test.ergo"), []byte(fsEntryNativeTestSrc), 0o644); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
				cmd.Dir = modDir
				cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
				out, err := runBound(cmd)
				if err != nil {
					t.Fatalf("ergo test --native failed (ERGO_FALLBACK=off): %v\n%s", err, out)
				}
				for _, want := range []string{"ok MemoryEntrySemantics", "ok HostEntryAndAgreement"} {
					if !strings.Contains(out, want) {
						t.Fatalf("expected %q in output:\n%s", want, out)
					}
				}

				badDir := filepath.Join(tmp, "badentrymod")
				if err := os.MkdirAll(badDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(badDir, "ergo.mod"), []byte("module badentrymod\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				const badSrc = `import fs
import time

type badDirProvider {} implements fs.Entries

func ReadFile(_ badDirProvider, _ string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ badDirProvider, _ string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ badDirProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false, Perm: 0}
}
func Lstat(_ badDirProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false, Perm: 0}
}
func ReadEntry(_ badDirProvider, _ string) fs.Entry fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Entry{Kind: fs.DirEntry, Size: 0, AllocatedBytes: 0, ModTime: time.Unix(0, 0), AccessTime: time.Unix(0, 0), Nlink: 1, ID: fs.NewFileID(1, 1)}
}
`
				if err := os.WriteFile(filepath.Join(badDir, "bad_test.ergo"), []byte(badSrc), 0o644); err != nil {
					t.Fatal(err)
				}
				badCmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
				badCmd.Dir = badDir
				badCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
				badOut, badErr := runBound(badCmd)
				if badErr == nil {
					t.Fatalf("expected broken fs.Entries provider to fail EntryAgreesWithLstat; got pass:\n%s", badOut)
				}
				if !strings.Contains(badOut, "EntryAgreesWithLstat") {
					t.Fatalf("expected EntryAgreesWithLstat failure in output:\n%s", badOut)
				}
			},
		},
	}
	lawledger.Run(t, laws)
}

// TestFSEntryHostAgreesWithOSLstat compiles an Ergo binary with ERGO_FALLBACK=off
// and verifies every Entry field (Kind, Size, AllocatedBytes, ModTime, AccessTime,
// Nlink, FileID, and NotFound) against Go's runtime/v1.FSLstatEntry / os.Lstat.
func TestFSEntryHostAgreesWithOSLstat(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo and probe binary")
	}
	tmp := t.TempDir()
	ergoBin := buildErgoForFSEntry(t, tmp)

	modDir := filepath.Join(tmp, "oraclemod")
	mainPath := filepath.Join(modDir, "cmd", "probe", "main.ergo")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module oraclemod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte(fsEntryOracleSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	probeBin := filepath.Join(tmp, "bin", "probe")
	compile := exec.Command(ergoBin, "build", "./cmd/probe", "-o", probeBin)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build probe (ERGO_FALLBACK=off): %v\n%s", err, out)
	}

	disk := filepath.Join(tmp, "disk")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(disk, "data.bin")
	if err := os.WriteFile(reg, []byte(strings.Repeat("x", 6000)), 0o644); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(disk, "data-hard.bin")
	if err := os.Link(reg, hard); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(disk, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(disk, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	symFile := filepath.Join(disk, "sym-file")
	if err := os.Symlink(reg, symFile); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(disk, "sym-dir")
	if err := os.Symlink(sub, symDir); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(disk, "sym-dangling")
	if err := os.Symlink("nonexistent-target", dangling); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(disk, "fifo")
	mkfifoForFSEntryTest(t, fifo)
	missing := filepath.Join(disk, "missing")

	// Force a distinct access and modification timestamp on reg before querying.
	if err := os.Chtimes(reg, unixTime(1700000111, 222000000), unixTime(1700000333, 444000000)); err != nil {
		t.Fatal(err)
	}

	paths := []string{reg, hard, empty, sub, symFile, symDir, dangling, fifo, missing}
	run := exec.Command(probeBin, paths...)
	out, err := runBound(run)
	if err != nil {
		t.Fatalf("run probe: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(paths) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(paths), out)
	}
	for i, p := range paths {
		fields := strings.Split(lines[i], "\t")
		if p == missing {
			if len(fields) != 2 || fields[0] != p || fields[1] != "notfound" {
				t.Fatalf("missing line = %q, want %q", lines[i], p+"\tnotfound")
			}
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		expKind, expSize, expAlloc, expModSec, expModNsec, expAccSec, expAccNsec, expNlink := oracleEntryFields(t, p, fi)
		want := fmt.Sprintf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d",
			p, expKind, expSize, expAlloc, expModSec, expModNsec, expAccSec, expAccNsec, expNlink)
		if lines[i] != want {
			t.Errorf("entry %s:\n got: %s\nwant: %s", p, lines[i], want)
		}
	}
}
