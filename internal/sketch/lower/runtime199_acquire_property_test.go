package lower

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Acquisition and registration as one masked step (runtime-199 stage 3c;
// resources.md R7, R8). TestRuntime199AcquireProperty generates inline
// wires of explicit providers, some embedding a named wire and some
// replacing one of its entries, and takes each with's acquisitions from the
// checker's fact (With.Acquired). It runs the emitted with in a native copy
// of the program and its runtime, once for every acquired entry and every
// outcome at that entry:
//
//   - fail: the constructor fails; only the earlier successes close;
//   - defect: the constructor panics; only the earlier successes close and
//     the defect is the exit;
//   - stop: the task is stopped while the constructor runs; it still
//     completes, its release still runs, and no later provider is built;
//   - gap: the task is stopped between the constructor's success and the
//     registration of its release (a test seam in the copied rt.AcquireInto,
//     which also polls there); the release is registered and runs once.
//
// The named twin of each sequence holds the same entries in a named wire,
// so its with runs through emitWire's builder function (stage 3c r1 m1). A
// named wire is closed, so its constructors call no Probe; the seam drives
// its outcomes: a normal end, and at the registration of each acquisition
// of a with that owns a release, a stop (gap) and a defect (seamdefect).
//
// Each inline case also runs in three nested roots (stage 5a M1): the same
// with inside an owning with of a named wire, in an if block, in a loop and
// in a function literal. The outer provider registers first and closes
// after every inner release, on every exit.
//
// Each case runs with the caller unmasked and inside two nested masks, which
// the acquisition must restore. The seam counts the completed acquisitions
// that reach registration and reports any that is not masked there, and the
// probe reports any constructor step that runs unmasked.

// acqEntry is one provider entry: of the inline wire, or of the named wire
// the inline wire embeds.
type acqEntry struct {
	caps  []int
	deps  []int // a named entry: capabilities of earlier named entries its provider uses
	res   bool  // its type has a checked Close
	named bool  // in the embedded named wire, which is closed: no Probe and no failure
	ctor  bool  // its provider is a constructor call, not a type literal
	fail  bool  // an inline constructor that can fail, written with try
	over  bool  // a named entry an inline replace overrides
	repl  bool  // the inline replace entry
}

type acqSeq struct {
	k, caps int
	entries []acqEntry
	order   []int // the inline entries in source order
	uses    []int // the capabilities the with's block uses
}

func (s *acqSeq) typ(e int) string { return fmt.Sprintf("R%d_%d", s.k, e) }
func (s *acqSeq) cap(c int) string { return fmt.Sprintf("P%d_%d", s.k, c) }

// probed reports an entry whose construction calls the probe.
func (s *acqSeq) probed(e int) bool { return s.entries[e].ctor && !s.entries[e].named }

func (s *acqSeq) binder(c int) int {
	for e, en := range s.entries {
		if !en.over && slices.Contains(en.caps, c) {
			return e
		}
	}
	panic("unbound capability")
}

// reached is the model's acquisition set: the entries that bind a used
// capability and, transitively, the entries their providers use.
func (s *acqSeq) reached() map[int]bool {
	out := map[int]bool{}
	var visit func(c int)
	visit = func(c int) {
		e := s.binder(c)
		if out[e] {
			return
		}
		out[e] = true
		for _, dep := range s.entries[e].deps {
			visit(dep)
		}
	}
	for _, c := range s.uses {
		visit(c)
	}
	return out
}

func genAcqSeq(r *rand.Rand, k int) *acqSeq {
	s := &acqSeq{k: k, caps: 2 + r.IntN(5)}
	perm := r.Perm(s.caps)
	var namedCaps []int
	for i := 0; i < len(perm); {
		n := 1
		if i+1 < len(perm) && r.IntN(3) == 0 {
			n = 2
		}
		// The first entry is a fallible inline constructor, so the with
		// can always fail and its function's row is never wider than it.
		en := acqEntry{caps: slices.Clone(perm[i : i+n]), res: r.IntN(4) != 0, named: i > 0 && r.IntN(2) == 0}
		switch {
		case i == 0:
			en.ctor, en.fail = true, true
		case en.named:
			for _, j := range r.Perm(len(namedCaps)) {
				if len(en.deps) < 2 && r.IntN(2) == 0 {
					en.deps = append(en.deps, namedCaps[j])
				}
			}
			slices.Sort(en.deps)
			en.ctor = len(en.deps) > 0 || r.IntN(2) == 0
			namedCaps = append(namedCaps, en.caps...)
		default:
			en.ctor = r.IntN(4) != 0
			en.fail = en.ctor && r.IntN(2) == 0
		}
		s.entries = append(s.entries, en)
		i += n
	}
	if r.IntN(2) == 0 {
		for e := range s.entries {
			en := s.entries[e]
			if !en.named || len(en.caps) != 1 {
				continue
			}
			used := false
			for _, o := range s.entries {
				used = used || slices.Contains(o.deps, en.caps[0])
			}
			if used {
				continue
			}
			s.entries[e].over = true
			s.entries = append(s.entries, acqEntry{caps: slices.Clone(en.caps), res: r.IntN(4) != 0, ctor: true, fail: r.IntN(2) == 0, repl: true})
			break
		}
	}
	for e, en := range s.entries {
		if !en.named {
			s.order = append(s.order, e)
		}
	}
	r.Shuffle(len(s.order), func(i, j int) { s.order[i], s.order[j] = s.order[j], s.order[i] })
	for _, c := range r.Perm(s.caps) {
		if r.IntN(2) == 0 || len(s.uses) == 0 {
			s.uses = append(s.uses, c)
		}
		if len(s.uses) == 4 {
			break
		}
	}
	slices.Sort(s.uses)
	return s
}

func (s *acqSeq) fn() string { return fmt.Sprintf("run%d", s.k) }

// namedFn is the function of the named twin's with.
func (s *acqSeq) namedFn() string { return fmt.Sprintf("named%d", s.k) }

// src writes the sequence's types, providers, embedded wire and the
// function whose with acquires them.
func (s *acqSeq) src() string {
	var b strings.Builder
	for c := 0; c < s.caps; c++ {
		fmt.Fprintf(&b, "type %s { Name() string }\n", s.cap(c))
	}
	for e, en := range s.entries {
		t := s.typ(e)
		fmt.Fprintf(&b, "type %s {}\nfunc Name(_ %s) string { %q }\n", t, t, t)
		if en.res {
			fmt.Fprintf(&b, "func Close(_ %s) needs Probe { let _ = Probe.Step(\"close %s\") }\n", t, t)
		}
		switch {
		case en.named && en.ctor:
			var needs, body []string
			for _, c := range en.deps {
				needs = append(needs, s.cap(c))
				body = append(body, "\tlet _ = "+s.cap(c)+".Name()\n")
			}
			row := ""
			if len(needs) > 0 {
				row = " needs " + strings.Join(needs, ", ")
			}
			fmt.Fprintf(&b, "func nk%d_%d() %s%s {\n%s\t%s\n}\n", s.k, e, t, row, strings.Join(body, ""), t)
		case en.ctor:
			row, open := "", "\tlet _ = Probe.Step(\"open "+t+"\")\n"
			if en.fail {
				row, open = " fails Fault", "\tif Probe.Step(\"open "+t+"\") { return Fault }\n"
			}
			fmt.Fprintf(&b, "func mk%d_%d() %s%s needs Probe {\n%s\tlet _ = Probe.Step(\"built %s\")\n\t%s\n}\n", s.k, e, t, row, open, t, t)
		}
	}
	entry := func(e int) string {
		en := s.entries[e]
		var keys []string
		for _, c := range en.caps {
			keys = append(keys, s.cap(c))
		}
		provider := s.typ(e)
		switch {
		case en.named && en.ctor:
			provider = fmt.Sprintf("nk%d_%d()", s.k, e)
		case en.fail:
			provider = fmt.Sprintf("try mk%d_%d()", s.k, e)
		case en.ctor:
			provider = fmt.Sprintf("mk%d_%d()", s.k, e)
		}
		head := ""
		if en.repl {
			head = "replace "
		}
		return head + strings.Join(keys, ", ") + ": " + provider
	}
	var named, inline []string
	for e, en := range s.entries {
		if en.named {
			named = append(named, entry(e))
		}
	}
	for _, e := range s.order {
		inline = append(inline, entry(e))
	}
	if len(named) > 0 {
		fmt.Fprintf(&b, "let N%d = wire { %s }\n", s.k, strings.Join(named, ", "))
		inline = append(inline, fmt.Sprintf("N%d", s.k))
	}
	var names []string
	for _, c := range s.uses {
		names = append(names, s.cap(c)+".Name()")
	}
	fmt.Fprintf(&b, "func %s() fails Fault needs Probe {\n\twith wire { %s } {\n\t\tlet _ = Probe.Step(\"body \" + %s)\n\t}\n\tlet _ = Probe.Step(\"end %d\")\n}\n",
		s.fn(), strings.Join(inline, ", "), strings.Join(names, " + \" \" + "), s.k)
	// The silent twin, for checkWithFootprints only (stage 3c r1 M1): the
	// same with, whose block reaches every capability through a need
	// alone, so its frame's footprint holds only what the with's providers
	// run, an embedded named provider's included. It is compiled, never run.
	var all []string
	for c := 0; c < s.caps; c++ {
		all = append(all, s.cap(c))
	}
	fmt.Fprintf(&b, "func need%d() needs %s {}\nfunc silent%d() fails Fault needs Probe {\n\twith wire { %s } { need%d() }\n}\n",
		s.k, strings.Join(all, ", "), s.k, strings.Join(inline, ", "), s.k)
	// The named twin (stage 3c r1 m1), run natively: the same entries in a
	// named wire, so its with is lowered through emitWire's builder. A named
	// wire is closed, so each inline constructor becomes a constructor that
	// calls no Probe; the seam in rt.AcquireInto drives its boundary
	// outcomes.
	var twin []string
	for _, e := range s.order {
		en := s.entries[e]
		if en.ctor {
			fmt.Fprintf(&b, "func qk%d_%d() %s { %s }\n", s.k, e, s.typ(e), s.typ(e))
		}
		var keys []string
		for _, c := range en.caps {
			keys = append(keys, s.cap(c))
		}
		provider := s.typ(e)
		if en.ctor {
			provider = fmt.Sprintf("qk%d_%d()", s.k, e)
		}
		head := ""
		if en.repl {
			head = "replace "
		}
		twin = append(twin, head+strings.Join(keys, ", ")+": "+provider)
	}
	if len(named) > 0 {
		twin = append(twin, fmt.Sprintf("N%d", s.k))
	}
	fmt.Fprintf(&b, "let Q%d = wire { %s }\nfunc %s() fails Fault needs Probe {\n\twith Q%d {\n\t\tlet _ = Probe.Step(\"body \" + %s)\n\t}\n\tlet _ = Probe.Step(\"end %d\")\n}\n",
		s.k, strings.Join(twin, ", "), s.namedFn(), s.k, strings.Join(names, " + \" \" + "), s.k)
	// The deep twin, for checkWithFootprints only (stage 3c r2 M1 and m2):
	// a with of a wire that embeds the named wire, so each named entry is
	// embedded at depth 2. Its block needs every named capability, so every
	// named entry is built here, and its frame's footprint holds only what
	// those constructors and their Closes run.
	if len(named) > 0 {
		var ncaps []string
		for _, en := range s.entries {
			if en.named {
				for _, c := range en.caps {
					ncaps = append(ncaps, s.cap(c))
				}
			}
		}
		fmt.Fprintf(&b, "let M%d = wire { N%d }\nfunc needN%d() needs %s {}\nfunc deep%d() needs Probe {\n\twith wire { M%d } { needN%d() }\n}\n",
			s.k, s.k, s.k, strings.Join(ncaps, ", "), s.k, s.k, s.k)
	}
	// The nested roots (stage 5a M1), run natively: the same inline with,
	// nested inside an owning with of the named wire W<k>, whose provider
	// O<k> has a Close, in each form: an if block, a loop and a function
	// literal the outer block calls. So every boundary outcome of the inner
	// with also meets an enclosing owner: O<k> closes after the inner with's
	// providers on every exit, and the outer block goes on only after the
	// inner with ended normally.
	fmt.Fprintf(&b, "type PO%d { Name() string }\ntype O%d {}\nfunc Name(_ O%d) string { \"O%d\" }\nfunc Close(_ O%d) needs Probe { let _ = Probe.Step(\"close O%d\") }\nfunc mo%d() O%d { O%d }\nlet W%d = wire { PO%d: mo%d() }\n",
		s.k, s.k, s.k, s.k, s.k, s.k, s.k, s.k, s.k, s.k, s.k, s.k)
	inner := fmt.Sprintf("with wire { %s } {\n\t\t\tlet _ = Probe.Step(\"body \" + %s)\n\t\t}", strings.Join(inline, ", "), strings.Join(names, " + \" \" + "))
	for _, form := range nestForms {
		var around string
		switch form {
		case "block":
			around = "\t\tif true {\n\t\t" + inner + "\n\t\t}\n"
		case "loop":
			around = "\t\tfor i in 0..1 {\n\t\t\tlet _ = i\n\t\t" + inner + "\n\t\t}\n"
		case "closure":
			around = "\t\tlet f = func() fails Fault needs Probe {\n\t\t" + inner + "\n\t\t}\n\t\ttry f()\n"
		}
		fmt.Fprintf(&b, "func %s() fails Fault needs Probe {\n\twith W%d {\n%s\t\tlet _ = Probe.Step(\"outer \" + PO%d.Name())\n\t}\n\tlet _ = Probe.Step(\"end %d\")\n}\n",
			s.nestFn(form), s.k, around, s.k, s.k)
	}
	return b.String()
}

// nestForms are the places of the nested roots (stage 5a M1).
var nestForms = []string{"block", "loop", "closure"}

// nestFn is the function of the nested root in form.
func (s *acqSeq) nestFn(form string) string { return fmt.Sprintf("nest%s%d", form, s.k) }

// nestIn is the inline case c run in the nested root of form: the owning
// with of W<k> around it acquires O<k> first (the seam's first
// registration, so a gap or seam defect at the inner index-th acquisition
// is the seam's index+1-th), its block's own line follows the inner with's
// normal end, and O<k> closes after every inner release, before a typed
// failure reaches the caller.
func (s *acqSeq) nestIn(c acqCase, form string) acqCase {
	n := c
	n.name, n.fn = "nest"+form+c.name, s.nestFn(form)
	if c.mode == acqGap || c.mode == acqSeamDefect {
		n.index = c.index + 1
	}
	n.gaps = c.gaps + 1
	closeO, end := fmt.Sprintf("close O%d", s.k), fmt.Sprintf("end %d", s.k)
	want := append([]string(nil), c.want...)
	last := ""
	if len(want) > 0 {
		last = want[len(want)-1]
	}
	switch {
	case last == end:
		want = append(want[:len(want)-1], fmt.Sprintf("outer O%d", s.k), closeO, end)
	case c.mode == acqFail:
		want = append(want[:len(want)-1], closeO, last)
	default:
		want = append(want, closeO)
	}
	n.want = want
	return n
}

// acqFact is the checker's acquisition order of the inline with in s's
// function, as entry indices, checked against the model: the reached
// entries, each once with every capability it binds and its Close, a named
// provider after the entries it uses, and no overridden provider.
func acqFact(t *testing.T, o *lowered, s *acqSeq) []int {
	return acqFactIn(t, o, s, s.fn(), "")
}

// acqFactIn is acqFact for the with in fn: of the inline wire when wire is
// empty, else of the named wire wire.
func acqFactIn(t *testing.T, o *lowered, s *acqSeq, fn, wire string) []int {
	t.Helper()
	var order []int
	found := 0
	for _, decl := range o.file.Decls {
		fd, ok := decl.(*syntax.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := o.pkg.Info.Withs[x]
			if w == nil || (wire == "") != (w.Wires[0].Lit != nil) || wire != "" && w.Wires[0].Name != wire {
				return true
			}
			found++
			for _, a := range w.Acquired {
				typ := strings.TrimPrefix(fmt.Sprint(o.pkg.Info.Types[a.Entry.Provider]), "main.")
				e, err := strconv.Atoi(strings.TrimPrefix(typ, fmt.Sprintf("R%d_", s.k)))
				if err != nil || s.typ(e) != typ {
					t.Fatalf("%s: acquisition of %s is not a generated provider", fn, typ)
				}
				var caps []string
				for _, i := range a.Caps {
					caps = append(caps, strings.TrimPrefix(a.Entry.Caps[i].String(), "main."))
				}
				var want []string
				for _, c := range s.entries[e].caps {
					want = append(want, s.cap(c))
				}
				if !sameSet(caps, want) || (a.Close != nil) != s.entries[e].res {
					t.Errorf("%s: %s binds %v closes %v, model %v %v", fn, typ, caps, a.Close != nil, want, s.entries[e].res)
				}
				order = append(order, e)
			}
			return true
		})
	}
	if found != 1 {
		t.Fatalf("%s has %d withs of %q (empty: inline), want 1", fn, found, wire)
	}
	reached := s.reached()
	pos := map[int]int{}
	for i, e := range order {
		if _, dup := pos[e]; dup {
			t.Errorf("%s: %s acquired twice", fn, s.typ(e))
		}
		pos[e] = i
	}
	for e, en := range s.entries {
		i, ok := pos[e]
		if ok != reached[e] {
			t.Errorf("%s: %s acquired %v, model %v (over %v, uses %v)", fn, s.typ(e), ok, reached[e], en.over, s.uses)
		}
		for _, c := range en.deps {
			if j, built := pos[s.binder(c)]; ok && (!built || j >= i) {
				t.Errorf("%s: %s is built before %s, whose capability it uses", fn, s.typ(e), s.typ(s.binder(c)))
			}
		}
	}
	return order
}

