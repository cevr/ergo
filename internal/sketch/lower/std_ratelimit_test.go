package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdRatelimitLawsProbe restates the laws whose runner is TestStdRatelimitLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdRatelimitLawsProbe = `import ratelimit
import ratelimit/memory
import task
import time

// provider is a memory limiter of each algorithm, by pick, over small
// limits and times, so a law's loop reaches a refusal.
func provider(pick int, n int, ms int) ratelimit.Limiter {
	let limit = 1 + ((n%4)+4)%4
	let unit = time.Times(1ms, 1+((ms%50)+50)%50)
	let rule ratelimit.Rule = match ((pick%3)+3)%3 {
	case 0:
		ratelimit.TokenBucket{Capacity: limit, Every: unit}
	case 1:
		ratelimit.FixedWindow{Limit: limit, Window: unit}
	case _:
		ratelimit.SlidingWindow{Limit: limit, Window: unit}
	}
	memory.New(rule) catch {
	case ratelimit.BadRule:
		Unreachable("every drawn rule has a limit and a time above zero")
	}
}

test RatelimitLimiterRefusalChangesNothing(pick int, n int, ms int, key string, cost int, sec int64) {
	let l = provider(pick, n, ms)
	let c = 1 + ((cost%3)+3)%3
	let now = time.NewTime(sec, 0)
	for _ in 0..10000 {
		l.TakeAt(key, c, now) catch {
		case ratelimit.Limited as first:
			let again = l.TakeAt(key, c, now) catch {
			case ratelimit.Limited as second:
				assert second.RetryAfter == first.RetryAfter
				return
			case ratelimit.BadCost:
				assert false
				return
			}
			assert again.Remaining < 0
			return
		case ratelimit.BadCost:
			return
		}
	}
}

test RatelimitLimiterRetryAfterIsExact(pick int, n int, ms int, key string, cost int, sec int64) {
	let l = provider(pick, n, ms)
	let c = 1 + ((cost%3)+3)%3
	let now = time.NewTime(sec, 0)
	for _ in 0..10000 {
		l.TakeAt(key, c, now) catch {
		case ratelimit.Limited as x:
			assert x.RetryAfter > 0ns
			let early = l.TakeAt(key, c, now.Add(x.RetryAfter-1ns)) catch {
			case ratelimit.Limited:
				let late = l.TakeAt(key, c, now.Add(x.RetryAfter)) catch {
				case ratelimit.Limited, ratelimit.BadCost:
					assert false
					return
				}
				assert late.Remaining >= 0
				return
			case ratelimit.BadCost:
				assert false
				return
			}
			assert early.Remaining < 0
			return
		case ratelimit.BadCost:
			return
		}
	}
}

test RatelimitLimiterCostBelowOneIsBad(pick int, n int, ms int, key string, cost int, sec int64) {
	let l = provider(pick, n, ms)
	let c = -(((cost % 1000) + 1000) % 1000)
	let g = l.TakeAt(key, c, time.NewTime(sec, 0)) catch {
	case ratelimit.BadCost:
		return
	case ratelimit.Limited:
		assert false
		return
	}
	assert g.Remaining < 0
}

test RatelimitCheckRefusesBadRules(pick int, n int, d duration) {
	let rule ratelimit.Rule = match ((pick%3)+3)%3 {
	case 0:
		ratelimit.TokenBucket{Capacity: n, Every: d}
	case 1:
		ratelimit.FixedWindow{Limit: n, Window: d}
	case _:
		ratelimit.SlidingWindow{Limit: n, Window: d}
	}
	let kept = ratelimit.Check(rule) catch {
	case ratelimit.BadRule:
		assert n < 1 || d <= 0ns
		return
	}
	assert n >= 1 && d > 0ns && kept == rule && ratelimit.LimitOf(rule) == n
}

// The model of MemoryAgreesWithTheModel, as memory.ergo declares it.
type mgrant { key int, at duration, cost int }

type level { tokens int, since duration }

type mout = mgranted{Remaining int} | mrefused{RetryAfter duration}

func fill(b ratelimit.TokenBucket, lv level, d duration) level {
	let total = lv.since + d
	let n = time.Count(total, b.Every)
	if lv.tokens+n >= b.Capacity {
		return level{tokens: b.Capacity, since: 0ns}
	}
	level{tokens: lv.tokens + n, since: total - time.Times(b.Every, n)}
}

func model(rule ratelimit.Rule, grants []mgrant, key int, now duration, cost int) mout {
	match rule {
	case ratelimit.TokenBucket as b:
		let mut lv = level{tokens: b.Capacity, since: 0ns}
		let mut last = 0ns
		for g in grants {
			if g.key == key {
				lv = fill(b, lv, g.at-last)
				lv = level{tokens: lv.tokens - g.cost, since: lv.since}
				last = g.at
			}
		}
		lv = fill(b, lv, now-last)
		if lv.tokens >= cost {
			return mgranted{Remaining: lv.tokens - cost}
		}
		mrefused{RetryAfter: time.Times(b.Every, cost-lv.tokens) - lv.since}
	case ratelimit.FixedWindow as f:
		let mut open = false
		let mut start = 0ns
		let mut used = 0
		for g in grants {
			if g.key == key {
				if !open || g.at-start >= f.Window {
					open = true
					start = g.at
					used = 0
				}
				used += g.cost
			}
		}
		if !open || now-start >= f.Window {
			start = now
			used = 0
		}
		if used+cost <= f.Limit {
			return mgranted{Remaining: f.Limit - used - cost}
		}
		mrefused{RetryAfter: f.Window - (now - start)}
	case ratelimit.SlidingWindow as s:
		let inWindow = func(u duration, after duration) int {
			let mut n = 0
			for g in grants {
				if g.key == key && u-g.at < s.Window && g.at > after {
					n += g.cost
				}
			}
			n
		}
		if inWindow(now, -1ns)+cost <= s.Limit {
			return mgranted{Remaining: s.Limit - inWindow(now, -1ns) - cost}
		}
		for g in grants {
			if g.key == key && now-g.at < s.Window && inWindow(now, g.at)+cost <= s.Limit {
				return mrefused{RetryAfter: g.at + s.Window - now}
			}
		}
		mrefused{RetryAfter: -1ns}
	}
}

test MemoryAgreesWithTheModel(seed int64) {
	let mut s = seed
	let draw = func(n int) int {
		s = s*6364136223846793005 + 1442695040888963407
		let r = (s >> 33) % int64(n)
		int(if r < 0 { r + int64(n) } else { r })
	}
	let unit = time.Times(1ms, 1+draw(20))
	let n = 1 + draw(4)
	let rule ratelimit.Rule = match draw(3) {
	case 0:
		ratelimit.TokenBucket{Capacity: n, Every: unit}
	case 1:
		ratelimit.FixedWindow{Limit: n, Window: time.Times(unit, n)}
	case _:
		ratelimit.SlidingWindow{Limit: n, Window: time.Times(unit, n)}
	}
	let l = memory.New(rule) catch {
	case ratelimit.BadRule:
		assert false
		return
	}
	let start = time.Unix(seed%1000000, 0)
	let mut grants = []mgrant{}
	let mut now = 0ns
	let mut wait = 0ns
	for _ in 0..80 {
		let op = draw(10)
		if op < 6 {
			let key = draw(2)
			let cost = draw(n + 2)
			let mut retry = 0ns
			let mut bad = false
			let g = memory.TakeAt(l, if key == 0 { "a" } else { "b" }, cost, start.Add(now)) catch {
			case ratelimit.Limited as x:
				retry = x.RetryAfter
				ratelimit.Grant{Remaining: -1}
			case ratelimit.BadCost:
				bad = true
				ratelimit.Grant{Remaining: -1}
			}
			if cost < 1 || cost > n {
				assert bad
			} else {
				match model(rule, grants, key, now, cost) {
				case mgranted as w:
					assert !bad && g.Remaining == w.Remaining
					grants = grants.Append(mgrant{key: key, at: now, cost: cost})
				case mrefused as w:
					assert !bad && g.Remaining == -1 && retry == w.RetryAfter
					wait = w.RetryAfter
				}
			}
		} else if op < 8 {
			now = now + time.Times(1ms, draw(3*time.Count(unit, 1ms)))
		} else if op < 9 {
			now = now + wait
		} else if wait > 1ns {
			now = now + wait - 1ns
		}
	}
}

test MemoryWaitSleepsTheRetryAfter(n int) {
	let d = time.Times(1ns, 2+((n%1000)+1000)%1000)
	let l = memory.New(ratelimit.TokenBucket{Capacity: 1, Every: d}) catch {
	case ratelimit.BadRule:
		assert false
		return
	}
	let m = time.NewManual(0)
	let ok = with context { time.Clock: m, Scheduler: m } {
		let first = ratelimit.Take(l, "k", 1) catch {
		case ratelimit.Limited, ratelimit.BadCost:
			ratelimit.Grant{Remaining: -1}
		}
		let t = task.Go() {
			ratelimit.Wait(l, "k", 1) catch {
			case ratelimit.BadCost:
				ratelimit.Grant{Remaining: -1}
			}
		}
		m.Advance(d - 1ns)
		let asleep = m.Sleepers() == 1
		m.Advance(1ns)
		first.Remaining == 0 && asleep && t.Wait().Remaining == 0
	}
	assert ok
}

test MemoryLateTakeCountsAsTheLastGrant(seed int64, back int) {
	let rules = []ratelimit.Rule{ratelimit.TokenBucket{Capacity: 2, Every: 10ms}, ratelimit.FixedWindow{Limit: 2, Window: 20ms}, ratelimit.SlidingWindow{Limit: 2, Window: 20ms}}
	let t = time.Unix(seed%1000000, 0)
	let early = t.Add(time.Times(1ms, -1-((back%50)+50)%50))
	for rule in rules {
		let late = memory.New(rule) catch {
		case ratelimit.BadRule:
			assert false
			return
		}
		let ontime = memory.New(rule) catch {
		case ratelimit.BadRule:
			assert false
			return
		}
		for at in []time.Time{t, early, early, t} {
			let a = memory.TakeAt(late, "k", 1, at) catch {
			case ratelimit.Limited as x:
				ratelimit.Grant{Remaining: -1 - int(int64(x.RetryAfter - t.Sub(at)))}
			case ratelimit.BadCost:
				ratelimit.Grant{Remaining: 1000}
			}
			let b = memory.TakeAt(ontime, "k", 1, t) catch {
			case ratelimit.Limited as x:
				ratelimit.Grant{Remaining: -1 - int(int64(x.RetryAfter))}
			case ratelimit.BadCost:
				ratelimit.Grant{Remaining: 2000}
			}
			assert a == b
		}
	}
}
`

// stdRatelimitLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdRatelimitLawStatements = map[string]string{
	"ratelimit CheckRefusesBadRules":                `CheckRefusesBadRules(pick int, n int, d duration) { let rule Rule = match ((pick%3)+3)%3 { case 0: TokenBucket{Capacity: n, Every: d} case 1: FixedWindow{Limit: n, Window: d} case _: SlidingWindow{Limit: n, Window: d} } let kept = Check(rule) catch { case BadRule: return n < 1 || d <= 0ns } n >= 1 && d > 0ns && kept == rule && LimitOf(rule) == n }`,
	"ratelimit Limiter.RefusalChangesNothing":       `RefusalChangesNothing(key string, cost int, sec int64) { let c = 1 + ((cost%3)+3)%3 let now = time.NewTime(sec, 0) for _ in 0..10000 { TakeAt(key, c, now) catch { case Limited as first: let again = TakeAt(key, c, now) catch { case Limited as second: return second.RetryAfter == first.RetryAfter case BadCost: return false } return again.Remaining < 0 case BadCost: return true } } true }`,
	"ratelimit Limiter.RetryAfterIsExact":           `RetryAfterIsExact(key string, cost int, sec int64) { let c = 1 + ((cost%3)+3)%3 let now = time.NewTime(sec, 0) for _ in 0..10000 { TakeAt(key, c, now) catch { case Limited as l: if l.RetryAfter <= 0ns { return false } let early = TakeAt(key, c, now.Add(l.RetryAfter-1ns)) catch { case Limited: let late = TakeAt(key, c, now.Add(l.RetryAfter)) catch { case Limited, BadCost: return false } return late.Remaining >= 0 case BadCost: return false } return early.Remaining < 0 case BadCost: return true } } true }`,
	"ratelimit Limiter.CostBelowOneIsBad":           `CostBelowOneIsBad(key string, cost int, sec int64) { let c = -(((cost % 1000) + 1000) % 1000) let g = TakeAt(key, c, time.NewTime(sec, 0)) catch { case BadCost: return true case Limited: return false } g.Remaining < 0 }`,
	"ratelimit/memory AgreesWithTheModel":           `AgreesWithTheModel(seed int64) { let mut s = seed let draw = func(n int) int { s = s*6364136223846793005 + 1442695040888963407 let r = (s >> 33) % int64(n) int(if r < 0 { r + int64(n) } else { r }) } let unit = time.Times(1ms, 1+draw(20)) let n = 1 + draw(4) let rule ratelimit.Rule = match draw(3) { case 0: ratelimit.TokenBucket{Capacity: n, Every: unit} case 1: ratelimit.FixedWindow{Limit: n, Window: time.Times(unit, n)} case _: ratelimit.SlidingWindow{Limit: n, Window: time.Times(unit, n)} } let l = New(rule) catch { case ratelimit.BadRule: return false } let start = time.Unix(seed%1000000, 0) let mut grants = []mgrant{} let mut now = 0ns let mut wait = 0ns for _ in 0..80 { let op = draw(10) if op < 6 { let key = draw(2) let cost = draw(n + 2) let mut retry = 0ns let mut bad = false let g = TakeAt(l, if key == 0 { "a" } else { "b" }, cost, start.Add(now)) catch { case ratelimit.Limited as x: retry = x.RetryAfter ratelimit.Grant{Remaining: -1} case ratelimit.BadCost: bad = true ratelimit.Grant{Remaining: -1} } if cost < 1 || cost > n { if !bad { return false } } else { match model(rule, grants, key, now, cost) { case granted as w: if bad || g.Remaining != w.Remaining { return false } grants = grants.Append(mgrant{key: key, at: now, cost: cost}) case refused as w: if bad || g.Remaining != -1 || retry != w.RetryAfter { return false } wait = w.RetryAfter } } } else if op < 8 { now = now + time.Times(1ms, draw(3*time.Count(unit, 1ms))) } else if op < 9 { now = now + wait } else if wait > 1ns { now = now + wait - 1ns } } true }`,
	"ratelimit/memory WaitSleepsTheRetryAfter":      `WaitSleepsTheRetryAfter(n int) { let d = time.Times(1ns, 2+((n%1000)+1000)%1000) let l = New(ratelimit.TokenBucket{Capacity: 1, Every: d}) catch { case ratelimit.BadRule: return false } let m = time.NewManual(0) with context { time.Clock: m, Scheduler: m } { let first = ratelimit.Take(l, "k", 1) catch { case ratelimit.Limited, ratelimit.BadCost: return false } let t = task.Go() { ratelimit.Wait(l, "k", 1) catch { case ratelimit.BadCost: ratelimit.Grant{Remaining: -1} } } m.Advance(d - 1ns) let asleep = m.Sleepers() == 1 m.Advance(1ns) first.Remaining == 0 && asleep && t.Wait().Remaining == 0 } }`,
	"ratelimit/memory LateTakeCountsAsTheLastGrant": `LateTakeCountsAsTheLastGrant(seed int64, back int) { let rules = []ratelimit.Rule{ratelimit.TokenBucket{Capacity: 2, Every: 10ms}, ratelimit.FixedWindow{Limit: 2, Window: 20ms}, ratelimit.SlidingWindow{Limit: 2, Window: 20ms}} let t = time.Unix(seed%1000000, 0) let early = t.Add(time.Times(1ms, -1-((back%50)+50)%50)) let mut ok = true for rule in rules { let late = New(rule) catch { case ratelimit.BadRule: return false } let ontime = New(rule) catch { case ratelimit.BadRule: return false } for at in []time.Time{t, early, early, t} { let a = TakeAt(late, "k", 1, at) catch { case ratelimit.Limited as x: ratelimit.Grant{Remaining: -1 - int(int64(x.RetryAfter - t.Sub(at)))} case ratelimit.BadCost: return false } let b = TakeAt(ontime, "k", 1, t) catch { case ratelimit.Limited as x: ratelimit.Grant{Remaining: -1 - int(int64(x.RetryAfter))} case ratelimit.BadCost: return false } ok = ok && a == b } } ok }`,
}

// stdRatelimitLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdRatelimitLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdRatelimitLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs; WaitSleepsTheRetryAfter runs a task on a manual clock, which only the runtime has.
func TestStdRatelimitLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdratelimit"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdratelimit failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdRatelimitLawStatements, stdRatelimitLawTestName, stdRatelimitLawsProbe, stdout))
}
