package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestStdRoles checks the std-roles fixture with Go check, evaluator,
// stock text build, and native IR (ERGO_FALLBACK=off) with zero IR fallbacks.
func TestStdRoles(t *testing.T) {
	needsBuild(t)
	dir := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "std-roles")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", a.Diagnostics)
	}
	b := eval.DefaultBudget()
	b.Seed, b.Source = 1, dir
	oracle, err := a.Evaluate(b)
	if err != nil {
		t.Fatal(err)
	}
	lines, stderr, exit, ok := eval.Summary(oracle.Results, dir)
	want := strings.Join(lines, "\n")
	if !ok || exit != 0 || stderr != "" || !strings.Contains(want, "ok StdRolesGenericAndOption") {
		t.Fatalf("evaluator did not pass StdRolesGenericAndOption: %+v", oracle.Results)
	}
	u, err := sketchfront.LibraryUnit(a, filepath.Join(dir, "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	census, err := lower.CensusIR(u, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range census.Funcs {
		if f.Std {
			continue
		}
		if len(f.Reasons) != 0 {
			t.Errorf("std-roles IR fallback in %s.%s: %v", f.Pkg, f.Name, f.Reasons)
		}
	}
	scratch := t.TempDir()
	bin := filepath.Join(scratch, "std-roles-text")
	if _, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode: build.ModeTest, Source: dir, Output: bin,
		Cache: build.Cache{Root: filepath.Join(scratch, "cache")}, Toolchain: build.HostToolchain(), Stderr: os.Stderr,
	}); err != nil {
		t.Fatal(err)
	}
	text, err := runCompiler(t, ctx, bin, "--seed", "1", "--source", dir)
	if err != nil || strings.TrimSpace(text) != want {
		t.Fatalf("stock text: %v\n%s\nwant:\n%s", err, text, want)
	}
	irDir := filepath.Join(scratch, "std-roles-ir")
	if err := os.MkdirAll(irDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ergo.mod", "main.ergo"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(irDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ERGO_FALLBACK", "off")
	ir, err := runCompiler(t, ctx, ergoCLI(t, ctx), "test", irDir, "--native", "--seed", "1", "--count", "1")
	if err != nil {
		t.Fatalf("native IR (ERGO_FALLBACK=off): %v\n%s", err, ir)
	}
	if !strings.HasPrefix(strings.TrimSpace(ir), want) {
		t.Fatalf("native IR (ERGO_FALLBACK=off) mismatch:\n%s\nwant prefix:\n%s", ir, want)
	}
	if !strings.Contains(ir, "licence law collections.Mappable.MapFuses for Box") || !strings.Contains(ir, "licence law collections.MapFold.FoldOfMap for Box") {
		t.Fatalf("native IR missing claim rewrite licence output:\n%s", ir)
	}
}

// TestStdRolesE verifies the self-hosted checker (compiler/check via checkdump)
// loads collections, option, core, and the std-roles fixture cleanly through
// E's import path and agrees byte-for-byte with internal/sketch/check.
func TestStdRolesE(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	dirs := []struct {
		path  string
		dir   string
		files []string
	}{
		{path: "prelude", dir: stdDir(), files: []string{filepath.Join(stdDir(), "builtin.ergo"), filepath.Join(stdDir(), "prelude.ergo")}},
		{path: "collections", dir: filepath.Join(stdDir(), "collections")},
		{path: "option", dir: filepath.Join(stdDir(), "option")},
		{path: "core", dir: filepath.Join(stdDir(), "core")},
		{path: "stdroles", dir: filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "std-roles")},
	}
	tmp := t.TempDir()
	var manifest []string
	for i, tc := range dirs {
		files := tc.files
		if len(files) == 0 {
			files = ergoFiles(t, tc.dir)
		}
		var ext, foreign []string
		switch tc.path {
		case "prelude":
			pkg := check.PreludePackage()
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
		case "stdroles":
			a, err := sketchfront.CheckDir(tc.dir)
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			if len(a.Diagnostics) != 0 {
				t.Fatalf("%s Go diagnostics: %v", tc.path, a.Diagnostics)
			}
			_, pkg, _, _ := a.RootPackage()
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
		default:
			pkg, err := check.Std.Import(tc.path)
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
		}
		ef, ff := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, ef, ext)
		writeLines(t, ff, foreign)
		manifest = append(manifest, strings.Join(append([]string{tc.path, ef, ff}, files...), " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	bin := filepath.Join(t.TempDir(), "checkdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/checkdump"); err != nil {
		t.Fatalf("ergo build ./cmd/checkdump: %v\n%s", err, o)
	}
	lines := runDump(t, ctx, bin, "-std", stdDir(), "-threads", "1", "-corpus", m)
	got := make([][]checkLine, len(dirs))
	at := -1
	for _, line := range lines {
		if strings.HasPrefix(line, "@pkg ") {
			at++
			continue
		}
		if at < 0 || at >= len(got) {
			continue
		}
		var d checkLine
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		got[at] = append(got[at], d)
	}
	if at != len(dirs)-1 {
		t.Fatalf("checkdump produced %d groups, want %d", at+1, len(dirs))
	}
	for i, tc := range dirs {
		if len(got[i]) != 0 {
			t.Errorf("%s: E reported unexpected diagnostics: %v", tc.path, got[i])
		}
	}
}
