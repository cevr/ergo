package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStageOneTakesTheParserThatReadsTheTree: a new token that the
// prelude itself uses (a type set's line) and a std package uses too.
// The linked parser on disk refuses both lines (refused names both
// files). Holding back the prelude's line would take its declaration with
// it, so stage 1 regenerates with the candidate parser that refuses
// fewer lines (a merge's other side, which carries the syntax), never
// holding back what a parser that reads it could parse. choose is
// checked against a reference model (filter to the layouts the decoder
// reads, then a stable sort by refused lines) over seeded candidate lists;
// ERGO_SEED=<n> replays one seed.
//
// Mutants: linked-choose-ignores-refusals (the first readable candidate
// wins), linked-choose-unreadable-layout (a candidate whose layout the
// decoder does not read is chosen).
func TestStageOneTakesTheParserThatReadsTheTree(t *testing.T) {
	root := t.TempDir()
	std := filepath.Join(root, "internal", "sketch", "std")
	for _, dir := range []string{filepath.Join(std, "p"), filepath.Join(root, "compiler")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prelude := filepath.Join(std, "prelude.ergo")
	user := filepath.Join(std, "p", "p.ergo")
	for path, src := range map[string]string{
		prelude: "type small {\n\t⊕int | ⊕int8,\n}\n",
		user:    "func twice[T small](x T) T {\n\tx ⊕ x\n}\n",
	} {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	held, err := refused(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := held[prelude]; !ok || len(held) != 2 || refusedLines(held) != 2 {
		t.Fatalf("held %d files, %d lines; want the prelude and p, one line each", len(held), refusedLines(held))
	}
	ps := []probed{
		{from: "the disk", layout: syntax.ColumnsVersion, refused: refusedLines(held)},
		{from: "MERGE_HEAD", layout: syntax.PreviousColumnsVersion, refused: 0},
	}
	if got := choose(ps); got != 1 {
		t.Errorf("choose picked %d (%s); want 1, the parser that reads the prelude's new token", got, ps[got].from)
	}

	seed := uint64(1)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	r := rand.New(rand.NewPCG(seed, 0))
	layouts := []uint32{syntax.PreviousColumnsVersion, syntax.ColumnsVersion, syntax.ColumnsVersion + 1, 0}
	for c := range 500 {
		ps := make([]probed, 1+r.IntN(4))
		for i := range ps {
			ps[i] = probed{from: strconv.Itoa(i), layout: layouts[r.IntN(len(layouts))], refused: r.IntN(5) - 1}
		}
		if got, want := choose(ps), chooseModel(ps); got != want {
			t.Fatalf("case %d of seed %d: choose(%v) = %d, the model %d\nreplay: ERGO_SEED=%d go test -run TestStageOneTakesTheParserThatReadsTheTree ./scripts/linked", c, seed, ps, got, want, seed)
		}
	}
}

// chooseModel is choose's reference: the candidates that answered with a
// layout the decoder reads, stably sorted by the lines they refuse; the
// first, or -1.
func chooseModel(ps []probed) int {
	var ok []int
	for i, p := range ps {
		if p.refused >= 0 && (p.layout == syntax.ColumnsVersion || p.layout == syntax.PreviousColumnsVersion) {
			ok = append(ok, i)
		}
	}
	slices.SortStableFunc(ok, func(a, b int) int { return ps[a].refused - ps[b].refused })
	if len(ok) == 0 {
		return -1
	}
	return ok[0]
}

func (p probed) String() string { return fmt.Sprintf("%s:%d/%d", p.from, p.layout, p.refused) }
