package lower

import (
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The lowering of a float to integer conversion (types.md, Conversions 4).
// runtime/v1 TestFloatToIntegerMatchesModel holds each FloatTo… function to
// the rule on exact numbers; these tests hold the lowering to calling them.
//
// Replay: go test -run '^TestFloatConversionIsPortable$' ./internal/sketch/lower -args -floatconv.seed=N

var floatConvSeed = flag.Uint64("floatconv.seed", 0, "run TestFloatConversionIsPortable on this seed alone")

// floatConvTargets are the integer types a float converts to, byte and
// rune included.
var floatConvTargets = []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune"}

// floatConvProgram is a generated program of conversions of a float64 and
// a float32 to every integer type (each type once, in a shape the seed
// picks: a function's result, a let, an argument, a nested conversion, or
// beside a constant), and the number of
// conversions of a float to an integer it holds.
func floatConvProgram(r *rand.Rand) (string, int) {
	var b strings.Builder
	n := 0
	for i, t := range floatConvTargets {
		src := []string{"f", "g", "f * 2.0", "-f", "f + float64(g)"}[r.IntN(5)]
		fmt.Fprintf(&b, "func use%d(x %s) %s { x }\n\n", i, t, t)
		fmt.Fprintf(&b, "func c%d(f float64, g float32) %s {\n", i, t)
		switch r.IntN(5) {
		case 0:
			fmt.Fprintf(&b, "\t%s(%s)\n", t, src)
			n++
		case 1:
			fmt.Fprintf(&b, "\tlet v %s = %s(%s)\n\tv\n", t, t, src)
			n++
		case 2:
			fmt.Fprintf(&b, "\tuse%d(%s(%s))\n", i, t, src)
			n++
		case 3:
			// A float of a converted float: two conversions to integers.
			fmt.Fprintf(&b, "\t%s(float64(int32(%s)) + 0.5)\n", t, src)
			n += 2
		default:
			// A constant converts at check time, exactly (Conversions 1):
			// the lowering writes its value, so it is no call.
			fmt.Fprintf(&b, "\t%s(2.0) + %s(g)\n", t, t)
			n++
		}
		b.WriteString("}\n\n")
	}
	return b.String(), n
}

// TestFloatConversionIsPortable is the seeded property that the lowered Go
// of every conversion of a float to an integer type is the same on every
// host: go/types, run with the sizes of linux/amd64 and of linux/arm64,
// finds in it no Go conversion of a float operand to an integer type,
// whose result Go leaves to the implementation, and the program
// type-checks on both, so each FloatTo… call has the target's type. The
// lowering reads no target architecture, so the text is one for both. It
// counts the FloatTo… calls against the program's conversions, so none
// lowers to a form the walk does not see.
//
// Mutants (lower ledger, sconv-*): floatToInteger never names a helper
// (the plain Go conversion); a target kind mapped to another width's
// helper; a float32 not widened to float64.
func TestFloatConversionIsPortable(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *floatConvSeed != 0 {
		seeds = []uint64{*floatConvSeed}
	}
	var programs []*lowered
	want := map[string]int{}
	for _, seed := range seeds {
		src, n := floatConvProgram(rand.New(rand.NewPCG(seed, 0xf1)))
		name := fmt.Sprintf("floatconv_%d", seed)
		o := lowerSource(name, src, false)
		if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
			t.Fatalf("seed %d: %v %v %v %v\n%s", seed, o.err, o.panicked, o.diags, o.refused, src)
		}
		programs = append(programs, o)
		want[name] = n
	}
	for _, arch := range []string{"amd64", "arm64"} {
		infos := map[string]*types.Info{}
		failed, err := typecheckArch(t, programs, arch, infos)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range programs {
			replay := "go test -run '^TestFloatConversionIsPortable$' ./internal/sketch/lower -args -floatconv.seed=" + strings.TrimPrefix(o.name, "floatconv_")
			if errs := failed[o.name]; len(errs) > 0 {
				t.Errorf("%s: %s does not type-check:\n%s\nErgo:\n%s\nGo:\n%s\nreplay: %s", arch, o.name, strings.Join(errs, "\n"), o.src, o.mainGo(), replay)
				continue
			}
			info := infos[o.name]
			var raw []string
			calls := 0
			for x := range info.Types {
				call, ok := x.(*ast.CallExpr)
				if !ok {
					continue
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "FloatTo") {
					calls++
				}
				if fun, ok := info.Types[call.Fun]; !ok || !fun.IsType() || len(call.Args) != 1 {
					continue
				}
				to, _ := info.Types[call.Fun].Type.Underlying().(*types.Basic)
				from, _ := info.Types[call.Args[0]].Type.Underlying().(*types.Basic)
				if to != nil && from != nil && to.Info()&types.IsInteger != 0 && from.Info()&types.IsFloat != 0 {
					raw = append(raw, types.ExprString(call))
				}
			}
			sort.Strings(raw)
			if len(raw) > 0 {
				t.Errorf("%s: %s converts a float to an integer with Go's conversion, which the host decides out of range: %v\nErgo:\n%s\nGo:\n%s\nreplay: %s", arch, o.name, raw, o.src, o.mainGo(), replay)
			}
			if calls != want[o.name] {
				t.Errorf("%s: %s has %d FloatTo calls for %d conversions\nErgo:\n%s\nGo:\n%s\nreplay: %s", arch, o.name, calls, want[o.name], o.src, o.mainGo(), replay)
			}
		}
	}
}

// TestFloatConversionInlines is the speed guard of the conversion: the
// FloatTo… call inlines into the lowered function, so the conversion
// costs Go's conversion and two compares, and the function that holds it
// stays within Go's inlining budget. It builds Go because the subject is
// gc's inlining decision (-gcflags=-m), which only gc makes.
//
// Mutant (runtime/v1 ledger, sconv-int64-noinline): FloatToInt64 marked
// //go:noinline.
func TestFloatConversionInlines(t *testing.T) {
	needsBuild(t)
	const src = `func Wait(seconds float64) int64 { int64(seconds * 1000000000.0) }

func Byte(f float32) byte { byte(f) }
`
	o := lowerSource("floatconv_inline", src, false)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("%v %v %v %v", o.err, o.panicked, o.diags, o.refused)
	}
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/inline/main.go", []byte(o.mainGo()))
	cmd := exec.Command("go", "build", "-trimpath", "-gcflags=-m", "-o", os.DevNull, "./p/inline")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, helper := range []string{"FloatToInt64", "FloatToUint8"} {
		if !strings.Contains(string(out), "inlining call to v1."+helper) {
			t.Errorf("the call of %s is not inlined:\n%s\nGo:\n%s", helper, firstLines(string(out), 30), o.mainGo())
		}
	}
}
