package lower

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"math/rand/v2"
	"strings"
	"testing"
)

// The lowering of a float product (floatLowering, expr.go): Go may fuse a
// product and a sum into one multiply-add on some hosts, so the lowering
// writes every product of floats under an explicit conversion, which
// rounds it (types.md, Numeric types; corpus-wave Q5).
//
// Replay: go test -run '^TestFloatProductsRound$' ./internal/sketch/lower -args -floatfuse.seed=N

var floatFuseSeed = flag.Uint64("floatfuse.seed", 0, "run TestFloatProductsRound on this seed alone")

// floatFuseForms are the places a product of floats is written that the
// fixed seeds must reach: a product in an expression, in a let, an
// argument, a return, and x *= y of a local, of a record field through a
// pointer, of a local record's field, of an array element and of a slice
// element.
var floatFuseForms = []string{"expr", "let", "arg", "mul-assign local", "mul-assign pointer field", "mul-assign record field", "mul-assign array", "mul-assign slice"}

// floatFuseTypes are the float types a seed draws: both widths and a type
// defined over float64.
var floatFuseTypes = []string{"float64", "float32", "Meters"}

// floatFuseProgram is a generated program of float products in every form
// of floatFuseForms over the types of floatFuseTypes, mixed with sums,
// differences, quotients and constant products, and the number of products
// of floats it writes that are not constants.
func floatFuseProgram(r *rand.Rand, reached map[string]bool) (string, int) {
	var b strings.Builder
	b.WriteString("type Meters float64\n\n")
	for _, t := range floatFuseTypes {
		fmt.Fprintf(&b, "type Rec%s { X %s, Y %s }\n\n", t, t, t)
		fmt.Fprintf(&b, "func id%s(v %s) %s { v }\n\n", t, t, t)
	}
	products := 0
	// expr is an expression of type t over a, b and c, depth at most d.
	var expr func(t string, d int) string
	expr = func(t string, d int) string {
		if d == 0 || r.IntN(3) == 0 {
			switch r.IntN(4) {
			case 0:
				return "a"
			case 1:
				return "b"
			case 2:
				return "c"
			default:
				return fmt.Sprintf("%s(%d.5)", t, r.IntN(9))
			}
		}
		switch op := []string{"+", "-", "*", "/", "const"}[r.IntN(5)]; op {
		case "const":
			// A constant product: Go folds it exactly, so it needs no
			// conversion and counts for nothing.
			return fmt.Sprintf("(%s(2.0 * 1.5) + %s)", t, expr(t, d-1))
		case "*":
			products++
			return "(" + expr(t, d-1) + " * " + expr(t, d-1) + ")"
		default:
			return "(" + expr(t, d-1) + " " + op + " " + expr(t, d-1) + ")"
		}
	}
	for i := range 12 {
		t := floatFuseTypes[r.IntN(len(floatFuseTypes))]
		form := floatFuseForms[r.IntN(len(floatFuseForms))]
		reached[form] = true
		rec := "Rec" + t
		fmt.Fprintf(&b, "func f%d(a %s, b %s, c %s, p *%s) %s {\n", i, t, t, t, rec, t)
		// prod is a product of floats the form writes, beside expr's.
		prod := func() string {
			products++
			return "a * " + expr(t, 2)
		}
		switch form {
		case "expr":
			fmt.Fprintf(&b, "\t%s + c\n", prod())
		case "let":
			fmt.Fprintf(&b, "\tlet v = %s\n\tv + %s\n", prod(), expr(t, 2))
		case "arg":
			fmt.Fprintf(&b, "\tid%s(%s) - c\n", t, prod())
		case "mul-assign local":
			products++
			fmt.Fprintf(&b, "\tlet mut v = %s\n\tv *= %s\n\tv + c\n", expr(t, 2), expr(t, 2))
		case "mul-assign pointer field":
			products++
			fmt.Fprintf(&b, "\tp.X *= %s\n\tp.X + c\n", expr(t, 2))
		case "mul-assign record field":
			products++
			fmt.Fprintf(&b, "\tlet mut q = %s{X: a, Y: b}\n\tq.Y *= %s\n\tq.Y + c\n", rec, expr(t, 2))
		case "mul-assign array":
			products++
			fmt.Fprintf(&b, "\tlet mut q = [2]%s{a, b}\n\tq[%d] *= %s\n\tq[0] + q[1] + c\n", t, r.IntN(2), expr(t, 2))
		case "mul-assign slice":
			products++
			fmt.Fprintf(&b, "\tlet mut q = []%s{a, b}\n\tq[%d] *= %s\n\tq[0] + q[1] + c\n", t, r.IntN(2), expr(t, 2))
		}
		b.WriteString("}\n\n")
	}
	return b.String(), products
}

