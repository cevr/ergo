package fix

import (
	"flag"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

var fixSeed = flag.Int64("fix.seed", 0, "replay one seed of TestFixProperty")

// TestFixProperty is EXPLICIT §4's property against a least-row model of
// a generated program: call chains, recursive groups, exported functions,
// leaves whose written rows stay, operation-type capabilities, methods,
// two failure kinds per function, results of function type, takes
// hand-offs, and a generic codec enc[T wire] whose needs row is wired[T]
// and whose instance enc[Lv] is charged Lv's codec needs (q141). A random
// subset of the clauses is deleted, wholly or in part. Checked: fix then
// check has no clause diagnostic and the same other diagnostics as the
// fully written program; each written row is the model's least row; fix
// twice is fix once; fix keeps the formatter's layout and changes bytes
// only on signature and import lines; the rounds stay within Bound.
// Mutants (ledger in testdata/mutants.json): drop one element of a
// clause, write needs for fails, skip the re-check loop, insert after
// takes, skip the import, reorder written elements, leave a
// function-typed result unparenthesized; and in the checker (ledger in
// ../check/testdata/mutants.json) a lent hand-off with no takes fix, an
// encode of a type parameter not charged wired[T], and an instance whose
// wired[T] is not replaced by its argument's codec needs.
func TestFixProperty(t *testing.T) {
	seeds := 60
	first := int64(1)
	if *fixSeed != 0 {
		first, seeds = *fixSeed, 1
	}
	reached := map[string]int{}
	for s := first; s < first+int64(seeds); s++ {
		g := generate(rand.New(rand.NewSource(s)))
		for _, what := range checkSeed(t, s, g) {
			reached[what]++
		}
	}
	// Every case the property claims is reached by some seed.
	if *fixSeed == 0 {
		for _, what := range []string{"import added", "second round", "clause extended", "exported widened", "result opened", "takes written", "takes through a call", "needs wired written", "needs wired written for a direct encode", "codec instance charged", "codec instance charged by ParseLv"} {
			if reached[what] == 0 {
				t.Errorf("no seed reached %q: %v", what, reached)
			}
		}
	}
}

// program is a generated program: its functions and the model's rows.
type program struct {
	errs, caps int
	funcs      []*gfunc
	// ext is mid.Mid of a second package, which fails lib.LE of a third
	// the main package does not import; nil for a one-file program.
	ext *gfunc
	// midWritten is whether mid.Mid writes its fails clause.
	midWritten bool
	// codecCap is the capability Lv's codec needs when the program has the
	// generic codec enc (the last function); -1 for none. onParse: ParseLv,
	// not Wire, requires it; an encode is charged both directions' needs
	// (wired[T] in a needs row is one row for both, q141).
	codecCap int
	onParse  bool
	// direct: enc calls the builtin encode on its T, which charges
	// wired[T] itself; otherwise it calls json.Encode, whose instance does.
	direct bool
}

// callee is the function a call index names: ext past the last function.
func (p *program) callee(j int) *gfunc {
	if j == p.extAt() {
		return p.ext
	}
	if j > p.extAt() {
		return p.funcs[j-1]
	}
	return p.funcs[j]
}

// extAt is the call index that names ext: one past the generated
// functions, before enc.
func (p *program) extAt() int {
	if p.codecCap >= 0 {
		return len(p.funcs) - 1
	}
	return len(p.funcs)
}

type gfunc struct {
	name     string
	method   bool // a function on R (first parameter), called as R{}.name()
	leafErr  int  // a leaf's own failure, written and never deleted; -1 for none
	ownCaps  []int
	calls    []int
	written  [2]map[string]bool // after deletion: fails, needs
	order    [2][]string        // written elements in written order
	least    [2]map[string]bool // the model's least rows
	takesArg bool               // the function has a parameter x Box, a resource
	// handoff is what the body does with x: 0 nothing (lent), 1 Close(x),
	// 2 passes it to the call passAt, whose callee has x too. leastTakes
	// is the model's least takes clause; writtenTakes whether it is
	// written after deletion (never beyond the least).
	handoff                  int
	passAt                   int
	leastTakes, writtenTakes bool
	withCaps                 []int  // capabilities used inside a with that provides them
	lit                      []bool // per call: through a local function literal
	// funcResult: the result is func() int, which takes a clause written
	// after it as its own row unless it is in parentheses.
	funcResult bool
	// codec: this is enc[T wire](t T), which encodes t; its least needs
	// row is wired[T], and a call enc(Lv{...}) is charged Lv's codec needs.
	codec bool
}

// callNeeds is the needs a call of g charges: an instance of enc replaces
// wired[T] by Lv's codec needs.
func (p *program) callNeeds(g *gfunc) map[string]bool {
	if g.codec {
		return map[string]bool{capName(p.codecCap): true}
	}
	return g.least[1]
}

func errName(i int) string { return fmt.Sprintf("E%d", i) }
func capName(i int) string { return fmt.Sprintf("C%d", i) }

func generate(r *rand.Rand) *program {
	p := &program{errs: 1 + r.Intn(3), caps: 1 + r.Intn(3), codecCap: -1}
	if r.Intn(2) == 0 {
		p.ext = &gfunc{name: "mid.Mid", least: [2]map[string]bool{{"lib.LE": true}, {}}}
		p.midWritten = r.Intn(2) == 0
	}
	n := 2 + r.Intn(7)
	for i := 0; i < n; i++ {
		f := &gfunc{leafErr: -1}
		switch {
		case r.Intn(4) == 0:
			f.name = fmt.Sprintf("F%d", i)
		case r.Intn(5) == 0:
			f.name = fmt.Sprintf("m%d", i)
			f.method = true
		default:
			f.name = fmt.Sprintf("f%d", i)
		}
		if r.Intn(3) == 0 {
			f.leafErr = r.Intn(p.errs)
		}
		for c := 0; c < p.caps; c++ {
			if r.Intn(4) == 0 {
				f.ownCaps = append(f.ownCaps, c)
			}
		}
		for k := r.Intn(3); k > 0; k-- {
			j := r.Intn(n) // any function: cycles too
			if p.ext != nil && r.Intn(3) == 0 {
				j = n
			}
			f.calls = append(f.calls, j)
		}
		f.takesArg = r.Intn(3) == 0
		for c := 0; c < p.caps; c++ {
			if r.Intn(5) == 0 {
				f.withCaps = append(f.withCaps, c)
			}
		}
		for range f.calls {
			f.lit = append(f.lit, r.Intn(4) == 0)
		}
		f.funcResult = r.Intn(4) == 0
		p.funcs = append(p.funcs, f)
	}
	if p.ext != nil && !slices.ContainsFunc(p.funcs, func(f *gfunc) bool { return slices.Contains(f.calls, n) }) {
		p.funcs[0].calls = append(p.funcs[0].calls, n)
		p.funcs[0].lit = append(p.funcs[0].lit, false)
	}
	// The generic codec: enc after the others (so a call index past n
	// still names ext), called from one function or more.
	if r.Intn(2) == 0 {
		p.codecCap, p.onParse, p.direct = r.Intn(p.caps), r.Intn(2) == 0, r.Intn(2) == 0
		enc := &gfunc{name: "enc", leafErr: -1, codec: true}
		p.funcs = append(p.funcs, enc)
		for i, f := range p.funcs[:n] {
			if i == 0 || r.Intn(3) == 0 {
				f.calls = append(f.calls, n+1)
				f.lit = append(f.lit, r.Intn(4) == 0)
			}
		}
	}
	// The hand-offs of x: Close, or one call (not through a literal, which
	// may run more than once) of a function that has x too.
	for _, f := range p.funcs {
		f.passAt = -1
		if !f.takesArg {
			continue
		}
		f.handoff = r.Intn(3)
		if f.handoff == 2 {
			var at []int
			for i, j := range f.calls {
				if j < n && p.funcs[j].takesArg && !f.lit[i] {
					at = append(at, i)
				}
			}
			if len(at) == 0 {
				f.handoff = 1
			} else {
				f.passAt = at[r.Intn(len(at))]
			}
		}
	}
	// The least takes clauses: Close takes x; a pass takes it when the
	// callee takes its x, to a fixpoint from none.
	for changed := true; changed; {
		changed = false
		for _, f := range p.funcs {
			take := f.handoff == 1 || f.handoff == 2 && p.funcs[f.calls[f.passAt]].leastTakes
			if take && !f.leastTakes {
				f.leastTakes, changed = true, true
			}
		}
	}
	for _, f := range p.funcs {
		f.writtenTakes = f.leastTakes && r.Intn(3) == 0
	}
	// The least rows: the leaf's failure and own capabilities, joined with
	// every callee's, to a fixpoint.
	for _, f := range p.funcs {
		f.least = [2]map[string]bool{{}, {}}
		if f.codec {
			f.least[1]["wired[T]"] = true
		}
		if f.leafErr >= 0 {
			f.least[0][errName(f.leafErr)] = true
		}
		for _, c := range f.ownCaps {
			f.least[1][capName(c)] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range p.funcs {
			for _, j := range f.calls {
				for k := 0; k < 2; k++ {
					from := p.callee(j).least[k]
					if k == 1 {
						from = p.callNeeds(p.callee(j))
					}
					for e := range from {
						if !f.least[k][e] {
							f.least[k][e] = true
							changed = true
						}
					}
				}
			}
		}
	}
	// Delete: a leaf keeps its failure; every other clause loses all, some
	// or none of its elements.
	for _, f := range p.funcs {
		for k := 0; k < 2; k++ {
			f.written[k] = map[string]bool{}
			for _, e := range sortedKeys(f.least[k]) {
				keep := false
				switch {
				case k == 0 && f.leafErr >= 0 && e == errName(f.leafErr):
					keep = true
				default:
					keep = r.Intn(3) == 0
				}
				if keep {
					f.written[k][e] = true
					f.order[k] = append(f.order[k], e)
				}
			}
			r.Shuffle(len(f.order[k]), func(i, j int) { f.order[k][i], f.order[k][j] = f.order[k][j], f.order[k][i] })
		}
		if f.leafErr >= 0 && len(f.order[0]) > 1 {
			// The leaf's own failure first, so a mutant that reorders
			// written elements shows.
			i := slices.Index(f.order[0], errName(f.leafErr))
			f.order[0][0], f.order[0][i] = f.order[0][i], f.order[0][0]
		}
	}
	return p
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// source prints the program with the rows rows(f) gives each function.
func (p *program) source(rows func(f *gfunc, k int) []string) string {
	var b strings.Builder
	if p.codecCap >= 0 {
		b.WriteString("import json\n")
	}
	if p.ext != nil {
		b.WriteString("import m/mid\n")
		for _, f := range p.funcs {
			if slices.Contains(rows(f, 0), "lib.LE") {
				b.WriteString("import m/lib\n")
				break
			}
		}
	}
	if p.codecCap >= 0 || p.ext != nil {
		b.WriteString("\n")
	}
	for i := 0; i < p.errs; i++ {
		fmt.Fprintf(&b, "type %s {}\n", errName(i))
	}
	for i := 0; i < p.caps; i++ {
		fmt.Fprintf(&b, "\ntype %s {\n\tOp() int,\n}\n", capName(i))
	}
	b.WriteString("\ntype R {}\n\ntype Box {\n\tN int,\n}\n\nfunc Close(b Box) {}\n\ntype Impl {}\n\nfunc Op(_ Impl) int {\n\t0\n}\n")
	for i := 0; i < p.caps; i++ {
		fmt.Fprintf(&b, "\nlet W%d = context {\n\t%s: Impl,\n}\n", i, capName(i))
	}
	if p.codecCap >= 0 {
		parse, wire := "", ""
		use := fmt.Sprintf(" needs %[1]s {\n\tlet _ = %[1]s.Op()\n", capName(p.codecCap))
		if p.onParse {
			parse, wire = use, " {\n"
		} else {
			parse, wire = " {\n", use
		}
		fmt.Fprintf(&b, "\ntype Lv {\n\tname string,\n}\n\ntype BadLv {}\n\nfunc ParseLv(s string) Lv fails BadLv%s\tLv{name: s}\n}\n\nfunc Wire(l Lv) string%s\tl.name\n}\n", parse, wire)
	}
	for _, f := range p.funcs {
		var params []string
		if f.method {
			params = append(params, "r R")
		}
		if f.takesArg {
			params = append(params, "x Box")
		}
		if f.codec {
			b.WriteString("\nfunc " + f.name + "[T wire](t T) int")
			if cs := rows(f, 1); len(cs) > 0 {
				b.WriteString(" needs " + strings.Join(cs, " | "))
			}
			if p.direct {
				b.WriteString(" {\n\tlet w = json.NewText()\n\tencode(t, w)\n\tlen(w.Written())\n}\n")
			} else {
				b.WriteString(" {\n\tlet _ = json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, t)\n\t0\n}\n")
			}
			continue
		}
		b.WriteString("\nfunc " + f.name + "(" + strings.Join(params, ", ") + ")")
		switch {
		case !f.funcResult:
			b.WriteString(" int")
		case len(rows(f, 0)) > 0 || len(rows(f, 1)) > 0 || len(rows(f, 2)) > 0:
			b.WriteString(" (func() int)")
		default:
			b.WriteString(" func() int")
		}
		if es := rows(f, 0); len(es) > 0 {
			b.WriteString(" fails " + strings.Join(es, " | "))
		}
		if cs := rows(f, 1); len(cs) > 0 {
			b.WriteString(" needs " + strings.Join(cs, " | "))
		}
		if ts := rows(f, 2); len(ts) > 0 {
			b.WriteString(" takes " + strings.Join(ts, ", "))
		}
		b.WriteString(" {\n")
		if f.handoff == 1 {
			b.WriteString("\tClose(x)\n")
		}
		sum := []string{"0"}
		for i, j := range f.calls {
			g := p.callee(j)
			call := g.name + "("
			if g.method {
				call = "R." + call
			}
			switch {
			case f.passAt == i:
				call += "x"
			case g.takesArg:
				call += "Box{N: 1}"
			case g.codec:
				call += "Lv{name: \"v\"}"
			}
			call += ")"
			if len(g.least[0]) > 0 {
				call = "try " + call
			}
			if f.lit[i] {
				result := "int"
				if g.funcResult {
					result = "func() int"
				}
				fmt.Fprintf(&b, "\tlet h%d = func() %s {\n\t\t%s\n\t}\n", i, result, call)
				call = fmt.Sprintf("h%d()", i)
				if len(g.least[0]) > 0 {
					call = "try " + call
				}
			}
			fmt.Fprintf(&b, "\tlet v%d = %s\n", i, call)
			if g.funcResult {
				sum = append(sum, fmt.Sprintf("v%d()", i))
			} else {
				sum = append(sum, fmt.Sprintf("v%d", i))
			}
		}
		for _, c := range f.ownCaps {
			fmt.Fprintf(&b, "\tlet c%d = %s.Op()\n", c, capName(c))
			sum = append(sum, fmt.Sprintf("c%d", c))
		}
		for _, c := range f.withCaps {
			fmt.Fprintf(&b, "\tlet w%d = with W%d { %s.Op() }\n", c, c, capName(c))
			sum = append(sum, fmt.Sprintf("w%d", c))
		}
		if f.funcResult {
			fmt.Fprintf(&b, "\tfunc() int {\n\t\t%s\n\t}\n}\n", strings.Join(sum, " + "))
		} else {
			fmt.Fprintf(&b, "\t%s\n}\n", strings.Join(sum, " + "))
		}
	}
	return b.String()
}

func (p *program) full() string {
	return p.source(func(f *gfunc, k int) []string {
		if k == 2 {
			return takesRow(f.leastTakes)
		}
		return sortedKeys(f.least[k])
	})
}

func (p *program) deleted() string {
	return p.source(func(f *gfunc, k int) []string {
		if k == 2 {
			return takesRow(f.writtenTakes)
		}
		return f.order[k]
	})
}

// takesRow is a takes clause of x, or none.
func takesRow(take bool) []string {
	if take {
		return []string{"x"}
	}
	return []string{}
}

func checkSeed(t *testing.T, seed int64, g *program) (reached []string) {
	t.Helper()
	src := g.deleted()
	key, texts, full, chk := "/p/main.ergo", map[string]string{"/p/main.ergo": src}, map[string]string{"/p/main.ergo": g.full()}, Source("/p/main.ergo")
	midKey := ""
	if g.ext != nil {
		key, texts, full, midKey, chk = packages(t, g, src)
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("seed %d: %s\nsource:\n%s\nreplay: go test ./internal/sketch/fix -run TestFixProperty -fix.seed=%d", seed, fmt.Sprintf(format, args...), src, seed)
	}
	if out, err := format.Source(src); err != nil || out != src {
		t.Fatalf("seed %d: the generator's source is not formatted (%v):\n%s\nformatted:\n%s", seed, err, src, out)
	}
	res, err := Run(texts, chk)
	if err != nil {
		fail("%v", err)
		return nil
	}
	fixed := res.Texts[key]
	if midKey != "" && res.Texts[midKey] != full[midKey] {
		fail("mid after fix:\n%s\nwant:\n%s", res.Texts[midKey], full[midKey])
	}
	// 1. fix then check: the other diagnostics of the full program.
	want, err := chk(full, check.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Diagnostics) > 0 {
		t.Fatalf("seed %d: the full program does not check:\n%s\n%s", seed, diagTexts(want.Diagnostics), g.full())
	}
	if got := diagTexts(res.Remaining); got != diagTexts(want.Diagnostics) {
		fail("after fix:\n%s\nfull program:\n%s\nfixed:\n%s", got, diagTexts(want.Diagnostics), fixed)
	}
	// 2. each row is the model's least row, written elements first.
	rows, err := writtenRows(fixed)
	if err != nil {
		fail("fixed source: %v\n%s", err, fixed)
		return nil
	}
	for _, f := range g.funcs {
		for k := 0; k < 2; k++ {
			got := rows[f.name][k]
			if !slices.Equal(sortedCopy(got), sortedKeys(f.least[k])) || !slices.Equal(got[:min(len(got), len(f.order[k]))], f.order[k]) {
				fail("%s clause %d: fixed %v, model %v, written %v\nfixed:\n%s", f.name, k, got, sortedKeys(f.least[k]), f.order[k], fixed)
			}
		}
		if got := rows[f.name][2]; !slices.Equal(got, takesRow(f.leastTakes)) {
			fail("%s takes: fixed %v, model %v\nfixed:\n%s", f.name, got, f.leastTakes, fixed)
		}
	}
	// 3. fix twice is fix once.
	again, err := Run(res.Texts, chk)
	if err != nil || again.Texts[key] != fixed || midKey != "" && again.Texts[midKey] != full[midKey] || len(again.Changed) != 0 {
		fail("fix twice: %v, %d changes\nonce:\n%s\ntwice:\n%s", err, len(again.Changed), fixed, again.Texts[key])
	}
	// 4. the formatter keeps the fixed text; only signature lines change.
	if out, err := format.Source(fixed); err != nil || out != fixed {
		fail("fmt(fix(x)) != fix(x) (%v):\n%s\nformatted:\n%s", err, fixed, out)
	}
	if a, b := otherLines(src), otherLines(fixed); a != b {
		fail("bytes outside the signatures changed:\n%s\nfixed:\n%s", src, fixed)
	}
	// 5. the rounds stay within the bound.
	if bound := Bound(len(g.funcs)+1, g.errs+g.caps+1); res.Rounds > bound {
		fail("%d rounds, bound %d", res.Rounds, bound)
	}
	if strings.Contains(fixed, "import m/lib") && !strings.Contains(src, "import m/lib") {
		reached = append(reached, "import added")
	}
	if res.Rounds >= 3 {
		reached = append(reached, "second round")
	}
	for _, f := range g.funcs {
		for k := 0; k < 2; k++ {
			if len(f.order[k]) > 0 && len(f.least[k]) > len(f.order[k]) {
				reached = append(reached, "clause extended")
			}
		}
	}
	for _, f := range g.funcs {
		if f.funcResult && !f.writtenTakes && len(f.order[0])+len(f.order[1]) == 0 && (len(f.least[0])+len(f.least[1]) > 0 || f.leastTakes) {
			reached = append(reached, "result opened")
		}
		if f.codec && len(f.order[1]) == 0 {
			reached = append(reached, "needs wired written")
			if g.direct {
				reached = append(reached, "needs wired written for a direct encode")
			}
		}
		for _, j := range f.calls {
			if g.callee(j).codec && !f.written[1][capName(g.codecCap)] {
				reached = append(reached, "codec instance charged")
				if g.onParse {
					reached = append(reached, "codec instance charged by ParseLv")
				}
			}
		}
		if f.leastTakes && !f.writtenTakes {
			reached = append(reached, "takes written")
			if f.handoff == 2 {
				reached = append(reached, "takes through a call")
			}
		}
	}
	for _, c := range res.Changed {
		if c.Exported {
			reached = append(reached, "exported widened")
		}
	}
	return reached
}

func sortedCopy(xs []string) []string {
	out := slices.Clone(xs)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func diagTexts(ds []Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%d:%d %s %s", d.Span.Line, d.Span.Column, d.Code, d.Message))
	}
	return strings.Join(out, "\n")
}

// writtenRows reads each function's written fails and needs elements and
// takes names.
func writtenRows(src string) (map[string][3][]string, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return nil, err
	}
	out := map[string][3][]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok {
			continue
		}
		var rows [3][]string
		rows[0], rows[1], rows[2] = []string{}, []string{}, []string{}
		if fd.Type.Takes != nil {
			for _, id := range fd.Type.Takes.Names {
				rows[2] = append(rows[2], id.Name)
			}
		}
		if fd.Type.Results != nil {
			for _, r := range fd.Type.Results.List {
				if er, ok := r.Type.(*syntax.ErrorRow); ok {
					for _, x := range er.Types {
						rows[0] = append(rows[0], src[x.Pos():x.End()])
					}
				}
			}
		}
		if fd.Type.Needs != nil {
			for _, x := range fd.Type.Needs.Names {
				rows[1] = append(rows[1], src[x.Pos():x.End()])
			}
		}
		out[fd.Name.Name] = rows
	}
	return out, nil
}

// otherLines is src without its signature and import lines.
func otherLines(src string) string {
	var keep []string
	for _, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(l, "func ") || strings.HasPrefix(l, "import ") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}