// The outcomes at one acquired entry.
const (
	acqNone   = "none"
	acqFail   = "fail"
	acqDefect = "defect"
	acqStop   = "stop"
	acqGap    = "gap"
	// acqSeamDefect is a defect at the seam, after the provider is built
	// and before its release is registered: the named twin's defect
	// outcome, as its constructors call no Probe.
	acqSeamDefect = "seamdefect"
)

// acqCase is one native run: the outcome at the index-th acquisition of
// the fact, and what the run must record.
type acqCase struct {
	name, mode, target string
	fn                 string // the function whose with runs
	k, index, depth    int
	want               []string
	exit               string // ok, defect or interrupted
	gaps               int    // completed acquisitions, each registered once
	owned              bool   // the with owns a release, so every entry is acquired masked
}

// expect derives a case from the fact: the probe lines, the exit and the
// number of registrations.
func (s *acqSeq) expect(fact []int, mode string, index, depth int) acqCase {
	return s.expectIn(fact, mode, index, depth, false)
}

// expectIn is expect for the inline with, or for the named twin's with
// when named is set, whose constructors call no Probe.
func (s *acqSeq) expectIn(fact []int, mode string, index, depth int, named bool) acqCase {
	c := acqCase{name: fmt.Sprintf("%d/%s%d/depth%d", s.k, mode, index, depth), mode: mode, fn: s.fn(), k: s.k, index: index, depth: depth, exit: "ok"}
	probed := s.probed
	if named {
		c.name, c.fn = "named"+c.name, s.namedFn()
		probed = func(int) bool { return false }
	}
	if index >= 0 {
		c.target = s.typ(fact[index])
	}
	var built []int
	rollback := func() {
		for i := len(built) - 1; i >= 0; i-- {
			if s.entries[built[i]].res {
				c.want = append(c.want, "close "+s.typ(built[i]))
			}
		}
		if c.owned {
			c.gaps = len(built)
		}
	}
	for _, e := range fact {
		c.owned = c.owned || s.entries[e].res
	}
	for j, e := range fact {
		at := j == index
		if probed(e) {
			c.want = append(c.want, "open "+s.typ(e))
		}
		if at && mode == acqStop && !c.owned {
			// A with that owns no release builds its providers unmasked
			// (stage 3b): the constructor's next capability call observes
			// the stop unless the caller masks.
			c.exit = "interrupted"
			if depth == 0 {
				return c
			}
		}
		if at && (mode == acqFail || mode == acqDefect) {
			rollback()
			if mode == acqFail {
				c.want = append(c.want, fmt.Sprintf("failure %d", s.k))
			} else {
				c.exit = "defect"
			}
			return c
		}
		if probed(e) {
			c.want = append(c.want, "built "+s.typ(e))
		}
		if at && mode == acqSeamDefect {
			// Built, never registered: only the earlier successes close.
			rollback()
			c.exit = "defect"
			return c
		}
		built = append(built, e)
		if at && c.owned && (mode == acqStop || mode == acqGap) {
			c.exit = "interrupted"
			if depth == 0 {
				rollback()
				return c
			}
		}
	}
	var names []string
	for _, cp := range s.uses {
		names = append(names, s.typ(s.binder(cp)))
	}
	c.want = append(c.want, "body "+strings.Join(names, " "))
	rollback()
	c.want = append(c.want, fmt.Sprintf("end %d", s.k))
	return c
}

