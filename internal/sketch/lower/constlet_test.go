package lower

import (
	"regexp"
	"testing"
)

// TestConstLetsStayValues holds the constant-let lowering (constValueLet, wf10
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
// fails the type check; const-let-never (constValueLet admits nothing) fails
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

// TestConstLetsFoldOnTheScannerPath holds the bare read of a constant
// let (constFold) to the places of S2b's scanner (selfhost S2b r4, inline
// report): a constant string indexed by a byte (identRest's classes), a
// bitwise mask of two constants (clsLetter|clsDigit), a conversion whose
// value fits its type (dense.Next's int(limit)) and a non-zero divisor
// (room). No Go fold over them can fail, so each read is the bare Go
// constant, not v1.Let(name): each v1.Let is a call that costs Go's
// inliner (identRest 53 against 33 bare; the Words loop that calls it 91,
// over Go's budget of 80, against 71) and keeps classes' base out of the
// instruction. The lowered Go still type-checks (typecheckGo), and
// TestConstLetsStayValues keeps the places where a fold can fail on
// v1.Let (int8(Wide), n / Zero, Big + Big, uint(Negative)).
//
// Mutant (lower ledger, with the fix): const-fold-scanner-places (the
// fix's index, bitwise, fitting conversion and divisor arms of
// constUses dropped) fails here. Red before the fix: constUses admits
// none of these places, so every read is v1.Let.
func TestConstLetsFoldOnTheScannerPath(t *testing.T) {
	const src = `let classes = "\x00\x09\x0a\x04\x00\x10"
let clsLetter = uint8(1)
let clsDigit = uint8(2)
let limit = uint32(0xffffffff)
let room = 64

type scanner { kinds []uint8, ids uint32 }

func identRest(src string, i int) int {
	let mut end = i
	for end < len(src) && classes[int(src[end])]&(clsLetter|clsDigit) != 0 {
		end = end + 1
	}
	end
}

func next(s mut scanner) int {
	let n = int(s.ids)
	if n >= int(limit) {
		return -1
	}
	s.ids = s.ids + uint32(1)
	n
}

func slot(c uint8) int { int(c) % room }

func main() needs Console {
	let mut s = scanner{kinds: []uint8{}, ids: uint32(0)}
	Console.Println("\(identRest("\x01\x02\x00", 0)) \(next(&s)) \(slot(uint8(200)))")
}
`
	p := lowerTestBuild(t, "constfold", src)
	goSrc := p.mainGo()
	for _, name := range []string{"classes", "clsLetter", "clsDigit", "limit", "room"} {
		if !regexp.MustCompile(`(?m)^const ` + name + ` `).MatchString(goSrc) {
			t.Errorf("the lowered Go has no const %s", name)
		}
		if regexp.MustCompile(`\.Let\(` + name + `\)`).MatchString(goSrc) {
			t.Errorf("%s is read through v1.Let, where no Go fold over it can fail", name)
		}
	}
	typecheckGo(t, []*lowered{p})
}
