package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/internal/toolchain"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestCompilerGobindLawsRun runs the pure-Ergo tests and laws of compiler/gofacts,
// compiler/gobind, and compiler/cmd/gobinddump.
func TestCompilerGobindLawsRun(t *testing.T) {
	needsBuild(t)
	t.Setenv("ERGO_FALLBACK", "off")
	runCompilerLaws(t, "./gofacts")
	runCompilerLaws(t, "./gobind")
	runCompilerLaws(t, "./cmd/gobinddump")
}

// TestCompilerGobindAgrees verifies byte-for-byte parity between pure-Ergo
// compiler/gofacts + compiler/gobind (via cmd/gobinddump) and Go
// internal/gofacts + internal/gobind across ledgers, source hashes, real
// committed bindings, stdlib bindings, synthetic packages, and staleness checks.
func TestCompilerGobindAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	ergo := ergoCLI(t, ctx)
	dump := filepath.Join(t.TempDir(), "gobinddump")
	if out, err := runCompiler(t, ctx, ergo, "build", "-o", dump, "./cmd/gobinddump"); err != nil {
		t.Fatalf("ergo build ./cmd/gobinddump: %v\n%s", err, out)
	}
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}

	root := repoRoot()
	lawsPath := filepath.Join(root, "internal", "sketch", "std", "ergo.laws")
	lawsBytes, err := os.ReadFile(lawsPath)
	if err != nil {
		t.Fatal(err)
	}
	lawsText := string(lawsBytes)

	t.Run("gofacts", func(t *testing.T) {
		agreeGofacts(t, ctx, dump, limit, root, lawsPath)
	})

	t.Run("std_and_stale", func(t *testing.T) {
		agreeStdAndStale(t, ctx, dump, limit, root)
	})

	t.Run("real_ergo_bind", func(t *testing.T) {
		agreeRealBindings(t, ctx, dump, limit, root, lawsText)
	})

	t.Run("synthetic", func(t *testing.T) {
		agreeSynthetic(t, ctx, dump, limit, lawsText)
	})
}

