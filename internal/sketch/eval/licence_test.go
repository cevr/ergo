package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// licenceFixture is the functions the generated laws compose. Each is the
// identity on its argument except in one planted class: Long at 4,096
// bytes and more, Tail at a string that ends inside a rune, NotMin at the
// minimum int, NaNz at a NaN. Zone calls strings.ToLower, which the test
// makes the identity under the first ambient state and not under the
// second (a function of the time zone); Skip calls strings.ToUpper, which
// the test makes a skip; Spin spins at 4,096 bytes and more, past any
// budget. Id, IId and FId are the identity, Cat a's and b's
// concatenation, and Key a float's class (NaN, zero, other).
const licenceFixture = `import strings

func Id(s string) string { s }

func Long(s string) string { if len(s) >= 4096 { s + "!" } else { s } }

func Tail(s string) string { if len(s) >= 2 && s[len(s)-2] == 0xe4 { "" } else { s } }

func Zone(s string) string { strings.ToLower(s) }

func Skip(s string) string { strings.ToUpper(s) }

func Spin(s string) string {
	if len(s) >= 4096 {
		for _ in 0..1000000000000 {
		}
	}
	s
}

func Cat(a string, b string) string { a + b }

func IId(n int) int { n }

func NotMin(n int) int { if n == -9223372036854775807 - 1 { 0 } else { n } }

func FId(x float64) float64 { x }

func NaNz(x float64) float64 { if x != x { 0.0 } else { x } }

func Key(x float64) int { if x != x { 0 } else if x == 0.0 { 1 } else { 2 } }

`

// licenceLaw is a generated law and its reference model's verdict.
type licenceLaw struct {
	name, src string
	// typ is the parameters' type; fns the chain of functions applied to
	// the (first) parameter, innermost last; second the chain of the
	// second parameter of a Cat law.
	typ          string
	fns, second  []string
	cat          bool
	verdict, why string
}

// genLicenceLaw draws a law: a chain of one to three fixture functions of
// one type, the identity with a weight of one half, over one parameter;
// or Cat of two such chains over two strings.
func genLicenceLaw(r *rand.Rand, name string) licenceLaw {
	pools := map[string][]string{
		"string":  {"Long", "Tail", "Zone", "Skip", "Spin"},
		"int":     {"NotMin"},
		"float64": {"NaNz"},
	}
	ids := map[string]string{"string": "Id", "int": "IId", "float64": "FId"}
	types := []string{"string", "string", "int", "float64"}
	l := licenceLaw{name: name, typ: types[r.IntN(len(types))]}
	chain := func(pool []string) []string {
		var out []string
		for range 1 + r.IntN(3) {
			if r.IntN(2) == 0 {
				out = append(out, ids[l.typ])
			} else {
				out = append(out, pool[r.IntN(len(pool))])
			}
		}
		return out
	}
	apply := func(fns []string, x string) string {
		for i := len(fns) - 1; i >= 0; i-- {
			x = fns[i] + "(" + x + ")"
		}
		return x
	}
	switch {
	case l.typ == "string" && r.IntN(3) == 0:
		l.cat = true
		pool := []string{"Long", "Tail"}
		l.fns, l.second = chain(pool), chain(pool)
		l.src = fmt.Sprintf("law %s(a string, b string) { Cat(%s, %s) } == { a + b }", name, apply(l.fns, "a"), apply(l.second, "b"))
	case l.typ == "float64" && r.IntN(2) == 0:
		l.fns = chain(pools[l.typ])
		l.src = fmt.Sprintf("law %s(x float64) { Key(%s) } == { Key(x) }", name, apply(l.fns, "x"))
	case l.typ == "float64":
		// The sides are floats: the run compares them by key equality,
		// so FId(x) at a NaN equals x (Go's == would refute it).
		l.fns = chain(pools[l.typ])
		l.src = fmt.Sprintf("law %s(x float64) { %s } == { x }", name, apply(l.fns, "x"))
	default:
		l.fns = chain(pools[l.typ])
		l.src = fmt.Sprintf("law %s(x %s) { %s } == { x }", name, l.typ, apply(l.fns, "x"))
	}
	l.verdict, l.why = l.model()
	return l
}

