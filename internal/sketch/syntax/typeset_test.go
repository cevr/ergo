package syntax

import (
	"strings"
	"testing"
)

// TestTypeSetAndHashingParse is the smallest repro of the CONSTRAINTS
// syntax that the self-host parser lacked once the Go parser was deleted
// (main 30ccad64e): the type-set member of an operation type, `~int |
// ~int64` (C1), and a Map's third type argument, its Hashing provider (C5).
// Each row is one boundary of grammar.md, Declarations 4, as the decoded
// tree shows it; TestDecodeAgreesWithView and TestFormatParseRoundTrip
// hold the two readings and the formatter to the same forms.
// Mutants it alone catches (compiler/syntax/testdata/mutants.json):
// typeset-tilde-illegal, typeset-never-starts, typeset-comma-op-is-field,
// typeset-variant-admitted, map-hashing-not-a-map.
func TestTypeSetAndHashingParse(t *testing.T) {
	spec := func(t *testing.T, src string) Expr {
		t.Helper()
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		return f.Decls[0].(*TypeDecl).Spec
	}
	terms := func(t *testing.T, src string) *InterfaceType {
		t.Helper()
		it, ok := spec(t, src).(*InterfaceType)
		if !ok {
			t.Fatalf("%q is %T, want an operation type", src, spec(t, src))
		}
		return it
	}

	// `~T | ~U`: two terms, each with its tilde; a bare term has none.
	it := terms(t, "type Count { ~int | ~int64 }\n")
	if len(it.Terms) != 2 || !it.Terms[0].Tilde.IsValid() || !it.Terms[1].Tilde.IsValid() {
		t.Fatalf("Count's terms %+v, want two ~ terms", it.Terms)
	}
	it = terms(t, "type K { int | pkg.T, Label() string }\n")
	if len(it.Terms) != 2 || it.Terms[0].Tilde.IsValid() || len(it.Methods) != 1 {
		t.Fatalf("K: terms %+v, methods %d; want two bare terms and one operation", it.Terms, len(it.Methods))
	}
	// `T, Name()` is a term and an operation; `X, Y (A, B)` stays a group
	// of two tuple fields.
	it = terms(t, "type K { comparable, Label() string }\n")
	if len(it.Terms) != 1 || len(it.Methods) != 1 {
		t.Fatalf("K { comparable, Label() string }: terms %d, methods %d; want 1 and 1", len(it.Terms), len(it.Methods))
	}
	if st, ok := spec(t, "type S { X, Y (int64, int64) }\n").(*StructType); !ok || len(st.Fields.List) != 1 || len(st.Fields.List[0].Names) != 2 {
		t.Fatalf("S is %T, want a record of one group of two tuple fields", spec(t, "type S { X, Y (int64, int64) }\n"))
	}

	// The parser's refusals.
	for _, row := range []struct{ src, want string }{
		{"type Two { ~int, ~string }\n", "an operation type has one type-set member; join the terms with |"},
		{"type V = A{~int} | B\n", "a variant holds fields only"},
		{"type R { x int, T }\n", "Ergo has no embedding"},
	} {
		_, err := Parse(row.src)
		if err == nil || !strings.Contains(err.Error(), row.want) {
			t.Errorf("%q: got %v, want %q", row.src, err, row.want)
		}
	}

	// Map[K, V, H] is a MapType with its Hashing; Map[K, V] has none.
	field := func(src string) Expr { return spec(t, src).(*StructType).Fields.List[0].Type }
	if m, ok := field("type H { m Map[string, int, Fnv] }\n").(*MapType); !ok || m.Hashing == nil {
		t.Fatalf("Map[string, int, Fnv] is %T, want a MapType with its Hashing", field("type H { m Map[string, int, Fnv] }\n"))
	}
	if m, ok := field("type H { m Map[string, int] }\n").(*MapType); !ok || m.Hashing != nil {
		t.Fatalf("Map[string, int] is %T, want a MapType with no Hashing", field("type H { m Map[string, int] }\n"))
	}
}
