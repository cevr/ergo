package lower

import "testing"

// BenchmarkLevel2Adapters is IG4's control: a call through a Level 2
// adapter against hand Go that writes the same check (Partial: the value
// and the error; Option: the (v, ok) test), in one module. Target: equal
// ns/op and allocs. bench-pending.md "IG4 control" has the command.
func BenchmarkLevel2Adapters(b *testing.B) {
	const src = `extern go strconv.ParseInt func parseInt(s string, base int, bits int) (int64, Option[go.Error])
extern go strings.CutPrefix func cutPrefix(s string, p string) Option[string]

func Partial(s string) int64 {
	let (n, e) = parseInt(s, 10, 64)
	match e {
		case Some: -1
		case None: n
	}
}

func Cut(s string) int {
	match cutPrefix(s, "ab") {
		case Some as x: len(x.Value)
		case None: -1
	}
}

func main() needs Console {
	Console.Println("\(Partial("12"))|\(Cut("abc"))")
}
`
	emittedBench(b, "level2", src, func(mainGo string) string {
		return `package main

import (
	"strconv"
	"strings"
	"testing"
)

var okText, cutText = "1234567", "abcdef"
var sinkI int64
var sinkN int

// The hand Go functions beside the emitted ones, written as a Go
// programmer writes each Ergo function.
func partialGo(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func cutGo(s string) int {
	after, ok := strings.CutPrefix(s, "ab")
	if !ok {
		return -1
	}
	return len(after)
}

func BenchmarkPartialErgo(b *testing.B) {
	for range b.N {
		sinkI = ` + emittedCall(mainGo, "Partial", "okText") + `
	}
}

func BenchmarkPartialGo(b *testing.B) {
	for range b.N {
		sinkI = partialGo(okText)
	}
}

func BenchmarkCutErgo(b *testing.B) {
	for range b.N {
		sinkN = ` + emittedCall(mainGo, "Cut", "cutText") + `
	}
}

func BenchmarkCutGo(b *testing.B) {
	for range b.N {
		sinkN = cutGo(cutText)
	}
}
`
	}, "PartialErgo", "PartialGo", "CutErgo", "CutGo")
}
