package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketchfront"
)

// TestUnionFlatWritesTheListing (FIX-UNION-FLAT, DECL-VALUES §6 unit 2)
// is a seeded property of the union pass. Each seed writes one program
// twice: the old spelling, a union B that re-lists, in a row, the
// variants of a union A (some declared in place in B, some apart), a
// function that widens A into B arm by arm, and a function whose tail
// match over B hands A's variants out one identity arm each, in any order;
// and the new spelling, B listing A with the variants declared in place
// moved into A, the widening `{ s }` and the arms one `case A as v: v`.
// Two kinds of seed must be left as written: B writing A's variants out
// of A's order keeps its entries (its arms still collapse: A is a subset
// of B in any order), and an arm that does more than hand out its value
// (here, a call on its binder) keeps the match's arms. Then: fix(old) is byte-equal to the new
// spelling, a second fix writes nothing, and both spellings check clean.
// ERGO_SEED=<n> replays one seed; a failure prints both programs.
//
// Mutants it kills that no other test does (ledger: fix): uflat-fix-order
// (a row of A's variants out of order taken as A), uflat-fix-rebuild-arm (an
// arm with more than its binder taken as identity), uflat-fix-no-move (a
// variant declared in place in B left behind).
func TestUnionFlatWritesTheListing(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(60) {
			seeds = append(seeds, i+1)
		}
	}
	kinds := map[string]int{}
	for _, seed := range seeds {
		oldSrc, newSrc, reached := flatProgram(rand.New(rand.NewPCG(seed, 0x5f1a7)))
		for _, k := range reached {
			kinds[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestUnionFlatWritesTheListing$' ./internal/sketch/fix\nold:\n%s\nnew:\n%s", seed, fmt.Sprintf(format, args...), seed, oldSrc, newSrc)
		}
		out, _, err := fix.UnionFlat(map[string]string{"p.ergo": oldSrc})
		if err != nil {
			fail("fix: %v", err)
		}
		if out["p.ergo"] != newSrc {
			fail("fix(old) is not the new spelling:\n%s", out["p.ergo"])
		}
		again, changes, err := fix.UnionFlat(map[string]string{"p.ergo": newSrc})
		if err != nil || again["p.ergo"] != newSrc || len(changes) > 0 {
			fail("a second fix writes %v (%v):\n%s", changes, err, again["p.ergo"])
		}
		for name, src := range map[string]string{"old": oldSrc, "new": newSrc} {
			if ds := sketchfront.Check(src).Diagnostics; len(ds) > 0 {
				fail("the %s spelling does not check clean: %v", name, ds)
			}
		}
	}
	if len(seeds) > 1 {
		for _, k := range []string{"relisted", "moved", "widened", "arms", "out-of-order", "rebuild-arm"} {
			if kinds[k] == 0 {
				t.Errorf("no seed reached %s (%v)", k, kinds)
			}
		}
	}
}

// flatProgram is one seed's program in the old spelling and the new, and
// the kinds of rewrite (or of left-alone spelling) it holds.
func flatProgram(r *rand.Rand) (oldSrc, newSrc string, reached []string) {
	nP, nA, nS := r.IntN(3), 2+r.IntN(2), r.IntN(3)
	if nP+nS == 0 {
		nS = 1
	}
	type variant struct {
		name   string
		inline bool
		fields string // "" or "N int"
	}
	mk := func(prefix string, n int) []variant {
		var out []variant
		for i := range n {
			v := variant{name: fmt.Sprintf("%s%d", prefix, i), inline: r.IntN(2) == 0}
			if !v.inline || r.IntN(2) == 0 { // a record apart has a field: `{}` alone is no record
				v.fields = "N int"
			}
			out = append(out, v)
		}
		return out
	}
	ps, as, ss := mk("P", nP), mk("V", nA), mk("S", nS)
	outOfOrder := r.IntN(4) == 0
	rebuildArm := r.IntN(4) == 0
	entry := func(v variant) string {
		if v.inline {
			return v.name + "{" + v.fields + "}"
		}
		return v.name
	}
	join := func(vs []variant, f func(variant) string) string {
		var parts []string
		for _, v := range vs {
			parts = append(parts, f(v))
		}
		return strings.Join(parts, " | ")
	}
	written := append([]variant{}, as...) // A's variants as B writes them
	if outOfOrder {
		written[0], written[1] = written[1], written[0]
	}
	all := append(append(append([]variant{}, ps...), written...), ss...)
	var apart strings.Builder // the variants declared apart
	for _, v := range all {
		if !v.inline {
			fmt.Fprintf(&apart, "type %s { %s }\n\n", v.name, v.fields)
		}
	}
	// The arms of keep: P and S rebuilt through a let, A's identity in a
	// shuffled order, one of them a rebuild when rebuildArm.
	shuffled := append([]variant{}, as...)
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	other := func(v variant) string { return "\tcase " + v.name + " as x:\n\t\tlet y = x\n\t\ty\n" }
	var aArms strings.Builder
	for i, v := range shuffled {
		b := strings.ToLower(v.name)
		if rebuildArm && i == len(shuffled)-1 {
			aArms.WriteString("\tcase " + v.name + " as " + b + ":\n\t\tsame(" + b + ")\n") // a call on the binder, not the binder
			continue
		}
		aArms.WriteString("\tcase " + v.name + " as " + b + ":\n\t\t" + b + "\n")
	}
	keep := func(aPart string) string {
		var b strings.Builder
		b.WriteString("func keep(b B) B {\n\tmatch b {\n")
		for _, v := range ps {
			b.WriteString(other(v))
		}
		b.WriteString(aPart)
		for _, v := range ss {
			b.WriteString(other(v))
		}
		b.WriteString("\t}\n}\n\nfunc same(b B) B { b }\n")
		return b.String()
	}
	var widenOld strings.Builder
	widenOld.WriteString("func widen(s A) B {\n\tmatch s {\n")
	for _, v := range shuffled {
		b := strings.ToLower(v.name)
		widenOld.WriteString("\tcase " + v.name + " as " + b + ":\n\t\t" + b + "\n")
	}
	widenOld.WriteString("\t}\n}\n")

	oldSrc = "type B = " + join(all, entry) + "\n\ntype A = " + join(as, func(v variant) string { return v.name }) + "\n\n" +
		apart.String() + widenOld.String() + "\n" + keep(aArms.String())

	// The new spelling.
	var bNew, aNew string
	if outOfOrder {
		bNew, aNew = join(all, entry), join(as, func(v variant) string { return v.name })
		reached = append(reached, "out-of-order")
	} else {
		bNew = strings.TrimPrefix(join(ps, entry)+" | A", " | ")
		if len(ps) == 0 {
			bNew = "A"
		}
		if len(ss) > 0 {
			bNew += " | " + join(ss, entry)
		}
		aNew = join(as, entry)
		reached = append(reached, "relisted")
		for _, v := range as {
			if v.inline {
				reached = append(reached, "moved")
			}
		}
	}
	aPart := aArms.String()
	if rebuildArm {
		reached = append(reached, "rebuild-arm")
	} else {
		first := strings.ToLower(shuffled[0].name)
		aPart = "\tcase A as " + first + ":\n\t\t" + first + "\n"
		reached = append(reached, "arms")
	}
	reached = append(reached, "widened")
	newSrc = "type B = " + bNew + "\n\ntype A = " + aNew + "\n\n" + apart.String() + "func widen(s A) B { s }\n\n" + keep(aPart)
	return oldSrc, newSrc, reached
}
