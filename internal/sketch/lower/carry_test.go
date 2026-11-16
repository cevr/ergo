package lower

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestZoneDataCarried is S-tz's acceptance (interop.md, Checking 6 and
// Lowering 12; stdlib.md, Build-time assets 5): `import go time/tzdata` in
// the package of main lowers to Go's blank import `_ "time/tzdata"` in the
// emitted main.go. That import is the one promised fact: Go's time/tzdata
// package documents that a program which imports it carries Go's zone
// database, which time.LoadLocation reads after the host's own sources.
// The shape is the spec (Lowering 12), so the test checks that import and
// nothing else of the lowered Go, in process. It catches the mutant
// stz-carry-not-imported (module.go drops the blank import), which no other
// test catches: the checker rows see Info.Carries, not the emitted file.
func TestZoneDataCarried(t *testing.T) {
	const src = `import go time
import go time/tzdata
func main() needs Console | go.Foreign {
	let loc = time.LoadLocation("America/Toronto") catch {
	case go.Error as e:
		Console.Println("no zone: " + e.Text)
		return
	}
	Console.Println("zone " + loc.String())
}
`
	o := mustLower(t, "zonecarried", src)
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range f.Imports {
		if spec.Path.Value == `"time/tzdata"` && spec.Name != nil && spec.Name.Name == "_" {
			return
		}
	}
	var got []string
	for _, spec := range f.Imports {
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name + " "
		}
		got = append(got, name+spec.Path.Value)
	}
	t.Errorf("the emitted main.go has no blank import of time/tzdata; its imports: %s", strings.Join(got, ", "))
}
