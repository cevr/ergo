package syntax

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestTryAddsNoGrouping: q105. A try may start any operand and adds no
// grouping: the parse tree of a generated expression with try before a
// random set of its operands is, with each TryExpr replaced by its
// operand, the tree of the same text without any try. The expressions mix
// every binary precedence, unary operators, calls with argument lists,
// indexes, selectors and parentheses. ERGO_TRY_TREE_SEED=<seed> replays
// one seed; a failure prints the source and both trees.
func TestTryAddsNoGrouping(t *testing.T) {
	seeds := 400
	first := int64(1)
	if s := os.Getenv("ERGO_TRY_TREE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	for seed := first; seed < first+int64(seeds); seed++ {
		g := &treeGen{rng: rand.New(rand.NewSource(seed))}
		e := g.expr(3)
		with, without := g.text(e, true), g.text(e, false)
		a, errA := Parse("func h() {\n\tlet r = " + with + "\n}\n")
		b, errB := Parse("func h() {\n\tlet r = " + without + "\n}\n")
		if errA != nil || errB != nil {
			t.Fatalf("seed %d: %v / %v\n%s\nreplay: ERGO_TRY_TREE_SEED=%d go test -run TestTryAddsNoGrouping ./internal/sketch/syntax", seed, errA, errB, with, seed)
		}
		if sa, sb := letShape(a), letShape(b); sa != sb {
			t.Fatalf("seed %d: the try changed the tree\nwith try:    %s\n  %s\nwithout try: %s\n  %s\nreplay: ERGO_TRY_TREE_SEED=%d go test -run TestTryAddsNoGrouping ./internal/sketch/syntax", seed, with, sa, without, sb, seed)
		}
	}
}

// texpr is a generated expression: an operand (text with a slot for its
// parts) or a binary expression.
type texpr struct {
	op   string // a binary operator, or "" for an operand
	x, y *texpr
	// an operand: its text around its parts
	pre, sep, post string
	parts          []*texpr
	try            bool
}

type treeGen struct{ rng *rand.Rand }

var treeOps = []string{"||", "&&", "==", "<", "+", "-", "|", "*", "/", "%", "<<", "&"}

func (g *treeGen) expr(depth int) *texpr {
	if depth <= 0 || g.rng.Intn(3) == 0 {
		return g.operand(depth)
	}
	return &texpr{op: treeOps[g.rng.Intn(len(treeOps))], x: g.expr(depth - 1), y: g.expr(depth - 1)}
}

func (g *treeGen) operand(depth int) *texpr {
	n := &texpr{try: g.rng.Intn(3) == 0}
	switch r := g.rng.Intn(8); {
	case r < 2 || depth <= 0:
		n.pre = []string{"a", "b.c", "7", "\"s\"", "true"}[g.rng.Intn(5)]
	case r < 3:
		n.pre = []string{"-", "!", "^", "*"}[g.rng.Intn(4)]
		n.parts = []*texpr{g.operand(depth - 1)}
	case r < 4:
		n.pre, n.post = "(", ")"
		n.parts = []*texpr{g.expr(depth - 1)}
	case r < 5:
		n.pre, n.post = "xs[", "]"
		n.parts = []*texpr{g.expr(depth - 1)}
	case r < 6:
		n.pre, n.post = "f(", ").g"
		n.parts = []*texpr{g.expr(depth - 1)}
	default:
		n.pre, n.sep, n.post = "k(", ", ", ")"
		for i := g.rng.Intn(3); i > 0; i-- {
			n.parts = append(n.parts, g.expr(depth-1))
		}
	}
	return n
}

// text writes e; a binary operand of a looser or equal operator than its
// parent's is in parentheses written as an operand, so the tree follows
// e's shape.
func (g *treeGen) text(e *texpr, withTry bool) string {
	var b strings.Builder
	g.write(&b, e, withTry)
	return b.String()
}

func treePrec(op string) int {
	return map[string]int{"||": 1, "&&": 2, "==": 3, "<": 3, "+": 4, "-": 4, "|": 4, "*": 5, "/": 5, "%": 5, "<<": 5, "&": 5}[op]
}

func (g *treeGen) write(b *strings.Builder, e *texpr, withTry bool) {
	if e.op != "" {
		g.side(b, e.x, treePrec(e.op), false, withTry)
		b.WriteString(" " + e.op + " ")
		g.side(b, e.y, treePrec(e.op), true, withTry)
		return
	}
	if e.try && withTry {
		b.WriteString("try ")
	}
	b.WriteString(e.pre)
	for i, p := range e.parts {
		if i > 0 {
			b.WriteString(e.sep)
		}
		g.write(b, p, withTry)
	}
	b.WriteString(e.post)
}

func (g *treeGen) side(b *strings.Builder, e *texpr, prec int, right bool, withTry bool) {
	if e.op != "" && (treePrec(e.op) < prec || right && treePrec(e.op) == prec) {
		b.WriteString("(")
		g.write(b, e, withTry)
		b.WriteString(")")
		return
	}
	g.write(b, e, withTry)
}

// letShape prints the tree of the let's value without positions, each
// TryExpr replaced by its operand.
func letShape(f *File) string {
	spec := f.Decls[0].(*FuncDecl).Body.List[0].(*DeclStmt).Decl.Specs[0]
	var b strings.Builder
	shapeOf(&b, reflect.ValueOf(spec.Values))
	return b.String()
}

var posType = reflect.TypeOf(Pos(0))

func shapeOf(b *strings.Builder, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		if tr, ok := v.Interface().(*TryExpr); ok {
			shapeOf(b, reflect.ValueOf(tr.X))
			return
		}
		shapeOf(b, v.Elem())
	case reflect.Struct:
		b.WriteString(v.Type().Name() + "{")
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				shapeOf(b, v.Field(i))
				b.WriteString(" ")
			}
		}
		b.WriteString("}")
	case reflect.Slice:
		b.WriteString("[")
		for i := 0; i < v.Len(); i++ {
			shapeOf(b, v.Index(i))
			b.WriteString(" ")
		}
		b.WriteString("]")
	default:
		if v.Type() != posType {
			fmt.Fprint(b, v.Interface())
		}
	}
}
