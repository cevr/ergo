package lower

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// namesTemplate reaches every kind of package-scope Go entity the lowering
// generates: the time and runtime imports, the environment and the shared
// empty one, a context builder, an operations adapter, a library type
// (timeout.Expired) and a prelude record (LiveScheduler), and an error
// type's text function. LOCAL marks where a local is declared.
const namesTemplate = `import timeout

type Kind = Red | Blue

type Red { n int }

type Blue { n int }

type Bad { code int, kind Kind, tags []string }

type Store { Get() int }

type Mem[T] { val T }

func Get[T](m Mem[T]) int { return 4 }

let W = wire {
	Store: Mem[int]{val: 1},
}

func pure(f func(n int) int) int { return f(1) }

func expire() int fails timeout.Expired { return 1 }

func bad(n int) fails Bad {
	if n > 0 {
		return Bad{code: n, kind: Red{n: 1}, tags: []string{"a"}}
	}
}

func helper(PARAM int) int {
	let BODY = 1
	let d duration = 2s
	if d == 2s {
		return PARAM + BODY
	}
	return 0
}

func main() fails Bad needs Console {
	let LOCAL = 1
	let got = with W {
		Store.Get()
	}
	let caught = expire() catch {
	case timeout.Expired:
		7
	}
	let viaValue = pure(func(LIT int) int {
		return LIT + with W {
			Store.Get()
		}
	})
	let s LiveScheduler
	s.Sleep(1ms)
	if got + caught + viaValue + helper(2) + LOCAL == 14 {
		Console.Println("ok")
	}
	try bad(0)
}
`

// TestLateNamesNeverCaptureLocals: every package-scope Go name the
// lowering gives the template is taken, in turn, by a local in each
// position a local can have (a let in main, a parameter, a let in another
// function, a function literal's parameter), declared before the entity
// is first used. Each program builds, vets and prints ok, so no package
// name chosen late captures a local (findings M3). The fixed local names
// of round 2's generated helpers (v, x, texts, i, e) join them. The names come from the
// lowering itself, so a new kind of generated entity joins the test.
func TestLateNamesNeverCaptureLocals(t *testing.T) {
	base := strings.NewReplacer("LOCAL", "local", "PARAM", "param", "BODY", "body", "LIT", "lit").Replace(namesTemplate)
	file, err := syntax.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("the template does not check: %v", diags)
	}
	l := newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: base}}})
	if _, err := l.program(); err != nil {
		t.Fatalf("the template does not lower: %v", err)
	}
	declared := map[string]bool{}
	// A name the source spells unqualified is a source name, which a local
	// would shadow in Ergo too.
	for _, m := range regexp.MustCompile(`(^|[^.A-Za-z0-9_])([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(base, -1) {
		declared[m[2]] = true
	}
	// A type or function in unqualified Ergo scope cannot be a bare let
	// binder even if this source never spells it. Qualified library names
	// (such as Expired) remain legal candidates and still exercise capture.
	for _, e := range l.pkgOrder {
		switch o := e.key.obj.(type) {
		case *check.TypeName:
			if o.Pkg == pkg || o.Pkg.Path == std.PreludePath {
				declared[o.Name] = true
			}
		case *check.Func:
			if o.Pkg == pkg || o.Pkg.Path == std.PreludePath {
				declared[o.Name] = true
			}
		}
	}
	var names []string
	for _, e := range l.pkgOrder {
		name := l.pkgNames[e.key]
		if !declared[name] && !goReserved[name] {
			names = append(names, name)
		}
	}
	// The fixed local names of generated helpers in round 2 (findings r2 M1).
	for _, name := range []string{"v", "x", "texts", "i", "e"} {
		if !declared[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, want := range []string{"time", "ergo", "Env", "hostConsole", "buildW", "opsMemAsStore", "Expired", "textBad", "textKind", "textList", "v", "x", "texts", "i", "e"} {
		if sort.SearchStrings(names, want) == len(names) || names[sort.SearchStrings(names, want)] != want {
			t.Errorf("the template's lowering has no package entity %s; it names %v", want, names)
		}
	}
	var programs []*lowered
	for _, name := range names {
		for _, slot := range []string{"LOCAL", "PARAM", "BODY", "LIT"} {
			r := map[string]string{"LOCAL": "local", "PARAM": "param", "BODY": "body", "LIT": "lit"}
			r[slot] = name
			src := strings.NewReplacer("LOCAL", r["LOCAL"], "PARAM", r["PARAM"], "BODY", r["BODY"], "LIT", r["LIT"]).Replace(namesTemplate)
			programs = append(programs, mustLower(t, "n_"+name+"_"+strings.ToLower(slot), src))
		}
	}
	exes := buildAll(t, programs, true)
	for _, o := range programs {
		if exes == nil {
			break
		}
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != "ok\n" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", o.name, code, stdout, firstLines(stderr, 6))
		}
	}
	t.Logf("%d package names x 4 local positions: %v", len(names), names)
}
