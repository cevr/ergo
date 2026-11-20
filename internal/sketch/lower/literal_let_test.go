package lower

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// literalLetProgram is a program of drawn package lets, and which of them
// are constant data: literals of slices, arrays and records over constants
// (static), or a let with a call, a record update or a Map in it, at any
// depth (not static).
func literalLetProgram(seed uint64) (src string, static map[string]bool) {
	r := rand.New(rand.NewPCG(seed, 0x4c4c4554))
	var b strings.Builder
	b.WriteString(`type Pt { X int, Y float64 }

type Row { Name string, On bool, At Pt, Tags []string, Grid [2]int }

func pt(n int) Pt { Pt{X: n, Y: 0.5} }

`)
	static = map[string]bool{}
	// value is a literal of type t ("pt", "row", "rows", "grid", "tags"),
	// and whether it is constant data; dyn is the chance in 8 that a
	// part is not.
	var value func(t string, dyn int) (string, bool)
	value = func(t string, dyn int) (string, bool) {
		n := r.IntN(100) - 20
		switch t {
		case "pt":
			switch {
			case r.IntN(8) < dyn && r.IntN(2) == 0:
				return fmt.Sprintf("pt(%d)", n), false
			case r.IntN(8) < dyn:
				return fmt.Sprintf("Pt{..pt(1), X: %d}", n), false
			}
			return fmt.Sprintf("Pt{X: %d, Y: %d.25}", n, r.IntN(9)), true
		case "tags":
			var parts []string
			for i := range r.IntN(3) {
				parts = append(parts, fmt.Sprintf("%q", fmt.Sprintf("t%d-%d", n, i)))
			}
			return "[]string{" + strings.Join(parts, ", ") + "}", true
		case "grid":
			return fmt.Sprintf("[2]int{%d, %d}", n, -n), true
		case "row":
			at, ok := value("pt", dyn)
			tags, _ := value("tags", dyn)
			grid, _ := value("grid", dyn)
			return fmt.Sprintf("Row{Name: %q, On: %v, At: %s, Tags: %s, Grid: %s}", fmt.Sprint("r", n), n%2 == 0, at, tags, grid), ok
		default: // rows
			var parts []string
			ok := true
			for range 1 + r.IntN(4) {
				v, s := value("row", dyn)
				parts = append(parts, v)
				ok = ok && s
			}
			return "[]Row{" + strings.Join(parts, ", ") + "}", ok
		}
	}
	for i := range 3 + r.IntN(6) {
		name := fmt.Sprintf("lit%d", i)
		var v string
		ok := true
		switch k := r.IntN(7); k {
		case 0:
			v = fmt.Sprintf("Map[string, int]{%q: %d}", name, i)
			ok = false
		default:
			t := []string{"pt", "row", "rows", "grid", "tags", "rows"}[k-1]
			v, ok = value(t, r.IntN(3))
		}
		fmt.Fprintf(&b, "let %s = %s\n\n", name, v)
		static[name] = ok
	}
	b.WriteString("func main() {}\n")
	return b.String(), static
}

// TestLiteralLetsAreStaticData: a package let of constant data lowers to a
// Go variable whose initializer is the literal, which gc lays out as static
// data, so the program does no work at start for it (LITERAL-LET); a let
// with a call, a record update or a Map in it anywhere stays in initFunc.
// Over drawn lets, each static let's Go declaration has the literal and
// initFunc never assigns it, each other let initFunc assigns, and every
// program's Go type-checks (typecheckGo).
//
// Mutants (lower ledger): literal-let-never-static (no let is static
// data), literal-let-update-static (a record update counts as a literal)
// and literal-let-map-static (a Map literal counts as one).
func TestLiteralLetsAreStaticData(t *testing.T) {
	var programs []*lowered
	for seed := uint64(1); seed <= 12; seed++ {
		src, static := literalLetProgram(seed)
		p := lowerSource(fmt.Sprintf("literallet%d", seed), src, true)
		if p.err != nil || len(p.diags) > 0 || p.panicked != nil {
			t.Fatalf("seed %d: %v %v %v\n%s", seed, p.err, p.diags, p.panicked, src)
		}
		code := p.mainGo()
		init := ""
		if i := strings.Index(code, "func ergoInit("); i >= 0 {
			init = code[i:]
			if j := strings.Index(init, "\n}\n"); j >= 0 {
				init = init[:j]
			}
		}
		for name, want := range static {
			declared := regexp.MustCompile(`(?m)^var ` + name + ` [^\n=]*= `).MatchString(code)
			assigned := regexp.MustCompile(`\b` + name + ` = `).MatchString(init)
			if declared != want || assigned == want {
				t.Errorf("seed %d: let %s, constant data %v: Go initializer %v, assigned in initFunc %v\nReplay: go test -run '^TestLiteralLetsAreStaticData$' ./internal/sketch/lower\n%s", seed, name, want, declared, assigned, src)
			}
		}
		programs = append(programs, p)
	}
	typecheckGo(t, programs)
}
