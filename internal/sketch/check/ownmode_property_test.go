package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOwnModeFacts (EXM1, q168; RSI-SURFACE-DIET) is a seeded property of the
// mode word own against a model of the facts it gives. It replaces
// fix's TestOwnSpellsTheOldModes, which compared own with the retired
// takes and escaping; with them gone the model states the rule itself.
// Each seed writes one program: an operation type whose operations take
// named or unnamed parameters and may be written own (it keeps its
// receiver), function declarations, a function type written as a
// parameter's type with a literal argument of the same parameters, and a
// let-bound literal. A parameter group is one to three names of a
// resource type R or of a function type, written own or not, and a named
// list may hold a parameter named own. The model: Takes are the indexes of
// the R parameters written own; TakesSelf is the operation written own; a
// function-typed parameter is non-escaping unless written own, and is
// never taken; a literal that writes own on no R parameter keeps the
// takes of the function type it is passed as, and one that does keeps its
// own. ERGO_SEED=<n> replays one seed; a failure prints the program.
//
// Mutants it kills that no other test does (ledgers check and syntax):
// own on a receiver is dropped (exm1-own-recv-dropped); own on a group
// marks only its first name (exm1-own-group-first); own on a function
// value is read as a take (exm1-own-fn-taken); an own function parameter
// stays non-escaping (exm1-own-fn-nonescaping); a literal that writes own
// takes its expected type's takes (exm1-literal-mode-inherited); a
// parameter named own is read as the mode (exm1-own-name-read-as-mode);
// own on a function parameter counts as a written take
// (exm1-writes-takes-fn).
func TestOwnModeFacts(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(200) {
			seeds = append(seeds, i+1)
		}
	}
	reached := map[string]int{}
	for _, seed := range seeds {
		p := ownProgram(rand.New(rand.NewPCG(seed, 0xe11)))
		for k := range p.reached {
			reached[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestOwnModeFacts$' ./internal/sketch/check\n%s", seed, fmt.Sprintf(format, args...), seed, p.src)
		}
		pkg, diags := check.CheckSource(p.src)
		if pkg == nil {
			fail("no package: %v", diags)
		}
		got := map[string]string{}
		for id, obj := range pkg.Info.Defs {
			switch x := obj.(type) {
			case *check.Func:
				got[id.Name] = ownSigFacts(x.Sig)
			case *check.Var:
				if s, ok := x.Type.(*check.Signature); ok {
					got[id.Name] = ownSigFacts(s)
				}
			case *check.TypeName:
				if n, ok := x.Type.(*check.Named); ok {
					if it, ok := n.Underlying().(*check.Interface); ok {
						for _, m := range it.Methods {
							got[x.Name+"."+m.Name] = ownSigFacts(m.Sig)
						}
					}
				}
			}
		}
		for name, want := range p.want {
			if strings.HasPrefix(name, "arg ") {
				continue
			}
			if got[name] != want {
				fail("%s: got %s, want %s", name, got[name], want)
			}
		}
		// A literal argument has no name: its facts are its checked
		// type's, found by its position.
		for lit, t := range pkg.Info.Types {
			fl, ok := lit.(*syntax.FuncLit)
			if !ok {
				continue
			}
			key := "arg " + litKey(p.src, fl)
			want, ok := p.want[key]
			if !ok {
				continue
			}
			s, _ := t.(*check.Signature)
			if have := ownTakesFacts(s); have != want {
				fail("%s: got %s, want %s", key, have, want)
			}
			delete(p.want, key)
		}
		for name := range p.want {
			if strings.HasPrefix(name, "arg ") {
				fail("%s: the checker gave the literal no type", name)
			}
		}
	}
	t.Logf("%d seeds reach: %v", len(seeds), reached)
	if len(seeds) > 1 {
		for _, k := range []string{"decl", "lit", "functype", "arg", "arg-own", "arg-inherit", "op", "self", "unnamed", "group", "fn-own", "named-own"} {
			if reached[k] == 0 {
				t.Errorf("no seed reaches %s", k)
			}
		}
	}
}

// ownGroup is one group of a generated parameter list: its type, its
// names (none in an unnamed list) and whether it is written own.
type ownGroup struct {
	typ   string
	names []string
	own   bool
}

// ownProg is a generated program, the model's facts by name ("arg L:C"
// for a literal argument at line L, column C) and the kinds it reached.
type ownProg struct {
	src     string
	want    map[string]string
	reached map[string]bool
}

