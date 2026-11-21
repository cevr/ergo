package lower

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/testwait"
)

// TestStdDecimalLawsRun runs std decimal's laws (std-spike D2) as the
// probe's tests with --seed 1; lawledger.Run holds that the probe restates
// exactly the ledger's decimal laws, each with the row's statement, and
// that each passed. It builds Go because each law is an Ergo test the
// built probe runs, as TestStdSlicesLawsRun's are.
func TestStdDecimalLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stddecimal", stdDecimalLawsProbe, stdDecimalLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// decimalProgram reads one case per line, "op a b scale mode", and prints
// one line per case: the result's Text, or overflow, divzero or
// malformed. Ops: P parses a; A, S and M add, subtract and multiply a and
// b; R rounds a; D divides a by b; C compares a and b.
const decimalProgram = `import decimal
import io
import strconv
import strings

func mode(s string) decimal.RoundingMode {
	if s == "HU" { decimal.HalfUp } else if s == "HE" { decimal.HalfEven } else if s == "DN" { decimal.Down } else if s == "UP" { decimal.Up } else if s == "FL" { decimal.Floor } else { decimal.Ceil }
}

func run(f []string) string fails decimal.Malformed | decimal.Overflow | decimal.DivideByZero {
	let a = try decimal.Parse(f[1])
	if f[0] == "P" {
		return decimal.Text(a)
	}
	let b = try decimal.Parse(f[2])
	let s = strconv.Atoi(f[3]) catch {
	case strconv.Malformed, strconv.OutOfRange:
		0
	}
	if f[0] == "A" {
		decimal.Text(try decimal.Add(a, b))
	} else if f[0] == "S" {
		decimal.Text(try decimal.Sub(a, b))
	} else if f[0] == "M" {
		decimal.Text(try decimal.Mul(a, b))
	} else if f[0] == "R" {
		decimal.Text(try decimal.Round(a, s, mode(f[4])))
	} else if f[0] == "D" {
		decimal.Text(try decimal.Div(a, b, s, mode(f[4])))
	} else {
		"\(decimal.Cmp(a, b)) \(decimal.Equal(a, b))"
	}
}

func step(line string) string {
	let f = strings.Fields(line + " _")
	run(f) catch {
	case decimal.Malformed:
		"malformed"
	case decimal.Overflow:
		"overflow"
	case decimal.DivideByZero:
		"divzero"
	}
}

func main() needs Console | Ambient {
	for line in io.NewStdin().Lines() {
		Console.Println(step(line))
	}
}
`

// dec is the reference's decimal: a signed coefficient and a scale.
type refDec struct {
	c *big.Int
	s int
}

var (
	ref128 = new(big.Int).Lsh(big.NewInt(1), 128)
	refTen = big.NewInt(10)
)

func refPow10(k int) *big.Int { return new(big.Int).Exp(refTen, big.NewInt(int64(k)), nil) }

func refFits(c *big.Int) bool { return new(big.Int).Abs(c).Cmp(ref128) < 0 }

