package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Check-v1 stage E6b: the forms stage E owned that the lowering still
// refused land lowered (C45, a generic context protocol function), and a
// std type with no runtime form is an internal error.

// C45: a value of a type defined over a tuple destructures through its
// underlying tuple, in a local let (letPattern), a package let (decls.go)
// and a tuple pattern; over a 2-tuple, a 3-tuple, a type defined over a
// defined tuple, a generic defined tuple, a record field, a function
// result and a catch. check/tuple_defined_test.go checks the same
// program.
func TestErgoRunDefinedTuple(t *testing.T) {
	wantRun(t, `type Pair (int, string)

type Triple (int, string, bool)

type Again Pair

type Two[T] (T, T)

type Box[T] { v T }

type Bad { why string }

func pair() Pair {
	Pair((1, "a"))
}

func risky(fail bool) Pair fails Bad {
	if fail {
		return Bad{why: "no"}
	}
	Pair((2, "b"))
}

func swap(p Pair) Pair {
	let n, s = p
	Pair((n + 10, s + "!"))
}

let top, word = pair()

let _, again = Again(Pair((3, "c")))

func main() needs Console {
	let a, b = pair()
	Console.Println("\(a) \(b)")
	let x, _, z = Triple((4, "d", true))
	Console.Println("\(x) \(z)")
	let _, m = Again(swap(pair()))
	Console.Println(m)
	let l, r = Two[string](("l", "r"))
	Console.Println(l + r)
	let i, _ = Box[Pair]{v: pair()}.v
	Console.Println("\(i)")
	let c, d = risky(true) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(c) \(d)")
	let e, f = risky(false) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(e) \(f)")
	match Triple((5, "e", false)) {
	case (n, s, true):
		Console.Println("\(n) \(s) yes")
	case (n, s, false):
		Console.Println("\(n) \(s) no")
	}
	Console.Println("\(top) \(word) \(again)")
}
`, "1 a\n4 true\na!\nlr\n1\n0 caught\n2 b\n5 e no\n1 a c\n")
}

// A context value whose contextKey and contextText are generic: the
// planner's probe (e6b-genkey.ergo) runs, and each instance's protocol
// functions are called at that instance (ContextBinding.KeyInst,
// TextInst): a Go method on a generic record (Tag, Keyed) and a generic
// Go function (Loose, Id, whose type parameter's constraint differs from
// the record's or whose type is no record), with the witness of a
// constrained type parameter. The Go side reads each key the program
// binds at each line it prints.
func TestErgoRunGenericContextKey(t *testing.T) {
	wantRun(t, `type Tag[T] { v T }

func contextKey[T](t Tag[T]) string {
	"tag"
}

func contextText[T](t Tag[T]) string {
	"text"
}

func main() needs Console {
	with (Tag[int]{v: 1}) {
		Console.Println("in")
	}
}
`, "in\n")
	o := mustLower(t, "generic-context-key", `type Tag[T] { name string, v T }

func contextKey[T](t Tag[T]) string { t.name }

func contextText[T](t Tag[T]) string { "tag " + t.name }

type Named {
	Label() string,
}

type Card { Number string }

func Label(c Card) string { "card " + c.Number }

type Dog {}

func Label(d Dog) string { "dog" }

type Keyed[T Named] { v T }

func contextKey[T Named](k Keyed[T]) string { k.v.Label() }

func contextText[T Named](k Keyed[T]) string { "keyed " + k.v.Label() }

type Loose[T] { v T }

func contextKey[T Named](k Loose[T]) string { "loose" }

func contextText[T Named](k Loose[T]) string { "loose " + k.v.Label() }

type Id[T] string

func contextKey[T](i Id[T]) string { "id" }

func contextText[T](i Id[T]) string { "id " + string(i) }

func reads() needs Console {
	with (Tag[int]{name: "a", v: 1}), (Tag[string]{name: "b", v: "x"}) {
		Console.Println("tags")
	}
	with (Keyed[Card]{v: Card{Number: "7"}}), (Keyed[Dog]{v: Dog}) {
		Console.Println("keyed")
	}
	with (Loose[Card]{v: Card{Number: "8"}}) {
		Console.Println("loose card")
		with (Loose[Dog]{v: Dog}) {
			Console.Println("loose dog")
		}
		Console.Println("loose card again")
	}
	with (Id[int]("one")) {
		Console.Println("id")
	}
}

func main() {}
`)
	contextModuleTest(t, o, genericKeyTest)
}