func runGobindDump(t *testing.T, ctx context.Context, dump string, limit uint64, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, dump, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := proclimit.Run(cmd, limit); err != nil {
		t.Fatalf("gobinddump %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func agreeGofacts(t *testing.T, ctx context.Context, dump string, limit uint64, root, lawsPath string) {
	t.Helper()
	out := runGobindDump(t, ctx, dump, limit, "-gofacts", lawsPath)
	var want strings.Builder
	tc := toolchain.Version
	for _, r := range gofacts.Rows {
		fmt.Fprintf(&want, "row %s\t%s\t%v\t%s\n", r.Key(), r.Source, r.View, r.Digest(tc))
	}
	for _, e := range gofacts.Effects {
		fmt.Fprintf(&want, "effect %s\t%s\t%s\n", e.Target, e.Source, e.Digest(tc))
	}
	for _, h := range gofacts.Hosts {
		fmt.Fprintf(&want, "host %s\t%s\t%s\n", h.Target, h.Source, h.HostDigest(tc))
	}
	syncs := v1.GoSyncCallbacks()
	syncKeys := make([]string, 0, len(syncs))
	for k := range syncs {
		syncKeys = append(syncKeys, k)
	}
	sort.Strings(syncKeys)
	syncLines := map[string]bool{}
	for _, k := range syncKeys {
		syncLines[fmt.Sprintf("sync %s\t%s", k, gofacts.SyncDigest(k, syncs[k], tc))] = true
	}
	for _, p := range v1.PureImports {
		want.WriteString(fmt.Sprintf("pure %s\t%s\t%s\n", p, strings.Join(v1.PureImportsExcept()[p], ","), gofacts.PureDigest(p, v1.PureImportsExcept()[p], tc)))
	}
	gotLines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	wantSet := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSuffix(want.String(), "\n"), "\n") {
		if l != "" {
			wantSet[l] = true
		}
	}
	for k := range syncLines {
		wantSet[k] = true
	}
	for tgt, w := range v1.GoWrites() {
		wantSet[fmt.Sprintf("write %s\t%d", tgt, w.Index)] = true
	}
	for tgt := range v1.GoReadOnlyVars() {
		wantSet["rovar "+tgt] = true
	}
	for tgt := range v1.GoConcurrentTypes() {
		wantSet["concurrent "+tgt] = true
	}
	gotSet := map[string]bool{}
	var summaryLine, issuesLine string
	for _, l := range gotLines {
		switch {
		case strings.HasPrefix(l, "ledger_issues "):
			issuesLine = l
		case strings.HasPrefix(l, "ledger_summary "):
			summaryLine = l
		case strings.HasPrefix(l, "guard_entries "):
		case strings.HasPrefix(l, "issue "):
			t.Errorf("unexpected ledger issue from shipped ergo.laws: %s", l)
		case l != "":
			gotSet[l] = true
		}
	}
	if diff := setDiffMap(wantSet, gotSet); diff != "" {
		t.Fatalf("gofacts table mismatch:\n%s", diff)
	}
	if issuesLine != "ledger_issues 0" {
		t.Fatalf("shipped ergo.laws: got %q, want ledger_issues 0", issuesLine)
	}
	expectedTotal := len(gofacts.Rows) + len(gofacts.Effects) + len(gofacts.Hosts) + len(v1.GoSyncCallbacks()) + len(v1.PureImports)
	wantSummary := fmt.Sprintf("ledger_summary %d %d 0 0 0", expectedTotal, expectedTotal)
	if summaryLine != wantSummary {
		t.Fatalf("ledger summary: got %q, want %q", summaryLine, wantSummary)
	}

	hostfsDir := filepath.Join(root, "runtime", "v1", "hostfs")
	hostfsTargets := []string{
		"ergo.local/prototype/runtime/v1/hostfs.Stat",
		"ergo.local/prototype/runtime/v1/hostfs.Lstat",
		"ergo.local/prototype/runtime/v1/hostfs.ReadDir",
		"ergo.local/prototype/runtime/v1/hostfs.FileStat",
		"ergo.local/prototype/runtime/v1/hostfs.FileReadDir",
		"ergo.local/prototype/runtime/v1/hostfs.FileReaddir",
		"ergo.local/prototype/runtime/v1/hostfs.Statfs",
	}
	shArgs := append([]string{"-sourcehash", hostfsDir}, hostfsTargets...)
	shOut := runGobindDump(t, ctx, dump, limit, shArgs...)
	shLines := strings.Split(strings.TrimSuffix(shOut, "\n"), "\n")
	if len(shLines) != len(hostfsTargets) {
		t.Fatalf("sourcehash line count = %d, want %d:\n%s", len(shLines), len(hostfsTargets), shOut)
	}
	for i, tgt := range hostfsTargets {
		wantHash, err := gofacts.SourceHash("", tgt)
		if err != nil {
			t.Fatalf("SourceHash(%s): %v", tgt, err)
		}
		wantLine := tgt + " " + wantHash
		if shLines[i] != wantLine {
			t.Errorf("sourcehash[%s]: got %q, want %q", tgt, shLines[i], wantLine)
		}
	}
}

func setDiffMap(want, got map[string]bool) string {
	var ws, gs []string
	for k := range want {
		ws = append(ws, k)
	}
	for k := range got {
		gs = append(gs, k)
	}
	return setDiff(ws, gs)
}

func agreeStdAndStale(t *testing.T, ctx context.Context, dump string, limit uint64, root string) {
	t.Helper()
	stdOut, err := exec.Command("go", "list", "std").Output()
	if err != nil {
		t.Fatalf("go list std: %v", err)
	}
	rawStd := strings.Fields(string(stdOut))
	wantStd, err := gobind.StdPaths(os.Environ())
	if err != nil {
		t.Fatalf("gobind.StdPaths: %v", err)
	}
	verOut, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	wantVer, err := gobind.GoVersion(os.Environ())
	if err != nil {
		t.Fatalf("gobind.GoVersion: %v", err)
	}

	tmpDir := t.TempDir()
	var manifestLines []string
	var expectedOutputs []string

	stdReq := gbDumpRequest{
		Mode:          "std",
		PkgPath:       "net/url",
		StdRaw:        rawStd,
		GoVersionRaw:  string(verOut),
		RootCheckPath: "example.com/internal/foo",
		RootCheckName: "main",
	}
	stdJSONPath := filepath.Join(tmpDir, "std.json")
	gbWriteJSON(t, stdJSONPath, stdReq)
	manifestLines = append(manifestLines, stdJSONPath)
	expectedOutputs = append(expectedOutputs, fmt.Sprintf("std %s\ngoversion %s\nfilename %s\nrootrefusal a main package is a program, which Go does not import\n",
		strings.Join(wantStd, " "), wantVer, filepath.ToSlash(gobind.FileName("net/url"))))

	admitBindDir := filepath.Join(root, "examples", "admit", "ergo.bind")
	admitFiles := gbReadBindDir(t, admitBindDir)
	dirReq := gbDumpRequest{
		Mode:    "stale",
		DirRoot: admitBindDir,
		Written: admitFiles,
	}
	dirJSONPath := filepath.Join(tmpDir, "stale_dir.json")
	gbWriteJSON(t, dirJSONPath, dirReq)
	manifestLines = append(manifestLines, dirJSONPath)
	expectedOutputs = append(expectedOutputs, "marker true true\n")

	for seed := uint64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		comm := map[string]string{}
		for i := range 1 + rng.IntN(5) {
			comm[fmt.Sprintf("pkg/%d", i)] = fmt.Sprintf("// ergo:bind pkg/%d\nline1\nline2\n", i)
		}
		writ := map[string]string{}
		for k, v := range comm {
			writ[k] = v
		}
		for k := range comm {
			switch rng.IntN(4) {
			case 0:
				writ[k] = strings.Replace(writ[k], "line2", "line3", 1)
			case 1:
				delete(writ, k)
			}
		}
		if rng.IntN(2) == 0 {
			writ["extra/p"] = "// ergo:bind extra/p\n"
		}
		wantDiffs := gobind.Stale(comm, writ)
		var commList, writList []gbSpecFile
		for k, v := range comm {
			commList = append(commList, gbSpecFile{Path: k, Text: v})
		}
		for k, v := range writ {
			writList = append(writList, gbSpecFile{Path: k, Text: v})
		}
		req := gbDumpRequest{Mode: "stale", Committed: commList, Written: writList}
		p := filepath.Join(tmpDir, fmt.Sprintf("stale_%d.json", seed))
		gbWriteJSON(t, p, req)
		manifestLines = append(manifestLines, p)
		wantOut := ""
		if len(wantDiffs) > 0 {
			wantOut = strings.Join(wantDiffs, "\n") + "\n"
		}
		expectedOutputs = append(expectedOutputs, wantOut)
	}

	manifestPath := filepath.Join(tmpDir, "manifest.txt")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(manifestLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	batchOut := runGobindDump(t, ctx, dump, limit, "-batch", manifestPath)
	frames := parseBatchFrames(t, batchOut)
	for i, path := range manifestLines {
		if got := frames[path]; got != expectedOutputs[i] {
			t.Fatalf("case %s (%d):\ngot:\n%s\nwant:\n%s", path, i, got, expectedOutputs[i])
		}
	}
}

func gbReadBindDir(t *testing.T, dir string) []gbSpecFile {
	t.Helper()
	var files []gbSpecFile
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "ergo.gen" || !strings.HasSuffix(d.Name(), ".ergo") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		base := strings.TrimSuffix(d.Name(), ".ergo")
		parent := filepath.ToSlash(filepath.Dir(rel))
		impPath := strings.TrimSuffix(rel, ".ergo")
		if parent != "." && (parent == base || strings.HasSuffix(parent, "/"+base)) {
			impPath = parent
		}
		files = append(files, gbSpecFile{Path: impPath, Text: string(b)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func agreeRealBindings(t *testing.T, ctx context.Context, dump string, limit uint64, root, lawsText string) {
	t.Helper()
	goVer, err := gobind.GoVersion(os.Environ())
	if err != nil {
		t.Fatalf("gobind.GoVersion: %v", err)
	}
	type targetBatch struct {
		dir      string
		patterns []string
	}
	batches := []targetBatch{
		{
			dir: root,
			patterns: []string{
				"bytes",
				"strings",
				"io",
				"os",
				"time",
				"slices",
				"sort",
				"math/rand/v2",
				"sync/atomic",
				"path/filepath",
				"regexp",
				"strconv",
				"bufio",
				"crypto/sha256",
				"encoding/hex",
				"encoding/base64",
				"net/url",
				gobind.HostFS,
			},
		},
		{
			dir:      filepath.Join(root, "examples", "admit"),
			patterns: []string{"golang.org/x/sync/semaphore"},
		},
		{
			dir:      filepath.Join(root, "hosts", "mygo"),
			patterns: boundImportPaths(t, filepath.Join(root, "hosts", "mygo", "ergo.bind")),
		},
		{
			dir:      filepath.Join(root, "hosts", "opentui"),
			patterns: boundImportPaths(t, filepath.Join(root, "hosts", "opentui", "ergo.bind")),
		},
	}

	tmpDir := t.TempDir()
	for bi, batch := range batches {
		if len(batch.patterns) == 0 {
			continue
		}
		wantWritten, err := gobind.Bind(batch.dir, os.Environ(), goVer, batch.patterns...)
		if err != nil {
			t.Fatalf("gobind.Bind(%s): %v", batch.dir, err)
		}
		seenReq := map[string]bool{}
		var reqPaths []string
		for _, p := range batch.patterns {
			if _, ok := wantWritten[p]; ok && !seenReq[p] {
				seenReq[p] = true
				reqPaths = append(reqPaths, p)
			}
		}
		var extraPaths []string
		for p := range wantWritten {
			if !seenReq[p] {
				extraPaths = append(extraPaths, p)
			}
		}
		sort.Strings(extraPaths)
		reqPaths = append(reqPaths, extraPaths...)

		plats := gbCollectPlatforms(t, batch.dir, os.Environ(), gobind.Platforms, reqPaths, wantWritten)
		var tgts []gbSpecBindTarget
		for _, pkgPath := range reqPaths {
			tgts = append(tgts, gbSpecBindTarget{
				Path:        pkgPath,
				Name:        gbFindPkgName(plats, pkgPath),
				ModuleStamp: plats.modules[pkgPath],
			})
		}
		req := gbDumpRequest{
			Mode:        "bindmany",
			GoVersion:   goVer,
			Toolchain:   toolchain.Version,
			LedgerText:  lawsText,
			Platforms:   plats.specs,
			BindTargets: tgts,
		}
		specPath := filepath.Join(tmpDir, fmt.Sprintf("real_%d.json", bi))
		gbWriteJSON(t, specPath, req)
		batchOut := runGobindDump(t, ctx, dump, limit, specPath)
		frames := parseBatchFrames(t, batchOut)
		mismatches := 0
		for _, pkgPath := range reqPaths {
			want := wantWritten[pkgPath]
			got, ok := frames[pkgPath]
			if !ok {
				t.Fatalf("missing bindmany frame for %s", pkgPath)
			}
			if got != want {
				mismatches++
				t.Errorf("binding mismatch for %s:\n%s", pkgPath, gobindFirstLineDiff(want, got))
			}
		}
		if mismatches > 0 {
			t.Fatalf("%d real binding mismatches in batch %d", mismatches, bi)
		}
	}
}

func boundImportPaths(t *testing.T, bindDir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(bindDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".ergo") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "// ergo:bind "); ok {
				fields := strings.Fields(rest)
				if len(fields) > 0 {
					paths = append(paths, fields[0])
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return slices.Compact(paths)
}

func agreeSynthetic(t *testing.T, ctx context.Context, dump string, limit uint64, lawsText string) {
	t.Helper()
	modDir := t.TempDir()
	mustWriteFile(t, filepath.Join(modDir, "go.mod"), "module example.com/synth\n\ngo 1.27\n")

	mustWriteFile(t, filepath.Join(modDir, "types", "types.go"), `package types

import (
	"cmp"
	"context"
	"io"
	"iter"
	"time"
	"unsafe"
)

type AliasInt = int
type Ch chan int
type BadPtr *int
type FuncVal func(int) int
type EmptyIface interface{}
type ConstraintIface interface { int }
type NoExpIface interface { hidden() }

type GenericBox[T any] struct { V T }

type Inner struct {
	X int
	Bad chan int
}

type Res struct {
	Name string
}
func (r *Res) Close() error { return nil }
func (r *Res) Self() *Res { return r }
func (r *Res) Borrow() int { return len(r.Name) }

type Handler interface {
	Handle(r *Res, p int, typeName string, mapParam int, Map int, error string, wire bool) (int, error)
	Close() error
	Variadic(xs ...int)
}

type ByteSlice []byte
type ArrBad [2][]int
type ArrGood [2]int

const (
	SmallConst = 42
	BigConst   = 1 << 63
	FloatZero  = 1e-400
	StrConst   = "hello"
	RuneConst  = 'a'
	BoolConst  = true
	CmplxConst = 1 + 2i
)

var (
	MutableVar = 10
	ErrVar = io.EOF
)

func Sum[T cmp.Ordered](a, b T) T {
	if cmp.Less(a, b) { return b }
	return a
}
func SliceLen[S ~[]E, E any](s S) int { return len(s) }
func MapLen[M ~map[K]V, K comparable, V any](m M) int { return len(m) }
func BadOrder[A any, B comparable](a A, b B) int { return 0 }
func BadRowParam[CE any](x CE, fn func() error) {}
func CtxFunc(ctx context.Context, x int) int { return x }
func WalkSeq(s iter.Seq[int], fn func(int) error) error { return nil }
func UnsafeRef(u uintptr, p unsafe.Pointer) {}
func TakeDur(d time.Duration) time.Duration { return d }
func MapAnyKey(m map[any]int) {}
func CbMulti(cb func() (int, int)) {}
func CbVariadic(cb func(...int)) {}
func CbGeneric(cb func() int, fn func(int)) {}
`)

	mustWriteFile(t, filepath.Join(modDir, "hide", "hide.go"), `package hide

type Map struct{ X int }
func Make() *Map { return &Map{X: 1} }
func UseStdMap(m map[string]int) int { return len(m) }
`)

	mustWriteFile(t, filepath.Join(modDir, "merge", "common.go"), `package merge

type Common struct {
	Keep int
}
`)
	mustWriteFile(t, filepath.Join(modDir, "merge", "plat_darwin.go"), `//go:build darwin

package merge

type Partial struct {
	Shared int
	DarwinOnly int
}
func OnDarwin() int { return 1 }
func DiffSig() int { return 1 }
`)
	mustWriteFile(t, filepath.Join(modDir, "merge", "plat_linux.go"), `//go:build linux

package merge

type Partial struct {
	Shared int
	LinuxOnly int
}
func DiffSig(x int) int { return x }
`)

	mustWriteFile(t, filepath.Join(modDir, "flow", "flow.go"), `package flow

import (
	"errors"
	"unsafe"
)

type Box struct {
	Err error
	Next *Box
}

func LeafTotal(a int, b uint) int {
	x := a + 1
	x += 2
	switch x {
	case 1:
		return int(b << (b & 3)) / 2
	default:
		for i := range 3 {
			x += i
		}
	}
	return min(x, 10)
}

func (b Box) ValMethod(n int) int {
	return LeafTotal(n, 1)
}

func CallsValMethod(b Box) int {
	return b.ValMethod(3)
}

func NotTotalDiv(a, b int) int { return a / b }

func NonNilNew() (*Box, error) {
	return new(Box), nil
}

func NonNilPair(flag bool) (out *Box, err error) {
	b, err := NonNilNew()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = errors.New("override")
		}
	}()
	if flag {
		b.Next = &Box{}
		return b.Next, nil
	}
	return b, nil
}

func NonNilSliceLoop(xs []int) (*Box, error) {
	if len(xs) == 0 {
		return nil, errors.New("empty")
	}
	var res *Box
	for range xs {
		res = &Box{}
	}
	return res, nil
}

func NonNilUnsafeCast(u *int) (*Box, error) {
	if u == nil {
		return nil, errors.New("nil")
	}
	return (*Box)(unsafe.Pointer(u)), nil
}

func MayReturnNil(flag bool) (*Box, error) {
	var b *Box
	fn := func() { b = nil }
	_ = fn
	return b, nil
}
`)

	goVer, err := gobind.GoVersion(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	patterns := []string{
		"example.com/synth/types",
		"example.com/synth/hide",
		"example.com/synth/merge",
		"example.com/synth/flow",
	}
	wantWritten, err := gobind.Bind(modDir, os.Environ(), goVer, patterns...)
	if err != nil {
		t.Fatalf("gobind.Bind synthetic: %v", err)
	}
	var reqPaths []string
	for p := range wantWritten {
		reqPaths = append(reqPaths, p)
	}
	sort.Strings(reqPaths)

	plats := gbCollectPlatforms(t, modDir, os.Environ(), gobind.Platforms, reqPaths, wantWritten)
	specDir := t.TempDir()
	var tgts []gbSpecBindTarget
	for _, pkgPath := range reqPaths {
		tgts = append(tgts, gbSpecBindTarget{
			Path:        pkgPath,
			Name:        gbFindPkgName(plats, pkgPath),
			ModuleStamp: plats.modules[pkgPath],
		})
	}
	req := gbDumpRequest{
		Mode:        "bindmany",
		GoVersion:   goVer,
		Toolchain:   toolchain.Version,
		LedgerText:  lawsText,
		Platforms:   plats.specs,
		BindTargets: tgts,
	}
	specPath := filepath.Join(specDir, "synth.json")
	gbWriteJSON(t, specPath, req)
	out := runGobindDump(t, ctx, dump, limit, specPath)
	frames := parseBatchFrames(t, out)
	for _, pkgPath := range reqPaths {
		got := frames[pkgPath]
		want := wantWritten[pkgPath]
		if got != want {
			t.Fatalf("synthetic mismatch for %s:\n%s\n--- GOT ---\n%s\n--- WANT ---\n%s", pkgPath, gobindFirstLineDiff(want, got), got, want)
		}
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gobindFirstLineDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := max(len(wl), len(gl))
	for i := 0; i < n; i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n  want: %q\n  got:  %q", i+1, w, g)
		}
	}
	return "identical"
}

func parseBatchFrames(t *testing.T, out string) map[string]string {
	t.Helper()
	res := map[string]string{}
	i := 0
	for i < len(out) {
		nl := strings.IndexByte(out[i:], '\n')
		if nl < 0 {
			t.Fatalf("malformed batch frame header at %d", i)
		}
		caseLine := out[i : i+nl]
		i += nl + 1
		if !strings.HasPrefix(caseLine, "@case ") {
			t.Fatalf("expected @case header, got %q", caseLine)
		}
		name := strings.TrimPrefix(caseLine, "@case ")
		nl2 := strings.IndexByte(out[i:], '\n')
		if nl2 < 0 {
			t.Fatalf("malformed batch frame @len at %d", i)
		}
		lenLine := out[i : i+nl2]
		i += nl2 + 1
		if !strings.HasPrefix(lenLine, "@len ") {
			t.Fatalf("expected @len header, got %q", lenLine)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(lenLine, "@len "))
		if err != nil || i+n > len(out) {
			t.Fatalf("invalid @len %q at %d (total %d)", lenLine, i, len(out))
		}
		res[name] = out[i : i+n]
		i += n
	}
	return res
}

func gbNonNilReq(req gbDumpRequest) gbDumpRequest {
	if req.Platforms == nil {
		req.Platforms = []gbSpecPlatform{}
	}
	if req.Facts == nil {
		req.Facts = []gbSpecFact{}
	}
	if req.BindTargets == nil {
		req.BindTargets = []gbSpecBindTarget{}
	}
	if req.Committed == nil {
		req.Committed = []gbSpecFile{}
	}
	if req.Written == nil {
		req.Written = []gbSpecFile{}
	}
	if req.StdRaw == nil {
		req.StdRaw = []string{}
	}
	for i := range req.Platforms {
		p := &req.Platforms[i]
		if p.Types == nil {
			p.Types = []gbSpecType{}
		}
		for ti := range p.Types {
			tp := &p.Types[ti]
			if tp.Fields == nil {
				tp.Fields = []gbSpecField{}
			}
			if tp.Params == nil {
				tp.Params = []gbSpecParam{}
			}
			if tp.Results == nil {
				tp.Results = []gbSpecParam{}
			}
			if tp.Methods == nil {
				tp.Methods = []gbSpecMethod{}
			}
			if tp.ValMethods == nil {
				tp.ValMethods = []gbSpecMethod{}
			}
			if tp.TypeParams == nil {
				tp.TypeParams = []uint32{}
			}
			if tp.TypeArgs == nil {
				tp.TypeArgs = []uint32{}
			}
			if tp.Terms == nil {
				tp.Terms = []gbSpecTerm{}
			}
		}
		if p.Packages == nil {
			p.Packages = []gbSpecPkg{}
		}
		for pi := range p.Packages {
			if p.Packages[pi].Decls == nil {
				p.Packages[pi].Decls = []gbSpecDecl{}
			}
		}
		if p.AstNodes == nil {
			p.AstNodes = []gbSpecAstNode{}
		}
		for ai := range p.AstNodes {
			an := &p.AstNodes[ai]
			if an.Children == nil {
				an.Children = []uint32{}
			}
			if an.Lhs == nil {
				an.Lhs = []uint32{}
			}
			if an.Rhs == nil {
				an.Rhs = []uint32{}
			}
			if an.Imports == nil {
				an.Imports = []gbSpecImport{}
			}
		}
		if p.AstPkgs == nil {
			p.AstPkgs = []gbSpecAstPkg{}
		}
		for api := range p.AstPkgs {
			if p.AstPkgs[api].Files == nil {
				p.AstPkgs[api].Files = []uint32{}
			}
		}
		if p.Sites == nil {
			p.Sites = []gbSpecParamSite{}
		}
	}
	return req
}

func gbWriteJSON(t *testing.T, path string, req gbDumpRequest) {
	t.Helper()
	b, err := json.Marshal(gbNonNilReq(req))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type gbCollectedPlatforms struct {
	specs   []gbSpecPlatform
	modules map[string]string
}

func gbFindPkgName(p gbCollectedPlatforms, pkgPath string) string {
	for _, sp := range p.specs {
		for _, pkg := range sp.Packages {
			if pkg.Path == pkgPath {
				return pkg.Name
			}
		}
	}
	i := strings.LastIndex(pkgPath, "/")
	if i < 0 {
		return pkgPath
	}
	return pkgPath[i+1:]
}

func gbGoEnv(env []string, p gobind.Platform) []string {
	flags := []string{"-mod=readonly"}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = []string{"-mod=readonly"}
			for _, f := range strings.Fields(v) {
				if !strings.HasPrefix(f, "-mod=") && !strings.HasPrefix(f, "--mod=") {
					flags = append(flags, f)
				}
			}
		}
	}
	return append(append([]string{}, env...), "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED=0", "GOFLAGS="+strings.Join(flags, " "))
}

func gbCollectPlatforms(t *testing.T, dir string, env []string, platforms []gobind.Platform, reqPaths []string, _ map[string]string) gbCollectedPlatforms {
	t.Helper()
	modules := map[string]string{}
	specs := make([]gbSpecPlatform, len(platforms))

	type listedPkg struct {
		ImportPath string
		Name       string
		Dir        string
		Export     string
		GoFiles    []string
		ImportMap  map[string]string
		DepOnly    bool
		Error      *struct{ Err string }
		Module     *struct {
			Path, Version string
			Main          bool
			Replace       *struct{ Path, Version string }
		}
	}
	stampOf := func(m *struct {
		Path, Version string
		Main          bool
		Replace       *struct{ Path, Version string }
	}) string {
		if m == nil {
			return ""
		}
		if m.Main {
			return m.Path
		}
		s := m.Path + " " + m.Version
		if m.Replace != nil {
			s += " => " + m.Replace.Path
			if m.Replace.Version != "" {
				s += " " + m.Replace.Version
			}
		}
		return s
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	reqSet := map[string]bool{}
	for _, p := range reqPaths {
		reqSet[p] = true
	}

	for pi, plat := range platforms {
		cmdEnv := gbGoEnv(env, plat)
		run := func(args ...string) ([]byte, []byte, error) {
			cmd := exec.Command("go", args...)
			cmd.Dir, cmd.Env = dir, cmdEnv
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			return stdout.Bytes(), stderr.Bytes(), err
		}
		listArgs := append([]string{"list", "-p", "2", "-deps", "-export", "-json=ImportPath,Name,Dir,Export,GoFiles,ImportMap,DepOnly,Error,Module"}, reqPaths...)
		listedJSON, stderr, err := run(listArgs...)
		if err != nil {
			t.Fatalf("go list (%s) in %s: %v\n%s", plat, dir, err, string(stderr))
		}
		_, escOut, err := run(append([]string{"build", "-p", "2", "-gcflags=" + gobind.EscapeFlags}, reqPaths...)...)
		if err != nil {
			t.Fatalf("go build -gcflags (%s) in %s: %v\n%s", plat, dir, err, string(escOut))
		}

		exports := map[string]string{}
		byPath := map[string]listedPkg{}
		dec := json.NewDecoder(bytes.NewReader(listedJSON))
		for {
			var lp listedPkg
			if err := dec.Decode(&lp); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if lp.Error != nil {
				t.Fatalf("go list err %s: %s", lp.ImportPath, lp.Error.Err)
			}
			exports[lp.ImportPath] = lp.Export
			byPath[lp.ImportPath] = lp
			if s := stampOf(lp.Module); s != "" {
				modules[lp.ImportPath] = s
			}
		}

		fset := token.NewFileSet()
		imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
			f := exports[path]
			if f == "" {
				return nil, fmt.Errorf("no export data for %s", path)
			}
			return os.Open(f)
		})

		allPkgs := map[string]*types.Package{}
		var addPkg func(p *types.Package)
		addPkg = func(p *types.Package) {
			if p == nil || p.Path() == "unsafe" || allPkgs[p.Path()] != nil {
				return
			}
			allPkgs[p.Path()] = p
			for _, dep := range p.Imports() {
				addPkg(dep)
			}
		}
		for _, p := range reqPaths {
			if p == "unsafe" {
				continue
			}
			pkg, err := imp.Import(p)
			if err != nil {
				t.Fatalf("import %s (%s): %v", p, plat, err)
			}
			addPkg(pkg)
		}
		if exports[gobind.HostFS] != "" {
			if pkg, err := imp.Import(gobind.HostFS); err == nil {
				addPkg(pkg)
			}
		}

		enc := newTypeEncoder()
		var pkgSpecs []gbSpecPkg
		var sortedPkgPaths []string
		for p := range allPkgs {
			sortedPkgPaths = append(sortedPkgPaths, p)
		}
		sort.Strings(sortedPkgPaths)
		for _, p := range sortedPkgPaths {
			pkg := allPkgs[p]
			pkgSpecs = append(pkgSpecs, enc.encodePackage(pkg))
		}

		astEnc := newAstEncoder(enc)
		var astPkgSpecs []gbSpecAstPkg
		var sites []gbSpecParamSite
		for _, p := range reqPaths {
			lp, ok := byPath[p]
			if !ok || len(lp.GoFiles) == 0 {
				continue
			}
			var parsed []*ast.File
			for _, name := range lp.GoFiles {
				file := filepath.Clean(filepath.Join(lp.Dir, name))
				f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
				if err != nil {
					t.Fatalf("parse %s: %v", file, err)
				}
				parsed = append(parsed, f)
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Type.TypeParams != nil {
						continue
					}
					tgt := p + "." + fd.Name.Name
					var fields []*ast.Field
					if fd.Recv != nil {
						rt := gbRecvTypeName(fd.Recv.List[0].Type)
						if rt == "" {
							continue
						}
						tgt = p + "." + rt + "." + fd.Name.Name
						fields = append(fields, fd.Recv.List...)
					}
					fields = append(fields, fd.Type.Params.List...)
					for _, fl := range fields {
						for _, id := range fl.Names {
							pos := fset.Position(id.Pos())
							sites = append(sites, gbSpecParamSite{
								Target: tgt,
								Param:  id.Name,
								File:   file,
								Line:   uint32(pos.Line),
								Col:    uint32(pos.Column),
							})
						}
					}
				}
			}
			info := &types.Info{
				Types: map[ast.Expr]types.TypeAndValue{},
				Uses:  map[*ast.Ident]types.Object{},
				Defs:  map[*ast.Ident]types.Object{},
			}
			var failed bool
			conf := types.Config{
				Importer: gbMappedImporter{imp: imp, m: lp.ImportMap},
				Sizes:    types.SizesFor("gc", plat.GOARCH),
				Error:    func(error) { failed = true },
			}
			tPkg, _ := conf.Check(p, fset, parsed, info)
			if failed {
				tPkg = nil
				info = nil
			}
			var fileIDs []uint32
			for _, f := range parsed {
				fileIDs = append(fileIDs, astEnc.encodeFile(p, f, tPkg, info))
			}
			astPkgSpecs = append(astPkgSpecs, gbSpecAstPkg{Path: p, Files: fileIDs})
		}

		specs[pi] = gbSpecPlatform{
			GOOS:      plat.GOOS,
			GOARCH:    plat.GOARCH,
			Types:     enc.types,
			Packages:  pkgSpecs,
			AstNodes:  astEnc.nodes,
			AstPkgs:   astPkgSpecs,
			EscapeOut: string(escOut),
			EscapeDir: absDir,
			Sites:     sites,
		}
	}
	return gbCollectedPlatforms{specs: specs, modules: modules}
}

func gbRecvTypeName(x ast.Expr) string {
	if s, ok := x.(*ast.StarExpr); ok {
		x = s.X
	}
	if p, ok := x.(*ast.ParenExpr); ok {
		return gbRecvTypeName(p.X)
	}
	if id, ok := x.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

type gbMappedImporter struct {
	imp types.Importer
	m   map[string]string
}

func (i gbMappedImporter) Import(path string) (*types.Package, error) {
	if to, ok := i.m[path]; ok {
		path = to
	}
	return i.imp.Import(path)
}

const (
	gbNoIndex = uint32(4294967295)

	gbTKBasic     = 1
	gbTKPointer   = 2
	gbTKSlice     = 3
	gbTKArray     = 4
	gbTKMap       = 5
	gbTKChan      = 6
	gbTKNamed     = 7
	gbTKAlias     = 8
	gbTKStruct    = 9
	gbTKInterface = 10
	gbTKSignature = 11
	gbTKTuple     = 12
	gbTKTypeParam = 13
	gbTKUnion     = 14

	gbBInvalid       = 0
	gbBByte          = 26
	gbBRune          = 27
	gbTFExported     = uint32(1)
	gbTFAlias        = uint32(2)
	gbTFIfaceMethSet = uint32(4)
	gbTFVariadic     = uint32(8)
	gbTFIsError      = uint32(32)
	gbTFComparable   = uint32(64)
	gbTFBasicUnsign  = uint32(256)
	gbTFBasicInt     = uint32(512)
	gbTFBasicNum     = uint32(1024)
	gbTFBasicStr     = uint32(2048)
	gbTFBasicUntyped = uint32(4096)
	gbTFIfaceEmpty   = uint32(8192)

	gbGKFunc  = 1
	gbGKType  = 2
	gbGKVar   = 3
	gbGKConst = 4

	gbDFExported      = uint32(1)
	gbDFAlias         = uint32(2)
	gbDFConstExactInt = uint32(4)
	gbDFConstFitsFlt  = uint32(8)

	gbMemberExported = uint8(1)
	gbMemberEmbedded = uint8(2)
)

var gbErrorType = types.Universe.Lookup("error").Type()

type gbTypeEncoder struct {
	ids   map[types.Type]uint32
	types []gbSpecType
}

func newTypeEncoder() *gbTypeEncoder {
	return &gbTypeEncoder{ids: map[types.Type]uint32{}}
}

func (e *gbTypeEncoder) encodeType(t types.Type) uint32 {
	if t == nil {
		return gbNoIndex
	}
	if id, ok := e.ids[t]; ok {
		return id
	}
	id := uint32(len(e.types))
	e.ids[t] = id
	e.types = append(e.types, gbSpecType{
		Elem:       gbNoIndex,
		Key:        gbNoIndex,
		Underlying: gbNoIndex,
		RecvType:   gbNoIndex,
		TypeText:   types.TypeString(t, nil),
		ShortText:  types.TypeString(t, func(p *types.Package) string { return p.Name() }),
	})

	if types.Identical(t, gbErrorType) {
		e.types[id].Flags |= gbTFIsError
	}
	switch x := t.(type) {
	case *types.Basic:
		e.types[id].Kind = gbTKBasic
		e.types[id].BasicKind = uint8(x.Kind())
		if x.Name() == "byte" {
			e.types[id].BasicKind = gbBByte
		} else if x.Name() == "rune" {
			e.types[id].BasicKind = gbBRune
		}
		e.types[id].BasicName = x.Name()
		e.types[id].Name = x.Name()
		if x.Info()&types.IsUnsigned != 0 {
			e.types[id].Flags |= gbTFBasicUnsign
		}
		if x.Info()&types.IsInteger != 0 {
			e.types[id].Flags |= gbTFBasicInt
		}
		if x.Info()&(types.IsInteger|types.IsFloat|types.IsComplex) != 0 {
			e.types[id].Flags |= gbTFBasicNum
		}
		if x.Info()&types.IsString != 0 {
			e.types[id].Flags |= gbTFBasicStr
		}
		if x.Info()&types.IsUntyped != 0 {
			e.types[id].Flags |= gbTFBasicUntyped
		}
	case *types.Pointer:
		e.types[id].Kind = gbTKPointer
		elem := e.encodeType(x.Elem())
		e.types[id].Elem = elem
	case *types.Slice:
		e.types[id].Kind = gbTKSlice
		elem := e.encodeType(x.Elem())
		e.types[id].Elem = elem
	case *types.Array:
		e.types[id].Kind = gbTKArray
		e.types[id].ArrLen = uint32(x.Len())
		elem := e.encodeType(x.Elem())
		e.types[id].Elem = elem
	case *types.Map:
		e.types[id].Kind = gbTKMap
		k := e.encodeType(x.Key())
		v := e.encodeType(x.Elem())
		e.types[id].Key = k
		e.types[id].Elem = v
	case *types.Chan:
		e.types[id].Kind = gbTKChan
		elem := e.encodeType(x.Elem())
		e.types[id].Elem = elem
	case *types.Alias:
		e.types[id].Kind = gbTKAlias
		obj := x.Obj()
		e.types[id].Name = obj.Name()
		if obj.Pkg() != nil {
			e.types[id].PkgPath = obj.Pkg().Path()
			e.types[id].PkgName = obj.Pkg().Name()
		}
		if obj.Exported() {
			e.types[id].Flags |= gbTFExported
		}
		e.types[id].Flags |= gbTFAlias
		elem := e.encodeType(types.Unalias(x))
		e.types[id].Elem = elem
	case *types.Named:
		e.types[id].Kind = gbTKNamed
		obj := x.Obj()
		e.types[id].Name = obj.Name()
		if obj.Pkg() != nil {
			e.types[id].PkgPath = obj.Pkg().Path()
			e.types[id].PkgName = obj.Pkg().Name()
		}
		if obj.Exported() {
			e.types[id].Flags |= gbTFExported
		}
		if obj.IsAlias() {
			e.types[id].Flags |= gbTFAlias
		}
		if obj.Pkg() == nil && obj.Name() == "comparable" {
			e.types[id].Flags |= gbTFComparable
		}
		under := e.encodeType(x.Underlying())
		e.types[id].Underlying = under
		var tps []uint32
		for i := 0; i < x.TypeParams().Len(); i++ {
			tps = append(tps, e.encodeType(x.TypeParams().At(i)))
		}
		e.types[id].TypeParams = tps
		var tas []uint32
		for i := 0; i < x.TypeArgs().Len(); i++ {
			tas = append(tas, e.encodeType(x.TypeArgs().At(i)))
		}
		e.types[id].TypeArgs = tas
		if obj.Pkg() != nil {
			e.types[id].ValMethods = e.encodeMethodSet(x, types.NewMethodSet(x), obj.Pkg())
			e.types[id].Methods = e.encodeMethodSet(x, types.NewMethodSet(types.NewPointer(x)), obj.Pkg())
		}
	case *types.Struct:
		e.types[id].Kind = gbTKStruct
		var fs []gbSpecField
		for i := 0; i < x.NumFields(); i++ {
			f := x.Field(i)
			var fl uint8
			if f.Exported() {
				fl |= gbMemberExported
			}
			if f.Embedded() {
				fl |= gbMemberEmbedded
			}
			fs = append(fs, gbSpecField{Name: f.Name(), Type: e.encodeType(f.Type()), Flags: fl})
		}
		e.types[id].Fields = fs
	case *types.Interface:
		e.types[id].Kind = gbTKInterface
		if x.IsMethodSet() {
			e.types[id].Flags |= gbTFIfaceMethSet
		}
		if x.Empty() {
			e.types[id].Flags |= gbTFIfaceEmpty
		}
		var ms []gbSpecMethod
		for i := 0; i < x.NumMethods(); i++ {
			m := x.Method(i)
			var fl uint8
			if m.Exported() {
				fl |= gbMemberExported
			}
			var mp, mn string
			if m.Pkg() != nil {
				mp = m.Pkg().Path()
				mn = m.Pkg().Name()
			}
			goText := strings.ReplaceAll(types.ObjectString(m, func(p *types.Package) string { return p.Name() }), "\n", " ")
			ms = append(ms, gbSpecMethod{
				Name:    m.Name(),
				PkgPath: mp,
				PkgName: mn,
				SigType: e.encodeType(m.Type()),
				GoText:  goText,
				Flags:   fl,
			})
		}
		e.types[id].Methods = ms
		if x.NumMethods() == 0 && x.NumEmbeddeds() == 1 {
			if u, ok := x.EmbeddedType(0).(*types.Union); ok {
				var ts []gbSpecTerm
				for i := 0; i < u.Len(); i++ {
					tm := u.Term(i)
					ts = append(ts, gbSpecTerm{Tilde: tm.Tilde(), Type: e.encodeType(tm.Type())})
				}
				e.types[id].Terms = ts
			}
		}
	case *types.Signature:
		e.types[id].Kind = gbTKSignature
		if x.Variadic() {
			e.types[id].Flags |= gbTFVariadic
		}
		if x.Recv() != nil {
			e.types[id].RecvName = x.Recv().Name()
			e.types[id].RecvType = e.encodeType(x.Recv().Type())
		}
		var ps []gbSpecParam
		for i := 0; i < x.Params().Len(); i++ {
			v := x.Params().At(i)
			ps = append(ps, gbSpecParam{Name: v.Name(), Type: e.encodeType(v.Type())})
		}
		e.types[id].Params = ps
		var rs []gbSpecParam
		for i := 0; i < x.Results().Len(); i++ {
			v := x.Results().At(i)
			rs = append(rs, gbSpecParam{Name: v.Name(), Type: e.encodeType(v.Type())})
		}
		e.types[id].Results = rs
		var tps []uint32
		for i := 0; i < x.TypeParams().Len(); i++ {
			tps = append(tps, e.encodeType(x.TypeParams().At(i)))
		}
		e.types[id].TypeParams = tps
	case *types.Tuple:
		e.types[id].Kind = gbTKTuple
		var rs []gbSpecParam
		for i := 0; i < x.Len(); i++ {
			v := x.At(i)
			rs = append(rs, gbSpecParam{Name: v.Name(), Type: e.encodeType(v.Type())})
		}
		e.types[id].Results = rs
	case *types.TypeParam:
		e.types[id].Kind = gbTKTypeParam
		e.types[id].Name = x.Obj().Name()
		elem := e.encodeType(x.Constraint())
		e.types[id].Elem = elem
	case *types.Union:
		e.types[id].Kind = gbTKUnion
		var ts []gbSpecTerm
		for i := 0; i < x.Len(); i++ {
			tm := x.Term(i)
			ts = append(ts, gbSpecTerm{Tilde: tm.Tilde(), Type: e.encodeType(tm.Type())})
		}
		e.types[id].Terms = ts
	}
	return id
}

func (e *gbTypeEncoder) encodeMethodSet(recv *types.Named, ms *types.MethodSet, pkg *types.Package) []gbSpecMethod {
	var out []gbSpecMethod
	qual := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
	for i := 0; i < ms.Len(); i++ {
		f, ok := ms.At(i).Obj().(*types.Func)
		if !ok {
			continue
		}
		var fl uint8
		if f.Exported() {
			fl |= gbMemberExported
		}
		var fp, fn string
		if f.Pkg() != nil {
			fp = f.Pkg().Path()
			fn = f.Pkg().Name()
		}
		var origPkg, origRecv string
		if sig, ok := f.Origin().Type().(*types.Signature); ok && sig.Recv() != nil && f.Pkg() != nil {
			rt := sig.Recv().Type()
			if p, ok := rt.(*types.Pointer); ok {
				rt = p.Elem()
			}
			if n, ok := rt.(*types.Named); ok {
				origPkg = f.Pkg().Path()
				origRecv = n.Obj().Name()
			}
		}
		out = append(out, gbSpecMethod{
			Name:     f.Name(),
			PkgPath:  fp,
			PkgName:  fn,
			SigType:  e.encodeType(f.Type()),
			GoText:   strings.ReplaceAll(types.ObjectString(f, qual), "\n", " "),
			OrigPkg:  origPkg,
			OrigRecv: origRecv,
			Flags:    fl,
		})
	}
	return out
}

func (e *gbTypeEncoder) encodePackage(pkg *types.Package) gbSpecPkg {
	qual := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
	kindOf := func(u types.Type) string {
		switch u := u.(type) {
		case *types.Struct:
			return "struct"
		case *types.Interface:
			return "interface"
		case *types.Signature:
			return "func"
		case *types.Basic:
			return u.Name()
		default:
			return types.TypeString(u, func(p *types.Package) string { return p.Name() })
		}
	}
	var decls []gbSpecDecl
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		var fl uint32
		if obj.Exported() {
			fl |= gbDFExported
		}
		goText := strings.ReplaceAll(types.ObjectString(obj, qual), "\n", " ")
		var dk uint8
		switch o := obj.(type) {
		case *types.Func:
			dk = gbGKFunc
		case *types.TypeName:
			dk = gbGKType
			if o.IsAlias() {
				fl |= gbDFAlias
			} else {
				goText = "type " + o.Name() + " " + kindOf(o.Type().Underlying())
				if n, ok := o.Type().(*types.Named); ok && n.TypeParams().Len() > 0 {
					goText = "type " + o.Name() + "[…] " + kindOf(o.Type().Underlying())
				}
			}
		case *types.Const:
			dk = gbGKConst
			goText = "const " + o.Name() + " " + strings.ReplaceAll(types.TypeString(o.Type(), qual), "\n", " ")
			if b, ok := o.Type().(*types.Basic); ok && b.Info()&types.IsUntyped != 0 {
				if b.Kind() == types.UntypedInt {
					if _, exact := constant.Int64Val(o.Val()); exact {
						fl |= gbDFConstExactInt
					}
				} else if b.Kind() == types.UntypedFloat {
					f, _ := constant.Float64Val(o.Val())
					if !(f == 0 && constant.Sign(o.Val()) != 0 || math.IsInf(f, 0)) {
						fl |= gbDFConstFitsFlt
					}
				}
			}
		case *types.Var:
			dk = gbGKVar
		default:
			continue
		}
		decls = append(decls, gbSpecDecl{
			Kind:       dk,
			Name:       obj.Name(),
			TypeID:     e.encodeType(obj.Type()),
			GoText:     goText,
			DeclTarget: pkg.Path() + "." + obj.Name(),
			Flags:      fl,
		})
	}
	return gbSpecPkg{Path: pkg.Path(), Name: pkg.Name(), Decls: decls}
}

const (
	gbNAIdent      = 1
	gbNABasicLit   = 2
	gbNAParen      = 3
	gbNASelector   = 4
	gbNAUnary      = 5
	gbNABinary     = 6
	gbNACall       = 7
	gbNACompLit    = 8
	gbNATypeAssert = 9
	gbNAStar       = 10
	gbNAFuncLit    = 11
	gbNAEllipsis   = 12
	gbNAArrayType  = 13
	gbNAMapType    = 14
	gbNAOtherType  = 15
	gbNAField      = 16
	gbNAOtherExpr  = 17

	gbNSBlock      = 20
	gbNSAssign     = 21
	gbNSDecl       = 22
	gbNSReturn     = 23
	gbNSIf         = 24
	gbNSFor        = 25
	gbNSRange      = 26
	gbNSSwitch     = 27
	gbNSTypeSwitch = 28
	gbNSSelect     = 29
	gbNSBranch     = 30
	gbNSExpr       = 31
	gbNSSend       = 32
	gbNSIncDec     = 33
	gbNSGo         = 34
	gbNSDefer      = 35
	gbNSLabeled    = 36
	gbNSEmpty      = 37
	gbNSCaseClause = 38
	gbNSCommClause = 39
	gbNSOtherStmt  = 40

	gbNDTypeSpec  = 50
	gbNDValueSpec = 51
	gbNDFuncDecl  = 52
	gbNDFile      = 53

	gbOpAdd         = 1
	gbOpSub         = 2
	gbOpMul         = 3
	gbOpQuo         = 4
	gbOpRem         = 5
	gbOpAnd         = 6
	gbOpOr          = 7
	gbOpXor         = 8
	gbOpAndNot      = 9
	gbOpShl         = 10
	gbOpShr         = 11
	gbOpLand        = 12
	gbOpLor         = 13
	gbOpEql         = 14
	gbOpNeq         = 15
	gbOpLss         = 16
	gbOpLeq         = 17
	gbOpGtr         = 18
	gbOpGeq         = 19
	gbOpNot         = 20
	gbOpArrow       = 21
	gbOpAssign      = 22
	gbOpDefine      = 23
	gbOpAddAssign   = 24
	gbOpSubAssign   = 25
	gbOpMulAssign   = 26
	gbOpQuoAssign   = 27
	gbOpRemAssign   = 28
	gbOpAndAssign   = 29
	gbOpOrAssign    = 30
	gbOpXorAssign   = 31
	gbOpShlAssign   = 32
	gbOpShrAssign   = 33
	gbOpAndNotAssgn = 34
	gbOpInc         = 35
	gbOpDec         = 36
	gbOpBreak       = 37
	gbOpContinue    = 38
	gbOpFallthrough = 39
	gbOpGoto        = 40
	gbOpVar         = 41
	gbOpConst       = 42
	gbOpAddr        = 43

	gbObjVar      = 1
	gbObjConst    = 2
	gbObjFunc     = 3
	gbObjBuiltin  = 4
	gbObjTypeName = 5

	gbAFTvConst     = uint32(1)
	gbAFTvConstNZ   = uint32(2)
	gbAFTvIsType    = uint32(4)
	gbAFObjPkgScope = uint32(8)
	gbAFObjIsField  = uint32(16)
	gbAFEllipsis    = uint32(32)
	gbAFIsIntZero   = uint32(64)
	gbAFConcrete    = uint32(128)
	gbAFTypeAlias   = uint32(256)
	gbAFHasTParams  = uint32(512)
	gbAFPtrRecv     = uint32(1024)
	gbAFIsDefault   = uint32(2048)
	gbAFBasicLitInt = uint32(8192)
	gbAFHasRecv     = uint32(16384)
	gbAFPredString  = uint32(65536)
)

type gbAstEncoder struct {
	te      *gbTypeEncoder
	nodes   []gbSpecAstNode
	funcSym map[*types.Func]uint32
}

func newAstEncoder(te *gbTypeEncoder) *gbAstEncoder {
	return &gbAstEncoder{te: te, funcSym: map[*types.Func]uint32{}}
}

func (a *gbAstEncoder) symOf(f *types.Func) uint32 {
	if f == nil {
		return gbNoIndex
	}
	if id, ok := a.funcSym[f]; ok {
		return id
	}
	id := uint32(len(a.funcSym) + 1)
	a.funcSym[f] = id
	return id
}

func (a *gbAstEncoder) alloc(n gbSpecAstNode) uint32 {
	if n.TypeID == 0 {
		n.TypeID = gbNoIndex
	}
	if n.RecvTypeID == 0 {
		n.RecvTypeID = gbNoIndex
	}
	if n.FuncSym == 0 {
		n.FuncSym = gbNoIndex
	}
	id := uint32(len(a.nodes))
	a.nodes = append(a.nodes, n)
	return id
}

func (a *gbAstEncoder) empty(kind uint8) uint32 {
	return a.alloc(gbSpecAstNode{
		Kind:       kind,
		TypeID:     gbNoIndex,
		RecvTypeID: gbNoIndex,
		FuncSym:    gbNoIndex,
		X:          gbNoIndex,
		Y:          gbNoIndex,
		Z:          gbNoIndex,
		W:          gbNoIndex,
	})
}

func gbTokOp(t token.Token) uint8 {
	switch t {
	case token.ADD:
		return gbOpAdd
	case token.SUB:
		return gbOpSub
	case token.MUL:
		return gbOpMul
	case token.QUO:
		return gbOpQuo
	case token.REM:
		return gbOpRem
	case token.AND:
		return gbOpAnd
	case token.OR:
		return gbOpOr
	case token.XOR:
		return gbOpXor
	case token.AND_NOT:
		return gbOpAndNot
	case token.SHL:
		return gbOpShl
	case token.SHR:
		return gbOpShr
	case token.LAND:
		return gbOpLand
	case token.LOR:
		return gbOpLor
	case token.EQL:
		return gbOpEql
	case token.NEQ:
		return gbOpNeq
	case token.LSS:
		return gbOpLss
	case token.LEQ:
		return gbOpLeq
	case token.GTR:
		return gbOpGtr
	case token.GEQ:
		return gbOpGeq
	case token.NOT:
		return gbOpNot
	case token.ARROW:
		return gbOpArrow
	case token.ASSIGN:
		return gbOpAssign
	case token.DEFINE:
		return gbOpDefine
	case token.ADD_ASSIGN:
		return gbOpAddAssign
	case token.SUB_ASSIGN:
		return gbOpSubAssign
	case token.MUL_ASSIGN:
		return gbOpMulAssign
	case token.QUO_ASSIGN:
		return gbOpQuoAssign
	case token.REM_ASSIGN:
		return gbOpRemAssign
	case token.AND_ASSIGN:
		return gbOpAndAssign
	case token.OR_ASSIGN:
		return gbOpOrAssign
	case token.XOR_ASSIGN:
		return gbOpXorAssign
	case token.SHL_ASSIGN:
		return gbOpShlAssign
	case token.SHR_ASSIGN:
		return gbOpShrAssign
	case token.AND_NOT_ASSIGN:
		return gbOpAndNotAssgn
	case token.INC:
		return gbOpInc
	case token.DEC:
		return gbOpDec
	case token.BREAK:
		return gbOpBreak
	case token.CONTINUE:
		return gbOpContinue
	case token.FALLTHROUGH:
		return gbOpFallthrough
	case token.GOTO:
		return gbOpGoto
	case token.VAR:
		return gbOpVar
	case token.CONST:
		return gbOpConst
	}
	return 0
}

func gbConcreteExpr(t ast.Expr) bool {
	switch t := ast.Unparen(t).(type) {
	case *ast.StructType, *ast.StarExpr, *ast.FuncType, *ast.ArrayType, *ast.MapType, *ast.ChanType:
		return true
	case *ast.Ident:
		switch t.Name {
		case "bool", "byte", "complex64", "complex128", "float32", "float64", "int", "int8", "int16", "int32", "int64",
			"rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
			return true
		}
	}
	return false
}

func (a *gbAstEncoder) typeInfoFlags(expr ast.Expr, tPkg *types.Package, info *types.Info) (uint32, uint32) {
	if info == nil {
		return gbNoIndex, 0
	}
	var tid = gbNoIndex
	var fl uint32
	if tv, ok := info.Types[expr]; ok {
		if tv.Type != nil {
			tid = a.te.encodeType(tv.Type)
		}
		if tv.Value != nil {
			fl |= gbAFTvConst
			if (tv.Value.Kind() == constant.Int || tv.Value.Kind() == constant.Float) && constant.Sign(tv.Value) != 0 {
				fl |= gbAFTvConstNZ
			}
		}
		if tv.IsType() {
			fl |= gbAFTvIsType
		}
	}
	if id, ok := expr.(*ast.Ident); ok {
		obj := info.Uses[id]
		if obj == nil {
			obj = info.Defs[id]
		}
		if obj != nil {
			if v, ok := obj.(*types.Var); ok && tPkg != nil {
				if v.Parent() == tPkg.Scope() {
					fl |= gbAFObjPkgScope
				}
				if v.IsField() {
					fl |= gbAFObjIsField
				}
			}
		}
	}
	return tid, fl
}

func (a *gbAstEncoder) encodeObjMeta(expr ast.Expr, info *types.Info) (uint8, uint32) {
	if info == nil {
		return 0, gbNoIndex
	}
	var id *ast.Ident
	switch e := expr.(type) {
	case *ast.Ident:
		id = e
	case *ast.SelectorExpr:
		id = e.Sel
	default:
		return 0, gbNoIndex
	}
	obj := info.Uses[id]
	if obj == nil {
		obj = info.Defs[id]
	}
	switch o := obj.(type) {
	case *types.Var:
		return gbObjVar, gbNoIndex
	case *types.Const:
		return gbObjConst, gbNoIndex
	case *types.Func:
		return gbObjFunc, a.symOf(o)
	case *types.Builtin:
		return gbObjBuiltin, gbNoIndex
	case *types.TypeName:
		return gbObjTypeName, gbNoIndex
	}
	return 0, gbNoIndex
}

func (a *gbAstEncoder) encodeFile(pkgPath string, f *ast.File, tPkg *types.Package, info *types.Info) uint32 {
	id := a.empty(gbNDFile)
	a.nodes[id].Name = pkgPath
	var imps []gbSpecImport
	for _, s := range f.Imports {
		p, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		alias := p[strings.LastIndex(p, "/")+1:]
		if s.Name != nil {
			alias = s.Name.Name
		}
		imps = append(imps, gbSpecImport{Alias: alias, Path: p})
	}
	a.nodes[id].Imports = imps
	var decls []uint32
	for _, d := range f.Decls {
		decls = append(decls, a.encodeDecl(d, tPkg, info))
	}
	a.nodes[id].Children = decls
	return id
}

func (a *gbAstEncoder) encodeDecl(d ast.Decl, tPkg *types.Package, info *types.Info) uint32 {
	switch d := d.(type) {
	case *ast.GenDecl:
		id := a.empty(gbNSDecl)
		a.nodes[id].Op = gbTokOp(d.Tok)
		var specs []uint32
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				sid := a.empty(gbNDTypeSpec)
				a.nodes[sid].Name = s.Name.Name
				if s.Assign != 0 {
					a.nodes[sid].Flags |= gbAFTypeAlias
				}
				if s.TypeParams != nil {
					a.nodes[sid].Flags |= gbAFHasTParams
				}
				if gbConcreteExpr(s.Type) {
					a.nodes[sid].Flags |= gbAFConcrete
				}
				a.nodes[sid].X = a.encodeExpr(s.Type, tPkg, info)
				specs = append(specs, sid)
			case *ast.ValueSpec:
				sid := a.empty(gbNDValueSpec)
				a.nodes[sid].Op = gbTokOp(d.Tok)
				var names []uint32
				for _, nm := range s.Names {
					names = append(names, a.encodeExpr(nm, tPkg, info))
				}
				a.nodes[sid].Lhs = names
				a.nodes[sid].X = a.encodeExpr(s.Type, tPkg, info)
				var vals []uint32
				for _, v := range s.Values {
					vals = append(vals, a.encodeExpr(v, tPkg, info))
				}
				a.nodes[sid].Rhs = vals
				specs = append(specs, sid)
			}
		}
		a.nodes[id].Children = specs
		return id
	case *ast.FuncDecl:
		id := a.empty(gbNDFuncDecl)
		a.nodes[id].Name = d.Name.Name
		var fl uint32
		if d.Type.TypeParams != nil {
			fl |= gbAFHasTParams
		}
		if d.Recv != nil {
			fl |= gbAFHasRecv
			if len(d.Recv.List) > 0 {
				a.nodes[id].Target = gbRecvTypeName(d.Recv.List[0].Type)
				a.nodes[id].Lhs = a.encodeFields(d.Recv, tPkg, info)
			}
		}
		if info != nil {
			if fn, ok := info.Defs[d.Name].(*types.Func); ok {
				a.nodes[id].FuncSym = a.symOf(fn)
				if d.Recv != nil {
					if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
						rt := sig.Recv().Type()
						a.nodes[id].RecvTypeID = a.te.encodeType(rt)
						if _, ptr := rt.Underlying().(*types.Pointer); ptr {
							fl |= gbAFPtrRecv
						}
					}
				}
			}
		}
		a.nodes[id].Flags = fl
		a.nodes[id].Children = a.encodeFields(d.Type.Params, tPkg, info)
		a.nodes[id].Rhs = a.encodeFields(d.Type.Results, tPkg, info)
		if d.Body != nil {
			a.nodes[id].W = a.encodeStmt(d.Body, tPkg, info)
		}
		return id
	}
	return a.empty(gbNSOtherStmt)
}