func TestRuntime199AcquireProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a generated native program")
	}
	r := rand.New(rand.NewPCG(199, 5))
	var seqs []*acqSeq
	for k := 0; k < 16; k++ {
		seqs = append(seqs, genAcqSeq(r, k))
	}
	var src strings.Builder
	src.WriteString("type Probe { Step(name string) bool }\ntype Fault {}\n")
	for _, s := range seqs {
		src.WriteString(s.src())
	}
	src.WriteString("func main() {}\n")
	o := mustLower(t, "acquire", src.String())
	// Every provider a with builds counts in its frame's footprint (M1).
	if checkWithFootprints(t, o) == 0 {
		t.Error("no acquired provider has a footprint: the footprint check is vacuous")
	}
	// The generator reaches every shape the property claims, as acquired
	// entries of the checker's fact.
	shapes := map[string]bool{}
	var cases []acqCase
	for _, s := range seqs {
		fact := acqFact(t, o, s)
		// Each nested root acquires what the top-level root acquires, in
		// the same order (the checker's fact of each inner with).
		for _, form := range nestForms {
			if got := acqFactIn(t, o, s, s.nestFn(form), ""); !slices.Equal(got, fact) {
				t.Errorf("%s acquires %v, the top-level root %v", s.nestFn(form), got, fact)
			}
		}
		// add appends an inline case and its run in each nested root.
		add := func(c acqCase) {
			cases = append(cases, c)
			for _, form := range nestForms {
				cases = append(cases, s.nestIn(c, form))
				shapes["nested "+form] = true
			}
		}
		for _, depth := range []int{0, 2} {
			none := s.expect(fact, acqNone, -1, depth)
			add(none)
			shapes["owning"] = shapes["owning"] || none.owned
			shapes["owning nothing"] = shapes["owning nothing"] || !none.owned
			for i, e := range fact {
				en := s.entries[e]
				var modes []string
				if none.owned {
					modes = append(modes, acqGap)
				}
				if s.probed(e) {
					modes = append(modes, acqStop, acqDefect)
				}
				if en.fail {
					modes = append(modes, acqFail)
				}
				for _, m := range modes {
					add(s.expect(fact, m, i, depth))
				}
				shapes["shared"] = shapes["shared"] || len(en.caps) == 2
				shapes["named dependency"] = shapes["named dependency"] || len(en.deps) > 0
				shapes["named literal"] = shapes["named literal"] || en.named && !en.ctor
				shapes["inline literal"] = shapes["inline literal"] || !en.named && !en.ctor
				shapes["replace"] = shapes["replace"] || en.repl
				shapes["fallible"] = shapes["fallible"] || en.fail
				shapes["later after earlier success"] = shapes["later after earlier success"] || i > 0 && s.probed(e)
			}
			reached := s.reached()
			for e, en := range s.entries {
				shapes["unreached constructor"] = shapes["unreached constructor"] || !reached[e] && s.probed(e)
				shapes["overridden"] = shapes["overridden"] || en.over
				shapes["named resource"] = shapes["named resource"] || en.named && en.res
			}
		}
		// The named twin at each boundary outcome its builder reaches: a
		// normal end, and for a with that owns a release, a stop and a
		// defect at the registration of each acquisition.
		nfact := acqFactIn(t, o, s, s.namedFn(), fmt.Sprintf("Q%d", s.k))
		for _, depth := range []int{0, 2} {
			none := s.expectIn(nfact, acqNone, -1, depth, true)
			cases = append(cases, none)
			if !none.owned {
				shapes["named twin owning nothing"] = true
				continue
			}
			shapes["named twin owning"] = true
			for i := range nfact {
				shapes["named twin later acquisition"] = shapes["named twin later acquisition"] || i > 0
				for _, m := range []string{acqGap, acqSeamDefect} {
					cases = append(cases, s.expectIn(nfact, m, i, depth, true))
				}
			}
		}
	}
	for _, shape := range []string{"owning", "owning nothing", "shared", "named dependency", "named literal", "inline literal", "replace", "fallible", "later after earlier success", "unreached constructor", "overridden", "named resource", "named twin owning", "named twin owning nothing", "named twin later acquisition", "nested block", "nested loop", "nested closure"} {
		if !shapes[shape] {
			t.Fatalf("the generator reaches no %s entry", shape)
		}
	}
	acquireNative(t, o, cases)
}

