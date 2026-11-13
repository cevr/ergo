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

func word(x int) string {
	if x%5 == 0 { "" } else { "w\(x)" }
}

func interned(xs []int) (dense.Interner[int], []dense.ID[int], dense.Strings, []dense.ID[dense.Str]) {
	let mut s = dense.NewInterner[int]()
	let mut t = dense.NewStrings()
	let mut ids = []dense.ID[int]{}
	let mut tids = []dense.ID[dense.Str]{}
	for x in xs {
		ids = ids.Append(s.Intern(x))
		tids = tids.Append(t.InternText(word(x)))
	}
	(s, ids, t, tids)
}

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

func lawInternTwiceSameID(xs []int) bool {
	let (s0, ids, t0, tids) = interned(xs)
	let mut s = s0
	let mut t = t0
	let mut ok = true
	for i, x in xs {
		ok = ok && s.Intern(x) == ids[i] && t.InternText(word(x)) == tids[i]
	}
	ok && dense.Interned(s) == dense.Interned(s0) && dense.Texts(t) == dense.Texts(t0)
}

test DenseInternTwiceSameID(xs []int) { assert lawInternTwiceSameID(xs) }

func lawDistinctValuesDistinctIDs(xs []int) bool {
	let (_, ids, _, tids) = interned(xs)
	let mut ok = true
	for i, x in xs {
		for j, y in xs {
			ok = ok && (x == y) == (ids[i] == ids[j]) && (word(x) == word(y)) == (tids[i] == tids[j])
		}
	}
	ok
}

test DenseDistinctValuesDistinctIDs(xs []int) { assert lawDistinctValuesDistinctIDs(xs) }

func lawValueOfIntern(xs []int, probe int) bool {
	let (s, ids, t, tids) = interned(xs)
	let mut values = Map[int, bool]{}
	let mut texts = Map[string, bool]{}
	let mut ok = true
	for i, x in xs {
		values[x] = true
		texts[word(x)] = true
		ok = ok && dense.Value(s, ids[i]) == x && dense.Text(t, tids[i]) == word(x)
		ok = ok && dense.Lookup(s, x) == Some[dense.ID[int]]{Value: ids[i]} && dense.FindText(t, word(x)) == Some[dense.ID[dense.Str]]{Value: tids[i]}
	}
	let absent = maps.GetOr(values, probe, false)
	let absentText = maps.GetOr(texts, word(probe), false)
	ok = ok && (absent || dense.Lookup(s, probe) == None) && (absentText || dense.FindText(t, word(probe)) == None)
	ok && dense.Interned(s) == len(values) && dense.Texts(t) == len(texts)
}

test DenseValueOfIntern(xs []int, probe int) { assert lawValueOfIntern(xs, probe) }

func lawReleaseThenReuse(seed int, count int) bool {
	let n = (count%64 + 64) % 64
	let mut x = (seed%2147483648 + 2147483648) % 2147483648
	let mut c = dense.NewCounted[int]()
	let mut live = Map[int, dense.ID[int]]{}
	let mut held = Map[int, int]{}
	let mut freed = Map[dense.ID[int], bool]{}
	for _ in 0..n {
		x = (x*1103515245 + 12345) % 2147483648
		let v = (x / 16) % 8
		let op = (x / 1024) % 3
		match live.Get(v) {
		case Some[dense.ID[int]] as id:
			if c.Add(v) != id.Value {
				return false
			}
			if op == 1 {
				c.Use(id.Value)
				held[v] = maps.GetOr(held, v, 0) + 1
			} else if op == 2 && maps.GetOr(held, v, 0) > 0 {
				c.Release(id.Value)
				held[v] = maps.GetOr(held, v, 0) - 1
				if maps.GetOr(held, v, 0) == 0 {
					live = live.Delete(v)
					held = held.Delete(v)
					freed[id.Value] = true
				}
			}
		case None:
			let reuse = len(freed) > 0
			let id = c.Add(v)
			for _, other in live {
				if other == id {
					return false
				}
			}
			if reuse != maps.GetOr(freed, id, false) {
				return false
			}
			freed = freed.Delete(id)
			live[v] = id
		}
		if dense.Live(c) != len(live) {
			return false
		}
		for w, id in live {
			if dense.Member(c, id) != w || dense.Uses(c, id) != maps.GetOr(held, w, 0) {
				return false
			}
		}
	}
	true
}

test DenseReleaseThenReuse(seed int, count int) { assert lawReleaseThenReuse(seed, count) }

