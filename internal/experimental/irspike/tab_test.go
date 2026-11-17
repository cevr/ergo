package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/lower/tabgen"
)

// tabTabled matches the flags of the table of a generated function gJ in
// the source path's Go.
var tabTabled = regexp.MustCompile(`(?m)^var (tabg[0-9]+)K `)

// TestIRTablesMatchSource is the agreement property of tabulated
// decisions on IR (TAB-IR; lower/ir_tab.go): each program of the
// tabulated property's generator (lower/tabgen, TestTabulatedAgree's)
// prints, exits and fails the same on both paths; main, which calls every
// generated function twice on every argument of its domain, is an IR body;
// and it reads every table the source path writes (a call of the table's
// fill function in the IR). A wrong index makes two arguments share an
// entry, so the second call reads the other's value. A failing seed
// prints the Ergo source, both outcomes and its replay command. It builds
// Go because the question is what the tables return at run time (the
// first call's fill, the second's read, the defect). It catches the
// ledger's mutants tab-ir-off, tab-ir-enum-stride, tab-ir-byte-stride and
// tab-ir-bool-index.
func TestIRTablesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	reads := 0
	for seed := first; seed < first+count; seed++ {
		text, _, _ := tabgen.Program(seed, map[string]bool{})
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := fmt.Sprintf("replay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRTablesMatchSource$' ./internal/experimental/irspike", seed)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if !slices.Contains(rep.IR, "main_1") { // main's body (its Go main is the runtime's entry)
			t.Fatalf("seed %d: main is not an IR body (IR %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
		goText, err := os.ReadFile(filepath.Join(dir, "source", "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		irText, err := os.ReadFile(filepath.Join(dir, "ir", lower.IRFileName))
		if err != nil {
			t.Fatal(err)
		}
		tabled := tabTabled.FindAllStringSubmatch(string(goText), -1)
		if len(tabled) == 0 {
			t.Fatalf("seed %d: the source path tabulates no function\n%s", seed, text)
		}
		for _, m := range tabled {
			if !regexp.MustCompile(`"k":"call"[^{}]*"name":"` + m[1] + `Fill"`).Match(irText) {
				t.Errorf("seed %d: the IR does not read %s's table\n%s\n%s", seed, m[1], replay, text)
			}
			reads++
		}
	}
	t.Logf("%d tables read on IR", reads)
}
