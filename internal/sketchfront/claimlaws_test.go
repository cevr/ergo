package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestClaimLaws runs a contract declaration once per local claim, even
// though two include paths give the claimant the same declaring slot.
// A false body result must name the declaring law and the claim, not a
// role value (there is no such value).
func TestClaimLaws(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "claimlaws"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		pkg, claimant, instance string
		status                  eval.Status
	}{
		{"tree", "Tree", "Tree[int]", eval.Passed},
		{"result", "Result", "Result[int, int]", eval.Refuted},
	} {
		t.Run(row.pkg, func(t *testing.T) {
			dir := filepath.Join(root, row.pkg)
			a, err := CheckDir(dir)
			if err != nil || len(a.Diagnostics) != 0 {
				t.Fatalf("%s must check before running its law: %v %v", dir, err, a.Diagnostics)
			}
			var bindings int
			for _, r := range a.rootPackage().LawRuns {
				if r.Law.Name == "MapIdentity" && r.Law.Owner != nil && r.Law.Owner.Name == "Mappable" {
					bindings += len(r.Bindings)
				}
			}
			if bindings != 1 {
				t.Fatalf("seed 1, %s: got %d bindings for %s; want one binding of the inherited Mappable.MapIdentity", dir, bindings, row.instance)
			}
			b := eval.DefaultBudget()
			b.Seed = 1
			rep, err := a.Evaluate(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, res := range rep.Results {
				if !strings.Contains(res.Name, "Mappable.MapIdentity") {
					continue
				}
				if res.Status != row.status || !strings.Contains(res.Name, "for "+row.claimant+" at "+row.instance) {
					t.Fatalf("seed 1, %s: %+v; want %s for %s", dir, res, row.status, row.instance)
				}
				if row.status == eval.Refuted && !strings.Contains(res.Message, row.claimant+" claims role.Left[T] at ") {
					t.Fatalf("seed 1, %s: refutation does not name the written claim: %+v", dir, res)
				}
				return
			}
			t.Fatalf("seed 1, %s: no verdict for Mappable.MapIdentity: %+v", dir, rep.Results)
		})
	}
	t.Run("licence", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.CopyFS(tmp, os.DirFS(root)); err != nil {
			t.Fatal(err)
		}
		ledger := filepath.Join(tmp, lower.LedgerFile)
		load := func(pkg string) Analysis {
			t.Helper()
			a, err := CheckDir(filepath.Join(tmp, pkg))
			if err != nil || len(a.Diagnostics) != 0 {
				t.Fatalf("%s: %v %v", pkg, err, a.Diagnostics)
			}
			return a
		}
		write := func(pkg string) {
			t.Helper()
			_, err := UpdateLedger(load(pkg), BuildRequest{Source: filepath.Join(tmp, pkg), Ledger: ledger})
			if err != nil {
				t.Fatal(err)
			}
		}
		write("tree")
		write("result")
		data, err := os.ReadFile(ledger)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"law claimlaws/role.Mappable.MapKeepsCount for claimlaws/tree.Tree validated",
			"law claimlaws/role.Mappable.MapKeepsCount for claimlaws/result.Result refuted",
		} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("licence verdicts did not stay with their claims: %s; want %q", data, want)
			}
		}
		entries, err := lower.ParseLedger(string(data))
		if err != nil {
			t.Fatal(err)
		}
		states := func(pkg string) map[string]bool {
			a := load(pkg)
			out := map[string]bool{}
			for _, l := range a.rootPackage().FunctionRewrites() {
				e := lower.FuncLawEntry(l, ledgerNames(a), "", nil)
				out[e.Key()] = entries[e.Key()].Digest == e.Digest
			}
			return out
		}
		treeKey := "claimlaws/role.Mappable.MapKeepsCount for claimlaws/tree.Tree"
		resultKey := "claimlaws/role.Mappable.MapKeepsCount for claimlaws/result.Result"
		if !states("tree")[treeKey] || !states("result")[resultKey] {
			t.Fatal("unchanged claim evidence is not current")
		}
		sites, err := InspectRewrites(load("tree"), filepath.Join(tmp, "tree"), ledger)
		if err != nil {
			t.Fatal(err)
		}
		table := RewritesText(sites)
		if strings.Count(table, "current  applied") != 2 || !strings.Contains(table, "is not a pure function literal or function") {
			t.Fatalf("site matching must agree on both spellings at Tree[string] and reject a callback that writes state:\n%s", table)
		}
		impl := filepath.Join(tmp, "result", "result.ergo")
		body, err := os.ReadFile(impl)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(impl, []byte(strings.Replace(string(body), "\t\tWaiting[E]{Note: good.Note}", "\t\tGood[U, E]{Value: f(good.Value), Note: good.Note}", 1)), 0600); err != nil {
			t.Fatal(err)
		}
		if states("result")[resultKey] || !states("tree")[treeKey] {
			t.Fatal("editing Result.Map did not stale exactly Result's evidence")
		}
	})
	t.Run("context", func(t *testing.T) {
		root := filepath.Join("testdata", "claimfeed")
		for _, row := range []struct {
			name, replace string
			fed           bool
		}{
			{"exact", "int", true},
			{"other instance", "string", false},
			{"no context", "", false},
		} {
			t.Run(row.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.CopyFS(dir, os.DirFS(root)); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "feeds.ergo")
				src, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				text := string(src)
				switch row.replace {
				case "":
					text = "func main() {}\n"
				case "string":
					text = strings.Replace(text, `NewDevice[int](5,`, `NewDevice[string]("five",`, 1)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				as, _, err := CheckTree(dir)
				if err != nil {
					t.Fatal(err)
				}
				var diagnostics, lawVerdicts []string
				for _, a := range as {
					for _, d := range a.Diagnostics {
						diagnostics = append(diagnostics, d.Message)
					}
					if !row.fed || len(a.Diagnostics) != 0 || a.rootPackage() == nil {
						continue
					}
					b := eval.DefaultBudget()
					b.Seed = 1
					rep, err := a.Evaluate(b)
					if err != nil {
						t.Fatal(err)
					}
					for _, r := range rep.Results {
						if strings.Contains(r.Name, "FreshReading") && r.Status == eval.Passed {
							lawVerdicts = append(lawVerdicts, r.Name)
						}
					}
				}
				if row.fed && (len(diagnostics) != 0 || len(lawVerdicts) != 1 || !strings.Contains(lawVerdicts[0], "Device[int] under built")) {
					t.Fatalf("expected one law using the exact context acquisition: checked %v, ran %v", diagnostics, lawVerdicts)
				}
				if !row.fed && (len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "FreshReading") || !strings.Contains(diagnostics[0], "Device[int] is built only by its constructor")) {
					t.Fatalf("did not report exactly the unfed Device[int] obligation: %v", diagnostics)
				}
			})
		}
	})
}
