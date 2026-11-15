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
// Half the seeds write the append form, ParseX(b []byte) with Wire(x X,
// into []byte) []byte (GX11): its law draws b []byte and calls Wire with
// one argument, the empty buffer. That kills gx11-pair-append-no-law (the
// append form derives no law, as it did in GX10) and
// gx11-pair-append-no-into (x.Wire() with no buffer).
func TestPairLawDraws(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		src, open, appends := pairSource(rand.New(rand.NewSource(seed)))
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
		if appends {
			want = "[]byte"
		}
		if open {
			want = "X"
		}
		draws := typeText(law.Params.List[0].Type)
		if draws != want {
			t.Fatalf("seed %d: %s draws %s, want %s (replay: go test -run TestPairLawDraws ./internal/sketch/syntax, seed %d)\n%s", seed, law.Name.Name, draws, want, seed, src)
		}
		wantArgs := 0
		if appends {
			wantArgs = 1
		}
		Inspect(law.Body, func(n Node) bool {
			if c, ok := n.(*CallExpr); ok {
				if s, ok := c.Fun.(*SelectorExpr); ok && s.Sel.Name == "Wire" && len(c.Args) != wantArgs {
					t.Fatalf("seed %d: %s calls Wire with %d arguments, want %d (replay: go test -run TestPairLawDraws ./internal/sketch/syntax, seed %d)\n%s", seed, law.Name.Name, len(c.Args), wantArgs, seed, src)
				}
			}
			return true
		})
	}
}

// pairSource is a file with a by-name pair over X, a record or a union of
// inline and named variants, and whether the reference model calls X open:
// every field of X's records named and exported, every named variant a
// record of the file.
func pairSource(r *rand.Rand) (string, bool, bool) {
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
	if r.Intn(2) == 0 {
		b.WriteString("func ParseX(b []byte) X fails Bad { return Bad{Name: \"x\"} }\n")
		b.WriteString("func Wire(x X, into []byte) []byte { into }\n")
		return b.String(), open, true
	}
	b.WriteString("func ParseX(s string) X fails Bad { return Bad{Name: s} }\n")
	b.WriteString("func Wire(x X) string { \"x\" }\n")
	return b.String(), open, false
}

// typeText is the text of an identifier or a slice type of one.
func typeText(t Expr) string {
	switch t := t.(type) {
	case *Ident:
		return t.Name
	case *ArrayType:
		return "[" + t.Len + "]" + typeText(t.Elt)
	}
	return fmt.Sprintf("%T", t)
}
