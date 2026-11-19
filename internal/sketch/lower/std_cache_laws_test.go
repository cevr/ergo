package lower

// Generated from the laws of internal/sketch/std/cache by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdCacheLawsProbe restates the cache laws whose runner is
// TestStdCacheLawsRun as Ergo tests over std cache.
const stdCacheLawsProbe = `import cache
import queue
import sync
import task
import time

func gated(inflight sync.Ref[int], most sync.Ref[int], gate queue.Queue[int], key int) int {
	let now = sync.Modify(inflight, func(v int) (int, int) { (v + 1, v + 1) })
	most.Update(func(v int) int { if now > v { now } else { v } })
	let _ = gate.Pop() catch {
	case queue.Closed:
		0
	}
	inflight.Update(func(v int) int { v - 1 })
	key % 1000
}

type mentry { key int, value int, born duration, used duration }

func mfind(model []mentry, k int) int {
	for i, e in model {
		if e.key == k {
			return i
		}
	}
	-1
}

func mdrop(model []mentry, i int) []mentry {
	let mut out = []mentry{}
	for j, e in model {
		if j != i {
			out = out.Append(e)
		}
	}
	out
}

func mlive(config cache.Config, e mentry, now duration) bool {
	(config.Life == 0s || now-e.born < config.Life) && (config.Idle == 0s || now-e.used < config.Idle)
}

func sameLeft(a []int, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for x in a {
		let mut n = 0
		for y in a {
			if y == x {
				n += 1
			}
		}
		for y in b {
			if y == x {
				n -= 1
			}
		}
		if n != 0 {
			return false
		}
	}
	true
}

func pairOf(k int, v int) int { k*1000000 + v }

test CacheNewRefusesBadConfig(capacity int, life int64, idle int64) {
	let config = cache.Config{Capacity: capacity, Life: duration(life), Idle: duration(idle)}
	let ok = capacity >= 1 && life >= 0 && idle >= 0
	let _ = cache.New[int, int](config) catch {
	case cache.BadConfig:
		assert !ok
		return
	}
	assert ok
}

test CacheFailureIsNotKept(k int, v int) {
	let m = time.NewManual(0)
	let c = cache.New[int, int](cache.Config{Capacity: 1, Life: 0s, Idle: 0s}) catch {
	case cache.BadConfig:
		assert false
		return
	}
	let ok = with context { time.Clock: m } {
		let failed = cache.Get(c, k, func(key int) int fails cache.BadConfig { cache.BadConfig{Reason: "down"} }) catch {
		case cache.BadConfig:
			-1
		}
		let mut runs = 0
		let got = cache.Get(c, k, func(key int) int {
			runs += 1
			v
		})
		failed == -1 && got == v && runs == 1 && cache.StatsOf(c).Size == 1
	}
	assert ok
}

test CacheOneLookupAtOnce(k int) {
	let c = cache.New[int, int](cache.Config{Capacity: 4, Life: 0s, Idle: 0s}) catch {
	case cache.BadConfig:
		assert false
		return
	}
	let inflight = sync.NewRef[int](0)
	let most = sync.NewRef[int](0)
	let gate = queue.New[int](0)
	let m = time.NewManual(0)
	let ok = with context { time.Clock: m } {
		let a = task.Go() { cache.Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) }
		let b = task.Go() { cache.Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) }
		let d = task.Go() { cache.Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) }
		for inflight.Get() == 0 {
		}
		gate.End()
		let sum = a.Wait() + b.Wait() + d.Wait()
		sum == 3*(k%1000) && most.Get() == 1 && cache.StatsOf(c).Misses+cache.StatsOf(c).Hits == 3
	}
	assert ok
}

test CacheModelAgrees(seed int64) {
	let mut s = seed
	let draw = func(n int) int {
		s = s*6364136223846793005 + 1442695040888963407
		let r = (s >> 33) % int64(n)
		int(if r < 0 { r + int64(n) } else { r })
	}
	let capacity = 1 + draw(4)
	let life = time.Times(1s, draw(2)*(1+draw(5)))
	let idle = time.Times(1s, draw(2)*(1+draw(5)))
	let config = cache.Config{Capacity: capacity, Life: life, Idle: idle}
	let left = sync.NewRef[[]int]([]int{})
	let c = cache.NewReleasing[int, int](config, func(k int, v int) { left.Update(func(xs []int) []int { xs.Append(pairOf(k, v)) }) }) catch {
	case cache.BadConfig:
		assert false
		return
	}
	let m = time.NewManual(0)
	let mut model = []mentry{}
	let mut gone = []int{}
	let mut now = 0s
	let mut loads = 0
	let mut hits = 0
	let mut misses = 0
	with context { time.Clock: m } {
		for _ in 0..60 {
			let k = draw(5)
			let op = draw(10)
			gone = []int{}
			left.Set([]int{})
			if op < 5 {
				let mut ran = false
				let got = cache.Get(c, k, func(key int) int {
					ran = true
					key*1000 + loads
				})
				let at = mfind(model, k)
				if at >= 0 && mlive(config, model[at], now) {
					let e = model[at]
					model = mdrop(model, at).Append(mentry{key: k, value: e.value, born: e.born, used: now})
					hits += 1
					if ran || got != e.value {
						assert false
						return
					}
				} else {
					if at >= 0 {
						gone = gone.Append(pairOf(k, model[at].value))
						model = mdrop(model, at)
					}
					misses += 1
					if !ran || got != k*1000+loads {
						assert false
						return
					}
					loads += 1
					model = model.Append(mentry{key: k, value: got, born: now, used: now})
				}
			} else if op < 7 {
				let v = 100000 + draw(1000)
				cache.Put(c, k, v)
				let at = mfind(model, k)
				if at >= 0 {
					gone = gone.Append(pairOf(k, model[at].value))
					model = mdrop(model, at)
				}
				model = model.Append(mentry{key: k, value: v, born: now, used: now})
			} else if op < 8 {
				let had = cache.Invalidate(c, k)
				let at = mfind(model, k)
				if had != (at >= 0) {
					assert false
					return
				}
				if at >= 0 {
					gone = gone.Append(pairOf(k, model[at].value))
					model = mdrop(model, at)
				}
			} else if op < 9 {
				cache.Prune(c)
				let mut kept = []mentry{}
				for e in model {
					if mlive(config, e, now) {
						kept = kept.Append(e)
					} else {
						gone = gone.Append(pairOf(e.key, e.value))
					}
				}
				model = kept
			} else {
				let d = time.Times(500ms, draw(9))
				m.Advance(d)
				now = now + d
			}
			if len(model) > capacity {
				gone = gone.Append(pairOf(model[0].key, model[0].value))
				model = mdrop(model, 0)
			}
			let st = cache.StatsOf(c)
			if !sameLeft(left.Get(), gone) || st.Size != len(model) || st.Hits != hits || st.Misses != misses {
				assert false
				return
			}
		}
	}
	assert true
}
`

// stdCacheLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdCacheLawStatements = map[string]string{
	"cache NewRefusesBadConfig": `NewRefusesBadConfig(capacity int, life int64, idle int64) { let config = Config{Capacity: capacity, Life: duration(life), Idle: duration(idle)} let ok = capacity >= 1 && life >= 0 && idle >= 0 let _ = New[int, int](config) catch { case BadConfig: return !ok } ok }`,
	"cache FailureIsNotKept":    `FailureIsNotKept(k int, v int) { let m = time.NewManual(0) let c = New[int, int](Config{Capacity: 1, Life: 0s, Idle: 0s}) catch { case BadConfig: return false } let ok = with context { time.Clock: m } { let failed = Get(c, k, func(key int) int fails BadConfig { BadConfig{Reason: "down"} }) catch { case BadConfig: -1 } let mut runs = 0 let got = Get(c, k, func(key int) int { runs += 1 v }) failed == -1 && got == v && runs == 1 && StatsOf(c).Size == 1 } ok }`,
	"cache OneLookupAtOnce":     `OneLookupAtOnce(k int) { let c = New[int, int](Config{Capacity: 4, Life: 0s, Idle: 0s}) catch { case BadConfig: return false } let inflight = sync.NewRef[int](0) let most = sync.NewRef[int](0) let gate = queue.New[int](0) let m = time.NewManual(0) let ok = with context { time.Clock: m } { let a = task.Go() { Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) } let b = task.Go() { Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) } let d = task.Go() { Get(c, k, func(key int) int { gated(inflight, most, gate, key) }) } for inflight.Get() == 0 { } gate.End() let sum = a.Wait() + b.Wait() + d.Wait() sum == 3*(k%1000) && most.Get() == 1 && StatsOf(c).Misses+StatsOf(c).Hits == 3 } ok }`,
	"cache ModelAgrees":         `ModelAgrees(seed int64) { let mut s = seed let draw = func(n int) int { s = s*6364136223846793005 + 1442695040888963407 let r = (s >> 33) % int64(n) int(if r < 0 { r + int64(n) } else { r }) } let capacity = 1 + draw(4) let life = time.Times(1s, draw(2)*(1+draw(5))) let idle = time.Times(1s, draw(2)*(1+draw(5))) let config = Config{Capacity: capacity, Life: life, Idle: idle} let left = sync.NewRef[[]int]([]int{}) let c = NewReleasing[int, int](config, func(k int, v int) { left.Update(func(xs []int) []int { xs.Append(pairOf(k, v)) }) }) catch { case BadConfig: return false } let m = time.NewManual(0) let mut model = []mentry{} let mut gone = []int{} let mut now = 0s let mut loads = 0 let mut hits = 0 let mut misses = 0 with context { time.Clock: m } { for _ in 0..60 { let k = draw(5) let op = draw(10) gone = []int{} left.Set([]int{}) if op < 5 { let mut ran = false let got = Get(c, k, func(key int) int { ran = true key*1000 + loads }) let at = mfind(model, k) if at >= 0 && mlive(config, model[at], now) { let e = model[at] model = mdrop(model, at).Append(mentry{key: k, value: e.value, born: e.born, used: now}) hits += 1 if ran || got != e.value { return false } } else { if at >= 0 { gone = gone.Append(pairOf(k, model[at].value)) model = mdrop(model, at) } misses += 1 if !ran || got != k*1000+loads { return false } loads += 1 model = model.Append(mentry{key: k, value: got, born: now, used: now}) } } else if op < 7 { let v = 100000 + draw(1000) Put(c, k, v) let at = mfind(model, k) if at >= 0 { gone = gone.Append(pairOf(k, model[at].value)) model = mdrop(model, at) } model = model.Append(mentry{key: k, value: v, born: now, used: now}) } else if op < 8 { let had = Invalidate(c, k) let at = mfind(model, k) if had != (at >= 0) { return false } if at >= 0 { gone = gone.Append(pairOf(k, model[at].value)) model = mdrop(model, at) } } else if op < 9 { Prune(c) let mut kept = []mentry{} for e in model { if mlive(config, e, now) { kept = kept.Append(e) } else { gone = gone.Append(pairOf(e.key, e.value)) } } model = kept } else { let d = time.Times(500ms, draw(9)) m.Advance(d) now = now + d } if len(model) > capacity { gone = gone.Append(pairOf(model[0].key, model[0].value)) model = mdrop(model, 0) } let st = StatsOf(c) if !sameLeft(left.Get(), gone) || st.Size != len(model) || st.Hits != hits || st.Misses != misses { return false } } } true }`,
}
