package lower

import (
	"strconv"
	"strings"
	"testing"
)

// traceVariantLib declares a machine whose state and signal variants share
// their names with declarations of the program that imports it, so the
// lowering names their Go types Idle_1, Go_1 and Box_1: the root package's
// declarations are named first (naming.go). St holds an instance of a
// generic record over a suffixed type, so it lowers boxed (an
// interface); Sg lowers flat.
const traceVariantLib = `type Idle { N int }

type Busy {}

type Box[T] { V T }

type St = Idle | Busy | Box[Idle]

type Go {}

type Halt {}

type Sg = Go | Halt

func step(s St, e Sg) Decision[St, int] {
	match (s, e) {
	case (_, _):
		Ignore
	}
}

type M machine[St, Sg] { initial: Idle{N: 1}, step: step }
`

const traceVariantMain = `import actor
import lib
import time

type Idle { X int }

type Go { Y int }

type Box[T] { W T }

func main() {
	let _ = Idle{X: 1}
	let _ = Go{Y: 2}
	let _ = Box[int]{W: 3}
	with context { Scheduler: time.NewManual(0) } {
		let a = actor.StartMachine(lib.M{}, 0, func(o int) {})
		a.Stop()
	}
}
`

// TestTracedVariantsHaveTheirErgoNames (TRACE-VARIANT-SUFFIX): a traced
// runner names a state's or a signal's variant by its Ergo name, never by
// its Go type's name with the suffix the lowering added to keep Go names
// unique, as the head or as a type argument. Every name the lowering
// writes for a runner is the Ergo name (a machine graph's arms, a flat
// union's ErgoVariant), and a program that starts a machine actor
// registers its suffixed names, by which the runtime names a value no
// graph names (a boxed variant, an actor of a shared pair, a generic flat
// union's). The lowered Go type-checks. Mutants (testdata/mutants.json):
// trace-variant-go-name, trace-variant-no-names-table.
func TestTracedVariantsHaveTheirErgoNames(t *testing.T) {
	lib := checkPackage(t, "lib", []pkgSource{{"/ergo-test/lib/lib.ergo", traceVariantLib}}, nil)
	u := checkPackage(t, "main", []pkgSource{{"/ergo-test/trace/main.ergo", traceVariantMain}}, pkgImporter{"lib": lib.Package})
	u.Imports = []Unit{lib}
	code, err := newLowerer(u).program()
	if err != nil {
		t.Fatal(err)
	}
	text := string(code)
	// The table: one line, which holds std's suffixed names too.
	_, table, _ := strings.Cut(text, `.RegisterTypeNames("main", `)
	table, _, _ = strings.Cut(table, "\n")
	for _, n := range []string{"Box", "Go", "Idle"} {
		pair := strconv.Quote(goOf("lib", n)) + ", " + strconv.Quote(n)
		if !strings.Contains(table, pair) {
			t.Errorf("the program registers no %s:\n%s", pair, text)
		}
	}
	names := strings.Replace(text, table, "", 1)
	for _, goName := range []string{goOf("lib", "Idle"), goOf("lib", "Go"), goOf("lib", "Box")} {
		if !strings.Contains(text, "type "+goName) {
			t.Fatalf("the lib's variant is not the Go type %s, so the case is not tested:\n%s", goName, text)
		}
		if strings.Contains(names, strconv.Quote(goName)) || strings.Contains(names, "main."+goName+`]"`) {
			t.Errorf("the lowering names a variant by %q, its Go name, not its Ergo name", goName)
		}
	}
	if !strings.Contains(text, `"Box[main.Idle]"`) {
		t.Errorf("the graph does not name the variant Box[main.Idle]:\n%s", text)
	}
	typecheckGo(t, []*lowered{{name: "tracevariant", src: traceVariantMain, files: moduleFiles(u.Go, code)}})
}