// TestFloatProductsRound is the seeded property of floatLowering: in the
// lowered Go of generated programs, go/types finds every product of floats
// that is not a constant as the operand of an explicit conversion to its
// own type, and no x *= y of floats, so no host may fuse a product into a
// sum (the Go spec, Arithmetic operators). The promised form is the spec
// here: it is the one fact that makes a float result the same on every
// host. It counts the rounded products against the program's (the inliner
// may copy a product, so at least as many), so none lowers to a form the
// walk does not see, and the program type-checks.
//
// Mutants (lower ledger, float-*): the product of binaryOf left bare; x *=
// y of floats left as Go's *=; a type defined over a float not seen as a
// float; float32 not seen as a float.
func TestFloatProductsRound(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4}
	if *floatFuseSeed != 0 {
		seeds = []uint64{*floatFuseSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	want := map[string]int{}
	for _, seed := range seeds {
		src, n := floatFuseProgram(rand.New(rand.NewPCG(seed, 0xf5)), reached)
		name := fmt.Sprintf("floatfuse_%d", seed)
		o := lowerSource(name, src, false)
		if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
			t.Fatalf("seed %d: %v %v %v %v\n%s", seed, o.err, o.panicked, o.diags, o.refused, src)
		}
		programs = append(programs, o)
		want[name] = n
	}
	infos := map[string]*types.Info{}
	files := map[string]*ast.File{}
	failed, err := typecheckFiles(t, programs, "arm64", infos, files)
	if err != nil {
		t.Fatal(err)
	}
	isFloat := func(tv types.TypeAndValue) bool {
		b, ok := tv.Type.Underlying().(*types.Basic)
		return ok && b.Info()&types.IsFloat != 0
	}
	for _, o := range programs {
		replay := "go test -run '^TestFloatProductsRound$' ./internal/sketch/lower -args -floatfuse.seed=" + strings.TrimPrefix(o.name, "floatfuse_")
		if errs := failed[o.name]; len(errs) > 0 {
			t.Errorf("%s does not type-check:\n%s\nErgo:\n%s\nGo:\n%s\nreplay: %s", o.name, strings.Join(errs, "\n"), o.src, o.mainGo(), replay)
			continue
		}
		info := infos[o.name]
		rounded := map[ast.Expr]bool{}
		for x, tv := range info.Types {
			call, ok := x.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 || !info.Types[call.Fun].IsType() {
				continue
			}
			arg := call.Args[0]
			for p, ok := arg.(*ast.ParenExpr); ok; p, ok = arg.(*ast.ParenExpr) {
				arg = p.X
			}
			if types.Identical(info.Types[arg].Type, tv.Type) {
				rounded[arg] = true
			}
		}
		var bare []string
		count := 0
		for x, tv := range info.Types {
			if bin, ok := x.(*ast.BinaryExpr); ok && bin.Op == token.MUL && isFloat(tv) && tv.Value == nil {
				if rounded[bin] {
					count++
				} else {
					bare = append(bare, types.ExprString(bin))
				}
			}
		}
		var assigns []string
		ast.Inspect(files[o.name], func(n ast.Node) bool {
			if a, ok := n.(*ast.AssignStmt); ok && a.Tok == token.MUL_ASSIGN && isFloat(info.Types[a.Lhs[0]]) {
				assigns = append(assigns, types.ExprString(a.Lhs[0])+" *= "+types.ExprString(a.Rhs[0]))
			}
			return true
		})
		if len(bare) > 0 || len(assigns) > 0 {
			t.Errorf("%s: products of floats a host may fuse: %v; x *= y of floats: %v\nErgo:\n%s\nGo:\n%s\nreplay: %s", o.name, bare, assigns, o.src, o.mainGo(), replay)
		}
		if count < want[o.name] {
			t.Errorf("%s: %d rounded products for %d in the source\nErgo:\n%s\nGo:\n%s\nreplay: %s", o.name, count, want[o.name], o.src, o.mainGo(), replay)
		}
	}
	if len(seeds) > 1 {
		for _, form := range floatFuseForms {
			if !reached[form] {
				t.Errorf("the fixed seeds never reach %q", form)
			}
		}
	}
}