// acquireNative runs the cases against the emitted program and a copy of
// the runtime whose rt.AcquireInto calls the test seam AcquireGap right
// before it registers a completed acquisition. The emitted code is not
// changed.
func acquireNative(t *testing.T, o *lowered, cases []acqCase) {
	t.Helper()
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	write("main.go", []byte(o.mainGo()))
	seam := 0
	for _, f := range Runtime() {
		data := f.Data
		if f.Path == "ergo/runtime/rt/scope.go" {
			// Exactly one registration in AcquireInto; an instrumentation
			// drift fails rather than running without the witness.
			const from = "\t\tregister(r)\n"
			if n := strings.Count(string(data), from); n != 1 {
				t.Fatalf("acquisition seam %q occurs %d times, want 1", from, n)
			}
			data = []byte(strings.Replace(string(data), from, "\t\tAcquireGap(t)\n"+from, 1))
			seam++
		}
		write(f.Path, data)
	}
	if seam != 1 {
		t.Fatal("the runtime copy has no rt/scope.go")
	}
	write("ergo/runtime/rt/acquire_probe.go", []byte("package rt\nvar AcquireGapHook func(*Task)\nfunc AcquireGap(t *Task) { if AcquireGapHook != nil { AcquireGapHook(t) } }\n"))
	write("ergo/runtime/v1/acquire_probe.go", []byte("package v1\nimport \"ergo.local/prototype/runtime/rt\"\nfunc AcquireRun(body func(*rt.Task) error) error { return runMain(body) }\n"))
	var table strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&table, "\t{name: %q, mode: %q, target: %q, index: %d, depth: %d, exit: %q, gaps: %d, owned: %v, want: %#v, run: func(env *Env, p *probe) error {\n\t\tif err := %s(env); err != nil {\n\t\t\tif _, ok := err.(Fault); ok { p.add(\"failure %d\"); return nil }\n\t\t\treturn err\n\t\t}\n\t\treturn nil\n\t}},\n",
			c.name, c.mode, c.target, c.index, c.depth, c.exit, c.gaps, c.owned, c.want, c.fn, c.k)
	}
	write("acquire_test.go", []byte(strings.Replace(acquireNativeTest, "CASES", table.String(), 1)))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native acquisition cases: %v\n%s\nErgo:\n%s", err, firstLines(string(output), 120), o.src)
	}
	t.Logf("%d native cases: fail, defect, stop and registration-gap outcomes at every acquired entry, top-level and in nested roots, unmasked and nested-masked; native PASS", len(cases))
}