func ownProgram(r *rand.Rand) ownProg {
	p := ownProg{want: map[string]string{}, reached: map[string]bool{}}
	var b strings.Builder
	b.WriteString("type R { n int } implements Closer\n\nfunc Close(r own R) {}\n\n")
	draw := func(named bool) []ownGroup {
		var gs []ownGroup
		k := 0
		for range 1 + r.IntN(3) {
			g := ownGroup{typ: []string{"R", "R", "func()", "func(x R)"}[r.IntN(4)], own: r.IntN(2) == 0}
			if named {
				for range 1 + r.IntN(3) {
					g.names = append(g.names, fmt.Sprintf("p%d", k))
					k++
				}
			}
			gs = append(gs, g)
		}
		if named && len(gs) > 1 && r.IntN(4) == 0 {
			p.reached["named-own"] = true
			gs = slices.Insert(gs, r.IntN(len(gs)+1), ownGroup{typ: "int", names: []string{"own"}})
		}
		return gs
	}
	// render spells gs and gives the model's facts of its signature.
	render := func(gs []ownGroup) (string, string, []int) {
		var parts, ps []string
		var takes []int
		i := 0
		for _, g := range gs {
			mode := ""
			if g.own {
				mode = "own "
			}
			fn := strings.HasPrefix(g.typ, "func")
			if g.names == nil {
				p.reached["unnamed"] = true
				parts = append(parts, mode+g.typ)
			} else {
				if len(g.names) > 1 && g.own {
					p.reached["group"] = true
				}
				parts = append(parts, strings.Join(g.names, ", ")+" "+mode+g.typ)
			}
			for range max(1, len(g.names)) {
				switch {
				case fn:
					if g.own {
						p.reached["fn-own"] = true
					}
					inner := "()"
					if g.typ == "func(x R)" {
						inner = "(R)"
					}
					ps = append(ps, fmt.Sprintf("func%s takes=[] self=false ne=%v", inner, !g.own))
				case g.own:
					takes = append(takes, i)
					ps = append(ps, g.typ)
				default:
					ps = append(ps, g.typ)
				}
				i++
			}
		}
		return "(" + strings.Join(parts, ", ") + ")", "(" + strings.Join(ps, ", ") + ")", takes
	}
	sig := func(ps string, takes []int, self bool) string {
		return fmt.Sprintf("%s takes=%v self=%v", ps, takes, self)
	}
	// rekeep is gs with own drawn again.
	rekeep := func(gs []ownGroup) []ownGroup {
		out := slices.Clone(gs)
		for i := range out {
			if out[i].typ != "int" {
				out[i].own = r.IntN(2) == 0
			}
		}
		return out
	}
	b.WriteString("type Ops {\n")
	for i := range 1 + r.IntN(3) {
		p.reached["op"] = true
		list, ps, takes := render(draw(r.IntN(3) > 0))
		self := r.IntN(3) == 0
		mode := ""
		if self {
			p.reached["self"] = true
			mode = "own "
		}
		fmt.Fprintf(&b, "\t%sM%d%s,\n", mode, i, list)
		p.want[fmt.Sprintf("Ops.M%d", i)] = sig(ps, takes, self)
	}
	b.WriteString("}\n\n")
	var body []string
	var decls strings.Builder
	type argLit struct {
		text string
		want string
	}
	var args []argLit
	for i := range 1 + r.IntN(3) {
		switch r.IntN(3) {
		case 0:
			p.reached["decl"] = true
			list, ps, takes := render(draw(true))
			fmt.Fprintf(&decls, "func f%d%s {}\n\n", i, list)
			p.want[fmt.Sprintf("f%d", i)] = sig(ps, takes, false)
		case 1:
			p.reached["functype"] = true
			gs := draw(true)
			list, ps, takes := render(gs)
			fmt.Fprintf(&decls, "func g%d(h func%s) {}\n\n", i, list)
			p.want[fmt.Sprintf("g%d", i)] = fmt.Sprintf("(func%s ne=true) takes=[] self=false", sig(ps, takes, false))
			// The literal argument: its own takes where it writes own on
			// an R parameter, else h's.
			lgs := rekeep(gs)
			llist, _, ltakes := render(lgs)
			writes := false
			for _, g := range lgs {
				writes = writes || g.own && g.typ == "R"
			}
			if writes {
				p.reached["arg-own"] = true
			} else {
				p.reached["arg-inherit"] = true
				ltakes = takes
			}
			p.reached["arg"] = true
			args = append(args, argLit{fmt.Sprintf("\tg%d(func%s {})\n", i, llist), fmt.Sprintf("takes=%v self=false", ltakes)})
		default:
			p.reached["lit"] = true
			list, ps, takes := render(draw(true))
			body = append(body, fmt.Sprintf("\tlet l%d = func%s {}\n\t_ = l%d\n", i, list, i))
			p.want[fmt.Sprintf("l%d", i)] = sig(ps, takes, false)
		}
	}
	b.WriteString(decls.String())
	line := strings.Count(b.String(), "\n") + 1
	b.WriteString("func main() {\n")
	line++
	for _, a := range args {
		// The literal starts after the tab and `gN(`, at column 5.
		p.want[fmt.Sprintf("arg %d:%d", line, 5)] = a.want
		b.WriteString(a.text)
		line++
	}
	for _, s := range body {
		b.WriteString(s)
	}
	b.WriteString("}\n")
	p.src = b.String()
	return p
}

// litKey is "L:C" of a function literal's position in src.
func litKey(src string, fl *syntax.FuncLit) string {
	off := int(fl.Pos())
	line := strings.Count(src[:off], "\n") + 1
	col := off - strings.LastIndex(src[:off], "\n")
	return fmt.Sprintf("%d:%d", line, col)
}

// ownSigFacts is a signature's parameters, each function-typed one with
// its non-escaping mark, its sorted takes and its receiver take.
func ownSigFacts(s *check.Signature) string {
	if s == nil {
		return "nil"
	}
	var ps []string
	for _, p := range s.Params {
		if f, ok := p.Type.(*check.Signature); ok {
			ps = append(ps, fmt.Sprintf("func%s ne=%v", ownSigFacts(f), f.NonEscaping))
			continue
		}
		ps = append(ps, strings.TrimPrefix(p.Type.String(), "main."))
	}
	return "(" + strings.Join(ps, ", ") + ") " + ownTakesFacts(s)
}

// ownTakesFacts is a signature's sorted takes and its receiver take.
func ownTakesFacts(s *check.Signature) string {
	if s == nil {
		return "nil"
	}
	takes := slices.Clone(s.Takes)
	slices.Sort(takes)
	if takes == nil {
		takes = []int{}
	}
	return fmt.Sprintf("takes=%v self=%v", takes, s.TakesSelf)
}
