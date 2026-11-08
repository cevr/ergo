package std_test

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

// This file is the host guard's independent oracle (fu-ambient-ctor r4,
// r5). A seeded generator builds a model of a package's exported surface
// (oType, oItem), renders it to Ergo source, and the oracle answers on the
// model alone by the rule as docs/spec/stdlib.md, Host packages 6 states
// it, not as the guard's code does:
//
//   - held is the provider Host and the records another package needs to
//     build it (here: Host itself for a record with an unexported field;
//     the record T1 for `type Host { C T1 }`; the implementer Live for a
//     sealed operation type);
//   - a type mentions what it is made of, at any depth: every field of a
//     record, hidden or not; type arguments; union variants and tuple
//     elements; slice, Map key and element; a function's parameters,
//     results, failure row and needs row; an operation type's operation
//     and, for a sealed one, each type of its package that implements it;
//     a machine's state and signal; a row argument of a generic type;
//   - an exported function whose signature mentions a held type, with no
//     Ambient in its row and no parameter whose type is a held type
//     itself, is reported; an exported let or named context that mentions
//     one is reported.
//
// It calls no checker helper (no check.Mentions, no check.Authorities):
// the model's types are a finite tree, so its walk has no seen-set.

// oKind is a constructor of the model's types.
type oKind int

const (
	oInt     oKind = iota
	oRec           // a record; auth when it has an unexported field
	oSlice         // []elem
	oMap           // Map[string, elem]
	oMapKey        // Map[elem, int]
	oOption        // Option[elem]
	oSeq           // Seq[elem]
	oFunc          // a defined function type over sig
	oOps           // an operation type with one operation, sig
	oTuple         // a defined tuple type (elems...)
	oUnion         // a union of the records elems
	oRowBox        // Rw<n>[elem], with `type Rw<n>[E] { F func() int fails E }`: elem, a record, is a row argument
	oMachine       // a machine over the unions elems[0] (state), elems[1] (signal)
	oBox           // a generic record Box<n>[T] { f T } at the argument elem
	oHost          // the provider; impls are a sealed one's implementers
)

type oType struct {
	kind   oKind
	name   string // declared name: oRec, oFunc, oOps, oTuple, oUnion, oMachine, oBox
	auth   bool   // oRec: an unexported field
	fields []oField
	elem   *oType
	sig    *oSig // oFunc; oOps's operation
	op     string
	elems  []*oType
	impls  []*oType // oHost: the implementers of a sealed Host
}

type oField struct {
	name string // exported when capitalised
	t    *oType
}

type oSig struct {
	params, results []*oType
	fails           []*oType // records
	needs           []*oType // operation types
	ambient         bool
}

// oItem is an exported name of the package: a function, a let or a named
// context binding the operation type t.
type oItem struct {
	kind string // "func", "let", "context"
	name string
	sig  *oSig
	t    *oType
	impl *oType // a context's provider record (rendering only)
}

// oMentions adds to out the name of every declared type t is made of, by
// the rule's list of parts.
func oMentions(t *oType, out map[string]bool) {
	if t.name != "" {
		out[t.name] = true
	}
	switch t.kind {
	case oInt:
	case oRec:
		for _, f := range t.fields {
			oMentions(f.t, out)
		}
	case oSlice, oMap, oMapKey, oOption, oSeq, oRowBox, oBox:
		oMentions(t.elem, out)
	case oFunc, oOps:
		oSigMentions(t.sig, out)
	case oTuple, oUnion, oMachine:
		for _, e := range t.elems {
			oMentions(e, out)
		}
	case oHost:
		for _, f := range t.fields {
			oMentions(f.t, out)
		}
		for _, c := range t.impls {
			oMentions(c, out)
		}
	default:
		panic("oMentions: kind " + strconv.Itoa(int(t.kind)))
	}
}

