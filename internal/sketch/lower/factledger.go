package lower

import (
	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
)

// The level of a foreign fact at a Go call (LAWFFI r2 §1; LF2). A derived
// fact is checked and acts. A vouched fact, a row of the reviewed table
// internal/gofacts that `ergo bind go` writes into the binding as
// `// ergo:reads p (vouched: reason)`, acts only when the shipped ledger
// holds its guard's line, `fact go <target> reads <param> validated by
// guard with go1.27.4 h1:…`, current: the digest is the one the row (its
// reviewed source hash and reason) and the pinned toolchain give
// (gofacts.Row.Digest). Then the fact is validated, a value-class change
// admitLevel admits. A missing or stale line keeps the copy, and says why
// in the copy report and ergo inspect facts. A current refuted line stops
// the build at the call: the table vouches for a function its own guard
// showed writing the operand.

// factLevel is the level of fact f of the Go function fn at the call n,
// and, for a fact that does not act, why. It stops the build on a current
// refuted guard line.
func factLevel(n syntax.Node, fn *check.Func, f check.ForeignFact) (level, why string) {
	switch f.Level {
	case check.FactChecked:
		return check.FactChecked, ""
	case check.FactVouched:
	default:
		return f.Level, "a " + f.Level + " fact changes no code"
	}
	var row *gofacts.Row
	for _, r := range gofacts.Of(goTarget(fn)) {
		if r.Key() == f.Law {
			row = &r
			break
		}
	}
	if row == nil {
		return check.FactVouched, "vouched by the binding, and the reviewed table has no row " + f.Law + ": regenerate the binding"
	}
	entries, err := ParseLedger(shippedLedger())
	if err != nil {
		ice(nil, "the shipped law ledger: %v", err)
	}
	entry, found := entries["fact "+f.Law]
	switch {
	case !found:
		return check.FactVouched, "vouched, and the ledger has no guard line for " + f.Law + ": run go run ./scripts/stdlaws"
	case entry.Digest != row.Digest(toolchain.Version) || entry.With != toolchain.Version:
		return check.FactVouched, "vouched, and the guard line for " + f.Law + " is stale (the row, its reviewed source or the toolchain changed): run go run ./scripts/stdlaws"
	case entry.Verdict == "refuted":
		stop(n, goTarget(fn)+" is vouched to only read "+f.Param+", and its guard refuted it (ergo.laws: fact "+f.Law+" refuted by "+entry.By+"): the build stops; correct or remove the row in internal/gofacts, then run go run ./scripts/stdlaws")
	case !admitLevel(entry.Verdict, entry.By, valueClass):
		return check.FactVouched, "vouched, and the guard line for " + f.Law + " is " + entry.Verdict + ": only a validated guard line with its run named lets a vouched fact act"
	}
	return check.FactValidated, ""
}

// goReads reports the Go call n's operand i (a method's receiver first) as
// one fn only reads by a fact that acts: checked, or vouched and validated
// (factLevel). why is the last reason a fact on the operand did not act.
func goReads(n syntax.Node, fn *check.Func, operand int) (ok bool, why string) {
	for _, f := range check.GoFacts(fn) {
		if f.Kind != "reads" || f.Operand != operand {
			continue
		}
		level, w := factLevel(n, fn, f)
		if level == check.FactChecked || level == check.FactValidated {
			return true, ""
		}
		why = w
	}
	return false, why
}
