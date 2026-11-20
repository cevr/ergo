package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIROwnedListMatchSource is the seeded property over frames that open
// a scope only for their owned values and keep them in the list form
// (OWNED-LOCAL, GenerateOwnList): for each seed the IR path and the
// source path print the same opens, uses, hand-offs and releases (each
// with the field written in place), in the same order, on early returns,
// a failure and a defect at a release; up to the program's mark every
// value opened is closed exactly once (a count per id, so a value both
// paths release twice or never fails too); and taker, failing and
// ol0..ol2 (every seed writes at least three) have IR bodies. A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the question is what the built program releases,
// with which writes, in which order, on which exit, which only a run
// shows.
//
// Mutants: ownlist-owning-at-dropped (listLet writes no v1.OwningAt, so a
// written local is released with its old fields), ownlist-param-dropped
// (openScope registers no taken parameter, so taker's r is never
// released), ownlist-plain-off (irFrame keeps a plain list frame's source:
// the olN have no IR body).
func TestIROwnedListMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	want := append([]string{"ol0", "ol1", "ol2"}, ownListFuncs...)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateOwnList(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIROwnedListMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if bad := unpaired(b.stdout); bad != "" {
			t.Fatalf("seed %d: %s\nir: %+v\n%s\n%s", seed, bad, b, replay, text)
		}
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, text)
			}
		}
	}
}

// unpaired is "" when, up to ownListMark, each id printed by "open ID" is
// printed as many times by "close ID n=…", or the first id that is not.
func unpaired(out string) string {
	before, _, found := strings.Cut(out, ownListMark)
	if !found {
		return "the program never printed its mark"
	}
	opens, closes := map[string]int{}, map[string]int{}
	var ids []string
	for _, line := range strings.Split(before, "\n") {
		if id, ok := strings.CutPrefix(line, "open "); ok {
			if opens[id] == 0 {
				ids = append(ids, id)
			}
			opens[id]++
		} else if rest, ok := strings.CutPrefix(line, "close "); ok {
			id, _, _ := strings.Cut(rest, " ")
			closes[id]++
		}
	}
	for id := range closes {
		if opens[id] == 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if opens[id] != closes[id] {
			return "value " + id + " opened " + itoa(uint64(opens[id])) + " times, closed " + itoa(uint64(closes[id]))
		}
	}
	return ""
}
