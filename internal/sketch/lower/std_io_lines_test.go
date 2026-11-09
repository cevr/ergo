package lower

import (
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// io.Input's Lines (std-strengths SS3; stdlib.md, io 2) scans the text once
// per line with strings.Index, as strings.Lines does, not by a Slice and a
// compare per byte. Its meaning does not change: each line without its line
// feed, a last line with no line feed is a line, an empty input has no
// lines, and a carriage return stays.

// inputLinesProgram reads Go-quoted texts from standard input, one per
// line, and prints the lines of io.Input{Text: text}.Lines() for each, each
// line Go-quoted, separated by spaces.
const inputLinesProgram = `import io
import strconv
import strings

func main() fails io.ReadFailed needs Console | Ambient {
	let all = with context { io.Stdin: io.NewHost() } { try io.Stdin.ReadAll() }
	for quoted in strings.Lines(all) {
		let text = strconv.Unquote(strings.TrimSuffix(quoted, "\n")) catch {
		case strconv.Malformed:
			"unquote failed"
		}
		let mut parts = []string{}
		for line in (io.Input{Text: text}).Lines() {
			parts = parts.Append(strconv.Quote(line))
		}
		Console.Println(strings.Join(parts, " "))
	}
}
`

// inputLinesModel is the lines of s by Go's strings: s without its final
// line feed, split at each line feed; the empty input has no lines.
func inputLinesModel(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// inputLinesCases are the corner cases, then n seeded texts over the whole
// domain of string: line feeds at the start, at the end and in runs,
// carriage returns, the empty text, multi-byte UTF-8, invalid bytes, NUL,
// and texts long enough to hold many lines.
func inputLinesCases(seed uint64, n int) []string {
	cases := []string{"", "\n", "\n\n", "\n\n\n", "a", "a\n", "\na", "a\n\n", "\n\na", "a\nb", "a\nb\n", "a\r\nb\r\n", "\r", "\r\n", "a\r", "é\n\xff", "\xff\xfe\n", "\x00\n\x00", strings.Repeat("x", 300) + "\n" + strings.Repeat("y", 300)}
	alphabet := []string{"\n", "\n", "\n\n", "\r", "\r\n", "a", "bc", " ", "é", "日本", "\xff", "\xc3", "\x00", "line"}
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	for len(cases) < n {
		size := r.IntN(24)
		if r.IntN(20) == 0 {
			size = 200 + r.IntN(400)
		}
		var b strings.Builder
		if r.IntN(4) == 0 {
			b.WriteString("\n")
		}
		for range size {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		if r.IntN(3) == 0 {
			b.WriteString("\n")
		}
		cases = append(cases, b.String())
	}
	return cases
}

// TestInputLinesMatchesModel: over 2,000 seeded texts, io.Input's Lines
// gives the lines Go's strings.Split gives (inputLinesModel). The seed is
// 1, or ERGO_SEED; a failure prints the seed, the text, and both outputs.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestInputLinesMatchesModel(t *testing.T) {
	needsBuild(t)
	seed := uint64(1)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED=%q: %v", s, err)
		}
		seed = v
	}
	cases := inputLinesCases(seed, 2000)
	exes := tableExes(t, "inputlines")
	if exes == nil {
		return
	}
	var in strings.Builder
	for _, c := range cases {
		in.WriteString(strconv.Quote(c) + "\n")
	}
	stdout, stderr, code := runExeInput(t, exes["inputlines"], in.String())
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s", seed, code, firstLines(stderr, 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("seed %d: %d output lines for %d cases", seed, len(got), len(cases))
	}
	failed := 0
	for i, c := range cases {
		var parts []string
		for _, line := range inputLinesModel(c) {
			parts = append(parts, strconv.Quote(line))
		}
		if want := strings.Join(parts, " "); got[i] != want && failed < 5 {
			failed++
			t.Errorf("seed %d, case %d: the lines of %q are\n\t%s\nthe model gives\n\t%s\n(Ergo source: io.Input{Text: %s}.Lines() in internal/sketch/std/io/io.ergo; replay with ERGO_SEED=%d)", seed, i, c, got[i], want, strconv.Quote(c), seed)
		}
	}
}

// BenchmarkInputLines (SS3): 1 MiB of 80-byte lines through the lowered
// io.Input Lines, against hand Go that finds each line feed with
// strings.IndexByte. Both sum the line lengths. Run it at the lane base
// (the per-byte body) and at the head (the strings.Index body) on one
// quiet host.
func BenchmarkInputLines(b *testing.B) {
	const src = `import io

func LinesLength(text string) int {
	let mut n = 0
	for line in (io.Input{Text: text}).Lines() {
		n = n + len(line)
	}
	n
}

func main() needs Console {
	Console.Println("\(LinesLength("a\nb"))")
}
`
	emittedBench(b, "inputlinesbench", src, func(mainGo string) string {
		return `package main

import (
	"strings"
	"testing"
)

var inputText = strings.Repeat(strings.Repeat("x", 79)+"\n", 1<<20/80)
var sink int

func BenchmarkInputLinesErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "LinesLength", "inputText") + `
	}
}

func BenchmarkInputLinesGo(b *testing.B) {
	for range b.N {
		s, n := inputText, 0
		for len(s) > 0 {
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				n += len(s)
				break
			}
			n += i
			s = s[i+1:]
		}
		sink = n
	}
}
`
	}, "InputLinesErgo", "InputLinesGo")
}
