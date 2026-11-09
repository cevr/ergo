package lower

import (
	"strings"
	"testing"
)

// importedContextLib is package lib of TestRunImportedContext: W binds Store
// to a resource (Conn has a Close) that open makes fresh, and Log to Quiet;
// Deep embeds W in lib itself.
const importedContextLib = `type Store { Get() string }
type Log { Say(s string) }
type Conn { n int }

func Get(c Conn) string { "lib \(c.n)" }

func Close(c Conn) needs Console {
	Console.Println("closed \(c.n)")
}

func open(n int) Conn { Conn{n: n} }

type Quiet {}

func Say(_ Quiet, s string) {}

let W = context { Log: Quiet, Store: open(1) }

let Deep = context { W }

func Peek() string needs Console {
	with W { Store.Get() }
}
`

// TestRunImportedContext (196b U4): a with of an imported context, an
// inline, a named and a local context that embed one, and a replace over
// an imported entry lower and run as a context of the root package does.
// Each with builds lib.W's Store provider, a resource, in its own scope
// and closes it once when its block ends; the replace builds Mine in its
// place and closes nothing.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestRunImportedContext(t *testing.T) {
	needsBuild(t)
	lib := checkPackage(t, "lib", []pkgSource{{"/ergo-test/lib/lib.ergo", importedContextLib}}, nil)
	imp := pkgImporter{"lib": lib.Package}
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/ctx/main.ergo", `import lib

type Mine {}

func Get(_ Mine) string { "mine" }

let Over = context { lib.W, replace lib.Store: Mine }

let Plain = context { lib.Deep }

func main() needs Console {
	Console.Println(with lib.W { lib.Store.Get() })
	Console.Println(with lib.Deep { lib.Store.Get() })
	Console.Println(with context { lib.W } { lib.Store.Get() })
	Console.Println(with Over { lib.Store.Get() })
	Console.Println(with Plain { lib.Store.Get() })
	let x = context { lib.W }
	Console.Println(with x { lib.Store.Get() })
	with lib.W, context { lib.Deep } {
		Console.Println("nested \(lib.Store.Get())")
	}
	Console.Println(lib.Peek())
}
`}}, imp)
	main.Imports = []Unit{lib}
	o := mustLowerUnit(t, "ctx_imported", main, false)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runExe(t, exes[o.name])
	want := strings.Join([]string{
		"closed 1", "lib 1", // with lib.W
		"closed 1", "lib 1", // with lib.Deep
		"closed 1", "lib 1", // with context { lib.W }
		"mine",              // with Over: the replace builds Mine, which has no Close
		"closed 1", "lib 1", // with Plain
		"closed 1", "lib 1", // with x
		// with lib.W, context { lib.Deep }: the inner operand binds Store,
		// so the outer one acquires nothing (With.Acquired) and one Conn
		// is built and closed.
		"nested lib 1", "closed 1",
		"closed 1", "lib 1", // lib.Peek: a with of lib's own context, in lib
	}, "\n") + "\n"
	if code != 0 || stdout != want {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, want, stderr)
	}
}
