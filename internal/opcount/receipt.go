package opcount

import (
	"math"
	"slices"
)

// Schema is the receipt format scripts/opcount writes: bench.v1 of the
// STROBE plan (run dir strobe/plan.md §4.1), so that `ergo bench` (BN7)
// replaces the writer and keeps the format.
const Schema = "bench.v1"

// Receipt is a bench.v1 receipt. Nothing in it names a home folder or
// another process.
type Receipt struct {
	Schema string `json:"schema"`
	Tool   string `json:"tool"`
	// Commit is the checkout's HEAD, with "+dirty" when it has changes.
	Commit string `json:"commit"`
	Host   Host   `json:"host"`
	// Load is /proc/loadavg at the start and at the end.
	Load     [2]string `json:"load"`
	Priority int       `json:"priority"`
	Cases    []Case    `json:"cases"`
}

// Host is what a receipt says of the machine.
type Host struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUs      int    `json:"cpus"`
	GoVersion string `json:"go_version"`
	Counter   string `json:"counter"` // "user instructions" or why there is none
}

// Case is one program (or one target of a command comparison) at its
// sizes, with each side's series and the comparisons of the sides.
type Case struct {
	Case        string       `json:"case"`
	Unit        string       `json:"unit,omitempty"`
	Sizes       []int        `json:"sizes,omitempty"`
	Rounds      int          `json:"rounds"`
	Workload    *Workload    `json:"workload,omitempty"`
	Series      []Series     `json:"series"`
	Comparisons []Comparison `json:"comparisons,omitempty"`
}

// Kind is the kind of a counter: exact (one value per build or run, held
// exactly), counted (a hardware count, tight under load, judged over
// pairs) or timed (time and memory, judged over pairs, never held).
type Kind string

// The kinds.
const (
	Exact   Kind = "exact"
	Counted Kind = "counted"
	Timed   Kind = "timed"
)

// Series is one counter of one side over the rounds. It has values or a
// refusal, never both (NewSeries).
type Series struct {
	Side    string `json:"side"`
	Counter string `json:"counter"`
	Kind    Kind   `json:"kind"`
	// PerUnit: the values are slopes per unit between the two sizes.
	PerUnit bool `json:"per_unit"`
	// Values are one per round; Estimate is their median and Spread
	// (max - min) / |median|.
	Values   []float64 `json:"values,omitempty"`
	Estimate *float64  `json:"estimate,omitempty"`
	Spread   *float64  `json:"spread,omitempty"`
	Refusal  *Refused  `json:"refusal,omitempty"`
}

// Refused is a refusal with its reason.
type Refused struct {
	ID     Refusal `json:"id"`
	Reason string  `json:"reason"`
}

// Comparison sets two sides of one counter side by side. Opcount takes no
// verdict: the paired test is the referee's (STROBE BN1, BN2). Ratio is
// the median of the per-round ratios head/base, descriptive. Verdict is
// set only to a refusal, when either side has none or the sides did not
// agree.
type Comparison struct {
	Counter string   `json:"counter"`
	Base    string   `json:"base"`
	Head    string   `json:"head"`
	Pairs   int      `json:"pairs"`
	Ratio   *float64 `json:"ratio,omitempty"`
	Verdict *Verdict `json:"verdict,omitempty"`
}

// Verdict is bench.v1's verdict; opcount writes only Refused.
type Verdict struct {
	Kind    string   `json:"verdict_kind"`
	Refusal *Refused `json:"refusal,omitempty"`
}

// NewSeries is the series of values, or of the refusal when refused is
// not nil: then it holds no number.
func NewSeries(side, counter string, kind Kind, perUnit bool, values []float64, refused *Refused) Series {
	s := Series{Side: side, Counter: counter, Kind: kind, PerUnit: perUnit}
	if refused != nil || len(values) == 0 {
		if refused == nil {
			refused = &Refused{NoMetric, "no run reported it"}
		}
		s.Refusal = refused
		return s
	}
	s.Values = slices.Clone(values)
	m := Median(values)
	spread := (slices.Max(values) - slices.Min(values)) / math.Abs(m)
	if m == 0 {
		spread = 0
		if slices.Max(values) != slices.Min(values) {
			spread = math.Inf(1)
		}
	}
	if math.IsInf(spread, 0) {
		spread = -1 // JSON has no infinity: -1 marks values spread around 0
	}
	s.Estimate, s.Spread = &m, &spread
	return s
}

// Compare is the comparison of two series of one counter, round by round.
func Compare(base, head Series) Comparison {
	c := Comparison{Counter: head.Counter, Base: base.Side, Head: head.Side}
	for _, s := range []Series{base, head} {
		if s.Refusal != nil {
			c.Verdict = &Verdict{Kind: "Refused", Refusal: s.Refusal}
			return c
		}
	}
	var ratios []float64
	for i := range min(len(base.Values), len(head.Values)) {
		if base.Values[i] != 0 {
			ratios = append(ratios, head.Values[i]/base.Values[i])
		}
	}
	c.Pairs = len(ratios)
	if len(ratios) > 0 {
		r := Median(ratios)
		c.Ratio = &r
	}
	return c
}

// Refuse marks every comparison of the case as refused for one reason
// (the sides printed different output: NotEquivalent).
func (c *Case) Refuse(r Refused) {
	for i := range c.Comparisons {
		c.Comparisons[i].Ratio = nil
		c.Comparisons[i].Verdict = &Verdict{Kind: "Refused", Refusal: &r}
	}
}

// Slope is the cost of one unit between two sizes: the difference of the
// two runs' values over the difference of the sizes, so the cost every
// run has once (start-up, the report) cancels.
func Slope(small, large float64, smallN, largeN int) float64 {
	return (large - small) / float64(largeN-smallN)
}

// Median is the median of xs (the mean of the middle two for an even
// count); xs is not changed.
func Median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
