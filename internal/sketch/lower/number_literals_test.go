package lower

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// TestErgoRunNumberLiterals (L1 acceptance): hexadecimal, octal and binary
// literals and digit separators check, lower and run. The a2 and a2b
// repros of the examples wave print their values, and seeded literals in
// each base print the value Go's strconv.ParseInt(text, 0, 64) reads; a
// literal takes the type its context expects (uint8, float64, duration).
func TestErgoRunNumberLiterals(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 0x4c31))
	var b strings.Builder
	b.WriteString(`func main() needs Console {
	let a = 0x80
	let p = 0o600
	let c = 1_000
	Console.Println("\(a) \(p) \(c)")
	let m uint8 = 0xFF
	let f float64 = 0x10 + 0.5
	let d duration = 1_500ms
	Console.Println("\(m) \(f) \(d)")
`)
	want := "128 384 1000\n255 16.5 1.5s\n"
	for i := range 24 {
		v := r.Uint64() >> uint(1+r.IntN(62))
		base := []int{2, 8, 10, 16}[i%4]
		lit := strconv.FormatUint(v, base)
		if base == 16 {
			lit = strings.ToUpper(lit)
		}
		if len(lit) > 3 {
			lit = lit[:1] + "_" + lit[1:]
		}
		lit = map[int]string{2: "0b", 8: "0o", 10: "", 16: "0x"}[base] + lit
		n, err := strconv.ParseInt(lit, 0, 64)
		if err != nil {
			t.Fatalf("the test wrote %s, which Go refuses: %v", lit, err)
		}
		b.WriteString("\tlet v" + strconv.Itoa(i) + " int64 = " + lit + "\n")
		b.WriteString("\tConsole.Println(\"" + lit + " \\(v" + strconv.Itoa(i) + ")\")\n")
		want += lit + " " + strconv.FormatInt(n, 10) + "\n"
	}
	b.WriteString("}\n")
	wantRun(t, b.String(), want)
}