// refParse is decimal.Parse's form: -?digits(.digits)?, a coefficient
// under 2^128 and at most 38 digits after the point.
func refParse(text string) (refDec, string) {
	s := strings.TrimPrefix(text, "-")
	whole, frac, dot := strings.Cut(s, ".")
	digitsOnly := func(x string) bool {
		for _, r := range x {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if whole == "" || !digitsOnly(whole) || !digitsOnly(frac) || dot && frac == "" {
		return refDec{}, "malformed"
	}
	c, _ := new(big.Int).SetString(whole+frac, 10)
	if !refFits(c) || len(frac) > 38 {
		return refDec{}, "overflow"
	}
	if strings.HasPrefix(text, "-") {
		c.Neg(c)
	}
	return refDec{c, len(frac)}, ""
}

// refText is decimal.Text's form.
func refText(d refDec) string {
	digits := new(big.Int).Abs(d.c).String()
	sign := ""
	if d.c.Sign() < 0 {
		sign = "-"
	}
	if d.s == 0 {
		return sign + digits
	}
	if len(digits) <= d.s {
		digits = strings.Repeat("0", d.s-len(digits)+1) + digits
	}
	p := len(digits) - d.s
	return sign + digits[:p] + "." + digits[p:]
}

func (d refDec) rat() *big.Rat { return new(big.Rat).SetFrac(d.c, refPow10(d.s)) }

// refRound is v·10^s rounded to an integer by mode, from big.Rat's exact
// quotient and remainder.
func refRound(v *big.Rat, s int, mode string) *big.Int {
	x := new(big.Rat).Mul(v, new(big.Rat).SetInt(refPow10(s)))
	neg := x.Sign() < 0
	x.Abs(x)
	q, r := new(big.Int).QuoRem(x.Num(), x.Denom(), new(big.Int))
	twice := new(big.Int).Lsh(r, 1).Cmp(x.Denom())
	bump := false
	if r.Sign() != 0 {
		switch mode {
		case "HU":
			bump = twice >= 0
		case "HE":
			bump = twice > 0 || twice == 0 && q.Bit(0) == 1
		case "DN":
		case "UP":
			bump = true
		case "FL":
			bump = neg
		case "CE":
			bump = !neg
		}
	}
	if bump {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return q
}

// refAlign is a and b at the larger scale, or overflow when a moved
// coefficient needs more than 128 bits (stdlib.md, decimal 2).
func refAlign(a, b refDec) (*big.Int, *big.Int, int, bool) {
	s := max(a.s, b.s)
	x := new(big.Int).Mul(a.c, refPow10(s-a.s))
	y := new(big.Int).Mul(b.c, refPow10(s-b.s))
	return x, y, s, refFits(x) && refFits(y)
}

func refResult(c *big.Int, s int) string {
	if !refFits(c) || s > 38 {
		return "overflow"
	}
	return refText(refDec{c, s})
}

// refAnswer is decimalProgram's line for one case.
func refAnswer(op, at, bt string, s int, mode string) string {
	a, bad := refParse(at)
	if bad != "" {
		return bad
	}
	if op == "P" {
		return refText(a)
	}
	b, bad := refParse(bt)
	if bad != "" {
		return bad
	}
	switch op {
	case "A", "S":
		x, y, sc, ok := refAlign(a, b)
		if !ok {
			return "overflow"
		}
		if op == "S" {
			y.Neg(y)
		}
		return refResult(x.Add(x, y), sc)
	case "M":
		return refResult(new(big.Int).Mul(a.c, b.c), a.s+b.s)
	case "R":
		if s < 0 || s > 38 {
			return "overflow"
		}
		return refResult(refRound(a.rat(), s, mode), s)
	case "D":
		if b.c.Sign() == 0 {
			return "divzero"
		}
		if s < 0 || s > 38 {
			return "overflow"
		}
		// Div multiplies a's coefficient by 10^e before it divides.
		if e := s + b.s - a.s; e >= 0 && a.c.Sign() != 0 && (e > 38 || !refFits(new(big.Int).Mul(a.c, refPow10(e)))) {
			return "overflow"
		}
		return refResult(refRound(new(big.Rat).Quo(a.rat(), b.rat()), s, mode), s)
	default:
		c := a.rat().Cmp(b.rat())
		return fmt.Sprintf("%d %t", c, c == 0)
	}
}

// decimalCases is n cases and the reference's answers: coefficients of
// every bit length up to 128 (and 129, past the bound), scales 0 to 40,
// both signs, values that end in 5 for rounding ties, and malformed and
// overflowing texts.
func decimalCases(r *rand.Rand, n int) (cases, want []string) {
	modes := []string{"HU", "HE", "DN", "UP", "FL", "CE"}
	operand := func() string {
		bitsN := r.IntN(130)
		if r.IntN(3) == 0 {
			bitsN = r.IntN(40)
		}
		// A random number of bitsN bits: 192 random bits shifted down.
		c := new(big.Int).SetUint64(r.Uint64())
		for range 2 {
			c.Lsh(c, 64).Or(c, new(big.Int).SetUint64(r.Uint64()))
		}
		c.Rsh(c, uint(192-bitsN))
		if r.IntN(4) == 0 {
			c.Mul(c, refTen).Add(c, big.NewInt(5))
		}
		if r.IntN(2) == 0 {
			c.Neg(c)
		}
		s := r.IntN(8)
		if r.IntN(5) == 0 {
			s = r.IntN(41)
		}
		return refText(refDec{c, s})
	}
	add := func(op, a, b string, s int, mode string) {
		cases = append(cases, fmt.Sprintf("%s %s %s %d %s", op, a, b, s, mode))
		want = append(want, refAnswer(op, a, b, s, mode))
	}
	for _, bad := range []string{"-", "1.", ".5", "+1", "1e5", "1..2", "--1", "1.2.3", "0x10", "1_000", "340282366920938463463374607431768211456", "0.000000000000000000000000000000000000001"} {
		add("P", bad, "0", 0, "HU")
	}
	// Divisions whose divisor, b·10^k, is past 128 bits while a is near
	// half of it: the quotient is 0 or 1, by a against b·5·10^(k-1).
	for len(cases) < n/20 {
		k := 1 + r.IntN(38)
		bc := new(big.Int).Rsh(new(big.Int).SetUint64(r.Uint64()), uint(r.IntN(64)))
		bc.Lsh(bc, uint(r.IntN(100))).Add(bc, big.NewInt(1))
		half := new(big.Int).Mul(bc, new(big.Int).Mul(big.NewInt(5), refPow10(k-1)))
		if new(big.Int).Lsh(half, 1).Cmp(ref128) < 0 || half.Cmp(ref128) >= 0 {
			continue
		}
		ac := half.Add(half, big.NewInt(int64(r.IntN(3)-1)))
		if !refFits(ac) {
			continue
		}
		add("D", refText(refDec{ac, k}), refText(refDec{bc, 0}), 0, modes[r.IntN(len(modes))])
	}
	for len(cases) < n {
		op := []string{"P", "A", "S", "M", "R", "D", "C"}[r.IntN(7)]
		s := r.IntN(10)
		if r.IntN(6) == 0 {
			s = r.IntN(42) - 1
		}
		b := operand()
		if op == "D" && r.IntN(20) == 0 {
			b = "0.00"
		}
		add(op, operand(), b, s, modes[r.IntN(len(modes))])
	}
	return cases, want
}

// TestStdDecimalMatchesRat (std-spike D2; stdlib.md, decimal 5): every
// decimal operation agrees with Go's math/big on 20,000 seeded cases
// (decimalCases): Parse and Text with the form, Add, Sub and Mul exact or
// Overflow, Round and Div in each mode against big.Rat's exact quotient,
// rounded by the mode, and Cmp against Rat.Cmp. It catches what the laws
// do not: a 128-bit step wrong near the bound (the laws draw small
// numbers), and a rounding consistent with itself but not with the
// rationals.
//
// It builds Go because it compares the runs of a built program with a
// reference (Go's own math/big), and the question is behaviour at run
// time; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestStdDecimalMatchesRat(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "decimalRat")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["decimalRat"]
	probeRan(exe)
	cases, want := decimalCases(rand.New(rand.NewPCG(2, 77)), 20000)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = strings.NewReader(strings.Join(cases, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd) // under the test kit's memory limit (proclimit)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("%v\n%s", err, firstLines(stderr.String(), 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("%d cases, %d lines", len(cases), len(got))
	}
	bad := 0
	for i := range cases {
		if got[i] != want[i] {
			t.Errorf("%s: Ergo %s, big.Rat %s", cases[i], got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
}
