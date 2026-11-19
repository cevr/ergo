package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// StdBodies are the std functions GenerateStd's programs call, by package
// and name: library bodies (G5), each of which gets an IR body at its own
// std file and line in every seed.
var StdBodies = [][2]string{
	{"time", "Unix"}, {"time", "UnixSeconds"}, {"time", "Nanosecond"},
	{"time", "Before"}, {"time", "After"}, {"time", "Equal"}, {"time", "Sub"},
	{"time", "LayoutText"},
	{"base64", "Encode"}, {"base64", "Decode"},
}

// GenerateStd is a program that calls StdBodies with seeded operands:
// instants with nanoseconds outside a second (Unix carries them), equal
// and ordered pairs, every layout's text, and base64 texts in both
// alphabets, some of them malformed (Decode fails, a catch reads its
// offset). main prints every result.
func GenerateStd(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	b.WriteString("import base64\nimport time\n\n")
	b.WriteString("func decoded(e base64.Encoding, s string) string {\n\tbase64.Decode(e, s) catch {\n\tcase base64.Malformed as m:\n\t\t\"bad \\(m.Offset)\"\n\t}\n}\n\n")
	b.WriteString("func main() needs Console {\n")
	nsec := func() int64 {
		switch r.IntN(4) {
		case 0:
			return -r.Int64N(3_000_000_000)
		case 1:
			return 1_000_000_000 + r.Int64N(3_000_000_000)
		default:
			return r.Int64N(1_000_000_000)
		}
	}
	for i := 0; i < 4; i++ {
		s1, n1 := r.Int64N(4_000_000_000)-2_000_000_000, nsec()
		s2, n2 := s1+r.Int64N(5)-2, nsec()
		if r.IntN(3) == 0 {
			s2, n2 = s1, n1
		}
		fmt.Fprintf(&b, "\tlet a%d = time.Unix(%d, %d)\n\tlet b%d = time.Unix(%d, %d)\n", i, s1, n1, i, s2, n2)
		fmt.Fprintf(&b, "\tConsole.Println(\"unix %d \\(a%d.UnixSeconds()) \\(a%d.Nanosecond()) \\(b%d.UnixSeconds()) \\(b%d.Nanosecond())\")\n", i, i, i, i, i)
		fmt.Fprintf(&b, "\tConsole.Println(\"cmp %d \\(a%d.Before(b%d)) \\(a%d.After(b%d)) \\(a%d.Equal(b%d)) \\(int64(a%d.Sub(b%d)))\")\n", i, i, i, i, i, i, i, i, i)
	}
	layouts := []string{"time.RFC3339", "time.RFC3339Nano", "time.DateTime", "time.DateOnly", "time.TimeOnly", "time.Custom{Text: \"15:04\"}"}
	r.Shuffle(len(layouts), func(i, j int) { layouts[i], layouts[j] = layouts[j], layouts[i] })
	for _, l := range layouts[:3] {
		fmt.Fprintf(&b, "\tConsole.Println(\"layout \\(time.LayoutText(%s))\")\n", l)
	}
	const letters = "ab+/-_=Zz09 \n"
	for i := 0; i < 4; i++ {
		var s strings.Builder
		for j := r.IntN(9); j > 0; j-- {
			s.WriteByte(letters[r.IntN(len(letters)-2)])
		}
		enc := []string{"base64.StdEncoding", "base64.URLEncoding"}[r.IntN(2)]
		text := fmt.Sprintf("%q", s.String())
		fmt.Fprintf(&b, "\tlet e%d = base64.Encode(%s, %s)\n", i, enc, text)
		fmt.Fprintf(&b, "\tConsole.Println(\"b64 %d \\(e%d) \\(decoded(%s, e%d))\")\n", i, i, enc, i)
		// A text that may not be base64 in the alphabet: Decode's failure.
		var junk strings.Builder
		for j := 1 + r.IntN(8); j > 0; j-- {
			junk.WriteByte(letters[r.IntN(len(letters))])
		}
		fmt.Fprintf(&b, "\tConsole.Println(\"junk %d \\(decoded(%s, %q))\")\n", i, enc, junk.String())
	}
	b.WriteString("}\n")
	return b.String()
}
