package lower

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// runePieces are the pieces a generated string is made of: ASCII, two-,
// three- and four-byte runes, U+FFFD itself, invalid bytes (a lone
// continuation byte, 0xff, an overlong encoding), a truncated multi-byte
// sequence, an encoded surrogate, and quoting's specials. Go's range gives
// each invalid byte as U+FFFD of width one.
var runePieces = []string{
	"a", "Z", " ", "0", "\\", "\"", "(", "\n", "\t",
	"é", "ß", "Ж", "€", "世", "界", "\ufffd", "😀", "𝄞",
	"\x80", "\xff", "\xc0\xaf", "\xe2\x82", "\xf0\x9f\x98", "\xed\xa0\x80", "\x00",
}

// genRuneStrings returns n strings of up to 12 pieces each, the empty
// string first.
func genRuneStrings(r *rand.Rand, n int) []string {
	out := []string{""}
	for len(out) < n {
		var b strings.Builder
		for range r.IntN(13) {
			b.WriteString(runePieces[r.IntN(len(runePieces))])
		}
		out = append(out, b.String())
	}
	return out
}

// runesProgram is an Ergo program that walks each string with the three
// forms, `for i, r in s`, `for r in s` and `for i, _ in s`, and the
// output Go's own range over the same strings gives (the model).
func runesProgram(strs []string) (string, string) {
	var src, want strings.Builder
	src.WriteString("func main() needs Console {\n\tlet strs = []string{\n")
	for _, s := range strs {
		// strconv.Quote spells invalid bytes as \x escapes, which Ergo
		// decodes by Go's rules (grammar.md, Literals 4).
		src.WriteString("\t\t" + strconv.Quote(s) + ",\n")
	}
	src.WriteString(`	}
	for s in strs {
		for i, r in s {
			Console.Println("\(i) \(r)")
		}
		let mut n = 0
		let mut sum = 0
		for r in s {
			n += 1
			sum += int(r)
		}
		Console.Println("n \(n) sum \(sum)")
		let mut last = -1
		for i, _ in s {
			last = i
		}
		Console.Println("last \(last)")
	}
}
`)
	for _, s := range strs {
		for i, r := range s {
			fmt.Fprintf(&want, "%d %d\n", i, r)
		}
		n, sum := 0, 0
		for _, r := range s {
			n++
			sum += int(r)
		}
		fmt.Fprintf(&want, "n %d sum %d\n", n, sum)
		last := -1
		for i := range s {
			last = i
		}
		fmt.Fprintf(&want, "last %d\n", last)
	}
	return src.String(), want.String()
}

// TestErgoRunRunesLoop (L4 ln-runes-loop; grammar.md Statements 1,
// types.md Loops 1): `for r in s` and `for i, r in s` iterate a string's
// runes as Go's `for i, r := range s` does: i is the byte offset of the
// rune's first byte, and each invalid byte is U+FFFD of width one. A
// seeded property over generated strings, valid and invalid UTF-8, against
// Go's range as the model. A failing seed prints the program.
func TestErgoRunRunesLoop(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x4c34))
		src, want := runesProgram(genRuneStrings(r, 40))
		if _, ds := check.CheckSource(src); len(ds) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, ds, src)
		}
		out, err := ergoRun(t, src)
		if err != nil || out != want {
			t.Fatalf("seed %d: ergo run (%v) differs from Go's range\n--- got\n%s\n--- want\n%s\n--- source\n%s", seed, err, out, want, src)
		}
	}
}

// TestRunesLoopLowersToGoRange: each form is Go's range over the string
// itself, with no []rune or []byte conversion, which would allocate (or,
// for bytes, change the meaning).
func TestRunesLoopLowersToGoRange(t *testing.T) {
	for _, form := range []string{"for r in s {\n\t\tn += int(r)\n\t}", "for i, r in s {\n\t\tn += i + int(r)\n\t}", "for i, _ in s {\n\t\tn += i\n\t}"} {
		src := "func f(s string) int {\n\tlet mut n = 0\n\t" + form + "\n\tn\n}\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(f(\"ab\"))\")\n}\n"
		gosrc := mustLower(t, "runes", src).mainGo()
		if strings.Contains(gosrc, "[]rune(") || strings.Contains(gosrc, "[]byte(") || !strings.Contains(gosrc, "range s") {
			t.Fatalf("%s lowers to more than Go's range over the string:\n%s", form, gosrc)
		}
	}
}

// TestErgoRunRunesLoopControl: break, continue and return inside a for
// over a string, and a defined string type, behave as over a slice.
func TestErgoRunRunesLoopControl(t *testing.T) {
	wantRun(t, `type Name string

// firstWide is the byte offset of the first rune wider than one byte.
func firstWide(s string) int {
	for i, r in s {
		if r > 127 {
			return i
		}
	}
	-1
}

func main() needs Console {
	let mut ascii = 0
	for r in "aé b世" {
		if r > 127 {
			continue
		}
		if r == 32 {
			break
		}
		ascii += 1
	}
	let mut runes = 0
	for _, _ in Name("Жx") {
		runes += 1
	}
	Console.Println("\(firstWide("ab€")) \(firstWide("ab")) \(ascii) \(runes)")
}
`, "2 -1 1 2\n")
}
