package check

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
)

func TestSelfRefusals(t *testing.T) {
	for _, tc := range []struct{ file, code, text string }{
		{"self-claim-concrete", CodeProvider, "Bytes cannot claim Mappable[byte]: Mappable re-applies Self, and byte is not a type parameter of Bytes"},
		{"self-claim-bounds", CodeProvider, "Set cannot claim Mappable[T]: Map's U is unconstrained, and Set's T is ordered"},
		{"self-outside", CodeUnknown, "unknown type Self; Self names the claiming type, inside an operation type"},
		{"self-parameter-no-reapply", CodeType, "M is not a generic type; only a parameter constrained by an operation type that re-applies Self is re-applied"},
		{"self-reapp-no-inference", CodeType, "cannot infer M; write the type arguments"},
		{"self-not-value", CodeType, "Clonable names Self, so it is a constraint and has no values"},
		{"self-include-claim-concrete", CodeProvider, "Bytes cannot claim Chainable[byte]: Chainable re-applies Self, and byte is not a type parameter of Bytes"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			src, err := os.ReadFile("testdata/fixtures/negative/" + tc.file + ".ergo")
			if err != nil {
				t.Fatal(err)
			}
			_, ds := CheckSource(string(src))
			for _, d := range ds {
				if d.Code == tc.code && strings.Contains(d.Message, tc.text) {
					return
				}
			}
			t.Fatalf("want %s %q; got %v", tc.code, tc.text, ds)
		})
	}
}

func TestSubstSelfAgreesWithPositions(t *testing.T) {
	const seed = 181
	r := rand.New(rand.NewPCG(seed, seed))
	for i := 0; i < 32; i++ {
		ps := []string{"A", "B", "C"}
		r.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
		claim, fixed := ps[0], ps[1:]
		src := fmt.Sprintf(`type Mappable[T] {
	Map[U](f func(x T) U) Self[U],
	law Keep[U](x Self, y Self[U], from T, to U, other Box[int, bool, string]) { true },
}
type Box[A, B, C] { Value %s } implements Mappable[%s]
func Map[%s, %s, %s, U](b Box[A, B, C], f func(x %s) U) %s { %s{Value: f(b.Value)} }
func Draw(b Box[int, bool, int]) {}
`, claim, claim, fixed[0], fixed[1], claim, claim, replaceArg(claim, "U"), replaceArg(claim, "U"))
		// Use the independently generated field/return parameter order.
		// The declaration always orders A, B, C; the shuffled claim is one
		// chosen position, and the provider brackets are shuffled too.
		c, ds := checkForInfer(src)
		if len(ds) != 0 {
			t.Fatalf("seed %d case %d: %v\n%s", seed, i, ds, src)
		}
		b := c.pkg.Funcs[len(c.pkg.Funcs)-1].Sig.Params[0].Type.(*Named)
		law := c.pkg.Laws[0]
		read, why := SubstSelf(law, b)
		if why != "" {
			t.Fatalf("seed %d case %d: %s\n%s", seed, i, why, src)
		}
		pos := int(claim[0] - 'A')
		// Equal concrete arguments at different positions must not decide
		// which parameter the claim names.
		args := []Type{Typ[Int], Typ[Bool], Typ[Int]}
		from := args[pos]
		u := law.TParams[0]
		args[pos] = u
		want := []Type{b, c.instantiate(b.origin(), args), from, u, law.Params[4].Type}
		for j, p := range law.Params {
			got := read(p.Type)
			if !c.identical(got, want[j]) || (j == 0 || j == 4) && got != want[j] {
				t.Fatalf("seed %d case %d param %s: got %v (%p), want %v (%p)\n%s\nreplay: go test -run TestSubstSelfAgreesWithPositions ./internal/sketch/check", seed, i, p.Name, got, got, want[j], want[j], src)
			}
		}
	}
}

func replaceArg(p, arg string) string {
	ts := []string{"A", "B", "C"}
	ts[p[0]-'A'] = arg
	return "Box[" + strings.Join(ts, ", ") + "]"
}
