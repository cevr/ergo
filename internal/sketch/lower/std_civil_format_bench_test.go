package lower

import "testing"

// BenchmarkStdCivilFormat is the typed format's hand-Go control (std-spike
// T4; plan §3.10 row 6, F7): over the same 4,096 instants of years 1970 to
// 2100, Ergo's FormatDateTime with a format built once by
// Pattern("yyyy-MM-dd HH:mm:ss") against Go's t.Format("2006-01-02
// 15:04:05"), which reads its layout on every call; and ParseDateTime
// against time.Parse of the same text; AppendDateTime into one reused
// buffer against Go's AppendFormat (std-spike T4c). Expected (F7): Format faster than
// Go's, the format being read once; Parse at least as fast. Reported
// either way.
func BenchmarkStdCivilFormat(b *testing.B) {
	const src = `import civil
import time

func layout() civil.DateTimeFormat {
	civil.Pattern("yyyy-MM-dd HH:mm:ss") catch {
	case civil.BadPattern:
		civil.ISODateTime()
	}
}

func Formats(secs []int64) int {
	let f = layout()
	let mut s = 0
	for n in secs {
		s = s + len(civil.FormatDateTime(civil.DateTimeOf(time.Unix(n, 0)), f))
	}
	s
}

func Appends(secs []int64) int {
	let f = layout()
	let mut buf = []byte{}
	let mut s = 0
	for n in secs {
		buf = civil.AppendDateTime(civil.DateTimeOf(time.Unix(n, 0)), f, buf[:0])
		s = s + len(buf)
	}
	s
}

func Texts(secs []int64) []string {
	let f = layout()
	let mut out = []string{}
	for n in secs {
		out = out.Append(civil.FormatDateTime(civil.DateTimeOf(time.Unix(n, 0)), f))
	}
	out
}

func Parses(texts []string) int64 {
	let f = layout()
	let mut s = int64(0)
	for t in texts {
		let dt = civil.ParseDateTime(f, t) catch {
		case civil.ParseError:
			continue
		}
		s = s + civil.UnixDays(dt.Date)
	}
	s
}

func main() needs Console {
	Console.Println("\(Formats([]int64{0})) \(Appends([]int64{0})) \(Parses(Texts([]int64{0})))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"testing"
	"time"
)

var secs = func() []int64 {
	xs := make([]int64, 4096)
	for i := range xs {
		xs[i] = int64(i) * 1000003 % 4102444800
	}
	return xs
}()

var texts = ` + emittedCall(mainGo, "Texts", "secs") + `

var sink int64

func BenchmarkFormatErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Formats", "secs") + `)
	}
}

func BenchmarkFormatGo(b *testing.B) {
	for range b.N {
		s := 0
		for _, n := range secs {
			s += len(time.Unix(n, 0).UTC().Format("2006-01-02 15:04:05"))
		}
		sink = int64(s)
	}
}

func BenchmarkAppendErgo(b *testing.B) {
	for range b.N {
		sink = int64(` + emittedCall(mainGo, "Appends", "secs") + `)
	}
}

func BenchmarkAppendGo(b *testing.B) {
	var buf []byte
	for range b.N {
		s := 0
		for _, n := range secs {
			buf = time.Unix(n, 0).UTC().AppendFormat(buf[:0], "2006-01-02 15:04:05")
			s += len(buf)
		}
		sink = int64(s)
	}
}

func BenchmarkParseErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Parses", "texts") + `
	}
}

func BenchmarkParseGo(b *testing.B) {
	for range b.N {
		var s int64
		for _, t := range texts {
			v, err := time.Parse("2006-01-02 15:04:05", t)
			if err == nil {
				s += v.Unix() / 86400
			}
		}
		sink = s
	}
}
`
	}
	emittedBench(b, "std_civil_format", src, gen, "FormatErgo", "FormatGo", "AppendErgo", "AppendGo", "ParseErgo", "ParseGo")
}
