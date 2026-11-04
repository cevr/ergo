package check_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestInterpolationRules is the text-form rule of an interpolated part
// (check-v1 E6a A2; D-E6; Q5) as a table: the E7-interpolation acceptance
// rules, each planner probe with its verdict after E6a, and one case per
// row of the A3 domain (textTypes) in a test body, a law body and a
// function literal. A row whose value no test body can build (a pointer, a
// type parameter) has no test-body case; a type parameter's law is on a
// generic operation type.
func TestInterpolationRules(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		for _, name := range []string{"E7-interpolation.pos", "E7-interpolation.neg"} {
			data, err := os.ReadFile(filepath.Join("testdata", "v1", name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			_, diags := check.CheckSource(src)
			if want, have := expected(src), got(diags); strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("%s: want %v, got %v\n\t%s", name, want, have, describe(diags))
			}
		}
	})
	t.Run("probes", func(t *testing.T) {
		for _, p := range interpProbes {
			_, diags := check.CheckSource(p.src)
			if want, have := expected(p.src), got(diags); strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("%s: want %v, got %v\n\t%s", p.name, want, have, describe(diags))
			}
		}
	})
	n := 0
	for _, tt := range textTypes() {
		for _, body := range textBodies(tt) {
			src, line, col := textProgram(tt, body.src)
			_, diags := check.CheckSource(src)
			want, have := textOracle(tt, line, col), textGot(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("%s, %s: want %v, got %v\n\t%s\n%s", tt.name, body.name, want, have, describe(diags), src)
			}
			n++
		}
	}
	if n < 3*len(textTypes())-6 {
		t.Errorf("%d body cases for %d rows", n, len(textTypes()))
	}
}

// textBodies are tt's part in a test body, a law body and a function
// literal ($TP, $T and $V as in textPositions).
func textBodies(tt textType) []struct{ name, src string } {
	var out []struct{ name, src string }
	if tt.mk != "" {
		out = append(out, struct{ name, src string }{"a test body", "test T {\n\t" + tt.mk + "\n\tassert \"<\\($V)>\" != \"\"\n}\n"})
	}
	out = append(out,
		struct{ name, src string }{"a law body", "type Codec$TP {\n\tEnc(n int) string,\n\n\tlaw L(v $T) { \"<\\($V)>\" != \"\" },\n}\n"},
		struct{ name, src string }{"a function literal", "func h$TP(w $T) int {\n\tlet k = func(v $T) string { \"<\\($V)>\" }\n\tlen(k(w))\n}\n"},
	)
	return out
}

// interpProbes are the planner's E6a probes
// (check-v1-wave2-plan/probe/e6-*.ergo) with their verdicts after E6a as
// want markers.
var interpProbes = []struct{ name, src string }{
	{"e6-interp", `// E6a probe: interpolation parts by type (D-E6, Q5).
type Celsius float64
type Name string
type Card { n int }
type Pick = Yes{} | No{}

func main() needs Console {
	let t Celsius = 21.5
	let n Name = "ada"
	let d duration = 3s
	Console.Println("t=\(t) n=\(n) d=\(d)")
}
`},
	{"e6-other", `// E6a probe: parts with no text form in v1 (Q5: S109 naming #49).
type Card { n int }
type Pick = Yes{} | No{}

func main() needs Console {
	let c = Card{n: 1}
	let p Pick = Yes{}
	let z complex128 = 2i // want S001
	Console.Println("c=\(c)")
	Console.Println("p=\(p)")
	Console.Println("xs=\([]int{1, 2})")
	Console.Println("z=\(z)")
	Console.Println("t=\((1, 2))")
}
`},
	{"e6-other with a zero complex", `type Card { n int }
type Pick = Yes{} | No{}

func main() needs Console {
	let c = Card{n: 1}
	let p Pick = Yes
	let z complex128
	Console.Println("c=\(c)") // want S109
	Console.Println("p=\(p)") // want S109
	Console.Println("xs=\([]int{1, 2})") // want S109
	Console.Println("z=\(z)")
	Console.Println("t=\((1, 2))") // want S109
	Console.Println("c.n=\(c.n)")
}
`},
	{"e6-fallible", `// E6a probe: a fallible interpolation part (D-E6: S105 unless tried).
type Bad {}

func get() int fails Bad {
	1
}

func show() string fails Bad {
	"a \(get()) b \(try get())" // want S105
}

func main() {}
`},
	{"one diagnostic per refused part, the other parts still checked", `type Card { n int }

func f(c Card, n int) string {
	"\(c) \(n + "x") \(c.n) \(c)" // want S108 S109 S109
}
`},
}
