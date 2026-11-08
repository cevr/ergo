package lower

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCatchTreeAgrees (M1b) is a generated property: a catch lowered
// through the match tree takes the same arm with the same bindings as the
// flat catch, both take the first arm whose pattern matches the error, and
// an error that no arm takes leaves the same way: a partial catch passes
// the same error value on through try, and a total catch re-panics with
// it (rows.md, Catch 5 and 6).
//
// A seeded generator writes groups (treeGen.catchGroup): an error union
// of 2 to 4 error types of 0 to 2 fields (int, string, bool, or a small
// union), whose types often share a field type, a show function per type
// (gGroup.decls), and a raise function that fails with the error it is
// given or returns "ok". Each catch has 1 to 6 arms of type tests,
// records, `as`, nested patterns and constants (treeGen.pat), and
// or-patterns over two error types that bind one name, or two names
// swapped, from fields of one type (`E0{F1: b0}, E1{F0: b0}`). It ends with
// `case _`, `case b0:` (the row value), one arm per error type it still
// misses, or nothing. The checker filters it: an arm it reports
// unreachable or caught twice is dropped, and a catch it reports not total
// is written again under try (partial), so it passes the rest on. Each arm
// yields its index and its bindings as text, and does so at one of three
// destinations: the catch's value, a return in the arm, or an arm in a
// loop that yields, continues or breaks.
//
// A reference model in Go takes the first arm that matches (gMatch.model).
// One program calls every catch with every error value (or 256 samples and
// a witness of each alternative) and with no error; a partial catch is
// called through a total catch whose `case err:` shows the error it passed
// on. It is lowered with the tree and flat (the hook lowerer.matchTest),
// built once, and both outputs must equal the model's.
//
// The foreign probe adds what Ergo cannot write: a Go error that is in no
// row, as an interruption or a defect is (Catch 6). The test patches each
// generated raise function in the emitted Go to return it, and calls each
// catch under recover: a total catch must panic with that very value, a
// partial catch must return it, and a catch-all arm must take it.
//
// Fixed probes add a partial catch in a loop whose arms continue and
// break, and a try inside a catch arm. -catch.seed=N runs the seed N
// alone. A failure prints the replay command, the catch's Ergo source, and
// its lines from the model, the tree and the flat program side by side.

var catchSeed = flag.Uint64("catch.seed", 0, "run TestCatchTreeAgrees on this seed alone")

// catchSeeds are the fixed seeds; each writes catchGroups groups.
var catchSeeds = []uint64{1, 2, 3, 4, 5, 6}

const (
	catchGroups  = 3 // groups per seed
	catchCatches = 3 // catches per group
)

// cCatch is a generated catch: a match on its group's error union (its one
// subject), and how it is written.
type cCatch struct {
	*gMatch
	group   *gGroup
	u       *gType
	partial bool
	acts    []int // per arm, destination 2: 0 yields, 1 continues, 2 breaks
}

// catchGroup writes a group: maybe a small union L for nested fields, and
// the error union E.
func (g *treeGen) catchGroup(prefix string) (*gGroup, *gType) {
	gr := &gGroup{prefix: prefix}
	var leaf []*gType
	if g.r.IntN(2) == 0 {
		leaf = append(leaf, g.union(gr, prefix+"L", nil))
	}
	un := &gUnion{name: prefix + "E"}
	for v := range 2 + g.r.IntN(3) {
		vr := &gVariant{name: fmt.Sprintf("%sV%d", un.name, v)}
		for f := range g.r.IntN(3) {
			var ft *gType
			switch {
			case f == 0 && v > 0 && len(un.variants[0].fields) > 0 && g.r.IntN(2) == 0:
				ft = un.variants[0].fields[0] // a field type two error types share
			case f == 1 && g.r.IntN(3) == 0:
				ft = vr.fields[0] // two fields of one type: two names to swap
			default:
				ft = g.field(leaf)
			}
			vr.fields = append(vr.fields, ft)
		}
		un.variants = append(un.variants, vr)
	}
	u := &gType{kind: gUnionKind, union: un}
	gr.types = append(gr.types, u)
	return gr, u
}

