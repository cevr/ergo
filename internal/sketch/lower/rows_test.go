package lower

import (
	"regexp"
	"strings"
	"testing"
)

// behavior is a program with its expected stdout and exit status, and text
// its stderr must hold.
type behavior struct {
	name, src, stdout string
	exit              int
	stderr            []string
}

// runBehaviors lowers, builds and runs each program and compares it with its
// expectation.
//
// It builds Go because its callers ask what the built program prints and how
// it exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func runBehaviors(t *testing.T, cases []behavior) map[string]*lowered {
	t.Helper()
	var built []*lowered
	byName := map[string]*lowered{}
	for _, c := range cases {
		o := mustLower(t, c.name, c.src)
		built = append(built, o)
		byName[c.name] = o
	}
	exes := buildAll(t, built, true)
	if exes == nil {
		return byName
	}
	for _, c := range cases {
		stdout, stderr, code := runExe(t, exes[c.name])
		if stdout != c.stdout || code != c.exit {
			t.Errorf("%s: exit %d, want %d\nstdout %q\nwant   %q\nstderr %q", c.name, code, c.exit, stdout, c.stdout, firstLines(stderr, 12))
		}
		for _, s := range c.stderr {
			if !strings.Contains(stderr, s) {
				t.Errorf("%s: stderr lacks %q:\n%s", c.name, s, firstLines(stderr, 12))
			}
		}
	}
	return byName
}

// TestRowsAndDefects: typed failures follow their rows, and defects are
// never typed failures. A total catch handles its row; a partial catch (try
// … catch) handles its arms and the rest escapes to the caller; a caught
// value returned is a failure again (rethrow); a no-value catch runs its arm
// for effect; a failure tried inside a with block leaves the enclosing
// function, not the catch around it; main's remaining failure is printed
// with status 1. Contexts: replace and nesting pick the innermost binding, an
// entry of several keys is one provider value under each, and an
// overridden provider never runs. A Go panic (here an integer division by
// zero) passes every catch and ends the program with Go's report, which the
// //line directives point at the .ergo line.
func TestRowsAndDefects(t *testing.T) {
	needsBuild(t)
	built := runBehaviors(t, rowsCases())
	checkContextBuilds(t, built)
}