func (a *gbAstEncoder) encodeFields(fl *ast.FieldList, tPkg *types.Package, info *types.Info) []uint32 {
	if fl == nil {
		return nil
	}
	var out []uint32
	for _, f := range fl.List {
		fid := a.empty(gbNAField)
		var nids []uint32
		for _, nm := range f.Names {
			nids = append(nids, a.encodeExpr(nm, tPkg, info))
		}
		a.nodes[fid].Lhs = nids
		a.nodes[fid].X = a.encodeExpr(f.Type, tPkg, info)
		out = append(out, fid)
	}
	return out
}

func (a *gbAstEncoder) encodeStmt(s ast.Stmt, tPkg *types.Package, info *types.Info) uint32 {
	if s == nil {
		return gbNoIndex
	}
	switch s := s.(type) {
	case *ast.EmptyStmt:
		return a.empty(gbNSEmpty)
	case *ast.BlockStmt:
		id := a.empty(gbNSBlock)
		var ch []uint32
		for _, st := range s.List {
			ch = append(ch, a.encodeStmt(st, tPkg, info))
		}
		a.nodes[id].Children = ch
		return id
	case *ast.LabeledStmt:
		id := a.empty(gbNSLabeled)
		a.nodes[id].Name = s.Label.Name
		a.nodes[id].X = a.encodeStmt(s.Stmt, tPkg, info)
		return id
	case *ast.ExprStmt:
		id := a.empty(gbNSExpr)
		a.nodes[id].X = a.encodeExpr(s.X, tPkg, info)
		return id
	case *ast.SendStmt:
		id := a.empty(gbNSSend)
		a.nodes[id].X = a.encodeExpr(s.Chan, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(s.Value, tPkg, info)
		return id
	case *ast.IncDecStmt:
		id := a.empty(gbNSIncDec)
		a.nodes[id].Op = gbTokOp(s.Tok)
		a.nodes[id].X = a.encodeExpr(s.X, tPkg, info)
		return id
	case *ast.GoStmt:
		id := a.empty(gbNSGo)
		a.nodes[id].X = a.encodeExpr(s.Call, tPkg, info)
		return id
	case *ast.DeferStmt:
		id := a.empty(gbNSDefer)
		a.nodes[id].X = a.encodeExpr(s.Call, tPkg, info)
		return id
	case *ast.AssignStmt:
		id := a.empty(gbNSAssign)
		a.nodes[id].Op = gbTokOp(s.Tok)
		var lhs, rhs []uint32
		for _, l := range s.Lhs {
			lhs = append(lhs, a.encodeExpr(l, tPkg, info))
		}
		for _, r := range s.Rhs {
			rhs = append(rhs, a.encodeExpr(r, tPkg, info))
		}
		a.nodes[id].Lhs = lhs
		a.nodes[id].Rhs = rhs
		return id
	case *ast.DeclStmt:
		return a.encodeDecl(s.Decl, tPkg, info)
	case *ast.ReturnStmt:
		id := a.empty(gbNSReturn)
		var res []uint32
		for _, r := range s.Results {
			res = append(res, a.encodeExpr(r, tPkg, info))
		}
		a.nodes[id].Rhs = res
		return id
	case *ast.IfStmt:
		id := a.empty(gbNSIf)
		a.nodes[id].X = a.encodeStmt(s.Init, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(s.Cond, tPkg, info)
		a.nodes[id].Z = a.encodeStmt(s.Body, tPkg, info)
		a.nodes[id].W = a.encodeStmt(s.Else, tPkg, info)
		return id
	case *ast.ForStmt:
		id := a.empty(gbNSFor)
		a.nodes[id].X = a.encodeStmt(s.Init, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(s.Cond, tPkg, info)
		a.nodes[id].Z = a.encodeStmt(s.Post, tPkg, info)
		a.nodes[id].W = a.encodeStmt(s.Body, tPkg, info)
		return id
	case *ast.RangeStmt:
		id := a.empty(gbNSRange)
		a.nodes[id].Op = gbTokOp(s.Tok)
		a.nodes[id].X = a.encodeExpr(s.Key, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(s.Value, tPkg, info)
		a.nodes[id].Z = a.encodeExpr(s.X, tPkg, info)
		a.nodes[id].W = a.encodeStmt(s.Body, tPkg, info)
		return id
	case *ast.SwitchStmt:
		id := a.empty(gbNSSwitch)
		a.nodes[id].X = a.encodeStmt(s.Init, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(s.Tag, tPkg, info)
		a.nodes[id].Z = a.encodeStmt(s.Body, tPkg, info)
		return id
	case *ast.TypeSwitchStmt:
		id := a.empty(gbNSTypeSwitch)
		a.nodes[id].X = a.encodeStmt(s.Init, tPkg, info)
		a.nodes[id].Y = a.encodeStmt(s.Assign, tPkg, info)
		a.nodes[id].Z = a.encodeStmt(s.Body, tPkg, info)
		return id
	case *ast.SelectStmt:
		id := a.empty(gbNSSelect)
		a.nodes[id].Z = a.encodeStmt(s.Body, tPkg, info)
		return id
	case *ast.CaseClause:
		id := a.empty(gbNSCaseClause)
		if s.List == nil {
			a.nodes[id].Flags |= gbAFIsDefault
		}
		var lhs []uint32
		for _, x := range s.List {
			lhs = append(lhs, a.encodeExpr(x, tPkg, info))
		}
		var body []uint32
		for _, st := range s.Body {
			body = append(body, a.encodeStmt(st, tPkg, info))
		}
		a.nodes[id].Lhs = lhs
		a.nodes[id].Children = body
		return id
	case *ast.CommClause:
		id := a.empty(gbNSCommClause)
		a.nodes[id].X = a.encodeStmt(s.Comm, tPkg, info)
		var body []uint32
		for _, st := range s.Body {
			body = append(body, a.encodeStmt(st, tPkg, info))
		}
		a.nodes[id].Children = body
		return id
	case *ast.BranchStmt:
		id := a.empty(gbNSBranch)
		a.nodes[id].Op = gbTokOp(s.Tok)
		if s.Label != nil {
			a.nodes[id].Name = s.Label.Name
		}
		return id
	}
	return a.empty(gbNSOtherStmt)
}

func (a *gbAstEncoder) encodeExpr(x ast.Expr, tPkg *types.Package, info *types.Info) uint32 {
	if x == nil {
		return gbNoIndex
	}
	tid, fl := a.typeInfoFlags(x, tPkg, info)
	okind, fsym := a.encodeObjMeta(x, info)
	if gbConcreteExpr(x) {
		fl |= gbAFConcrete
	}
	switch x := x.(type) {
	case *ast.Ident:
		if x.Name == "string" && x.Obj == nil {
			fl |= gbAFPredString
		}
		return a.alloc(gbSpecAstNode{
			Kind: gbNAIdent, ObjKind: okind, Flags: fl, Name: x.Name,
			TypeID: tid, RecvTypeID: gbNoIndex, FuncSym: fsym,
			X: gbNoIndex, Y: gbNoIndex, Z: gbNoIndex, W: gbNoIndex,
		})
	case *ast.BasicLit:
		if x.Kind == token.INT {
			fl |= gbAFBasicLitInt
			if x.Value == "0" {
				fl |= gbAFIsIntZero
			}
		}
		return a.alloc(gbSpecAstNode{
			Kind: gbNABasicLit, Flags: fl,
			TypeID: tid, RecvTypeID: gbNoIndex, FuncSym: gbNoIndex,
			X: gbNoIndex, Y: gbNoIndex, Z: gbNoIndex, W: gbNoIndex,
		})
	case *ast.ParenExpr:
		id := a.empty(gbNAParen)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		return id
	case *ast.SelectorExpr:
		id := a.empty(gbNASelector)
		a.nodes[id].ObjKind = okind
		a.nodes[id].Flags |= fl
		a.nodes[id].Name = x.Sel.Name
		a.nodes[id].TypeID = tid
		a.nodes[id].FuncSym = fsym
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		return id
	case *ast.UnaryExpr:
		id := a.empty(gbNAUnary)
		op := gbTokOp(x.Op)
		if x.Op == token.AND {
			op = gbOpAddr
		}
		a.nodes[id].Op = op
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		return id
	case *ast.BinaryExpr:
		id := a.empty(gbNABinary)
		a.nodes[id].Op = gbTokOp(x.Op)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(x.Y, tPkg, info)
		return id
	case *ast.CallExpr:
		id := a.empty(gbNACall)
		if x.Ellipsis.IsValid() {
			fl |= gbAFEllipsis
		}
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.Fun, tPkg, info)
		var args []uint32
		for _, arg := range x.Args {
			args = append(args, a.encodeExpr(arg, tPkg, info))
		}
		a.nodes[id].Children = args
		return id
	case *ast.CompositeLit:
		id := a.empty(gbNACompLit)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.Type, tPkg, info)
		return id
	case *ast.TypeAssertExpr:
		id := a.empty(gbNATypeAssert)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(x.Type, tPkg, info)
		return id
	case *ast.StarExpr:
		id := a.empty(gbNAStar)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.X, tPkg, info)
		return id
	case *ast.FuncLit:
		id := a.empty(gbNAFuncLit)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].Children = a.encodeFields(x.Type.Params, tPkg, info)
		a.nodes[id].Rhs = a.encodeFields(x.Type.Results, tPkg, info)
		a.nodes[id].W = a.encodeStmt(x.Body, tPkg, info)
		return id
	case *ast.Ellipsis:
		id := a.empty(gbNAEllipsis)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.Elt, tPkg, info)
		return id
	case *ast.ArrayType:
		id := a.empty(gbNAArrayType)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.Len, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(x.Elt, tPkg, info)
		return id
	case *ast.MapType:
		id := a.empty(gbNAMapType)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		a.nodes[id].X = a.encodeExpr(x.Key, tPkg, info)
		a.nodes[id].Y = a.encodeExpr(x.Value, tPkg, info)
		return id
	case *ast.StructType, *ast.InterfaceType, *ast.FuncType, *ast.ChanType:
		id := a.empty(gbNAOtherType)
		a.nodes[id].Flags |= fl
		a.nodes[id].TypeID = tid
		return id
	}
	id := a.empty(gbNAOtherExpr)
	a.nodes[id].Flags |= fl
	a.nodes[id].TypeID = tid
	return id
}

