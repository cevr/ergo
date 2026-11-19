package lower

import (
	"flag"
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/uiskipgen"
)

var uiSkipSeed = flag.Uint64("uiskip.seed", 0, "run TestUISkipAgrees on this seed alone")

// TestUISkipAgrees is the property of skipping (P-ui; stdlib.md, UI 13):
// a program of generated components, rendered pass after pass over
// edited states with presses between, writes the same tree, the same
// patches and the same signals with component calls lowered as groups as
// with every call run (SetUISkipForTest(false), the reference). The
// components nest, repeat at one site in loops and Each rows, change
// their node counts with their arguments, and some read a flag, so are
// never kept; a press may flip one. Some sit under a Fieldset whose
// disabled state follows the state, in a Same, or in a Boundary whose
// body stops on some states after writing. Two fixed views hold the
// cases a draw seldom reaches: a component whose Same holds a toggle,
// pressed after a pass that skipped the Same, and a component under a
// Fieldset that disables it while its arguments stay, pressed. A component that takes a pointer
// renders a counter the program changes through it, at drawn values: an
// equal pointer must not skip it. A failing seed is replayed with
// -uiskip.seed=N, and the program's text is in the failure.
//
// It also holds that skipping happens, as counted work: a pass that
// returns the same arguments runs fewer components than the first pass,
// and one changed card of n runs one component and Commit compares its
// three nodes alone, at n 8 and 64; the groups kept follow the cards when
// they drop to one (growth); a pass that changes only a header above n
// cards runs none of them and compares no node of theirs, at n 50 and
// 200 (elsewhere, NS1); a card inside a component written again is kept
// with it (nested). After each seed a pass that calls no component holds
// no argument slot (held 0, as the reference holds none). A component
// argument that calls a function counting its calls is evaluated once a
// pass (tallied), and rows whose label is computed are kept when it is
// equal and run once when every label changes, comparing the nodes the
// reference compares (allChange, at 50 and 200). A component whose
// record argument holds a slice it does not read compares the fields it
// reads, through a nested call too (NS2b): generated components read
// subsets of their Item, and subset counts a record kept while its slice
// and an unread field change, and run when a field read through label
// changes. A list holder, cards(f, s.Items), and a record with a nested
// slice, Bag, are groups compared by their structural equality against
// a copy the group keeps (NS2c): each seed's view calls cards over its
// edited, grown, shrunk and shuffled items; cardsGuard counts a header
// change above n cards (no run, the header alone compared, at 50 and 200)
// and one edited card (one run); aliased writes a card in place after a
// pass, which the kept copy must not see.
//
// It builds Go because the subject is the lowered program's behaviour
// over passes and events, which only runs.
//
// Mutants (testdata/mutants.json): ui-group-args-ignored,
// ui-group-impure-kept, ui-group-count-unchecked, ui-group-again-compared,
// ui-group-slots-leak, ui-skip-never, ui-skip-pointer-arg,
// ui-group-off-unchecked, ui-group-same-skip-pure, ui-group-cut-kept,
// ui-group-again-skips, ui-group-fresh-impure-leak, ui-group-cut-slot-leak, ui-group-unsee-short, ui-prior-place-unchecked,
// ui-group-inner-unstamped, ui-group-inner-unlisted, ui-ns2a-arg-twice,
// ui-ns2a-arg-unvalued, ui-ns2a-computed-refused, ui-ns2b-nested-unjoined,
// ui-ns2b-whole-record, ui-ns2b-field-unread, ui-ns2c-off,
// ui-ns2c-same-length, ui-ns2c-keep-aliases, ui-ns2c-fields-unkept,
// ui-prior-place-uncounted, ui-cut-nkids-kept, ui-group-seen-unpassed,
// ui-keep-sweep-always, ui-keep-sweep-never, ui-keep-nheld-drift,
// ui-pending-untaken, ui-pending-each-unchecked, ui-pending-not-alone,
// ui-pending-cut-kept, ui-pending-cut-counts-kept, ui-block-one-node,
// ui-block-unplaced, ui-block-first-top, ui-block-kept-first,
// ui-block-committed-specs, ui-keep-through-never, ui-keep-through-stale,
// ui-keep-inner-uncounted, ui-cut-gives-back-group-counts (gaveBack) (and dropped: ui-group-inner-unstamped; trailed: ui-block-one-node).
func TestUISkipAgrees(t *testing.T) {
	needsBuild(t)
	var seeds []uint64
	for s := range uint64(32) {
		seeds = append(seeds, s+1)
	}
	if *uiSkipSeed != 0 {
		seeds = []uint64{*uiSkipSeed}
	}
	src := uiskipgen.Program(seeds)
	skip := mustLower(t, "uiskip_groups", src)
	restore := SetUISkipForTest(false)
	ref := mustLower(t, "uiskip_reference", src)
	restore()
	exes := buildAll(t, []*lowered{skip, ref}, true)
	if exes == nil {
		return
	}
	out := map[string]string{}
	for _, prog := range []string{"uiskip_groups", "uiskip_reference"} {
		stdout, stderr, code := runExe(t, exes[prog])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s\n%s", prog, code, firstLines(stderr, 8), src)
		}
		out[prog] = stdout
	}
	// The behaviour, without the counted work.
	behaviour := func(s string) []string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "runs ") && !strings.HasPrefix(l, "growth ") && !strings.HasPrefix(l, "elsewhere ") && !strings.HasPrefix(l, "nested ") && !strings.HasPrefix(l, "allcount ") && !strings.HasPrefix(l, "subset ") && !strings.HasPrefix(l, "cards ") && !strings.HasPrefix(l, "looped ") && !strings.HasPrefix(l, "cut ") && !strings.HasPrefix(l, "shrunk ") && !strings.HasPrefix(l, "dropped ") && !strings.HasPrefix(l, "trailed ") {
				keep = append(keep, l)
			}
		}
		return keep
	}
	got, want := behaviour(out["uiskip_groups"]), behaviour(out["uiskip_reference"])
	seed := ""
	for i := 0; i < len(got) || i < len(want); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if strings.HasPrefix(w, "seed ") {
			seed = w
		}
		if g != w {
			t.Fatalf("%s, line %d: groups %q, reference %q (replay with -uiskip.seed=N)\n%s", seed, i, g, w, src)
		}
	}
	// Counted work: the first pass of each seed against the passes after.
	var first, later, seeds0 int
	var growth, elsewhere []string
	for _, l := range strings.Split(out["uiskip_groups"], "\n") {
		switch {
		case strings.HasPrefix(l, "seed "):
			seeds0 = 0
		case strings.HasPrefix(l, "runs "):
			var n int
			fmt.Sscanf(l, "runs %d", &n)
			if seeds0 == 0 {
				first += n
			} else {
				later += n
			}
			seeds0++
		case strings.HasPrefix(l, "growth "):
			growth = append(growth, l)
		case strings.HasPrefix(l, "elsewhere "), strings.HasPrefix(l, "nested "), strings.HasPrefix(l, "allcount "), strings.HasPrefix(l, "subset "), strings.HasPrefix(l, "cards "), strings.HasPrefix(l, "looped "), strings.HasPrefix(l, "cut "), strings.HasPrefix(l, "shrunk "), strings.HasPrefix(l, "dropped "), strings.HasPrefix(l, "trailed "):
			elsewhere = append(elsewhere, l)
		}
	}
	if first == 0 || later >= first*9 {
		t.Errorf("the groups build ran %d components in first passes and %d in the 9 later ones of each seed: no component was skipped", first, later)
	}
	if want := []string{"growth 8 8 1 3 0 1", "growth 64 64 1 3 0 1"}; strings.Join(growth, "|") != strings.Join(want, "|") {
		t.Errorf("growth: %q, want %q (n, first runs, runs after one change, nodes compared then, Sames skipped then, groups kept at one card)", growth, want)
	}
	// NS1: a change elsewhere than the rows runs no row and compares the
	// header and its sibling alone, at 50 rows as at 200.
	// A group written again keeps the groups inside it: the card in framed
	// is kept through it, so framed alone runs when its tag changes.
	// When every row's computed argument changes, each row runs once and
	// Commit compares what the reference compares (allchange, a behaviour
	// line, equal under both builds); a later pass that changes the header
	// alone runs none of them (allcount, NS1 and NS2a).
	// NS2b: a record argument with a slice is a group of the fields its
	// component reads: kept while only the slice or an unread field
	// changes, run when a field read through a nested call changes.
	if want := []string{"elsewhere 50 0 2", "elsewhere 200 0 2", "nested 1", "allcount 50 50 50", "allcount 50 header 0 2", "allcount 200 200 200", "allcount 200 header 0 2", "subset 0 1", "subset 0 1", "subset 1 1", "cards 50 tick 0 1 0 0", "cards 50 edit 1 0", "cards 200 tick 0 1 0 0", "cards 200 edit 1 0", "looped 0", "cut 1", "shrunk 50 50 0", "dropped 50 52 1", "trailed 50 0"}; strings.Join(elsewhere, "|") != strings.Join(want, "|") {
		t.Errorf("counted work: %q, want %q (n, components run, nodes compared; components run in nested; components run and groups kept when every row changed, then after a header change; components run and groups kept in subset)", elsewhere, want)
	}
}