func oSigMentions(s *oSig, out map[string]bool) {
	for _, ts := range [][]*oType{s.params, s.results, s.fails, s.needs} {
		for _, x := range ts {
			oMentions(x, out)
		}
	}
}

// oracle answers on the model: the exported items the rule reports, and
// how many functions that mention a held type the exemption spares.
func oracle(items []oItem, held []string) (flagged []string, spared int) {
	hits := func(m map[string]bool) bool {
		for _, h := range held {
			if m[h] {
				return true
			}
		}
		return false
	}
	for _, it := range items {
		m := map[string]bool{}
		switch it.kind {
		case "func":
			if it.sig.ambient {
				continue
			}
			exempt := false
			for _, p := range it.sig.params {
				exempt = exempt || slices.Contains(held, p.name) && (p.kind == oRec || p.kind == oHost)
			}
			oSigMentions(it.sig, m)
			if exempt {
				if hits(m) {
					spared++
				}
				continue
			}
		default:
			oMentions(it.t, m)
		}
		if hits(m) {
			flagged = append(flagged, it.name)
		}
	}
	slices.Sort(flagged)
	return flagged, spared
}

// oGen builds one package of the model and its source.
type oGen struct {
	r     *rand.Rand
	n     int
	decls strings.Builder
	done  map[*oType]bool
	hits  map[string]int
	items []oItem
	other *oType   // a record with authority that nothing hands out
	held  []*oType // Host and what it needs
}

func (g *oGen) fresh(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *oGen) rec(auth bool, fields ...oField) *oType {
	return &oType{kind: oRec, name: g.fresh("R"), auth: auth, fields: fields}
}

func (g *oGen) fn(s *oSig) *oType { return &oType{kind: oFunc, name: g.fresh("Fn"), sig: s} }

func (g *oGen) ops(s *oSig) *oType {
	return &oType{kind: oOps, name: g.fresh("Op"), sig: s, op: g.fresh("Do")}
}

// keyable reports whether x is a Map key the checker accepts: an int or a
// record of keyable fields.
func keyable(x *oType) bool {
	switch x.kind {
	case oInt:
		return true
	case oRec, oHost:
		if x.kind == oHost && len(x.impls) > 0 {
			return false
		}
		for _, f := range x.fields {
			if !keyable(f.t) {
				return false
			}
		}
		return true
	}
	return false
}

// oWords are the constructors wrap picks from.
var oWords = []string{"slice", "map", "map-key", "option", "seq", "open", "hidden", "shown", "union", "tuple",
	"func-result", "func-param", "func-failure", "func-needs", "func-needs-source",
	"ops-result", "ops-param", "ops-failure", "twin", "row-arg", "machine-state", "machine-signal", "box"}

