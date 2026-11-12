package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRCensusFindsEveryReason: the census of a generated program names
// every construct the generator planted in each function outside the
// subset, not only the first (mutant irt-first-reason-only), and names no
// reason for a function the IR path lowers. A failing seed prints the
// Ergo source, the planted and the censused reasons, and its replay.
func TestIRCensusFindsEveryReason(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 64)
	multi := 0
	for seed := first; seed < first+count; seed++ {
		text, planted := GeneratePlanted(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		c, err := lower.CensusIR(u, false)
		if err != nil {
			t.Fatal(err)
		}
		_, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatal(err)
		}
		lowered := map[string]bool{}
		for _, name := range rep.IR {
			lowered[name] = true
		}
		got := map[string][]string{}
		for _, f := range c.Funcs {
			if f.Std {
				continue
			}
			// A function whose first parameter is a record lowers as its
			// method: Rec.f1.
			name := f.Name[strings.LastIndex(f.Name, ".")+1:]
			got[name] = append(got[name], f.Reasons...)
			if lowered[f.Name] && len(f.Reasons) > 0 {
				t.Fatalf("seed %d: %s is lowered by IR, and the census names %v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRCensusFindsEveryReason ./internal/experimental/irspike\n%s", seed, f.Name, f.Reasons, seed, text)
			}
		}
		for fn, want := range planted {
			if len(want) > 1 {
				multi++
			}
			for _, r := range want {
				if !contains(got[fn], r) {
					t.Fatalf("seed %d: %s: planted %v, the census names %v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRCensusFindsEveryReason ./internal/experimental/irspike\n%s", seed, fn, want, got[fn], seed, text)
				}
			}
		}
	}
	if count >= 64 && multi == 0 {
		t.Fatal("no seed planted two reasons in one function: the property does not reach a second reason")
	}
}

// TestIRCoverage is the IR coverage report (plan-tier §1.2): the census of
// every example binary and test target, every std body and the lowering
// corpus. It is report-only until FLIP: it fails only when a reason has no
// owning unit, or when the census lowered another number of example
// binaries than the folders hold (mutant irt-example-skipped). With
// ERGO_IRCOVERAGE_OUT set it writes the table there.
func TestIRCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: lowers every example, std body and corpus program")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	c, err := TakeCensus(root, nopWriter{})
	if err != nil {
		t.Fatal(err)
	}
	// The reference count: every binary is a main.ergo at depth 1 to 3
	// under examples (P, P/sub, P/cmd/B).
	want := 0
	for _, pat := range []string{"*", "*/*", "*/*/*"} {
		m, _ := filepath.Glob(filepath.Join(root, "examples", pat, "main.ergo"))
		want += len(m)
	}
	if c.Targets[Examples] != want || want == 0 {
		t.Errorf("the census lowered %d example binaries; the examples hold %d", c.Targets[Examples], want)
	}
	var unowned []string
	for _, r := range c.Table() {
		if r.Unit == "" {
			unowned = append(unowned, r.Reason)
		}
	}
	var b strings.Builder
	c.WriteTable(&b)
	if out := os.Getenv("ERGO_IRCOVERAGE_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(unowned) > 0 {
		t.Fatalf("reasons no coverage unit owns (add each to reasonUnits): %q\n%s", unowned, b.String())
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
