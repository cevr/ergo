package selfhostcheck

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestFunctionClaims checks that a module can use a structural receiver
// through its declaring contract, and that invalid claims point to the clause.
// TestCompilerCheckAgrees runs the independent checker over the same files.
func TestFunctionClaims(t *testing.T) {
	root := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "func-claim")
	for _, tc := range []struct {
		dir  string
		code string
		text string
	}{
		{dir: "client"},
		{dir: "disjoint-bounds"},
		{dir: "wrong-owner", code: "S108", text: "contract's package"},
		{dir: "own-type", code: "S108", text: "on the type"},
		{dir: "no-receiver", code: "S108", text: "first parameter"},
		{dir: "projection", code: "S106", text: "re-applies Self"},
		{dir: "incomplete", code: "S106", text: "no function"},
		{dir: "overlap", code: "S106", text: "overlaps"},
		{dir: "crossing", code: "S106", text: "overlaps"},
		{dir: "wrong-signature", code: "S106", text: "has signature"},
		{dir: "wrong-row", code: "S106", text: "requires {Console}"},
		{dir: "wrong-result"},
		{dir: "include-overlap", code: "S106", text: "overlaps"},
		{dir: "foreign-include", code: "S106", text: "in its declaring package"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			a, err := sketchfront.CheckDir(filepath.Join(root, tc.dir))
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" {
				if len(a.Diagnostics) != 0 {
					t.Fatalf("valid claims: %v", a.Diagnostics)
				}
				if tc.dir == "client" {
					checkFunctionClaimCopies(t, a, filepath.Join(root, tc.dir))
				}
				return
			}
			if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != tc.code || !strings.Contains(a.Diagnostics[0].Message, tc.text) {
				t.Fatalf("want one %s containing %q, got %v", tc.code, tc.text, a.Diagnostics)
			}
		})
	}
}

// TestFunctionClaimTiers builds Go because both native backends must
// receive the runtime receiver as the first extern operand. A checker or
// type inspection alone cannot show that the test's assertions execute.
func TestFunctionClaimTiers(t *testing.T) {
	needsBuild(t)
	dir := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "func-claim", "client")
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
	if !ok || exit != 0 || stderr != "" || !strings.Contains(want, "ok ConcreteClaimsUseTheirProviders") {
		t.Fatalf("evaluator did not prove the test: %+v", oracle.Results)
	}
	u, err := sketchfront.LibraryUnit(a, filepath.Join(dir, "use.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	controlFile := filepath.Join(dir, "use.ergo")
	control, err := os.ReadFile(controlFile)
	if err != nil {
		t.Fatal(err)
	}
	direct := strings.NewReplacer(
		"roles.MapValue(", "roles.Map(",
		"roles.SizeOf(", "roles.Size(",
		"roles.ScoreOf(", "roles.Score(",
		"roles.TrimValue(", "roles.Trim(",
		"roles.BumpValue(", "roles.Bump(",
		"roles.Measure(xs)", "(foreign.Count(xs), roles.Nonempty(xs))",
	).Replace(string(control))
	ref, err := sketchfront.CheckDirOverlay(dir, map[string]string{controlFile: direct})
	if err != nil || len(ref.Diagnostics) != 0 {
		t.Fatalf("explicit direct-call control: %v; %v", err, ref.Diagnostics)
	}
	reference, err := sketchfront.LibraryUnit(ref, controlFile)
	if err != nil {
		t.Fatal(err)
	}
	claimCensus(t, "explicit providers", reference)
	claimCensus(t, "function claims", u)
	scratch := t.TempDir()
	bin := filepath.Join(scratch, "claims-text")
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
	ir, err := runCompiler(t, ctx, ergoCLI(t, ctx), "test", dir, "--native", "--seed", "1", "--count", "1")
	if err != nil || strings.TrimSpace(ir) != want {
		t.Fatalf("native IR: %v\n%s\nwant:\n%s", err, ir, want)
	}
	checkNativeClaimResult(t, ctx, filepath.Dir(dir))
}

func checkNativeClaimResult(t *testing.T, ctx context.Context, root string) {
	t.Helper()
	tmp := t.TempDir()
	var manifest []string
	var result string
	for _, name := range []string{"foreign", "roles", "wrong-result"} {
		dir := filepath.Join(root, name)
		a, err := sketchfront.CheckDir(dir)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("%s: %v; %v", name, err, a.Diagnostics)
		}
		_, pkg, _, _ := a.RootPackage()
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		e, f := filepath.Join(tmp, name+".ext"), filepath.Join(tmp, name+".foreign")
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		manifest = append(manifest, strings.Join(append([]string{importPath(dir, pkg), e, f}, ergoFiles(t, dir)...), " "))
		if name == "wrong-result" {
			const call = "roles.MapValue(xs, func(s string) int { len(s) })"
			src, err := os.ReadFile(filepath.Join(dir, "use.ergo"))
			if err != nil {
				t.Fatal(err)
			}
			start := strings.Index(string(src), call)
			if start < 0 {
				t.Fatalf("fixture does not contain %q", call)
			}
			result = "E use.ergo " + strconv.Itoa(start) + " " + strconv.Itoa(start+len(call)) + " []int"
		}
	}
	path := filepath.Join(tmp, "manifest")
	writeLines(t, path, manifest)
	lines := runDump(t, ctx, exprDump(t, ctx), "-std", stdDir(), "-corpus", path)
	if !slices.Contains(lines, result) {
		t.Fatalf("expected native checked result %q in:\n%s", result, strings.Join(lines, "\n"))
	}
}