// model is the reference verdict: the classes in the run's order (each
// class of the one parameter; each pair of classes of a Cat law's two),
// the chain applied by the fixture's meaning. A skip at the first case is
// skipped, a Zone at the first case refuted (not a function of its
// arguments), Spin at 4,096 bytes tested, a planted class refuted; past
// the classes, no drawn case reaches a planted class it has not met, so
// the law is validated.
func (l licenceLaw) model() (string, string) {
	all := append(slices.Clone(l.fns), l.second...)
	if slices.Contains(all, "Skip") {
		return "skipped", "check-time evaluation does not run"
	}
	if slices.Contains(all, "Zone") {
		return "refuted", "not a function of its arguments"
	}
	str := func(fns []string, s string) (string, string) {
		for i := len(fns) - 1; i >= 0; i-- {
			switch fns[i] {
			case "Long":
				if len(s) >= 4096 {
					s += "!"
				}
			case "Tail":
				if len(s) >= 2 && s[len(s)-2] == 0xe4 {
					s = ""
				}
			case "Spin":
				if len(s) >= 4096 {
					return s, "tested"
				}
			}
		}
		return s, ""
	}
	// The model's own statement of the classes that matter here, in the
	// spec's order (lengths before contents; runtime.md Tests 6), not
	// testgen's tables: a class dropped from a table is then a run the
	// model disagrees with.
	strs := []string{"", strings.Repeat("abcd", 1024), "a\xe4\xb8"}
	switch {
	case l.cat:
		for _, a := range strs {
			for _, b := range strs {
				x, _ := str(l.fns, a)
				y, _ := str(l.second, b)
				if x != a || y != b {
					return "refuted", "the law is false"
				}
			}
		}
	case l.typ == "string":
		for _, c := range strs {
			s, stop := str(l.fns, c)
			if stop != "" {
				return "tested", "string.len>=4096"
			}
			if s != c {
				return "refuted", "the law is false"
			}
		}
	case l.typ == "int" && slices.Contains(l.fns, "NotMin"):
		// The minimum int is a class.
		return "refuted", "the law is false"
	case l.typ == "float64" && slices.Contains(l.fns, "NaNz"):
		// A NaN is a class.
		return "refuted", "the law is false"
	}
	return "validated", ""
}

