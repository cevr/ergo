package v1

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
)

// durationText is a text that may or may not be a duration: a sign or
// none, one to three numbers with or without a fraction, each with a unit,
// a missing or unknown unit, an empty fraction, a bare "0", the extremes,
// spaces and stray bytes.
func durationText(r *rand.Rand) string {
	switch r.IntN(10) {
	case 0:
		return []string{"", "0", "-0", "+0", "0s", ".", "-", "1", "1.", ".5s", "1.s", "s", "1h1", "1 h", " 1s", "1e3s", "1hh",
			"9223372036854775807ns", "9223372036854775808ns", "-9223372036854775808ns", "-9223372036854775809ns",
			"2562047h47m16.854775807s", "2562047h47m16.854775808s", "-2562047h47m16.854775808s", "\xff1s", "1µs", "1μs", "1us"}[r.IntN(28)]
	case 1:
		return durationAny(r).String()
	}
	var b strings.Builder
	b.WriteString([]string{"", "", "-", "+"}[r.IntN(4)])
	units := []string{"ns", "us", "µs", "μs", "ms", "s", "m", "h", "", "d", "x"}
	for range 1 + r.IntN(3) {
		b.WriteString(digits(r))
		switch r.IntN(4) {
		case 0:
			b.WriteString("." + digits(r))
		case 1:
			b.WriteString(".")
		}
		b.WriteString(units[r.IntN(len(units))])
	}
	return b.String()
}

// digits is one to twenty decimal digits, often few.
func digits(r *rand.Rand) string {
	n := 1 + r.IntN(4)
	if r.IntN(8) == 0 {
		n = 1 + r.IntN(20)
	}
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + r.IntN(10)))
	}
	return b.String()
}

// durationAny is a duration of every size: small, round, random bits and
// the extremes.
func durationAny(r *rand.Rand) time.Duration {
	switch r.IntN(5) {
	case 0:
		return time.Duration(r.IntN(2001) - 1000)
	case 1:
		return time.Duration(r.IntN(200)-100) * []time.Duration{time.Microsecond, time.Millisecond, time.Second, time.Minute, time.Hour}[r.IntN(5)]
	case 2:
		return []time.Duration{math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, math.MinInt64 + 1, 0, -1, 1}[r.IntN(7)]
	}
	return time.Duration(r.Uint64())
}

// intAny is an int of every size: small, the extremes, random bits.
func intAny(r *rand.Rand) int {
	switch r.IntN(4) {
	case 0:
		return r.IntN(21) - 10
	case 1:
		return []int{math.MaxInt, math.MinInt, -1, 1, 0}[r.IntN(5)]
	case 2:
		return int(r.Int64()) >> r.IntN(64)
	}
	return int(r.Uint64())
}

// saturated is d * n over the integers, clamped to the range of a
// duration: the reference for TimeTimes.
func saturated(d time.Duration, n int) time.Duration {
	p := new(big.Int).Mul(big.NewInt(int64(d)), big.NewInt(int64(n)))
	switch {
	case p.Cmp(big.NewInt(math.MaxInt64)) > 0:
		return math.MaxInt64
	case p.Cmp(big.NewInt(math.MinInt64)) < 0:
		return math.MinInt64
	}
	return time.Duration(p.Int64())
}

// TestStdDurationMatchesGo (std-duration): ParseDuration gives Go's
// time.ParseDuration's duration, or its message in a ParseError with the
// text as Value, on 10,000 seeded texts, valid and not; DurationText is
// Go's Duration.String; Times is the exact product clamped to the range
// of a duration (Go's wraps); Divide and Count are Go's quotients except
// the one that overflows, which saturates.
func TestStdDurationMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	valid := 0
	for i := range 10000 {
		s := durationText(r)
		want, werr := time.ParseDuration(s)
		got, err := TimeParseDuration(s)
		var pe ParseError
		switch {
		case werr == nil && (err != nil || got != want):
			t.Fatalf("case %d: ParseDuration(%q) = %v, %v; Go %v", i, s, got, err, want)
		case werr != nil && (!errors.As(err, &pe) || pe != (ParseError{Value: s, Message: werr.Error()})):
			t.Fatalf("case %d: ParseDuration(%q) = %v, %#v; Go fails %q", i, s, got, err, werr)
		case werr == nil:
			valid++
		}
		d := durationAny(r)
		if TimeDurationText(d) != d.String() {
			t.Fatalf("case %d: DurationText(%d) = %q", i, d, TimeDurationText(d))
		}
		n := intAny(r)
		if got, want := TimeTimes(d, n), saturated(d, n); got != want {
			t.Fatalf("case %d: Times(%d, %d) = %d; want %d", i, d, n, got, want)
		}
		if n != 0 {
			want := d / time.Duration(n)
			if d == math.MinInt64 && n == -1 {
				want = math.MaxInt64
			}
			if got := TimeDivide(d, n); got != want {
				t.Fatalf("case %d: Divide(%d, %d) = %d; want %d", i, d, n, got, want)
			}
		}
		if unit := durationAny(r); unit != 0 {
			want := int(d / unit)
			if d == math.MinInt64 && unit == -1 {
				want = math.MaxInt
			}
			if got := TimeCount(d, unit); got != want {
				t.Fatalf("case %d: Count(%d, %d) = %d; want %d", i, d, unit, got, want)
			}
		}
	}
	if valid < 2000 || valid > 8000 {
		t.Fatalf("%d of 10,000 texts are valid durations; the generator should give both kinds", valid)
	}
}

