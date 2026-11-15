package check_test

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestPairLawDrawsOwnValues: the round-trip law of a by-name pair runs
// only when X has its own generator, the values its package makes: its
// constructor NewX, or any value of a record or union whose fields every
// package may write (types.md, Codecs 7; GX11). Otherwise the law does not
// run and says why. It is a seeded property against a reference model of
// "own generator" over records, unions of inline variants and defined
// basic types, with and without NewX, in both pair forms, for an exported X
// and an unexported x, whose constructor is newX (GX12b: it was Newx). A
// failing seed prints the source and the replay.
//
// It kills gx11-pair-own-generator-ignored (the checker runs every pair
// law whose X is generatable at all, so a record of an unexported field is
// drawn field by field, values its ParseX may refuse) and
// gx11-pair-ctor-ignored (a closed X with NewX is not run), and
// gx12b-ctor-name-new-always (an unexported x's constructor named Newx).
func TestPairLawDrawsOwnValues(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		src, own := pairLawSource(r)
		pkg, ds := check.CheckSource(src)
		if len(ds) != 0 {
			t.Fatalf("seed %d: %v\n%s", seed, ds, src)
		}
		var law *check.Law
		for _, l := range pkg.Laws {
			if l.Decl.Pair != nil {
				law = l
			}
		}
		if law == nil {
			t.Fatalf("seed %d: no round-trip law derived from\n%s", seed, src)
		}
		if (law.Unrun == "") != own {
			t.Fatalf("seed %d: the model says X has its own generator: %t; the law's Unrun is %q\n%s\nreplay: go test ./internal/sketch/check -run '^TestPairLawDrawsOwnValues$'", seed, own, law.Unrun, src)
		}
	}
}

// pairLawSource is a file with a by-name pair over X and whether X has
// its own generator in the reference model: a NewX, or a record or union
// whose fields are all exported.
func pairLawSource(r *rand.Rand) (string, bool) {
	var b strings.Builder
	open := true
	fields := func() string {
		var fs []string
		for i := range r.IntN(3) {
			name := fmt.Sprintf("F%d", i)
			if r.IntN(3) == 0 {
				name = fmt.Sprintf("f%d", i)
				open = false
			}
			fs = append(fs, name+" int")
		}
		return strings.Join(fs, ", ")
	}
	var ctor string
	switch r.IntN(3) {
	case 0:
		fs := fields()
		fmt.Fprintf(&b, "type X { %s }\n", fs)
		ctor = strings.TrimSuffix("X{"+strings.ReplaceAll(fs, " int", ": n")+"}", "{}")
	case 1:
		variant := func(name string) string {
			fs := fields()
			if fs == "" {
				return name + "{}"
			}
			return name + "{ " + fs + " }"
		}
		first := variant("V0")
		fmt.Fprintf(&b, "type X = %s | %s\n", first, variant("V1"))
		// A value of a variant with no fields is written bare (types.md,
		// Structs 2).
		ctor = strings.TrimSuffix(strings.ReplaceAll(first, " int", ": n"), "{}")
	default:
		b.WriteString("type X string\n")
		ctor = `X("x")`
		open = false
	}
	withCtor := r.IntN(2) == 0
	if withCtor {
		fmt.Fprintf(&b, "func NewX(n int) X { %s }\n", ctor)
	}
	if r.IntN(2) == 0 {
		b.WriteString("func ParseX(b []byte) X { Unreachable(\"not run\") }\n")
		b.WriteString("func Wire(v X, into []byte) []byte { into }\n")
	} else {
		b.WriteString("func ParseX(s string) X { Unreachable(\"not run\") }\n")
		b.WriteString("func Wire(v X) string { \"\" }\n")
	}
	src := b.String()
	if r.IntN(2) == 0 {
		// An unexported type: its parse is parseX and its constructor
		// newX, each named as ParseName names the parse.
		src = strings.NewReplacer("NewX(", "newX(", "ParseX(", "parseX(").Replace(src)
		src = regexp.MustCompile(`\bX\b`).ReplaceAllString(src, "x")
	}
	return src, open || withCtor
}

// TestPairLawUnrunNamesConstructor: the reason a pair's law does not run
// names the constructor to declare, newHost for an unexported host (the
// smallest repro of the GX12b bug: the rule named it Newhost).
func TestPairLawUnrunNamesConstructor(t *testing.T) {
	src := "type host { name string }\nfunc parseHost(s string) host { host{name: s} }\nfunc Wire(h host) string { h.name }\n"
	pkg, ds := check.CheckSource(src)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	for _, l := range pkg.Laws {
		if l.Decl.Pair != nil && !strings.Contains(l.Unrun, "declare newHost,") {
			t.Fatalf("the law's reason is %q, want it to name newHost", l.Unrun)
		}
	}
}
