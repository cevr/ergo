package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// rowGapPrelude declares a generic record whose E is an error row (viaSlot
// fails E) with a function field and a value field of it, and a generic
// function with a function parameter of E: the declarations' places a
// function of Boom, or a Boom, meets at Slot[Boom] (rowgap.go).
const rowGapPrelude = `type Boom { Run int }

type Slot[E] { Of func(e E) int, V E }

func apply[E](f func(e E) int, h func() int fails E) int {
	let v = h() catch {
	case err:
		return f(err)
	}
	v
}

func viaSlot[E](s Slot[E], h func() int fails E) int {
	let v = h() catch {
	case err:
		return s.Of(err)
	}
	v
}

func named(e Boom) int { 100 + e.Run }
`

// rowGapPlaces are the kinds of place the property writes a value into or
// reads it from, each a place rowgap.go lists: an argument, a record
// literal's field, a field assignment, a called field (its operand a
// write, its value a read), a field read as a function value, a field read
// as a record. rowGapForms are the functions of Boom it writes: a
// literal, a named function, a local holding a literal, and a literal
// whose parameter type the argument infers (argument places only).
var (
	rowGapPlaces = []string{"argument", "field", "assign", "called", "readfunc", "readvalue"}
	rowGapForms  = []string{"literal", "named", "local", "inferred"}
)

// rowGapCase is case k of a program: its function, the value it returns
// and its place and form.
type rowGapCase struct {
	src         string
	want        int
	place, form string
}

// rowGapGen writes case k.
func rowGapGen(r *rand.Rand, k int) rowGapCase {
	run := r.IntN(50)
	place := rowGapPlaces[r.IntN(len(rowGapPlaces))]
	forms := rowGapForms
	if place != "argument" {
		forms = forms[:3]
	}
	form := forms[r.IntN(len(forms))]
	var pre, f string
	want := run
	switch form {
	case "literal":
		f, want = "func(e Boom) int { 200 + e.Run }", 200+run
	case "named":
		f, want = "named", 100+run
	case "local":
		pre = "\tlet g = func(e Boom) int { 300 + e.Run }\n"
		f, want = "g", 300+run
	default:
		f, want = "func(e) { 400 + e.Run }", 400+run
	}
	boom := fmt.Sprintf("Boom{Run: %d}", run)
	h := fmt.Sprintf("func() int fails Boom { %s }", boom)
	var body string
	switch place {
	case "argument":
		body = fmt.Sprintf("\tapply(%s, %s)\n", f, h)
	case "field":
		body = fmt.Sprintf("\tviaSlot(Slot[Boom]{Of: %s, V: Boom{Run: 0}}, %s)\n", f, h)
	case "assign":
		body = fmt.Sprintf("\tlet mut s = Slot[Boom]{Of: named, V: Boom{Run: 0}}\n\ts.Of = %s\n\tviaSlot(s, %s)\n", f, h)
	case "called":
		body = fmt.Sprintf("\tlet s = Slot[Boom]{Of: %s, V: %s}\n\ts.Of(s.V)\n", f, boom)
	case "readfunc":
		body = fmt.Sprintf("\tlet s = Slot[Boom]{Of: %s, V: Boom{Run: 0}}\n\tlet o = s.Of\n\to(%s)\n", f, boom)
	default:
		body = fmt.Sprintf("\tlet s = Slot[Boom]{Of: %s, V: %s}\n\tlet b = s.V\n\tb.Run + 0 * s.Of(b)\n", f, boom)
		want = run
	}
	src := fmt.Sprintf("func case%d() int {\n%s%s}\n\ntest c%d {\n\tassert case%d() == %d\n}\n", k, pre, body, k, k, want)
	return rowGapCase{src: src, want: want, place: place, form: form}
}

