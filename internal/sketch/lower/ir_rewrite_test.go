package lower

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRewriteAtTakesOneDecision is rewriteAt (LAWFFI r2 §3.6, LF6c on the
// IR path): one decision in three forms, read from LowerIR's IR in process
// (the promised fact of each form is which functions a body calls, so no
// Go is built). The law DecEnc is `Dec(Enc(s)) == s`; F's body is the
// site, H's site has an operand that is a call (Read), G's site is inside
// AsWritten; PickDrops's right side drops its operand b, so P's Read is
// still evaluated, once, though no side reads it. Pick and Id read their
// operand (a[:len(a)]): a call that only moves its operands is computed
// at build time by CF2's template (ir_template.go), which runs before the
// rewrite pass, so P and M would hold no site (the red of 0b9985881's
// merge; M's IdId then hid lf6p-right-side-not-walked).
//
//   - Run build, the laws validated by a named run: F and H call no Enc;
//     H calls Read once; P calls no Pick and Read once. G calls Enc, and
//     its row says AsWritten.
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
// LF6d: the shadow's enc argument (Q's bool has no corpus text), and the
// canary: F's string site compares itself and calls CanaryFail, R's float
// site calls Canary, each site ticks its own counter every N, an
// unapplied site has none. Mutants: lf6d-canary-unlowered,
// lf6d-site-index-shared, lf6d-canary-every-ignored,
// lf6d-canary-go-equality-for-floats, lf6d-canary-default,
// lf6d-enc-ignored. LF6w: St's statement call is no site; mutant
// lf6w-stmt-site. Loop's operand reads s, which the loop writes, so LF3's
// loop memo does not hoist Read out of the condition (I-FLIP).
func TestRewriteAtTakesOneDecision(t *testing.T) {
	const src = `func Enc(s string) string { s + "!" }

func Dec(s string) string { s[:len(s) - 1] }

func Read(s string) string { s + s }

law DecEnc(s string) { Dec(Enc(s)) } == { s }

func F(t string) string { Dec(Enc(t)) }

func G(t string) string { AsWritten(Dec(Enc(t))) }

func H(t string) string { Dec(Enc(Read(t))) }

func Pick(a string, b string) string { a[:len(a)] }

law PickDrops(a string, b string) { Pick(Enc(a), b) } == { Enc(a) }

func P(t string) string { Pick(Enc(t), Read(t)) }

func Neg(b bool) bool { !b }

law NegNeg(b bool) { Neg(Neg(b)) } == { b }

func Q(b bool) bool { Neg(Neg(b)) }

func Half(x float64) float64 { x / 2.0 }

func Dbl(x float64) float64 { x * 2.0 }

law HalfDbl(x float64) { Half(Dbl(x)) } == { x }

func R(x float64) float64 { Half(Dbl(x)) }

func Id(s string) string { s[:len(s)] }

law IdId(s string) { Id(Id(Id(s))) } == { Dec(Enc(s)) }

func M(t string) string { Id(Id(Id(t))) }

func Div(n int, d int, t string) int { n / d + len(Dec(Enc(Read(t)))) }

func Sum(n int, d int, t string) int { n + d + len(Dec(Enc(Read(t)))) }

func And(b bool, t string) bool { b && Dec(Enc(Read(t))) == "" }

func AndLocal(b bool, t string) bool { b && Dec(Enc(t)) == "" }

func Loop(t string) int {
	let mut n = 0
	let mut s = t
	for Dec(Enc(Read(s))) != "" && n < 3 {
		n = n + 1
		s = s + "x"
	}
	n
}

func St(t string) int {
	let _ = Dec(Enc(t))
	1
}

func main() {
	let _ = F("a")
	let _ = St("k")
	let _ = G("b")
	let _ = H("c")
	let _ = P("d")
	let _ = Q(true)
	let _ = R(1.5)
	let _ = M("e")
	let _ = Div(1, 1, "f")
	let _ = Sum(1, 1, "g")
	let _ = And(true, "h")
	let _ = AndLocal(true, "i")
	let _ = Loop("j")
}
`
	keys := func(string) ForeignKey { return ForeignKey{} }
	lowerWith := func(step string, mode string, verdict string, test bool) map[string][]string {
		t.Helper()
		u := factsUnit(t, src)
		u.Rewrites = mode
		entries := map[string]LedgerEntry{}
		for _, law := range u.Package.Laws {
			if law.FuncRewrite == nil {
				t.Fatalf("%s is not in the rewrite's scope", law.Name)
			}
			if verdict != "" {
				e := FuncLawEntry(law, LedgerNames{}, verdict, keys)
				entries[e.Key()] = e
			}
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
	if count(on["P"], "Pick") != 0 || count(on["P"], "Read") != 1 {
		t.Errorf("run build: P calls %v; want no Pick, and Read still evaluated once, though the right side drops it", on["P"])
	}
	if count(on["G"], "Enc") != 1 || len(on["row not applied: AsWritten"]) != 1 {
		t.Errorf("run build: G calls %v, rows %v; want Enc, kept by AsWritten", on["G"], on)
	}
	// The pass (LF6p): a right side another law matches is rewritten too
	// (IdId's Dec(Enc(t)) by DecEnc); a site whose operand would move
	// before code that may fail (Div's division), into a loop's condition
	// (Loop) or out of the right operand of && (And) keeps its left side;
	// with a local operand it needs no statement and rewrites (AndLocal),
	// as with nothing that may fail before it (Sum).
	if len(on["M"]) != 0 {
		t.Errorf("run build: M calls %v; want none: IdId, then DecEnc on its right side", on["M"])
	}
	for _, fn := range []string{"Div", "And", "Loop"} {
		if count(on[fn], "Enc") != 1 || count(on[fn], "Read") != 1 {
			t.Errorf("run build: %s calls %v; want the left side, Enc and Read once", fn, on[fn])
		}
	}
	if n := len(on["row "+rwNoSlot]); n != 3 {
		t.Errorf("run build: %d rows say %q; want 3 (Div, And, Loop)", n, rwNoSlot)
	}
	for _, fn := range []string{"Sum", "AndLocal"} {
		if count(on[fn], "Enc") != 0 {
			t.Errorf("run build: %s calls %v; want no Enc", fn, on[fn])
		}
	}
	// A call in statement position is no site (LF6w): the IR reader takes
	// only a call as an expression statement (toolchain/ir, "an
	// expression statement is a call"), and St's right side is the local t.
	// Dec's value is dropped, so IR-DROP may call its variant with no
	// result, DecDropped (ir_forms_drop.go): still Dec as written. (I-FLIP:
	// LF6w x IR-DROP.)
	if count(on["St"], "Enc") != 1 || count(on["St"], "Dec")+count(on["St"], "DecDropped") != 1 {
		t.Errorf("run build: St calls %v; want Dec(Enc(t)) as written: a statement's call is no site", on["St"])
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
	// The shadow passes the law's arguments and whether each has a corpus
	// text: a string has one, a bool (listable: its text is a class
	// index) none.
	if enc := shadowEnc(t, shadowFiles(t, src), "F"); enc != "true" {
		t.Errorf("test build: F's shadow says enc %s; want true (string s)", enc)
	}
	if enc := shadowEnc(t, shadowFiles(t, src), "Q"); enc != "false" {
		t.Errorf("test build: Q's shadow says enc %s; want false (bool b)", enc)
	}
	// The canary (LF6d): an applied site lowers its right side and, on a
	// tick of its own counter, the left side at the same operands; an
	// unapplied site has no canary.
	canary := lowerWith("canary", "canary=8", "validated", false)
	if count(canary["F"], "Tick") != 1 || count(canary["F"], "CanaryFail") != 1 || count(canary["F"], "Canary") != 0 || count(canary["F"], "Enc") != 1 {
		t.Errorf("canary: F calls %v; want Tick, then the left side's Enc, compared at the site (a string's == is key equality) with CanaryFail", canary["F"])
	}
	if count(canary["R"], "Canary") != 1 || count(canary["R"], "CanaryFail") != 0 {
		t.Errorf("canary: R calls %v; want Canary, key equality, since a float's == is not", canary["R"])
	}
	if count(canary["H"], "Read") != 1 {
		t.Errorf("canary: H calls %v; want Read once: the left side reads the bound operand", canary["H"])
	}
	if count(canary["G"], "Tick") != 0 {
		t.Errorf("canary: G calls %v; want no canary at a site kept as written", canary["G"])
	}
	fTick, hTick := canaryTick(t, src, "F"), canaryTick(t, src, "H")
	if fTick[1] != "8" || hTick[1] != "8" || fTick[0] == hTick[0] {
		t.Errorf("canary: F ticks %v, H ticks %v; want every 8, and a counter per site", fTick, hTick)
	}
	if count(lowerWith("canary, missing entry", "canary", "", false)["F"], "Tick") != 0 {
		t.Errorf("canary: an unapplied site has a canary")
	}
	for in, want := range map[string]uint32{"canary": CanaryEvery, "canary=1": 1, "on": 0} {
		if _, every, err := ParseRewrites(in); err != nil || every != want {
			t.Errorf("ParseRewrites(%q) = %d, %v; want %d", in, every, err, want)
		}
	}
	if _, _, err := ParseRewrites("canary=0"); err == nil {
		t.Errorf("ParseRewrites(canary=0) holds; want an error")
	}
}

// shadowFiles is src's test build, every law's entry missing.
func shadowFiles(t *testing.T, src string) []File {
	t.Helper()
	u := factsUnit(t, src)
	u.Rewrite = NewLedgerEvidence(u.Package, LedgerNames{}, map[string]LedgerEntry{}, func(string) ForeignKey { return ForeignKey{} })
	files, _, err := LowerIRTests(u)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// shadowEnc is the enc argument of fn's Shadow call.
func shadowEnc(t *testing.T, files []File, fn string) string {
	t.Helper()
	c := irPcall(t, files, fn, "Shadow")
	if c == nil || len(c.Args) < 7 {
		t.Fatalf("%s has no Shadow call of eight arguments", fn)
	}
	return c.Args[6].V
}

// canaryTick is the site index and N of fn's Tick call in src's run build
// under -rewrites=canary=8, the laws validated.
func canaryTick(t *testing.T, src, fn string) [2]string {
	t.Helper()
	u := factsUnit(t, src)
	u.Rewrites = "canary=8"
	keys := func(string) ForeignKey { return ForeignKey{} }
	entries := map[string]LedgerEntry{}
	for _, law := range u.Package.Laws {
		e := FuncLawEntry(law, LedgerNames{}, "validated", keys)
		entries[e.Key()] = e
	}
	u.Rewrite = NewLedgerEvidence(u.Package, LedgerNames{}, entries, keys)
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	c := irPcall(t, files, fn, "Tick")
	if c == nil || len(c.Args) != 2 {
		t.Fatalf("%s has no Tick call", fn)
	}
	return [2]string{c.Args[0].V, c.Args[1].V}
}

// irPcall is the first pcall named name in the IR function fn.
func irPcall(t *testing.T, files []File, fn, name string) *irExpr {
	t.Helper()
	var ir irFile
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	var found *irExpr
	var expr func(e *irExpr)
	var stmts func(list []*irStmt)
	expr = func(e *irExpr) {
		if e == nil || found != nil {
			return
		}
		if e.K == "pcall" && e.Name == name {
			found = e
			return
		}
		expr(e.X)
		expr(e.Y)
		for _, a := range e.Args {
			expr(a)
		}
		for _, a := range e.Elems {
			expr(a)
		}
		stmts(e.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				stmts(c.Body)
			}
		}
	}
	for _, f := range ir.Funcs {
		if f.Name == fn || strings.HasSuffix(f.Name, "."+fn) {
			stmts(f.Body)
		}
	}
	return found
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