// wrap puts x in one random constructor; the hit names it.
func (g *oGen) wrap(x *oType) *oType {
	w := oWords[g.r.IntN(len(oWords))]
	if w == "map-key" && !keyable(x) {
		w = "map"
	}
	g.hits["word "+w]++
	switch w {
	case "slice":
		return &oType{kind: oSlice, elem: x}
	case "map":
		return &oType{kind: oMap, elem: x}
	case "map-key":
		return &oType{kind: oMapKey, elem: x}
	case "option":
		return &oType{kind: oOption, elem: x}
	case "seq":
		return &oType{kind: oSeq, elem: x}
	case "open":
		return g.rec(false, oField{"X", x})
	case "hidden":
		return g.rec(true, oField{"x", x}, oField{"N", &oType{kind: oInt}})
	case "shown":
		return g.rec(true, oField{"X", x}, oField{"n", &oType{kind: oInt}})
	case "union":
		return &oType{kind: oUnion, name: g.fresh("U"), elems: []*oType{g.rec(false, oField{"X", x}), g.rec(false)}}
	case "tuple":
		return &oType{kind: oTuple, name: g.fresh("Tp"), elems: []*oType{x, {kind: oInt}}}
	case "func-result":
		return g.fn(&oSig{results: []*oType{x}})
	case "func-param":
		return g.fn(&oSig{params: []*oType{x}})
	case "func-failure":
		return g.fn(&oSig{results: []*oType{{kind: oInt}}, fails: []*oType{g.rec(false, oField{"X", x})}})
	case "func-needs":
		return g.fn(&oSig{needs: []*oType{g.ops(&oSig{params: []*oType{x}})}})
	case "func-needs-source":
		return g.fn(&oSig{needs: []*oType{g.ops(&oSig{results: []*oType{x}})}})
	case "ops-result":
		return g.ops(&oSig{results: []*oType{x}})
	case "ops-param":
		return g.ops(&oSig{params: []*oType{x}})
	case "ops-failure":
		return g.ops(&oSig{results: []*oType{{kind: oInt}}, fails: []*oType{g.rec(false, oField{"X", x})}})
	case "row-arg":
		return &oType{kind: oRowBox, name: g.fresh("Rw"), elem: g.rec(false, oField{"X", x})}
	case "machine-state", "machine-signal":
		holder := g.rec(false, oField{"X", x})
		state := &oType{kind: oUnion, name: g.fresh("U"), elems: []*oType{g.rec(false), g.rec(false)}}
		signal := &oType{kind: oUnion, name: g.fresh("U"), elems: []*oType{g.rec(false), g.rec(false)}}
		if w == "machine-state" {
			state.elems[1] = holder
		} else {
			signal.elems[1] = holder
		}
		return &oType{kind: oMachine, name: g.fresh("Mc"), elems: []*oType{state, signal}}
	case "box":
		return &oType{kind: oBox, name: g.fresh("Box"), elem: x}
	}
	// twin: x where the caller supplies it (F's parameter), then where it
	// gets it (Y), in one record (batch-10 r3 T1).
	w2 := g.rec(false, oField{"X", x})
	return g.rec(false, oField{"F", g.fn(&oSig{params: []*oType{w2}})}, oField{"Y", w2})
}

// shape is x under 0 to 3 random constructors.
func (g *oGen) shape(x *oType) *oType {
	d := g.r.IntN(4)
	g.hits["depth "+strconv.Itoa(d)]++
	for range d {
		x = g.wrap(x)
	}
	return x
}

// oForms are the parameter forms param picks from.
var oForms = []string{"direct", "box", "union", "slice", "callback", "option", "held"}

// param is a parameter type over the record x, in a random form; "held"
// is a held type itself, the rule's one exemption.
func (g *oGen) param(x *oType) *oType {
	f := oForms[g.r.IntN(len(oForms))]
	g.hits["param "+f]++
	switch f {
	case "box":
		return g.rec(false, oField{"T", x})
	case "union":
		return &oType{kind: oUnion, name: g.fresh("U"), elems: []*oType{g.rec(false, oField{"T", x}), g.rec(false, oField{"O", g.other})}}
	case "slice":
		return &oType{kind: oSlice, elem: x}
	case "callback":
		return g.fn(&oSig{params: []*oType{x}})
	case "option":
		return &oType{kind: oOption, elem: x}
	case "held":
		return g.held[g.r.IntN(len(g.held))]
	}
	return x
}

