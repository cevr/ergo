package v1

// ShrinkTape shrinks the tape of a refuted case (L10, #162): the draws its
// generators made, in order. fails replays the case from a candidate tape
// (past its end a tape gives 0) and reports whether it is still refuted.
// Every candidate a pass makes is smaller in shortlex order (shorter, else
// lexicographically smaller), and one is kept when it still fails;
// shortlex is a well-order on finite tapes, so the shrinker ends, and
// budget bounds the replays as well. The passes, repeated until a round keeps nothing: delete
// a chunk of 8, 4, 2 or 1 draws; set a chunk to zero; lower each draw, to
// zero first, then by a binary search on the gap and adjacent decrements
// until the next decrement no longer refutes the case. Revisit every component after any
// improvement: changing a later component can unlock an earlier one.
// The result is the smallest tape found and the number of replays (tries), the work counter
// a test reads. Prior art: Hypothesis's buffer shrinker; Go's rapid.
func ShrinkTape(tape []uint64, fails func([]uint64) bool, budget int) (best []uint64, tries int) {
	best = append([]uint64(nil), tape...)
	try := func(c []uint64) bool {
		if tries >= budget {
			return false
		}
		tries++
		if fails(c) {
			best = c
			return true
		}
		return false
	}
	for improved := true; improved && tries < budget; {
		improved = false
		for _, k := range []int{8, 4, 2, 1} {
			for i := 0; i+k <= len(best); {
				c := append(append([]uint64(nil), best[:i]...), best[i+k:]...)
				if try(c) {
					improved = true
					continue // the same place now holds the next draws
				}
				i++
			}
		}
		for _, k := range []int{8, 4, 2} {
			for i := 0; i+k <= len(best); i++ {
				c := append([]uint64(nil), best...)
				zero := true
				for j := i; j < i+k; j++ {
					zero = zero && c[j] == 0
					c[j] = 0
				}
				if !zero && try(c) {
					improved = true
				}
			}
		}
		for i := 0; i < len(best); i++ {
			if best[i] == 0 {
				continue
			}
			c := append([]uint64(nil), best...)
			c[i] = 0
			if try(c) {
				improved = true
				continue
			}
			// best[i] fails and lo does not: search the gap.
			lo := uint64(0)
			for best[i]-lo > 2 && tries < budget {
				mid := lo + (best[i]-lo)/2
				c := append([]uint64(nil), best...)
				c[i] = mid
				if try(c) {
					improved = true
				} else {
					lo = mid
				}
			}
			// Binary search can skip failing islands of a non-monotone
			// predicate. The adjacent step still gives a local minimum.
			for best[i] > 0 && tries < budget {
				c := append([]uint64(nil), best...)
				c[i]--
				if !try(c) {
					break
				}
				improved = true
			}
		}
	}
	return best, tries
}
