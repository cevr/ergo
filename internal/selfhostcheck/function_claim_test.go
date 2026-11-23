package selfhostcheck

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
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
		{dir: "wrong-owner", code: "S108", text: "contract's package"},
		{dir: "own-type", code: "S108", text: "on the type"},
		{dir: "no-receiver", code: "S108", text: "first parameter"},
		{dir: "projection", code: "S106", text: "re-applies Self"},
		{dir: "incomplete", code: "S106", text: "no function"},
		{dir: "overlap", code: "S106", text: "overlaps"},
		{dir: "crossing", code: "S106", text: "overlaps"},
		{dir: "wrong-signature", code: "S106", text: "has signature"},
		{dir: "wrong-result", code: "S108", text: "cannot use []int as []bool"},
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
				checkFunctionClaimCopies(t, a, filepath.Join(root, tc.dir))
				return
			}
			if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != tc.code || !strings.Contains(a.Diagnostics[0].Message, tc.text) {
				t.Fatalf("want one %s containing %q, got %v", tc.code, tc.text, a.Diagnostics)
			}
		})
	}
}

func checkFunctionClaimCopies(t *testing.T, a sketchfront.Analysis, dir string) {
	t.Helper()
	_, client, _, _ := a.RootPackage()
	expected := map[string]map[string]string{
		"MapValue":  {"Map": "functionclaims/roles"},
		"SizeOf":    {"Size": "functionclaims/roles"},
		"ScoreOf":   {"Score": "functionclaims/roles"},
		"TrimValue": {"Trim": "functionclaims/roles"},
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
