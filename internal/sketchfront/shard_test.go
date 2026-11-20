package sketchfront

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// shardProgram lowers the test program of the package in dir (main
// source file main) with declaration shards, and returns its files by Go
// package folder ("." the root): the Go text and the IR file of each.
func shardProgram(t *testing.T, dir, main string) (map[string][]byte, lower.SplitReport) {
	t.Helper()
	a, err := CheckDir(dir)
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("%s does not check: %v %v", dir, err, a.Diagnostics)
	}
	unit, err := analysisUnit(a, filepath.Join(dir, main))
	if err != nil {
		t.Fatal(err)
	}
	files, _, rep, err := lower.LowerIRShards(unit, true)
	if err != nil || rep.Reason != "" {
		t.Fatalf("%s: the shard lowering fails: %v %+v", dir, err, rep)
	}
	pkgs := map[string][]byte{}
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".go") || path.Base(f.Path) == lower.IRFileName {
			d := path.Dir(f.Path)
			pkgs[d] = append(pkgs[d], []byte("\x00"+f.Path+"\x00")...)
			pkgs[d] = append(pkgs[d], f.Data...)
		}
	}
	return pkgs, rep
}

// TestShardsLeaveTheRootUnimported: Go imports no main package, so the
// root keeps every group that reaches a pinned one (main, an init, an
// embedded asset), and only those (law InitsKeepTheirOrder, and the
// split's rule for embedded assets). On the router example, whose root
// reads its embedded help through functions, and the deps test programs:
// the root is cut into shards, no Go package imports the root, and every
// //go:embed and Go init is in the root. The router's root is under the
// 64 KiB a build cuts from (its code is in its packages), so its case cuts
// after every component (lower.SetEveryShardForTest). Mutant:
// ft9-shard-root-reach-dropped.
func TestShardsLeaveTheRootUnimported(t *testing.T) {
	for _, c := range []struct {
		dir, file string
		modes     []bool // tests
		every     bool   // cut after every component
	}{
		{filepath.Join("router", "cmd", "router"), "main.ergo", []bool{false}, true},
		{filepath.Join("deps", "graph"), "graph.ergo", []bool{true}, false},
	} {
		dir, err := filepath.Abs(filepath.Join("..", "..", "examples", c.dir))
		if err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v %v", dir, err, a.Diagnostics)
		}
		unit, err := analysisUnit(a, filepath.Join(dir, c.file))
		if err != nil {
			t.Fatal(err)
		}
		for _, tests := range c.modes {
			restore := func() {}
			if c.every {
				restore = lower.SetEveryShardForTest()
			}
			files, _, rep, err := lower.LowerIRShards(unit, tests)
			restore()
			if err != nil || rep.Reason != "" {
				t.Fatalf("%s: %v %+v", c.dir, err, rep)
			}
			if rep.Shards < 1 {
				t.Fatalf("%s tests=%v: the root is not cut: %+v", c.dir, tests, rep)
			}
			for _, f := range files {
				if !strings.HasSuffix(f.Path, ".go") || !strings.HasPrefix(f.Path, "shard/") {
					continue
				}
				text := string(f.Data)
				if importsRoot.MatchString(text) {
					t.Errorf("%s tests=%v: %s imports the root", c.dir, tests, f.Path)
				}
				if strings.Contains(text, "//go:embed") || strings.Contains(text, "\nfunc init() {") {
					t.Errorf("%s tests=%v: %s has an embedded asset or a Go init:\n%s", c.dir, tests, f.Path, text)
				}
			}
		}
	}
}

// TestShardsAfterTheFirstBuild: a test build shards only a program built
// before with other inputs (lowerAuto). The first build of a program is
// cold, and shards would make it dearer (each shard compiles its imports
// and instantiations again); a build after an edit is the edit loop. An
// unchanged build names the module the one before it did (NG-Q8: an
// unchanged second `ergo test -work` landed in another module). With one
// cache, on a copy of examples/deps/graph: the first test build emits no
// shard, an unchanged second one reuses its module, a build after a body
// edit emits shards, and ERGO_LOWER=split keeps the split. It lowers in
// process and stops before Go compiles (BuildRequest.Reuse). Mutants:
// ft9-auto-shards-cold, ft9-auto-never-marks, q8-auto-shards-unchanged.
func TestShardsAfterTheFirstBuild(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("..", "..", "examples", "deps"))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "deps")
	if err := os.CopyFS(work, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "graph")
	cache := build.Cache{Root: t.TempDir()}
	build1 := func() (string, bool) {
		t.Helper()
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v %v", dir, err, a.Diagnostics)
		}
		var has bool
		m, err := Build(a, BuildRequest{Mode: build.ModeTest, Source: dir, Output: filepath.Join(t.TempDir(), "t"),
			Cache: cache, Toolchain: build.Toolchain{Go: "go"}, Reuse: func(m build.Module) bool {
				_, err := os.Stat(filepath.Join(m.Dir, "shard"))
				has = err == nil
				return true
			}})
		if err != nil {
			t.Fatalf("the test build does not lower: %v", err)
		}
		return m.Dir, has
	}
	first, has := build1()
	if has {
		t.Fatal("the first test build of the program shards: its cold compile pays every shard's imports")
	}
	if again, _ := build1(); again != first {
		t.Fatalf("an unchanged second test build names the module %s, not the first build's %s", again, first)
	}
	file := filepath.Join(dir, "graph.ergo")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	at := bodyEdits(string(text))
	if len(at) == 0 {
		t.Fatal("graph.ergo has no integer literal in a body to edit")
	}
	d := text[at[0]]
	text[at[0]] = '0' + (d-'0'+1)%10
	if err := os.WriteFile(file, text, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, has := build1(); !has {
		t.Fatal("the test build after a body edit does not shard: a body edit compiles the whole package")
	}
	t.Setenv(LowerSingleEnv, "split")
	if _, has := build1(); has {
		t.Fatal("ERGO_LOWER=split shards")
	}
}

