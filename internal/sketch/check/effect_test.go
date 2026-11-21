package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// memoryOnlySrc draws from a Go PCG with no capability row: NewPCG and
// PCG.Uint64 are memory-only rows (GBX6).
const memoryOnlySrc = `import go math/rand/v2

func Draw(seed uint64) uint64 {
	match rand.NewPCG(seed, 7) {
	case Some[go.Ref[rand.PCG]] as p:
		p.Value.Uint64()
	case None:
		0
	}
}
`

// TestMemoryOnlyChargesNothing is GBX6's acceptance (np4f-plan §4.2;
// interop.md, Purity and authority): a function that calls Go's PCG, a
// memory-only row, checks with no capability row, and the call is still
// not pure (its footprint is opaque, so no pure frame and no law treats
// it as a function of its arguments). Red at base: S102, go.Foreign is
// not in Draw's row.
//
// Mutant it alone catches: ffi-effect-pure (a call of an effect row
// joins the pure tier).
func TestMemoryOnlyChargesNothing(t *testing.T) {
	f, err := syntax.Parse(memoryOnlySrc)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("example.test/draw", []*syntax.File{f}, nil)
	for _, d := range ds {
		t.Errorf("want clean, got %s", d)
	}
	if pkg == nil || pkg.Info == nil {
		t.Fatal("no package info")
	}
	seen := 0
	for _, c := range pkg.Info.Calls {
		if c.Func == nil || (c.Func.Name != "Uint64" && c.Func.Name != "NewPCG") {
			continue
		}
		seen++
		if !check.GoEffect(c.Func) || check.PureStd(c.Func) {
			t.Errorf("%s: memory-only %v, pure %v; want memory-only and not pure", c.Func.Name, check.GoEffect(c.Func), check.PureStd(c.Func))
		}
	}
	if seen != 2 {
		t.Errorf("found %d of the two Go calls", seen)
	}
}

// TestEffectRowsActOnlyValidated is GBX6's admission of a memory-only row:
// the binding of math/rand/v2, whose PCG.Uint64 is bound memory-only,
// checks clean exactly when the std ledger holds the row's guard line
// current and validated; a missing line and a refuted one are diagnostics
// at the binding that say which.
//
// Mutants it alone catches: ffi-effect-unguarded-acts (an effect row acts
// with no guard line), ffi-effect-refuted-acts (a refuted line reads
// as a missing one).
func TestEffectRowsActOnlyValidated(t *testing.T) {
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	target := "math/rand/v2.PCG.Uint64"
	key := gofacts.EffectKey(target)
	current, ok := gofacts.Find(stdlib.Ledger(), key)
	if !ok || current.Verdict != "validated" {
		t.Fatalf("the shipped ledger has no validated line for %s: %+v", key, current)
	}
	refuted := current
	refuted.Verdict = "refuted"
	for _, c := range []struct {
		name, ledger, want string
	}{
		{"validated", stdlib.Ledger(), ""},
		{"missing", withLine(key, ""), target + " is bound as memory-only without a reviewed row that acts (the std ledger has no guard line for " + key},
		{"refuted", withLine(key, refuted.Line()), target + " is bound as memory-only, and its reviewed row does not hold: its guard refuted it"},
	} {
		ledger := c.ledger
		check.FactLedger = func() string { return ledger }
		set := check.NewGoBindings(nil)
		if !set.ResolveAll("math/rand/v2") {
			t.Fatal("no binding of math/rand/v2")
		}
		var got []string
		for _, d := range set.Diagnostics() {
			if strings.Contains(d, target+" ") {
				got = append(got, d)
			}
		}
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("%s: the binding of %s has diagnostics: %q", c.name, target, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: the binding of %s gives %q, want one diagnostic with %q", c.name, target, got, c.want)
		}
	}
}