// TestStdDurationDefects: Divide by 0 and Count by 0ns panic, as Go's
// division does (a defect in Ergo).
func TestStdDurationDefects(t *testing.T) {
	for name, f := range map[string]func(){
		"Divide": func() { TimeDivide(time.Second, 0) },
		"Count":  func() { TimeCount(time.Second, 0) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s by zero did not panic", name)
				}
			}()
			f()
		}()
	}
}

// durationLaws are the duration laws, each over a seeded case;
// durationLawStatements are their ledger statements.
var durationLaws = map[string]func(r *rand.Rand) (bool, any){
	"time DurationTextRoundTrips": func(r *rand.Rand) (bool, any) {
		d := durationAny(r)
		back, err := TimeParseDuration(TimeDurationText(d))
		return err == nil && back == d, d
	},
	"time DivideInvertsTimes": func(r *rand.Rand) (bool, any) {
		d, n := durationAny(r), intAny(r)
		small := TimeDivide(d, 1000000)
		k := n % 1000
		return k == 0 || TimeDivide(TimeTimes(small, k), k) == small, []any{d, n}
	},
	"time TimesKeepsTheSign": func(r *rand.Rand) (bool, any) {
		d, n := durationAny(r), intAny(r)
		p := TimeTimes(d, n)
		if d == 0 || n == 0 {
			return p == 0, []any{d, n}
		}
		return (p > 0) == ((d > 0) == (n > 0)), []any{d, n}
	},
	"time CountIsWholeUnits": func(r *rand.Rand) (bool, any) {
		d, unit := durationAny(r), durationAny(r)
		if d < 0 || unit <= 0 {
			return true, []any{d, unit}
		}
		c := TimeCount(d, unit)
		return TimeTimes(unit, c) <= d && d-TimeTimes(unit, c) < unit, []any{d, unit}
	},
}

var durationLawStatements = map[string]string{
	"time DurationTextRoundTrips": `DurationTextRoundTrips(d duration) { try ParseDuration(DurationText(d)) == d }`,
	"time DivideInvertsTimes":     `DivideInvertsTimes(d duration, n int) { let small = Divide(d, 1000000) let k = n % 1000 if k == 0 { return true } Divide(Times(small, k), k) == small }`,
	"time TimesKeepsTheSign":      `TimesKeepsTheSign(d duration, n int) { let p = Times(d, n) if d == 0ns || n == 0 { return p == 0ns } (p > 0ns) == ((d > 0ns) == (n > 0)) }`,
	"time CountIsWholeUnits":      `CountIsWholeUnits(d duration, unit duration) { if d < 0ns || unit <= 0ns { return true } let c = Count(d, unit) Times(unit, c) <= d && d-Times(unit, c) < unit }`,
}

// TestStdDurationLaws runs the duration laws over the Go bodies, each on
// 10,000 seeded cases; it is their ledger runner.
func TestStdDurationLaws(t *testing.T) {
	laws := map[string]lawledger.Law{}
	for name, holds := range durationLaws {
		laws[name] = lawledger.Law{Statement: durationLawStatements[name], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(4, uint64(len(name))))
			for i := range 10_000 {
				if ok, c := holds(r); !ok {
					t.Errorf("%s fails on case %d: %#v", name, i, c)
					break
				}
			}
		}}
	}
	lawledger.Run(t, laws)
}

// BenchmarkDuration: each duration body against the Go it stands for.
func BenchmarkDuration(b *testing.B) {
	d := 90*time.Minute + 1500*time.Millisecond
	var sink any
	for _, c := range []struct {
		name      string
		ergo, go_ func()
	}{
		{"ParseDuration", func() { sink, _ = TimeParseDuration("1h30m1.5s") }, func() { sink, _ = time.ParseDuration("1h30m1.5s") }},
		{"DurationText", func() { sink = TimeDurationText(d) }, func() { sink = d.String() }},
		{"Times", func() { sink = TimeTimes(d, 7) }, func() { sink = d * 7 }},
		{"Divide", func() { sink = TimeDivide(d, 7) }, func() { sink = d / 7 }},
		{"Count", func() { sink = TimeCount(d, time.Millisecond) }, func() { sink = d.Milliseconds() }},
	} {
		b.Run(c.name+"/ergo", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c.ergo()
			}
		})
		b.Run(c.name+"/go", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c.go_()
			}
		})
	}
	_ = sink
}
