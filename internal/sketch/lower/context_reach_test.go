package lower

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The packages of TestContextReachProperty. p declares the contract S and
// a function that needs it; n provides S with a resource made by its own
// function; m1 and m2 embed n.W one and two levels up. A program that
// names n's context only through m1 or m2 reaches n through embeddings
// alone (196b U4, round 1 B1).
const (
	reachP  = "type S { N() string }\n\nfunc Use() string needs S { S.N() }\n"
	reachN  = "import p\n\ntype C { k int }\n\nfunc N(c C) string { \"n \\(c.k)\" }\n\nfunc Close(c C) needs Console {\n\tConsole.Println(\"closed n\")\n}\n\nfunc mk() C { C{k: 2} }\n\nfunc Make(k int) C { C{k: k} }\n\nfunc K() int { 5 }\n\nlet W = context { p.S: mk() }\n"
	reachM1 = "import n\n\nlet W = context { n.W }\n"
	reachM2 = "import m1\n\nlet W = context { m1.W }\n"
	// q is an imported context that the program also names: a with body
	// calls q.R.
	reachQ = "type R { Get() string }\n\ntype G {}\n\nfunc Get(_ G) string { \"q\" }\n\nlet V = context { R: G }\n"
	// reachT is the root's own contract T and its provider H.
	reachT = "type T { Hi() string }\n\ntype H {}\n\nfunc Hi(_ H) string { \"h\" }\n"
)

// reachShortStride is the stride of the programs TestContextReachProperty
// builds under -short.
const reachShortStride = 4

// reachUnits checks p, n, m1, m2 and q.
func reachUnits(t *testing.T) map[string]Unit {
	t.Helper()
	units := map[string]Unit{}
	imp := pkgImporter{}
	for _, s := range []struct{ path, src string }{{"p", reachP}, {"n", reachN}, {"m1", reachM1}, {"m2", reachM2}, {"q", reachQ}} {
		u := checkPackage(t, s.path, []pkgSource{{"/ergo-test/" + s.path + "/" + s.path + ".ergo", s.src}}, imp)
		units[s.path] = u
		imp[s.path] = u.Package
	}
	return units
}

