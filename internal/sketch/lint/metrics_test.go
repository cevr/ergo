package lint_test

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// bodyModel generates a function body from a small grammar of Ergo
// statements and computes, as it writes, each metric of the strict preset
// by the metric's definition: the reference the rules must agree with.
type bodyModel struct {
	r          *rand.Rand
	b          strings.Builder
	cyclomatic int
	cognitive  int
	operators  map[string]bool
	operands   map[string]bool
	total      int // N2: operand occurrences
}

func (m *bodyModel) op(name string) { m.operators[name] = true }

func (m *bodyModel) operand(text string) {
	m.operands[text] = true
	m.total++
}

func (m *bodyModel) line(depth int, text string) {
	m.b.WriteString(strings.Repeat("\t", depth) + text + "\n")
}

// cond writes a condition of 1 to 4 terms joined by && and ||, without
// parentheses, and counts it: each operator is a path (cyclomatic), each
// change of operator in source order a new run (cognitive).
func (m *bodyModel) cond() string {
	terms := 1 + m.r.IntN(4)
	var b strings.Builder
	var prev string
	for i := 0; i < terms; i++ {
		if i > 0 {
			o := []string{"&&", "||"}[m.r.IntN(2)]
			b.WriteString(" " + o + " ")
			m.op(o)
			m.cyclomatic++
			if i == 1 || o != prev {
				m.cognitive++
			}
			prev = o
		}
		switch k := m.r.IntN(4); k {
		case 3:
			b.WriteString("n > 0")
			m.op(">")
			m.operand("id n")
			m.operand("lit 0")
		default:
			name := []string{"p", "q", "r"}[k]
			b.WriteString(name)
			m.operand("id " + name)
		}
	}
	return b.String()
}

func (m *bodyModel) inc(depth int) {
	m.line(depth, "count = count + 1")
	m.op("=")
	m.op("+")
	m.operand("id count")
	m.operand("id count")
	m.operand("lit 1")
}

// stmts writes 1 to 3 statements at depth (1 is the body) and nesting
// (cognitive's level).
func (m *bodyModel) stmts(depth, nesting int) {
	for i := 0; i < 1+m.r.IntN(3); i++ {
		kind := m.r.IntN(5)
		if depth > 3 {
			kind = 0
		}
		switch kind {
		case 0, 1:
			m.inc(depth)
		case 2:
			m.op("if")
			m.cyclomatic++
			m.cognitive += 1 + nesting
			m.line(depth, "if "+m.cond()+" {")
			m.stmts(depth+1, nesting+1)
			for m.r.IntN(2) == 0 {
				m.op("if")
				m.cyclomatic++
				m.cognitive++
				m.line(depth, "} else if "+m.cond()+" {")
				m.stmts(depth+1, nesting+1)
			}
			if m.r.IntN(2) == 0 {
				m.cognitive++
				m.line(depth, "} else {")
				m.stmts(depth+1, nesting+1)
			}
			m.line(depth, "}")
		case 3:
			m.op("for")
			m.cyclomatic++
			m.cognitive += 1 + nesting
			x := fmt.Sprintf("x%d", depth) // no shadowing (S108)
			m.operand("id " + x)
			m.operand("id xs")
			m.line(depth, "for "+x+" in xs {")
			m.line(depth+1, "count = count + "+x)
			m.op("=")
			m.op("+")
			m.operand("id count")
			m.operand("id count")
			m.operand("id " + x)
			if m.r.IntN(2) == 0 {
				m.stmts(depth+1, nesting+1)
			}
			m.line(depth, "}")
		case 4:
			m.op("match")
			m.op("case")
			m.cyclomatic += 2
			m.cognitive += 1 + nesting
			m.operand("id n")
			m.line(depth, "match n {")
			m.line(depth, "case 0:")
			m.operand("lit 0")
			m.stmts(depth+1, nesting+1)
			m.line(depth, "case _:") // a wildcard pattern, no operand
			m.stmts(depth+1, nesting+1)
			m.line(depth, "}")
		}
	}
}