// item hands out s (a shape over a target) through a random channel, with
// the parameters ps.
func (g *oGen) item(s *oType, ps []*oType) {
	chans := []string{"result", "results", "failure", "param", "needs", "needs-source", "let", "context-result", "context-param"}
	c := chans[g.r.IntN(len(chans))]
	g.hits["channel "+c]++
	sig := &oSig{params: ps, ambient: g.r.IntN(4) == 0}
	switch c {
	case "result":
		sig.results = []*oType{s}
	case "results":
		sig.results = []*oType{{kind: oInt}, s}
	case "failure":
		sig.results = []*oType{{kind: oInt}}
		sig.fails = []*oType{g.rec(false, oField{"X", s})}
	case "param":
		sig.params = append(slices.Clone(ps), s)
	case "needs":
		sig.needs = []*oType{g.ops(&oSig{params: []*oType{s}})}
	case "needs-source":
		sig.needs = []*oType{g.ops(&oSig{results: []*oType{s}})}
	case "let":
		g.items = append(g.items, oItem{kind: "let", name: g.fresh("Given"), t: s})
		return
	case "context-result", "context-param":
		o := g.ops(&oSig{results: []*oType{s}})
		if c == "context-param" {
			o = g.ops(&oSig{params: []*oType{s}})
		}
		// The provider is a record with authority that nothing hands out;
		// its operation is an exported function of the package whose
		// first parameter is the provider.
		impl := g.rec(true, oField{"n", &oType{kind: oInt}})
		g.items = append(g.items,
			oItem{kind: "context", name: g.fresh("Ctx"), t: o, impl: impl},
			oItem{kind: "func", name: o.op, sig: &oSig{params: append([]*oType{impl}, o.sig.params...), results: o.sig.results}})
		return
	}
	if sig.ambient {
		g.hits["ambient"]++
	}
	g.items = append(g.items, oItem{kind: "func", name: g.fresh("Make"), sig: sig})
}

// text is t's source, declaring its named types once.
func (g *oGen) text(t *oType) string {
	switch t.kind {
	case oInt:
		return "int"
	case oSlice:
		return "[]" + g.text(t.elem)
	case oMap:
		return "Map[string, " + g.text(t.elem) + "]"
	case oMapKey:
		return "Map[" + g.text(t.elem) + ", int]"
	case oOption:
		return "Option[" + g.text(t.elem) + "]"
	case oSeq:
		return "Seq[" + g.text(t.elem) + "]"
	case oRowBox:
		if !g.done[t] {
			g.done[t] = true
			g.decls.WriteString("type " + t.name + "[E] { F func() int fails E }\n\n")
		}
		return t.name + "[" + g.text(t.elem) + "]"
	case oBox:
		if !g.done[t] {
			g.done[t] = true
			g.decls.WriteString("type " + t.name + "[T] { f T }\n\n")
		}
		return t.name + "[" + g.text(t.elem) + "]"
	}
	if !g.done[t] {
		g.done[t] = true
		var d string
		switch t.kind {
		case oRec:
			var fs []string
			for _, f := range t.fields {
				fs = append(fs, f.name+" "+g.text(f.t))
			}
			d = "type " + t.name + " { " + strings.Join(fs, ", ") + " }"
			if len(fs) == 0 {
				d = "type " + t.name + " {}"
			}
		case oFunc:
			d = "type " + t.name + " func" + g.sigText(t.sig)
		case oOps:
			d = "type " + t.name + " { " + t.op + g.sigText(t.sig) + " }"
		case oTuple:
			var es []string
			for _, e := range t.elems {
				es = append(es, g.text(e))
			}
			d = "type " + t.name + " (" + strings.Join(es, ", ") + ")"
		case oUnion:
			var vs []string
			for _, e := range t.elems {
				vs = append(vs, g.text(e))
			}
			d = "type " + t.name + " = " + strings.Join(vs, " | ")
		case oMachine:
			st, sg := g.text(t.elems[0]), g.text(t.elems[1])
			step := strings.ToLower(t.name) + "Step"
			d = "func " + step + "(s " + st + ", e " + sg + ") Decision[" + st + ", int] { Ignore }\n\n" +
				"type " + t.name + " machine[" + st + ", " + sg + "] { initial: " + g.text(t.elems[0].elems[0]) + ", step: " + step + " }"
		}
		g.decls.WriteString(d + "\n\n")
	}
	return t.name
}

