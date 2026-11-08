package std_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// This file is fu-ambient-ctor r4's independent oracle. Three rounds of
// the host guard each missed one channel by which a package hands a value
// to its caller (constructor chains, failures, needs rows). The guard reads
// the checker's types through check.Exposes and check.BuildableGiven; the
// oracle below reads none of them. A seeded generator builds a model of a
// package's exported surface (oType, oItem), renders it to Ergo source, and
// the oracle answers on the model alone: which records a caller with no
// Ambient obtains (a least fixed point), and so which exported items hand
// it a Host. The guard, run on the checked source, must flag exactly those
// items.

// oKind is a constructor of the model's types.
type oKind int

const (
	oInt    oKind = iota
	oRec          // a record; auth when it has an unexported field
	oSlice        // []elem
	oMap          // Map[string, elem]
	oOption       // Option[elem]
	oSeq          // Seq[elem]
	oFunc         // a defined function type over sig
	oOps          // an operation type with one operation, sig
	oTuple        // a defined tuple type (elems...)
	oUnion        // a union of the records elems
)

type oType struct {
	kind   oKind
	name   string // declared name: oRec, oFunc, oOps, oTuple, oUnion
	auth   bool   // oRec: an unexported field
	fields []oField
	elem   *oType
	sig    *oSig // oFunc; oOps's operation
	op     string
	elems  []*oType
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

func exported(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

// oExposed adds to out the records with authority a caller gets out of a
// value of t where it gets one (gets), or that it is given where it
// supplies one: a caller supplies a parameter and a needs row's provider,
// and a supplied function is given its parameters. The model is a finite
// tree (shared nodes, no cycle), so the walk needs no seen-set.
func oExposed(t *oType, gets bool, out map[string]bool) {
	switch t.kind {
	case oInt:
	case oRec:
		if t.auth && gets {
			out[t.name] = true
		}
		for _, f := range t.fields {
			if !t.auth || exported(f.name) {
				oExposed(f.t, gets, out)
			}
		}
	case oSlice, oMap, oOption:
		oExposed(t.elem, gets, out)
	case oSeq:
		// Seq[T] is func(yield func(v T) bool): it calls the caller's
		// yield with each T.
		oExposed(t.elem, gets, out)
	case oFunc, oOps:
		oSigExposed(t.sig, gets, out)
	case oTuple, oUnion:
		for _, e := range t.elems {
			oExposed(e, gets, out)
		}
	default:
		panic("oExposed: kind " + strconv.Itoa(int(t.kind)))
	}
}

func oSigExposed(s *oSig, gets bool, out map[string]bool) {
	for _, r := range s.results {
		oExposed(r, gets, out)
	}
	for _, f := range s.fails {
		oExposed(f, gets, out)
	}
	for _, p := range s.params {
		oExposed(p, !gets, out)
	}
	for _, n := range s.needs {
		oExposed(n, !gets, out)
	}
}

// oBuildable reports whether a caller makes a value of t from literals
// and the records it holds (got).
func oBuildable(t *oType, got map[string]bool) bool {
	switch t.kind {
	case oRec:
		if t.auth {
			return got[t.name]
		}
		for _, f := range t.fields {
			if !oBuildable(f.t, got) {
				return false
			}
		}
		return true
	case oTuple:
		for _, e := range t.elems {
			if !oBuildable(e, got) {
				return false
			}
		}
		return true
	case oUnion:
		for _, e := range t.elems {
			if oBuildable(e, got) {
				return true
			}
		}
		return false
	}
	// int; an empty slice or Map; None; a function literal; an operation
	// type the caller implements.
	return true
}

// oracle answers on the model: the records a caller with no Ambient
// obtains, and the items that hand it a Host.
func oracle(items []oItem) (got map[string]bool, flagged []string) {
	usable := func(it oItem, got map[string]bool) bool {
		if it.kind != "func" {
			return true // a let; a named context, which needs no authority
		}
		if it.sig.ambient {
			return false
		}
		for _, p := range it.sig.params {
			if !oBuildable(p, got) {
				return false
			}
		}
		return true
	}
	gives := func(it oItem) map[string]bool {
		out := map[string]bool{}
		if it.kind == "func" {
			oSigExposed(it.sig, true, out)
		} else {
			oExposed(it.t, true, out)
		}
		return out
	}
	got = map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, it := range items {
			if !usable(it, got) {
				continue
			}
			for r := range gives(it) {
				if !got[r] {
					got[r], changed = true, true
				}
			}
		}
	}
	for _, it := range items {
		if usable(it, got) && gives(it)["Host"] {
			flagged = append(flagged, it.name)
		}
	}
	slices.Sort(flagged)
	return got, flagged
}

// oGen builds one package of the model and its source.
type oGen struct {
	r     *rand.Rand
	n     int
	decls strings.Builder
	done  map[*oType]bool
	hits  map[string]int
	items []oItem
	other *oType // a record with authority that nothing hands out
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

// wrap puts x in one random constructor; the hit names it.
func (g *oGen) wrap(x *oType) *oType {
	words := []string{"slice", "map", "option", "seq", "open", "hidden", "shown", "union", "tuple",
		"func-result", "func-param", "func-failure", "func-needs", "func-needs-source",
		"ops-result", "ops-param", "ops-failure", "twin"}
	w := words[g.r.IntN(len(words))]
	g.hits["word "+w]++
	switch w {
	case "slice":
		return &oType{kind: oSlice, elem: x}
	case "map":
		return &oType{kind: oMap, elem: x}
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

// param is a parameter type over the record x, in a random form.
func (g *oGen) param(x *oType) *oType {
	forms := []string{"direct", "box", "union", "slice", "callback", "option"}
	f := forms[g.r.IntN(len(forms))]
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
	case oOption:
		return "Option[" + g.text(t.elem) + "]"
	case oSeq:
		return "Seq[" + g.text(t.elem) + "]"
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
// Host's reach of the host.
func (g *oGen) source() string {
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
	return b.String() + g.decls.String() + `type Host { root string }

func NewHost() Host needs Ambient { NewHost() }

type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
}

// oPackage is the package of one seed: chain records T1..Tk (k = 1..3)
// and Host, each handed out by one or two items whose parameters may be
// over the record before it, plus decoys over any of them.
func oPackage(seed uint64) (*oGen, []oItem) {
	g := &oGen{r: rand.New(rand.NewPCG(seed, 0x0fac7)), done: map[*oType]bool{}, hits: map[string]int{}}
	g.other = g.rec(true, oField{"v", &oType{kind: oInt}})
	k := 1 + g.r.IntN(3)
	var targets []*oType
	for range k {
		targets = append(targets, g.rec(true, oField{"v", &oType{kind: oInt}}))
	}
	host := &oType{kind: oRec, name: "Host", auth: true}
	g.done[host] = true // declared by source()
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
	return g, g.items
}

// TestStdHostGuardAgreesWithOracle is the property: on every seed, the
// exported items the host guard flags for fixture.Host are the items the
// oracle says hand a caller with no Ambient a Host. A failing seed prints
// its source and both verdicts; ERGO_HANDOUT_SEED=<n> replays one seed.
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
		g, items := oPackage(seed)
		src := g.source()
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
		got, want := oracle(items)
		verdicts[len(want) > 0]++
		if len(seeds) == 1 {
			t.Logf("seed %d: the guard flags %v, the oracle %v (a caller with no Ambient obtains %v)\n%s", seed, guard, want, slices.Sorted(maps.Keys(got)), src)
		}
		if !slices.Equal(guard, want) {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (ERGO_HANDOUT_SEED=%d): the guard flags %v, the oracle %v (a caller with no Ambient obtains %v)\nguard: %q\n%s", seed, seed, guard, want, slices.Sorted(maps.Keys(got)), violations, src)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d seeds disagree", failed, len(seeds))
	}
	if len(seeds) > 1 {
		// Every constructor, channel, parameter form and depth is reached,
		// and both verdicts occur.
		var miss []string
		for _, w := range []string{"slice", "map", "option", "seq", "open", "hidden", "shown", "union", "tuple",
			"func-result", "func-param", "func-failure", "func-needs", "func-needs-source",
			"ops-result", "ops-param", "ops-failure", "twin"} {
			if hits["word "+w] == 0 {
				miss = append(miss, "word "+w)
			}
		}
		for _, c := range []string{"result", "results", "failure", "param", "needs", "needs-source", "let", "context-result", "context-param"} {
			if hits["channel "+c] == 0 {
				miss = append(miss, "channel "+c)
			}
		}
		for _, p := range []string{"direct", "box", "union", "slice", "callback", "option"} {
			if hits["param "+p] == 0 {
				miss = append(miss, "param "+p)
			}
		}
		for d := range 4 {
			if hits["depth "+strconv.Itoa(d)] == 0 {
				miss = append(miss, "depth "+strconv.Itoa(d))
			}
		}
		if hits["ambient"] == 0 || hits["decoy"] == 0 || verdicts[true] == 0 || verdicts[false] == 0 {
			miss = append(miss, "ambient, decoy or a verdict")
		}
		if len(miss) > 0 {
			t.Errorf("the generator never reached %v", miss)
		}
	}
	t.Logf("%d seeds; Host handed out in %d, not in %d", len(seeds), verdicts[true], verdicts[false])
}
