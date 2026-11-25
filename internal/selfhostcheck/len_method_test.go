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

// TestLenMethod verifies the len-method fixture across Go check, evaluator,
// zero IR fallbacks, stock text build, and native IR (ERGO_FALLBACK=off).
func TestLenMethod(t *testing.T) {
	needsBuild(t)
	dir := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "len-method")
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
	if !ok || exit != 0 || stderr != "" || !strings.Contains(want, "ok LenMethodNoImport") || !strings.Contains(want, "ok LenMethodGenericLengthed") {
		t.Fatalf("evaluator failed len-method tests: %+v", oracle.Results)
	}
	u, err := sketchfront.LibraryUnit(a, filepath.Join(dir, "no_import.ergo"))
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
			t.Errorf("len-method IR fallback in %s.%s: %v", f.Pkg, f.Name, f.Reasons)
		}
	}
	scratch := t.TempDir()
	bin := filepath.Join(scratch, "len-method-text")
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
	irDir := filepath.Join(scratch, "len-method-ir")
	if err := os.MkdirAll(irDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ergo.mod", "no_import.ergo", "generic.ergo"} {
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
	if strings.TrimSpace(ir) != want {
		t.Fatalf("native IR (ERGO_FALLBACK=off) mismatch:\n%s\nwant:\n%s", ir, want)
	}
	if _, ds := check.CheckSource("type NonEmpty string where it.Len() > 0\nfunc f() NonEmpty { \"\" }\n"); len(ds) == 0 || ds[0].Code != "S108" {
		t.Fatalf("expected S108 for empty string literal against it.Len() > 0 refinement, got: %v", ds)
	}
}

// TestLenMethodE verifies the self-hosted checker (compiler/check via checkdump)
// agrees with Go check on both the positive len-method package, slice length
// refinements, and negative refinement / method-lookup cases using .Len().
func TestLenMethodE(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	tmp := t.TempDir()
	sliceRefDir := filepath.Join(tmp, "slice-ref")
	negUnsatDir := filepath.Join(tmp, "neg-unsat")
	negSliceDir := filepath.Join(tmp, "neg-slice")
	negIntDir := filepath.Join(tmp, "neg-int")
	for _, d := range []string{sliceRefDir, negUnsatDir, negSliceDir, negIntDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	extraCases := []struct {
		name string
		dir  string
		src  string
		code string
	}{
		{
			name: "slice_ref",
			dir:  sliceRefDir,
			src:  "type ShortSlice []int where 1 <= it.Len() && it.Len() <= 4\n",
			code: "",
		},
		{
			name: "neg_unsat",
			dir:  negUnsatDir,
			src:  "type Impossible string where it.Len() < 0\n",
			code: "S108",
		},
		{
			name: "neg_slice_unsat",
			dir:  negSliceDir,
			src:  "type ImpossibleSlice []int where it.Len() < 0\n",
			code: "S108",
		},
		{
			name: "neg_int",
			dir:  negIntDir,
			src:  "func f(n int) int {\n\tn.Len()\n}\n",
			code: "S104",
		},
	}
	for _, nc := range extraCases {
		if err := os.WriteFile(filepath.Join(nc.dir, "main.ergo"), []byte(nc.src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	type unitSpec struct {
		path     string
		dir      string
		files    []string
		wantCode string
	}
	units := []unitSpec{
		{path: "prelude", dir: stdDir(), files: []string{filepath.Join(stdDir(), "builtin.ergo"), filepath.Join(stdDir(), "prelude.ergo")}},
		{path: "collections", dir: filepath.Join(stdDir(), "collections")},
		{path: "option", dir: filepath.Join(stdDir(), "option")},
		{path: "core", dir: filepath.Join(stdDir(), "core")},
		{path: "lenmethod", dir: filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "len-method")},
	}
	for _, nc := range extraCases {
		units = append(units, unitSpec{path: nc.name, dir: nc.dir, files: []string{filepath.Join(nc.dir, "main.ergo")}, wantCode: nc.code})
	}

	var manifest []string
	goWant := map[int][]checkLine{}
	for i, tc := range units {
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
		case "collections", "option", "core":
			pkg, err := check.Std.Import(tc.path)
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
		case "lenmethod":
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
			src, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			pkg, ds := check.CheckSource(string(src))
			if tc.wantCode == "" {
				if len(ds) != 0 {
					t.Fatalf("%s: Go checker gave unexpected diagnostics %v", tc.path, ds)
				}
			} else if len(ds) == 0 || ds[0].Code != tc.wantCode {
				t.Fatalf("%s: Go checker gave %v, want %s", tc.path, ds, tc.wantCode)
			}
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
			goWant[i] = goCheckLines(ds, []string{"main.ergo"})
		}
		ef := filepath.Join(tmp, fmt.Sprintf("ext-%d", i))
		ff := filepath.Join(tmp, fmt.Sprintf("foreign-%d", i))
		writeLines(t, ef, ext)
		writeLines(t, ff, foreign)
		manifest = append(manifest, strings.Join(append([]string{tc.path, ef, ff}, files...), " "))
	}
	mf := filepath.Join(tmp, "manifest")
	writeLines(t, mf, manifest)
	bin := filepath.Join(t.TempDir(), "checkdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/checkdump"); err != nil {
		t.Fatalf("ergo build ./cmd/checkdump: %v\n%s", err, o)
	}
	out := runDump(t, ctx, bin, "-std", stdDir(), "-threads", "1", "-corpus", mf)
	got := make([][]checkLine, len(units))
	at := -1
	for _, line := range out {
		if strings.HasPrefix(line, "@pkg ") {
			at++
			continue
		}
		if at < 0 || at >= len(got) {
			continue
		}
		var r checkLine
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad checkdump line %q: %v", line, err)
		}
		got[at] = append(got[at], r)
	}
	if at != len(units)-1 {
		t.Fatalf("checkdump produced %d groups, want %d", at+1, len(units))
	}
	for i, tc := range units {
		if tc.wantCode == "" {
			if len(got[i]) != 0 {
				t.Errorf("%s: E checkdump reported unexpected diagnostics: %+v", tc.path, got[i])
			}
			continue
		}
		want := goWant[i]
		if len(got[i]) != len(want) {
			t.Errorf("%s: E checkdump gave %+v, want %+v", tc.path, got[i], want)
			continue
		}
		for j := range want {
			if got[i][j].Code != want[j].Code || got[i][j].Line != want[j].Line {
				t.Errorf("%s[%d]: E gave %+v, Go gave %+v", tc.path, j, got[i][j], want[j])
			}
		}
	}
}
