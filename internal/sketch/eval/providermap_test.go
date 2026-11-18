package eval

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
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
// The provider is one of five (CONSTRAINTS C5b, C5d): keys equal modulo M
// with a bucketed hash (the table), with a normal form k % M, k / M or k
// that the checker derives (types.md Maps 6), or with a bucketed hash and
// a written NormalForm and NormalKey k % M; a seed checks the derived
// NormalForm is the one the shape gives. Each seed runs under the default
// and the reference lowering (mapLowering=table, every derived NormalForm
// false). The run also compares m == o and m != p, o built of the model's
// entries in reverse order under another equal spelling and p the same
// with one value changed, and reads m through generic functions over the
// provider.
//
// Mutants only it kills (testdata/mutants.json): c5-table-hash-only,
// c5-table-delete-hole, c5-table-grow-other-hash,
// c5b-keytable-latest-spelling, c5b-map-equal-structural,
// c5b-map-equal-spelling, c5d-table-latest-spelling,
// c5d-normal-form-ignored, c5d-written-normal-form-ignored.
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
		// table), or with a normal form (Go's map by NormalKey, types.md Maps 5):
		// k % mod, k / mod, or k itself.
		kind := seed % 5
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
			fmt.Sprintf("func Hash(p Mod, k int) uint64 { uint64((k %% p.m) %% %d) }\n\nfunc Equal(p Mod, a int, b int) bool { a %% p.m == b %% p.m }", buckets),
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { hash(k %% %[1]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a %% %[1]d == b %% %[1]d }", mod),
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { hash(k / %[1]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a / %[1]d == b / %[1]d }", mod),
			"func Hash(_ Mod, k int) uint64 { hash(k) }\n\nfunc Equal(_ Mod, a int, b int) bool { a == b }",
			fmt.Sprintf("func Hash(_ Mod, k int) uint64 { uint64((k %% %[1]d) %% %[2]d) }\n\nfunc Equal(_ Mod, a int, b int) bool { a %% %[1]d == b %% %[1]d }\n\nfunc NormalForm(_ Mod) bool { true }\n\nfunc NormalKey(_ Mod, k int) int { k %% %[1]d }", mod, buckets),
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
				fmt.Fprintf(&body, "\tConsole.Println(show(got(m, %d)))\n", k)
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
		// Kind 0's provider has a field, so it has no default: its Maps
		// are made by MakeMap with the modulus (types.md, Maps 3).
		field, made := "", "MakeMap[int, int, Mod](Mod)"
		if kind == 0 {
			field, made = "\n\tm int,\n", fmt.Sprintf("MakeMap[int, int, Mod](Mod{m: %d})", mod)
		}
		body.WriteString("\t" + mut + "o = " + made + "\n" + same.String() + "\t" + mut + "p = " + made + "\n" + other.String() +
			"\tConsole.Println(\"\\(m == o) \\(m != p)\")\n")
		src := fmt.Sprintf(`type Mod {%[3]s} implements Hashing[int]

%[1]s

func show(o Option[int]) string {
	match o {
		case Some{Value}: "\(Value)"
		case None: "none"
	}
}

func main() needs Console {
	let mut m = %[4]s
%[2]s	let mut sum = 0
	for k, v in m {
		sum = sum + k * 1000 + v
	}
	Console.Println("\(size(m)) \(sum)")
}

func got[H Hashing[int]](m Map[int, int, H], k int) Option[int] { m.Get(k) }

func size[H Hashing[int]](m Map[int, int, H]) int { len(m) }
`, provider, body.String(), field, made)
		for _, lowering := range []string{"", "table"} {
			restore := check.SetMapLowering(lowering)
			pkg := checked(t, src)
			restore()
			form := derivedNormalForm(pkg)
			wantForm := "false"
			switch {
			case kind == 4:
				wantForm = "" // written, not derived
			case kind != 0 && lowering == "":
				wantForm = "true"
			}
			if form != wantForm {
				t.Fatalf("seed %d, lowering %q: the derived NormalForm is %q, want %q\n%s", seed, lowering, form, wantForm, src)
			}
			out, err := RunMain(pkg, DefaultBudget(), Process{})
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, src)
			}
			if out.Skipped != "" || out.Stdout != want.String() {
				t.Fatalf("seed %d, lowering %q: the Map disagrees with the model (skipped %q)\ngot:\n%s\nwant:\n%s\nsource:\n%s\nreplay: go test ./internal/sketch/eval -run TestProviderMapAgrees -providermap.seed %d",
					seed, lowering, out.Skipped, out.Stdout, want.String(), src, seed)
			}
		}
	}
}

// derivedNormalForm is the body of the NormalForm the checker derived in
// pkg, "true" or "false", or "" when it derived none.
func derivedNormalForm(pkg *check.Package) string {
	for _, f := range pkg.Funcs {
		if f.Name != "NormalForm" || f.Decl == nil || f.Decl.Derived == nil || f.Decl.Body == nil || len(f.Decl.Body.List) != 1 {
			continue
		}
		if s, ok := f.Decl.Body.List[0].(*syntax.ExprStmt); ok {
			if id, ok := s.X.(*syntax.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}
