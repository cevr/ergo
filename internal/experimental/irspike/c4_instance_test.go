package irspike

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// The seeded property of the copies for an instance on the IR path
// (CONSTRAINTS C4; lower/ir_instance.go). Each seed writes one program:
// an operation type Ord[T] (sketch B, a provider) and a self-bound one
// Ranked[T] (sketch A); providers of Ord[Item] with no fields and with a
// field read at run time, two of one element type; a generic provider
// Flip[T, O] that takes a witness of its own; generic functions over the
// two sketches, one that calls another with its own type parameters (a
// copy inside a copy) and one that calls itself; and a main that calls
// them on seeded data. The program is built four ways: by the IR with its
// copies calling each operation directly, by the IR with the copies
// calling through the witness (instanceOps "witness"), by the IR with no
// copies (genericLowering "stencil"), and by the source path. All four
// print the same. Across the seeds the IR of the copies reaches every
// form: a direct call of a provider's Cmp, a call through the witness (a
// Flip provider, whose Cmp takes a witness of its own), and a copy that
// calls a copy. top over Ranked takes the known-record copy (ir_known.go),
// which calls Rank directly already; it is here so that both sketches
// print alike under every lowering. A failing seed prints the Ergo
// source, the outputs and its replay command.
//
// It builds Go because the question is what the built program prints
// under each lowering of its copies; no in-process check runs a copy.
//
// Mutants it alone catches (lower testdata/mutants.json):
// c4-direct-op-other-type (a copy calls another provider's Cmp of the
// same element type), c4-direct-op-zero-receiver (a copy drops the
// provider's fields), c4-copy-in-test-build (the IR test build calls a
// copy, where the generic function and its witness are the reference).