// TestRuntime199AcquireOwner fails an acquisition that bypasses its owner.
// The lowering emits WithAcquire only from the with lifetime owner's
// acquisition step and binds no bare registration. In the runtime, rt's
// acquiring region is entered only by AcquireInto, AcquireInto is called
// only by rt.Acquire and v1's WithAcquire, and the with's one release list
// is written only by WithAcquire: no second list and no second mask.
func TestRuntime199AcquireOwner(t *testing.T) {
	if _, ok := runtimeBindings["Scope.WithClose"]; ok {
		t.Error("the runtime catalog binds a bare with registration")
	}
	if _, ok := runtimeBindings["Scope.WithAcquire"]; !ok {
		t.Error("the runtime catalog has no acquisition step")
	}
	emits := 0
	i1LowerSource(t, func(owner string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if value == "Scope.WithAcquire" && owner == "blocking.go:package" {
				return true // the catalog key, whose one emitter is withClose
			}
			if strings.Contains(value, "WithAcquire") {
				if owner == "withClose" {
					emits++
				} else {
					t.Errorf("%s emits a with acquisition outside the acquisition owner", owner)
				}
			}
			for _, name := range []string{"WithClose", "ownRelease", "AcquireInto", "rt.Acquire", ".Own(", ".Defer("} {
				if strings.Contains(value, name) {
					t.Errorf("%s emits %s, a registration outside WithAcquire", owner, name)
				}
			}
			return true
		})
	})
	if emits != 1 {
		t.Errorf("the acquisition owner emits WithAcquire %d times, want 1", emits)
	}
	allowed := map[string]map[string]bool{
		"acquire":     {"rt.AcquireInto": true},
		"AcquireInto": {"rt.Acquire": true, "v1.WithAcquire": true},
		"Acquire":     {},
		"ownRelease":  {"v1.WithAcquire": true},
	}
	seen := map[string]int{}
	for _, pkg := range []string{"rt", "v1"} {
		dir := filepath.Join("..", "..", "..", "runtime", pkg)
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				owner := pkg + "." + file.Name()
				if fn, ok := decl.(*ast.FuncDecl); ok {
					owner = pkg + "." + fn.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					fun := call.Fun
					if ix, ok := fun.(*ast.IndexExpr); ok {
						fun = ix.X
					}
					var name string
					switch f := fun.(type) {
					case *ast.Ident:
						name = f.Name
					case *ast.SelectorExpr:
						name = f.Sel.Name
					}
					// rt's Acquire is the one caller-facing acquisition of an rt
					// Scope; v1 must not call it (a second list).
					if name == "Acquire" && pkg == "rt" {
						return true
					}
					if want, ok := allowed[name]; ok {
						seen[name]++
						if !want[owner] {
							t.Errorf("%s calls %s outside the acquisition owner", owner, name)
						}
					}
					return true
				})
			}
		}
	}
	for _, name := range []string{"acquire", "AcquireInto", "ownRelease"} {
		if seen[name] == 0 {
			t.Errorf("the scan found no call of %s; it no longer reaches the owner", name)
		}
	}
}

