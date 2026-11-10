package check

import (
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestRefinementMatchesPredicate is the property of types.md, Refinements
// 2 and 6: for generated predicates of the decidable fragment, the spans
// the checker decides hold exactly the subject values for which a direct
// evaluation of the predicate's tree (the reference model) is true, for
// every int8 and for the lengths 0 to 40 of a string; an empty set is the
// one S108 "no value of … satisfies"; and a constant of the type checks
// exactly when its value is in the set. It kills the span mutants of
// refine_mutants in testdata/mutants.json (union, complement, clip, the
// flipped comparison, each comparison's bound). ERGO_SEED=<n> replays one
// seed; a failure prints the seed and the source.
func TestRefinementMatchesPredicate(t *testing.T) {
	seeds := make([]int64, 300)
	if testing.Short() {
		seeds = seeds[:60]
	}
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []int64{n}
	}
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		isLen := rng.Intn(3) == 0
		subject, base, lo, hi := "it", "int8", -128, 127
		if isLen {
			subject, base, lo, hi = "len(it)", "string", 0, 50
		}
		pred := genPredicate(rng, subject, isLen, 3)
		decl := "type T " + base + " where " + pred + "\n"
		// A constant of T: a value the predicate may or may not hold.
		probe := lo + rng.Intn(hi-lo+1)
		lit := strconv.Itoa(probe)
		if isLen {
			lit = strconv.Quote(strings.Repeat("a", probe))
		}
		src := decl + "func f() T { " + lit + " }\n"
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestRefinementMatchesPredicate ./internal/sketch/check): %s\n%s", seed, seed, fmt.Sprintf(format, args...), src)
		}
		file, err := syntax.Parse(src)
		if err != nil {
			fail("parse: %v", err)
		}
		where := file.Decls[0].(*syntax.TypeDecl).Where
		holds := map[int]bool{}
		some := false
		for v := lo; v <= hi; v++ {
			holds[v] = evalPredicate(where, v, isLen)
			some = some || holds[v]
		}
		pkg, ds := CheckSource(src)
		if !some {
			if len(ds) == 0 || !strings.Contains(ds[0].Message, "satisfies this where") {
				fail("an empty predicate: want S108 no value satisfies, got %s", describeDiags(ds))
			}
			continue
		}
		wantConst := holds[probe]
		if wantConst != (len(ds) == 0) {
			fail("the constant %s: want accepted %v, got %s", lit, wantConst, describeDiags(ds))
		}
		obj, _ := pkg.Scope.lookup("T")
		r := obj.(*TypeName).Refine
		if r == nil {
			fail("no refinement")
		}
		for v := lo; v <= hi; v++ {
			if got := r.Holds(big.NewInt(int64(v))); got != holds[v] {
				fail("value %d: spans %v say %v, the predicate says %v", v, r.Spans, got, holds[v])
			}
		}
		if isLen {
			top := big.NewInt(1 << 62)
			if got, want := r.Holds(top), evalPredicate(where, 1<<62, true); got != want {
				fail("length 2^62: spans say %v, the predicate says %v", got, want)
			}
		}
	}
}

// genPredicate is a predicate of the fragment over subject, of depth d.
func genPredicate(rng *rand.Rand, subject string, isLen bool, d int) string {
	if d <= 0 || rng.Intn(3) == 0 {
		switch rng.Intn(12) {
		case 0:
			return []string{"true", "false"}[rng.Intn(2)]
		}
		ops := []string{"==", "!=", "<", "<=", ">", ">="}
		op := ops[rng.Intn(len(ops))]
		c := rng.Intn(256) - 128
		if isLen {
			c = rng.Intn(46) - 3
		}
		lit := strconv.Itoa(c)
		if rng.Intn(2) == 0 {
			return subject + " " + op + " " + lit
		}
		return lit + " " + op + " " + subject
	}
	switch rng.Intn(4) {
	case 0:
		return "!(" + genPredicate(rng, subject, isLen, d-1) + ")"
	case 1:
		return "(" + genPredicate(rng, subject, isLen, d-1) + ")"
	case 2:
		return genPredicate(rng, subject, isLen, d-1) + " && " + genPredicate(rng, subject, isLen, d-1)
	}
	return genPredicate(rng, subject, isLen, d-1) + " || " + genPredicate(rng, subject, isLen, d-1)
}

// evalPredicate is the reference model: the predicate's tree evaluated
// at the subject value v.
func evalPredicate(x syntax.Expr, v int, isLen bool) bool {
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return evalPredicate(x.X, v, isLen)
	case *syntax.Ident:
		return x.Name == "true"
	case *syntax.UnaryExpr:
		return !evalPredicate(x.X, v, isLen)
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.LAND:
			return evalPredicate(x.X, v, isLen) && evalPredicate(x.Y, v, isLen)
		case syntax.LOR:
			return evalPredicate(x.X, v, isLen) || evalPredicate(x.Y, v, isLen)
		}
		a, b := evalOperand(x.X, v), evalOperand(x.Y, v)
		switch x.Op {
		case syntax.EQL:
			return a == b
		case syntax.NEQ:
			return a != b
		case syntax.LSS:
			return a < b
		case syntax.LEQ:
			return a <= b
		case syntax.GTR:
			return a > b
		case syntax.GEQ:
			return a >= b
		}
	}
	panic(fmt.Sprintf("evalPredicate: %T", x))
}

func evalOperand(x syntax.Expr, v int) int {
	switch x := x.(type) {
	case *syntax.Ident, *syntax.CallExpr:
		return v
	case *syntax.UnaryExpr:
		return -evalOperand(x.X, v)
	case *syntax.BasicLit:
		n, err := strconv.Atoi(x.Value)
		if err != nil {
			panic(err)
		}
		return n
	}
	panic(fmt.Sprintf("evalOperand: %T", x))
}
