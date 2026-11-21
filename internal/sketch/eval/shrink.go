package eval

// shrinkBudget is how many replays a shrink may make: the native runner's
// ($Prop's 500), so both tiers stop at the same tape (TESTSPEED FT1).
const shrinkBudget = 500

// shrinkTape is the native runner's shrinker (L10, #162), the evaluator's
// own implementation of its rule: tape is the draws of a refuted case,
// fails replays a candidate and reports whether it is still refuted the
// same way. Each candidate is smaller in shortlex order, and one is kept
// when it still fails. The passes, repeated until a round keeps nothing or
// the budget is spent: delete a chunk of 8, 4, 2 or 1 draws; set a chunk
// of 8, 4 or 2 to zero; lower each draw, to zero first, then by a binary
// search on the gap, then by single decrements while they still fail.
// TestShrinkAgreesWithRuntime holds it equal to v1.ShrinkTape, tape for
// tape and try for try.
func shrinkTape(tape []uint64, fails func([]uint64) bool, budget int) (best []uint64, tries int) {
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
				if try(append(append([]uint64(nil), best[:i]...), best[i+k:]...)) {
					improved = true
					continue // the next draws now stand at i
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
			lo := uint64(0) // best[i] fails and lo does not
			for best[i]-lo > 2 && tries < budget {
				c := append([]uint64(nil), best...)
				c[i] = lo + (best[i]-lo)/2
				if try(c) {
					improved = true
				} else {
					lo = c[i]
				}
			}
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
