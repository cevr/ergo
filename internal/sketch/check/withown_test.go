package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The wire ownership fact (runtime-199 stage 3b; resources.md, Wire
// ownership): per with site, the entries it acquires in acquisition order,
// each with its resolved Close, the release needs charged at the with
// site, and the located refusals.

const ownPrelude = `type A { Use() }
type B { Use() }
type Log { Say(s string) }
type First {}
type Second {}
type Pair {}
type Other {}
type Quiet {}
func Use(_ First)  {}
func Use(_ Second) {}
func Use(_ Pair)   {}
func Use(_ Other)  {}
func Say(_ Quiet, s string) {}
func Close(_ First)  {}
func Close(_ Pair)   {}
func Close(_ Other)  {}
`

// ownFiles is the parsed file of each package checkOwn checked.
var ownFiles = map[*check.Package]*syntax.File{}

// acquisitions renders each with site, by its function, in source order:
// the acquired providers' types, the capabilities each binds, and "!" for
// a resource (an acquisition with a Close).
func acquisitions(t *testing.T, pkg *check.Package) []string {
	t.Helper()
	var out []string
	for _, d := range ownFiles[pkg].Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := pkg.Info.Withs[x]
			if w == nil {
				return true
			}
			var parts []string
			for _, a := range w.Acquired {
				var caps []string
				for _, i := range a.Caps {
					caps = append(caps, strings.TrimPrefix(a.Entry.Caps[i].String(), "main."))
				}
				s := strings.TrimPrefix(fmt.Sprint(pkg.Info.Types[a.Entry.Provider]), "main.") + "[" + strings.Join(caps, ",") + "]"
				if a.Close != nil {
					if a.Close.Func == nil {
						t.Errorf("%s: a with acquisition whose Close is not a function", fd.Name.Name)
					}
					s += "!"
				}
				parts = append(parts, s)
			}
			out = append(out, fd.Name.Name+": "+strings.Join(parts, " "))
			return true
		})
	}
	return out
}

func checkOwn(t *testing.T, src string) (*check.Package, []check.Diagnostic) {
	t.Helper()
	var imports, rest []string
	for _, line := range strings.SplitAfter(src, "\n") {
		if strings.HasPrefix(line, "import ") {
			imports = append(imports, line)
		} else {
			rest = append(rest, line)
		}
	}
	f, err := syntax.Parse(strings.Join(imports, "") + ownPrelude + strings.Join(rest, ""))
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	ownFiles[pkg] = f
	return pkg, ds
}

