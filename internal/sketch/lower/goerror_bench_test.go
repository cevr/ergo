package lower

import "testing"

// BenchmarkGoErrorNative is IG5's control: the failure path of a Go call
// whose error an Ergo function passes up (try) and its caller catches,
// emitted lazily (the go.Error keeps Go's error; Text is computed when
// read) and with the eager reference (SetGoErrorEagerForTest: the text is
// computed at the failure, G4's form), each against hand Go of the same
// functions in the same binary. Up: the caller does not read the error
// (hand Go: `if err != nil { return -1 }`); the lazy form does no
// Error() formatting. Text: the caller reads e.Text (hand Go:
// err.Error()); both forms format once. Target: lazy equal to hand Go
// within noise on both; the eager form pays one formatting on Up.
func BenchmarkGoErrorNative(b *testing.B) {
	const src = `import go strconv

func parse(s string) int64 fails go.Error {
	let n = try strconv.ParseInt(s, 10, 64)
	n + 1
}

func Up(s string) int64 {
	parse(s) catch { case go.Error: -1 }
}

func TextLen(s string) int {
	let n = parse(s) catch { case go.Error as e: return len(e.Text) }
	int(n)
}

func main() needs Console {
	Console.Println("\(Up("12"))|\(TextLen("x"))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"strconv"
	"testing"
)

var badText = "12x4567"
var sinkI int64
var sinkN int

func parseGo(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return n + 1, nil
}

func upGo(s string) int64 {
	n, err := parseGo(s)
	if err != nil {
		return -1
	}
	return n
}

func textLenGo(s string) int {
	n, err := parseGo(s)
	if err != nil {
		return len(err.Error())
	}
	return int(n)
}

func BenchmarkUpErgo(b *testing.B) {
	for range b.N {
		sinkI = ` + emittedCall(mainGo, "Up", "badText") + `
	}
}

func BenchmarkUpGo(b *testing.B) {
	for range b.N {
		sinkI = upGo(badText)
	}
}

func BenchmarkTextErgo(b *testing.B) {
	for range b.N {
		sinkN = ` + emittedCall(mainGo, "TextLen", "badText") + `
	}
}

func BenchmarkTextGo(b *testing.B) {
	for range b.N {
		sinkN = textLenGo(badText)
	}
}
`
	}
	for _, eager := range []bool{false, true} {
		name := "lazy"
		if eager {
			name = "eager"
		}
		b.Run(name, func(b *testing.B) {
			restore := SetGoErrorEagerForTest(eager)
			defer restore()
			emittedBench(b, "goerror_"+name, src, gen, "UpErgo", "UpGo", "TextErgo", "TextGo")
		})
	}
}
