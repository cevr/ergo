package eval

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var providerMapSeed = flag.Uint64("providermap.seed", 0, "replay TestProviderMapAgrees at this seed alone")

// TestProviderMapAgrees is the property of the prelude's hashTable (types.md,
// Maps 5; CONSTRAINTS C5): seeded sequences of m[k] = v, Delete, Get, Take,
// len and for on a Map[int, int, Mod], whose provider makes keys equal modulo
// M and hashes them into B buckets (B = 1 puts every key on one probe
// chain), agree with a reference model written here: a slice of entries
// searched with the provider's Equal, each keeping the key it was first
// put with. The table is ordinary Ergo, so the evaluator runs the same
// source the build lowers. Sequences long enough to cross each growth
// step, with deletes between puts, reach the probe past a deleted slot and
// the compaction. A failing seed prints the Ergo source, both outputs and
// the replay flag.
//
// The provider is one of four (CONSTRAINTS C5b): keys equal modulo M with
// a bucketed hash (the table), or with a normal form k % M, k / M or k
// (keyTable, types.md Maps 5); a seed checks the checker chose the one
// the shape gives. The run also compares m == o and m != p, o built of
// the model's entries in reverse order under another equal spelling and
// p the same with one value changed.
//
// Mutants only it kills (testdata/mutants.json): c5-table-hash-only,
// c5-table-delete-hole, c5-table-grow-other-hash,
// c5b-keytable-latest-spelling, c5b-map-equal-structural,
// c5b-keytable-equal-spelling.
func TestProviderMapAgrees(t *testing.T) {
	seeds := []uint64{}
	for s := uint64(1); s <= 48; s++ {
		seeds = append(seeds, s)
	}
	if *providerMapSeed != 0 {
		seeds = []uint64{*providerMapSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 218))
		mod := []int{5, 13, 40}[r.IntN(3)]
		buckets := []int{1, 3, 64}[r.IntN(3)]
		// The provider: keys equal modulo mod with a bucketed hash (the
		// table), or with a normal form (keyTable, types.md Maps 5):
		// k % mod, k / mod, or k itself.
		kind := seed % 4
		norm := func(k int) int {
			switch kind {
			case 2:
				return k / mod
			case 3:
				return k
			}
			return k % mod
		}
		provider := []string{
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { uint64((k %% %[1]d) %% %[2]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a %% %[1]d == b %% %[1]d }", mod, buckets),
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { hash(k %% %[1]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a %% %[1]d == b %% %[1]d }", mod),
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { hash(k / %[1]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a / %[1]d == b / %[1]d }", mod),
			"func Hash(_ Mod, k int) uint64 { hash(k) }\n\nfunc Equal(_ Mod, a int, b int) bool { a == b }",
		}[kind]
		type entry struct{ k, v int }
		var model []entry
		find := func(k int) int {
			for i, e := range model {
				if norm(e.k) == norm(k) {
					return i
				}
			}
			return -1
		}
		var body, want strings.Builder
		for op := range 20 + r.IntN(40) {
			k := r.IntN(60)
			switch r.IntN(6) {
			case 0, 1, 2:
				v := op
				fmt.Fprintf(&body, "\tm[%d] = %d\n", k, v)
				if i := find(k); i >= 0 {
					model[i].v = v
				} else {
					model = append(model, entry{k, v})
				}
			case 3:
				fmt.Fprintf(&body, "\tm = m.Delete(%d)\n", k)
				if i := find(k); i >= 0 {
					model = append(model[:i], model[i+1:]...)
				}
			case 4:
				fmt.Fprintf(&body, "\tConsole.Println(show(m.Get(%d)))\n", k)
				if i := find(k); i >= 0 {
					fmt.Fprintf(&want, "%d\n", model[i].v)
				} else {
					want.WriteString("none\n")
				}
			case 5:
				fmt.Fprintf(&body, "\tlet (got%d, rest%d) = m.Take(%d)\n\tConsole.Println(show(got%[1]d))\n\tm = rest%[1]d\n", op, op, k)
				if i := find(k); i >= 0 {
					fmt.Fprintf(&want, "%d\n", model[i].v)
					model = append(model[:i], model[i+1:]...)
				} else {
					want.WriteString("none\n")
				}
			}
		}
		// An order-free summary of every stored entry: the stored key
		// (the first spelling) and its value.
		sum := 0
		for _, e := range model {
			sum += e.k*1000 + e.v
		}
		// == by the provider's Equal: the model's entries put in reverse
		// order under another equal spelling, and the same with one value
		// changed.
		var same, other strings.Builder
		for i := len(model) - 1; i >= 0; i-- {
			k := model[i].k
			if kind != 3 && kind != 2 {
				k += mod // equal modulo mod, another spelling
			}
			fmt.Fprintf(&same, "\to[%d] = %d\n", k, model[i].v)
			v := model[i].v
			if i == 0 {
				v++
			}
			fmt.Fprintf(&other, "\tp[%d] = %d\n", k, v)
		}
		fmt.Fprintf(&want, "true %v\n", len(model) > 0)
		fmt.Fprintf(&want, "%d %d\n", len(model), sum)
		mut := "let "
		if len(model) > 0 {
			mut = "let mut "
		}
		body.WriteString("\t" + mut + "o = Map[int, int, Mod]{}\n" + same.String() + "\t" + mut + "p = Map[int, int, Mod]{}\n" + other.String() +
			"\tConsole.Println(\"\\(m == o) \\(m != p)\")\n")
		src := fmt.Sprintf(`type Mod {} implements Hashing[int]

%[1]s

func show(o Option[int]) string {
	match o {
		case Some{Value}: "\(Value)"
		case None: "none"
	}
}

func main() needs Console {
	let mut m = Map[int, int, Mod]{}
%[2]s	let mut sum = 0
	for k, v in m {
		sum = sum + k * 1000 + v
	}
	Console.Println("\(len(m)) \(sum)")
}
`, provider, body.String())
		pkg := checked(t, src)
		shaped := false
		for _, f := range pkg.Funcs {
			shaped = shaped || f.Name == "NormalKey"
		}
		if shaped != (kind != 0) {
			t.Fatalf("seed %d: the provider's normal form derived: %v, want %v\n%s", seed, shaped, kind != 0, src)
		}
		out, err := RunMain(pkg, DefaultBudget(), Process{})
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		if out.Skipped != "" || out.Stdout != want.String() {
			t.Fatalf("seed %d: the table disagrees with the model (skipped %q)\ngot:\n%s\nwant:\n%s\nsource:\n%s\nreplay: go test ./internal/sketch/eval -run TestProviderMapAgrees -providermap.seed %d",
				seed, out.Skipped, out.Stdout, want.String(), src, seed)
		}
	}
}