// raiseDecl is the group's raise function.
func catchRaiseDecl(gr *gGroup, u *gType) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func %sraise(e %s, ok bool) string fails %s {\n\tif ok {\n\t\treturn \"ok\"\n\t}\n\tmatch e {\n", gr.prefix, u.ergo(), catchRow(u))
	for _, vr := range u.union.variants {
		fmt.Fprintf(&b, "\tcase %s as x:\n\t\treturn x\n", vr.name)
	}
	b.WriteString("\t}\n}\n\n")
	return b.String()
}

// catchRow is the error row of u's variants.
func catchRow(u *gType) string {
	var names []string
	for _, vr := range u.union.variants {
		names = append(names, vr.name)
	}
	return strings.Join(names, " | ")
}

// catch generates a catch over the group's union u.
func (g *treeGen) catch(gr *gGroup, u *gType, id string) *cCatch {
	c := &cCatch{gMatch: &gMatch{id: id, subjects: []*gType{u}, dest: g.r.IntN(3)}, group: gr, u: u}
	for range 1 + g.r.IntN(6) {
		c.arms = append(c.arms, g.catchArm(u))
	}
	switch g.r.IntN(5) { // 3 and 4 add nothing: most such catches are partial
	case 0:
		c.arms = append(c.arms, &gArm{wild: true})
	case 1:
		c.arms = append(c.arms, &gArm{alts: [][]*gPat{{{kind: pBind, t: u, name: "b0"}}}, names: []string{"b0"}})
	case 2:
		missed := g.missed(c.gMatch)
		for v := range u.union.variants {
			p := &gPat{kind: pTest, t: u, variant: v}
			for _, vs := range missed {
				if p.matches(vs[0], map[string]gVal{}) {
					c.arms = append(c.arms, &gArm{alts: [][]*gPat{{p}}})
					break
				}
			}
		}
	}
	for range c.arms {
		c.acts = append(c.acts, []int{0, 0, 1, 2}[g.r.IntN(4)])
	}
	return c
}

// catchArm is an arm that tests the error: a pattern (never `_` or a lone
// name: the generator adds those last), or an or-pattern of two.
func (g *treeGen) catchArm(u *gType) *gArm {
	for {
		a := &gArm{}
		if g.r.IntN(3) != 0 {
			p := g.pat(u, 0, &a.names)
			if p.kind == pWild || p.kind == pBind {
				continue
			}
			a.alts = [][]*gPat{{p}}
			return a
		}
		alts := [2]*gPat{g.pat(u, 0, nil), g.pat(u, 0, nil)}
		if alts[0].kind == pWild || alts[1].kind == pWild {
			continue
		}
		if k := g.r.IntN(3); k > 0 {
			if b := g.catchBinds(u, k); b != nil {
				alts = *b
				a.names = []string{"b0", "b1"}[:k]
			}
		}
		a.alts = [][]*gPat{{alts[0]}, {alts[1]}}
		return a
	}
}

// catchBinds is an or-pattern over two error types that binds n names
// from fields of one type: one name (`V0{F1: b0}, V2{F0: b0}`), or two
// swapped (`V0{F0: b0, F1: b1}, V1{F0: b1, F1: b0}`); nil when the union has
// no such fields.
func (g *treeGen) catchBinds(u *gType, n int) *[2]*gPat {
	type fieldAt struct{ v, f int }
	var pairs [][2]fieldAt // fields of one type in two variants
	var twins [][2]fieldAt // per variant, two fields of one type
	vs := u.union.variants
	for v, vr := range vs {
		for f, ft := range vr.fields {
			for w := v + 1; w < len(vs); w++ {
				for h, ht := range vs[w].fields {
					if gSameType(ft, ht) {
						pairs = append(pairs, [2]fieldAt{{v, f}, {w, h}})
					}
				}
			}
			for h := f + 1; h < len(vr.fields); h++ {
				if gSameType(ft, vr.fields[h]) {
					twins = append(twins, [2]fieldAt{{v, f}, {v, h}})
				}
			}
		}
	}
	rec := func(v int, fields map[int]*gPat) *gPat {
		return &gPat{kind: pRecord, t: u, variant: v, fields: fields}
	}
	bind := func(t *gType, name string) *gPat { return &gPat{kind: pBind, t: t, name: name} }
	if n == 1 && len(pairs) > 0 {
		p := pairs[g.r.IntN(len(pairs))]
		if g.r.IntN(2) == 0 {
			p[0], p[1] = p[1], p[0]
		}
		ft := vs[p[0].v].fields[p[0].f]
		return &[2]*gPat{rec(p[0].v, map[int]*gPat{p[0].f: bind(ft, "b0")}), rec(p[1].v, map[int]*gPat{p[1].f: bind(ft, "b0")})}
	}
	if n == 2 {
		var swaps [][2][2]fieldAt
		for _, a := range twins {
			for _, b := range twins {
				if a[0].v != b[0].v && gSameType(vs[a[0].v].fields[a[0].f], vs[b[0].v].fields[b[0].f]) {
					swaps = append(swaps, [2][2]fieldAt{a, b})
				}
			}
		}
		if len(swaps) > 0 {
			s := swaps[g.r.IntN(len(swaps))]
			ft := vs[s[0][0].v].fields[s[0][0].f]
			return &[2]*gPat{
				rec(s[0][0].v, map[int]*gPat{s[0][0].f: bind(ft, "b0"), s[0][1].f: bind(ft, "b1")}),
				rec(s[1][0].v, map[int]*gPat{s[1][0].f: bind(ft, "b1"), s[1][1].f: bind(ft, "b0")}),
			}
		}
	}
	return nil
}

