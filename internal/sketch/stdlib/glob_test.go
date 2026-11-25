package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdGlobLawsRun runs the laws of std glob at check time in process and
// records every row via lawledger.Run.
func TestStdGlobLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("glob")
	if err != nil {
		t.Fatalf("import glob: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate glob: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		name := strings.TrimPrefix(r.Name, "law ")
		if idx := strings.Index(name, " for "); idx > 0 {
			name = name[:idx]
		}
		results["glob "+name] = r
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			switch {
			case !ok:
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
			case r.Status != eval.Passed || (r.Evidence != "tested" && r.Evidence != "proved"):
				t.Errorf("law %s: %s (%s, evidence=%q) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Evidence, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const globNativeTestSrc = `import filepath
import fs
import fstest
import glob
import os

let onDiskGlob = context needs fstest.TestAmbient {
	fs.Files:   fs.NewTestHost(),
	fs.Writes:  fs.NewTestHost(),
	os.Process: os.NewTestHost(),
}

test GlobSyntaxAntiReDoSAndScan {
	assert try glob.MatchString("*.ergo", "main.ergo")
	assert !(try glob.MatchString("*.ergo", "dir/main.ergo"))
	assert try glob.MatchString("**/*.ergo", "foo.ergo")
	assert try glob.MatchString("**/*.ergo", "a/b/c/foo.ergo")
	assert try glob.MatchString("a/**/b", "a/b")
	assert try glob.MatchString("a/**/b", "a/x/b")
	assert try glob.MatchString("a/**/b", "a/x/y/z/b")
	assert try glob.MatchString("file?.txt", "file1.txt")
	assert !(try glob.MatchString("file?.txt", "file12.txt"))
	assert try glob.MatchString("[a-c][0-9]", "b7")
	assert !(try glob.MatchString("[!a-c]", "b"))
	assert try glob.MatchString("[!a-c]", "z")
	assert try glob.MatchString("[^0-9]", "x")
	assert try glob.MatchString("src/{a,b/{c,d}}.ergo", "src/a.ergo")
	assert try glob.MatchString("src/{a,b/{c,d}}.ergo", "src/b/d.ergo")
	assert !(try glob.MatchString("src/{a,b/{c,d}}.ergo", "src/b/e.ergo"))
	assert try glob.MatchString("lit\\*star\\?q", "lit*star?q")
	assert !(try glob.MatchString("lit\\*star\\?q", "litXstarYq"))

	let redosPat = try glob.Compile("a*a*a*a*a*a*a*a*a*a*b")
	let mut longA = ""
	for _ in 0..400 {
		longA = longA + "aaaaa"
	}
	assert !glob.Match(redosPat, longA+"c")
	assert glob.Match(redosPat, longA+"b")

	let ddPat = try glob.Compile("**/**/**/**/**/target.ergo")
	let mut deepPath = ""
	for _ in 0..40 {
		deepPath = deepPath + "d/"
	}
	assert !glob.Match(ddPat, deepPath+"other.ergo")
	assert glob.Match(ddPat, deepPath+"target.ergo")

	let scoped = try glob.Compile("src/api/**/*.ergo")
	assert glob.StaticPrefix(scoped).Len() == 2
	assert glob.StaticPrefix(scoped)[0] == "src" && glob.StaticPrefix(scoped)[1] == "api"
	assert glob.CanDescend(scoped, "src")
	assert glob.CanDescend(scoped, "src/api")
	assert !glob.CanDescend(scoped, "vendor")
	assert !glob.CanDescend(scoped, "node_modules")

	let mem = fstest.NewMemory()
	with context { fs.Files: mem, fs.Writes: mem } {
		try fs.Writes.MkdirAll("/proj/src/api/v1")
		try fs.Writes.MkdirAll("/proj/vendor/big")
		try fs.Writes.WriteFile("/proj/src/api/root.ergo", "1")
		try fs.Writes.WriteFile("/proj/src/api/v1/sub.ergo", "2")
		try fs.Writes.WriteFile("/proj/src/api/.secret.ergo", "3")
		try fs.Writes.WriteFile("/proj/vendor/big/ignored.ergo", "4")
		let files = try glob.Scan(scoped, "/proj", glob.DefaultScanOptions())
		assert files.Len() == 2
		assert files[0] == "src/api/root.ergo" && files[1] == "src/api/v1/sub.ergo"
		let withDots = try glob.Scan(scoped, "/proj", glob.ScanOptions{IncludeDirs: false, IncludeDotfiles: true, MaxDepth: 0})
		assert withDots.Len() == 3
		assert withDots[0] == "src/api/.secret.ergo"
		let explicitDotPat = try glob.Compile("src/api/.*.ergo")
		let explicitDots = try glob.Scan(explicitDotPat, "/proj", glob.DefaultScanOptions())
		assert explicitDots.Len() == 1 && explicitDots[0] == "src/api/.secret.ergo"
	}

	with onDiskGlob {
		let cwd = try os.Process.Getwd()
		let diskRoot = filepath.Join([]string{cwd, "disk-glob-tree"})
		try fs.Writes.MkdirAll(filepath.Join([]string{diskRoot, "pkg", "sub"}))
		try fs.Writes.MkdirAll(filepath.Join([]string{diskRoot, "other"}))
		try fs.Writes.WriteFile(filepath.Join([]string{diskRoot, "pkg", "b.ergo"}), "b")
		try fs.Writes.WriteFile(filepath.Join([]string{diskRoot, "pkg", "sub", "a.ergo"}), "a")
		try fs.Writes.WriteFile(filepath.Join([]string{diskRoot, "other", "x.ergo"}), "x")
		let pat = try glob.Compile("pkg/**/*.ergo")
		let diskFound = try glob.Scan(pat, diskRoot, glob.DefaultScanOptions())
		assert diskFound.Len() == 2
		assert diskFound[0] == "pkg/b.ergo" && diskFound[1] == "pkg/sub/a.ergo"
	}
}
`

// TestStdGlobNativeAndScan compiles and executes glob syntax, anti-ReDoS
// pathological patterns, prefix pruning, dotfile rules, and Scan over both
// fstest.Memory and a real disk directory natively with ERGO_FALLBACK=off.
func TestStdGlobNativeAndScan(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo binary")
	}
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}

	modDir := filepath.Join(tmp, "globmod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module globmod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "glob_native_test.ergo"), []byte(globNativeTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("ergo test --native failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok GlobSyntaxAntiReDoSAndScan") {
		t.Fatalf("expected ok GlobSyntaxAntiReDoSAndScan in output:\n%s", out)
	}
}
