package check

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

var onePassSeed = flag.Int64("onepass.seed", 0, "replay one seed of TestCheckOnePass")

// onePassWork checks one file of package main and returns the syntax
// nodes the body checks visited (c.workNodes) and the sum of the bodies'
// sizes: equal when every function and package var is checked once.
func onePassWork(src string) (work, sum int, diags []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, -1, diags
	}
	for _, f := range c.funcs {
		if f.Decl != nil && f.Decl.Body != nil && f.Sig != nil {
			sum += f.bodySize()
		}
	}
	for _, pv := range c.vars {
		sum += pv.bodySize()
	}
	return c.workNodes, sum, diags
}

// onePassProgram is a program of n functions that call each other at
// random, so recursive groups form: plain calls, x.f() calls whose callee
// only typing picks (a local bound to mk()), and package vars whose
// initializers call them. Every row is written.
func onePassProgram(r *rand.Rand) string {
	var b strings.Builder
	b.WriteString("type E {}\n\ntype R {}\n\nfunc mk() R {\n\tR\n}\n")
	n := 2 + r.Intn(6)
	method := make([]bool, n)
	for i := range method {
		method[i] = r.Intn(3) == 0
	}
	call := func(j int) string {
		if method[j] {
			return fmt.Sprintf("try mk().m%d(n - 1)", j)
		}
		return fmt.Sprintf("try f%d(n - 1)", j)
	}
	for i := range n {
		if method[i] {
			fmt.Fprintf(&b, "\nfunc m%d(_ R, n int) int fails E {\n", i)
		} else {
			fmt.Fprintf(&b, "\nfunc f%d(n int) int fails E {\n", i)
		}
		b.WriteString("\tif n < 1 {\n\t\treturn E\n\t}\n")
		terms := []string{"1"}
		for range r.Intn(3) {
			j := r.Intn(n)
			if method[j] && r.Intn(2) == 0 {
				fmt.Fprintf(&b, "\tlet r%d = mk()\n", len(terms))
				fmt.Fprintf(&b, "\tlet v%d = %s\n", len(terms), strings.Replace(call(j), "mk()", fmt.Sprintf("r%d", len(terms)), 1))
			} else {
				fmt.Fprintf(&b, "\tlet v%d = %s\n", len(terms), call(j))
			}
			terms = append(terms, fmt.Sprintf("v%d", len(terms)))
		}
		fmt.Fprintf(&b, "\t%s\n}\n", strings.Join(terms, " + "))
	}
	var uses []string
	for i := range r.Intn(3) {
		j := r.Intn(n)
		c := strings.Replace(strings.Replace(call(j), "try ", "", 1), "n - 1", "3", 1)
		fmt.Fprintf(&b, "\nlet p%d = %s catch {\n\tcase E: 0\n}\n", i, c)
		uses = append(uses, fmt.Sprintf("p%d", i))
	}
	b.WriteString("\nfunc main() needs Console {\n")
	for _, u := range uses {
		fmt.Fprintf(&b, "\tConsole.Println(\"\\(%s)\")\n", u)
	}
	b.WriteString("}\n")
	return b.String()
}

// TestCheckOnePass (EXPLICIT EX2): with every row written, the checker
// checks each body once. Over generated programs with recursive groups,
// typed x.f() calls and package vars, and over every example file, the
// body checks visit exactly the bodies' syntax nodes. Mutants (ledger):
// the typing of waiting x.f() calls and the init discovery's silent
// pass, each run without inference.
func TestCheckOnePass(t *testing.T) {
	first, seeds := int64(1), 200
	if *onePassSeed != 0 {
		first, seeds = *onePassSeed, 1
	}
	cyclic := 0
	for s := first; s < first+int64(seeds); s++ {
		src := onePassProgram(rand.New(rand.NewSource(s)))
		work, sum, diags := onePassWork(src)
		if len(diags) > 0 || work != sum {
			t.Fatalf("seed %d: the body checks visited %d nodes, the bodies have %d; diagnostics %v\n%s\nreplay: go test ./internal/sketch/check -run TestCheckOnePass -onepass.seed=%d", s, work, sum, diags, src, s)
		}
		if c, _ := checkForInfer(src); c != nil && c.workNodes > sum {
			cyclic++
		}
	}
	// The domain reaches what inference checks more than once.
	if *onePassSeed == 0 && cyclic < seeds/4 {
		t.Errorf("%d of %d programs are checked more than once with inference, want a quarter", cyclic, seeds)
	}
	files, _ := filepath.Glob("../../../examples/*/*/*.ergo")
	more, _ := filepath.Glob("../../../examples/*/*/*/*.ergo")
	files = append(files, more...)
	if len(files) < 100 {
		t.Fatalf("found %d example files", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := syntax.Parse(string(data)); err != nil {
			continue
		}
		if work, sum, _ := onePassWork(string(data)); work != sum {
			t.Errorf("%s: the body checks visited %d nodes, the bodies have %d", f, work, sum)
		}
	}
}
