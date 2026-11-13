package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// trustingImporter is std, for a root package of module app whose ergo.mod
// trusts app (docs/spec/interop.md, Trusted Go).
type trustingImporter struct{}

func (trustingImporter) Import(path string) (*check.Package, error) { return check.Std.Import(path) }
func (trustingImporter) TrustsGo() (string, bool)                   { return "app", true }

// TestTrustedRootBindsRuntime (STD-NOPRIV NP4e): the root package of a
// trusted module binds runtime/v1 bodies that take hidden arguments (the
// frame of OnExit; the task and the context package of ContextBound), and
// the lowering calls them as it calls std's, so the Go type-checks. An
// imported package's binding is std TestStdCopyAgrees. Mutant:
// stdbody-std-only (stdBody keys on std membership for a root package).
func TestTrustedRootBindsRuntime(t *testing.T) {
	const src = `extern go ergo.local/prototype/runtime/v1.ContextBound func contextBound(key string) bool
extern go ergo.local/prototype/runtime/v1.OnExit func onExit[R](body func() needs R) needs R

func main() needs Console {
	onExit(func() needs Console { Console.Println("bye") })
	Console.Println(if contextBound("k") { "bound" } else { "free" })
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "/ergo-test/door/main.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, trustingImporter{})
	if len(ds) != 0 {
		t.Fatalf("checker diagnostics %v\n%s", ds, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: f.Name, Text: src}}}
	typecheckGo(t, []*lowered{mustLowerUnit(t, "door", u, false)})
}