// sigText is "(p0 A, …) R fails F needs C".
func (g *oGen) sigText(s *oSig) string {
	var ps []string
	for i, p := range s.params {
		ps = append(ps, fmt.Sprintf("p%d %s", i, g.text(p)))
	}
	out := "(" + strings.Join(ps, ", ") + ")"
	var rs []string
	for _, r := range s.results {
		rs = append(rs, g.text(r))
	}
	switch len(rs) {
	case 0:
	case 1:
		out += " " + rs[0]
	default:
		out += " (" + strings.Join(rs, ", ") + ")"
	}
	var fs, ns []string
	for _, f := range s.fails {
		fs = append(fs, g.text(f))
	}
	for _, n := range s.needs {
		ns = append(ns, g.text(n))
	}
	if s.ambient {
		ns = append(ns, "Ambient")
	}
	if len(fs) > 0 {
		out += " fails " + strings.Join(fs, " | ")
	}
	if len(ns) > 0 {
		out += " needs " + strings.Join(ns, " | ")
	}
	return out
}

// source renders the package: the items, then every type they name, then
// Host's declaration (hostDecl) and its reach of the host.
func (g *oGen) source(hostDecl string) string {
	var b strings.Builder
	for i, it := range g.items {
		switch it.kind {
		case "func":
			var args []string
			for j := range it.sig.params {
				args = append(args, fmt.Sprintf("p%d", j))
			}
			fmt.Fprintf(&b, "func %s%s { %s(%s) }\n\n", it.name, g.sigText(it.sig), it.name, strings.Join(args, ", "))
		case "let":
			low := strings.ToLower(it.name)
			fmt.Fprintf(&b, "let %s %s = %s()\n\nfunc %s() %s { %s() }\n\n", it.name, g.text(it.t), low, low, g.text(it.t), low)
		case "context":
			fmt.Fprintf(&b, "let %s = context { %s: %s{n: %d} }\n\n", it.name, g.text(it.t), g.text(it.impl), i)
		}
	}
	return b.String() + g.decls.String() + hostDecl + `
func NewHost() Host needs Ambient { NewHost() }

type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
}

// oPackage is the package of one seed: chain records T1..Tk (k = 1..3)
// and Host in one of three forms, each handed out by one or two items
// whose parameters may be over the record before it, plus decoys over any
// of them. It returns the generator, Host's declaration and the held names.
func oPackage(seed uint64) (*oGen, string, []string) {
	g := &oGen{r: rand.New(rand.NewPCG(seed, 0x0fac7)), done: map[*oType]bool{}, hits: map[string]int{}}
	g.other = g.rec(true, oField{"v", &oType{kind: oInt}})
	k := 1 + g.r.IntN(3)
	var targets []*oType
	for range k {
		targets = append(targets, g.rec(true, oField{"v", &oType{kind: oInt}}))
	}
	host := &oType{kind: oHost, name: "Host"}
	g.done[host] = true // declared by source()
	var hostDecl string
	switch form := []string{"own", "holds", "sealed"}[g.r.IntN(3)]; form {
	case "own":
		g.hits["host own"]++
		hostDecl = "type Host { root string }\n"
		g.held = []*oType{host}
	case "holds":
		g.hits["host holds"]++
		host.fields = []oField{{"C", targets[0]}}
		hostDecl = "type Host { C " + g.text(targets[0]) + " }\n"
		g.held = []*oType{host, targets[0]}
	case "sealed":
		g.hits["host sealed"]++
		live := &oType{kind: oRec, name: "Live", auth: true, fields: []oField{{"root", &oType{kind: oInt}}}}
		g.done[live] = true
		host.impls = []*oType{live}
		hostDecl = "type Host { peek() int }\n\ntype Live { root int }\n\nfunc peek(_ Live) int { 1 }\n"
		g.held = []*oType{host, live}
	}
	targets = append(targets, host)
	for i, t := range targets {
		for range 1 + g.r.IntN(2) {
			var ps []*oType
			if i > 0 && g.r.IntN(4) != 0 {
				ps = append(ps, g.param(targets[i-1]))
			}
			g.item(g.shape(t), ps)
		}
	}
	for range g.r.IntN(3) {
		g.hits["decoy"]++
		g.item(g.shape(targets[g.r.IntN(len(targets))]), nil)
	}
	var held []string
	for _, h := range g.held {
		held = append(held, h.name)
	}
	return g, hostDecl, held
}

// TestStdHostGuardAgreesWithOracle is the property: on every seed, the
// exported items the host guard flags for fixture.Host are the items the
// oracle reports. A failing seed prints its source and both verdicts;
// ERGO_HANDOUT_SEED=<n> replays one seed.
func TestStdHostGuardAgreesWithOracle(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	seeds := make([]uint64, 0, 1500)
	if s := os.Getenv("ERGO_HANDOUT_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for n := range uint64(1500) {
			seeds = append(seeds, n+1)
		}
	}
	hits := map[string]int{}
	verdicts := map[bool]int{}
	failed := 0
	for _, seed := range seeds {
		g, hostDecl, held := oPackage(seed)
		src := g.source(hostDecl)
		for k, v := range g.hits {
			hits[k] += v
		}
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
		for _, d := range ds {
			if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
				t.Fatalf("seed %d: %v\n%s", seed, ds, src)
			}
		}
		var host *check.TypeName
		for _, tn := range hostProviderTypes(fake) {
			if tn.Name == "Host" {
				host = tn
			}
		}
		if host == nil {
			t.Fatalf("seed %d: no host provider Host\n%s", seed, src)
		}
		violations := hostAuthorityViolations(fake, host, outside)
		var guard []string
		for _, v := range violations {
			name, _, _ := strings.Cut(strings.TrimPrefix(v, "fixture."), " ")
			if !slices.Contains(guard, name) {
				guard = append(guard, name)
			}
		}
		slices.Sort(guard)
		want, spared := oracle(g.items, held)
		hits["spared"] += spared
		verdicts[len(want) > 0]++
		if len(seeds) == 1 {
			t.Logf("seed %d: the guard flags %v, the oracle %v (held %v)\n%s", seed, guard, want, held, src)
		}
		if !slices.Equal(guard, want) {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (ERGO_HANDOUT_SEED=%d): the guard flags %v, the oracle %v (held %v)\nguard: %q\n%s", seed, seed, guard, want, held, violations, src)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d seeds disagree", failed, len(seeds))
	}
	if len(seeds) > 1 {
		// Every constructor, channel, parameter form, depth and host form
		// is reached, and both verdicts occur.
		var miss []string
		for _, w := range oWords {
			if hits["word "+w] == 0 {
				miss = append(miss, "word "+w)
			}
		}
		for _, c := range []string{"result", "results", "failure", "param", "needs", "needs-source", "let", "context-result", "context-param"} {
			if hits["channel "+c] == 0 {
				miss = append(miss, "channel "+c)
			}
		}
		for _, p := range oForms {
			if hits["param "+p] == 0 {
				miss = append(miss, "param "+p)
			}
		}
		for d := range 4 {
			if hits["depth "+strconv.Itoa(d)] == 0 {
				miss = append(miss, "depth "+strconv.Itoa(d))
			}
		}
		for _, h := range []string{"own", "holds", "sealed"} {
			if hits["host "+h] == 0 {
				miss = append(miss, "host "+h)
			}
		}
		if hits["ambient"] == 0 || hits["decoy"] == 0 || hits["spared"] == 0 || verdicts[true] == 0 || verdicts[false] == 0 {
			miss = append(miss, "ambient, decoy, the exemption or a verdict")
		}
		if len(miss) > 0 {
			t.Errorf("the generator never reached %v", miss)
		}
	}
	t.Logf("%d seeds; an item reported in %d, none in %d; %d mentioning functions spared by the exemption", len(seeds), verdicts[true], verdicts[false], hits["spared"])
}
