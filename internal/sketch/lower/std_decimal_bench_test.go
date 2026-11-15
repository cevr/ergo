package lower

import "testing"

// BenchmarkStdDecimal is std-spike D2's hand-Go control: an invoice of
// 4,096 lines, each a price in cents times a quantity, summed, and a tax
// of 8.25% rounded half-even to the cent. Ergo's decimal against the Go a
// programmer writes on int64 cents, and on math/big.Rat. Reported either
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
	"fmt"
	"math/big"
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

func BenchmarkInvoiceErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Invoice", "xs") + `
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
	if e, c, r := ` + emittedCall(mainGo, "Invoice", "xs") + `, cents(), rat(); e != c || c != r {
		t.Fatalf("Ergo %s, Go cents %s, big.Rat %s", e, c, r)
	}
}
`
	}
	emittedBench(b, "std_decimal", src, gen, "InvoiceErgo", "InvoiceGoCents", "InvoiceGoRat")
}
