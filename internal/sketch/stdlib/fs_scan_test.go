package stdlib_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

var fsScanErgoOnce struct {
	once sync.Once
	bin  string
	err  error
}

func fsScanRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func fsScanErgoBin(t *testing.T) string {
	t.Helper()
	fsScanErgoOnce.once.Do(func() {
		dir, err := os.MkdirTemp("", "ergo-fs-scan-bin-*")
		if err != nil {
			fsScanErgoOnce.err = err
			return
		}
		root := fsScanRepoRoot(t)
		bin := filepath.Join(dir, "ergo")
		cmd := ergobuild.Command(bin, "./cmd/ergo", "-buildvcs=false")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			fsScanErgoOnce.err = fmt.Errorf("build cmd/ergo: %v\n%s", err, out)
			return
		}
		fsScanErgoOnce.bin = bin
	})
	if fsScanErgoOnce.err != nil {
		t.Fatal(fsScanErgoOnce.err)
	}
	return fsScanErgoOnce.bin
}

func runFSScanErgoFile(t *testing.T, src string, args ...string) string {
	t.Helper()
	ergo := fsScanErgoBin(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "probe")
	buildCmd := exec.Command(ergo, "build", path, "-o", exe)
	buildCmd.Dir = dir
	buildCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("ergo build failed: %v\n%s", err, out)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe exec failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestStdFSScanLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}

	b := eval.DefaultBudget()
	b.Steps = 200_000_000
	b.Bytes = 1 << 30
	results := map[string][]eval.Result{}
	for _, pkgName := range []string{"fs", "fstest"} {
		pkg, err := check.Std.Import(pkgName)
		if err != nil {
			t.Fatalf("import %s: %v", pkgName, err)
		}
		rep, err := eval.Run(pkg, b)
		if err != nil {
			t.Fatalf("eval %s: %v", pkgName, err)
		}
		for _, r := range rep.Results {
			name := strings.TrimPrefix(r.Name, "law ")
			if head, _, ok := strings.Cut(name, " for "); ok {
				name = head
			}
			if head, _, ok := strings.Cut(name, " under "); ok {
				name = head
			}
			key := pkgName + " " + name
			results[key] = append(results[key], r)
		}
	}

	// Native host execution of std:fs laws (exercises Host.ReadDirEntries on disk).
	ergo := fsScanErgoBin(t)
	fsCmd := exec.Command(ergo, "test", "--fresh", "std:fs")
	fsCmd.Dir = fsScanRepoRoot(t)
	fsCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	fsOut, fsErr := fsCmd.CombinedOutput()
	if fsErr != nil {
		t.Fatalf("ergo test --fresh std:fs failed: %v\n%s", fsErr, fsOut)
	}
	fsStdout := string(fsOut)

	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		statement, _ := lawledger.StdStatement(row.Key())
		if row.Pkg == "fs" {
			key := row.Key()
			laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
				if !strings.Contains(fsStdout, "ok law "+row.Law) {
					t.Errorf("std:fs native run did not pass %s:\n%s", row.Law, fsStdout)
				}
			}}
			continue
		}
		rs := results[row.Key()]
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if len(rs) == 0 {
				t.Errorf("std %s has no evaluation result for %s", row.Pkg, row.Law)
				return
			}
			passed := 0
			for _, r := range rs {
				switch r.Status {
				case eval.Passed:
					if r.Evidence != "tested" && r.Evidence != "proved" {
						t.Errorf("law %s (%s): passed with evidence=%q", row.Key(), r.Name, r.Evidence)
					} else {
						passed++
					}
				case eval.Skipped, eval.Native:
				default:
					t.Errorf("law %s (%s): %s (%s) after %d cases: %+v", row.Key(), r.Name, r.Status, r.Reason, r.Cases, r.Arguments)
				}
			}
			if passed == 0 {
				t.Errorf("law %s: no run passed (%+v)", row.Key(), rs)
			}
		}}
	}
	if len(laws) < 6 {
		t.Fatalf("expected >= 6 laws wired to %s, got %d", t.Name(), len(laws))
	}
	lawledger.Run(t, laws)

	// Negative provider test: a broken Scans implementation whose ReadDirEntries
	// disagrees with ReadDir must fail Scans.ScanAgreesWithReadDir.
	badDir := t.TempDir()
	badSrc := `import fs
import time

type badScanProvider {} implements fs.Scans

func ReadFile(_ badScanProvider, _ string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ badScanProvider, path string) []string fails fs.NotFound | fs.Denied | fs.Failed {
	if path == "." { return []string{"a"} }
	fs.NotFound{Path: path}
}
func Stat(_ badScanProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: path == ".", Perm: 0o644}
}
func Lstat(_ badScanProvider, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: path == ".", Perm: 0o644}
}
func ReadEntry(_ badScanProvider, path string) fs.Entry fails fs.NotFound | fs.Denied | fs.Failed {
	if path == "." || path == "a" {
		return fs.Entry{Kind: if path == "." { fs.DirEntry } else { fs.FileEntry }, Size: 1, AllocatedBytes: 512, ModTime: time.Unix(0, 0), AccessTime: time.Unix(0, 0), Nlink: 1, ID: fs.NewFileID(1, 2)}
	}
	fs.NotFound{Path: path}
}
func ReadDirEntries(_ badScanProvider, path string) []fs.DirChild fails fs.NotFound | fs.Denied | fs.Failed {
	if path == "." { return []fs.DirChild{} }
	fs.NotFound{Path: path}
}

let onBad = context {
	fs.Files:   badScanProvider,
	fs.Entries: badScanProvider,
	fs.Scans:   badScanProvider,
}

test BadProviderCaught() {
	assert true
}
`
	badFile := filepath.Join(badDir, "bad_test.ergo")
	if err := os.WriteFile(badFile, []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	badCmd := exec.Command(ergo, "test", "--fresh", badFile)
	badCmd.Dir = badDir
	badCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	badOut, badErr := badCmd.CombinedOutput()
	if badErr == nil || !strings.Contains(string(badOut), "ScanAgreesWithReadDir") {
		t.Fatalf("expected broken fs.Scans provider to fail ScanAgreesWithReadDir; err=%v out:\n%s", badErr, badOut)
	}

	// Confined NewTestHost probe: escaping paths must fail fs.Denied on both
	// ReadDirEntries and Scan, and child entries must return sorted by Name.
	confDir := t.TempDir()
	confSrc := `import fs

let onConfinedScan = context needs TestAmbient {
	fs.Files, fs.Writes, fs.Entries, fs.Scans: fs.NewTestHost(),
}

test ConfinedScanRefusesEscapeAndSorts() {
	with onConfinedScan {
		let mut deniedDir = false
		let _ = fs.Scans.ReadDirEntries("..") catch {
		case fs.Denied:
			deniedDir = true
			[]fs.DirChild{}
		case fs.NotFound, fs.Failed:
			[]fs.DirChild{}
		}
		assert deniedDir

		let mut deniedScan = false
		let _ = fs.Scan("../outside", fs.DefaultScanOptions()) catch {
		case fs.Denied:
			deniedScan = true
			fs.NewScanBuffer()
		case fs.NotFound, fs.Failed:
			fs.NewScanBuffer()
		}
		assert deniedScan

		try fs.Writes.WriteFile("zzz.txt", "3")
		try fs.Writes.WriteFile("mmm.txt", "2")
		try fs.Writes.WriteFile("aaa.txt", "1")
		let kids = try fs.Scans.ReadDirEntries(".")
		assert kids.Len() == 3
		assert kids[0].Name == "aaa.txt" && kids[1].Name == "mmm.txt" && kids[2].Name == "zzz.txt"
		for k in kids {
			let single = try fs.Entries.ReadEntry(k.Name)
			assert k.Entry.Kind == single.Kind && k.Entry.Size == single.Size && k.Entry.AllocatedBytes == single.AllocatedBytes && k.Entry.ID == single.ID
		}
	}
}
`
	confFile := filepath.Join(confDir, "confined_scan_test.ergo")
	if err := os.WriteFile(confFile, []byte(confSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	confCmd := exec.Command(ergo, "test", "--fresh", confFile)
	confCmd.Dir = confDir
	confCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if confOut, confErr := confCmd.CombinedOutput(); confErr != nil {
		t.Fatalf("confined scan test failed: %v\n%s", confErr, confOut)
	}
}

const fsScanParityProg = `import fs
import fstest
import filepath
import io
import os
import strings

func kindTag(k fs.EntryKind) string {
	match k {
	case fs.FileEntry:
		"F"
	case fs.DirEntry:
		"D"
	case fs.SymlinkEntry:
		"L"
	case fs.OtherEntry:
		"O"
	}
}

func errKindTag(k fs.ScanErrorKind) string {
	match k {
	case fs.ErrDenied:
		"DENIED"
	case fs.ErrNotFound:
		"NOTFOUND"
	case fs.ErrFailed:
		"FAILED"
	}
}

func formatScan(label string, root string, b fs.ScanBuffer) string {
	let mut lines = []string{}
	lines = lines.Append("\(label)|COUNT=\(b.Len())|TSIZE=\(b.TotalSize)|TALLOC=\(b.TotalAlloc)|UNIQ=\(b.UniqueFiles)|HLINK=\(b.Hardlinks)|ERRS=\(b.Errors.Len())")
	for i in 0..b.Len() {
		let p = b.Paths[i]
		let rel = if p == root { "." } else if strings.HasPrefix(p, root + "/") { strings.Slice(p, root.Len()+1, p.Len()) } else { p }
		let ent = fs.EntryAt(b, uint32(i))
		let dupTag = if fs.IsDuplicate(b, uint32(i)) { "1" } else { "0" }
		lines = lines.Append("\(label)|N|\(i)|\(rel)|\(b.Names[i])|\(b.Parents[i])|\(b.Depths[i])|\(kindTag(ent.Kind))|\(ent.Size)|\(ent.AllocatedBytes)|\(ent.Nlink)|\(fs.Dev(ent.ID))|\(fs.Ino(ent.ID))|\(dupTag)")
	}
	for e in b.Errors {
		let erel = if strings.HasPrefix(e.Path, root + "/") { strings.Slice(e.Path, root.Len()+1, e.Path.Len()) } else { e.Path }
		lines = lines.Append("\(label)|E|\(erel)|\(e.Node)|\(errKindTag(e.Kind))")
	}
	strings.Join(lines, "\n")
}

func buildMemFixture() fstest.Memory fails fs.NotFound | fs.Denied | fs.Failed {
	let m = fstest.NewMemory()
	try m.MkdirAll("/tree/a/deep")
	try m.MkdirAll("/tree/b")
	try m.MkdirAll("/tree/prune")
	try m.MkdirAll("/tree/skip")
	try m.WriteFile("/tree/empty.txt", "")
	try m.WriteFile("/tree/small.txt", "hello")
	let mut big = ""
	for _ in 0..513 {
		big = big + "z"
	}
	try m.WriteFile("/tree/a/mid.bin", big)
	try m.WriteFile("/tree/a/deep/leaf.txt", "leaf")
	try m.WriteFile("/tree/b/beta.txt", "bb")
	try m.WriteFile("/tree/prune/hidden.txt", "hidden")
	try m.WriteFile("/tree/skip/gone.txt", "gone")
	m
}

func runFilter(path string, _ fs.Entry, _ int) fs.VisitAction {
	let base = filepath.Base(path)
	if base == "skip" {
		return fs.Skip
	}
	if base == "prune" {
		return fs.Prune
	}
	fs.Include
}

func main() needs io.Console | os.Ambient {
	let args = os.Args(os.NewHost())
	let hostRoot = args[0]
	let permDir = args[1]
	let h = fs.NewHost()
	let m = buildMemFixture() catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		io.Console.Println("MEM_BUILD_ERR")
		return
	}

	let mut bufH = fs.NewScanBuffer()
	let mut bufM = fs.NewScanBuffer()

	with context { fs.Files: h, fs.Entries: h, fs.Scans: h } {
		fs.ScanInto(hostRoot, fs.DefaultScanOptions(), &bufH) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("HOST_SCAN_ERR")
			return
		}
		io.Console.Println(formatScan("HOST_FULL", hostRoot, bufH))

		for d in []int{0, 1, 2} {
			fs.ScanInto(hostRoot, fs.ScanOptions{OneFileSystem: false, MaxDepth: d}, &bufH) catch {
			case fs.NotFound, fs.Denied, fs.Failed:
				io.Console.Println("HOST_DEPTH_ERR")
				return
			}
			io.Console.Println(formatScan("HOST_D\(d)", hostRoot, bufH))
		}

		fs.ScanWith(hostRoot, fs.DefaultScanOptions(), &bufH, runFilter) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("HOST_FILTER_ERR")
			return
		}
		io.Console.Println(formatScan("HOST_FILTER", hostRoot, bufH))

		// Verify buffer reuse clears earlier nodes/errors and preserves accuracy.
		fs.Reset(&bufH)
		fs.ScanInto(hostRoot + "/b", fs.DefaultScanOptions(), &bufH) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("HOST_REUSE_ERR")
			return
		}
		io.Console.Println(formatScan("HOST_REUSE", hostRoot + "/b", bufH))

		// Verify ReadDirEntries agrees with ReadDir + ReadEntry on hostRoot.
		let kids = h.ReadDirEntries(hostRoot) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("HOST_READDIR_ERR")
			return
		}
		let names = h.ReadDir(hostRoot) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("HOST_NAMES_ERR")
			return
		}
		io.Console.Println("KIDS_MATCH=\(kids.Len() == names.Len())")

		// Verify non-fatal permission-denied collection when permDir is non-empty,
		// then reuse the same ScanBuffer via ScanInto to confirm Errors resets.
		if permDir != "-" {
			let mut pbuf = fs.NewScanBuffer()
			fs.ScanInto(permDir, fs.DefaultScanOptions(), &pbuf) catch {
			case fs.NotFound, fs.Denied, fs.Failed:
				io.Console.Println("PERM_ERR")
				return
			}
			io.Console.Println(formatScan("PERM", permDir, pbuf))
			fs.ScanInto(hostRoot + "/b", fs.DefaultScanOptions(), &pbuf) catch {
			case fs.NotFound, fs.Denied, fs.Failed:
				io.Console.Println("PERM_REUSE_ERR")
				return
			}
			io.Console.Println("REUSE_AFTER_PERM=\(pbuf.Errors.Len())|NODES=\(pbuf.Len())")
		}

		// Verify OneFileSystem on host "/" at MaxDepth 1: every returned entry stays on root Dev.
		let rootScan = fs.Scan("/", fs.ScanOptions{OneFileSystem: true, MaxDepth: 1}) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("ROOT_1FS_ERR")
			return
		}
		let rDev = rootScan.Devs[0]
		let mut sameDev = true
		for i in 0..rootScan.Len() {
			if rootScan.Devs[i] != rDev {
				sameDev = false
			}
		}
		io.Console.Println("HOST_1FS_SAME_DEV=\(sameDev && rootScan.Len() >= 1)")
	}

	with context { fs.Files: m, fs.Writes: m, fs.Entries: m, fs.Scans: m } {
		fs.ScanInto("/tree", fs.DefaultScanOptions(), &bufM) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("MEM_SCAN_ERR")
			return
		}
		io.Console.Println(formatScan("MEM_FULL", "/tree", bufM))

		for d in []int{0, 1, 2} {
			fs.ScanInto("/tree", fs.ScanOptions{OneFileSystem: false, MaxDepth: d}, &bufM) catch {
			case fs.NotFound, fs.Denied, fs.Failed:
				io.Console.Println("MEM_DEPTH_ERR")
				return
			}
			io.Console.Println(formatScan("MEM_D\(d)", "/tree", bufM))
		}

		fs.ScanWith("/tree", fs.DefaultScanOptions(), &bufM, runFilter) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("MEM_FILTER_ERR")
			return
		}
		io.Console.Println(formatScan("MEM_FILTER", "/tree", bufM))

		fs.Reset(&bufM)
		fs.ScanInto("/tree/b", fs.DefaultScanOptions(), &bufM) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("MEM_REUSE_ERR")
			return
		}
		io.Console.Println(formatScan("MEM_REUSE", "/tree/b", bufM))
	}
}
`

func TestFSScanHostAgreesWithMemory(t *testing.T) {
	base := t.TempDir()
	hostTree := filepath.Join(base, "tree")
	if err := os.MkdirAll(filepath.Join(hostTree, "a", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostTree, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostTree, "prune"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostTree, "skip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "small.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 513)
	for i := range big {
		big[i] = 'z'
	}
	if err := os.WriteFile(filepath.Join(hostTree, "a", "mid.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "a", "deep", "leaf.txt"), []byte("leaf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "b", "beta.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "prune", "hidden.txt"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostTree, "skip", "gone.txt"), []byte("gone"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Host-only features: cyclic/dangling symlinks + multi-link hardlinks + permission-denied dir.
	linkTree := filepath.Join(base, "links")
	if err := os.MkdirAll(filepath.Join(linkTree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte('A' + (i % 26))
	}
	if err := os.WriteFile(filepath.Join(linkTree, "a-orig.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(linkTree, "a-orig.bin"), filepath.Join(linkTree, "b-hard.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(linkTree, "a-orig.bin"), filepath.Join(linkTree, "sub", "c-hard.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a-orig.bin", filepath.Join(linkTree, "sym-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(linkTree, "sub", "sym-cycle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", filepath.Join(linkTree, "sym-dangling")); err != nil {
		t.Fatal(err)
	}

	permArg := "-"
	if os.Geteuid() != 0 {
		permRoot := filepath.Join(base, "perm")
		if err := os.MkdirAll(filepath.Join(permRoot, "aaa-secret"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(permRoot, "zzz-ok"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(permRoot, "aaa-secret", "hidden.txt"), []byte("no"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(permRoot, "zzz-ok", "visible.txt"), []byte("yes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(permRoot, "aaa-secret"), 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(filepath.Join(permRoot, "aaa-secret"), 0o755)
		permArg = permRoot
	}

	out := runFSScanErgoFile(t, fsScanParityProg, hostTree, permArg)
	if !strings.Contains(out, "KIDS_MATCH=true") {
		t.Fatalf("KIDS_MATCH=true missing:\n%s", out)
	}
	if !strings.Contains(out, "HOST_1FS_SAME_DEV=true") {
		t.Fatalf("HOST_1FS_SAME_DEV=true missing:\n%s", out)
	}

	type scanNode struct {
		idx    int
		rel    string
		name   string
		parent string
		depth  int
		kind   string
		size   int
		alloc  int
		nlink  int
		dup    bool
	}
	type scanSummary struct {
		count int
		tsize int
		alloc int
		uniq  int
		hlink int
		errs  int
		nodes []scanNode
		errRs []string
	}
	parsed := map[string]*scanSummary{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		lbl := parts[0]
		s := parsed[lbl]
		if s == nil {
			s = &scanSummary{}
			parsed[lbl] = s
		}
		switch parts[1] {
		case "N":
			idx, _ := strconv.Atoi(parts[2])
			depth, _ := strconv.Atoi(parts[6])
			size, _ := strconv.Atoi(parts[8])
			alloc, _ := strconv.Atoi(parts[9])
			nlink, _ := strconv.Atoi(parts[10])
			s.nodes = append(s.nodes, scanNode{
				idx: idx, rel: parts[3], name: parts[4], parent: parts[5],
				depth: depth, kind: parts[7], size: size, alloc: alloc,
				nlink: nlink, dup: parts[13] == "1",
			})
		case "E":
			s.errRs = append(s.errRs, parts[2]+"|"+parts[4])
		default:
			if strings.HasPrefix(parts[1], "COUNT=") {
				s.count, _ = strconv.Atoi(strings.TrimPrefix(parts[1], "COUNT="))
				s.tsize, _ = strconv.Atoi(strings.TrimPrefix(parts[2], "TSIZE="))
				s.alloc, _ = strconv.Atoi(strings.TrimPrefix(parts[3], "TALLOC="))
				s.uniq, _ = strconv.Atoi(strings.TrimPrefix(parts[4], "UNIQ="))
				s.hlink, _ = strconv.Atoi(strings.TrimPrefix(parts[5], "HLINK="))
				s.errs, _ = strconv.Atoi(strings.TrimPrefix(parts[6], "ERRS="))
			}
		}
	}

	// Host vs Memory structural, order, size, depth, parent, and filter parity,
	// plus explicit node counts per mode so shared scanCore defects cannot cancel out.
	wantCount := map[string]int{
		"HOST_FULL":   13,
		"HOST_D0":     1,
		"HOST_D1":     7,
		"HOST_D2":     12,
		"HOST_FILTER": 10,
		"HOST_REUSE":  2,
	}
	for _, pair := range [][2]string{
		{"HOST_FULL", "MEM_FULL"},
		{"HOST_D0", "MEM_D0"},
		{"HOST_D1", "MEM_D1"},
		{"HOST_D2", "MEM_D2"},
		{"HOST_FILTER", "MEM_FILTER"},
		{"HOST_REUSE", "MEM_REUSE"},
	} {
		hs, ms := parsed[pair[0]], parsed[pair[1]]
		if hs == nil || ms == nil {
			t.Fatalf("missing output for pair %v:\n%s", pair, out)
		}
		if want := wantCount[pair[0]]; hs.count != want {
			t.Fatalf("%s count=%d, want %d:\n%s", pair[0], hs.count, want, out)
		}
		if hs.count != ms.count || hs.tsize != ms.tsize || hs.uniq != ms.uniq || hs.hlink != ms.hlink || hs.errs != ms.errs {
			t.Fatalf("%s vs %s summary mismatch:\n host=%+v\n mem =%+v", pair[0], pair[1], hs, ms)
		}
		if len(hs.nodes) != len(ms.nodes) {
			t.Fatalf("%s vs %s node count mismatch: %d vs %d", pair[0], pair[1], len(hs.nodes), len(ms.nodes))
		}
		for i := range hs.nodes {
			hn, mn := hs.nodes[i], ms.nodes[i]
			if hn.rel != mn.rel || hn.parent != mn.parent || hn.depth != mn.depth || hn.kind != mn.kind || hn.dup != mn.dup {
				t.Fatalf("%s vs %s node %d mismatch:\n host=%+v\n mem =%+v", pair[0], pair[1], i, hn, mn)
			}
			if hn.kind != "D" && hn.size != mn.size {
				t.Fatalf("%s vs %s file size %d vs %d at %s", pair[0], pair[1], hn.size, mn.size, hn.rel)
			}
			if hn.alloc < 0 || hn.alloc%512 != 0 {
				t.Fatalf("host node %s invalid alloc %d", hn.rel, hn.alloc)
			}
		}
	}

	if permArg != "-" {
		ps := parsed["PERM"]
		if ps == nil || ps.errs != 1 || !slices.Equal(ps.errRs, []string{"aaa-secret|DENIED"}) {
			t.Fatalf("unexpected PERM scan result: %+v", ps)
		}
		var sawVisible bool
		for _, n := range ps.nodes {
			if n.rel == "zzz-ok/visible.txt" {
				sawVisible = true
			}
		}
		if !sawVisible {
			t.Fatalf("PERM scan did not continue to sibling zzz-ok/visible.txt: %+v", ps.nodes)
		}
		if !strings.Contains(out, "REUSE_AFTER_PERM=0|NODES=2") {
			t.Fatalf("ScanBuffer reuse after permission-denied scan retained stale errors or nodes:\n%s", out)
		}
	}

	// Second native Ergo run verifying symlinks + hardlink dedup on host linkTree.
	linkProg := `import fs
import io
import os
import strings

func main() needs io.Console | os.Ambient {
	let root = os.Args(os.NewHost())[0]
	let h = fs.NewHost()
	with context { fs.Files: h, fs.Entries: h, fs.Scans: h } {
		let b = fs.Scan(root, fs.DefaultScanOptions()) catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			io.Console.Println("LINK_ERR")
			return
		}
		let mut rows = []string{}
		for i in 0..b.Len() {
			let rel = if b.Paths[i] == root { "." } else { strings.Slice(b.Paths[i], root.Len()+1, b.Paths[i].Len()) }
			let dup = if b.Duplicates[i] { "1" } else { "0" }
			let k = match b.Kinds[i] {
			case fs.FileEntry: "F"
			case fs.DirEntry: "D"
			case fs.SymlinkEntry: "L"
			case fs.OtherEntry: "O"
			}
			rows = rows.Append("\(rel):\(k):\(b.Sizes[i]):\(b.AllocBytes[i]):\(b.Nlinks[i]):\(dup)")
		}
		io.Console.Println("SUMMARY|\(b.Len())|\(b.TotalSize)|\(b.TotalAlloc)|\(b.UniqueFiles)|\(b.Hardlinks)|\(b.Errors.Len())")
		io.Console.Println(strings.Join(rows, "|"))
	}
}
`
	linkOut := runFSScanErgoFile(t, linkProg, linkTree)
	lines := strings.Split(strings.TrimSpace(linkOut), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "SUMMARY|") {
		t.Fatalf("unexpected linkOut:\n%s", linkOut)
	}
	sumParts := strings.Split(lines[0], "|")
	gotLen, _ := strconv.Atoi(sumParts[1])
	gotSize, _ := strconv.Atoi(sumParts[2])
	gotAlloc, _ := strconv.Atoi(sumParts[3])
	gotUniq, _ := strconv.Atoi(sumParts[4])
	gotHlink, _ := strconv.Atoi(sumParts[5])
	gotErrs, _ := strconv.Atoi(sumParts[6])

	// Entries: ".", "a-orig.bin", "b-hard.bin", "sub", "sub/c-hard.bin", "sub/sym-cycle", "sym-dangling", "sym-file" => 8 entries.
	// Note symlinks add their small symlink target lengths to TotalSize, while 8192B hardlinked file must be counted ONCE!
	if gotLen != 8 || gotErrs != 0 {
		t.Fatalf("linkTree len=%d errs=%d, want 8 and 0\n%s", gotLen, gotErrs, linkOut)
	}
	if gotHlink != 2 || gotUniq != 4 {
		t.Fatalf("linkTree unique=%d hardlinks=%d, want 4 and 2 (1 orig file + 3 symlinks, 2 hardlink dups)\n%s", gotUniq, gotHlink, linkOut)
	}
	// Hardlink dedup: TotalSize must equal 8192 + len("a-orig.bin") + len("..") + len("missing-target"), NEVER 3*8192!
	wantSymSize := len("a-orig.bin") + len("..") + len("missing-target")
	if gotSize != 8192+wantSymSize {
		t.Fatalf("linkTree TotalSize=%d, want %d (8192 once + symlinks)\n%s", gotSize, 8192+wantSymSize, linkOut)
	}
	// Sum non-duplicate AllocBytes directly from rows to verify hardlink dedup matches exact OS block counts.
	var wantAlloc int
	var rawAllocSum int
	for _, entry := range strings.Split(lines[1], "|") {
		cols := strings.Split(entry, ":")
		if len(cols) != 6 {
			t.Fatalf("unexpected row format %q", entry)
		}
		a, _ := strconv.Atoi(cols[3])
		rawAllocSum += a
		if cols[5] == "0" {
			wantAlloc += a
		}
	}
	if gotAlloc != wantAlloc || rawAllocSum <= wantAlloc {
		t.Fatalf("linkTree TotalAlloc=%d wantAlloc=%d rawAllocSum=%d\n%s", gotAlloc, wantAlloc, rawAllocSum, linkOut)
	}
	if runtime.GOOS == "linux" && gotAlloc >= rawAllocSum {
		t.Fatalf("linkTree TotalAlloc=%d was not deduplicated below raw=%d\n%s", gotAlloc, rawAllocSum, linkOut)
	}
	// Check exact order and duplicate flags: a-orig.bin has dup=0, b-hard.bin dup=1, sub/c-hard.bin dup=1.
	rowLine := lines[1]
	if !strings.Contains(rowLine, "a-orig.bin:F:8192:") || !strings.Contains(rowLine, ":3:0|") ||
		!strings.Contains(rowLine, "b-hard.bin:F:8192:") || !strings.Contains(rowLine, ":3:1|") ||
		!strings.Contains(rowLine, "sub/c-hard.bin:F:8192:") ||
		!strings.Contains(rowLine, "sub/sym-cycle:L:") ||
		!strings.Contains(rowLine, "sym-dangling:L:") ||
		!strings.Contains(rowLine, "sym-file:L:") {
		t.Fatalf("unexpected row metadata/dedup/order:\n%s", rowLine)
	}
}