// rowGapProgram is seed's program, the lines its main prints and the
// cases.
func rowGapProgram(seed uint64) (string, string, []rowGapCase) {
	r := rand.New(rand.NewPCG(seed, 61))
	var b, main, want strings.Builder
	b.WriteString(rowGapPrelude)
	var cases []rowGapCase
	for k := range 16 {
		c := rowGapGen(r, k)
		cases = append(cases, c)
		b.WriteString("\n" + c.src)
		fmt.Fprintf(&main, "\tConsole.Println(\"\\(case%d())\")\n", k)
		want.WriteString(strconv.Itoa(c.want) + "\n")
	}
	b.WriteString("\nfunc main() needs Console {\n" + main.String() + "}\n")
	return b.String(), want.String(), cases
}

func rowGapSeeds(t *testing.T) []uint64 {
	if s := os.Getenv("ERGO_ROWGAP_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_ROWGAP_SEED=%q: %v", s, err)
		}
		return []uint64{v}
	}
	return []uint64{1, 2, 3, 4, 5, 6}
}

// TestRowGapsAgree is the seeded property of the row gaps (rowgap.go,
// NARROW-GO): a function of Boom, or a Boom, goes into or comes out of
// each kind of place of Slot[Boom] and apply[Boom], whose declarations
// erase E to Go's error. The evaluator passes each case's assert (the
// model: the value the generator computed); the text path type-checks
// (go/types); the IR path gives every case an IR body. The seeds reach
// every place and form. Replay: ERGO_ROWGAP_SEED=<seed>.
//
// Mutants it kills (testdata/mutants.json): rowgap-no-wrap,
// rowgap-double-wrap, rowgap-no-read, ir-rowgap-no-write.
func TestRowGapsAgree(t *testing.T) {
	reached := map[string]bool{}
	var programs []*lowered
	for _, seed := range rowGapSeeds(t) {
		src, _, cases := rowGapProgram(seed)
		replay := fmt.Sprintf("replay: ERGO_ROWGAP_SEED=%d go test -run TestRowGapsAgree ./internal/sketch/lower", seed)
		for _, c := range cases {
			reached["place "+c.place] = true
			reached["form "+c.form] = true
		}
		rep, err := evaluate(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s\n%s", seed, err, src, replay)
		}
		for _, r := range rep.Results {
			if r.Status != eval.Passed {
				t.Fatalf("seed %d: the evaluator: test %s %s: %s\n%s\n%s", seed, r.Name, r.Status, r.Reason, src, replay)
			}
		}
		o := mustLower(t, fmt.Sprintf("rowgap%d", seed), src)
		programs = append(programs, o)
		u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		_, irRep, err := LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: LowerIR: %v\n%s", seed, err, replay)
		}
		for k, c := range cases {
			if name := fmt.Sprintf("case%d", k); !slices.Contains(irRep.IR, name) {
				t.Errorf("seed %d: %s (%s, %s) has no IR body (fallbacks %v)\n%s", seed, name, c.place, c.form, irRep.Fallback, replay)
			}
		}
	}
	typecheckGo(t, programs)
	if os.Getenv("ERGO_ROWGAP_SEED") == "" {
		for _, p := range rowGapPlaces {
			if !reached["place "+p] {
				t.Errorf("no case reached the place %s", p)
			}
		}
		for _, f := range rowGapForms {
			if !reached["form "+f] {
				t.Errorf("no case reached the form %s", f)
			}
		}
	}
}

// TestRowGapsRunOnTheIR runs TestRowGapsAgree's programs on the IR path
// (ergo run): each prints the model's lines.
//
// It builds Go because the question is the patched compiler's check of
// the IR's places (toolchain/ir, assignable), which no in-process check
// reads.
func TestRowGapsRunOnTheIR(t *testing.T) {
	needsBuild(t)
	for _, seed := range rowGapSeeds(t) {
		src, want, _ := rowGapProgram(seed)
		dir := t.TempDir()
		path := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "run", path)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if got := string(withoutBuildNote(out)); err != nil || got != want {
			t.Fatalf("seed %d (ERGO_ROWGAP_SEED=%d): the IR path (ergo run): %v\n%s\nwant\n%s\n%s", seed, seed, err, got, want, src)
		}
	}
}
