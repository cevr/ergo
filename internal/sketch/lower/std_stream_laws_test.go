package lower

// Generated from the laws of internal/sketch/std/stream by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdStreamLawsProbe restates the stream laws whose runner is
// TestStdStreamLawsRun as Ergo tests over std stream.
const stdStreamLawsProbe = `import queue
import stream
import task
import time

// gapAt is the gap before the i-th value, in milliseconds: gaps[i] mod 50,
// or 0 past the end of gaps.
func gapAt(gaps []int, i int) int {
	if i < len(gaps) {
		((gaps[i] % 50) + 50) % 50
	} else {
		0
	}
}

// window is w as a window from 0 to 59 milliseconds: 0 keeps every value.
func window(w int) int { ((w % 60) + 60) % 60 }

// size is k as a chunk size from 0 to 5.
func size(k int) int { ((k % 6) + 6) % 6 }

// millis is n milliseconds.
func millis(n int) duration { time.Times(1ms, n) }

// arrivals is xs as a Seq on m: before each value it moves m on by the
// value's gap.
func arrivals(m time.Manual, gaps []int, xs []int) Seq[int] {
	Seq[int](func(yield func(v int) bool) {
		for i, x in xs {
			m.Advance(millis(gapAt(gaps, i)))
			if !yield(x) {
				return
			}
		}
	})
}

// arrivalsAt is the arrival time of each of n values, in milliseconds
// from the first run's start.
func arrivalsAt(gaps []int, n int) []int {
	let mut at = []int{}
	let mut t = 0
	for i in 0..n {
		t = t + gapAt(gaps, i)
		at = at.Append(t)
	}
	at
}

// throttleModel keeps xs[0] and each xs[i] whose arrival is d or more
// after the last kept one's.
func throttleModel(at []int, xs []int, d int) []int {
	let mut out = []int{}
	let mut last = 0
	for i, x in xs {
		if i == 0 || at[i]-last >= d {
			out = out.Append(x)
			last = at[i]
		}
	}
	out
}

// debounceModel keeps each xs[i] that the next value arrives d or more
// after, and the last.
func debounceModel(at []int, xs []int, d int) []int {
	let mut out = []int{}
	for i, x in xs {
		if i == len(xs)-1 || at[i+1]-at[i] >= d {
			out = out.Append(x)
		}
	}
	out
}

// groupedModel is the chunks of xs: one closes at n values or before a
// value arriving d or more after its first.
func groupedModel(at []int, xs []int, n int, d int) [][]int {
	let mut out = [][]int{}
	let mut chunk = []int{}
	let mut start = 0
	for i, x in xs {
		if len(chunk) > 0 && at[i]-start >= d {
			out = out.Append(chunk)
			chunk = []int{}
		}
		if len(chunk) == 0 {
			start = at[i]
		}
		chunk = chunk.Append(x)
		if len(chunk) >= n {
			out = out.Append(chunk)
			chunk = []int{}
		}
	}
	if len(chunk) > 0 {
		out = out.Append(chunk)
	}
	out
}

// debouncedModel is what Debounced emits for values xs arriving at at
// (ms) with window d and src ending at end, as value, time pairs: each
// xs[i] whose next value arrives more than d later, at at[i]+d, and the
// last at at[i]+d when src ends more than d after it, else at end.
func debouncedModel(at []int, xs []int, d int, end int) []int {
	let mut out = []int{}
	for i, x in xs {
		if i == len(xs)-1 {
			out = out.Append(x).Append(if end-at[i] > d { at[i] + d } else { end })
		} else if at[i+1]-at[i] > d {
			out = out.Append(x).Append(at[i] + d)
		}
	}
	out
}

// doubled, in milliseconds.
func evenArrivals(gaps []int, n int) []int {
	let mut at = []int{}
	let mut t = 0
	for i in 0..n {
		t = t + 2*gapAt(gaps, i)
		at = at.Append(t)
	}
	at
}

// lastOf is the last value of at, or 0 when at is empty.
func lastOf(at []int) int {
	if len(at) == 0 {
		0
	} else {
		at[len(at)-1]
	}
}

// collect is the values of s.
func collect(s Seq[int]) []int {
	let mut out = []int{}
	for v in s {
		out = out.Append(v)
	}
	out
}

// collectChunks is the chunks of s.
func collectChunks(s Seq[[]int]) [][]int {
	let mut out = [][]int{}
	for c in s {
		out = out.Append(c)
	}
	out
}

// sameInts reports that a and b hold the same values in order.
func sameInts(a []int, b []int) bool {
	let mut same = len(a) == len(b)
	for i in 0..len(a) {
		same = same && i < len(b) && a[i] == b[i]
	}
	same
}

// sameChunks reports that a and b hold the same chunks in order.
func sameChunks(a [][]int, b [][]int) bool {
	let mut same = len(a) == len(b)
	for i in 0..len(a) {
		same = same && i < len(b) && sameInts(a[i], b[i])
	}
	same
}

// stops: s is a Seq that stops (stdlib.md, Std laws 5): with n the count
// of its values, a yield that returns false at its k-th call (k from 1 to
// n+1) is called min(k, n) times.
func stops[T](s Seq[T]) bool {
	let mut n = 0
	s(func(v T) bool {
		n = n + 1
		true
	})
	for k in 1..n+2 {
		let mut calls = 0
		s(func(v T) bool {
			calls = calls + 1
			calls < k
		})
		if calls != (if k < n { k } else { n }) {
			return false
		}
	}
	true
}

test StreamThrottleMatchesModel(gaps []int, xs []int, w int) {
	let m = time.NewManual(0)
	assert sameInts(collect(stream.Throttle(arrivals(m, gaps, xs), millis(window(w)), m)), throttleModel(arrivalsAt(gaps, len(xs)), xs, window(w)))
}

test StreamDebounceMatchesModel(gaps []int, xs []int, w int) {
	let m = time.NewManual(0)
	assert sameInts(collect(stream.Debounce(arrivals(m, gaps, xs), millis(window(w)), m)), debounceModel(arrivalsAt(gaps, len(xs)), xs, window(w)))
}

test StreamGroupedWithinMatchesModel(gaps []int, xs []int, w int, k int) {
	let m = time.NewManual(0)
	assert sameChunks(collectChunks(stream.GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m)), groupedModel(arrivalsAt(gaps, len(xs)), xs, size(k), window(w)))
}

test StreamGroupedWithinKeepsEveryValue(gaps []int, xs []int, w int, k int) {
	let m = time.NewManual(0)
	let mut joined = []int{}
	let mut empty = false
	for c in stream.GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m) {
		empty = empty || len(c) == 0
		for x in c {
			joined = joined.Append(x)
		}
	}
	assert !empty && sameInts(joined, xs)
}

test StreamThrottleStops(gaps []int, xs []int, w int) {
	let m = time.NewManual(0)
	assert stops(stream.Throttle(arrivals(m, gaps, xs), millis(window(w)), m))
}

test StreamDebouncedMatchesModel(gaps []int, xs []int, w int) {
	let m = time.NewManual(0)
	let d = 2*(window(w)%30) + 1
	let tail = 2*(((w/7)%50 + 50)%50)
	let n = len(xs)
	let start = m.Now()
	let ok = with context { Scheduler: m, time.Clock: m } {
		let src = queue.New[int](n + 1)
		let out = queue.New[int](n + 1)
		let seen = queue.New[int](2*n + 1)
		let producer = task.Go() {
			for i, x in xs {
				Scheduler.Sleep(millis(2 * gapAt(gaps, i)))
				src.Push(x) catch {
				case queue.Closed:
				}
			}
			Scheduler.Sleep(millis(tail))
			src.End()
		}
		let debouncer = task.Go() {
			stream.Debounced(src, millis(d), out) catch {
			case queue.Closed:
			}
		}
		let reader = task.Go() {
			for v in out.Values() {
				seen.Push(v) catch {
				case queue.Closed:
				}
				seen.Push(time.Count(m.Elapsed(start), 1ms)) catch {
				case queue.Closed:
				}
			}
			seen.End()
		}
		let at = evenArrivals(gaps, n)
		m.Advance(millis(lastOf(at) + tail + d + 1))
		producer.Wait()
		debouncer.Wait()
		reader.Wait()
		sameInts(collect(seen.Values()), debouncedModel(at, xs, d, lastOf(at) + tail))
	}
	assert ok
}

test StreamDebounceStops(gaps []int, xs []int, w int) {
	let m = time.NewManual(0)
	assert stops(stream.Debounce(arrivals(m, gaps, xs), millis(window(w)), m))
}

test StreamGroupedWithinStops(gaps []int, xs []int, w int, k int) {
	let m = time.NewManual(0)
	assert stops(stream.GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m))
}
`

// stdStreamLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdStreamLawStatements = map[string]string{
	"stream ThrottleMatchesModel":         `ThrottleMatchesModel(gaps []int, xs []int, w int) { let m = time.NewManual(0) sameInts(collect(Throttle(arrivals(m, gaps, xs), millis(window(w)), m)), throttleModel(arrivalsAt(gaps, len(xs)), xs, window(w))) }`,
	"stream DebounceMatchesModel":         `DebounceMatchesModel(gaps []int, xs []int, w int) { let m = time.NewManual(0) sameInts(collect(Debounce(arrivals(m, gaps, xs), millis(window(w)), m)), debounceModel(arrivalsAt(gaps, len(xs)), xs, window(w))) }`,
	"stream GroupedWithinMatchesModel":    `GroupedWithinMatchesModel(gaps []int, xs []int, w int, k int) { let m = time.NewManual(0) sameChunks(collectChunks(GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m)), groupedModel(arrivalsAt(gaps, len(xs)), xs, size(k), window(w))) }`,
	"stream GroupedWithinKeepsEveryValue": `GroupedWithinKeepsEveryValue(gaps []int, xs []int, w int, k int) { let m = time.NewManual(0) let mut joined = []int{} let mut empty = false for c in GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m) { empty = empty || len(c) == 0 for x in c { joined = joined.Append(x) } } !empty && sameInts(joined, xs) }`,
	"stream ThrottleStops":                `ThrottleStops(gaps []int, xs []int, w int) { let m = time.NewManual(0) stops(Throttle(arrivals(m, gaps, xs), millis(window(w)), m)) }`,
	"stream DebouncedMatchesModel":        `DebouncedMatchesModel(gaps []int, xs []int, w int) { let m = time.NewManual(0) let d = 2*(window(w)%30) + 1 let tail = 2*(((w/7)%50 + 50)%50) let n = len(xs) let start = m.Now() with context { Scheduler: m, time.Clock: m } { let src = queue.New[int](n + 1) let out = queue.New[int](n + 1) let seen = queue.New[int](2*n + 1) let producer = task.Go() { for i, x in xs { Scheduler.Sleep(millis(2 * gapAt(gaps, i))) src.Push(x) catch { case queue.Closed: } } Scheduler.Sleep(millis(tail)) src.End() } let debouncer = task.Go() { Debounced(src, millis(d), out) catch { case queue.Closed: } } let reader = task.Go() { for v in out.Values() { seen.Push(v) catch { case queue.Closed: } seen.Push(time.Count(m.Elapsed(start), 1ms)) catch { case queue.Closed: } } seen.End() } let at = evenArrivals(gaps, n) m.Advance(millis(lastOf(at) + tail + d + 1)) producer.Wait() debouncer.Wait() reader.Wait() sameInts(collect(seen.Values()), debouncedModel(at, xs, d, lastOf(at) + tail)) } }`,
	"stream DebounceStops":                `DebounceStops(gaps []int, xs []int, w int) { let m = time.NewManual(0) stops(Debounce(arrivals(m, gaps, xs), millis(window(w)), m)) }`,
	"stream GroupedWithinStops":           `GroupedWithinStops(gaps []int, xs []int, w int, k int) { let m = time.NewManual(0) stops(GroupedWithin(arrivals(m, gaps, xs), size(k), millis(window(w)), m)) }`,
}