// importsRoot matches an import of the lowered module's root package,
// ergo.local/program (the split spells it with a trailing slash).
var importsRoot = regexp.MustCompile(`(?m)^\s*\w+ "ergo\.local/program/?"$`)

// bodyLiteral finds integer literals in indented lines of Ergo source
// (inside a body), outside comments and lines with a string.
var bodyLiteral = regexp.MustCompile(`\b[0-9]+\b`)

// bodyEdits are the offsets of the integer literals of src that a body
// edit may change.
func bodyEdits(src string) []int {
	var out []int
	at := 0
	for _, line := range strings.SplitAfter(src, "\n") {
		code, _, _ := strings.Cut(line, "//")
		if (strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "  ")) && !strings.Contains(code, `"`) {
			for _, m := range bodyLiteral.FindAllStringIndex(code, -1) {
				out = append(out, at+m[1]-1)
			}
		}
		at += len(line)
	}
	return out
}

// TestBodyEditChangesOneShard is the red acceptance check of declaration
// shards (test-speed FT9, law BodyEditChangesOneShard): on the test
// programs of examples/deps/graph and examples/deps/resolve, a seeded
// body edit that keeps the lines and the references (the last digit of an
// integer literal in a body) changes the files of one Go package, a shard
// no bigger than the cut's maximum and its largest component, of a root
// cut into at least two shards. Before shards, the root holds the whole
// program, so every edit changes a package of 144 KB and more. It lowers
// in process; TestBodyEditCompilesOneShard counts the compiles of a build.
// A failing seed prints the edit and the replay command
// (ERGO_SHARD_SEED=n).
func TestBodyEditChangesOneShard(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SHARD_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "examples", "deps"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ pkg, file string }{{"graph", "graph.ergo"}, {"resolve", "resolve.ergo"}} {
		t.Run(c.pkg, func(t *testing.T) {
			work := filepath.Join(t.TempDir(), "deps")
			if err := os.CopyFS(work, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(work, c.pkg)
			before, rep := shardProgram(t, dir, c.file)
			t.Logf("%s: %+v", c.pkg, rep)
			if rep.Shards < 2 {
				t.Fatalf("%s: the test program is cut into %d shards, want 2 or more", c.pkg, rep.Shards)
			}
			// The root's code: the root and its shards, and since FT6 the
			// test build's root package below the harness and its shards.
			root := 0
			for d, data := range before {
				if d == "." || d == "ergoroot" || strings.HasPrefix(d, "shard/root/") || strings.HasPrefix(d, "shard/ergoroot/") {
					root += len(data)
					t.Logf("%s: %d bytes", d, len(data))
				}
			}
			r := rand.New(rand.NewPCG(seed, 0x5a4d))
			file := filepath.Join(dir, c.file)
			orig, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			edits := bodyEdits(string(orig))
			if len(edits) == 0 {
				t.Fatalf("%s has no integer literal in a body", c.file)
			}
			done := 0
			for step := 0; step < 40 && done < 10; step++ {
				at := edits[r.IntN(len(edits))]
				text := slices.Clone(orig)
				text[at] = '0' + (text[at]-'0'+1+byte(r.IntN(8)))%10
				if err := os.WriteFile(file, text, 0o644); err != nil {
					t.Fatal(err)
				}
				a, err := CheckDir(dir)
				if err != nil || len(a.Diagnostics) > 0 {
					continue
				}
				after, _ := shardProgram(t, dir, c.file)
				var changed []string
				for d := range before {
					if !bytes.Equal(before[d], after[d]) {
						changed = append(changed, d)
					}
				}
				for d := range after {
					if _, ok := before[d]; !ok {
						changed = append(changed, d)
					}
				}
				slices.Sort(changed)
				line := bytes.Count(orig[:at], []byte("\n")) + 1
				edit := fmt.Sprintf("%s:%d: %q -> %q", c.file, line, orig[at], text[at])
				switch {
				case len(changed) != 1:
					t.Errorf("seed %d: the edit %s changes the Go packages %v, want one\nreplay: ERGO_SHARD_SEED=%d go test -run TestBodyEditChangesOneShard ./internal/sketchfront", seed, edit, changed, seed)
				case 2*len(after[changed[0]]) > root:
					t.Errorf("seed %d: the edit %s changes %s, %d bytes of the root's %d: a shard holds half the program\nreplay: ERGO_SHARD_SEED=%d go test -run TestBodyEditChangesOneShard ./internal/sketchfront", seed, edit, changed[0], len(after[changed[0]]), root, seed)
				default:
					t.Logf("seed %d: %s changes %s (%d bytes)", seed, edit, changed[0], len(after[changed[0]]))
				}
				done++
			}
			if done < 5 {
				t.Fatalf("only %d of the seeded edits check", done)
			}
		})
	}
}
