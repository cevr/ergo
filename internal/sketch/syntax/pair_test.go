package syntax

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// TestPairLawDraws: the round-trip law of a by-name pair draws x itself for
// an open type, so it reaches every value, and a drawn b through ParseX
// for a type only its package builds (types.md, Codecs 7). It is a seeded
// property against a reference model of "open": X is a record or a union
// whose variants, named records of the file or inline ones, have only
// named exported fields. A failing seed prints the source and the replay.
//
// It kills the mutant l11b-s1 (openType always false), which every law
// still passes: the b form over random names is vacuous for a union of
// four levels; and l11b-inline-variant-closed (an inline variant is never
// open: batch-15 L11b M1, where a broken Wire of Low{} | High{} passed).
func TestPairLawDraws(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		src, open := pairSource(rand.New(rand.NewSource(seed)))
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		var law *LawDecl
		for _, d := range f.Decls {
			if l, ok := d.(*LawDecl); ok && l.Pair != nil {
				law = l
			}
		}
		if law == nil {
			t.Fatalf("seed %d: no round-trip law derived from\n%s", seed, src)
		}
		want := "string"
		if open {
			want = "X"
		}
		if id, ok := law.Params.List[0].Type.(*Ident); !ok || id.Name != want {
			t.Fatalf("seed %d: %s draws %v, want %s (replay: go test -run TestPairLawDraws ./internal/sketch/syntax, seed %d)\n%s", seed, law.Name.Name, law.Params.List[0].Type, want, seed, src)
		}
	}
}

// pairSource is a file with a by-name pair over X, a record or a union of
// inline and named variants, and whether the reference model calls X open:
// every field of X's records named and exported, every named variant a
// record of the file.
func pairSource(r *rand.Rand) (string, bool) {
	var b strings.Builder
	open := true
	fields := func() string {
		var fs []string
		for i := range r.Intn(3) {
			name := fmt.Sprintf("F%d", i)
			if r.Intn(4) == 0 {
				name = fmt.Sprintf("f%d", i)
				open = false
			}
			fs = append(fs, name+" int")
		}
		return "{" + strings.Join(fs, ", ") + "}"
	}
	if r.Intn(4) == 0 {
		b.WriteString("type X " + fields() + "\n")
	} else {
		var variants, decls []string
		for i := range 1 + r.Intn(3) {
			name := fmt.Sprintf("V%d", i)
			switch r.Intn(3) {
			case 0:
				variants = append(variants, name+fields())
			case 1:
				variants = append(variants, name)
				decls = append(decls, "type "+name+" "+fields()+"\n")
			default:
				// Declared in another file: not drawn.
				variants = append(variants, name)
				open = false
			}
		}
		if len(variants) == 1 {
			variants = append(variants, "W{}")
		}
		b.WriteString("type X = " + strings.Join(variants, " | ") + "\n")
		b.WriteString(strings.Join(decls, ""))
	}
	b.WriteString("type Bad { Name string }\n")
	b.WriteString("func ParseX(s string) X fails Bad { return Bad{Name: s} }\n")
	b.WriteString("func Wire(x X) string { \"x\" }\n")
	return b.String(), open
}
