package fix

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFixWritesCapabilityArguments: under explicit rows (q137) ergo fix
// can write every clause the checker asks for. Over generated capability
// arguments, built from every form a capability's argument takes
// (types.md, Capabilities 7; check's typExpr): a named type and a generic
// instance, a slice, an array, a map, a tuple, a borrow, a function type
// with an escaping or plain function parameter, results, fails and needs
// rows and a takes clause, and an error row argument. Each seed charges
// one argument of this package's types four ways: the release of a with's
// provider (Context ownership 6, which the second check names again,
// withown.go seedType) and a call; and one argument of two other
// packages' types, which the package does not import, the same two ways.
// Checked: the checker asks (four S102, each with a clause fix can
// write), and fix writes them all and adds the imports, so the program
// then checks clean. -fix.seed replays one seed.
//
// Mutants it kills that no other test does (ledger: testdata/mutants.json,
// rel4-*): a printer branch of spell (clausefix.go) or a rebuilding branch
// of seedType (withown.go) dropped or miswritten, and an element's second
// import dropped.
func TestFixWritesCapabilityArguments(t *testing.T) {
	seeds := 200
	first := uint64(1)
	if *fixSeed != 0 {
		first, seeds = uint64(*fixSeed), 1
	}
	root := t.TempDir()
	reached := map[string]bool{}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0xca9a))
		local := &argGen{r: r, reached: reached}
		remote := &argGen{r: r, remote: true, reached: reached}
		localArg, remoteArg := local.top(), remote.top()
		dir := filepath.Join(root, fmt.Sprint(seed))
		files := map[string]string{
			"ergo.mod":         "module m\n",
			"lib/lib.ergo":     capLib,
			"other/other.ergo": capOther,
			"more/more.ergo":   capMore,
			"mid/mid.ergo":     strings.ReplaceAll(capMid, "ARG", remoteArg),
			"main.ergo":        strings.ReplaceAll(capMain, "ARG", localArg),
		}
		texts := map[string]string{}
		for name, text := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			if name != "ergo.mod" {
				texts[path] = text
			}
		}
		key := filepath.Join(dir, "main.ergo")
		fail := func(format string, args ...any) {
			t.Helper()
			t.Errorf("seed %d: %s\nmain.ergo:\n%s\nmid.ergo:\n%s\nreplay: go test ./internal/sketch/fix -run TestFixWritesCapabilityArguments -fix.seed=%d",
				seed, fmt.Sprintf(format, args...), texts[key], texts[filepath.Join(dir, "mid", "mid.ergo")], seed)
		}
		checker := Dir(dir)
		out, err := checker(texts, check.Options{})
		if err != nil {
			fail("check: %v", err)
			continue
		}
		asks := 0
		var other []string
		for _, d := range out.Diagnostics {
			switch {
			case d.Code == check.CodeMissingCap && d.File == key && d.Fix != nil && d.Fix.Text != "":
				asks++
			default:
				other = append(other, fmt.Sprintf("%s %s %s (fix %v)", filepath.Base(d.File), d.Code, d.Message, d.Fix != nil))
			}
		}
		if asks != 4 || len(other) > 0 {
			fail("want four S102 in main.ergo, each with a clause fix writes; got %d and:\n%s", asks, strings.Join(other, "\n"))
			continue
		}
		res, err := Run(texts, checker)
		if err != nil {
			fail("fix: %v", err)
			continue
		}
		if len(res.Remaining) > 0 {
			var rest []string
			for _, d := range res.Remaining {
				rest = append(rest, fmt.Sprintf("%s %s %s", filepath.Base(d.File), d.Code, d.Message))
			}
			fail("after fix:\n%s\nfixed main.ergo:\n%s", strings.Join(rest, "\n"), res.Texts[key])
		}
	}
	if *fixSeed == 0 {
		for _, form := range argForms {
			if !reached[form] {
				t.Errorf("no seed built the form %q", form)
			}
		}
	}
}

// The packages of TestFixWritesCapabilityArguments. ARG is the generated
// capability: main's of its own types, mid's of lib's and other's, which
// main does not import.
const (
	capLib   = "type LBox[T] { v T }\n"
	capOther = "type Tab[K] { Put(k K) }\ntype Ev[E] { Do() fails E }\ntype OBox[T] { v T }\ntype OJob { id int }\ntype OBad { text string }\ntype OA { Use() }\n"
	capMore  = "type MJob { n int }\ntype MBad { n int }\n"
	capMid   = "import m/other\nimport m/more\n\ntype D { Run() }\ntype RB { id int }\n\nfunc NewRB() RB { RB{id: 1} }\nfunc Close(r RB) needs ARG {}\nfunc Run(_ RB) {}\nfunc Need() needs ARG {}\nfunc Keep(_ more.MJob) {}\n"
	capMain  = "import m/lib\nimport m/mid\n\nfunc KeepLib(_ lib.LBox[int]) {}\n\ntype Tab[K] { Put(k K) }\ntype Ev[E] { Do() fails E }\ntype Box[T] { v T }\ntype Job { id int }\ntype Bad { text string }\ntype Worse { n int }\ntype A { Use() }\ntype C { Use() }\ntype RB { id int }\n\nfunc Close(r RB) needs ARG {}\nfunc Use(_ RB) {}\nfunc H() needs ARG {}\n\nfunc G() {\n\twith context { C: RB{id: 1} } { C.Use() }\n}\n\nfunc K() {\n\tH()\n}\n\nfunc R() {\n\twith context { mid.D: mid.NewRB() } { mid.D.Run() }\n}\n\nfunc Q() {\n\tmid.Need()\n}\n\nfunc main() {}\n"
)

