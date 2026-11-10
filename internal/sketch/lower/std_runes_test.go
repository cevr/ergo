package lower

import "testing"

// TestStdRunesRuns (std-text-runes): each new function of utf8, strings,
// strconv, math and bits lowers and runs from an Ergo program. A loop
// over DecodeRuneInString reads a text with a 4-byte rune and invalid
// bytes rune by rune, each invalid byte as U+FFFD of size 1, and ends;
// RuneText of a valid rune decodes back to it and that of a surrogate is
// U+FFFD; ToValidUTF8 replaces each run of invalid bytes once; FormatInt
// writes a negative number in base 2, 16 and 36; the math and bits
// functions give Go's answers, special cases included.
func TestStdRunesRuns(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{
		{name: "runes", src: stdRunesProgram, stdout: stdRunesOutput},
	})
}

const stdRunesProgram = `import utf8
import strings
import strconv
import math
import bits

func walk(s string) string {
	let mut out = ""
	let mut rest = s
	for _ in 0..len(s) {
		if len(rest) == 0 {
			break
		}
		let d = utf8.DecodeRuneInString(rest)
		out = out + "\(int(d.Rune)):\(d.Size) "
		rest = strings.Slice(rest, d.Size, len(rest))
	}
	out
}

func main() needs Console {
	let bad = strconv.Unquote("\"\\xff\\xe4\\xb8\"") catch {
	case strconv.Malformed:
		return
	}
	let text = "aé世" + utf8.RuneText(128512) + bad
	Console.Println(walk(text))
	let empty = utf8.DecodeRuneInString("")
	Console.Println("\(int(empty.Rune)):\(empty.Size)")
	let back = utf8.DecodeRuneInString(utf8.RuneText(128512))
	Console.Println("\(int(back.Rune)) \(back.Size) \(utf8.RuneLen(128512)) \(utf8.RuneLen(55296)) \(utf8.ValidRune(55296)) \(utf8.ValidRune(1114111))")
	Console.Println(strconv.Quote(utf8.RuneText(55296)) + " " + strconv.Quote(utf8.RuneText(-1)))
	Console.Println("\(utf8.ValidString(text)) \(utf8.ValidString(utf8.RuneText(233))) \(len(utf8.AppendRune([]byte{97}, 128512)))")
	Console.Println(strconv.Quote(strings.ToValidUTF8(text, "?")) + " " + strconv.Quote(strings.ToValidUTF8(text, "")))
	Console.Println(strconv.FormatInt(-255, 2) + " " + strconv.FormatInt(-255, 16) + " " + strconv.FormatInt(9223372036854775807, 36))
	Console.Println("\(math.Floor(-2.5)) \(math.Ceil(-2.5)) \(math.Trunc(-2.5)) \(math.Round(-2.5)) \(math.Abs(-3.0)) \(math.Sqrt(16.0)) \(math.Pow(2.0, 10.0)) \(math.Mod(-7.0, 3.0))")
	Console.Println("\(math.IsNaN(math.NaN())) \(math.IsInf(math.Inf(-1), -1)) \(math.IsInf(math.Inf(1), -1)) \(math.MaxInt64()) \(math.MinInt64()) \(math.MaxInt()) \(math.MinInt())")
	Console.Println("\(bits.OnesCount(255)) \(bits.OnesCount64(0)) \(bits.LeadingZeros(1)) \(bits.LeadingZeros64(0)) \(bits.TrailingZeros(8)) \(bits.TrailingZeros64(0)) \(bits.Len(255)) \(bits.Len64(0))")
	Console.Println("\(bits.RotateLeft32(2147483649, 1)) \(bits.RotateLeft32(3, -1)) \(bits.RotateLeft64(1, 63)) \(bits.RotateLeft64(1, -1))")
}
`

const stdRunesOutput = `97:1 233:2 19990:3 128512:4 65533:1 65533:1 65533:1 
65533:0
128512 4 4 -1 false true
"�" "�"
false true 5
"aé世😀?" "aé世😀"
-11111111 -ff 1y2p0ij32e8e7
-3 -2 -2 -3 3 4 1024 -1
true true false 9223372036854775807 -9223372036854775808 9223372036854775807 -9223372036854775808
8 0 63 64 3 64 8 0
3 2147483649 9223372036854775808 9223372036854775808
`
