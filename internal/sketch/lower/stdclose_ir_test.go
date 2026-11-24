package lower

import (
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// stdCloseSrc closes a value whose type binds a runtime Go type (fs's
// Mapped) in a function of its own (size) and through std's io.File,
// whose body closes its mapping.
const stdCloseSrc = `import fs
import io

func size(h fs.Host, path string) int fails fs.NotFound | fs.Denied | fs.Failed {
	let m = try fs.MapFile(h, path)
	let t = fs.Clone(m.Text())
	m.Close()
	len(t)
}

func main() needs Console | Ambient {
	let h = fs.NewHost()
	let s = size(h, "f") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		0 - 1
	}
	let _ = io.File(h, "f", 64) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return
	}
	Console.Println("\(s)")
}
`

// TestStdCloseTakesTheIR is the in-process half of the IR leg of a
// checked Close on a runtime-bound type: the Close is the bound Go
// method, called with no poll before it (the emitter's runtimeCall and
// entersRelease), so the functions that call it have IR bodies and no
// body falls back on "std Close". The decision reads the binding's
// declared waiting policy, never the type's package. The program runs on
// both paths in TestIRReprosMatchSource/stdclose (irspike).
//
// Mutant (testdata/mutants.json): ir-std-close-refused.
func TestStdCloseTakesTheIR(t *testing.T) {
	o := mustLower(t, "stdclose", stdCloseSrc)
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: stdCloseSrc}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if n := rep.Fallback["std Close"]; n != 0 {
		t.Errorf("%d bodies keep their source body for \"std Close\" (fallbacks %v)", n, rep.Fallback)
	}
	for _, name := range []string{"size", "io__File"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (fallbacks %v; IR %v)", name, rep.Fallback, rep.IR)
		}
	}
}
