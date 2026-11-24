package sketchfront

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/check/snapshot"
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
			found := map[string]bool{}
			for _, res := range rep.Results {
				if !strings.Contains(res.Name, "Mappable.MapIdentity") && !strings.Contains(res.Name, "Mappable.MapCallback") {
					continue
				}
				if res.Status != row.status || !strings.Contains(res.Name, "for "+row.claimant+" at "+row.instance) {
					t.Fatalf("seed 1, %s: %+v; want %s for %s", dir, res, row.status, row.instance)
				}
				if row.status == eval.Refuted && !strings.Contains(res.Message, row.claimant+" claims role.Left[T] at ") {
					t.Fatalf("seed 1, %s: refutation does not name the written claim: %+v", dir, res)
				}
				found[res.Name] = true
			}
			if len(found) != 2 {
				t.Fatalf("seed 1, %s: expected verdicts for Mappable.MapIdentity and Mappable.MapCallback: %+v", dir, rep.Results)
			}
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
			var refuted *ClaimLicenceRefutedError
			if pkg == "result" && !errors.As(err, &refuted) || pkg != "result" && err != nil {
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
			a := load("tree")
			u, e := analysisUnit(a, filepath.Join(tmp, "tree"))
			if e != nil {
				t.Fatal(e)
			}
			u.Rewrite, e = ledgerEvidence(a, u, string(data), nil)
			if e != nil {
				t.Fatal(e)
			}
			u.NoFallback = true
			_, report, e := lower.LowerIR(u)
			t.Fatalf("site matching must agree on both spellings at Tree[string] and reject a callback that writes state:\n%s\nlower: %v, fallback: %v", table, e, report.Fallback)
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
		_, err = UpdateLedger(load("skip"), BuildRequest{Source: filepath.Join(tmp, "skip"), Ledger: ledger})
		var skipped *LicenceSkippedError
		if !errors.As(err, &skipped) || len(skipped.Skipped) != 1 || !strings.Contains(skipped.Skipped[0].Line(), "for claimlaws/skip.Word: skipped:") ||
			!strings.Contains(skipped.Skipped[0].Reason, "no family of pure functions") {
			t.Fatalf("expected an explicit skipped claim licence error, got %v", err)
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
						diagnostics = append(diagnostics, fmt.Sprintf("%s %+v: %s %s", d.File, d.Span, d.Code, d.Message))
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
							unit, err := analysisUnit(a, filepath.Join(dir, "feeds.ergo"))
							if err != nil {
								t.Fatal(err)
							}
							if _, _, err := lower.LowerIRTests(unit); err != nil {
								t.Fatalf("native law plan did not reserve the exact context builder: %v", err)
							}
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
	t.Run("snapshot", func(t *testing.T) {
		dir := t.TempDir()
		cache := filepath.Join(dir, "modcache")
		dep := filepath.Join(cache, "claimlaws@v0.0.0")
		app := filepath.Join(dir, "app")
		const mod = "module claimlaws\n\ngo 1.27\n"
		files := map[string]string{"go.mod": mod, "ergo.mod": "module claimlaws\n"}
		for _, name := range []string{"tree/tree.ergo", "role/map.ergo"} {
			src, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			files[name] = string(src)
		}
		write := func(name, src string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte(src), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for name, src := range files {
			write(filepath.Join(dep, name), src)
		}
		for name, src := range map[string]string{
			filepath.Join(cache, "cache", "download", "claimlaws", "@v", "v0.0.0.mod"): mod,
			filepath.Join(app, "ergo.mod"):                                             "module snapshotapp\n",
			filepath.Join(app, "go.mod"):                                               "module snapshotapp\n\ngo 1.27\n\nrequire claimlaws v0.0.0\n",
			filepath.Join(app, "go.sum"):                                               "claimlaws v0.0.0 " + refHash("claimlaws@v0.0.0/", files) + "\nclaimlaws v0.0.0/go.mod " + refHash("", map[string]string{"go.mod": mod}) + "\n",
			filepath.Join(app, "root.ergo"):                                            "import claimlaws/tree\n\nfunc Main(x tree.Tree[string]) int { tree.Dot(x) }\n",
		} {
			write(name, src)
		}
		a, err := checkDirIn(hostFS{}, app, gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, cache)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("source check: %v %v", err, a.Diagnostics)
		}
		var ps []*check.Package
		var keys []string
		for _, p := range a.Packages() {
			if p.Path == "snapshotapp" {
				continue
			}
			ps = append(ps, p.Package)
			keys = append(keys, check.ModuleSegment(p.Package.Path, "claimlaws seed 1"))
		}
		if len(ps) != 2 {
			t.Fatalf("expected exactly the role and the claimant: %v", keys)
		}
		verdicts := func(p *check.Package) string {
			t.Helper()
			b := eval.DefaultBudget()
			b.Seed = 1
			report, err := eval.Run(p, b)
			if err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for _, r := range report.Results {
				if strings.Contains(r.Name, "Mappable.") {
					got.WriteString(r.Name + ":" + string(r.Status) + "\n")
				}
			}
			for _, l := range p.FunctionRewrites() {
				e := lower.FuncLawEntry(l, nil, "", nil)
				got.WriteString(e.Key() + ":" + e.Digest + "\n")
			}
			return got.String()
		}
		cold := verdicts(ps[len(ps)-1])
		b, err := snapshot.ExportSnapshot(ps, keys)
		if err != nil {
			t.Fatal(err)
		}
		resetStdCache(t)
		t.Cleanup(func() { resetStdCache(t) })
		if err := snapshot.InstallStd(bytes.NewReader(b), int64(len(b))); err != nil {
			t.Fatal(err)
		}
		warm, ok := check.InstalledPackage(keys[len(keys)-1])
		if !ok {
			t.Fatal("claimant package did not hydrate")
		}
		if got := verdicts(warm); cold == "" || got != cold {
			t.Fatalf("claim plan differs after decoding:\nsource:\n%s\nsnapshot:\n%s", cold, got)
		}
	})
}
