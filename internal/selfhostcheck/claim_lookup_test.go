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

// TestClaimLookup verifies file-local function-form claim lookup precedence,
// ambiguity diagnostics, inherent-member priority, and plain-function refusal.
// TestCompilerCheckAgrees checks the self-hosted checker on the same fixtures.
func TestClaimLookup(t *testing.T) {
	root := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "claim-lookup")
	for _, tc := range []struct {
		dir   string
		code  string
		texts []string
	}{
		{dir: "ok"},
		{dir: "alias-ok"},
		{dir: "no-import", code: "S104", texts: []string{"[]int has no field or method Sum"}},
		{dir: "sibling-import", code: "S104", texts: []string{"[]int has no field or method Sum"}},
		{dir: "ambiguous", code: "S108", texts: []string{"ambiguous method Sum", "exta.Sum", "extb.Sum"}},
		{dir: "plain-func", code: "S104", texts: []string{"[]int has no field or method PlainOnly"}},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			a, err := sketchfront.CheckDir(filepath.Join(root, tc.dir))
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" {
				if len(a.Diagnostics) != 0 {
					t.Fatalf("unexpected diagnostics: %v", a.Diagnostics)
				}
				if tc.dir == "ok" {
					checkInherentWinsCallTarget(t, a)
				}
				return
			}
			if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != tc.code {
				t.Fatalf("want one %s, got %v", tc.code, a.Diagnostics)
			}
			for _, want := range tc.texts {
				if !strings.Contains(a.Diagnostics[0].Message, want) {
					t.Fatalf("diagnostic %q does not contain %q", a.Diagnostics[0].Message, want)
				}
			}
		})
	}
}

func checkInherentWinsCallTarget(t *testing.T, a sketchfront.Analysis) {
	t.Helper()
	_, pkg, _, _ := a.RootPackage()
	foundTag := false
	foundSum := false
	for _, call := range pkg.Info.Calls {
		if call == nil || call.Kind != check.CallFunc || call.Func == nil || call.Recv == nil {
			continue
		}
		if call.Func.Name == "Tag" {
			foundTag = true
			if call.Func.Pkg == nil || call.Func.Pkg.Path != "claimlookup/base" {
				t.Fatalf("b.Tag() resolved to %v, want inherent claimlookup/base.Tag", call.Func.Pkg)
			}
		}
		if call.Func.Name == "Sum" {
			foundSum = true
			if call.Func.Pkg == nil || call.Func.Pkg.Path != "claimlookup/exta" {
				t.Fatalf("xs.Sum() resolved to %v, want claimlookup/exta.Sum", call.Func.Pkg)
			}
		}
	}
	if !foundTag {
		t.Fatal("did not observe inherent b.Tag() call")
	}
	if !foundSum {
		t.Fatal("did not observe claim-lookup xs.Sum() call")
	}
}

// TestClaimLookupTiers proves dot-syntax calls to function-form claims
// execute identically to package-qualified calls under eval and native IR
// (ERGO_FALLBACK=off) with zero text fallbacks.
func TestClaimLookupTiers(t *testing.T) {
	needsBuild(t)
	dir := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "claim-lookup", "ok")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	a, err := sketchfront.CheckDir(dir)
	if err != nil || len(a.Diagnostics) != 0 {
		t.Fatalf("check: %v; %v", err, a.Diagnostics)
	}
	b := eval.DefaultBudget()
	b.Seed, b.Source = 1, dir
	oracle, err := a.Evaluate(b)
	if err != nil {
		t.Fatal(err)
	}
	lines, stderr, exit, ok := eval.Summary(oracle.Results, dir)
	want := strings.Join(lines, "\n")
	if !ok || exit != 0 || stderr != "" || !strings.Contains(want, "ok ClaimLookupParity") {
		t.Fatalf("evaluator did not prove ClaimLookupParity: %+v", oracle.Results)
	}
	u, err := sketchfront.LibraryUnit(a, filepath.Join(dir, "direct.ergo"))
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
			t.Errorf("claim-lookup IR fallback in %s.%s: %v", f.Pkg, f.Name, f.Reasons)
		}
	}
	scratch := t.TempDir()
	bin := filepath.Join(scratch, "claim-lookup-text")
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
	t.Setenv("ERGO_FALLBACK", "off")
	ir, err := runCompiler(t, ctx, ergoCLI(t, ctx), "test", dir, "--native", "--seed", "1", "--count", "1")
	if err != nil || strings.TrimSpace(ir) != want {
		t.Fatalf("native IR (ERGO_FALLBACK=off): %v\n%s\nwant:\n%s", err, ir, want)
	}
}

// TestClaimLookupNative verifies byte-for-byte diagnostic agreement between
// internal/sketch/check and compiler/check (checkdump) across every case in
// testdata/agree/claim-lookup.
func TestClaimLookupNative(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	root := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "claim-lookup")
	dirs := []string{
		filepath.Join(root, "base"),
		filepath.Join(root, "exta"),
		filepath.Join(root, "extb"),
		filepath.Join(root, "ok"),
		filepath.Join(root, "alias-ok"),
		filepath.Join(root, "no-import"),
		filepath.Join(root, "sibling-import"),
		filepath.Join(root, "ambiguous"),
		filepath.Join(root, "plain-func"),
	}
	tmp := t.TempDir()
	var manifest []string
	wants := make([][]checkLine, len(dirs))
	for i, dir := range dirs {
		files := ergoFiles(t, dir)
		a, err := sketchfront.CheckDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", rel(dir), err)
		}
		_, pkg, _, _ := a.RootPackage()
		var ext, foreign []string
		pkgPath := sourceImportPath(dir, "main")
		if pkg != nil {
			_, ext = goKinds(pkg)
			_, foreign = goTypes(pkg)
			pkgPath = importPath(dir, pkg)
		}
		names := make(map[string]bool, len(files))
		for _, f := range files {
			names[filepath.Base(f)] = true
		}
		for _, d := range a.Diagnostics {
			name := filepath.Base(d.File)
			if names[name] {
				wants[i] = append(wants[i], checkLine{File: name, Line: d.Span.Line, Col: d.Span.Column, Code: d.Code, Message: d.Message})
			}
		}
		ef, ff := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, ef, ext)
		writeLines(t, ff, foreign)
		manifest = append(manifest, strings.Join(append([]string{pkgPath, ef, ff}, files...), " "))
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
	for _, l := range lines {
		if strings.HasPrefix(l, "@pkg ") {
			at++
			continue
		}
		var cl checkLine
		if err := json.Unmarshal([]byte(l), &cl); err != nil {
			t.Fatalf("checkdump json: %v\n%s", err, l)
		}
		if at >= 0 && at < len(dirs) {
			got[at] = append(got[at], cl)
		}
	}
	for i, dir := range dirs {
		var wStr, gStr []string
		for _, w := range wants[i] {
			wStr = append(wStr, w.String())
		}
		for _, g := range got[i] {
			gStr = append(gStr, g.String())
		}
		if strings.Join(wStr, "\n") != strings.Join(gStr, "\n") {
			t.Errorf("%s mismatch:\nwant:\n%s\ngot:\n%s", rel(dir), strings.Join(wStr, "\n"), strings.Join(gStr, "\n"))
		}
	}
}