// fn is the Ergo text of the catch's function; it records each arm's case
// line and its second alternative's column.
func (c *cCatch) fn() string {
	var b strings.Builder
	line := 1
	w := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		line += strings.Count(s, "\n")
		b.WriteString(s)
	}
	fails := ""
	call := c.group.prefix + "raise(e, ok)"
	if c.partial {
		fails = " fails " + catchRow(c.u)
		call = "try " + call
	}
	w("func %s(e %s, ok bool) string%s {\n", c.id, c.u.ergo(), fails)
	in := "\t"
	switch c.dest {
	case 0:
		w("\tlet v = %s catch {\n", call)
	case 1:
		w("\t%s catch {\n", call)
	case 2:
		w("\tlet mut out = \"\"\n\tfor x in []int{0, 1} {\n\t\tlet v = %s catch {\n", call)
		in = "\t\t"
	}
	for k, a := range c.arms {
		a.line = line
		if a.wild {
			w("%scase _:\n", in)
		} else {
			var alts []string
			for _, alt := range a.alts {
				alts = append(alts, alt[0].ergo())
			}
			if len(alts) == 2 {
				a.alt2 = len(in+"case "+alts[0]+", ") + 1
			}
			w("%scase %s:\n", in, strings.Join(alts, ", "))
		}
		val := strconv.Quote(strconv.Itoa(k))
		if !a.wild {
			bs := binders(a.alts[0])
			for _, n := range a.names {
				val += " + \":\" + " + showCall(bs[n], n)
			}
		}
		switch {
		case c.dest == 0:
			w("%s\t%s\n", in, val)
		case c.dest == 1:
			w("%s\treturn %s\n", in, val)
		case c.acts[k] == 0:
			w("%s\t%s\n", in, val)
		default:
			w("%s\tout = out + %s\n%s\t%s\n", in, val, in, []string{"", "continue", "break"}[c.acts[k]])
		}
	}
	w("%s}\n", in)
	switch c.dest {
	case 0:
		w("\tv\n")
	case 1:
		w("\t\"ok\"\n")
	case 2:
		w("\t\tout = out + v + strconv.Itoa(x)\n\t}\n\tout\n")
	}
	w("}\n")
	if c.partial {
		fmt.Fprintf(&b, "\nfunc o%s(e %s, ok bool) string {\n\t%s(e, ok) catch {\n\tcase err:\n\t\t\"rest:\" + show%s(err)\n\t}\n}\n", c.id, c.u.ergo(), c.id, c.u.union.name)
	}
	return b.String()
}

// render is the line the catch's function returns when the arm k yields
// val.
func (c *cCatch) render(k int, val string) string {
	if c.dest != 2 {
		return val
	}
	out := ""
	for x := range 2 {
		switch c.acts[k] {
		case 0:
			out += val + strconv.Itoa(x)
		case 1:
			out += val
		case 2:
			return out + val
		}
	}
	return out
}

