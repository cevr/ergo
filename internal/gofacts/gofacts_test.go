package gofacts

import (
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
)

// TestVouchedRowsReviewed: each row's Source is the hash of its
// function's declaration in the pinned toolchain's source tree, so a row
// whose function changed fails here until a reviewer looks again and
// writes the new hash.
//
// Mutant it alone catches: ffi-vouch-stale-source (a row keeps an old
// hash).
func TestVouchedRowsReviewed(t *testing.T) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	goroot := strings.TrimSpace(string(out))
	for _, r := range Rows {
		h, err := SourceHash(goroot, r.Target)
		if err != nil {
			t.Fatal(err)
		}
		if r.Reason == "" || h != r.Source {
			t.Errorf("%s %s %s: Source %q, the declaration's hash is %q (reason %q): review the function again", r.Target, r.Fact, r.Param, r.Source, h, r.Reason)
		}
	}
}

// TestVouchedFactsHold runs every row's guard (the std gate runs it under
// -race); and a planted false row, a function that writes its operand, is
// refuted with the case that showed it.
//
// Mutant it alone catches: ffi-guard-blind (the guard compares nothing).
func TestVouchedFactsHold(t *testing.T) {
	for _, r := range Rows {
		if err := r.Guard(1); err != nil {
			t.Errorf("%s: %v", r.Key(), err)
		}
	}
	planted := Row{Target: "fixture.Bump", Fact: "reads", Param: "p", Call: func(_ *rand.Rand, p []byte) {
		if len(p) > 3 {
			p[3]++
		}
	}}
	if err := planted.Guard(1); err == nil || !strings.Contains(err.Error(), "fixture.Bump wrote p at case") {
		t.Errorf("a function that writes its operand passes the guard: %v", err)
	}
}
