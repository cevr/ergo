package check

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// trustingImporter is std, for a package of module app whose root trusts
// app's Go (ergo.mod `trust app`; docs/spec/interop.md, Trusted Go).
type trustingImporter struct{}

func (trustingImporter) Import(path string) (*Package, error) { return Std.Import(path) }
func (trustingImporter) TrustsGo() (string, bool)             { return "app", true }

// TestTrustedDoorRefusals (STD-NOPRIV NP4e): a trusted package binds a
// runtime/v1 body the runtime's table has, so the lowering always has its
// convention; any other runtime target is S108 with its reason. The open
// door itself is std TestStdCopyAgrees and sketchfront
// TestTrustLineOpensTheRuntimeDoor. Mutants: door-trusted-unknown-body,
// door-trusted-rt.
func TestTrustedDoorRefusals(t *testing.T) {
	for _, c := range []struct{ decl, want string }{
		{"extern go ergo.local/prototype/runtime/v1.NoSuchBody func f(n int) int", "extern go ergo.local/prototype/runtime/v1.NoSuchBody: "},
		{"extern go ergo.local/prototype/runtime/rt.Sleep func f(d duration)", "a trusted package binds a body of ergo.local/prototype/runtime/v1"},
	} {
		f, err := syntax.Parse(c.decl + "\n")
		if err != nil {
			t.Fatal(err)
		}
		_, diags := CheckTest("app/x", []*syntax.File{f}, trustingImporter{}, nil)
		if len(diags) != 1 || diags[0].Code != CodeType || !strings.Contains(diags[0].Message, c.want) {
			t.Errorf("%s: got %v, want one S108 %q", c.decl, diags, c.want)
		}
	}
}

// TestRuntimeTypeDeclarations (STD-NOPRIV NP4c): a type declaration names
// its runtime Go type (docs/spec/stdlib.md, The body contract) in any
// package the root trusts, as std's do, and the checker holds it to the Go
// type's shape; anything else is S108 with its reason. Std's own
// declarations are std TestStdRecordTable. Mutants: runtime-type-std-only,
// runtime-type-untrusted, runtime-type-arity, runtime-type-fields,
// runtime-type-open.
func TestRuntimeTypeDeclarations(t *testing.T) {
	const v1 = "extern go ergo.local/prototype/runtime/v1."
	for _, c := range []struct {
		decl    string
		trusted bool
		want    string // "" for no diagnostic
	}{
		{v1 + "Closed type Closed {}", true, ""},
		{v1 + "Closed type Closed {}", false, "which a package names only when the root module trusts its Go"},
		{v1 + "Closed type Closed[T] {}", true, "Closed has 1 type parameters and v1.Closed has 0"},
		{v1 + "Closed type Closed { Reason string }", true, "is not a struct with those fields"},
		{v1 + "Queue type Queue[T] { Push(v T) }", true, "declares an unexported operation"},
	} {
		f, err := syntax.Parse(c.decl + "\n")
		if err != nil {
			t.Fatal(err)
		}
		var imp Importer = Std
		if c.trusted {
			imp = trustingImporter{}
		}
		pkg, diags := CheckTest("app/x", []*syntax.File{f}, imp, nil)
		if c.want == "" {
			if len(diags) != 0 {
				t.Errorf("%s: %v", c.decl, diags)
			} else if goName, ok := GoTypeOf(pkg.Types[0].Type.(*Named)); !ok || goName != "Closed" {
				t.Errorf("%s: GoTypeOf = %q, %v", c.decl, goName, ok)
			}
			continue
		}
		if len(diags) != 1 || diags[0].Code != CodeType || !strings.Contains(diags[0].Message, c.want) {
			t.Errorf("%s (trusted %v): got %v, want one S108 %q", c.decl, c.trusted, diags, c.want)
		}
	}
}
