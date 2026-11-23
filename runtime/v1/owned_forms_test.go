package v1

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// ownedTwins are the twin laws of the //ergo:owned directives (REUSE RU6;
// docs/spec/types.md, Bindings 2, consumed arguments), one per body that
// names an owned form. A run draws its arguments from r and reports a
// broken law, or "". Each law is the directive's licence, read by the
// lowering as a reviewed fact: the plain form leaves its first argument
// unchanged, its spare capacity included, and the owned form (and its
// checking form, where it has one) on a copy of that argument gives what
// the plain form gives.
var ownedTwins = map[string]func(r *rand.Rand) string{
	"SlicesSort": func(r *rand.Rand) string {
		xs := spareInts(r)
		return twinLaw(xs, func(a []int) []int { return SlicesSort(a) }, func(a []int) []int { return SlicesSortOwned(a) }, nil)
	},
	"SlicesReverse": func(r *rand.Rand) string {
		xs := spareInts(r)
		return twinLaw(xs, func(a []int) []int { return SlicesReverse(a) }, func(a []int) []int { return SlicesReverseOwned(a) }, nil)
	},
	"SlicesConcat": func(r *rand.Rand) string {
		xs, ys := spareInts(r), spareInts(r)
		return twinLaw(xs, func(a []int) []int { return SlicesConcat(a, ys) }, func(a []int) []int { return SlicesConcatOwned(a, ys) }, func(a []int) []int { return SlicesConcatPoison(a, ys) })
	},
	"StringsBuilderText": func(r *rand.Rand) string {
		ints := spareInts(r)
		buf := make([]byte, len(ints), cap(ints))
		for i := range buf[:cap(buf)] {
			buf[:cap(buf)][i] = byte(ints[:cap(ints)][i])
		}
		return twinLaw(buf, func(a []byte) string { return StringsBuilderText(a) }, func(a []byte) string { return StringsBuilderTextOwned(a) }, nil)
	},
}

// TestOwnedFormsAgree checks each directive's twin law over seeded
// arguments, and that every body naming an owned form has its law here,
// so a new directive cannot land without one. A failing case prints the
// body and its seed, the first word of its rand.NewPCG source.
//
// Mutant (testdata/mutants.json): ru6-concat-plain-writes.
func TestOwnedFormsAgree(t *testing.T) {
	directives := map[string]bool{}
	for _, name := range BodyNames() {
		if b, err := BodyOf(name); err == nil && b.Owned != "" {
			directives[name] = true
			if ownedTwins[name] == nil {
				t.Errorf("%s names the owned form %s and has no twin law in ownedTwins", name, b.Owned)
			}
		}
	}
	for name, run := range ownedTwins {
		if !directives[name] {
			t.Errorf("ownedTwins has a law for %s, which names no owned form: drop it", name)
			continue
		}
		for seed := range uint64(2000) {
			if msg := run(rand.New(rand.NewPCG(seed, 0x52553600))); msg != "" {
				t.Fatalf("%s, seed %d: %s", name, seed, msg)
			}
		}
	}
}

// spareInts is a slice of 0 to 8 small ints with 0 to 4 elements of
// spare capacity, which hold their own values, so a body that writes past
// the length is seen.
func spareInts(r *rand.Rand) []int {
	n, spare := r.IntN(9), r.IntN(5)
	xs := make([]int, n+spare)
	for i := range xs {
		xs[i] = r.IntN(7) - 3
	}
	return xs[:n]
}

// twinLaw checks one directive's law on the argument a (see ownedTwins).
func twinLaw[E comparable, R any](a []E, plain, owned, checked func([]E) R) string {
	before := slices.Clone(a[:cap(a)])
	want := plain(a)
	if !slices.Equal(a[:cap(a)], before) {
		return fmt.Sprintf("the plain form wrote its argument: %v became %v", before, a[:cap(a)])
	}
	copyOf := func() []E { c := slices.Clone(before); return c[:len(a)] }
	if got := owned(copyOf()); fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Sprintf("the owned form on a copy of %v gives %v, the plain form %v", a, got, want)
	}
	if checked != nil {
		if got := checked(copyOf()); fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Sprintf("the checking form on a copy of %v gives %v, the plain form %v", a, got, want)
		}
	}
	return ""
}