// rowsCases are TestRowsAndDefects' programs.
func rowsCases() []behavior {
	const errs = `type NotFound { Name string }
type Denied { Who string }

func find(name string) string fails NotFound | Denied {
	if name == "root" {
		return Denied{Who: name}
	}
	if name == "ada" {
		return "Ada"
	}
	return NotFound{Name: name}
}
`
	return []behavior{{
		name: "total",
		src: errs + `
func show(name string) needs Console {
	let line = find(name) catch {
	case NotFound as e:
		"no " + e.Name
	case Denied:
		"denied"
	}
	Console.Println(line)
}

func main() needs Console {
	show("ada")
	show("bob")
	show("root")
}
`,
		stdout: "Ada\nno bob\ndenied\n",
	}, {
		name: "partial",
		src: errs + `
func lookup(name string) string fails Denied needs Console {
	let v = try find(name) catch {
	case NotFound:
		Console.Println("defaulted " + name)
		"nobody"
	}
	return v
}

func main() needs Console {
	let a = lookup("bob") catch {
	case Denied:
		"unreachable"
	}
	Console.Println(a)
	let b = lookup("root") catch {
	case Denied as d:
		"escaped " + d.Who
	}
	Console.Println(b)
}
`,
		stdout: "defaulted bob\nnobody\nescaped root\n",
	}, {
		name: "rethrow",
		src: errs + `
func again(name string) string fails NotFound | Denied needs Console {
	let v = find(name) catch {
	case NotFound as e:
		Console.Println("logged " + e.Name)
		return e
	case Denied as d:
		return d
	}
	return v
}

func main() needs Console {
	let v = again("bob") catch {
	case NotFound as e:
		"rethrown " + e.Name
	case Denied:
		"denied"
	}
	Console.Println(v)
}
`,
		stdout: "logged bob\nrethrown bob\n",
	}, {
		name: "novalue",
		src: `type Bad { At string }

func check(ok bool) fails Bad needs Console {
	if !ok {
		return Bad{At: "check"}
	}
	Console.Println("checked")
}

func main() needs Console {
	check(true) catch {
	case Bad:
		Console.Println("unreachable")
	}
	check(false) catch {
	case Bad as b:
		Console.Println("handled " + b.At)
	}
}
`,
		stdout: "checked\nhandled check\n",
	}, {
		name: "trywith",
		src: `type Bad { At string }

type Num { Get() int }

type One {}

func Get(_ One) int { return 1 }

let W = context {
	Num: One,
}

func f(fail bool) int fails Bad {
	if fail {
		return Bad{At: "f"}
	}
	return 2
}

func h(fail bool) int fails Bad needs Console {
	let x = with W {
		let y = try f(fail)
		Console.Println("after try")
		y + Num.Get()
	}
	Console.Println("after with")
	return x
}

func main() needs Console {
	let a = h(false) catch {
	case Bad:
		0
	}
	if a == 3 {
		Console.Println("three")
	}
	let b = h(true) catch {
	case Bad as e:
		Console.Println("caught " + e.At)
		0
	}
	if b == 0 {
		Console.Println("zero")
	}
}
`,
		stdout: "after try\nafter with\nthree\ncaught f\nzero\n",
	}, {
		name: "mainfails",
		src: `type Bad { At string }

func fail() fails Bad { return Bad{At: "deep"} }

func main() fails Bad needs Console {
	Console.Println("before")
	try fail()
	Console.Println("unreachable")
}
`,
		stdout: "before\n",
		exit:   1,
		stderr: []string{"error: Bad{At:deep}"},
	}, {
		name: "contexts",
		src: `type Store { Get() string }
type Timer { Now() string }

type Holder { V string }

func Get(h Holder) string { return h.V }

type Both { V string }

func Get(b Both) string { return "store " + b.V }
func Now(b Both) string { return "clock " + b.V }

type Div { N int }

func Get(d Div) string { return "div" }

func zero() int { return 0 }

let Base = context {
	Store: Holder{V: "base"},
}

let Swapped = context {
	Base,
	replace Store: Holder{V: "replaced"},
}

let Poisoned = context {
	Store: Div{N: 1 / zero()},
}

let Healed = context {
	Poisoned,
	replace Store: Holder{V: "healed"},
}

let Shared = context {
	Store, Timer: Both{V: "one"},
}

func show() needs Console | Store {
	Console.Println(Store.Get())
}

func main() needs Console {
	with Base {
		show()
		with Swapped { show() }
		show()
	}
	with Healed { show() }
	with Shared {
		Console.Println(Store.Get())
		Console.Println(Timer.Now())
	}
}
`,
		stdout: "base\nreplaced\nbase\nhealed\nstore one\nclock one\n",
	}, {
		name: "defect",
		src: `type Bad { At string }

func risky(n int) int fails Bad needs Console {
	Console.Println("dividing")
	if n < 0 {
		return Bad{At: "negative"}
	}
	return 10 / n
}

func main() needs Console {
	let v = risky(0) catch {
	case Bad:
		Console.Println("the catch ran")
		0
	}
	if v == 0 {
		Console.Println("unreachable")
	}
}
`,
		stdout: "dividing\n",
		exit:   2,
		stderr: []string{"integer divide by zero", testSourcePath + ":8"},
	}}
}

// checkContextBuilds checks the context program's Go builders.
func checkContextBuilds(t *testing.T, built map[string]*lowered) {
	// The shared entry is evaluated once and bound under both keys; the
	// overridden entry is not built at all.
	if o := built["contexts"]; o != nil {
		goSrc := o.mainGo()
		if n := strings.Count(goSrc, `Both{V: "one"}`); n != 1 {
			t.Errorf("the shared provider is built %d times, want once:\n%s", n, goSrc)
		}
		healed := regexp.MustCompile(`(?s)func buildHealed\(.*?\n(?:/\*line [^*]*\*/)?}\n`).FindString(goSrc)
		if healed == "" || strings.Contains(healed, "Div{") {
			t.Errorf("Healed's builder builds the overridden provider Div, or is missing:\n%s", healed)
		}
	}
}