// generatedFunc is one generated function and its reference metrics.
type generatedFunc struct {
	text                  string
	cognitive, cyclomatic int
	halstead              float64
}

func generateFunc(r *rand.Rand, name string) generatedFunc {
	m := &bodyModel{r: r, cyclomatic: 1, operators: map[string]bool{}, operands: map[string]bool{}}
	m.line(0, "func "+name+"(n int, p bool, q bool, r bool, xs []int) int {")
	m.line(1, "let mut count = 0")
	m.op("let")
	m.operand("id count")
	m.operand("lit 0")
	m.stmts(1, 0)
	m.line(1, "count")
	m.operand("id count")
	m.line(0, "}")
	h := float64(len(m.operators)) / 2 * float64(m.total) / float64(len(m.operands))
	return generatedFunc{text: m.b.String(), cognitive: m.cognitive, cyclomatic: m.cyclomatic, halstead: h}
}

// TestComplexityAgreesWithReference generates functions from a grammar of
// ifs, else-ifs, elses, loops, matches and boolean conditions, with the
// metric of each computed by the generator, and checks that each cap rule
// fires on exactly the functions above a random max, and reports the
// generator's value.
func TestComplexityAgreesWithReference(t *testing.T) {
	type metric struct {
		rule  string
		value func(generatedFunc) float64
	}
	metrics := []metric{
		{"cognitive-complexity", func(g generatedFunc) float64 { return float64(g.cognitive) }},
		{"cyclomatic-complexity", func(g generatedFunc) float64 { return float64(g.cyclomatic) }},
		{"halstead-difficulty", func(g generatedFunc) float64 { return g.halstead }},
	}
	for _, seed := range propertySeeds(60) {
		r := rand.New(rand.NewPCG(seed, 0xc7))
		var src strings.Builder
		var funcs []generatedFunc
		var lines []int
		line := 1
		for i := 0; i < 1+r.IntN(4); i++ {
			g := generateFunc(r, fmt.Sprintf("g%d", i))
			funcs = append(funcs, g)
			lines = append(lines, line)
			src.WriteString(g.text + "\n")
			line += strings.Count(g.text, "\n") + 1
		}
		// main's body is empty: cyclomatic 1, the others 0.
		funcs = append(funcs, generatedFunc{text: "func main() {\n}\n", cyclomatic: 1})
		lines = append(lines, line)
		src.WriteString("func main() {\n}\n")
		in := lintInput(src.String())
		if !in.Checked {
			t.Fatalf("seed %d: the generated program does not check:\n%s", seed, numbered(src.String()))
		}
		for _, mt := range metrics {
			values := make([]float64, len(funcs))
			for i, g := range funcs {
				values[i] = mt.value(g)
			}
			max := math.Max(0, values[r.IntN(len(values))]-1+float64(r.IntN(3)))
			var want []int
			for i, v := range values {
				if v > max {
					want = append(want, lines[i])
				}
			}
			options, _ := json.Marshal(map[string]float64{"max": max})
			settings := map[string]lint.Setting{}
			for _, rule := range lint.Builtin().Rules() {
				settings[rule.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
			}
			settings[mt.rule] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: options}
			conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
			if err != nil {
				t.Fatal(err)
			}
			res := conf.Run(in)
			var got []int
			for _, f := range res.Findings {
				got = append(got, f.Line)
				i := slices.Index(lines, f.Line)
				if i >= 0 && !strings.Contains(f.Message, fmt.Sprintf(" is %v,", values[i])) {
					t.Fatalf("seed %d: %s reports %q; the reference value is %v\n%s", seed, mt.rule, f.Message, values[i], numbered(src.String()))
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("seed %d: %s with max %v fired on lines %v, the reference says %v (values %v)\n%s\nreplay: go test ./internal/sketch/lint -run TestComplexityAgreesWithReference -lint.seed=%d", seed, mt.rule, max, got, want, values, numbered(src.String()), seed)
			}
		}
	}
}
