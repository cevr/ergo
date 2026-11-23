//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/hosts/opentui/ot"
)

// statuses are the library's status codes a check fails with.
var statuses = []ot.Status{ot.StatusInvalidArgument, ot.StatusOutOfMemory, ot.StatusStaleHandle, ot.StatusOutputBackpressure,
	ot.StatusSessionClosed, ot.StatusOutputFailed, ot.StatusObjectLimit, ot.StatusLayoutLimit, ot.StatusInternalError}

// failScript makes a screen and plays every call that can fail on it
// (scene commands of each kind, frames, an edit, a reveal and a scroll, a
// notification, a resize, a call on a closed screen), then quits. It
// stops at the first error and answers it, with the calls made.
func failScript() (err error, said []string) {
	s, err := Headless(24, 8)
	if err != nil {
		return err, []string{"Headless"}
	}
	step := func(name string, f func() error) bool {
		if err != nil {
			return false
		}
		said = append(said, name)
		err = f()
		return err == nil
	}
	defer func() {
		said = append(said, "Quit")
		if qerr := s.Quit(); err == nil {
			err = qerr
		}
	}()
	s.Create(1, Box)
	s.Move(1, 0, 0)
	s.Create(2, Text)
	s.Move(2, 1, 0)
	s.SetText(2, "one two")
	s.Create(3, Lister)
	s.Move(3, 1, 1)
	for n := 4; n < 9; n++ {
		s.Create(n, Text)
		s.SetText(n, fmt.Sprintf("row %d", n))
		s.Move(n, 3, n-4)
	}
	s.Create(9, Editor)
	s.SetPlaceholder(9, "name")
	s.Move(9, 1, 2)
	s.SetLook(2, 0xff0000ff, 0, Bold)
	s.Focus(9)
	step("Present", s.Present)
	step("Edit", func() error { _, e := s.Edit(9, EditInsert, 0, "bob"); return e })
	step("Reveal", func() error { return s.Reveal(8) })
	step("ScrollTo", func() error { return s.ScrollTo(3, 1, true) })
	step("Notify", func() error { return s.Notify("saved") })
	step("Resize", func() error { return s.Resize(20, 7) })
	s.Destroy(4)
	step("Present", s.Present)
	step("Next", func() error {
		s.EndInput()
		_, err := s.Next(context.Background())
		return err
	})
	return err, said
}

// TestScreenErrorsAreFailures: every error the screen returns is a
// *Failure that keeps the call, the node and the library's status. The
// script runs once to count its checks, then once for each check made to
// fail with a seeded status: the error the script stops at is a *Failure
// itself (not one wrapped), with the call and node of that check,
// Status() the status, and errors.Is reaches the ot.Status through it.
// Every screen is quit on every path (Live). The script's own failure (a
// ScrollTo of a node that does not scroll) and a call on a closed screen
// are Failures too. A failing seed prints the check, the calls made and
// -screen.seed=N.
//
// Mutants (testdata/mutants.json): OTF-chain-cut, OTF-trace-always-on.
func TestScreenErrorsAreFailures(t *testing.T) {
	if err, _ := failScript(); err != nil || len(Checks()) != 0 {
		t.Fatalf("a screen with no fault tracing: error %v, trace %v", err, Checks())
	}
	FailAt(0, AnyCall, 0)
	err, _ := failScript()
	checks := Checks()
	if err != nil {
		t.Fatalf("the script fails with no check failed: %v", err)
	}
	seen := map[int]bool{}
	for _, c := range checks {
		seen[c.Call] = true
	}
	// Setup is covered by the pty property; Closed is a Go-only cause.
	for call := CallOpen; call <= CallQuit; call++ {
		if call != CallSetup && !seen[call] {
			t.Errorf("the script did not exercise call %d: %v", call, checks)
		}
	}
	live := Live()
	for _, sd := range seeds(t, 3) {
		r := rand.New(rand.NewPCG(uint64(sd), 0xfa11))
		for at := 1; at <= len(checks); at++ {
			st := statuses[r.IntN(len(statuses))]
			FailAt(at, AnyCall, int(st))
			err, said := failScript()
			FailAt(0, AnyCall, 0)
			c := checks[at-1]
			f, ok := err.(*Failure)
			switch {
			case !ok:
				t.Errorf("seed %d, check %d (%+v): the error is %T, not a *Failure: %v", sd, at, c, err, err)
			case f.Call != c.Call || f.Node != c.Node:
				t.Errorf("seed %d, check %d: the Failure names call %d node %d, the check was call %d node %d: %v", sd, at, f.Call, f.Node, c.Call, c.Node, err)
			case f.Status() != int(st) || !errors.Is(err, st):
				t.Errorf("seed %d, check %d (%+v): Status() %d, errors.Is %v; failed with %d: %v", sd, at, c, f.Status(), errors.Is(err, st), st, err)
			default:
				continue
			}
			t.Logf("calls: %s\nreplay: go test -run TestScreenErrorsAreFailures -screen.seed=%d", strings.Join(said, ", "), sd)
			return
		}
	}
	if Live() != live {
		t.Errorf("%d screens left unquit", Live()-live)
	}
	s, err := Headless(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	s.Create(1, Text)
	s.Move(1, 0, 0)
	if err := s.Present(); err != nil {
		t.Fatal(err)
	}
	// No node 1 scrolls: a failure of the screen's own, with no status.
	err = s.ScrollTo(1, 1, false)
	if f, ok := err.(*Failure); !ok || f.Call != CallScrollTo || f.Node != 1 || f.Status() != 0 {
		t.Errorf("ScrollTo of a node that does not scroll: %T %v", err, err)
	}
	if err := s.Quit(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"Present": s.Present(), "Notify": s.Notify("x")} {
		if f, ok := err.(*Failure); !ok || f.Call != CallClosed || f.Status() != 0 || !errors.Is(err, ErrClosed) {
			t.Errorf("%s on a closed screen: %T %v", name, f, err)
		}
	}
}
