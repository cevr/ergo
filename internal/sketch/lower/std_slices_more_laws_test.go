package lower

// Generated from the laws of internal/sketch/std/slices by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdSlicesMoreLawsProbe restates the slices laws whose runner is
// TestStdSlicesMoreLawsRun as Ergo tests over std slices.
const stdSlicesMoreLawsProbe = `import slices

test SlicesFlatMapIsConcatOfTransform(xs []int) {
	let f = func(x int) []int { slices.Repeat([]int{x}, (x%3+3)%3) }
	let mut joined = []int{}
	for ys in slices.Transform(xs, f) {
		joined = slices.Concat(joined, ys)
	}
	assert slices.Equal(slices.FlatMap(xs, f), joined)
}

test SlicesZipLen(a []int, b []string) {
	let z = slices.Zip(a, b)
	let n = if len(a) < len(b) { len(a) } else { len(b) }
	let mut ok = len(z) == n
	for i, p in z {
		let (x, y) = p
		ok = ok && x == a[i] && y == b[i]
	}
	assert ok
}

test SlicesFilterMapIsFilterThenMap(xs []int) {
	let even = func(x int) bool { x%2 == 0 }
	let triple = func(x int) int { x * 3 }
	let f = func(x int) Option[int] { if even(x) { Some[int]{Value: triple(x)} } else { None } }
	assert slices.Equal(slices.FilterMap(xs, f), slices.Transform(slices.Filter(xs, even), triple))
}

test SlicesFindAnyAllAgree(xs []int, k int) {
	let m = (k%5+5)%5 + 1
	let accept = func(x int) bool { x%m == 0 }
	let kept = slices.Filter(xs, accept)
	let first Option[int] = if len(kept) == 0 { None } else { Some[int]{Value: kept[0]} }
	assert slices.Find(xs, accept) == first && slices.Any(xs, accept) == (len(kept) > 0) && slices.All(xs, accept) == (len(kept) == len(xs))
}

test SlicesPartitionIsFilterTwice(xs []int) {
	let keep = func(x int) bool { x%3 == 0 }
	let (yes, no) = slices.Partition(xs, keep)
	assert slices.Equal(yes, slices.Filter(xs, keep)) && slices.Equal(no, slices.Filter(xs, func(x int) bool { !keep(x) }))
}

test SlicesDedupeKeepsFirst(xs []int) {
	let d = slices.Dedupe(xs)
	let mut ok = true
	for x in xs {
		ok = ok && slices.Contains(d, x)
	}
	for i, x in d {
		ok = ok && slices.Index(d, x) == i
		if i > 0 {
			ok = ok && slices.Index(xs, d[i-1]) < slices.Index(xs, x)
		}
	}
	assert ok
}

test SlicesChunksConcat(xs []int, k int) {
	let n = (k%5+5)%5 + 1
	let cs = slices.Chunks(xs, n)
	let mut joined = []int{}
	let mut ok = true
	for i, c in cs {
		joined = slices.Concat(joined, c)
		ok = ok && len(c) >= 1 && (i == len(cs)-1 || len(c) == n)
	}
	assert ok && slices.Equal(joined, xs)
}

test SlicesWindowsSlide(xs []int, k int) {
	let n = (k%4+4)%4 + 1
	let ws = slices.Windows(xs, n)
	let want = if len(xs) < n { 0 } else { len(xs) - n + 1 }
	let mut ok = len(ws) == want
	for i, w in ws {
		ok = ok && slices.Equal(w, slices.Slice(xs, i, i+n))
	}
	assert ok
}

test SlicesGroupByPartitions(xs []int) {
	let key = func(x int) int { (x%3 + 3) % 3 }
	let g = slices.GroupBy(xs, key)
	let mut total = 0
	let mut ok = true
	for k, group in g {
		total = total + len(group)
		ok = ok && slices.Equal(group, slices.Filter(xs, func(x int) bool { key(x) == k }))
	}
	assert ok && total == len(xs)
}
`

// stdSlicesMoreLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdSlicesMoreLawStatements = map[string]string{
	"slices FlatMapIsConcatOfTransform": `FlatMapIsConcatOfTransform(xs []int) { let f = func(x int) []int { Repeat([]int{x}, (x%3+3)%3) } let mut joined = []int{} for ys in Transform(xs, f) { joined = Concat(joined, ys) } Equal(FlatMap(xs, f), joined) }`,
	"slices ZipLen":                     `ZipLen(a []int, b []string) { let z = Zip(a, b) let n = if len(a) < len(b) { len(a) } else { len(b) } let mut ok = len(z) == n for i, p in z { let (x, y) = p ok = ok && x == a[i] && y == b[i] } ok }`,
	"slices FilterMapIsFilterThenMap":   `FilterMapIsFilterThenMap(xs []int) { let even = func(x int) bool { x%2 == 0 } let triple = func(x int) int { x * 3 } let f = func(x int) Option[int] { if even(x) { Some[int]{Value: triple(x)} } else { None } } Equal(FilterMap(xs, f), Transform(Filter(xs, even), triple)) }`,
	"slices FindAnyAllAgree":            `FindAnyAllAgree(xs []int, k int) { let m = (k%5+5)%5 + 1 let accept = func(x int) bool { x%m == 0 } let kept = Filter(xs, accept) let first Option[int] = if len(kept) == 0 { None } else { Some[int]{Value: kept[0]} } Find(xs, accept) == first && Any(xs, accept) == (len(kept) > 0) && All(xs, accept) == (len(kept) == len(xs)) }`,
	"slices PartitionIsFilterTwice":     `PartitionIsFilterTwice(xs []int) { let keep = func(x int) bool { x%3 == 0 } let (yes, no) = Partition(xs, keep) Equal(yes, Filter(xs, keep)) && Equal(no, Filter(xs, func(x int) bool { !keep(x) })) }`,
	"slices DedupeKeepsFirst":           `DedupeKeepsFirst(xs []int) { let d = Dedupe(xs) let mut ok = true for x in xs { ok = ok && Contains(d, x) } for i, x in d { ok = ok && Index(d, x) == i if i > 0 { ok = ok && Index(xs, d[i-1]) < Index(xs, x) } } ok }`,
	"slices ChunksConcat":               `ChunksConcat(xs []int, k int) { let n = (k%5+5)%5 + 1 let cs = Chunks(xs, n) let mut joined = []int{} let mut ok = true for i, c in cs { joined = Concat(joined, c) ok = ok && len(c) >= 1 && (i == len(cs)-1 || len(c) == n) } ok && Equal(joined, xs) }`,
	"slices WindowsSlide":               `WindowsSlide(xs []int, k int) { let n = (k%4+4)%4 + 1 let ws = Windows(xs, n) let want = if len(xs) < n { 0 } else { len(xs) - n + 1 } let mut ok = len(ws) == want for i, w in ws { ok = ok && Equal(w, Slice(xs, i, i+n)) } ok }`,
	"slices GroupByPartitions":          `GroupByPartitions(xs []int) { let key = func(x int) int { (x%3 + 3) % 3 } let g = GroupBy(xs, key) let mut total = 0 let mut ok = true for k, group in g { total = total + len(group) ok = ok && Equal(group, Filter(xs, func(x int) bool { key(x) == k })) } ok && total == len(xs) }`,
}
