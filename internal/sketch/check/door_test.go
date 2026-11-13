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
