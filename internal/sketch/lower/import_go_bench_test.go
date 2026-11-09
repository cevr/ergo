package lower

import "testing"

// BenchmarkImportGo is IG3's control: calls of import go, emitted and
// measured in process, against hand Go of the same calls in the same
// package. strconv.ParseInt on a number (the success path is the Go call
// and one compare, as hand Go writes) and on a non-number (the failure
// path wraps the error in go.Error; IG5 makes it lazy), strings.Join of a
// dead slice (handed over with no copy) and url.Parse(s).Hostname()
// through a go.Ref (one nil compare beside the error check). Target: equal
// ns/op and allocs/op on success.
func BenchmarkImportGo(b *testing.B) {
	const src = `import go strconv
import go strings
import go net/url

func Parse(s string) int64 {
	strconv.ParseInt(s, 10, 64) catch { case go.Error: -1 }
}

func JoinDead(a string, b string) int {
	let xs = []string{a, b, a, b}
	len(strings.Join(xs, ","))
}

func host(s string) string fails go.Error needs go.Foreign {
	let u = try url.Parse(s)
	u.Hostname()
}

func Host(s string) string needs go.Foreign {
	host(s) catch { case go.Error: "" }
}

func main() needs Console | go.Foreign {
	Console.Println("\(Parse("12"))|\(JoinDead("a", "b"))|\(Host("https://example.com/x"))")
}
`
	emittedBench(b, "importgo", src, func(mainGo string) string {
		return `package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

var okText, badText, joinA, joinB, rawURL = "1234567", "12x4567", "alpha", "beta", "https://example.com:8080/a/b?q=1"
var sinkI int64
var sinkN int
var sinkS string

func BenchmarkParseOkErgo(b *testing.B) {
	for range b.N {
		sinkI = ` + emittedCall(mainGo, "Parse", "okText") + `
	}
}

// The hand Go functions beside the emitted ones, written as a Go
// programmer writes each Ergo function.
func parseGo(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func joinDeadGo(a, b string) int {
	xs := []string{a, b, a, b}
	return len(strings.Join(xs, ","))
}

func hostGo(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func BenchmarkParseOkGo(b *testing.B) {
	for range b.N {
		sinkI = parseGo(okText)
	}
}

func BenchmarkParseBadErgo(b *testing.B) {
	for range b.N {
		sinkI = ` + emittedCall(mainGo, "Parse", "badText") + `
	}
}

func BenchmarkParseBadGo(b *testing.B) {
	for range b.N {
		sinkI = parseGo(badText)
	}
}

func BenchmarkJoinDeadErgo(b *testing.B) {
	for range b.N {
		sinkN = ` + emittedCall(mainGo, "JoinDead", "joinA, joinB") + `
	}
}

func BenchmarkJoinDeadGo(b *testing.B) {
	for range b.N {
		sinkN = joinDeadGo(joinA, joinB)
	}
}

func BenchmarkHostErgo(b *testing.B) {
	for range b.N {
		sinkS = ` + emittedCall(mainGo, "Host", "rawURL") + `
	}
}

func BenchmarkHostGo(b *testing.B) {
	for range b.N {
		sinkS = hostGo(rawURL)
	}
}
`
	}, "ParseOkErgo", "ParseOkGo", "ParseBadErgo", "ParseBadGo", "JoinDeadErgo", "JoinDeadGo", "HostErgo", "HostGo")
}