// line is the model's line for the error value v.
func (c *cCatch) line(v gVal) (string, bool) {
	val, ok := c.model([]gVal{v})
	if !ok {
		if c.partial {
			return "rest:" + v.show(), true
		}
		return "", false
	}
	k, _ := strconv.Atoi(strings.SplitN(val, ":", 2)[0])
	return c.render(k, val), true
}

// okLine is the model's line when raise does not fail.
func (c *cCatch) okLine() string {
	if c.dest == 2 {
		return "ok0ok1"
	}
	return "ok"
}

// foreignLine is the model's line for a Go error in no row: a `case _`
// arm takes it; a `case b0:` arm takes it and its show of the row value
// panics (the value is not of the union); else a total catch re-panics
// with it and a partial catch returns it.
func (c *cCatch) foreignLine() string {
	for k, a := range c.arms {
		if a.wild {
			return "value " + c.render(k, strconv.Itoa(k))
		}
		if len(a.alts) == 1 && a.alts[0][0].kind == pBind {
			return "panic same=false"
		}
	}
	if c.partial {
		return "error same=true text=catch foreign"
	}
	return "panic same=true text=catch foreign"
}

// catchFilter checks the catch with its group's declarations: it drops an
// arm (or an or-pattern's alternative) the checker reports unreachable or
// caught twice, and writes the catch under try when the checker reports it
// not total. It reports false when no arm is left. Any other diagnostic is
// a generator defect.
func catchFilter(t *testing.T, pre string, c *cCatch) (dropped int, ok bool) {
	for {
		fn := c.fn()
		src := pre + fn + "\nfunc main() {}\n"
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("generator defect (%s): %v\n%s", c.id, err, src)
		}
		_, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) == 0 {
			return dropped, true
		}
		first := strings.Count(pre, "\n") + 1
		drop, alt := -1, -1
		retry := false
		for _, d := range diags {
			switch {
			case d.Code == check.CodeNotTotal && !c.partial:
				c.partial, retry = true, true
			case d.Code == "S108" && (strings.Contains(d.Message, "unreachable") || strings.Contains(d.Message, "caught twice")):
				if drop >= 0 {
					continue // one at a time: dropping it may change the rest
				}
				for k, a := range c.arms {
					if first+a.line-1 == d.Line {
						drop = k
						if len(a.alts) == 2 && d.Column >= a.alt2 {
							alt = 1
						} else if len(a.alts) == 2 {
							alt = 0
						}
					}
				}
				if drop < 0 {
					t.Fatalf("generator defect (%s): S108 at line %d is on no case: %v\n%s", c.id, d.Line, d, src)
				}
			default:
				t.Fatalf("generator defect (%s): %v\n%s", c.id, d, src)
			}
		}
		if retry {
			continue
		}
		if alt >= 0 {
			// An or-pattern's unreachable alternative: keep the other.
			a := c.arms[drop]
			a.alts = append(a.alts[:alt:alt], a.alts[alt+1:]...)
			continue
		}
		c.arms = append(c.arms[:drop:drop], c.arms[drop+1:]...)
		c.acts = append(c.acts[:drop:drop], c.acts[drop+1:]...)
		dropped++
		if len(c.arms) == 0 {
			return dropped, false
		}
	}
}

