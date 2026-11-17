package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/runtime/v1/shadow"
)

// TestShadowRefutationJoinsTheLedger is ergo test's writer of a rewrite
// shadow's refutation (LF6d; LAWFFI r2 §3.6 "ergo test writes the
// refutation of §3.4"), in process. DecEnc is false only at "zq-17", which
// no licence class or draw reaches, so the licence run validates it; a
// test's shadow then finds it false at a site:
//
//   - the runtime's line parses back to the law and its case;
//   - RecordRefutations makes the entry `refuted by shadow` at the
//     sources' digest and adds the case to ergo.cases;
//   - the same refutation again changes nothing;
//   - a later licence run of the unchanged sources keeps the refutation
//     (its entry is current), so a passing ergo test does not hide it;
//   - a law the module does not declare is named and not written.
//
// Mutants: lf6d-refutation-unwritten (RecordRefutations writes nothing),
// lf6d-refutation-case-dropped (the case does not join ergo.cases),
// lf6d-shadow-line-unparsed (a refutation line is not read),
// lf6d-refutation-by-eval (the entry names the licence run, not the
// shadow).
func TestShadowRefutationJoinsTheLedger(t *testing.T) {
	const src = "func Enc(s string) string { s + \"!\" }\n\nfunc Dec(s string) string {\n\tif s == \"zq-17!\" {\n\t\treturn \"other\"\n\t}\n\ts[:len(s) - 1]\n}\n\nlaw DecEnc(s string) { Dec(Enc(s)) } == { s }\n"
	dir := t.TempDir()
	ledger := filepath.Join(dir, lower.LedgerFile)
	req := func(stderr *strings.Builder) BuildRequest {
		return BuildRequest{Source: "/ergo-test/shadowref.ergo", Ledger: ledger, Stderr: stderr}
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	a := checked(t, src)
	if _, err := UpdateLedger(a, req(&strings.Builder{})); err != nil || !strings.Contains(read(ledger), ".DecEnc validated by eval ") {
		t.Fatalf("licence run: %v, ledger %q; want DecEnc validated", err, read(ledger))
	}
	key := lower.FuncLawEntry(a.pkg.Laws[0], ledgerNames(a), "", foreignKeys(a, nil)).Key()
	r, ok := ParseShadowLine(shadow.RefutedPrefix + key + " refuted (\"zq-17\")\n")
	if !ok || r.Law != key || r.Case != `("zq-17")` {
		t.Fatalf("ParseShadowLine: %+v, %v; want law %s and its case", r, ok, key)
	}
	if bare, ok := ParseShadowLine(shadow.RefutedPrefix + key + " refuted"); !ok || bare.Case != "" {
		t.Errorf("ParseShadowLine of a line with no case: %+v, %v", bare, ok)
	}
	if _, ok := ParseShadowLine("panic: law " + key + " is false"); ok {
		t.Errorf("ParseShadowLine read a defect's text as a refutation line")
	}
	var stderr strings.Builder
	if n, err := RecordRefutations(a, req(&stderr), []ShadowRefutation{r}); err != nil || n == 0 {
		t.Fatalf("RecordRefutations: %d, %v; want a change", n, err)
	}
	if l := read(ledger); !strings.Contains(l, key+" refuted by shadow h1:") {
		t.Errorf("ledger %q; want %s refuted by shadow", l, key)
	}
	if c := read(filepath.Join(dir, CorpusFile)); c != "law "+key+" (\"zq-17\")\n" {
		t.Errorf("ergo.cases %q; want the shadow's case", c)
	}
	if n, err := RecordRefutations(a, req(&strings.Builder{}), []ShadowRefutation{r}); err != nil || n != 0 {
		t.Errorf("the same refutation again: %d, %v; want no change", n, err)
	}
	if n, err := UpdateLedger(a, req(&strings.Builder{})); err != nil || n != 0 || !strings.Contains(read(ledger), " refuted by shadow ") {
		t.Errorf("licence run after the refutation: %d changed, %v, ledger %q; want the refutation kept", n, err, read(ledger))
	}
	stderr.Reset()
	if n, err := RecordRefutations(a, req(&stderr), []ShadowRefutation{{Law: "strconv.UnquoteInvertsQuote"}}); err != nil || n != 0 || !strings.Contains(stderr.String(), "its own ledger holds it") {
		t.Errorf("a std law: %d, %v, %q; want it named and not written", n, err, stderr.String())
	}
}