// TestContextReachProperty (round 1 B1): a program that reaches a
// provider package only through a context, by a with operand or an
// embedding at any depth, builds and runs as one that also names the
// package. The generator takes every operand form (with X, with context
// { X }, a named context and a local context that embed X, a replace over
// X's entry by a provider of the root and by one of n), every depth of X
// (n.W, m1.W ⊃ n.W, m2.W ⊃ m1.W ⊃ n.W) and both reaches of n (through the
// context only, or also by a call of n.K). It also makes withs of two and
// three operands, with X in every position beside a root context, a
// body-local context and an imported context whose package the program
// also names (round 2 M1: the reach reads every operand, not only the
// first). Every generated program checks
// (checkPackage fails otherwise), lowers with no refusal and no internal
// error, builds, runs, closes n's resource once when it builds it, and
// prints the provider's value.
//
// Each program is a go build and a run, so the domain is the test's cost
// (11 s of the fast tier's lower CPU, fast-cpu batch 9). Under -short it
// keeps every reachShortStride-th program in generation order: each depth
// generates 21 programs, so a stride of 4 takes other forms and operand
// orders at each depth.
func TestContextReachProperty(t *testing.T) {
	units := reachUnits(t)
	imp := pkgImporter{}
	for path, u := range units {
		imp[path] = u.Package
	}
	// The import closure of each package, as the loader lists it.
	closure := map[string][]string{"p": {"p"}, "n": {"n", "p"}, "m1": {"m1", "n", "p"}, "m2": {"m2", "m1", "n", "p"}, "q": {"q"}}
	var programs []*lowered
	want := map[string]string{}
	generated := 0
	program := func(name string, imports []string, src, out string) {
		generated++
		// Under -short (the gate's fast tier) the test builds every
		// reachShortStride-th program of the generation order, a fixed
		// subset; the full tier builds them all.
		if testing.Short() && (generated-1)%reachShortStride != 0 {
			return
		}
		main := checkPackage(t, "main", []pkgSource{{"/ergo-test/" + name + "/main.ergo", src}}, imp)
		seen := map[string]bool{}
		for _, i := range imports {
			for _, dep := range closure[i] {
				if !seen[dep] {
					seen[dep] = true
					main.Imports = append(main.Imports, units[dep])
				}
			}
		}
		programs = append(programs, mustLowerUnit(t, name, main, false))
		want[name] = out
	}
	type form struct {
		name, decls, expr, want string
		namesN                  bool // the form itself names n
	}
	forms := func(x string) []form {
		return []form{
			{"with", "", "with " + x + " { p.Use() }", "closed n\nn 2\n", false},
			{"inline", "", "with context { " + x + " } { p.Use() }", "closed n\nn 2\n", false},
			{"named", "let C = context { " + x + " }\n", "with C { p.Use() }", "closed n\nn 2\n", false},
			{"local", "", "", "closed n\nn 2\n", false},
			{"replace-root", "type Mine {}\n\nfunc N(_ Mine) string { \"mine\" }\n\nlet C = context { " + x + ", replace p.S: Mine }\n", "with C { p.Use() }", "mine\n", false},
			{"replace-n", "let C = context { " + x + ", replace p.S: n.Make(9) }\n", "with C { p.Use() }", "closed n\nn 9\n", true},
		}
	}
	for depth, pkg := range []string{"n", "m1", "m2"} {
		x := pkg + ".W"
		for _, f := range forms(x) {
			for _, alsoN := range []bool{false, true} {
				if f.namesN && !alsoN {
					continue // the form names n itself
				}
				imports := []string{pkg, "p"}
				if alsoN && pkg != "n" {
					imports = append(imports, "n")
				}
				var src strings.Builder
				for _, i := range imports {
					src.WriteString("import " + i + "\n")
				}
				src.WriteString("\n" + f.decls + "\nfunc main() needs Console {\n")
				if f.name == "local" {
					src.WriteString("\tlet c = context { " + x + " }\n\tConsole.Println(with c { p.Use() })\n")
				} else {
					src.WriteString("\tConsole.Println(" + f.expr + ")\n")
				}
				out := f.want
				if alsoN {
					src.WriteString("\tConsole.Println(\"\\(n.K())\")\n")
					out += "5\n"
				}
				src.WriteString("}\n")
				program(fmt.Sprintf("reach_d%d_%s_%v", depth+1, strings.ReplaceAll(f.name, "-", "_"), alsoN), imports, src.String(), out)
			}
		}
		// Withs of two and three operands (round 2 M1): X, the only reach
		// of n, in every position, beside a context of the root (Mine), a
		// body-local context (l) and an imported context whose package
		// the body also names (q.V).
		for _, set := range [][]string{{x, "Mine"}, {x, "q.V"}, {x, "l", "q.V"}} {
			for _, order := range permutations(len(set)) {
				ops := make([]string, len(order))
				for i, k := range order {
					ops[i] = set[k]
				}
				has := func(op string) bool { return slices.Contains(ops, op) }
				imports := []string{pkg, "p"}
				if has("q.V") {
					imports = append(imports, "q")
				}
				var src strings.Builder
				for _, i := range imports {
					src.WriteString("import " + i + "\n")
				}
				src.WriteString("\n" + reachT + "\n")
				if has("Mine") {
					src.WriteString("let Mine = context { T: H }\n\n")
				}
				src.WriteString("func main() needs Console {\n")
				if has("l") {
					src.WriteString("\tlet l = context { T: H }\n")
				}
				body, out := `\(p.Use())`, "n 2"
				if has("Mine") || has("l") {
					body, out = body+` \(T.Hi())`, out+" h"
				}
				if has("q.V") {
					body, out = body+` \(q.R.Get())`, out+" q"
				}
				src.WriteString("\tConsole.Println(with " + strings.Join(ops, ", ") + " { \"" + body + "\" })\n}\n")
				label := strings.NewReplacer(pkg+".W", "X", "q.V", "V").Replace(strings.Join(ops, "_"))
				program(fmt.Sprintf("reach_d%d_ops_%s", depth+1, label), imports, src.String(), "closed n\n"+out+"\n")
			}
		}
	}
	if generated < 63 {
		t.Fatalf("the generator made %d programs", generated)
	}
	if want := (generated + reachShortStride - 1) / reachShortStride; testing.Short() && len(programs) != want || !testing.Short() && len(programs) != generated {
		t.Fatalf("the test builds %d of %d generated programs", len(programs), generated)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the generated programs do not build")
	}
	for _, p := range programs {
		stdout, stderr, code := runExe(t, exes[p.name])
		if code != 0 || stdout != want[p.name] {
			t.Errorf("%s: exit %d stdout %q, want %q\n%s\nstderr:\n%s", p.name, code, stdout, want[p.name], p.src, stderr)
		}
	}
}

// TestContextLibOwnWithSet (round 1, the verifier's probe): a with of lib's
// own context inside lib acquires a set that no with of main acquires, so
// its builder is named only from lib's own with facts (withsOf reads every
// sourced package's).
func TestContextLibOwnWithSet(t *testing.T) {
	lib := checkPackage(t, "lib", []pkgSource{{"/ergo-test/lib/lib.ergo", importedContextLib + `
func Both() string needs Console {
	with W {
		Log.Say("x")
		Store.Get()
	}
}
`}}, nil)
	imp := pkgImporter{"lib": lib.Package}
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/own/main.ergo", "import lib\n\nfunc main() needs Console {\n\tConsole.Println(with lib.W { lib.Store.Get() })\n\tConsole.Println(lib.Both())\n}\n"}}, imp)
	main.Imports = []Unit{lib}
	o := mustLowerUnit(t, "ctx_lib_own_set", main, false)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runExe(t, exes[o.name])
	if want := "closed 1\nlib 1\nclosed 1\nlib 1\n"; code != 0 || stdout != want {
		t.Errorf("exit %d stdout %q, want %q\nstderr:\n%s", code, stdout, want, stderr)
	}
}
