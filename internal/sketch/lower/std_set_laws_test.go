package lower

// Generated from the laws of internal/sketch/std/set by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdSetLawsProbe restates the set laws whose runner is
// TestStdSetLawsRun as Ergo tests over std set.
const stdSetLawsProbe = `import set
import slices

test SetSetValuesStops(xs []int) {
	let s = set.Values(set.Of(xs))
	let n = set.Len(set.Of(xs))
	for k in 1..n+2 {
		let mut calls = 0
		s(func(v int) bool {
			calls = calls + 1
			calls < k
		})
		if calls != (if k < n { k } else { n }) {
			assert false
			return
		}
	}
	assert true
}

test SetAddHasRemoveHasNot(xs []int, k int) {
	let s = set.Of(xs)
	let added = set.Add(s, k)
	let removed = set.Remove(s, k)
	let mut ok = set.Has(added, k) && !set.Has(removed, k) && set.Has(s, k) == slices.Contains(xs, k)
	for x in xs {
		ok = ok && set.Has(s, x) && set.Has(added, x) && set.Has(removed, x) == (x != k)
	}
	assert ok && set.Len(s) == len(slices.Dedupe(xs))
}

test SetSetAlgebra(xs []int, ys []int, k int) {
	let a = set.Of(xs)
	let b = set.Of(ys)
	let inA = set.Has(a, k)
	let inB = set.Has(b, k)
	let d = set.Difference(a, b)
	let i = set.Intersection(a, b)
	assert set.Has(set.Union(a, b), k) == (inA || inB) && set.Has(i, k) == (inA && inB) && set.Has(d, k) == (inA && !inB) && set.Equal(set.Union(a, b), set.Union(b, a)) && set.Equal(set.Union(d, i), a) && set.Len(set.Intersection(d, i)) == 0 && set.IsSubset(i, a) && set.IsSubset(i, b)
}
`

// stdSetLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdSetLawStatements = map[string]string{
	"set SetValuesStops":     `SetValuesStops(xs []int) { let s = Values(Of(xs)) let n = Len(Of(xs)) for k in 1..n+2 { let mut calls = 0 s(func(v int) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"set AddHasRemoveHasNot": `AddHasRemoveHasNot(xs []int, k int) { let s = Of(xs) let added = Add(s, k) let removed = Remove(s, k) let mut ok = Has(added, k) && !Has(removed, k) && Has(s, k) == slices.Contains(xs, k) for x in xs { ok = ok && Has(s, x) && Has(added, x) && Has(removed, x) == (x != k) } ok && Len(s) == len(slices.Dedupe(xs)) }`,
	"set SetAlgebra":         `SetAlgebra(xs []int, ys []int, k int) { let a = Of(xs) let b = Of(ys) let inA = Has(a, k) let inB = Has(b, k) let d = Difference(a, b) let i = Intersection(a, b) Has(Union(a, b), k) == (inA || inB) && Has(i, k) == (inA && inB) && Has(d, k) == (inA && !inB) && Equal(Union(a, b), Union(b, a)) && Equal(Union(d, i), a) && Len(Intersection(d, i)) == 0 && IsSubset(i, a) && IsSubset(i, b) }`,
}
