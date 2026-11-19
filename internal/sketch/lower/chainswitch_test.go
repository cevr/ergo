package lower

import (
	"regexp"
	"testing"
)

// TestCompareChainIsASwitch holds L2's promised form on S2b's opKind
// (selfhost S2b r4, inline report: opKind, cost 414, a chain of 30
// comparisons, on every operator token). A body whose statements are a
// chain of `if c == K { … return … }` on one byte or integer value c, K
// constants, each arm leaving the body, lowers to one Go switch on c, so
// gc picks a jump table or a binary search over the constants; the chain
// compares them one by one. Measured on S2b's opKind (CONST-FOLD probe,
// L2 red-check.md): 55.6 instructions per call as a chain, 41.3 as a
// switch, and gc's inline cost 414 against 376.
//
// The fact checked: opKind's Go body is a switch on c and keeps no
// `c == K` comparison of the chain; the lowered Go type-checks
// (typecheckGo). Red before L2: the chain lowers as written.
func TestCompareChainIsASwitch(t *testing.T) {
	const src = `let Period = uint8(1)
let DotDot = uint8(2)
let Comma = uint8(3)
let LBrace = uint8(4)
let RBrace = uint8(5)
let Colon = uint8(6)
let Define = uint8(7)
let Assign = uint8(8)
let Eql = uint8(9)
let Lss = uint8(10)
let Shl = uint8(11)
let Add = uint8(12)
let Inc = uint8(13)
let Illegal = uint8(0)

func opKind(c byte, c1 byte) uint8 {
	if c == '.' {
		return if c1 == '.' { DotDot } else { Period }
	}
	if c == ',' {
		return Comma
	}
	if c == '{' {
		return LBrace
	}
	if c == '}' {
		return RBrace
	}
	if c == ':' {
		return if c1 == '=' { Define } else { Colon }
	}
	if c == '=' {
		return if c1 == '=' { Eql } else { Assign }
	}
	if c == '<' {
		if c1 == '<' {
			return Shl
		}
		return Lss
	}
	if c == '+' {
		return if c1 == '+' { Inc } else { Add }
	}
	Illegal
}

func main() needs Console {
	let ops = ".,{}:=<+..:===<<++x"
	let mut sum = 0
	for i in 0..len(ops) {
		let c1 = if i+1 < len(ops) { ops[i+1] } else { byte(0) }
		sum += int(opKind(ops[i], c1))
	}
	Console.Println("\(sum)")
}
`
	p := lowerTestBuild(t, "chainswitch", src)
	body := goFunc(t, p.mainGo(), "opKind")
	if !regexp.MustCompile(`switch c \{`).MatchString(body) {
		t.Errorf("opKind's chain of comparisons of c is no Go switch on c:\n%s", body)
	}
	if n := len(regexp.MustCompile(`\bc == uint8\(`).FindAllString(body, -1)); n != 0 {
		t.Errorf("opKind keeps %d comparisons of c from its chain:\n%s", n, body)
	}
	typecheckGo(t, []*lowered{p})
}
