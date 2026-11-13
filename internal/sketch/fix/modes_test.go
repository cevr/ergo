package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/format"
)

// TestOwnSpellsTheOldModes (EXM1; audit CSX §7, q168) is a seeded
// property of the mode word own. Each seed writes one program twice:
// in the old spelling (a takes clause, a bare takes on an operation,
// escaping before a function type) and in the new one (own before a
// parameter's type, before an unnamed parameter's type and before an
// operation). Every position is generated: a declaration, a function
// literal, a function type written as a parameter's type, and an
// operation, each with groups of one to three parameters of a resource
// type, int (own on it is S110 either way) and function types, each
// parameter kept or not at random, a law's parameter, and a field's and a
// result's function type, where escaping repeats the default. Then: ergo fix(old) is
// byte-equal to the new spelling; the old spelling has one diagnostic
// naming own per site fix rewrites, and beside them both check with the
// same diagnostics (code and line) and the same checked facts (each
// signature's takes, its receiver take, and each function parameter's
// escaping); format leaves the new spelling as written; and a second fix
// writes nothing. ERGO_SEED=<n>
// replays one seed; a failure prints both programs.
//
// Mutants it kills that no other test does (ledgers: syntax, check,
// fix): own on a receiver is dropped; own on a group marks only its first
// name; fix moves a takes name to the wrong parameter; own on a function
// value is read as a take; a retired takes or escaping checks clean.
func TestOwnSpellsTheOldModes(t *testing.T) {
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
	kinds := map[string]int{}
	for _, seed := range seeds {
		oldSrc, newSrc, reached := modesProgram(rand.New(rand.NewPCG(seed, 0xe11)))
		for k := range reached {
			kinds[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestOwnSpellsTheOldModes$' ./internal/sketch/fix\nold:\n%s\nnew:\n%s", seed, fmt.Sprintf(format, args...), seed, oldSrc, newSrc)
		}
		fixed, sites, _, err := fix.Modes(oldSrc)
		if err != nil {
			fail("fix: %v", err)
		}
		if fixed != newSrc {
			fail("fix(old) is not the new spelling:\n%s", fixed)
		}
		if again, n, _, _ := fix.Modes(newSrc); n != 0 || again != newSrc {
			fail("a second fix rewrites %d sites", n)
		}
		if out, err := format.Source(newSrc); err != nil || out != newSrc {
			fail("format gives (%v):\n%s", err, out)
		}
		oldFacts, oldDiags, retired := modeFacts(oldSrc)
		newFacts, newDiags, again := modeFacts(newSrc)
		if retired != sites || again != 0 {
			fail("%d diagnostics name own in the old spelling and %d in the new; fix rewrote %d sites", retired, again, sites)
		}
		if oldDiags != newDiags {
			fail("diagnostics differ:\nold %s\nnew %s", oldDiags, newDiags)
		}
		if oldFacts != newFacts {
			fail("facts differ:\nold %s\nnew %s", oldFacts, newFacts)
		}
	}
	t.Logf("%d seeds reach: %v", len(seeds), kinds)
	if len(seeds) > 1 {
		for _, k := range []string{"decl", "lit", "functype", "op", "self", "group-split", "group-all", "unnamed", "scalar", "escaping", "arg", "named-own", "field", "result", "law"} {
			if kinds[k] == 0 {
				t.Errorf("no seed reaches %s", k)
			}
		}
	}
}

// pgroup is one group of a generated parameter list: its type, its
// names (none in an unnamed list) and which of them the callee keeps.
type pgroup struct {
	typ   string
	names []string
	kept  []bool
}

// modesProgram writes one program in the old and in the new spelling,
// and the kinds of site it reached.
func modesProgram(r *rand.Rand) (string, string, map[string]bool) {
	reached := map[string]bool{}
	var o, n strings.Builder
	both := func(s string) { o.WriteString(s); n.WriteString(s) }
	both("type R { n int } implements Closer\n\n")
	o.WriteString("func Close(r R) takes r {}\n\n")
	n.WriteString("func Close(r own R) {}\n\n")
	// A field's and a result's function type is escaping already: fix
	// deletes the word there.
	if r.IntN(2) == 0 {
		reached["field"] = true
		o.WriteString("type Hold { run escaping func() int }\n\n")
		n.WriteString("type Hold { run func() int }\n\n")
	}
	// A law's parameter is a parameter: escaping there becomes own.
	if r.IntN(2) == 0 {
		reached["law"] = true
		o.WriteString("law Kept(g escaping func() int, h func() int) { g() == h() }\n\n")
		n.WriteString("law Kept(g own func() int, h func() int) { g() == h() }\n\n")
	}
	if r.IntN(2) == 0 {
		reached["result"] = true
		o.WriteString("func give() escaping func() {\n\tfunc() {}\n}\n\n")
		n.WriteString("func give() func() {\n\tfunc() {}\n}\n\n")
	}
	// draw writes a parameter list's shape, named or (only for function
	// types and operations) not. A named list of two groups or more may
	// hold a parameter named own, which stays a name.
	draw := func(named bool) []pgroup {
		var gs []pgroup
		k := 0
		for range 1 + r.IntN(3) {
			g := pgroup{typ: []string{"R", "int", "func()", "func(x R)"}[r.IntN(4)]}
			if !named {
				g.kept = []bool{r.IntN(2) == 0}
				gs = append(gs, g)
				continue
			}
			for range 1 + r.IntN(3) {
				g.names = append(g.names, fmt.Sprintf("p%d", k))
				k++
				g.kept = append(g.kept, r.IntN(2) == 0)
			}
			gs = append(gs, g)
		}
		if named && len(gs) > 1 && r.IntN(4) == 0 {
			reached["named-own"] = true
			at := r.IntN(len(gs) + 1)
			gs = slices.Insert(gs, at, pgroup{typ: "int", names: []string{"own"}, kept: []bool{false}})
		}
		return gs
	}
	// rekeep is gs with the kept parameters drawn again.
	rekeep := func(gs []pgroup) []pgroup {
		out := make([]pgroup, len(gs))
		for i, g := range gs {
			g.kept = slices.Clone(g.kept)
			for k := range g.kept {
				g.kept[k] = g.names == nil || g.names[0] != "own" && r.IntN(2) == 0
				if g.names == nil {
					g.kept[k] = r.IntN(2) == 0
				}
			}
			out[i] = g
		}
		return out
	}
	// render spells gs in the old and the new form, and the old clause.
	render := func(gs []pgroup) (string, string, string) {
		var ol, nl, taken []string
		for _, g := range gs {
			fn := strings.HasPrefix(g.typ, "func")
			if g.names == nil {
				if fn && g.kept[0] {
					reached["unnamed"], reached["escaping"] = true, true
					ol, nl = append(ol, "escaping "+g.typ), append(nl, "own "+g.typ)
				} else {
					ol, nl = append(ol, g.typ), append(nl, g.typ)
				}
				continue
			}
			names := strings.Join(g.names, ", ")
			if fn {
				// escaping qualifies the group's one type.
				if g.kept[0] {
					reached["escaping"] = true
					ol, nl = append(ol, names+" escaping "+g.typ), append(nl, names+" own "+g.typ)
				} else {
					ol, nl = append(ol, names+" "+g.typ), append(nl, names+" "+g.typ)
				}
				continue
			}
			ol = append(ol, names+" "+g.typ)
			all, some := true, false
			for i, keep := range g.kept {
				if keep {
					taken = append(taken, g.names[i])
					some = true
					if g.typ == "int" {
						reached["scalar"] = true
					}
				} else {
					all = false
				}
			}
			switch {
			case !some:
				nl = append(nl, names+" "+g.typ)
			case all:
				if len(g.names) > 1 {
					reached["group-all"] = true
				}
				nl = append(nl, names+" own "+g.typ)
			default:
				reached["group-split"] = true
				var parts, run []string
				runOwn := g.kept[0]
				flush := func() {
					mode := ""
					if runOwn {
						mode = "own "
					}
					parts = append(parts, strings.Join(run, ", ")+" "+mode+g.typ)
					run = nil
				}
				for i, name := range g.names {
					if g.kept[i] != runOwn {
						flush()
						runOwn = g.kept[i]
					}
					run = append(run, name)
				}
				flush()
				nl = append(nl, strings.Join(parts, ", "))
			}
		}
		// The clause names the taken parameters in any order.
		r.Shuffle(len(taken), func(i, j int) { taken[i], taken[j] = taken[j], taken[i] })
		clause := ""
		if len(taken) > 0 {
			clause = " takes " + strings.Join(taken, ", ")
		}
		return "(" + strings.Join(ol, ", ") + ")", "(" + strings.Join(nl, ", ") + ")", clause
	}
	// oneTake draws a named list whose old clause names one parameter at
	// most: in a parameter list or an argument a comma ends the clause.
	oneTake := func(gs []pgroup) ([]pgroup, string, string, string) {
		for {
			ol, nl, clause := render(gs)
			if !strings.Contains(clause, ",") {
				return gs, ol, nl, clause
			}
			gs = rekeep(gs)
		}
	}
	// An operation type: named operations, unnamed ones, and receivers.
	both("type Ops {\n")
	for i := range 1 + r.IntN(3) {
		ol, nl, clause := render(draw(r.IntN(3) > 0))
		reached["op"] = true
		if r.IntN(3) == 0 && clause == "" {
			reached["self"] = true
			fmt.Fprintf(&o, "\tM%d%s takes,\n", i, ol)
			fmt.Fprintf(&n, "\town M%d%s,\n", i, nl)
			continue
		}
		fmt.Fprintf(&o, "\tM%d%s%s,\n", i, ol, clause)
		fmt.Fprintf(&n, "\tM%d%s,\n", i, nl)
	}
	both("}\n\n")
	var mainOld, mainNew []string
	for i := range 1 + r.IntN(3) {
		switch r.IntN(3) {
		case 0:
			ol, nl, clause := render(draw(true))
			reached["decl"] = true
			fmt.Fprintf(&o, "func f%d%s%s {}\n\n", i, ol, clause)
			fmt.Fprintf(&n, "func f%d%s {}\n\n", i, nl)
		case 1:
			gs, ol, nl, clause := oneTake(draw(true))
			reached["functype"] = true
			fmt.Fprintf(&o, "func g%d(h func%s%s) {}\n\n", i, ol, clause)
			fmt.Fprintf(&n, "func g%d(h func%s) {}\n\n", i, nl)
			// A literal argument of the same parameters, kept anew: its
			// mode is its own where it writes one, else the parameter's.
			_, lo, ln, lc := oneTake(rekeep(gs))
			reached["arg"] = true
			mainOld = append(mainOld, fmt.Sprintf("\tg%d(func%s%s {})\n", i, lo, lc))
			mainNew = append(mainNew, fmt.Sprintf("\tg%d(func%s {})\n", i, ln))
		default:
			ol, nl, clause := render(draw(true))
			reached["lit"] = true
			mainOld = append(mainOld, fmt.Sprintf("\tlet l%d = func%s%s {}\n\t_ = l%d\n", i, ol, clause, i))
			mainNew = append(mainNew, fmt.Sprintf("\tlet l%d = func%s {}\n\t_ = l%d\n", i, nl, i))
		}
	}
	both("func main() {\n")
	o.WriteString(strings.Join(mainOld, ""))
	n.WriteString(strings.Join(mainNew, ""))
	both("}\n")
	return o.String(), n.String(), reached
}

// modeFacts are a program's checked facts of the modes, its diagnostics
// as code and line, and apart from them the number of diagnostics that
// name own for a retired spelling.
func modeFacts(src string) (string, string, int) {
	pkg, diags := check.CheckSource(src)
	var ds []string
	retired := 0
	for _, d := range diags {
		if d.Code == "S108" && strings.Contains(d.Message, " is gone: write own ") {
			retired++
			continue
		}
		ds = append(ds, fmt.Sprintf("%s:%d", d.Code, d.Line))
	}
	sort.Strings(ds)
	if pkg == nil {
		return "", strings.Join(ds, " "), retired
	}
	var facts []string
	for id, obj := range pkg.Info.Defs {
		switch x := obj.(type) {
		case *check.Func:
			facts = append(facts, fmt.Sprintf("%d %s %s", id.Pos(), x.Name, sigFacts(x.Sig)))
		case *check.Var:
			facts = append(facts, fmt.Sprintf("%d %s %s", id.Pos(), x.Name, typeFacts(x.Type)))
		case *check.TypeName:
			if n, ok := x.Type.(*check.Named); ok {
				if it, ok := n.Underlying().(*check.Interface); ok {
					for _, m := range it.Methods {
						facts = append(facts, fmt.Sprintf("%s.%s %s", x.Name, m.Name, sigFacts(m.Sig)))
					}
				}
			}
		}
	}
	// Positions shift between the spellings; the order of declaration
	// does not.
	sort.Slice(facts, func(i, j int) bool { return factKey(facts[i]) < factKey(facts[j]) })
	for i, f := range facts {
		if _, rest, ok := strings.Cut(f, " "); ok && f[0] >= '0' && f[0] <= '9' {
			facts[i] = rest
		}
	}
	return strings.Join(facts, "\n"), strings.Join(ds, " "), retired
}

// factKey orders facts by position, then by text.
func factKey(f string) string {
	if p, rest, ok := strings.Cut(f, " "); ok {
		if n, err := strconv.Atoi(p); err == nil {
			return fmt.Sprintf("0%010d %s", n, rest)
		}
	}
	return "1" + f
}

func sigFacts(s *check.Signature) string {
	if s == nil {
		return "nil"
	}
	var ps []string
	for _, p := range s.Params {
		ps = append(ps, typeFacts(p.Type))
	}
	takes := slices.Clone(s.Takes)
	slices.Sort(takes)
	return fmt.Sprintf("(%s) takes=%v self=%v", strings.Join(ps, ", "), takes, s.TakesSelf)
}

func typeFacts(t check.Type) string {
	if s, ok := t.(*check.Signature); ok {
		return fmt.Sprintf("func ne=%v %s", s.NonEscaping, sigFacts(s))
	}
	if t == nil {
		return "nil"
	}
	return t.String()
}
