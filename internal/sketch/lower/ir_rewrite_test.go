package lower

import (
	"encoding/json"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRewriteAtTakesOneDecision is rewriteAt (LAWFFI r2 §3.6, LF6c on the
// IR path): one decision in three forms, read from LowerIR's IR in process
// (the promised fact of each form is which functions a body calls, so no
// Go is built). The law DecEnc is `Dec(Enc(s)) == s`; F's body is the
// site, H's site has an operand that is a call (Read), G's site is inside
// AsWritten.
//
//   - Run build, the law validated by a named run: F and H call no Enc;
//     H calls Read once. G calls Enc, and its row says AsWritten.
//   - The reference, -rewrites=off, -rewrites=proved (the law is only
//     validated) and a missing entry: F calls Enc.
//   - Test build: F calls v1.Shadow, even with no ledger; G does not.
//
// Mutants: lf6c-ir-rewrite-unapplied (rewriteAt never rewrites),
// lf6c-ir-admission-ignored (a site is rewritten without admission),
// lf6c-ir-proved-ignored (-rewrites=proved admits a validated law),
// lf6c-ir-off-ignored, lf6c-ir-aswritten-ignored, lf6c-ir-shadow-skipped
// (the test build lowers the left side), lf6c-ir-operand-twice (the
// operand is lowered again where the side reads it, not bound once).
func TestRewriteAtTakesOneDecision(t *testing.T) {
	const src = `func Enc(s string) string { s + "!" }

func Dec(s string) string { s[:len(s) - 1] }

func Read(s string) string { s + s }

law DecEnc(s string) { Dec(Enc(s)) } == { s }

func F(t string) string { Dec(Enc(t)) }

func G(t string) string { AsWritten(Dec(Enc(t))) }

func H(t string) string { Dec(Enc(Read(t))) }

func main() {
	let _ = F("a")
	let _ = G("b")
	let _ = H("c")
}
`
	keys := func(string) ForeignKey { return ForeignKey{} }
	lowerWith := func(step string, mode string, verdict string, test bool) map[string][]string {
		t.Helper()
		u := factsUnit(t, src)
		u.Rewrites = mode
		var law *check.Law
		for _, l := range u.Package.Laws {
			if l.Name == "DecEnc" {
				law = l
			}
		}
		if law == nil || law.FuncRewrite == nil {
			t.Fatalf("DecEnc is not in the rewrite's scope")
		}
		entries := map[string]LedgerEntry{}
		if verdict != "" {
			e := FuncLawEntry(law, LedgerNames{}, verdict, keys)
			entries[e.Key()] = e
		}
		ev := NewLedgerEvidence(u.Package, LedgerNames{}, entries, keys)
		u.Rewrite = ev
		lower := LowerIR
		if test {
			lower = LowerIRTests
		}
		files, rep, err := lower(u)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		calls := irCalls(t, files)
		for _, fn := range []string{"F", "G", "H"} {
			if _, ok := calls[fn]; !ok {
				t.Fatalf("%s: %s has no IR body (fallbacks %v)", step, fn, rep.Fallback)
			}
		}
		if !test {
			for _, s := range ev.Sites() {
				calls["row "+s.Why] = append(calls["row "+s.Why], s.Law)
			}
		}
		return calls
	}
	count := func(calls []string, name string) int {
		n := 0
		for _, c := range calls {
			if c == name {
				n++
			}
		}
		return n
	}
	on := lowerWith("run build", "", "validated", false)
	if count(on["F"], "Enc") != 0 || count(on["H"], "Enc") != 0 {
		t.Errorf("run build: F calls %v, H calls %v; want no Enc", on["F"], on["H"])
	}
	if count(on["H"], "Read") != 1 {
		t.Errorf("run build: H calls %v; want Read once", on["H"])
	}
	if count(on["G"], "Enc") != 1 || len(on["row not applied: AsWritten"]) != 1 {
		t.Errorf("run build: G calls %v, rows %v; want Enc, kept by AsWritten", on["G"], on)
	}
	saved := rewriteLowering
	rewriteLowering = "reference"
	ref := lowerWith("reference", "", "validated", false)
	rewriteLowering = saved
	for step, calls := range map[string]map[string][]string{
		"reference":        ref,
		"-rewrites=off":    lowerWith("-rewrites=off", "off", "validated", false),
		"-rewrites=proved": lowerWith("-rewrites=proved", "proved", "validated", false),
		"missing entry":    lowerWith("missing entry", "", "", false),
	} {
		if count(calls["F"], "Enc") != 1 {
			t.Errorf("%s: F calls %v; want Enc, the left side", step, calls["F"])
		}
	}
	shadow := lowerWith("test build", "", "", true)
	if count(shadow["F"], "Shadow") != 1 || count(shadow["G"], "Shadow") != 0 {
		t.Errorf("test build: F calls %v, G calls %v; want F shadowed and G not", shadow["F"], shadow["G"])
	}
}

// irCalls are the names each IR function calls (call and pcall), by its
// name, in the order the body holds them.
func irCalls(t *testing.T, files []File) map[string][]string {
	t.Helper()
	var ir irFile
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := map[string][]string{}
	var expr func(name string, e *irExpr)
	var stmts func(name string, list []*irStmt)
	expr = func(name string, e *irExpr) {
		if e == nil {
			return
		}
		if e.K == "call" || e.K == "pcall" {
			out[name] = append(out[name], e.Name)
		}
		expr(name, e.X)
		expr(name, e.Y)
		for _, a := range e.Args {
			expr(name, a)
		}
		for _, a := range e.Elems {
			expr(name, a)
		}
		stmts(name, e.Body)
	}
	stmts = func(name string, list []*irStmt) {
		for _, s := range list {
			expr(name, s.X)
			expr(name, s.Y)
			stmts(name, s.Then)
			stmts(name, s.Else)
			stmts(name, s.Body)
			for _, c := range s.Cases {
				stmts(name, c.Body)
			}
		}
	}
	for _, f := range ir.Funcs {
		name := f.Name
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		out[name] = append(out[name][:0:0], out[name]...)
		stmts(name, f.Body)
	}
	return out
}
