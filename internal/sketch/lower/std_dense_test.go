package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdDenseLawsProbe restates each law of std dense (ledger runner
// TestStdDenseLawsRun) as a function over the package's exported
// operations, with the laws' helpers so restated, and a property test of
// its own name (stdLawsTestName) that asserts it.
const stdDenseLawsProbe = `import dense
import maps
import slices

func filled(xs []int) (dense.Table[int], []dense.ID[int]) {
	let mut t = dense.New[int]()
	let mut ids = []dense.ID[int]{}
	for x in xs {
		ids = ids.Append(t.Put(x))
	}
	(t, ids)
}

func pick(k int, n int) int { (k%n + n) % n }

func lawPutThenAt(xs []int, v int) bool {
	let (start, _) = filled(xs)
	let mut t = start
	let id = t.Put(v)
	dense.At(t, id) == v && dense.Has(t, id) && dense.Len(t) == len(xs)+1
}

test DensePutThenAt(xs []int, v int) { assert lawPutThenAt(xs, v) }

func lawPutKeepsOthers(xs []int, v int) bool {
	let (start, ids) = filled(xs)
	let mut t = start
	let fresh = t.Put(v)
	let mut ok = true
	for i, id in ids {
		ok = ok && id != fresh && dense.At(t, id) == xs[i]
	}
	ok
}

test DensePutKeepsOthers(xs []int, v int) { assert lawPutKeepsOthers(xs, v) }

func lawIDsAreDense(xs []int) bool {
	let (t, ids) = filled(xs)
	let mut i = 0
	let mut ok = true
	for e in dense.Each(t) {
		ok = ok && i < len(xs) && e.ID == ids[i] && dense.Index(e.ID) == i && e.Row == xs[i]
		i = i + 1
	}
	let mut j = 0
	for id in dense.IDs(t) {
		ok = ok && j < len(ids) && id == ids[j]
		j = j + 1
	}
	for k, row in dense.Rows(t) {
		ok = ok && row == xs[k] && dense.IDOf(t, k) == ids[k]
	}
	ok && i == len(xs) && j == len(xs) && len(dense.Rows(t)) == len(xs)
}

test DenseIDsAreDense(xs []int) { assert lawIDsAreDense(xs) }

func lawTableEachStops(xs []int) bool {
	let (t, _) = filled(xs)
	let s = dense.Each(t)
	let n = len(xs)
	for k in 1..n+2 {
		let mut calls = 0
		s(func(e dense.Entry[int]) bool {
			calls = calls + 1
			calls < k
		})
		if calls != (if k < n { k } else { n }) {
			return false
		}
	}
	true
}

test DenseTableEachStops(xs []int) { assert lawTableEachStops(xs) }

func lawTableIDsStops(xs []int) bool {
	let (t, _) = filled(xs)
	let s = dense.IDs(t)
	let n = len(xs)
	for k in 1..n+2 {
		let mut calls = 0
		s(func(id dense.ID[int]) bool {
			calls = calls + 1
			calls < k
		})
		if calls != (if k < n { k } else { n }) {
			return false
		}
	}
	true
}

test DenseTableIDsStops(xs []int) { assert lawTableIDsStops(xs) }

func lawGetAgreesWithHas(xs []int) bool {
	let (t, _) = filled(xs)
	let (_, ids) = filled(xs.Append(0).Append(0))
	let mut ok = true
	for id in ids {
		match dense.Get(t, id) {
		case Some[int] as v:
			ok = ok && dense.Has(t, id) && v.Value == dense.At(t, id)
		case None:
			ok = ok && !dense.Has(t, id)
		}
	}
	ok
}

test DenseGetAgreesWithHas(xs []int) { assert lawGetAgreesWithHas(xs) }

func lawSetThenAt(xs []int, k int, v int) bool {
	if len(xs) == 0 {
		return true
	}
	let (start, ids) = filled(xs)
	let mut t = start
	let i = pick(k, len(xs))
	t.Set(ids[i], v)
	let mut ok = dense.At(t, ids[i]) == v && dense.Len(t) == len(xs)
	for j, id in ids {
		ok = ok && (j == i || dense.At(t, id) == xs[j])
	}
	ok
}

test DenseSetThenAt(xs []int, k int, v int) { assert lawSetThenAt(xs, k, v) }

func lawOptRoundTrips(xs []int, k int) bool {
	if len(xs) == 0 {
		return dense.Unpack(dense.NoID[int]()) == None
	}
	let (_, ids) = filled(xs)
	let id = ids[pick(k, len(ids))]
	let other = ids[0]
	dense.Unpack(dense.OptOf(id)) == Some[dense.ID[int]]{Value: id} && dense.Unpack(dense.NoID[int]()) == None && dense.Or(dense.OptOf(id), other) == id && dense.Or(dense.NoID[int](), other) == other
}

test DenseOptRoundTrips(xs []int, k int) { assert lawOptRoundTrips(xs, k) }

func lawNextThenOf(xs []int) bool {
	let mut c = dense.NewCount[int]()
	let mut col = dense.NewCol[int, int]()
	let mut ids = []dense.ID[int]{}
	for x in xs {
		ids = ids.Append(c.Next())
		col.Push(x)
	}
	let mut ok = dense.Total(c) == len(xs)
	for i, id in ids {
		ok = ok && dense.In(col, id) && dense.Of(col, id) == xs[i] && dense.Index(id) == i
	}
	if len(ids) > 0 {
		col.SetOf(ids[0], -1)
		ok = ok && dense.Of(col, ids[0]) == -1
	}
	ok
}

test DenseNextThenOf(xs []int) { assert lawNextThenOf(xs) }

func lawSideDefaults(xs []int, sets []int, d int) bool {
	let (_, ids) = filled(xs)
	let mut s = dense.NewSide[int, int](d)
	let mut model = Map[dense.ID[int], int]{}
	if len(ids) > 0 {
		for v in sets {
			let id = ids[pick(v, len(ids))]
			s.SideSet(id, v)
			model[id] = v
		}
	}
	let mut ok = true
	for id in ids {
		ok = ok && dense.SideOf(s, id) == maps.GetOr(model, id, d)
	}
	ok
}

test DenseSideDefaults(xs []int, sets []int, d int) { assert lawSideDefaults(xs, sets, d) }

func lawListRoundTrips(xs []int) bool {
	let mut p = dense.NewPool[int]()
	let r = p.PutList(xs)
	slices.Equal(dense.List(p, r), xs) && dense.RunLen(r) == len(xs)
}

test DenseListRoundTrips(xs []int) { assert lawListRoundTrips(xs) }

func lawListsDoNotOverlap(a []int, b []int, c []int) bool {
	let mut p = dense.NewPool[int]()
	let ra = p.PutList(a)
	let rb = p.PutList(b)
	let rc = p.PutList(c)
	slices.Equal(dense.List(p, ra), a) && slices.Equal(dense.List(p, rb), b) && slices.Equal(dense.List(p, rc), c) && dense.Items(p) == len(a)+len(b)+len(c)
}

test DenseListsDoNotOverlap(a []int, b []int, c []int) { assert lawListsDoNotOverlap(a, b, c) }
`

// stdDenseLawStatements is the statement of each dense law, as the ledger
// writes it, by ledger key.
var stdDenseLawStatements = map[string]string{
	"dense PutThenAt":         `PutThenAt(xs []int, v int) { let (start, _) = filled(xs) let mut t = start let id = t.Put(v) At(t, id) == v && Has(t, id) && Len(t) == len(xs)+1 }`,
	"dense PutKeepsOthers":    `PutKeepsOthers(xs []int, v int) { let (start, ids) = filled(xs) let mut t = start let fresh = t.Put(v) let mut ok = true for i, id in ids { ok = ok && id != fresh && At(t, id) == xs[i] } ok }`,
	"dense IDsAreDense":       `IDsAreDense(xs []int) { let (t, ids) = filled(xs) let mut i = 0 let mut ok = true for e in Each(t) { ok = ok && i < len(xs) && e.ID == ids[i] && Index(e.ID) == i && e.Row == xs[i] i = i + 1 } let mut j = 0 for id in IDs(t) { ok = ok && j < len(ids) && id == ids[j] j = j + 1 } for k, row in Rows(t) { ok = ok && row == xs[k] && IDOf(t, k) == ids[k] } ok && i == len(xs) && j == len(xs) && len(Rows(t)) == len(xs) }`,
	"dense TableEachStops":    `TableEachStops(xs []int) { let (t, _) = filled(xs) let s = Each(t) let n = len(xs) for k in 1..n+2 { let mut calls = 0 s(func(e Entry[int]) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"dense TableIDsStops":     `TableIDsStops(xs []int) { let (t, _) = filled(xs) let s = IDs(t) let n = len(xs) for k in 1..n+2 { let mut calls = 0 s(func(id ID[int]) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"dense GetAgreesWithHas":  `GetAgreesWithHas(xs []int) { let (t, _) = filled(xs) let (_, ids) = filled(xs.Append(0).Append(0)) let mut ok = true for id in ids { match Get(t, id) { case Some[int] as v: ok = ok && Has(t, id) && v.Value == At(t, id) case None: ok = ok && !Has(t, id) } } ok }`,
	"dense SetThenAt":         `SetThenAt(xs []int, k int, v int) { if len(xs) == 0 { return true } let (start, ids) = filled(xs) let mut t = start let i = pick(k, len(xs)) t.Set(ids[i], v) let mut ok = At(t, ids[i]) == v && Len(t) == len(xs) for j, id in ids { ok = ok && (j == i || At(t, id) == xs[j]) } ok }`,
	"dense OptRoundTrips":     `OptRoundTrips(xs []int, k int) { if len(xs) == 0 { return Unpack(NoID[int]()) == None } let (_, ids) = filled(xs) let id = ids[pick(k, len(ids))] let other = ids[0] Unpack(OptOf(id)) == Some[ID[int]]{Value: id} && Unpack(NoID[int]()) == None && Or(OptOf(id), other) == id && Or(NoID[int](), other) == other }`,
	"dense NextThenOf":        `NextThenOf(xs []int) { let mut c = NewCount[int]() let mut col = NewCol[int, int]() let mut ids = []ID[int]{} for x in xs { ids = ids.Append(c.Next()) col.Push(x) } let mut ok = Total(c) == len(xs) for i, id in ids { ok = ok && In(col, id) && Of(col, id) == xs[i] && Index(id) == i } if len(ids) > 0 { col.SetOf(ids[0], -1) ok = ok && Of(col, ids[0]) == -1 } ok }`,
	"dense SideDefaults":      `SideDefaults(xs []int, sets []int, d int) { let (_, ids) = filled(xs) let mut s = NewSide[int, int](d) let mut model = Map[ID[int], int]{} if len(ids) > 0 { for v in sets { let id = ids[pick(v, len(ids))] s.SideSet(id, v) model[id] = v } } let mut ok = true for id in ids { ok = ok && SideOf(s, id) == maps.GetOr(model, id, d) } ok }`,
	"dense ListRoundTrips":    `ListRoundTrips(xs []int) { let mut p = NewPool[int]() let r = p.PutList(xs) slices.Equal(List(p, r), xs) && RunLen(r) == len(xs) }`,
	"dense ListsDoNotOverlap": `ListsDoNotOverlap(a []int, b []int, c []int) { let mut p = NewPool[int]() let ra = p.PutList(a) let rb = p.PutList(b) let rc = p.PutList(c) slices.Equal(List(p, ra), a) && slices.Equal(List(p, rb), b) && slices.Equal(List(p, rc), c) && Items(p) == len(a)+len(b)+len(c) }`,
}

// TestStdDenseLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's dense laws, each with
// the row's statement, and that each passed. The dense-* mutants of the
// std mutant ledger name it where a law kills them.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs: the check-time evaluator skips them, since
// Put and Set borrow their store.
func TestStdDenseLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stddense", stdDenseLawsProbe, stdDenseLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