func TestWireOwnershipAcquisitions(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		{"a shared entry is one acquisition", `let W = wire { A, B: Pair }
func f() { with W { A.Use(); B.Use() } }
`, []string{"f: Pair[A,B]!"}},
		{"an overridden entry is not acquired", `let Base = wire { A: First }
let W = wire { replace A: Other, Base }
func f() { with W { A.Use() } }
`, []string{"f: Other[A]!"}},
		{"an unreachable entry is not acquired", `let W = wire { A: First, B: Second }
func f() { with W { B.Use() } }
`, []string{"f: Second[B]"}},
		{"a provider's dependency is acquired first", `func second() Second needs A { A.Use(); Second }
let W = wire { B: second(), A: First }
func f() { with W { B.Use() } }
`, []string{"f: First[A]! Second[B]"}},
		{"independent entries follow the wire pass's entry order", `let Base = wire { B: Second }
let W = wire { A: First, Base }
let V = wire { A: First, B: Second }
func f() { with W { B.Use(); A.Use() } }
func g() { with V { B.Use(); A.Use() } }
`, []string{"f: Second[B] First[A]!", "g: First[A]! Second[B]"}},
		{"a shared entry replaced for one capability binds the other", `let Base = wire { A, B: Pair }
let W = wire { replace A: Other, Base }
func f() { with W { A.Use(); B.Use() } }
`, []string{"f: Pair[B]! Other[A]!"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg, ds := checkOwn(t, tc.src)
			if len(ds) > 0 {
				t.Fatalf("diagnostics: %v", ds)
			}
			got := acquisitions(t, pkg)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("acquisitions:\ngot  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestWireOwnershipReleaseNeeds(t *testing.T) {
	const closes = `type Res {}
func Use(_ Res) {}
func Close(_ Res) needs Log { Log.Say("closed") }
`
	t.Run("a declared row holds the release need", func(t *testing.T) {
		_, ds := checkOwn(t, closes+`let W = wire { A: Res }
func f() needs Log { with W { A.Use() } }
`)
		if len(ds) > 0 {
			t.Fatalf("diagnostics: %v", ds)
		}
	})
	t.Run("a need no row holds is S102 at the with", func(t *testing.T) {
		src := closes + `let W = wire { A: Res }
func f() needs A { with W { A.Use() } }
func g() { with W { A.Use() } }
`
		_, ds := checkOwn(t, src)
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+fmt.Sprint(d.Line))
			if !strings.Contains(d.Message, "the release of main.Res when this with ends requires main.Log") {
				t.Errorf("message: %s", d.Message)
			}
		}
		base := strings.Count(ownPrelude+closes, "\n")
		want := []string{fmt.Sprintf("S102 %d", base+2), fmt.Sprintf("S102 %d", base+3)}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("diagnostics %v, want %v", got, want)
		}
	})
	t.Run("an outer with discharges the need and acquires its provider", func(t *testing.T) {
		pkg, ds := checkOwn(t, closes+`let Out = wire { Log: Quiet, B: Second }
let W = wire { A: Res }
func f() { with Out { with W { A.Use() } } }
`)
		if len(ds) > 0 {
			t.Fatalf("diagnostics: %v", ds)
		}
		got := acquisitions(t, pkg)
		want := []string{"f: Quiet[Log]", "f: Res[A]!"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("acquisitions:\ngot  %q\nwant %q", got, want)
		}
	})
	t.Run("the release need reaches the outer with's own release", func(t *testing.T) {
		pkg, ds := checkOwn(t, closes+`type Sink {}
func Say(_ Sink, s string) {}
func Close(_ Sink) {}
let Out = wire { Log: Sink }
let W = wire { A: Res }
func f() { with Out { with W { A.Use() } } }
`)
		if len(ds) > 0 {
			t.Fatalf("diagnostics: %v", ds)
		}
		got := acquisitions(t, pkg)
		want := []string{"f: Sink[Log]!", "f: Res[A]!"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("acquisitions:\ngot  %q\nwant %q", got, want)
		}
	})
}

func TestWireOwnershipRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, src, code, msg string
	}{
		{"a provider naming a package let", `let shared = First
let W = wire { A: shared }
func f() { with W { A.Use() } }
`, "S109", "this resource provider names an existing value"},
		{"a provider naming a field", `type Box { R First }
let box = Box{R: First}
let W = wire { A: box.R }
func f() { with W { A.Use() } }
`, "S109", "this resource provider names an existing value"},
		{"an exit action that needs a provider of the with", `import scope
let W = wire { A: First }
func f() { with W { scope.OnExit() { A.Use() } } }
`, "S108", "this exit action runs when the function returns"},
		{"a with value that holds a Task", `import task
let W = wire { A: First }
func f() {
	let t = with W { task.Go() { A.Use() } }
	t.Wait()
}
`, "S108", "the value of this with holds a Task"},
		{"a capability's Close", `type R { Close() }
let W = wire { R: First }
func g() needs R { R.Close() }
func f() { with W { g() } }
`, "S108", "R.Close releases the capability's provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ds := checkOwn(t, tc.src)
			if len(ds) != 1 || ds[0].Code != tc.code || !strings.Contains(ds[0].Message, tc.msg) {
				t.Errorf("diagnostics %v, want one %s %q", ds, tc.code, tc.msg)
			}
		})
	}
	// Admitted: an exit action that needs nothing the owning with
	// provides, one inside a task block, a non-resource provider naming a
	// value, and a fresh conversion.
	for _, src := range []string{
		`import scope
let W = wire { A: First }
func f() needs Log { with W { A.Use(); scope.OnExit() { Log.Say("x") } } }
`,
		`import scope
import task
let W = wire { A: First }
func f() { with W { let t = task.Go() { scope.OnExit() { A.Use() } }; t.Wait() } }
`,
		`let s = Second
let W = wire { B: s }
func f() { with W { B.Use() } }
`,
	} {
		_, ds := checkOwn(t, src)
		if len(ds) > 0 {
			t.Errorf("refused %s\ndiagnostics: %v", src, ds)
		}
	}
}
