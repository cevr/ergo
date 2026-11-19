package lower

import "testing"

// BenchmarkStdCivilRelative is relative text's hand-Go control (std-spike
// T5; plan §3.10 row 9): Phrase(Distance(a, b), EnglishWords()) over 4,096
// pairs of instants of every distance from a second to centuries, either
// way, against the same rule written by hand in Go (the thresholds, the
// rounding, strconv and the English words). Expected: equal to hand Go.
// Reported either way.
func BenchmarkStdCivilRelative(b *testing.B) {
	const src = `import civil
import time

func Phrases(from []int64, to []int64) int {
	let w = civil.EnglishWords()
	let mut s = 0
	for i in 0..len(from) {
		s = s + len(civil.Phrase(civil.Distance(time.Unix(from[i], 0), time.Unix(to[i], 0)), w))
	}
	s
}

func main() needs Console {
	Console.Println("\(Phrases([]int64{0}, []int64{1}))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"strconv"
	"testing"
)

var from, to = func() ([]int64, []int64) {
	a, b := make([]int64, 4096), make([]int64, 4096)
	for i := range a {
		a[i] = 1700000000 + int64(i)*7919
		d := int64(1) << (i % 34)
		if i%2 == 0 {
			d = -d
		}
		b[i] = a[i] + d + int64(i%7)
	}
	return a, b
}()

var sink int

func BenchmarkRelativeErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Phrases", "from, to") + `
	}
}

// handPhrase is the rule by hand: whole seconds, the thresholds, the
// count rounded half up, 12 months as a year, and English words.
func handPhrase(a, b int64) string {
	past := b < a
	s := b - a
	if past {
		s = -s
	}
	var n int64
	var unit string
	switch {
	case s < 60:
		n, unit = s, "second"
	case s < 3600:
		n, unit = (2*s+60)/120, "minute"
	case s < 86400:
		n, unit = (2*s+3600)/7200, "hour"
	case s < 2592000:
		n, unit = (2*s+86400)/172800, "day"
	case s < 31536000:
		n, unit = (2*s+2592000)/5184000, "month"
		if n == 12 {
			n, unit = 1, "year"
		}
	default:
		n, unit = (2*s+31536000)/63072000, "year"
	}
	if n == 0 {
		return "just now"
	}
	if n != 1 {
		unit += "s"
	}
	if past {
		return strconv.FormatInt(n, 10) + " " + unit + " ago"
	}
	return "in " + strconv.FormatInt(n, 10) + " " + unit
}

func BenchmarkRelativeGo(b *testing.B) {
	for range b.N {
		s := 0
		for i := range from {
			s += len(handPhrase(from[i], to[i]))
		}
		sink = s
	}
}
`
	}
	emittedBench(b, "std_civil_relative", src, gen, "RelativeErgo", "RelativeGo")
}