// TestLicenceRunFindsEdgeFalsehoods is the licence run (runtime.md, Tests
// 7; LAWFFI r2 §3.4), seeded and model-based: each seed generates laws
// that compose fixture functions, each the identity but in one planted
// class (4,096 bytes and more, a string that ends inside a rune, the
// minimum int, a NaN, another time zone), and the run's verdict is the
// reference model's: refuted for each false law, validated for each true
// one, skipped for one the evaluator skips, and tested, never validated,
// when the model says a class was not reached (Spin runs out of budget
// there), with that class missed. A drawn case alone (a law's run today)
// reaches no planted class but the minimum int: such laws print
// `ok … (100 cases)`.
//
// Mutants: lf6-skipped-run-licenses, lf6-missed-class-licenses,
// lf6-vacuous-case-counted, lf6-edges-after-draws-only-length,
// lf6-edges-after-draws-only-content, lf6-edges-after-draws-only-float,
// lf6-second-run-same-state, lf6-corpus-unreplayed (a refuted case given
// back is not the first case), lf6-licence-float-by-go-equality (a float
// law whose sides are equal at a NaN is refuted by Go's ==).
//
// ERGO_LICENCE_SEED=N replays one seed.
func TestLicenceRunFindsEdgeFalsehoods(t *testing.T) {
	lower, upper := loadedNatives().goLib["strings.ToLower"], loadedNatives().goLib["strings.ToUpper"]
	if lower == nil || upper == nil {
		t.Fatal("no native of strings.ToLower or strings.ToUpper")
	}
	loadedNatives().goLib["strings.ToLower"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if in.ambient.zone != ambients[0].zone {
			return args[0].(string) + "@", nil
		}
		return args[0], nil
	}
	loadedNatives().goLib["strings.ToUpper"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return nil, fr.unsupported(at, "the fixture's Skip")
	}
	t.Cleanup(func() {
		loadedNatives().goLib["strings.ToLower"], loadedNatives().goLib["strings.ToUpper"] = lower, upper
	})
	seeds := []uint64{}
	if s := os.Getenv("ERGO_LICENCE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(12) {
			seeds = append(seeds, i+1)
		}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6c6963))
		var laws []licenceLaw
		var b strings.Builder
		b.WriteString(licenceFixture)
		for i := range 8 {
			l := genLicenceLaw(r, fmt.Sprintf("L%d", i))
			laws = append(laws, l)
			b.WriteString(l.src + "\n\n")
		}
		src := b.String()
		pkg, diags := check.CheckSource(src)
		if pkg == nil || len(diags) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, diags, src)
		}
		byName := map[string]*check.Law{}
		for _, law := range pkg.Laws {
			byName[law.Name] = law
		}
		for _, l := range laws {
			law := byName[l.name]
			if law == nil || law.FuncRewrite == nil {
				t.Fatalf("seed %d: %s is no rewrite in scope\n%s", seed, l.name, src)
			}
			got, err := LicenceRun(pkg, law, Budget{Steps: 4_000_000, Depth: 1024, Seed: 1}, nil, nil)
			if err != nil {
				t.Fatalf("seed %d, %s: %v", seed, l.name, err)
			}
			key := l.verdict
			if l.verdict == "refuted" {
				key += ": " + l.why
			}
			reached[key] = true
			if l.typ == "float64" && l.verdict == "validated" && !strings.Contains(l.src, "Key(") {
				reached["validated float sides"] = true
			}
			fail := func(why string) {
				t.Errorf("seed %d, %s: %s\nreplay: ERGO_LICENCE_SEED=%d go test -run '^TestLicenceRunFindsEdgeFalsehoods$' ./internal/sketch/eval\nlaw: %s\nmodel: %s (%s)\nrun:   %s\nmissed: %v\n--- source\n%s", seed, l.name, why, seed, l.src, l.verdict, l.why, got.Line(), got.Missed, src)
			}
			switch {
			case got.Verdict != l.verdict:
				fail("the run's verdict is not the model's")
			case l.verdict == "refuted" && !strings.Contains(got.Reason, l.why):
				fail("the run refutes the law for another reason than the model's")
			case l.verdict == "skipped" && !strings.Contains(got.Reason, l.why):
				fail("the run skips the law for another reason than the model's")
			case l.verdict == "tested" && !slices.Contains(got.Missed, l.why):
				fail("the run does not name the class the model says it missed")
			case l.verdict == "validated" && len(got.Missed) > 0:
				fail("a validated run missed classes")
			}
			if got.Verdict == "refuted" && got.Case != "" {
				// The refuted case's corpus line, given back, is the
				// next run's first case, and refutes it there.
				again, err := LicenceRun(pkg, law, Budget{Steps: 4_000_000, Depth: 1024, Seed: 1}, []string{got.Case}, nil)
				if err != nil || again.Verdict != "refuted" || again.Cases != 0 {
					fail(fmt.Sprintf("the corpus line %s does not refute the law at the first case: %s, %v", got.Case, again.Line(), err))
				}
				reached["corpus replayed"] = true
			}
		}
	}
	if len(seeds) > 1 {
		for _, v := range []string{"validated", "validated float sides", "corpus replayed", "tested", "skipped", "refuted: the law is false", "refuted: not a function of its arguments"} {
			if !reached[v] {
				t.Errorf("no seed reaches the model's %s", v)
			}
		}
	}
}
