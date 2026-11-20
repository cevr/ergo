package fix

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lint"
)

// etaRecv is how an eta case's receiver a is bound, and what writes it
// after the literal is taken.
type etaRecv int

const (
	etaParam    etaRecv = iota // func cN(a Acc): no write can follow: fixed
	etaLet                     // let a = Acc{…}: fixed
	etaMutLater                // let mut a; a = … after the take: kept
	etaMutQuiet                // let mut a, never written again: kept (the rule reads the binding, not the writes)
	etaBorrow                  // func cN(a mut Acc): a.N = … after the take: kept
	etaOwnRecv                 // func(b Acc) int { b.Same(b) }: b is the literal's own: kept
	etaRecvKinds
)

// fixed reports a case the rule rewrites to the method value.
func (r etaRecv) fixed() bool { return r == etaParam || r == etaLet }

// TestFixEtaMethodValueProperty generates programs of eta closures
// func(k int) int { a.Add(k) } over every kind of receiver binding, each
// bound by a let and called or passed to apply, before and after a write
// to the receiver, plus a resource receiver that must stay a literal
// (S108). After ergo fix --lint with the strict preset: exactly the cases
// whose receiver is a parameter or a let are rewritten to a.Add; the
// fixed program checks; the evaluator prints the model's line before and
// after (the method value binds at the take, the literal reads when it
// runs, so a rewrite of a let mut or a borrow receiver would print the
// new value: CV1 capture-rule.md); a second fix changes nothing; and the
// formatter keeps the text. Mutants: cv-fix-let-mut, cv-fix-borrow,
// cv-fix-literal-own-recv, cv-fix-refused-resource (check),
// cv-fix-not-safe (lint).
func TestFixEtaMethodValueProperty(t *testing.T) {
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, []string{lint.Strict})
	if err != nil {
		t.Fatal(err)
	}
	const key = "/p/main.ergo"
	seeds := []uint64{}
	if *fixSeed != 0 {
		seeds = append(seeds, uint64(*fixSeed))
	} else {
		for s := uint64(1); s <= 12; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0xe7a))
		var b strings.Builder
		b.WriteString("type Acc { N int }\n\nfunc Add(a Acc, b int) int { a.N + b }\n\nfunc Same(a Acc, b Acc) int { a.N + b.N }\n\n")
		b.WriteString("type File {} implements Closer\n\nfunc Close(_ File) {}\n\nfunc Name(f File) string { \"f\" }\n\n")
		b.WriteString("func named(f File) string {\n\tlet g = func() string { f.Name() }\n\tg()\n}\n\n")
		b.WriteString("func apply(f func(b int) int, x int) int { f(x) }\n\n")
		var parts, want []string
		fixes := 0
		kinds := []etaRecv{}
		for k := etaRecv(0); k < etaRecvKinds; k++ {
			kinds = append(kinds, k)
		}
		for range 3 {
			kinds = append(kinds, etaRecv(r.IntN(int(etaRecvKinds))))
		}
		r.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })
		for i, kind := range kinds {
			n, d, m := r.IntN(40)-10, 1+r.IntN(20), r.IntN(30)-5
			call := fmt.Sprintf("f(%d)", m)
			if r.IntN(2) == 0 {
				call = fmt.Sprintf("apply(f, %d)", m)
			}
			eta := "func(k int) int { a.Add(k) }"
			got := n + m
			switch kind {
			case etaParam:
				fmt.Fprintf(&b, "func c%d(a Acc) int {\n\tlet f = %s\n\t%s\n}\n\n", i, eta, call)
				parts = append(parts, fmt.Sprintf("\\(c%d(Acc{N: %d}))", i, n))
			case etaLet:
				fmt.Fprintf(&b, "func c%d() int {\n\tlet a = Acc{N: %d}\n\tlet f = %s\n\t%s\n}\n\n", i, n, eta, call)
				parts = append(parts, fmt.Sprintf("\\(c%d())", i))
			case etaMutLater:
				fmt.Fprintf(&b, "func c%d() int {\n\tlet mut a = Acc{N: %d}\n\tlet f = %s\n\ta = Acc{N: %d}\n\t%s\n}\n\n", i, n, eta, n+d, call)
				parts = append(parts, fmt.Sprintf("\\(c%d())", i))
				got = n + d + m
			case etaMutQuiet:
				fmt.Fprintf(&b, "func c%d() int {\n\tlet mut a = Acc{N: %d}\n\tlet f = %s\n\t%s\n}\n\n", i, n, eta, call)
				parts = append(parts, fmt.Sprintf("\\(c%d())", i))
			case etaBorrow:
				fmt.Fprintf(&b, "func c%d(a mut Acc) int {\n\tlet f = %s\n\ta.N = %d\n\t%s\n}\n\n", i, eta, n+d, call)
				fmt.Fprintf(&b, "func d%d() int {\n\tlet mut h = Acc{N: %d}\n\tc%d(&h)\n}\n\n", i, n, i)
				parts = append(parts, fmt.Sprintf("\\(d%d())", i))
				got = n + d + m
			case etaOwnRecv:
				fmt.Fprintf(&b, "func c%d() int {\n\tlet f = func(b Acc) int { b.Same(b) }\n\tf(Acc{N: %d})\n}\n\n", i, n)
				parts = append(parts, fmt.Sprintf("\\(c%d())", i))
				got = 2 * n
			}
			if kind.fixed() {
				fixes++
			}
			want = append(want, fmt.Sprint(got))
		}
		fmt.Fprintf(&b, "func line() string { \"%s\" }\n\n", strings.Join(parts, " "))
		b.WriteString("func main() needs Console {\n\tConsole.Println(line())\n}\n")
		src := b.String()
		line := strings.Join(want, " ") + "\n"
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nsource:\n%s\nreplay: go test ./internal/sketch/fix -run TestFixEtaMethodValueProperty -fix.seed=%d", seed, fmt.Sprintf(format, args...), src, seed)
		}
		if out, err := format.Source(src); err != nil || out != src {
			fail("the generator's source is not formatted (%v):\n%s", err, out)
		}
		printed := func(text string) string {
			t.Helper()
			pkg, diags := check.CheckSource(text)
			if len(diags) > 0 {
				fail("does not check: %v\n%s", diags, text)
			}
			out, err := eval.RunMain(pkg, eval.DefaultBudget(), eval.Process{})
			if err != nil {
				fail("eval: %v\n%s", err, text)
			}
			return out.Stdout
		}
		if got := printed(src); got != line {
			fail("before the fix the evaluator prints %q, the model %q", got, line)
		}
		chk, lintr := Source(key), LintSource(key, conf)
		res, err := RunLint(map[string]string{key: src}, chk, lintr)
		if err != nil {
			fail("fix --lint: %v", err)
		}
		fixed := res.Texts[key]
		if len(res.Remaining) > 0 || len(res.Linted) != fixes {
			fail("fix --lint fixed %d literals, want %d; remaining %v\nfixed:\n%s", len(res.Linted), fixes, res.Remaining, fixed)
		}
		for _, l := range res.Linted {
			if l.Rule != "eta-method-value" {
				fail("fix --lint applied %s at line %d\n%s", l.Rule, l.Line, fixed)
			}
		}
		if got := strings.Count(fixed, "a.Add\n") + strings.Count(fixed, "a.Add,"); got != fixes {
			fail("the fixed program holds %d method values a.Add, want %d\n%s", got, fixes, fixed)
		}
		if got := printed(fixed); got != line {
			fail("after the fix the evaluator prints %q, the model %q\n%s", got, line, fixed)
		}
		again, err := RunLint(res.Texts, chk, lintr)
		if err != nil || again.Texts[key] != fixed || len(again.Linted) > 0 {
			fail("a second fix --lint changed the program (%v):\n%s\nthen:\n%s", err, fixed, again.Texts[key])
		}
		if out, err := format.Source(fixed); err != nil || out != fixed {
			fail("fmt(fix(x)) != fix(x) (%v):\n%s\nformatted:\n%s", err, fixed, out)
		}
	}
}