type gbSpecField struct {
	Name  string `json:"name"`
	Type  uint32 `json:"type"`
	Flags uint8  `json:"flags"`
}

type gbSpecParam struct {
	Name string `json:"name"`
	Type uint32 `json:"type"`
}

type gbSpecMethod struct {
	Name     string `json:"name"`
	PkgPath  string `json:"pkgPath"`
	PkgName  string `json:"pkgName"`
	SigType  uint32 `json:"sigType"`
	GoText   string `json:"goText"`
	OrigPkg  string `json:"origPkg"`
	OrigRecv string `json:"origRecv"`
	Flags    uint8  `json:"flags"`
	InValSet bool   `json:"inValSet"`
}

type gbSpecTerm struct {
	Tilde bool   `json:"tilde"`
	Type  uint32 `json:"type"`
}

type gbSpecType struct {
	Kind       uint8          `json:"kind"`
	BasicKind  uint8          `json:"basicKind"`
	BasicName  string         `json:"basicName"`
	Name       string         `json:"name"`
	PkgPath    string         `json:"pkgPath"`
	PkgName    string         `json:"pkgName"`
	TypeText   string         `json:"typeText"`
	ShortText  string         `json:"shortText"`
	Elem       uint32         `json:"elem"`
	Key        uint32         `json:"key"`
	ArrLen     uint32         `json:"arrLen"`
	Underlying uint32         `json:"underlying"`
	RecvType   uint32         `json:"recvType"`
	RecvName   string         `json:"recvName"`
	Flags      uint32         `json:"flags"`
	Fields     []gbSpecField  `json:"fields"`
	Params     []gbSpecParam  `json:"params"`
	Results    []gbSpecParam  `json:"results"`
	Methods    []gbSpecMethod `json:"methods"`
	ValMethods []gbSpecMethod `json:"valMethods"`
	TypeParams []uint32       `json:"typeParams"`
	TypeArgs   []uint32       `json:"typeArgs"`
	Terms      []gbSpecTerm   `json:"terms"`
}