func lawSparseAgreesWithSet(seed int, count int) bool {
	let n = (count%64 + 64) % 64
	let mut x = (seed%2147483648 + 2147483648) % 2147483648
	let (_, ids) = filled([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	let mut s = dense.NewSparse[int]()
	let mut model = Map[dense.ID[int], bool]{}
	for _ in 0..n {
		x = (x*1103515245 + 12345) % 2147483648
		let id = ids[(x/16)%16]
		let op = (x / 1024) % 7
		if op < 4 {
			s.Insert(id)
			model[id] = true
		} else if op < 6 {
			s.Remove(id)
			model = model.Delete(id)
		} else {
			s.Clear()
			model = Map[dense.ID[int], bool]{}
		}
		if dense.Size(s) != len(model) {
			return false
		}
		for other in ids {
			if dense.Contains(s, other) != maps.GetOr(model, other, false) {
				return false
			}
		}
		let mut seen = Map[dense.ID[int], bool]{}
		for m in dense.Members(s) {
			if !maps.GetOr(model, m, false) || maps.GetOr(seen, m, false) {
				return false
			}
			seen[m] = true
		}
	}
	true
}

test DenseSparseAgreesWithSet(seed int, count int) { assert lawSparseAgreesWithSet(seed, count) }
`

// stdDenseLawStatements is the statement of each dense law, as the ledger
// writes it, by ledger key.
var stdDenseLawStatements = map[string]string{
	"dense PutThenAt":                 `PutThenAt(xs []int, v int) { let (start, _) = filled(xs) let mut t = start let id = t.Put(v) At(t, id) == v && Has(t, id) && Len(t) == len(xs)+1 }`,
	"dense PutKeepsOthers":            `PutKeepsOthers(xs []int, v int) { let (start, ids) = filled(xs) let mut t = start let fresh = t.Put(v) let mut ok = true for i, id in ids { ok = ok && id != fresh && At(t, id) == xs[i] } ok }`,
	"dense IDsAreDense":               `IDsAreDense(xs []int) { let (t, ids) = filled(xs) let mut i = 0 let mut ok = true for e in Each(t) { ok = ok && i < len(xs) && e.ID == ids[i] && Index(e.ID) == i && e.Row == xs[i] i = i + 1 } let mut j = 0 for id in IDs(t) { ok = ok && j < len(ids) && id == ids[j] j = j + 1 } for k, row in Rows(t) { ok = ok && row == xs[k] && IDOf(t, k) == ids[k] } ok && i == len(xs) && j == len(xs) && len(Rows(t)) == len(xs) }`,
	"dense TableEachStops":            `TableEachStops(xs []int) { let (t, _) = filled(xs) let s = Each(t) let n = len(xs) for k in 1..n+2 { let mut calls = 0 s(func(e Entry[int]) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"dense TableIDsStops":             `TableIDsStops(xs []int) { let (t, _) = filled(xs) let s = IDs(t) let n = len(xs) for k in 1..n+2 { let mut calls = 0 s(func(id ID[int]) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"dense GetAgreesWithHas":          `GetAgreesWithHas(xs []int) { let (t, _) = filled(xs) let (_, ids) = filled(xs.Append(0).Append(0)) let mut ok = true for id in ids { match Get(t, id) { case Some[int] as v: ok = ok && Has(t, id) && v.Value == At(t, id) case None: ok = ok && !Has(t, id) } } ok }`,
	"dense SetThenAt":                 `SetThenAt(xs []int, k int, v int) { if len(xs) == 0 { return true } let (start, ids) = filled(xs) let mut t = start let i = pick(k, len(xs)) t.Set(ids[i], v) let mut ok = At(t, ids[i]) == v && Len(t) == len(xs) for j, id in ids { ok = ok && (j == i || At(t, id) == xs[j]) } ok }`,
	"dense OptRoundTrips":             `OptRoundTrips(xs []int, k int) { if len(xs) == 0 { return Unpack(NoID[int]()) == None } let (_, ids) = filled(xs) let id = ids[pick(k, len(ids))] let other = ids[0] Unpack(OptOf(id)) == Some[ID[int]]{Value: id} && Unpack(NoID[int]()) == None && Or(OptOf(id), other) == id && Or(NoID[int](), other) == other }`,
	"dense NextThenOf":                `NextThenOf(xs []int) { let mut c = NewCount[int]() let mut col = NewCol[int, int]() let mut ids = []ID[int]{} for x in xs { ids = ids.Append(c.Next()) col.Push(x) } let mut ok = Total(c) == len(xs) for i, id in ids { ok = ok && In(col, id) && Of(col, id) == xs[i] && Index(id) == i } if len(ids) > 0 { col.SetOf(ids[0], -1) ok = ok && Of(col, ids[0]) == -1 } ok }`,
	"dense SideDefaults":              `SideDefaults(xs []int, sets []int, d int) { let (_, ids) = filled(xs) let mut s = NewSide[int, int](d) let mut model = Map[ID[int], int]{} if len(ids) > 0 { for v in sets { let id = ids[pick(v, len(ids))] s.SideSet(id, v) model[id] = v } } let mut ok = true for id in ids { ok = ok && SideOf(s, id) == maps.GetOr(model, id, d) } ok }`,
	"dense ListRoundTrips":            `ListRoundTrips(xs []int) { let mut p = NewPool[int]() let r = p.PutList(xs) slices.Equal(List(p, r), xs) && RunLen(r) == len(xs) }`,
	"dense ListsDoNotOverlap":         `ListsDoNotOverlap(a []int, b []int, c []int) { let mut p = NewPool[int]() let ra = p.PutList(a) let rb = p.PutList(b) let rc = p.PutList(c) slices.Equal(List(p, ra), a) && slices.Equal(List(p, rb), b) && slices.Equal(List(p, rc), c) && Items(p) == len(a)+len(b)+len(c) }`,
	"dense InternTwiceSameID":         `InternTwiceSameID(xs []int) { let (s0, ids, t0, tids) = interned(xs) let mut s = s0 let mut t = t0 let mut ok = true for i, x in xs { ok = ok && s.Intern(x) == ids[i] && t.InternText(word(x)) == tids[i] } ok && Interned(s) == Interned(s0) && Texts(t) == Texts(t0) }`,
	"dense DistinctValuesDistinctIDs": `DistinctValuesDistinctIDs(xs []int) { let (_, ids, _, tids) = interned(xs) let mut ok = true for i, x in xs { for j, y in xs { ok = ok && (x == y) == (ids[i] == ids[j]) && (word(x) == word(y)) == (tids[i] == tids[j]) } } ok }`,
	"dense ValueOfIntern":             `ValueOfIntern(xs []int, probe int) { let (s, ids, t, tids) = interned(xs) let mut values = Map[int, bool]{} let mut texts = Map[string, bool]{} let mut ok = true for i, x in xs { values[x] = true texts[word(x)] = true ok = ok && Value(s, ids[i]) == x && Text(t, tids[i]) == word(x) ok = ok && Lookup(s, x) == Some[ID[int]]{Value: ids[i]} && FindText(t, word(x)) == Some[ID[Str]]{Value: tids[i]} } let absent = maps.GetOr(values, probe, false) let absentText = maps.GetOr(texts, word(probe), false) ok = ok && (absent || Lookup(s, probe) == None) && (absentText || FindText(t, word(probe)) == None) ok && Interned(s) == len(values) && Texts(t) == len(texts) }`,
	"dense ReleaseThenReuse":          `ReleaseThenReuse(seed int, count int) { let n = (count%64 + 64) % 64 let mut x = (seed%2147483648 + 2147483648) % 2147483648 let mut c = NewCounted[int]() let mut live = Map[int, ID[int]]{} let mut held = Map[int, int]{} let mut freed = Map[ID[int], bool]{} for _ in 0..n { x = (x*1103515245 + 12345) % 2147483648 let v = (x / 16) % 8 let op = (x / 1024) % 3 match live.Get(v) { case Some[ID[int]] as id: if c.Add(v) != id.Value { return false } if op == 1 { c.Use(id.Value) held[v] = maps.GetOr(held, v, 0) + 1 } else if op == 2 && maps.GetOr(held, v, 0) > 0 { c.Release(id.Value) held[v] = maps.GetOr(held, v, 0) - 1 if maps.GetOr(held, v, 0) == 0 { live = live.Delete(v) held = held.Delete(v) freed[id.Value] = true } } case None: let reuse = len(freed) > 0 let id = c.Add(v) for _, other in live { if other == id { return false } } if reuse != maps.GetOr(freed, id, false) { return false } freed = freed.Delete(id) live[v] = id } if Live(c) != len(live) { return false } for w, id in live { if Member(c, id) != w || Uses(c, id) != maps.GetOr(held, w, 0) { return false } } } true }`,
	"dense SparseAgreesWithSet":       `SparseAgreesWithSet(seed int, count int) { let n = (count%64 + 64) % 64 let mut x = (seed%2147483648 + 2147483648) % 2147483648 let (_, ids) = filled([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}) let mut s = NewSparse[int]() let mut model = Map[ID[int], bool]{} for _ in 0..n { x = (x*1103515245 + 12345) % 2147483648 let id = ids[(x/16)%16] let op = (x / 1024) % 7 if op < 4 { s.Insert(id) model[id] = true } else if op < 6 { s.Remove(id) model = model.Delete(id) } else { s.Clear() model = Map[ID[int], bool]{} } if Size(s) != len(model) { return false } for other in ids { if Contains(s, other) != maps.GetOr(model, other, false) { return false } } let mut seen = Map[ID[int], bool]{} for m in Members(s) { if !maps.GetOr(model, m, false) || maps.GetOr(seen, m, false) { return false } seen[m] = true } } true }`,
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
