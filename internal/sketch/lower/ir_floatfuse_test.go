package lower

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestIRFloatProductsRound is TestFloatProductsRound on the IR path: in
// the IR file of the generated programs of floatFuseProgram, whose every
// product is of floats, each product ("bin" "*") is the operand of a
// "conv" to a float type, and no statement keeps Go's x *= y. The promised
// form is the spec here: gc fuses a product into a sum (one multiply-add,
// rounding once) at GOAMD64=v3 and above and on arm64, across statements
// too (cmd/compile ssa rules AMD64 "Detect FMA", ARM64 FMADDD), and only an
// explicit conversion stops it (the Go spec, Arithmetic operators). So the
// form is the one fact that keeps a float result, and a law verdict over
// floats, the same at every CPU level (types.md, Numeric types; U3
// CPU-LEVEL). Every generated function must lower on the IR, so no seed
// passes by falling back to the text body.
//
// Mutants (lower ledger, ir-float-*): the IR binaryOf's product left bare;
// the branch-operand product (ir_expr.go value) left bare; x *= y of
// floats left as the set's Op.
//
// Replay: go test -run '^TestIRFloatProductsRound$' ./internal/sketch/lower -args -floatfuse.seed=N
func TestIRFloatProductsRound(t *testing.T) {
	if floatLowering == "fuse" {
		t.Skip("floatLowering=fuse is the reference that leaves products bare")
	}
	seeds := []uint64{1, 2, 3, 4}
	if *floatFuseSeed != 0 {
		seeds = []uint64{*floatFuseSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, products := floatFuseProgram(rand.New(rand.NewPCG(seed, 0xf5)), reached)
		src += "func main() {}\n" // LowerIR lowers a program
		replay := fmt.Sprintf("go test -run '^TestIRFloatProductsRound$' ./internal/sketch/lower -args -floatfuse.seed=%d", seed)
		o := lowerSource(fmt.Sprintf("irfloatfuse_%d", seed), src, true)
		if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
			t.Fatalf("seed %d: %v %v %v %v\n%s", seed, o.err, o.panicked, o.diags, o.refused, src)
		}
		u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		files, rep, err := LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		// An array literal has no IR form yet: its function keeps the text
		// body, which TestFloatProductsRound covers, and its products are
		// not the IR's to round. Any other fallback fails the seed.
		for reason := range rep.Fallback {
			if reason != "literal of *check.Array" {
				t.Errorf("seed %d: a body falls back for %q\nErgo:\n%s\nreplay: %s", seed, reason, src, replay)
			}
		}
		for i := range 12 {
			if !slices.Contains(rep.IR, fmt.Sprintf("f%d", i)) {
				products -= textProducts(src, fmt.Sprintf("f%d", i))
			}
		}
		var doc any
		for _, f := range files {
			if f.Path == IRFileName {
				if err := json.Unmarshal(f.Data, &doc); err != nil {
					t.Fatal(err)
				}
			}
		}
		if doc == nil {
			t.Fatalf("seed %d: no IR file", seed)
		}
		rounded, bare, assigns := 0, 0, []string{}
		var walk func(v any, underConv string)
		walk = func(v any, underConv string) {
			switch v := v.(type) {
			case map[string]any:
				k, _ := v["k"].(string)
				op, _ := v["op"].(string)
				if k == "bin" && op == "*" {
					if strings.HasPrefix(underConv, "float") || underConv == "Meters" || strings.HasSuffix(underConv, ".Meters") {
						rounded++
					} else {
						bare++
					}
				} else if op == "*" {
					assigns = append(assigns, k)
				}
				conv := ""
				if k == "conv" {
					conv, _ = v["t"].(string)
				}
				for key, x := range v {
					if key == "x" && conv != "" {
						walk(x, conv)
					} else {
						walk(x, "")
					}
				}
			case []any:
				for _, x := range v {
					walk(x, "")
				}
			}
		}
		walk(doc, "")
		if bare > 0 || len(assigns) > 0 {
			t.Errorf("seed %d: %d bare products of floats a CPU level may fuse; op-assignments of *: %v\nErgo:\n%s\nreplay: %s", seed, bare, assigns, src, replay)
		}
		if rounded < products {
			t.Errorf("seed %d: %d rounded products for %d in the source\nErgo:\n%s\nreplay: %s", seed, rounded, products, src, replay)
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

// textProducts counts the products of floats floatFuseProgram wrote in
// its function name: each " * " and "*=" but the constant 2.0 * 1.5.
func textProducts(src, name string) int {
	_, body, ok := strings.Cut(src, "\nfunc "+name+"(")
	if !ok {
		return 0
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	return strings.Count(body, " * ") + strings.Count(body, "*=") - strings.Count(body, "2.0 * 1.5")
}