func TestInstanceCopiesAgree(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	reached := map[string]bool{}
	for seed := first; seed < first+count; seed++ {
		text := generateInstances(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestInstanceCopiesAgree ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		// The test build makes no copy: its calls are the reference.
		if _, rep, err := lower.LowerIRTests(u); err != nil {
			t.Fatalf("seed %d test build: %v\n%s", seed, err, text)
		} else {
			for _, name := range rep.IR {
				if strings.Contains(name, "Mono_i") {
					t.Errorf("seed %d: the test build has the copy %s\n%s\n%s", seed, name, replay, text)
				}
			}
		}
		outs := map[string]outcome{}
		for _, m := range []struct {
			name            string
			direct, stencil bool
		}{{"direct", true, false}, {"witness", false, false}, {"stencil", false, true}} {
			restore := lower.SetInstanceLowering(m.direct, m.stencil)
			d := filepath.Join(dir, m.name)
			if _, err := Emit(u, IR, d); err != nil {
				restore()
				t.Fatalf("seed %d %s: %v\n%s", seed, m.name, err, text)
			}
			if m.direct {
				copyForms(t, filepath.Join(d, lower.IRFileName), reached)
			}
			restore()
			bin := d + ".bin"
			if _, err := Build(ir, IR, d, bin, BuildOptions{}); err != nil {
				t.Fatalf("seed %d %s: %v\n%s", seed, m.name, err, text)
			}
			outs[m.name] = run(t, bin)
		}
		d := filepath.Join(dir, "source")
		if _, err := Emit(u, Source, d); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(src, Source, d, d+".bin", BuildOptions{}); err != nil {
			t.Fatalf("seed %d source: %v\n%s", seed, err, text)
		}
		outs["source"] = run(t, d+".bin")
		for _, m := range []string{"witness", "stencil", "source"} {
			if outs[m] != outs["direct"] {
				t.Errorf("seed %d: the direct copies and %s differ\ndirect: %+v\n%s: %+v\n%s\n%s", seed, m, outs["direct"], m, outs[m], replay, text)
			}
		}
		if outs["direct"].exit != 0 || outs["direct"].stdout == "" {
			t.Errorf("seed %d: the program did not run: %+v\n%s\n%s", seed, outs["direct"], replay, text)
		}
	}
	if count >= 4 {
		for _, form := range []string{"direct provider", "witness call", "copy in a copy"} {
			if !reached[form] {
				t.Errorf("no seed reached the form %q (reached %v)", form, reached)
			}
		}
	}
}

// copyForms reads the IR file and records the forms the copies take: a
// call through a witness ("witness call"), else a direct call of a
// provider's Cmp ("direct provider"), and a spread copy that calls a
// below copy ("copy in a copy").
func copyForms(t *testing.T, file string, reached map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Funcs []struct {
			Name string `json:"name"`
			Body any    `json:"body"`
		} `json:"funcs"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, f := range v.Funcs {
		if !strings.Contains(f.Name, "Mono_i") {
			continue
		}
		kinds := map[string]bool{}
		nodeKinds(f.Body, kinds)
		names := map[string]bool{}
		callNames(f.Body, names)
		switch {
		case kinds["vcall"]:
			reached["witness call"] = true
		case names["Cmp"]:
			reached["direct provider"] = true
		}
		for n := range names {
			if strings.HasPrefix(f.Name, "spreadMono") && strings.HasPrefix(n, "belowMono_i") {
				reached["copy in a copy"] = true
			}
		}
	}
}

// callNames adds the name of every call and method call in v.
func callNames(v any, names map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		if k, _ := v["k"].(string); k == "call" || k == "mcall" {
			if n, ok := v["name"].(string); ok {
				names[n] = true
			}
		}
		for _, x := range v {
			callNames(x, names)
		}
	case []any:
		for _, x := range v {
			callNames(x, names)
		}
	}
}

// generateInstances is the program of seed (TestInstanceCopiesAgree).
func generateInstances(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0xc4))
	var b strings.Builder
	b.WriteString(`type Item { K int, S string }

// Ord is sketch B: the order is a provider value.
type Ord[T] {
	Cmp(a T, b T) int,
}

type ByK {} implements Ord[Item]

`)
	// ByK's compare: by K, forward or backward, maybe then by S.
	kExpr := []string{"a.K - b.K", "b.K - a.K", "if a.K == b.K { return sgn(a.S, b.S) }\n\ta.K - b.K"}[r.IntN(3)]
	fmt.Fprintf(&b, "func Cmp(_ ByK, a Item, b Item) int {\n\t%s\n}\n\n", kExpr)
	b.WriteString(`// ByS reads its field at run time.
type ByS { Down bool } implements Ord[Item]

func Cmp(o ByS, a Item, b Item) int {
	if o.Down {
		return sgn(b.S, a.S)
	}
	sgn(a.S, b.S)
}

// Flip is a generic provider with a witness of its own.
type Flip[T any, O Ord[T]] { Of O } implements Ord[T]

func Cmp[T any, O Ord[T]](f Flip[T, O], a T, b T) int { f.Of.Cmp(b, a) }

type Num { N int } implements Ord[int]

func Cmp(m Num, a int, b int) int { (a % m.N) - (b % m.N) }

// Ranked is sketch A: the type orders itself.
type Ranked[T] {
	Rank(other T) int,
}

type Score { V int } implements Ranked[Score]

func Rank(s Score, other Score) int { s.V - other.V }

func sgn(a string, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	0
}

// best is the greatest of xs under o, or z when xs is empty.
func best[T any, O Ord[T]](xs []T, o O, z T) T {
	let mut m = z
	let mut first = true
	for x in xs {
		if first || o.Cmp(x, m) > 0 {
			m = x
			first = false
		}
	}
	m
}

// below counts the elements of xs before p under o.
func below[T any, O Ord[T]](xs []T, o O, p T) int {
	let mut n = 0
	for x in xs {
		if o.Cmp(x, p) < 0 {
			n += 1
		}
	}
	n
}

// spread calls below with its own type parameters: a copy calls a copy.
func spread[T any, O Ord[T]](xs []T, o O, p T, q T) int { below(xs, o, q) - below(xs, o, p) }

// depth calls itself.
func depth[T any, O Ord[T]](xs []T, o O, i int) int {
	if i + 1 >= len(xs) {
		return 0
	}
	let step = if o.Cmp(xs[i], xs[i+1]) <= 0 { 1 } else { 0 }
	step + depth[T, O](xs, o, i+1)
}

// top is the greatest of xs by their own rank, or z.
func top[T Ranked[T]](xs []T, z T) T {
	let mut m = z
	for x in xs {
		if x.Rank(m) > 0 {
			m = x
		}
	}
	m
}

`)
	items := func() string {
		n := r.IntN(9)
		var lits []string
		for range n {
			lits = append(lits, fmt.Sprintf("Item{K: %d, S: %q}", r.IntN(5)-2, string(rune('a'+r.IntN(4)))))
		}
		return "[]Item{" + strings.Join(lits, ", ") + "}"
	}
	ints := func() string {
		n := r.IntN(9)
		var lits []string
		for range n {
			lits = append(lits, fmt.Sprint(r.IntN(40)))
		}
		return "[]int{" + strings.Join(lits, ", ") + "}"
	}
	b.WriteString("func show(x Item) string { \"\\(x.K)/\\(x.S)\" }\n\nfunc main() needs Console {\n")
	z := `Item{K: 0, S: "-"}`
	for i := range 6 + r.IntN(4) {
		xs := items()
		fmt.Fprintf(&b, "\tlet xs%d = %s\n", i, xs)
		provs := []string{"ByK", fmt.Sprintf("ByS{Down: %v}", r.IntN(2) == 0), "Flip[Item, ByK]{Of: ByK}", fmt.Sprintf("Flip[Item, ByS]{Of: ByS{Down: %v}}", r.IntN(2) == 0)}
		p := provs[r.IntN(len(provs))]
		switch r.IntN(5) {
		case 0:
			fmt.Fprintf(&b, "\tConsole.Println(show(best(xs%d, %s, %s)))\n", i, p, z)
		case 1:
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(below(xs%d, %s, Item{K: %d, S: %q}))\")\n", i, p, r.IntN(5)-2, "b")
		case 2:
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(spread(xs%d, %s, Item{K: -1, S: \"a\"}, Item{K: 1, S: \"c\"}))\")\n", i, p)
		case 3:
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(depth(xs%d, %s, 0))\")\n", i, p)
		default:
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(best(%s, Num{N: %d}, -1))\")\n", ints(), 3+r.IntN(5))
		}
		// Two providers of one element type at one function, every seed.
		fmt.Fprintf(&b, "\tConsole.Println(show(best(xs%d, ByK, %s)) + \" \" + show(best(xs%d, ByS{Down: true}, %s)))\n", i, z, i, z)
	}
	fmt.Fprintf(&b, "\tConsole.Println(\"\\(top([]Score{Score{V: %d}, Score{V: %d}, Score{V: %d}}, Score{V: -100}).V)\")\n", r.IntN(9), r.IntN(9), r.IntN(9))
	b.WriteString("}\n")
	return b.String()
}
