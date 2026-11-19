package lower

import "testing"

// BenchmarkStdDecimal is std-spike D2's hand-Go control: an invoice of
// 4,096 lines, each a price in cents times a quantity, summed, and a tax
// of 8.25% rounded half-even to the cent. Ergo's decimal against the same
// 128-bit algorithm written in Go on math/bits, twice: as a Go programmer
// first writes it, whose decMul and decAdd Go does not inline (D2b), and
// with small inlinable fast paths and the full functions on the cold path,
// the best hand Go (D3); and against Go on int64 cents and math/big.Rat. Reported either
// way; the program's TestInvoicesAgree holds the three to one answer.
func BenchmarkStdDecimal(b *testing.B) {
	const src = `import decimal

func Invoice(xs []int) string {
	let rate = decimal.New(825, 4) catch { case decimal.Overflow: decimal.Zero() }
	let mut total = decimal.Zero()
	for x in xs {
		let price = decimal.New(int64(x), 2) catch { case decimal.Overflow: decimal.Zero() }
		let line = decimal.Mul(price, decimal.FromInt(int64(x % 7 + 1))) catch { case decimal.Overflow: decimal.Zero() }
		total = decimal.Add(total, line) catch { case decimal.Overflow: decimal.Zero() }
	}
	let tax = decimal.Mul(total, rate) catch { case decimal.Overflow: decimal.Zero() }
	decimal.Text(decimal.Round(tax, 2, decimal.HalfEven) catch { case decimal.Overflow: decimal.Zero() })
}

func main() needs Console {
	Console.Println(Invoice([]int{1999, 250}))
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
	"testing"
)

var xs = func() []int {
	out := make([]int, 4096)
	for i := range out {
		out[i] = i * 7919 % 100003
	}
	return out
}()

var sink string

// cents is the tax on int64 cents: the total times 825, divided by 10,000
// rounded half-even by hand.
func cents() string {
	var total int64
	for _, x := range xs {
		total += int64(x) * int64(x%7+1)
	}
	n := total * 825
	q, r := n/10000, n%10000
	if r > 5000 || r == 5000 && q%2 == 1 {
		q++
	}
	return fmt.Sprintf("%d.%02d", q/100, q%100)
}

// rat is the tax on math/big.Rat, rounded half-even to the cent.
func rat() string {
	total := new(big.Rat)
	line := new(big.Rat)
	for _, x := range xs {
		line.SetFrac64(int64(x)*int64(x%7+1), 100)
		total.Add(total, line)
	}
	tax := new(big.Rat).Mul(total, big.NewRat(825, 10000))
	scaled := new(big.Rat).Mul(tax, big.NewRat(100, 1))
	q, r := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	twice := new(big.Int).Lsh(r, 1).Cmp(scaled.Denom())
	if twice > 0 || twice == 0 && q.Bit(0) == 1 {
		q.Add(q, big.NewInt(1))
	}
	c := q.Int64()
	return fmt.Sprintf("%d.%02d", c/100, c%100)
}

// u128Dec is the hand-Go control: decimal's algorithm written as a Go
// programmer would, on math/bits, with (value, error) results.
type h128 struct{ hi, lo uint64 }

type u128Dec struct {
	neg   bool
	c     h128
	scale int
}

var errOverflow = errors.New("overflow")

func (a h128) zero() bool { return a.hi == 0 && a.lo == 0 }

func (a h128) cmp(b h128) int {
	switch {
	case a.hi != b.hi:
		if a.hi < b.hi {
			return -1
		}
		return 1
	case a.lo != b.lo:
		if a.lo < b.lo {
			return -1
		}
		return 1
	}
	return 0
}

func (a h128) add(b h128) (h128, bool) {
	lo, c := bits.Add64(a.lo, b.lo, 0)
	hi, out := bits.Add64(a.hi, b.hi, c)
	return h128{hi, lo}, out != 0
}

func (a h128) sub(b h128) h128 {
	lo, br := bits.Sub64(a.lo, b.lo, 0)
	hi, _ := bits.Sub64(a.hi, b.hi, br)
	return h128{hi, lo}
}

func (a h128) mul64(m uint64) (h128, bool) {
	h0, lo := bits.Mul64(a.lo, m)
	h1, l1 := bits.Mul64(a.hi, m)
	hi, c := bits.Add64(l1, h0, 0)
	return h128{hi, lo}, h1 != 0 || c != 0
}

func (a h128) mul(b h128) (h128, bool) {
	if a.hi == 0 && b.hi == 0 {
		h, l := bits.Mul64(a.lo, b.lo)
		return h128{h, l}, false
	}
	if a.hi != 0 && b.hi != 0 {
		return h128{}, true
	}
	h0, lo := bits.Mul64(a.lo, b.lo)
	c1h, c1 := bits.Mul64(a.hi, b.lo)
	c2h, c2 := bits.Mul64(a.lo, b.hi)
	s, k1 := bits.Add64(h0, c1, 0)
	hi, k2 := bits.Add64(s, c2, 0)
	return h128{hi, lo}, c1h != 0 || c2h != 0 || k1 != 0 || k2 != 0
}

func (a h128) divmod64(y uint64) (h128, uint64) {
	ql, r := bits.Div64(a.hi%y, a.lo, y)
	return h128{a.hi / y, ql}, r
}

func hpow10(k int) h128 {
	p := h128{0, 1}
	for ; k >= 19; k -= 19 {
		p, _ = p.mul64(10000000000000000000)
	}
	m := uint64(1)
	for range k {
		m *= 10
	}
	p, _ = p.mul64(m)
	return p
}

func newDec(units int64, scale int) (u128Dec, error) {
	if scale < 0 || scale > 38 {
		return u128Dec{}, errOverflow
	}
	mag := uint64(units)
	if units < 0 {
		mag = uint64(-(units + 1)) + 1
	}
	return u128Dec{units < 0, h128{0, mag}, scale}, nil
}

func hrescale(c h128, from, to int) (h128, error) {
	if to == from || c.zero() {
		return c, nil
	}
	out, over := c.mul(hpow10(to - from))
	if over {
		return h128{}, errOverflow
	}
	return out, nil
}

func decAdd(a, b u128Dec) (u128Dec, error) {
	s := max(a.scale, b.scale)
	x, err := hrescale(a.c, a.scale, s)
	if err != nil {
		return u128Dec{}, err
	}
	y, err := hrescale(b.c, b.scale, s)
	if err != nil {
		return u128Dec{}, err
	}
	if a.neg == b.neg {
		sum, over := x.add(y)
		if over {
			return u128Dec{}, errOverflow
		}
		return u128Dec{a.neg && !sum.zero(), sum, s}, nil
	}
	if x.cmp(y) >= 0 {
		d := x.sub(y)
		return u128Dec{a.neg && !d.zero(), d, s}, nil
	}
	d := y.sub(x)
	return u128Dec{b.neg && !d.zero(), d, s}, nil
}

func decMul(a, b u128Dec) (u128Dec, error) {
	s := a.scale + b.scale
	if s > 38 {
		return u128Dec{}, errOverflow
	}
	c, over := a.c.mul(b.c)
	if over {
		return u128Dec{}, errOverflow
	}
	neg := a.neg != b.neg
	return u128Dec{neg && !c.zero(), c, s}, nil
}

// decRoundHalfEven is Round(d, scale, HalfEven) for a divisor 10^k under
// 2^64, the case the invoice takes.
func decRoundHalfEven(d u128Dec, scale int) (u128Dec, error) {
	if scale >= d.scale {
		c, err := hrescale(d.c, d.scale, scale)
		return u128Dec{d.neg, c, scale}, err
	}
	dv := hpow10(d.scale - scale).lo
	q, r := d.c.divmod64(dv)
	if r > dv-r || r == dv-r && q.lo%2 == 1 {
		var over bool
		q, over = q.add(h128{0, 1})
		if over {
			return u128Dec{}, errOverflow
		}
	}
	return u128Dec{d.neg && !q.zero(), q, scale}, nil
}

func decText(d u128Dec) string {
	digits := strconv.FormatUint(d.c.lo, 10)
	if d.c.hi != 0 {
		var b big.Int
		b.SetUint64(d.c.hi)
		b.Lsh(&b, 64).Or(&b, new(big.Int).SetUint64(d.c.lo))
		digits = b.String()
	}
	sign := ""
	if d.neg {
		sign = "-"
	}
	if d.scale == 0 {
		return sign + digits
	}
	if len(digits) <= d.scale {
		digits = strings.Repeat("0", d.scale-len(digits)+1) + digits
	}
	p := len(digits) - d.scale
	return sign + digits[:p] + "." + digits[p:]
}

func u128Side() {
	rate, _ := newDec(825, 4)
	total := u128Dec{}
	for _, x := range xs {
		price, err := newDec(int64(x), 2)
		if err != nil {
			price = u128Dec{}
		}
		q, _ := newDec(int64(x%7+1), 0)
		line, err := decMul(price, q)
		if err != nil {
			line = u128Dec{}
		}
		total, err = decAdd(total, line)
		if err != nil {
			total = u128Dec{}
		}
	}
	tax, _ := decMul(total, rate)
	r, _ := decRoundHalfEven(tax, 2)
	sink = decText(r)
}

// The best hand Go of the same algorithm: each operation has a small
// inlinable fast path (both coefficients under 2^64, one scale, one sign)
// that reports whether it applied, and the caller takes the full function
// above on the cold path, so Go's inliner budget admits the fast paths.
func mulFast(a, b u128Dec) (u128Dec, bool) {
	if a.c.hi|b.c.hi != 0 || a.scale+b.scale > 38 {
		return u128Dec{}, false
	}
	h, l := bits.Mul64(a.c.lo, b.c.lo)
	return u128Dec{a.neg != b.neg && h|l != 0, h128{h, l}, a.scale + b.scale}, true
}

func addFast(a, b u128Dec) (u128Dec, bool) {
	if a.scale != b.scale || a.neg != b.neg {
		return u128Dec{}, false
	}
	lo, c := bits.Add64(a.c.lo, b.c.lo, 0)
	hi, out := bits.Add64(a.c.hi, b.c.hi, c)
	return u128Dec{a.neg && hi|lo != 0, h128{hi, lo}, a.scale}, out == 0
}

func u128FastSide() {
	rate, _ := newDec(825, 4)
	total := u128Dec{}
	for _, x := range xs {
		price, err := newDec(int64(x), 2)
		if err != nil {
			price = u128Dec{}
		}
		q, _ := newDec(int64(x%7+1), 0)
		line, ok := mulFast(price, q)
		if !ok {
			if line, err = decMul(price, q); err != nil {
				line = u128Dec{}
			}
		}
		sum, ok := addFast(total, line)
		if !ok {
			if sum, err = decAdd(total, line); err != nil {
				sum = u128Dec{}
			}
		}
		total = sum
	}
	tax, _ := decMul(total, rate)
	r, _ := decRoundHalfEven(tax, 2)
	sink = decText(r)
}

func BenchmarkInvoiceErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Invoice", "xs") + `
	}
}

func BenchmarkInvoiceGoU128(b *testing.B) {
	for range b.N {
		u128Side()
	}
}

func BenchmarkInvoiceGoU128Inl(b *testing.B) {
	for range b.N {
		u128FastSide()
	}
}

func BenchmarkInvoiceGoCents(b *testing.B) {
	for range b.N {
		sink = cents()
	}
}

func BenchmarkInvoiceGoRat(b *testing.B) {
	for range b.N {
		sink = rat()
	}
}

func TestInvoicesAgree(t *testing.T) {
	u128FastSide()
	fast := sink
	u128Side()
	if sink != fast {
		t.Fatalf("Go u128 %s, Go u128 inlined %s", sink, fast)
	}
	if e, c, r := ` + emittedCall(mainGo, "Invoice", "xs") + `, cents(), rat(); e != c || c != r || sink != e {
		t.Fatalf("Ergo %s, Go u128 %s, Go cents %s, big.Rat %s", e, sink, c, r)
	}
}
`
	}
	emittedBench(b, "std_decimal", src, gen, "InvoiceErgo", "InvoiceGoU128", "InvoiceGoU128Inl", "InvoiceGoCents", "InvoiceGoRat")
}
