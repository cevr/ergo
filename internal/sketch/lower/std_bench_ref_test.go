package lower

import (
	"regexp"
	"testing"
)

// stdBenchRefProgram holds std bench's signed-rank p-value and
// Hodges-Lehmann interval to a reference model at the sizes the laws do
// not reach: the laws draw at most lawPairs() pairs so that each decides
// inside check time's budget, and this property draws 13 to MaxPairs
// (BENCH-PROP). The reference is BN1's code before BENCH-LAW-COST,
// verbatim but for its names: the whole null table built by halving, the
// cubic path. Draws are many-tied whole sizes (zeros, runs of every
// length) or untied reals, at levels from 0 to 99.9%, and every
// comparison is ==, as the new tables are the old ones bit for bit.
const stdBenchRefProgram = `import bench
import cmp
import math
import slices

type refSigned { size float64, up bool }

func refSignedRankP(d []float64) float64 {
	let mut s = []refSigned{}
	for x in d {
		if x != 0.0 {
			s = s.Append(refSigned{size: math.Abs(x), up: x > 0.0})
		}
	}
	let n = len(s)
	if n == 0 {
		return 1.0
	}
	let sorted = slices.SortFunc(s, func(a refSigned, b refSigned) int { cmp.Compare(a.size, b.size) })
	let mut ranks = []int{}
	let mut t2 = 0
	let mut i = 0
	for i < n {
		let mut j = i
		for j+1 < n && sorted[j+1].size == sorted[i].size {
			j = j + 1
		}
		for k in i..j+1 {
			let r = i + j + 2
			ranks = ranks.Append(r)
			if sorted[k].up {
				t2 = t2 + r
			}
		}
		i = j + 1
	}
	let null = refNullOf(ranks)
	let top = len(null) - 1
	let t = if t2 <= top-t2 { t2 } else { top - t2 }
	let mut tail = 0.0
	for k in 0..t+1 {
		tail = tail + null[k]
	}
	let p = 2.0 * tail
	if p < 1.0 { p } else { 1.0 }
}

func refNullOf(ranks []int) []float64 {
	let mut top = 0
	for r in ranks {
		top = top + r
	}
	let mut c = []float64{}
	for _ in 0..top+1 {
		c = c.Append(0.0)
	}
	c[0] = 1.0
	let mut reach = 0
	for r in ranks {
		reach = reach + r
		let mut s = reach
		for s >= 0 {
			let below = if s >= r { c[s-r] } else { 0.0 }
			c[s] = 0.5 * (c[s] + below)
			s = s - 1
		}
	}
	c
}

func refHodgesLehmann(d []float64, level float64) bench.Estimate {
	let n = len(d)
	if n == 0 {
		return bench.Estimate{Center: 0.0, Low: 0.0, High: 0.0, Coverage: 0.0}
	}
	let mut walsh = []float64{}
	for i in 0..n {
		for j in i..n {
			walsh = walsh.Append((d[i] + d[j]) / 2.0)
		}
	}
	let w = slices.Sort(walsh)
	let m = len(w)
	let center = if m%2 == 1 { w[m/2] } else { (w[m/2-1] + w[m/2]) / 2.0 }
	let mut ranks = []int{}
	for k in 1..n+1 {
		ranks = ranks.Append(k)
	}
	let null = refNullOf(ranks)
	let half = (1.0 - level) / 2.0
	let mut acc = 0.0
	let mut c = -1
	let mut k = 0
	for k < len(null) && acc+null[k] <= half {
		acc = acc + null[k]
		c = k
		k = k + 1
	}
	if c < 0 {
		return bench.Estimate{Center: center, Low: w[0], High: w[m-1], Coverage: 1.0 - 2.0*null[0]}
	}
	bench.Estimate{Center: center, Low: w[c], High: w[m-1-c], Coverage: 1.0 - 2.0*acc}
}

func within(n int, lo int, hi int) int {
	let span = hi - lo + 1
	let r = n % span
	if r < 0 { lo + r + span } else { lo + r }
}

func mix(x uint64) uint64 {
	let mut z = x + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^ (z >> 31)
}

func draw(seed uint64, k int, n int) int { int(mix(seed+uint64(k)*0x9e3779b97f4a7c15) % uint64(n)) }

func unit(seed uint64, k int) float64 { float64(mix(seed+uint64(k)*0x9e3779b97f4a7c15)>>11) / 9007199254740992.0 }

// largePairs is 13 to 96 pairs, and one case in eight 13 to MaxPairs:
// past every law's lawPairs().
func largePairs(seed uint64, size int) int {
	if draw(seed, 1000, 8) == 0 { within(size, 13, bench.MaxPairs()) } else { within(size, 13, 96) }
}

// differences are n drawn differences: untied reals in [-0.5, 0.5) at
// ties 0, else whole sizes from -ties to ties, so runs of equal sizes
// longer than any law's series, and zeros.
func differences(seed uint64, n int, ties int) []float64 {
	let levels = within(ties, 0, 6)
	let mut d = []float64{}
	for k in 0..n {
		d = d.Append(if levels == 0 { unit(seed, k) - 0.5 } else { float64(draw(seed, k, 2*levels+1) - levels) })
	}
	d
}

test BenchSignedRankPMatchesReference(seed uint64, size int, ties int) {
	let d = differences(seed, largePairs(seed, size), ties)
	assert bench.SignedRankP(d) == refSignedRankP(d)
}

test BenchHodgesLehmannMatchesReference(seed uint64, size int, ties int, pick int) {
	let d = differences(seed, largePairs(seed, size), ties)
	let levels = []float64{0.0, 0.05, 0.5, 0.9, 0.95, 0.99, 0.999}
	let level = levels[within(pick, 0, 6)]
	assert bench.HodgesLehmann(d, level) == refHodgesLehmann(d, level)
}
`

// TestStdBenchMatchesReference runs stdBenchRefProgram's properties with
// --seed 1, as ergo test --seed 1 does; a refuted case prints its
// arguments and its replay line (runtime.md, Tests 5).
//
// It builds Go because the reference's cubic tables at up to MaxPairs
// pairs are far past check time's step budget; only the built program
// runs them.
//
// Mutants (internal/sketch/stdlib/testdata/mutants.json): blp-scale-by-shift,
// blp-tie-run-capped, blp-untied-fixed-length.
func TestStdBenchMatchesReference(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdbenchref"), "--seed", "1")
	if code != 0 {
		t.Errorf("std bench differs from its reference at large n: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)`).FindAllStringSubmatch(stdBenchRefProgram, -1) {
		if !regexp.MustCompile(`(?m)^ok ` + m[1] + ` \(\d+ cases\)$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", m[1], stdout)
		}
	}
}
