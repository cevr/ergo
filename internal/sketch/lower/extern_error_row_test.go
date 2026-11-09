package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// TestExternErrorRowRuns is G4's property (gx-extern-error-row; decision
// O14): a user extern whose Go target returns an error declares fails
// go.Error, and a call gives Go's value, or fails go.Error whose Text is
// Go's err.Error(). A seeded generator writes texts that parse, overflow
// their bit size, carry a sign, a base prefix, an underscore, a space or a
// stray byte, and calls strconv's ParseUint, ParseInt, Atoi, ParseBool and
// ParseFloat on them through user externs, three ways: a direct call
// caught by a catch, a try that passes the failure to a caller's catch,
// and the extern used as a function value (G1). The reference is Go's
// strconv on the same text, computed in the test; on an error the line is
// Go's error text, so the partial value Go returns with a range error is
// dropped, never printed. A failure prints the replay flag and both lines.
//
// It builds and runs the program because the subject is the emitted Go: the
// target's (T, error) bound at the Ergo result's type, the error turned
// into v1.GoError, and a catch that matches it; the checker sees only the
// Ergo program.
var externErrorSeed = flag.Uint64("externerror.seed", 0, "run TestExternErrorRowRuns on this seed alone")

func TestExternErrorRowRuns(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2}
	if *externErrorSeed != 0 {
		seeds = []uint64{*externErrorSeed}
	}
	var src strings.Builder
	src.WriteString(`extern go strconv.ParseUint func ParseUint(s string, base int, bits int) uint64 fails go.Error
extern go strconv.ParseInt func ParseInt(s string, base int, bits int) int64 fails go.Error
extern go strconv.Atoi func Atoi(s string) int fails go.Error
extern go strconv.ParseBool func ParseBool(s string) bool fails go.Error
extern go strconv.ParseFloat func ParseFloat(s string, bits int) float64 fails go.Error

func unsigned(s string, base int, bits int) string {
	let n = ParseUint(s, base, bits) catch {
	case go.Error as e:
		return "error " + e.Text
	}
	"\(n)"
}

func signed(s string, base int, bits int) string {
	let n = ParseInt(s, base, bits) catch {
	case go.Error as e:
		return "error " + e.Text
	}
	"\(n)"
}

func passed(s string) int fails go.Error {
	let n = try Atoi(s)
	n * 2
}

func atoi(s string) string {
	let n = passed(s) catch {
	case go.Error as e:
		return "error " + e.Text
	}
	"\(n)"
}

func apply(f func(s string) bool fails go.Error, s string) string {
	let b = f(s) catch {
	case go.Error as e:
		return "error " + e.Text
	}
	"\(b)"
}

func half(s string) string {
	let f = ParseFloat(s, 64) catch {
	case go.Error as e:
		return "error " + e.Text
	}
	"\(f == 0.5)"
}

func main() needs Console {
`)
	var want []string
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6734))
		for range 40 {
			text := errorRowText(r)
			q := strconv.Quote(text)
			base := []int{0, 2, 8, 10, 16, 36}[r.IntN(6)]
			bits := []int{0, 8, 16, 32, 64}[r.IntN(5)]
			switch r.IntN(5) {
			case 0:
				fmt.Fprintf(&src, "\tConsole.Println(unsigned(%s, %d, %d))\n", q, base, bits)
				n, err := strconv.ParseUint(text, base, bits)
				want = append(want, errorRowLine(fmt.Sprint(n), err))
			case 1:
				fmt.Fprintf(&src, "\tConsole.Println(signed(%s, %d, %d))\n", q, base, bits)
				n, err := strconv.ParseInt(text, base, bits)
				want = append(want, errorRowLine(fmt.Sprint(n), err))
			case 2:
				fmt.Fprintf(&src, "\tConsole.Println(atoi(%s))\n", q)
				n, err := strconv.Atoi(text)
				want = append(want, errorRowLine(fmt.Sprint(n*2), err))
			case 3:
				bools := []string{"true", "F", "1", "0", "TRUE", "yes", ""}
				text = bools[r.IntN(len(bools))]
				fmt.Fprintf(&src, "\tConsole.Println(apply(ParseBool, %s))\n", strconv.Quote(text))
				b, err := strconv.ParseBool(text)
				want = append(want, errorRowLine(fmt.Sprint(b), err))
			default:
				floats := []string{"0.5", "5e-1", "1e400", "x", "0x1p-1", ".5", ""}
				text = floats[r.IntN(len(floats))]
				fmt.Fprintf(&src, "\tConsole.Println(half(%s))\n", strconv.Quote(text))
				f, err := strconv.ParseFloat(text, 64)
				want = append(want, errorRowLine(fmt.Sprint(f == 0.5), err))
			}
		}
	}
	src.WriteString("}\n")
	prog := mustLower(t, "extern_error_row", src.String())
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["extern_error_row"])
	if code != 0 {
		t.Fatalf("exit %d, stderr %s\n%s", code, firstLines(stderr, 8), src.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines for %d cases:\n%s", len(got), len(want), stdout)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d (replay with -externerror.seed=N):\nergo %s\ngo   %s", i, got[i], want[i])
		}
	}
}

// errorRowLine is the line a case prints: Go's error text, or the value.
func errorRowLine(value string, err error) string {
	if err != nil {
		return "error " + err.Error()
	}
	return value
}

// errorRowText is a number text that may parse: digits of a size that may
// overflow, with a sign, a base prefix, an underscore, a space or a stray
// byte.
func errorRowText(r *rand.Rand) string {
	digits := "0123456789abcdefz"
	var b strings.Builder
	switch r.IntN(4) {
	case 0:
		b.WriteByte('-')
	case 1:
		b.WriteByte('+')
	}
	if r.IntN(4) == 0 {
		b.WriteString([]string{"0x", "0b", "0o", "0"}[r.IntN(4)])
	}
	n := 1 + r.IntN(24)
	for i := 0; i < n; i++ {
		switch r.IntN(30) {
		case 0:
			b.WriteByte('_')
		case 1:
			b.WriteByte(' ')
		default:
			if r.IntN(5) == 0 {
				b.WriteByte(digits[r.IntN(len(digits))])
			} else {
				b.WriteByte(digits[r.IntN(10)])
			}
		}
	}
	return b.String()
}