func claimCensus(t *testing.T, label string, u lower.Unit) {
	t.Helper()
	census, err := lower.CensusIR(u, true)
	if err != nil {
		t.Fatal(err)
	}
	bodies, fallbacks := 0, 0
	for _, f := range census.Funcs {
		if f.Std {
			continue
		}
		bodies++
		if len(f.Reasons) != 0 {
			t.Errorf("%s: %s.%s fell back: %v", label, f.Pkg, f.Name, f.Reasons)
			fallbacks++
		}
	}
	t.Logf("IR census (%s): %d non-std bodies, %d text fallbacks", label, bodies, fallbacks)
}

func checkFunctionClaimCopies(t *testing.T, a sketchfront.Analysis, dir string) {
	t.Helper()
	_, client, _, _ := a.RootPackage()
	expected := map[string]map[string]string{
		"MapValue":  {"Map": "functionclaims/roles"},
		"SizeOf":    {"Size": "functionclaims/roles"},
		"ScoreOf":   {"Score": "functionclaims/roles"},
		"TrimValue": {"Trim": "functionclaims/roles"},
		"BumpValue": {"Bump": "functionclaims/roles"},
		"Measure":   {"Count": "functionclaims/foreign", "Nonempty": "functionclaims/roles"},
	}
	for _, inst := range client.Info.Instances {
		f := inst.Func
		if f == nil || expected[f.Name] == nil {
			continue
		}
		bound := make(map[*check.TypeParam]check.Type)
		for i, p := range f.Sig.TParams {
			bound[p] = inst.TypeArgs[i]
		}
		facts, why := check.CopyInfo(f.Pkg.Info, f.Decl, check.CopyReader(f.Pkg, bound))
		if why != "" {
			t.Fatal(why)
		}
		for site, call := range facts.Calls {
			if call.Kind != check.CallFunc || call.Func == nil || call.Func.Pkg.Path != expected[f.Name][call.Func.Name] {
				t.Fatalf("%s's closed copy resolved to %v, expected %v", f.Name, call, expected[f.Name])
			}
			result := facts.Types[site]
			if f.Name == "MapValue" && (result == nil || result.String() != "[]int" || len(call.TypeArgs) != 2 ||
				call.TypeArgs[0].String() != "string" || call.TypeArgs[1].String() != "int") {
				t.Fatalf("Map's result and arguments = %v, %v; want []int from Map[string, int]", result, call.TypeArgs)
			}
			delete(expected[f.Name], call.Func.Name)
		}
		if len(expected[f.Name]) != 0 {
			t.Fatalf("%s did not select %v", f.Name, expected[f.Name])
		}
		delete(expected, f.Name)
	}
	if len(expected) != 0 {
		t.Fatalf("client did not instantiate %v", expected)
	}
	u, err := sketchfront.LibraryUnit(a, filepath.Join(dir, "use.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lower.LowerIRLibrary(u, "claims"); err != nil {
		t.Fatalf("direct copies do not lower: %v", err)
	}
}
