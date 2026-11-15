package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

var reachSeed = flag.Uint64("reach.seed", 0, "replay one seed of TestReachLowersNoUnreachableDeclaration")

// reachBases are the programs the property adds unreachable declarations
// to. Each reaches its declarations by one edge kind of reach.go: a call,
// a dot call (196m), a function value, a let read, a provider bound in a
// named context, a machine run by a fold, a generic call with a witness,
// and a declaration of an imported package.
var reachBases = []string{
	"func main() needs Console {\n\tConsole.Println(\"hello\")\n}\n",
	`type Point { x int, y int }

func sum(p Point) int { p.x + p.y }

func twice(n int) int { n * 2 }

let origin = Point{x: 1, y: 2}

func apply(f func(n int) int, n int) int { f(n) }

func main() needs Console {
	Console.Println("\(origin.sum()) \(apply(twice, 3))")
}
`,
	`type Store { Get() string }

type Holder { V string }

func Get(h Holder) string { h.V }

let Base = context {
	Store: Holder{V: "base"},
}

func show() needs Console | Store {
	Console.Println(Store.Get())
}

func main() needs Console {
	with Base {
		show()
	}
}
`,
	`import machines

type Idle { n int }
type Busy {}
type St = Idle | Busy
type Tick {}
type Halt {}
type Sig = Tick | Halt

func step(s St, e Sig) Decision[St, int] {
	match s {
	case Idle as a:
		Emit[St, int]{Next: Idle{n: a.n + 1}, Output: a.n}
	case Busy:
		Ignore
	}
}

type Loop machine[St, Sig] {
	initial: Idle{n: 0},
	step:    step,
}

func main() needs Console {
	let o = machines.Run(Loop{}, []Sig{Tick, Tick}) catch {
	case machines.Rejected:
		Unreachable("step never rejects")
	}
	match o {
	case machines.Finished[St, int]:
		Console.Println("finished")
	case machines.Running[St, int] as r:
		Console.Println("\(len(r.Emitted))")
	}
}
`,
	`type Sized { Size() int }

type Box { n int }

func Size(b Box) int { b.n }

func total[T Sized](xs []T) int {
	let mut n = 0
	for x in xs {
		n = n + x.Size()
	}
	n
}

func main() needs Console {
	Console.Println("\(total([]Box{Box{n: 1}, Box{n: 2}}))")
}
`,
	`type Count { n int }

func Add(c *Count, k int) { c.n = c.n + k }

type Pair[T] { a T, b T }

func Left[T](p Pair[T]) T { p.a }

func main() needs Console {
	let mut c = Count{n: 1}
	c.Add(2)
	let p = Pair[string]{a: "l", b: "r"}
	Console.Println("\(c.n) \(p.Left())")
}
`,
	`type Map[K, V] { keys []K, vals []V }

func size[K, V](m Map[K, V]) int { len(m.keys) }

func main() needs Console {
	let m = Map[string, bool]{keys: []string{"a"}, vals: []bool{true}}
	Console.Println("\(m.size())")
}
`,
}

// reachAdditions are the kinds of declaration that nothing reaches; %d is
// the declaration's index. Each holds the marker "mark" in its names and
// in a string literal.
var reachAdditions = []string{
	"func markFn%[1]d() string { \"mark-fn-%[1]d\" }\n",
	"type MarkRec%[1]d { N int }\n\nfunc markLabel%[1]d(r MarkRec%[1]d) string { \"mark-method-%[1]d\" }\n",
	"let markLet%[1]d = \"mark-let-%[1]d\"\n",
	"let markCall%[1]d = len(\"mark-call-%[1]d\") + markHelp%[1]d()\n\nfunc markHelp%[1]d() int { 1 }\n",
	"type MarkOps%[1]d { MarkRead%[1]d(n string) int }\n\ntype MarkProv%[1]d {}\n\nfunc MarkRead%[1]d(_ MarkProv%[1]d, n string) int { len(\"mark-provider-%[1]d\" + n) }\n\nlet MarkCtx%[1]d = context {\n\tMarkOps%[1]d: MarkProv%[1]d,\n}\n",
	"type MarkIdle%[1]d { tag string }\ntype MarkStop%[1]d {}\ntype MarkSt%[1]d = MarkIdle%[1]d | MarkStop%[1]d\ntype MarkGo%[1]d {}\ntype MarkHalt%[1]d {}\ntype MarkSig%[1]d = MarkGo%[1]d | MarkHalt%[1]d\n\nfunc markStep%[1]d(s MarkSt%[1]d, e MarkSig%[1]d) Decision[MarkSt%[1]d, int] { Ignore }\n\ntype MarkLoop%[1]d machine[MarkSt%[1]d, MarkSig%[1]d] {\n\tinitial: MarkIdle%[1]d{tag: \"mark-machine-%[1]d\"},\n\tstep:    markStep%[1]d,\n}\n",
}