// catchProbes are the fixed probes over catchShapeDecls: a partial catch
// in a loop whose arms continue and break (one shared by an or-pattern),
// and a try inside a catch arm. catchProbeOut is their output.
const catchProbes = `
func loopProbe(ks []int) string fails Frozen | Expired {
	let mut out = ""
	for k in ks {
		let v = try raise(k) catch {
		case Declined{Reason: "card"}:
			out = out + "c"
			continue
		case Declined{Reason: r}, Refused{Reason: r}:
			out = out + r
			break
		}
		out = out + v
	}
	out
}

func nestedProbe(k int, j int) string fails Declined | Refused | Frozen | Expired {
	let v = try raise(k) catch {
	case Declined{Reason: "card"}:
		let w = try raise(j) catch {
		case Declined{Code: 2}, Expired{Days: 9}:
			"in"
		case Refused{Reason: r}:
			r
		}
		"card+" + w
	case Declined{Reason: r}, Refused{Reason: r}:
		"r:" + r
	}
	v
}

func showRest(k int, j int) string {
	nestedProbe(k, j) catch {
	case Declined{Reason: r}:
		"D" + r
	case Refused:
		"R"
	case Frozen:
		"F"
	case Expired{Days: d}:
		"E" + strconv.Itoa(d)
	}
}

func showLoop(ks []int) string {
	loopProbe(ks) catch {
	case Frozen:
		"F"
	case Expired{Days: d}:
		"E" + strconv.Itoa(d)
	}
}

func probes() needs Console {
	Console.Println("probe|" + showLoop([]int{9, 0, 9, 2, 9}))
	Console.Println("probe|" + showLoop([]int{0, 1, 9}))
	Console.Println("probe|" + showLoop([]int{9, 3, 0}))
	Console.Println("probe|" + showLoop([]int{0, 5}))
	Console.Println("probe|" + showRest(0, 9) + "," + showRest(0, 1) + "," + showRest(0, 5) + "," + showRest(0, 2) + "," + showRest(0, 3) + "," + showRest(0, 0))
	Console.Println("probe|" + showRest(1, 9) + "," + showRest(2, 9) + "," + showRest(3, 9) + "," + showRest(4, 9) + "," + showRest(9, 0))
}
`

const catchProbeOut = "probe|okcokfraud\nprobe|climit\nprobe|F\nprobe|E9\n" +
	"probe|card+ok,card+in,card+in,card+fraud,F,Dcard\n" +
	"probe|r:limit,r:fraud,F,E0,ok\n"

// catchForeignGo is the Go the foreign probe adds to the emitted program.
const catchForeignGo = `
type catchForeignT struct{}

func (*catchForeignT) Error() string { return "catch foreign" }

var catchForeignErr error = &catchForeignT{}

var catchForeignOn bool

func catchForeignRun(id string, f func() string) {
	out := func() (s string) {
		defer func() {
			if r := recover(); r != nil {
				s = "panic same=" + catchStrconv.FormatBool(r == any(catchForeignErr))
				if r == any(catchForeignErr) {
					s += " text=" + r.(error).Error()
				}
			}
		}()
		return f()
	}()
	catchOS.Stdout.WriteString(id + "|foreign => " + out + "\n")
}
`

// catchForeignPatch adds the foreign probe to the emitted main.go src:
// each generated raise function fails with catchForeignErr while the probe
// runs, and after the Ergo main the probe calls each catch.
func catchForeignPatch(t *testing.T, src string, catches []*cCatch) string {
	t.Helper()
	pkg := "\npackage main\n"
	at := strings.Index(src, pkg)
	if at < 0 {
		t.Fatalf("emitted Go has no line %q", pkg)
	}
	at += len(pkg)
	src = src[:at] + "import catchOS \"os\"\nimport catchStrconv \"strconv\"\n" + src[at:]
	raised := map[string]bool{}
	var calls strings.Builder
	for _, c := range catches {
		if !raised[c.group.prefix] {
			raised[c.group.prefix] = true
			head := fmt.Sprintf("func %sraise(env *Env, e %s, ok bool) (string, error) {\n", c.group.prefix, c.u.ergo())
			if strings.Count(src, head) != 1 {
				t.Fatalf("the foreign probe finds %q %d times in the emitted Go", head, strings.Count(src, head))
			}
			src = strings.Replace(src, head, head+"\tif catchForeignOn {\n\t\treturn \"\", catchForeignErr\n\t}\n", 1)
		}
		arg := c.u.union.variants[0].name + "{}"
		if c.partial {
			fmt.Fprintf(&calls, "\tcatchForeignRun(%q, func() string {\n\t\tv, err := %s(env, %s, false)\n\t\tif err != nil {\n\t\t\treturn \"error same=\" + catchStrconv.FormatBool(err == catchForeignErr) + \" text=\" + err.Error()\n\t\t}\n\t\treturn \"value \" + v\n\t})\n", c.id, c.id, arg)
		} else {
			fmt.Fprintf(&calls, "\tcatchForeignRun(%q, func() string { return \"value \" + %s(env, %s, false) })\n", c.id, c.id, arg)
		}
	}
	entry := "\t\tmain_1(env)\n"
	if strings.Count(src, entry) != 1 {
		t.Fatalf("the foreign probe finds %q %d times in the emitted Go", entry, strings.Count(src, entry))
	}
	src = strings.Replace(src, entry, entry+"\t\tcatchForeign(env)\n", 1)
	return src + catchForeignGo + "\nfunc catchForeign(env *Env) {\n\tcatchForeignOn = true\n" + calls.String() + "\tcatchForeignOn = false\n}\n"
}