type gbSpecDecl struct {
	Kind       uint8  `json:"kind"`
	Name       string `json:"name"`
	TypeID     uint32 `json:"typeID"`
	GoText     string `json:"goText"`
	DeclTarget string `json:"declTarget"`
	Flags      uint32 `json:"flags"`
}

type gbSpecPkg struct {
	Path  string       `json:"path"`
	Name  string       `json:"name"`
	Decls []gbSpecDecl `json:"decls"`
}

type gbSpecImport struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
}

type gbSpecAstNode struct {
	Kind       uint8          `json:"kind"`
	Op         uint8          `json:"op"`
	ObjKind    uint8          `json:"objKind"`
	Flags      uint32         `json:"flags"`
	Name       string         `json:"name"`
	Target     string         `json:"target"`
	TypeID     uint32         `json:"typeID"`
	RecvTypeID uint32         `json:"recvTypeID"`
	FuncSym    uint32         `json:"funcSym"`
	X          uint32         `json:"x"`
	Y          uint32         `json:"y"`
	Z          uint32         `json:"z"`
	W          uint32         `json:"w"`
	Children   []uint32       `json:"children"`
	Lhs        []uint32       `json:"lhs"`
	Rhs        []uint32       `json:"rhs"`
	Imports    []gbSpecImport `json:"imports"`
}

