package check

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestProviderRuntimeField: a trusted module's field type that names a
// runtime Go type without the recording contract, v1.NewV and v1.V.Call
// for a V of two type parameters, is refused at the derive (patterns.md,
// Provider plans 3). Mutant: the runtime field type's contract never
// checked (provider-field-unchecked: the derive is admitted, and the
// lowering would call a Go function that does not exist).
func TestProviderRuntimeField(t *testing.T) {
	src := "extern go ergo.local/prototype/runtime/v1.Ref type R[A, G] {\n\truntimeOnly(),\n}\n\ntype E {}\n\ntype P[S] derive Provider(Of[S](), Of[R](), Of[E]())\n"
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	_, diags := CheckTest("app/x", []*syntax.File{f}, trustingImporter{}, nil)
	for _, d := range diags {
		if d.Code == CodeType && d.Line == 7 && strings.Contains(d.Message, "the Provider derive of P: its field type's Go type v1.Ref") {
			return
		}
	}
	t.Fatalf("want S108 at line 7, the field type's Go type v1.Ref refused: %v", diags)
}
