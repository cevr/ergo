package sizes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBudgetSubtractsTheControl checks a ceiling on synthetic receipts:
// one whose file is over the ceiling but whose Ergo-only bytes are within
// it passes, and one whose Ergo-only bytes pass it fails and names the
// fixture, its file size and the package that grew most.
// Mutants: bs0-compare-off (the comparison accepts any size; the second
// receipt passes) and bs0-no-control (Ergo-only bytes omit the
// subtraction; the first receipt fails).
func TestBudgetSubtractsTheControl(t *testing.T) {
	control := Binary{Size: 4500, Stripped: 3000, Text: map[string]uint64{"runtime": 900}}
	budget := Budget{Fixtures: map[string]Fixture{"within": {ErgoOnly: 1000, ErgoOnlyStripped: 1000, GoPackages: 10}, "over": {ErgoOnly: 1000, ErgoOnlyStripped: 1000, GoPackages: 10}}}
	within := Receipt{Fixture: "within", Binary: Binary{Size: 5000, Stripped: 3500, GoPackages: 10}, Control: control}
	over := Receipt{Fixture: "over", Binary: Binary{Size: 6000, Stripped: 3500, GoPackages: 10, Text: map[string]uint64{"runtime": 950, "fmt": 400}}, Control: control}
	problems := budget.Check([]Receipt{within, over})
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "over: ") || !strings.Contains(problems[0], "file 6000") || !strings.Contains(problems[0], "fmt +400, runtime +50") {
		t.Fatalf("problems %q, want one for over naming file 6000 and fmt +400, runtime +50", problems)
	}
}

// TestBudgetFactsMatchSymbols checks a fact against a synthetic receipt
// that links a forbidden symbol.
// Mutant: bs0-fact-off (the symbol matcher matches nothing).
func TestBudgetFactsMatchSymbols(t *testing.T) {
	budget := Budget{Fixtures: map[string]Fixture{"hello": {ErgoOnly: 1, ErgoOnlyStripped: 1, GoPackages: 1, Forbid: []string{`^crypto/`}}}}
	r := Receipt{Fixture: "hello", Binary: Binary{symbols: []string{"main.main", "crypto/sha256.Sum256"}}}
	problems := budget.Check([]Receipt{r})
	if len(problems) != 1 || !strings.Contains(problems[0], "crypto/sha256.Sum256") {
		t.Fatalf("problems %q, want the fact to name crypto/sha256.Sum256", problems)
	}
}

// TestMeasureReadsEachFile measures the sizes of a synthetic pair of files
// of different lengths.
// Mutant: bs0-wrong-file (the stripped size is read from the default file).
func TestMeasureReadsEachFile(t *testing.T) {
	dir := t.TempDir()
	full, stripped := filepath.Join(dir, "default"), filepath.Join(dir, "stripped")
	if err := os.WriteFile(full, []byte("eleven byte"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stripped, []byte("seven b"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, small, err := fileSizes(full, stripped)
	if err != nil || size != 11 || small != 7 {
		t.Fatalf("measured %d and %d (%v), want 11 and 7", size, small, err)
	}
}

// TestRegistryFactsForbidTheRegistryOnly checks the actor fixtures' facts
// in budgets.json against synthetic receipts: the reference lowering's
// start-up registration (RegisterMachine) breaks them, and the lazy read
// of a generic flat union's variant name (VariantOf, which BS8's
// ErgoVariant calls on the trace path) does not.
// Mutants: bs8-fact-names-variant (the fact forbids VariantOf again) and
// bs8-fact-misses-register (the fact no longer names RegisterMachine).
func TestRegistryFactsForbidTheRegistryOnly(t *testing.T) {
	budget, err := Checked()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"deploys", "pairing"} {
		f := budget.Fixtures[fixture]
		one := Budget{Fixtures: map[string]Fixture{fixture: f}}
		receipt := func(symbol string) Receipt {
			return Receipt{Fixture: fixture, Binary: Binary{Size: 1, Stripped: 1, symbols: []string{"main.main", symbol}}}
		}
		if problems := one.Check([]Receipt{receipt("ergo.local/prototype/runtime/v1.VariantOf[go.shape.struct {}]")}); len(problems) != 0 {
			t.Errorf("%s: a lazy VariantOf breaks %q; only the registry lowering should", fixture, problems)
		}
		if problems := one.Check([]Receipt{receipt("ergo.local/prototype/runtime/v1.RegisterMachine[go.shape.struct {},go.shape.struct {}]")}); len(problems) != 1 {
			t.Errorf("%s: RegisterMachine gives %q; want the registry fact", fixture, problems)
		}
	}
}