// argForms are the forms argGen builds, each reached by some seed.
var argForms = []string{"named", "instance", "foreign instance", "slice", "array", "map", "tuple", "borrow", "func", "escaping", "results", "fails", "needs", "takes", "row"}

// argGen draws a capability argument of main's types or, remote, of lib's
// and other's.
type argGen struct {
	r       *rand.Rand
	remote  bool
	reached map[string]bool
}

func (g *argGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g *argGen) top() string {
	if g.r.IntN(4) == 0 {
		g.reached["row"] = true
		if g.remote {
			return "other.Ev[" + g.errs() + "]"
		}
		return "Ev[" + g.errs() + "]"
	}
	if g.remote {
		return "other.Tab[" + g.typ(3) + "]"
	}
	return "Tab[" + g.typ(3) + "]"
}

func (g *argGen) named() string {
	g.reached["named"] = true
	if g.remote {
		return g.pick("other.OJob", "more.MJob")
	}
	return "Job"
}

// errs is a row of one or two error types, other's and more's both when
// remote, so one element needs two imports.
func (g *argGen) errs() string {
	if g.remote {
		return g.pick("other.OBad", "more.MBad", "more.MBad | other.OBad")
	}
	return g.pick("Bad", "Worse", "Bad | Worse")
}

func (g *argGen) typ(d int) string {
	if d <= 0 {
		return g.atom()
	}
	switch g.r.IntN(9) {
	case 0:
		return g.atom()
	case 1:
		g.reached["instance"] = true
		// main's own generic type, and lib's (imported) of main's types.
		box := g.pick("Box", "lib.LBox")
		if g.remote {
			box = "other.OBox"
		} else if box == "lib.LBox" {
			g.reached["foreign instance"] = true
		}
		return box + "[" + g.typ(d-1) + "]"
	case 2:
		g.reached["slice"] = true
		return "[]" + g.typ(d-1)
	case 3:
		g.reached["array"] = true
		return "[2]" + g.typ(d-1)
	case 4:
		g.reached["map"] = true
		return "Map[" + g.pick("int", "string", g.named()) + ", " + g.typ(d-1) + "]"
	case 5:
		g.reached["tuple"] = true
		return "(" + g.typ(d-1) + ", " + g.typ(d-1) + ")"
	}
	return g.fn(d - 1)
}

func (g *argGen) atom() string {
	switch g.r.IntN(3) {
	case 0:
		return g.pick("int", "string", "bool")
	}
	return g.named()
}

// fn is a function type: parameters (a borrow, a function type written
// escaping or not, or a type), results, rows and, of main's resource, a
// takes clause, which names every parameter.
func (g *argGen) fn(d int) string {
	g.reached["func"] = true
	var params []string
	for range g.r.IntN(3) {
		switch g.r.IntN(4) {
		case 0:
			g.reached["borrow"] = true
			params = append(params, "*"+g.named())
		case 1:
			esc := ""
			if g.r.IntN(2) == 0 {
				g.reached["escaping"] = true
				esc = "escaping "
			}
			params = append(params, esc+g.fn(d-1))
		default:
			params = append(params, g.typ(d))
		}
	}
	takes := ""
	if !g.remote && g.r.IntN(5) == 0 {
		g.reached["takes"] = true
		for i := range params {
			params[i] = fmt.Sprintf("p%d %s", i, params[i])
		}
		params = append([]string{"r RB"}, params...)
		takes = " takes r"
	}
	s := "func(" + strings.Join(params, ", ") + ")"
	switch g.r.IntN(3) {
	case 1:
		g.reached["results"] = true
		s += " " + g.typ(d)
	case 2:
		g.reached["results"] = true
		s += " (" + g.typ(d) + ", " + g.typ(d) + ")"
	}
	if g.r.IntN(2) == 0 {
		g.reached["fails"] = true
		s += " fails " + g.errs()
	}
	if g.r.IntN(3) == 0 {
		g.reached["needs"] = true
		if g.remote {
			s += " needs other.OA"
		} else {
			s += " needs " + g.pick("A", "C", "A | C")
		}
	}
	return s + takes
}