type gbSpecAstPkg struct {
	Path  string   `json:"path"`
	Files []uint32 `json:"files"`
}

type gbSpecParamSite struct {
	Target string `json:"target"`
	Param  string `json:"param"`
	File   string `json:"file"`
	Line   uint32 `json:"line"`
	Col    uint32 `json:"col"`
}

type gbSpecPlatform struct {
	GOOS      string            `json:"goos"`
	GOARCH    string            `json:"goarch"`
	Types     []gbSpecType      `json:"types"`
	Packages  []gbSpecPkg       `json:"packages"`
	AstNodes  []gbSpecAstNode   `json:"astNodes"`
	AstPkgs   []gbSpecAstPkg    `json:"astPkgs"`
	EscapeOut string            `json:"escapeOut"`
	EscapeDir string            `json:"escapeDir"`
	Sites     []gbSpecParamSite `json:"sites"`
}

type gbSpecFact struct {
	Target string `json:"target"`
	Key    string `json:"key"`
}

type gbSpecBindTarget struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	ModuleStamp string `json:"moduleStamp"`
}

type gbSpecFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type gbDumpRequest struct {
	Mode          string             `json:"mode"`
	PkgPath       string             `json:"pkgPath"`
	PkgName       string             `json:"pkgName"`
	GoVersion     string             `json:"goVersion"`
	Toolchain     string             `json:"toolchain"`
	ModuleStamp   string             `json:"moduleStamp"`
	LedgerText    string             `json:"ledgerText"`
	Platforms     []gbSpecPlatform   `json:"platforms"`
	Facts         []gbSpecFact       `json:"facts"`
	BindTargets   []gbSpecBindTarget `json:"bindTargets"`
	Committed     []gbSpecFile       `json:"committed"`
	Written       []gbSpecFile       `json:"written"`
	DirRoot       string             `json:"dirRoot"`
	StdRaw        []string           `json:"stdRaw"`
	GoVersionRaw  string             `json:"goVersionRaw"`
	RootCheckPath string             `json:"rootCheckPath"`
	RootCheckName string             `json:"rootCheckName"`
}
