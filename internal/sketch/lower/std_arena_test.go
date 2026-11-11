package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdArenaLawsProbe restates each law of std arena (ledger runner
// TestStdArenaLawsRun) as a property test of its own name
// (stdLawsTestName), with the laws' helpers filled and stale restated
// over the package's exported operations.
const stdArenaLawsProbe = `import arena

func filled(xs []int) (arena.Of[int], []arena.Ref[int]) {
	let mut a = arena.New[int]()
	let mut refs = []arena.Ref[int]{}
	for x in xs {
		refs = refs.Append(a.Put(x))
	}
	(a, refs)
}

func stale(a arena.Of[int], r arena.Ref[int]) bool {
	let mut b = a
	let size = arena.Len(b)
	let mut failed = 0
	let mut called = false
	let _ = b.Get(r) catch {
	case arena.Stale:
		failed = failed + 1
		0
	}
	b.Set(r, 0) catch {
	case arena.Stale:
		failed = failed + 1
	}
	b.Update(r, func(v *int) { called = true }) catch {
	case arena.Stale:
		failed = failed + 1
	}
	let _ = b.Free(r) catch {
	case arena.Stale:
		failed = failed + 1
		0
	}
	!arena.Has(b, r) && failed == 4 && !called && arena.Len(b) == size
}

test ArenaGetAfterPut(xs []int) {
	let (start, refs) = filled(xs)
	let mut a = start
	assert arena.Len(a) == len(xs)
	for i, r in refs {
		let v = try a.Get(r)
		assert v == xs[i] && arena.Has(a, r)
	}
}

test ArenaFreeThenStale(xs []int, k int) {
	if len(xs) == 0 {
		return
	}
	let (start, refs) = filled(xs)
	let mut a = start
	let i = (k%len(xs) + len(xs)) % len(xs)
	let v = try a.Free(refs[i])
	assert v == xs[i] && stale(a, refs[i]) && arena.Len(a) == len(xs)-1
	for j, r in refs {
		if j != i {
			assert try a.Get(r) == xs[j]
		}
	}
}

test ArenaReuseNeverAliases(xs []int, k int, v int) {
	if len(xs) == 0 {
		return
	}
	let (start, refs) = filled(xs)
	let mut a = start
	let i = (k%len(xs) + len(xs)) % len(xs)
	let slots = arena.Cap(a)
	let _ = try a.Free(refs[i])
	let fresh = a.Put(v)
	assert try a.Get(fresh) == v
	assert arena.Cap(a) == slots && stale(a, refs[i])
	for r in refs {
		assert r != fresh
	}
}

test ArenaNullIsStale(xs []int) {
	let (a, refs) = filled(xs)
	assert stale(a, arena.Null[int]()) && stale(arena.New[int](), arena.Null[int]())
	for r in refs {
		assert r != arena.Null[int]()
	}
}

test ArenaModelAgrees(seed int, count int) {
	let n = (count%64 + 64) % 64
	let mut x = (seed%2147483648 + 2147483648) % 2147483648
	let mut a = arena.New[int]()
	let mut model = Map[arena.Ref[int], int]{}
	let mut refs = []arena.Ref[int]{}
	for i in 0..n {
		x = (x*1103515245 + 12345) % 2147483648
		let op = (x / 8) % 5
		if op == 0 || len(refs) == 0 {
			let r = a.Put(i)
			assert model.Get(r) == None
			model[r] = i
			refs = refs.Append(r)
		} else {
			let r = refs[(x/65536)%len(refs)]
			let want = model.Get(r)
			let mut failed = false
			let mut got = i
			match op {
			case 1:
				got = a.Get(r) catch {
				case arena.Stale:
					failed = true
					i
				}
			case 2:
				a.Set(r, -i) catch {
				case arena.Stale:
					failed = true
				}
				got = -i
			case 3:
				let mut seen = i
				a.Update(r, func(v *int) {
					seen = *v
					*v = *v + i
				}) catch {
				case arena.Stale:
					failed = true
				}
				got = seen
			case _:
				got = a.Free(r) catch {
				case arena.Stale:
					failed = true
					i
				}
			}
			match want {
			case Some[int] as w:
				assert !failed && (op == 2 || got == w.Value)
				match op {
				case 2:
					model[r] = -i
				case 3:
					model[r] = w.Value + i
				case 4:
					model = model.Delete(r)
				case _:
				}
			case None:
				assert failed && !arena.Has(a, r)
			}
		}
		assert arena.Len(a) == len(model)
	}
	for r, v in model {
		assert try a.Get(r) == v
	}
}
`

// stdArenaLawStatements is the statement of each arena law, as the ledger
// writes it, by ledger key.
var stdArenaLawStatements = map[string]string{
	"arena GetAfterPut":       `GetAfterPut(xs []int) { let (start, refs) = filled(xs) let mut a = start let mut ok = Len(a) == len(xs) for i, r in refs { let v = a.Get(r) catch { case Stale: return false } ok = ok && v == xs[i] && Has(a, r) } ok }`,
	"arena FreeThenStale":     `FreeThenStale(xs []int, k int) { if len(xs) == 0 { return true } let (start, refs) = filled(xs) let mut a = start let i = (k%len(xs) + len(xs)) % len(xs) let v = a.Free(refs[i]) catch { case Stale: return false } let mut ok = v == xs[i] && stale(a, refs[i]) && Len(a) == len(xs)-1 for j, r in refs { if j != i { let w = a.Get(r) catch { case Stale: return false } ok = ok && w == xs[j] } } ok }`,
	"arena ReuseNeverAliases": `ReuseNeverAliases(xs []int, k int, v int) { if len(xs) == 0 { return true } let (start, refs) = filled(xs) let mut a = start let i = (k%len(xs) + len(xs)) % len(xs) let slots = Cap(a) let _ = a.Free(refs[i]) catch { case Stale: return false } let fresh = a.Put(v) let got = a.Get(fresh) catch { case Stale: return false } let mut ok = Cap(a) == slots && got == v && stale(a, refs[i]) for r in refs { ok = ok && r != fresh } ok }`,
	"arena NullIsStale":       `NullIsStale(xs []int) { let (a, refs) = filled(xs) let mut ok = stale(a, Null[int]()) && stale(New[int](), Null[int]()) for r in refs { ok = ok && r != Null[int]() } ok }`,
	"arena ModelAgrees":       `ModelAgrees(seed int, count int) { let n = (count%64 + 64) % 64 let mut x = (seed%2147483648 + 2147483648) % 2147483648 let mut a = New[int]() let mut model = Map[Ref[int], int]{} let mut refs = []Ref[int]{} for i in 0..n { x = (x*1103515245 + 12345) % 2147483648 let op = (x / 8) % 5 if op == 0 || len(refs) == 0 { let r = a.Put(i) if model.Get(r) != None { return false } model[r] = i refs = refs.Append(r) } else { let r = refs[(x/65536)%len(refs)] let want = model.Get(r) let mut failed = false let mut got = i match op { case 1: got = a.Get(r) catch { case Stale: failed = true i } case 2: a.Set(r, -i) catch { case Stale: failed = true } got = -i case 3: let mut seen = i a.Update(r, func(v *int) { seen = *v *v = *v + i }) catch { case Stale: failed = true } got = seen case _: got = a.Free(r) catch { case Stale: failed = true i } } match want { case Some[int] as w: if failed || (op != 2 && got != w.Value) { return false } match op { case 2: model[r] = -i case 3: model[r] = w.Value + i case 4: model = model.Delete(r) case _: } case None: if !failed || Has(a, r) { return false } } } if Len(a) != len(model) { return false } } for r, v in model { let got = a.Get(r) catch { case Stale: return false } if got != v { return false } } true }`,
}

// TestStdArenaLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's arena laws, each with
// the row's statement, and that each passed. The mutants of the arena's
// generations, free list and stale checks in testdata/mutants.json are
// its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdArenaLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdarena", stdArenaLawsProbe, stdArenaLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
