package hostguard

import (
	"flag"
	"strings"
	"sync"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestSyncCallbacksHold is the guard of v1.GoSyncCallbacks(): every row has
// a guarded call and passes it, and every guarded call is a row; two
// planted false rows, a function that runs its callback on another
// goroutine and one that keeps it for its next call, are refuted.
//
// Mutants it alone catches: ffi-sync-row-unguarded (a row with no guarded
// call), ffi-sync-guard-goroutine-blind, ffi-sync-guard-one-call.
func TestSyncCallbacksHold(t *testing.T) {
	calls := syncCalls()
	for target, reason := range v1.GoSyncCallbacks() {
		call, ok := calls[target]
		switch {
		case !ok:
			t.Errorf("%s: a row of v1.GoSyncCallbacks() (%q) with no guarded call in syncCalls", target, reason)
		case reason == "":
			t.Errorf("%s: a row with no reason", target)
		default:
			if err := syncGuard(target, call); err != nil {
				t.Error(err)
			}
		}
	}
	for target := range calls {
		if v1.GoSyncCallbacks()[target] == "" {
			t.Errorf("%s: a guarded call with no row in v1.GoSyncCallbacks()", target)
		}
	}
	var kept func()
	for name, c := range map[string]struct {
		call func(p *probe)
		want string
	}{
		"fixture.Async": {func(p *probe) {
			done := make(chan struct{})
			go func() { p.hit(); close(done) }()
			<-done
		}, "a callback ran on another goroutine"},
		"fixture.Keep": {func(p *probe) {
			p.hit()
			if kept != nil {
				kept()
			}
			kept = p.hit
		}, "a callback ran after the call returned"},
	} {
		if err := syncGuard(name, c.call); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("a planted false row %s passes the sync guard: %v, want %q", name, err, c.want)
		}
	}
}

// TestSyncGuardsHoldAlone: each guard of v1.GoSyncCallbacks() gives its
// verdict from a fresh process state, whatever guard ran before it: the
// flag guards each start from a new flag.CommandLine (the global state
// that differs between orders). The std ledger's guard lines must not
// change from one run of stdlaws to the next (LAWFFI LF2d: flag.VisitAll
// was written refuted, then validated).
//
// Mutant it alone catches: ffi-sync-visitall-order (VisitAll's guard
// defines no flag and relies on Visit's having run first; the full-table
// run above still passes when Visit runs first).
func TestSyncGuardsHoldAlone(t *testing.T) {
	saved := flag.CommandLine
	defer func() { flag.CommandLine, probeFlag = saved, sync.Once{} }()
	for target, call := range syncCalls() {
		flag.CommandLine, probeFlag = flag.NewFlagSet("alone", flag.ContinueOnError), sync.Once{}
		if err := syncGuard(target, call); err != nil {
			t.Errorf("run alone: %v", err)
		}
	}
}
