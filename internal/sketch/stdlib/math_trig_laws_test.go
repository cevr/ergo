package stdlib_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdMathTrigLaws is the statement of each trig law of std math (SQ1), by
// ledger key.
var stdMathTrigLaws = map[string]string{
	"math AcosInRange":              `AcosInRange(x float64) { IsNaN(Acos(x)) || Acos(x) >= 0.0 && Acos(x) <= 3.141592653589793 }`,
	"math AsinIsOdd":                `AsinIsOdd(x float64) { IsNaN(Asin(x)) || Asin(-x) == -Asin(x) }`,
	"math Atan2IsAtanOnTheRight":    `Atan2IsAtanOnTheRight(y float64, x float64) { IsNaN(y) || IsInf(y, 0) || IsNaN(x) || IsInf(x, 0) || x <= 0.0 || Atan2(y, x) == Atan(y/x) }`,
	"math AtanIsMonotonic":          `AtanIsMonotonic(a float64, b float64) { IsNaN(a) || IsNaN(b) || a > b || Atan(a) <= Atan(b) }`,
	"math AtanIsOddAndBounded":      `AtanIsOddAndBounded(x float64) { IsNaN(x) || Atan(-x) == -Atan(x) && Atan(x) >= -1.5707963267948966 && Atan(x) <= 1.5707963267948966 }`,
	"math CbrtIsOdd":                `CbrtIsOdd(x float64) { IsNaN(x) || Cbrt(-x) == -Cbrt(x) }`,
	"math CbrtOfCube":               `CbrtOfCube(k int) { let x = float64(k % 100000) Cbrt(x * x * x) == x }`,
	"math CosIsEven":                `CosIsEven(x float64) { IsNaN(Cos(x)) || Cos(-x) == Cos(x) }`,
	"math HypotIgnoresSignAndOrder": `HypotIgnoresSignAndOrder(p float64, q float64) { IsNaN(Hypot(p, q)) || Hypot(-q, p) == Hypot(p, q) && Hypot(p, -q) == Hypot(p, q) }`,
	"math Log10IsMonotonic":         `Log10IsMonotonic(a float64, b float64) { IsNaN(a) || IsNaN(b) || a > b || Log10(a) <= Log10(b) || a < 0.0 }`,
	"math Log2OfPowerOfTwo":         `Log2OfPowerOfTwo(k int) { let e = float64(k % 1023) Log2(Pow(2.0, e)) == e }`,
	"math SinIsOdd":                 `SinIsOdd(x float64) { IsNaN(Sin(x)) || Sin(-x) == -Sin(x) }`,
	"math TanIsOdd":                 `TanIsOdd(x float64) { IsNaN(Tan(x)) || Tan(-x) == -Tan(x) }`,
}

// stdMathNames are the std math functions the trig laws call, which the
// probe qualifies.
var stdMathNames = regexp.MustCompile(`(^|[^.\w])(Sin|Cos|Tan|Asin|Acos|Atan2|Atan|Hypot|Cbrt|Log2|Log10|IsNaN|IsInf|Pow)\(`)

// TestStdMathTrigLaws runs the trig laws of std math in process: a probe
// program that imports std math restates each law with the package's
// names qualified (its law `L` of each ledger key), and the evaluator runs
// the probe's laws over Go's own math functions (eval's natives, the
// functions a build calls); lawledger.Run holds that this runner restates
// exactly the ledger's rows that name it and that each passed.
//
// Mutants only it kills (internal/sketch/stdlib/testdata/mutants.json and
// internal/sketch/eval/testdata/mutants.json): sq1-sin-is-cos,
// sq1-atan2-swapped, sq1-hypot-one-side, sq1-cbrt-is-sqrt,
// sq1-log2-is-log, sq1-acos-is-asin, sq1-eval-cbrt-is-sqrt.
func TestStdMathTrigLaws(t *testing.T) {
	keys := make([]string, 0, len(stdMathTrigLaws))
	for key := range stdMathTrigLaws {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sources, ok := stdlib.Package("math")
	if !ok {
		t.Fatal("no std package math")
	}
	var b strings.Builder
	b.WriteString("import math\n")
	for _, key := range keys {
		// The law's source text, with its lines (a let needs its own).
		law := lawSource(t, sources, key[len("math "):])
		if !lawledger.Same(law, stdMathTrigLaws[key]) {
			t.Errorf("%s: the source law %q is not the restated %q", key, law, stdMathTrigLaws[key])
		}
		// One pass misses a call right after another's "(": repeat.
		for next := ""; next != law; {
			next, law = law, stdMathNames.ReplaceAllString(law, "${1}math.${2}(")
		}
		b.WriteString("\nlaw " + law + "\n")
	}
	f, err := syntax.Parse(b.String())
	if err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v\n%s", diags, b.String())
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results[r.Name] = r
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range stdMathTrigLaws {
		name := "law " + key[len("math "):]
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			r, ok := results[name]
			if !ok {
				t.Fatalf("the evaluator ran no %s", name)
			}
			if r.Status != eval.Passed || r.Cases == 0 {
				t.Errorf("%s: %s after %d cases: %s", name, r.Status, r.Cases, r.Reason)
			}
		}}
	}
	lawledger.Run(t, laws)
}

// lawSource is the text of the law name in one of the files of sources,
// from its name to its closing brace.
func lawSource(t *testing.T, sources map[string]string, name string) string {
	t.Helper()
	for _, src := range sources {
		at := strings.Index(src, "\nlaw "+name+"(")
		if at < 0 {
			continue
		}
		at += len("\nlaw ")
		depth := 0
		for i := strings.IndexByte(src[at:], '{') + at; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return src[at : i+1]
				}
			}
		}
	}
	t.Fatalf("std math has no law %s", name)
	return ""
}