func TestCatchTreeAgrees(t *testing.T) {
	if matchLowering == "flat" {
		t.Skip("matchLowering=flat: the build has no tree to compare")
	}
	seeds := catchSeeds
	if *catchSeed != 0 {
		seeds = []uint64{*catchSeed}
	}
	var src, want, foreign strings.Builder
	src.WriteString(treeShowPrelude + catchShapeDecls + catchProbes)
	want.WriteString(catchProbeOut)
	var runs []string
	var catches []*cCatch
	fns := map[string]string{} // catch id to its function, for failures
	generated, dropped, partial, regenerated := 0, 0, 0, 0
	for _, seed := range seeds {
		g := &treeGen{r: rand.New(rand.NewPCG(seed, 0x6d31b)), seed: seed}
		for gi := range catchGroups {
			gr, u := g.catchGroup(fmt.Sprintf("C%dG%d", seed, gi))
			decls := gr.decls() + catchRaiseDecl(gr, u)
			src.WriteString(decls)
			for ci := range catchCatches {
				id := fmt.Sprintf("c%dg%dm%d", seed, gi, ci)
				var c *cCatch
				for attempt := 0; ; attempt++ {
					if attempt == 50 {
						t.Fatalf("seed %d: no catch in 50 attempts for %s", seed, id)
					}
					c = g.catch(gr, u, id)
					n := len(c.arms)
					generated += n
					d, ok := catchFilter(t, treeShowPrelude+decls, c)
					if ok {
						dropped += d
						break
					}
					dropped += n
					regenerated++
				}
				if c.partial {
					partial++
				}
				catches = append(catches, c)
				fn := c.fn()
				fns[id] = fn
				src.WriteString(fn + "\n")
				call := id
				if c.partial {
					call = "o" + id
				}
				run := "run" + id
				runs = append(runs, run)
				fmt.Fprintf(&src, "func %s() needs Console {\n", run)
				vals := g.values(c.gMatch)
				fmt.Fprintf(&src, "\tConsole.Println(%s + %s(%s, true))\n", strconv.Quote(id+"|ok => "), call, vals[0][0].ergo())
				want.WriteString(id + "|ok => " + c.okLine() + "\n")
				for _, vs := range vals {
					line, ok := c.line(vs[0])
					if !ok {
						t.Fatalf("seed %d: the checker reports %s total, but no arm takes %v\n%s", seed, id, vs[0].ergo(), fn)
					}
					in := vs[0].ergo()
					fmt.Fprintf(&src, "\tConsole.Println(%s + %s(%s, false))\n", strconv.Quote(id+"|"+in+" => "), call, in)
					want.WriteString(id + "|" + in + " => " + line + "\n")
				}
				src.WriteString("}\n\n")
				foreign.WriteString(id + "|foreign => " + c.foreignLine() + "\n")
			}
		}
	}
	want.WriteString(foreign.String())
	src.WriteString("func main() needs Console {\n\tprobes()\n")
	for _, r := range runs {
		src.WriteString("\t" + r + "()\n")
	}
	src.WriteString("}\n")
	rate := float64(dropped) / float64(generated)
	t.Logf("seeds %v: %d catches (%d partial; %d generated again), %d of %d generated arms dropped by the checker (%.0f%%), %d lines", seeds, len(catches), partial, regenerated, dropped, generated, 100*rate, strings.Count(want.String(), "\n"))
	if rate > 0.5 && *catchSeed == 0 {
		t.Errorf("drop rate %.0f%% is over 50%%: a generator defect", 100*rate)
	}
	if partial == 0 || partial == len(catches) {
		t.Errorf("%d of %d catches are partial: the generator must write both kinds", partial, len(catches))
	}

	tree := lowerSourceMode("tree", src.String(), false)
	flat := lowerSourceMode("flat", src.String(), true)
	for _, o := range []*lowered{tree.lowered, flat.lowered} {
		if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
			t.Fatalf("%s: err %v, diags %v, panic %v, refused %v", o.name, o.err, firstN(o.diags, 5), o.panicked, o.refused)
		}
	}
	// Every generated catch that is not a simple type switch takes the
	// tree in the tree program, and the flat switch in the flat one.
	treeFlat, flatFlat := catchFlatFuncs(t, tree.mainGo()), catchFlatFuncs(t, flat.mainGo())
	if len(treeFlat) > 0 {
		t.Errorf("the tree program lowers %d generated catches flat: %v", len(treeFlat), firstN(treeFlat, 5))
	}
	t.Logf("the flat program lowers %d of %d generated catches flat (the rest by the type-switch fast path)", len(flatFlat), len(catches))
	if len(flatFlat) == 0 {
		t.Errorf("the hook does not force the flat catch")
	}
	for _, k := range []string{"ctor", "const"} {
		if tree.hook.splits[k] == 0 {
			t.Errorf("no generated catch makes the tree split a %s column (splits %v)", k, tree.hook.splits)
		}
	}
	if len(tree.hook.fallbacks) > 0 {
		t.Errorf("the tree gives up on %v", tree.hook.fallbacks)
	}
	t.Logf("tree splits: %v", tree.hook.splits)
	// The paths a catch adds to the tree are all reached: a shared arm, an
	// error passed on, the re-panic of a total catch, a catch-all arm.
	if gotos := strings.Count(stripDirectives(tree.mainGo()), "\tgoto "); gotos == 0 {
		t.Errorf("no generated catch shares an arm (goto)")
	}
	w := want.String()
	for _, reach := range []string{" => rest:", "=> panic same=true", "=> error same=true", "=> value "} {
		if !strings.Contains(w, reach) {
			t.Errorf("no line of the model holds %q", reach)
		}
	}
	for _, o := range []*lowered{tree.lowered, flat.lowered} {
		for i, f := range o.files {
			if f.Path == "main.go" {
				o.files[i].Data = []byte(catchForeignPatch(t, string(f.Data), catches))
			}
		}
	}
	exes := buildAll(t, []*lowered{tree.lowered, flat.lowered}, true)
	if exes == nil {
		t.FailNow()
	}
	outs := map[string]string{}
	for _, o := range []*lowered{tree.lowered, flat.lowered} {
		stdout, stderr, code := runExe(t, exes[o.name])
		outs[o.name] = stdout
		if code != 0 {
			t.Errorf("%s exited %d: %s", o.name, code, firstLines(stderr, 20))
		}
	}
	for _, name := range []string{"tree", "flat"} {
		if outs[name] == w {
			continue
		}
		got, exp := strings.Split(outs[name], "\n"), strings.Split(w, "\n")
		for i := range max(len(got), len(exp)) {
			var gl, el string
			if i < len(got) {
				gl = got[i]
			}
			if i < len(exp) {
				el = exp[i]
			}
			if gl == el {
				continue
			}
			id, _, _ := strings.Cut(el, "|")
			seed := strings.TrimPrefix(strings.SplitN(id, "g", 2)[0], "c")
			t.Errorf("%s: line %d is %q, the model's %q.\nReplay: go test -run '^TestCatchTreeAgrees$' ./internal/sketch/lower -args -catch.seed=%s\nThe catch:\n%s\nIts lines (model | tree | flat):\n%s",
				name, i+1, gl, el, seed, fns[id], treeLinesOf(id, w, outs["tree"], outs["flat"]))
			break
		}
	}
}

// catchFlatFuncs lists the generated catch functions (c…g…m…) of the
// emitted Go that hold an expressionless switch: the flat catch.
func catchFlatFuncs(t *testing.T, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatalf("emitted Go does not parse: %v", err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !catchFunc.MatchString(fd.Name.Name) {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if s, ok := n.(*ast.SwitchStmt); ok && s.Tag == nil {
				out = append(out, fd.Name.Name)
				return false
			}
			return true
		})
	}
	return out
}

var catchFunc = regexp.MustCompile(`^c\d+g\d+m\d+$`)