// genericKeyTest runs reads with a Console that appends, to each line,
// every key of package main the program binds, with its text.
const genericKeyTest = `package main
import (
 "strings"
 "testing"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
type console struct{ lines []string }
func (c *console) Println(t *rt.Task, line string) {
 for _, key := range []string{"a", "b", "card 7", "dog", "loose", "id"} {
  if ergo.ContextBound(t, "main", key) { line += " [" + key + "=" + ergo.ContextValue(t, "main", key) + "]" }
 }
 c.lines = append(c.lines, line)
}
func TestGenericKeys(t *testing.T) {
 c := &console{}
 if err := ergo.ContextRun(func(task *rt.Task) error { reads(&Env{task: task, Console: c}); return nil }); err != nil {
  t.Fatal(err)
 }
 want := strings.Join([]string{
  "tags [a=tag a] [b=tag b]",
  "keyed [card 7=keyed card 7] [dog=keyed dog]",
  "loose card [loose=loose card 8]",
  "loose dog [loose=loose dog]",
  "loose card again [loose=loose card 8]",
  "id [id=id one]",
 }, "\n")
 if got := strings.Join(c.lines, "\n"); got != want {
  t.Fatalf("generic keys:\n%s\nwant:\n%s", got, want)
 }
}
`

// The docs claims of stage E's close (types.md, Interface assignability and
// Generics): an operations conversion whose implementing function is
// itself generic over a method-set constraint lowers, as a value and
// through a dot call.
func TestErgoRunGenericImplementingFunction(t *testing.T) {
	wantRun(t, `type Valid {
	Check() bool,
}

type Card { Number string }

func Check(c Card) bool {
	c.Number != ""
}

type Holder[T Valid] { item T }

func Check[T Valid](h Holder[T]) bool {
	h.item.Check()
}

func use(v Valid) bool {
	v.Check()
}

func main() needs Console {
	let h = Holder[Card]{item: Card{Number: "1"}}
	let e = Holder[Card]{item: Card{Number: ""}}
	Console.Println("\(use(h)) \(h.Check()) \(use(e))")
}
`, "true true false\n")
}

// types.md, Operators: each operator of rules 1 to 4 runs, on the
// operand types the rules name.
func TestErgoRunOperators(t *testing.T) {
	wantRun(t, `type Celsius float64

type P { X int, Y string }

func main() needs Console {
	let a int8 = 7
	let b uint16 = 65535
	let f float32 = 1.5
	let c Celsius = 20.5
	let s = "ab"
	let d = 90s
	let z complex128 = complex128(2)
	Console.Println("\(a + 2) \(a - 9) \(a * 3) \(a / 2) \(a % 4) \(-a)")
	Console.Println("\(b + 1) \(b & 255) \(b | 1) \(b ^ 1) \(b &^ 255) \(b >> 8) \(1 << 4)")
	Console.Println("\(f * 2) \(c - 0.5) \(z * z)")
	Console.Println("\(s + "c") \(s < "b") \(s == "ab") \(s != "ab")")
	Console.Println("\(d + 30s) \(d - 30s) \(d > 1m) \(d == 90s)")
	Console.Println("\(a < 8 && b > 1) \(a > 8 || !(b > 1)) \(P{X: 1, Y: "y"} == P{X: 1, Y: "y"})")
}
`, "9 -2 21 3 3 -7\n0 255 65535 65534 65280 255 16\n3 20 (4+0i)\nabc true true false\n2m0s 1m0s true true\ntrue false true\n")
}

// Every type of every std package and of the prelude has a runtime form or
// lowers as a declaration: runtimeType never reaches its last branch, which
// is an internal error (C43: it named a closed stage before).
func TestStdTypesHaveRuntimeForms(t *testing.T) {
	paths := std.Packages()
	if len(paths) < 5 {
		t.Fatalf("std.Packages() lists %d packages: %v", len(paths), paths)
	}
	const src = "func main() {}\nfunc probe(o Option[int]) {}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	mainPkg, ds := check.Check("main", []*syntax.File{file}, nil)
	if len(ds) != 0 {
		t.Fatalf("%v\n%s", ds, src)
	}
	pkgs := map[string]*check.Package{}
	for _, p := range paths {
		pkg, err := check.Std.Import(p)
		if err != nil {
			t.Fatal(err)
		}
		pkgs[p] = pkg
	}
	for _, f := range mainPkg.Funcs {
		if f.Name != "probe" {
			continue
		}
		if n, ok := f.Sig.Params[0].Type.(*check.Named); ok && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" {
			pkgs["prelude"] = n.Obj.Pkg
		}
	}
	if pkgs["prelude"] == nil {
		t.Fatal("no prelude package reached")
	}
	l := newLowerer(Unit{Package: mainPkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	n := 0
	for path, pkg := range pkgs {
		for _, tn := range pkg.Types {
			named, ok := tn.Type.(*check.Named)
			if !ok {
				t.Errorf("%s.%s is not a named type", path, tn.Name)
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s.%s: %v", path, tn.Name, r)
					}
				}()
				l.runtimeType(named, file)
			}()
			n++
		}
	}
	// The std packages declare 5 types (log, queue, timeout, trace) and the
	// prelude the rest; fewer means the enumeration lost a package.
	if n < 20 {
		t.Errorf("%d types in %d packages", n, len(pkgs))
	}
	t.Logf("%d types in %d packages", n, len(pkgs))
}