const acquireNativeTest = `package main
import (
 "strings"
 "sync"
 "testing"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
type probe struct {
 mu sync.Mutex
 lines []string
 mode, target string
 unmasked []string
}
func (p *probe) add(line string) { p.mu.Lock(); p.lines = append(p.lines, line); p.mu.Unlock() }
// Step is the probe capability. A constructor step must run masked. At the
// target's open it fails, panics or stops the calling task, by mode.
func (p *probe) Step(t *rt.Task, line string) bool {
 p.add(line)
 if (strings.HasPrefix(line, "open ") || strings.HasPrefix(line, "built ")) && !t.Masked() {
  p.unmasked = append(p.unmasked, line)
 }
 if line != "open "+p.target { return false }
 switch p.mode {
 case "fail": return true
 case "defect": panic("defect " + p.target)
 case "stop": t.Interrupt()
 }
 return false
}
type acqCase struct {
 name, mode, target string
 index, depth, gaps int
 exit string
 owned bool
 want []string
 run func(*Env, *probe) error
}
var cases = []acqCase{
CASES}
func classify(err error, target string) string {
 switch {
 case err == nil: return "ok"
 case err == rt.Interrupted: return "interrupted"
 case strings.Contains(err.Error(), "defect "+target): return "defect"
 }
 return "unexpected: " + err.Error()
}
func TestAcquire(t *testing.T) {
 for _, c := range cases {
  t.Run(c.name, func(t *testing.T) {
   p := &probe{mode: c.mode, target: c.target}
   gaps := 0
   var unmaskedGaps int
   rt.AcquireGapHook = func(task *rt.Task) {
    if !task.Masked() { unmaskedGaps++ }
    if c.mode == "gap" && gaps == c.index { task.Interrupt() }
    if c.mode == "seamdefect" && gaps == c.index { panic("defect " + c.target) }
    gaps++
    // The seam is an interruption point: only the acquisition's mask
    // keeps a stop here from being observed before registration.
    task.Check()
   }
   defer func() { rt.AcquireGapHook = nil }()
   err := ergo.AcquireRun(func(task *rt.Task) error {
    for range c.depth { m := task.Mask(); defer m.Unmask() }
    return c.run(&Env{task: task, Probe: p}, p)
   })
   if got := classify(err, c.target); got != c.exit {
    t.Errorf("exit %s, want %s", got, c.exit)
   }
   if got, want := strings.Join(p.lines, "\n"), strings.Join(c.want, "\n"); got != want {
    t.Errorf("probe:\n%s\nwant:\n%s", got, want)
   }
   if gaps != c.gaps {
    t.Errorf("%d acquisitions reached registration, want %d", gaps, c.gaps)
   }
   if unmaskedGaps != 0 || c.owned && len(p.unmasked) != 0 {
    t.Errorf("unmasked: %d registrations, constructor steps %v", unmaskedGaps, p.unmasked)
   }
   if !c.owned && c.depth == 0 && len(p.unmasked) == 0 && len(c.want) > 0 && strings.HasPrefix(c.want[0], "open ") {
    t.Error("control: a with that owns no release masked its constructor, so the mask probe proves nothing")
   }
  })
 }
}
`
