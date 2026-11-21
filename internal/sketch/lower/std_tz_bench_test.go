package lower

import "testing"

// BenchmarkStdTz is std tz's hand-Go control (std-spike T2; plan §3.10 rows
// 7 and 8), in one test binary over the same 4,096 instants of
// America/Toronto from 1970 to 2100: the offset at an instant (Ergo's
// OffsetAt against Go's t.In(loc).Zone()), wall time to instant (Ergo's
// Compatible(Resolve) against Go's time.Date in loc, whose gap rule
// differs: F1), and reading a zone from its TZif bytes (Ergo's FromTZif
// against Go's LoadLocationFromTZData). Expected: OffsetAt equal to Go
// within the boundary cost; Resolve slower (it reads the window's
// changes), recorded with its follow-up; FromTZif slower (Ergo reads the
// bytes into slices where Go's reader works on a []byte). Reported either
// way.
func BenchmarkStdTz(b *testing.B) {
	const src = `import civil
import time
import tz
import tz/iana

func zone() tz.Zone {
	tz.FromTZif("America/Toronto", iana.TZif("America/Toronto")) catch {
	case tz.BadData:
		tz.UTC()
	}
}

func Offsets(secs []int64) int {
	let z = zone()
	let mut s = 0
	for n in secs {
		s = s + tz.OffsetAt(z, time.Unix(n, 0)).Seconds
	}
	s
}

func Resolves(secs []int64) int64 {
	let z = zone()
	let mut s = int64(0)
	for n in secs {
		s = s + time.UnixSeconds(tz.Compatible(tz.Resolve(z, civil.DateTimeOf(time.Unix(n, 0)))))
	}
	s
}

func Loads(n int) int {
	let data = iana.TZif("America/Toronto")
	let mut s = 0
	for _ in 0..n {
		let z = tz.FromTZif("America/Toronto", data) catch {
		case tz.BadData:
			tz.UTC()
		}
		s = s + len(tz.Name(z))
	}
	s
}

func main() needs Console {
	Console.Println("\(Offsets([]int64{0})) \(Resolves([]int64{0})) \(Loads(1))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"testing"
	"time"

	"ergo.local/prototype/runtime/tzdata"
)

var secs = func() []int64 {
	xs := make([]int64, 4096)
	for i := range xs {
		xs[i] = int64(i) * 1000003 % 4102444800
	}
	return xs
}()

var loc = func() *time.Location {
	l, err := time.LoadLocationFromTZData("America/Toronto", []byte(tzdata.Zone("America/Toronto")))
	if err != nil {
		panic(err)
	}
	return l
}()

var sink int64

func BenchmarkOffsetErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Offsets", "secs") + `)
	}
}

func BenchmarkOffsetGo(b *testing.B) {
	for range b.N {
		s := 0
		for _, n := range secs {
			_, o := time.Unix(n, 0).In(loc).Zone()
			s += o
		}
		sink = int64(s)
	}
}

func BenchmarkResolveErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Resolves", "secs") + `
	}
}

func BenchmarkResolveGo(b *testing.B) {
	for range b.N {
		var s int64
		for _, n := range secs {
			w := time.Unix(n, 0).UTC()
			s += time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, loc).Unix()
		}
		sink = s
	}
}

func BenchmarkLoadErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Loads", "1") + `)
	}
}

func BenchmarkLoadGo(b *testing.B) {
	for range b.N {
		data := []byte(tzdata.Zone("America/Toronto"))
		l, _ := time.LoadLocationFromTZData("America/Toronto", data)
		sink = int64(len(l.String()))
	}
}
`
	}
	emittedBench(b, "std_tz", src, gen, "OffsetErgo", "OffsetGo", "ResolveErgo", "ResolveGo", "LoadErgo", "LoadGo")
}
