package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// withLine is the shipped ledger with key's line replaced by line ("" drops
// it).
func withLine(key, line string) string {
	var out []string
	for _, l := range strings.Split(stdlib.Ledger(), "\n") {
		if strings.HasPrefix(l, "fact "+key+" ") {
			if line != "" {
				out = append(out, line)
			}
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// TestSyncRowsActOnlyValidated is LF2d's admission of a sync row
// (interop.md, Foreign facts 7): the binding of sort.Search, whose
// callback is non-escaping by the row of v1.GoSyncCallbacks, checks clean
// exactly when the std ledger holds the row's guard line current and
// validated; a missing line and a refuted one are diagnostics at the
// binding that say which.
//
// Mutants it alone catches: ffi-sync-unguarded-acts (a sync row acts with
// no guard line), ffi-sync-refuted-acts (a refuted line reads as a
// missing one) and ffi-host-stale-acts (a stale line acts).
func TestSyncRowsActOnlyValidated(t *testing.T) {
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	key := gofacts.SyncKey("sort.Search")
	current, ok := gofacts.Find(stdlib.Ledger(), key)
	if !ok || current.Verdict != "validated" {
		t.Fatalf("the shipped ledger has no validated line for %s: %+v", key, current)
	}
	refuted := current
	refuted.Verdict = "refuted"
	stale := current
	stale.Digest = gofacts.SyncDigest("sort.Search", "an older reason", stale.With)
	for _, c := range []struct {
		name, ledger, want string
	}{
		{"validated", stdlib.Ledger(), ""},
		{"missing", withLine(key, ""), "sort.Search declares a non-escaping Go callback without a reviewed clause that acts (the std ledger has no guard line for " + key},
		{"stale", withLine(key, stale.Line()), "sort.Search declares a non-escaping Go callback without a reviewed clause that acts (the guard line for " + key + " is stale"},
		{"refuted", withLine(key, refuted.Line()), "sort.Search declares a non-escaping Go callback, and its reviewed clause does not hold: its guard refuted it"},
	} {
		ledger := c.ledger
		check.FactLedger = func() string { return ledger }
		set := check.NewGoBindings(nil)
		if !set.ResolveAll("sort") {
			t.Fatal("no binding of sort")
		}
		var got []string
		for _, d := range set.Diagnostics() {
			if strings.Contains(d, "sort.Search ") {
				got = append(got, d)
			}
		}
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("%s: the binding of sort.Search has diagnostics: %q", c.name, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: the binding of sort.Search gives %q, want one diagnostic with %q", c.name, got, c.want)
		}
	}
}
