package lower

import (
	"regexp"
	"testing"
)

// TestConstLetsStayValues holds the constant-let lowering (constLet, wf10
// 1BRC) to its promise and its guard. The promise, the one fact the speed
// lowering gives: a package let of an integer, bool or string type with a
// constant initializer is a Go constant, and a float let stays a variable.
// The guard: each read is v1.Let(name), no Go constant expression, so the
// expressions an Ergo program may write over such a let and that Go
// refuses over a constant (an int8 sum that wraps, a division by a zero
// let, a conversion out of range, a negative let as an unsigned) still
// type-check (typecheckGo), as they do over a variable.
//
// Mutants (lower ledger): const-let-bare-read (a read is the bare name)
// fails the type check; const-let-never (constLet admits nothing) fails
// the constant declarations; const-let-float (it admits a float64) fails
// the variable Half.
func TestConstLetsStayValues(t *testing.T) {
	const src = `let Big int8 = 100
let Zero = 0
let Wide = 300
let Negative = -1
let Mask uint64 = 0xFF
let Name = "a"
let On = true
let Half float64 = 0.5

func sum() int8 { Big + Big }

func quotient(n int) int { n / Zero + 1 / Zero }

func narrow() int8 { int8(Wide) }

func unsigned() uint { uint(Negative) }

func masked(h uint64) uint64 { h & (Mask - 1) }

func half() float64 { Half + Half }

func main() needs Console {
	if On && sum() == narrow() && quotient(1) == 0 && unsigned() == 0 && masked(1) == 0 && half() == 0 {
		Console.Println(Name)
	}
}
`
	p := lowerTestBuild(t, "constlet", src)
	goSrc := p.mainGo()
	for _, decl := range []string{"const Big int8 = ", "const Zero int = ", "const Wide int = ", "const Negative int = ", "const Mask uint64 = ", "const Name string = ", "const On bool = ", "var Half float64 = "} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(decl)).MatchString(goSrc) {
			t.Errorf("the lowered Go has no %q declaration", decl)
		}
	}
	typecheckGo(t, []*lowered{p})
}