// reachLib is an imported package whose Used the import base calls, and
// Used reads the let Greeting; its other declarations and let are
// unreachable.
const reachLib = `func greet(s string) string { s }

let Greeting = greet("lib")

func Used() string { Greeting }

func markLibFn() string { "mark-lib-fn" }

let markLibLet = markLibLen("mark-lib-let")

func markLibLen(s string) int { len(s) }
`

const reachImportBase = "import lib\n\nfunc main() needs Console {\n\tConsole.Println(lib.Used())\n}\n"

var markText = regexp.MustCompile(`(?i)mark`)

// TestReachLowersNoUnreachableDeclaration is the reachability property
// (binsize BS5; plan design (a) and (f)). For each seed it takes a base
// program and adds, in a second file, declarations that nothing reaches,
// each with a marker in its names and in a string literal: a function, a
// record with a method, a let with a literal, a let whose initializer
// calls a function, an operation type with a provider and a named context,
// and a machine type; or it imports a package with unreachable
// declarations. Under the reach lowering the emitted main.go holds no
// marker, equals the base's own main.go byte for byte, and type-checks
// (a declaration the reachable set missed has no Go declaration);
// under the reference lowering ("all") every marker is emitted, so the
// additions are real. A failing seed prints the Ergo source, both outputs
// and the replay command.
// Mutants (testdata/mutants.json): bs5-edge-uses, bs5-edge-selector,
// bs5-edge-withs, bs5-edge-types, bs5-edge-firsts, bs5-firsts-pointer, bs5-main-root,
// bs5-root-all, bs5-lib-lets-all.
func TestReachLowersNoUnreachableDeclaration(t *testing.T) {
	seeds := []uint64{*reachSeed}
	if *reachSeed == 0 {
		seeds = seeds[:0]
		for s := uint64(1); s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	var programs []*lowered
	for _, seed := range seeds {
		rng := rand.New(rand.NewPCG(seed, 0))
		var base, extra string
		var imports bool
		if rng.IntN(6) == 0 {
			base, imports = reachImportBase, true
		} else {
			base = reachBases[rng.IntN(len(reachBases))]
			var b strings.Builder
			for i, k := 0, 1+rng.IntN(len(reachAdditions)); i < k; i++ {
				fmt.Fprintf(&b, reachAdditions[rng.IntN(len(reachAdditions))]+"\n", i)
			}
			extra = b.String()
		}
		replay := fmt.Sprintf("go test ./internal/sketch/lower -run TestReachLowersNoUnreachableDeclaration -reach.seed %d", seed)
		lowerWith := func(withExtra bool, mode string) string {
			srcs := []pkgSource{{"/ergo-test/reach/main.ergo", base}}
			if withExtra && extra != "" {
				srcs = append(srcs, pkgSource{"/ergo-test/reach/more.ergo", extra})
			}
			var imp pkgImporter
			var lib Unit
			if imports {
				lib = checkPackage(t, "lib", []pkgSource{{"/ergo-test/lib/lib.ergo", reachLib}}, nil)
				imp = pkgImporter{"lib": lib.Package}
			}
			u := checkPackage(t, "main", srcs, imp)
			if imports {
				u.Imports = []Unit{lib}
			}
			saved := reachLowering
			reachLowering = mode
			defer func() { reachLowering = saved }()
			l := newLowerer(u)
			code, err := l.program()
			if err != nil {
				t.Fatalf("seed %d (%s lowering): %v\nsource:\n%s\n%s\nreplay: %s", seed, mode, err, base, extra, replay)
			}
			if withExtra && mode == "" {
				programs = append(programs, &lowered{name: fmt.Sprintf("reach%d", seed), src: base + extra, files: moduleFiles(u.Go, code)})
			}
			return string(code)
		}
		reached := lowerWith(true, "")
		alone := lowerWith(false, "")
		all := lowerWith(true, "all")
		fail := func(what string) {
			t.Errorf("seed %d: %s\nsource:\n%s\n// more.ergo\n%s\nreach main.go:\n%s\nall main.go:\n%s\nreplay: %s", seed, what, base, extra, reached, all, replay)
		}
		switch {
		case markText.MatchString(reached):
			fail("the reach lowering emits a marker: " + markText.FindString(reached))
		case reached != alone:
			fail("the reach lowering of base + additions differs from the base's:\n" + alone)
		case !strings.Contains(all, "mark-"):
			fail("the reference lowering emits no marker: the additions are not declarations")
		}
	}
	typecheckGo(t, programs)
}
