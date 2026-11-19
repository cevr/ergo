package sketchfront

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestInspectRewritesReportsFuncSites is ergo inspect rewrites over a
// module's top-level rewrite law (LAWFFI r2 §3.6, Report; LF6c on the IR
// path), from the ledger ergo test's licence run writes (UpdateLedger):
// with no ledger, F's site is missing and not applied; after the licence
// run it is validated by eval, current and applied; G's site, inside
// AsWritten, is current and not applied, for AsWritten. The table names
// each.
//
// Mutants: lf6c-ir-sites-text-path (InspectRewrites lowers the text path,
// which rewrites no function law, so it finds no function site),
// lf6c-ir-site-unrecorded (rewriteAt records no row),
// lf6c-ir-switch-unkeyed (the build cache keys no -rewrites).
func TestInspectRewritesReportsFuncSites(t *testing.T) {
	const src = `func Enc(s string) string { s + "!" }

func Dec(s string) string { s[:len(s) - 1] }

law DecEnc(s string) { Dec(Enc(s)) } == { s }

func F(t string) string { Dec(Enc(t)) }

func G(t string) string { AsWritten(Dec(Enc(t))) }

func main() {
	let _ = F("a")
	let _ = G("b")
}
`
	// -rewrites keys the module (the bug this found: an off build reused
	// an on build's cached module, rewrites applied).
	keyed := map[string]string{}
	for _, mode := range []string{"", "on", "proved", "off"} {
		unit, err := analysisUnit(checked(t, src), "/ergo-test/rewrite.ergo")
		if err != nil {
			t.Fatal(err)
		}
		unit.Rewrites = mode
		in, err := unitInput(unit, nil, BuildRequest{IR: true})
		if err != nil {
			t.Fatal(err)
		}
		keyed[mode] = in.Emitter
	}
	if keyed[""] != keyed["on"] || keyed["on"] == keyed["off"] || keyed["on"] == keyed["proved"] || keyed["off"] == keyed["proved"] {
		t.Errorf("the module keys by -rewrites: %v; want on alone the default's, and three distinct", keyed)
	}
	ledger := filepath.Join(t.TempDir(), lower.LedgerFile)
	rows := func(step string) string {
		t.Helper()
		sites, err := InspectRewrites(checked(t, src), "/ergo-test/rewrite.ergo", ledger)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		return RewritesText(sites)
	}
	if got := rows("no ledger"); !strings.Contains(got, ":7:27  main.DecEnc  none  missing  not applied") {
		t.Errorf("no ledger: %q; want F's site missing and not applied", got)
	}
	if _, err := UpdateLedger(checked(t, src), BuildRequest{Source: "/ergo-test/rewrite.ergo", Ledger: ledger}); err != nil {
		t.Fatal(err)
	}
	got := rows("validated")
	for _, want := range []string{
		":7:27  main.DecEnc  validated by eval  current  applied\n",
		":9:37  main.DecEnc  validated by eval  current  not applied: AsWritten\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("after the licence run: %q; want a line ending %q", got, want)
		}
	}
}
