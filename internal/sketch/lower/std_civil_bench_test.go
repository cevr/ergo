package lower

import "testing"

// BenchmarkStdCivil is std civil's hand-Go control (std-spike T1; plan
// §3.10 rows 1 to 5): each operation over the same 4,096 dates, Ergo's
// civil against Go's time (and, for sorting, a Go {y, m, d} struct as
// cloud.google.com/go/civil has it), in one test binary. The Ergo side
// takes Unix day numbers and builds each Date with FromUnixDays (a clamp),
// since the Go test file cannot name civil's opaque Date. Targets: add
// months, compare and sort at least as fast as Go; field reads equal or
// slower by the day-number conversion, reported either way.
func BenchmarkStdCivil(b *testing.B) {
	const src = `import civil
import slices

func AddMonths(days []int64) int64 {
	let mut s = int64(0)
	for n in days {
		s = s + civil.UnixDays(civil.AddMonths(civil.FromUnixDays(n), 1))
	}
	s
}

func Fields(days []int64) int {
	let mut s = 0
	for n in days {
		let d = civil.FromUnixDays(n)
		s = s + civil.Year(d) + civil.Day(d)
	}
	s
}

func StartOfMonth(days []int64) int64 {
	let mut s = int64(0)
	for n in days {
		s = s + civil.UnixDays(civil.StartOf(civil.FromUnixDays(n), civil.Months))
	}
	s
}

func Sort(days []int64) int64 {
	let mut ds = slices.WithCapacity[civil.Date](len(days))
	for n in days {
		ds = ds.Append(civil.FromUnixDays(n))
	}
	let sorted = slices.SortFunc(ds, func(a civil.Date, b civil.Date) int { civil.Compare(a, b) })
	civil.UnixDays(sorted[0])
}

func Text(days []int64) int {
	let mut s = 0
	for n in days {
		s = s + len(civil.Text(civil.FromUnixDays(n)))
	}
	s
}

func main() needs Console {
	Console.Println("\(AddMonths([]int64{0})) \(Fields([]int64{0})) \(StartOfMonth([]int64{0})) \(Sort([]int64{1, 0})) \(Text([]int64{0}))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"slices"
	"testing"
	"time"
)

var days = func() []int64 {
	xs := make([]int64, 4096)
	for i := range xs {
		xs[i] = int64((i*7919)%40000) - 10000
	}
	return xs
}()

var times = func() []time.Time {
	ts := make([]time.Time, len(days))
	for i, n := range days {
		ts[i] = time.Unix(n*86400, 0).UTC()
	}
	return ts
}()

type goYMD struct{ y, m, d int }

var goYMDs = func() []goYMD {
	out := make([]goYMD, len(times))
	for i, t := range times {
		y, m, d := t.Date()
		out[i] = goYMD{y, int(m), d}
	}
	return out
}()

var sink int64

func addMonthsGo() int64 {
	var s int64
	for _, t := range times {
		y, m, d := t.Date()
		last := time.Date(y, m+2, 0, 0, 0, 0, 0, time.UTC).Day()
		s += time.Date(y, m+1, min(d, last), 0, 0, 0, 0, time.UTC).Unix() / 86400
	}
	return s
}

func BenchmarkAddMonthsErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "AddMonths", "days") + `
	}
}

func BenchmarkAddMonthsGo(b *testing.B) {
	for range b.N {
		sink = addMonthsGo()
	}
}

func BenchmarkFieldsErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Fields", "days") + `)
	}
}

func BenchmarkFieldsGo(b *testing.B) {
	for range b.N {
		s := 0
		for _, t := range times {
			y, _, d := t.Date()
			s += y + d
		}
		sink = int64(s)
	}
}

func BenchmarkStartOfMonthErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "StartOfMonth", "days") + `
	}
}

func BenchmarkStartOfMonthGo(b *testing.B) {
	for range b.N {
		var s int64
		for _, t := range times {
			y, m, _ := t.Date()
			s += time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).Unix() / 86400
		}
		sink = s
	}
}

func BenchmarkSortErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Sort", "days") + `
	}
}

func BenchmarkSortGoTime(b *testing.B) {
	for range b.N {
		ts := slices.Clone(times)
		slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
		sink = ts[0].Unix()
	}
}

func BenchmarkSortGoYMD(b *testing.B) {
	for range b.N {
		ds := slices.Clone(goYMDs)
		slices.SortFunc(ds, func(a, b goYMD) int {
			if a.y != b.y {
				return a.y - b.y
			}
			if a.m != b.m {
				return a.m - b.m
			}
			return a.d - b.d
		})
		sink = int64(ds[0].y)
	}
}

func BenchmarkTextErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Text", "days") + `)
	}
}

func BenchmarkTextGo(b *testing.B) {
	buf := make([]byte, 0, 16)
	for range b.N {
		s := 0
		for _, t := range times {
			s += len(t.AppendFormat(buf[:0], time.DateOnly))
		}
		sink = int64(s)
	}
}
`
	}
	emittedBench(b, "std_civil", src, gen, "AddMonthsErgo", "AddMonthsGo", "FieldsErgo", "FieldsGo", "StartOfMonthErgo", "StartOfMonthGo", "SortErgo", "SortGoTime", "SortGoYMD", "TextErgo", "TextGo")
}
