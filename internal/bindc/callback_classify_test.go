package bindc

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// This file holds TestCallbackClassificationTotality and its own rule table
// for callback positions and for the callback and token positions of
// functions. Like tRules (classify_test.go), the table restates the rules
// of the package doc from the C types alone and calls no classifier code.

// tCbPos is the table's class of one callback parameter.
type tCbPos struct {
	class  Class
	length int
	bytes  bool
	array  int64
	why    string // "none", "two" or "exception" for a raw position
}

// tCbEnv is one generated header and contract as the table sees them.
type tCbEnv struct {
	*tEnv
	entries map[string]CallbackRule
	cbExc   map[string]map[int]Exception
}

// lenPrev: parameter i is the length of the `const T *` buffer before it.
func (e *tCbEnv) lenPrev(ps []Param, i int) bool {
	if i == 0 || !isConstPtr(ps[i-1].Type) || !e.item(*ps[i-1].Type.Elem) {
		return false
	}
	el, by := e.length(ps[i-1], ps[i])
	return el || by
}

// cbHits are the names of the callback rules that match parameter i of cf.
func (e *tCbEnv) cbHits(cf *Function, key []string, i int) []tRule {
	p := cf.Params[i]
	keyed := p.Name != "" && slices.Contains(key, p.Name)
	var hits []tRule
	if keyed {
		hits = append(hits, tRule{name: "key", class: ClassKey})
	}
	if _, ok := e.scalar(p.Type); ok && !keyed && !e.lenPrev(cf.Params, i) {
		hits = append(hits, tRule{name: "scalar", class: ClassScalar})
	}
	if e.lenPrev(cf.Params, i) {
		hits = append(hits, tRule{name: "length", class: ClassLength})
	}
	if p.Type.Kind == TypePointer {
		for _, r := range tRules {
			if (r.name == "value" || r.name == "in_buffer" || r.name == "in_buffer_bytes" || r.name == "out") && r.match(e.tEnv, cf, i) {
				hits = append(hits, r)
			}
		}
	}
	return hits
}

// expectCallback classifies the parameters of the callback d with its
// entry by the table, and says whether it is bound.
func (e *tCbEnv) expectCallback(d TypeDecl) ([]tCbPos, bool) {
	cf := &Function{Name: d.Name, Params: d.Params}
	key := e.entries[d.Name].Key.Params
	out := make([]tCbPos, len(d.Params))
	for i := range d.Params {
		if x, ok := e.cbExc[d.Name][i]; ok {
			out[i] = tCbPos{class: x.Class, length: -1, array: x.ArrayLength, why: "exception"}
			continue
		}
		hits := e.cbHits(cf, key, i)
		q := tCbPos{length: -1}
		switch len(hits) {
		case 0:
			q.class, q.why = ClassRaw, "none"
		case 1:
			q.class = hits[0].class
			switch hits[0].name {
			case "in_buffer", "in_buffer_bytes":
				q.length, q.bytes = i+1, hits[0].name == "in_buffer_bytes"
			case "length":
				q.length = i - 1
			}
		default:
			q.class, q.why = ClassRaw, "two"
		}
		out[i] = q
	}
	for i, q := range out {
		switch {
		case q.class == ClassRaw:
			return out, false
		case q.class == ClassInBuffer && (out[q.length].class != ClassLength || out[q.length].length != i):
			return out, false
		case q.class == ClassLength && (out[q.length].class != ClassInBuffer || out[q.length].length != i):
			return out, false
		}
	}
	return out, true
}

// source: f has the parameter that gives the key of entry r: a `void *`
// for a token, or a `const S *` whose struct S has exactly the key's
// fields, with the callback's types.
func (e *tCbEnv) source(d TypeDecl, r CallbackRule, f *Function) bool {
	for _, p := range f.Params {
		if p.Name != r.Key.Source {
			continue
		}
		t := p.Type
		if e.token(d, r) {
			return t.Kind == TypePointer && t.LenExpr == "" && t.Elem.Kind == TypeVoid && !t.Elem.Const
		}
		if !isConstPtr(t) || t.Elem.Kind != TypeNamed {
			return false
		}
		s, ok := e.types[t.Elem.Name]
		if !ok || s.Kind != DeclStruct || len(s.Fields) != len(r.Key.Params) {
			return false
		}
		for i, fl := range s.Fields {
			if fl.Name != r.Key.Params[i] {
				return false
			}
			for _, cp := range d.Params {
				if cp.Name == fl.Name {
					a, _ := e.scalar(fl.Type)
					b, _ := e.scalar(cp.Type)
					if a != b {
						return false
					}
				}
			}
		}
		return true
	}
	return false
}

// token: the key of r is one `void *` parameter of d.
func (e *tCbEnv) token(d TypeDecl, r CallbackRule) bool {
	if len(r.Key.Params) != 1 {
		return false
	}
	for _, p := range d.Params {
		if p.Name == r.Key.Params[0] {
			return p.Type.Kind == TypePointer && p.Type.Elem.Kind == TypeVoid && !p.Type.Elem.Const
		}
	}
	return false
}

// ownerThread: f has a `H *` parameter of a contract handle H whose thread
// is owner.
func (e *tCbEnv) ownerThread(f *Function) bool {
	for _, p := range f.Params {
		if p.Type.Kind == TypePointer && p.Type.Elem.Kind == TypeNamed {
			if h, ok := e.handles[p.Type.Elem.Name]; ok && h.Thread == "owner" {
				return true
			}
		}
	}
	return false
}

// registrars counts the functions that could register d typed: they take
// d, have its key's source and (when registered) its owner.
func (e *tCbEnv) registrars(d TypeDecl, r CallbackRule, funcs []Function) int {
	n := 0
	for i := range funcs {
		f := &funcs[i]
		takes := false
		for _, p := range f.Params {
			takes = takes || p.Type.Kind == TypeNamed && p.Type.Name == d.Name
		}
		if takes && e.source(d, r, f) && (r.Scope == ScopeCall || e.takes(f, r.Owner)) {
			n++
		}
	}
	return n
}

// expectFunc classifies the pointer positions of f by the table (the rules
// of tRules, the callback rule and the token rule), and says whether f is
// typed; bound says which callback typedefs are bound. A call-scoped
// callback with a handle key needs an owner-thread handle parameter (two
// threads with one key would share one entry), and a handle-keyed typedef
// serves one position of f (two would share one entry); why says which of
// these made f raw.
func (e *tCbEnv) expectFunc(f *Function, bound map[string]bool) (out []Class, typed bool, why string) {
	tokens := map[string]int{}
	handleKeyed := map[string]int{}
	typed = true
	for i, p := range f.Params {
		if !e.pointerPos(p.Type) {
			continue
		}
		var hits []Class
		if p.Type.Kind == TypeNamed {
			d := e.types[p.Type.Name]
			r, has := e.entries[d.Name]
			if has && bound[d.Name] && e.source(d, r, f) && (r.Scope == ScopeCall || e.takes(f, r.Owner)) {
				if r.Scope == ScopeCall && !e.token(d, r) && !e.ownerThread(f) {
					why = "call handle without owner thread"
				} else {
					hits = append(hits, ClassCallback)
					if e.token(d, r) {
						tokens[r.Key.Source]++
					} else {
						handleKeyed[d.Name]++
					}
				}
			}
		} else {
			for _, r := range tRules {
				if r.match(e.tEnv, f, i) {
					hits = append(hits, r.class)
				}
			}
			isVoid := p.Type.LenExpr == "" && p.Type.Elem.Kind == TypeVoid && !p.Type.Elem.Const
			for _, q := range f.Params {
				r, has := e.entries[q.Type.Name]
				if isVoid && p.Name != "" && q.Type.Kind == TypeNamed && has && e.token(e.types[q.Type.Name], r) && r.Key.Source == p.Name {
					hits = append(hits, ClassKey)
					break
				}
			}
		}
		if len(hits) == 1 {
			out = append(out, hits[0])
		} else {
			out = append(out, ClassRaw)
			typed = false
		}
	}
	if e.pointerPos(f.Result) {
		out, typed = append(out, ClassRaw), false
	}
	for _, n := range tokens {
		if n > 1 {
			typed = false // one token serves two callbacks
		}
	}
	for _, n := range handleKeyed {
		if n > 1 && typed {
			typed, why = false, "two handle positions" // one handle key serves two callbacks
		}
	}
	return out, typed, why
}

func (e *tCbEnv) takes(f *Function, h string) bool {
	for _, p := range f.Params {
		if p.Type.Kind == TypePointer && p.Type.Elem.Kind == TypeNamed && p.Type.Elem.Name == h {
			return true
		}
	}
	return false
}

// The generator of callback typedefs: groups of parameters that put the
// shapes of the callback rules next to each other, with names that the
// length templates and the key forms read.
var (
	cbGroups = [][]string{
		{"void *user_data"},
		{"uint64_t context_id", "uint32_t slot", "uint32_t generation"},
		{"uint32_t mode"}, {"float width"}, {"double depth"}, {"bool flag"}, {"int64_t delta"},
		{"const uint8_t *bytes", "uint32_t byte_count"}, // {stem}_count and byte_count: two rules
		{"const uint8_t *bytes", "uint32_t byte_count"},
		{"const uint8_t *bytes", "uint32_t byte_count"},
		{"const uint8_t *data", "uint32_t data_bytes"},
		{"const uint32_t *items", "uint32_t items_count"},
		{"const uint32_t *values", "uint32_t count"},
		{"const g_pt *pt"}, {"const g_pt *pts", "uint32_t pts_count"},
		{"float *out_size"}, {"uint32_t *out_value"},
		{"uint8_t *buf"}, {"void *other"}, {"uint32_t *count_ptr"},
		{"uint32_t byte_count"},
	}
	cbLengthPool = []string{"{stem}_count", "{name}_count", "count"}
	cbBytePool   = []string{"byte_count", "{name}_bytes", "{stem}_byte_count"}
	cbResults    = []string{"void", "void", "int64_t", "uint32_t", "bool"}
)

// cbGen is one generated case: its header and contract.
type cbGen struct {
	r   *rand.Rand
	src strings.Builder
	c   *Contract
}

// cbParamName is the name in a parameter declaration.
func cbParamName(decl string) string {
	return decl[strings.LastIndexAny(decl, " *")+1:]
}

// typedef makes callback g_cb<n>, maybe with an entry and exceptions.
func (g *cbGen) typedef(n int) {
	used := map[string]bool{}
	var ps []string
	// Most typedefs start with a key group, so that they can have an entry.
	if k := g.r.IntN(5); k < 2 {
		ps, used["user_data"] = []string{cbGroups[0][0]}, true
	} else if k < 4 {
		ps = slices.Clone(cbGroups[1])
		used["context_id"], used["generation"] = true, true
	}
	for len(ps) < 2 || len(ps) < 7 && g.r.IntN(4) != 0 {
		grp := cbGroups[g.r.IntN(len(cbGroups))]
		if used[cbParamName(grp[0])] || used[cbParamName(grp[len(grp)-1])] {
			continue
		}
		for _, p := range grp {
			used[cbParamName(p)] = true
		}
		ps = append(ps, grp...)
	}
	if g.r.IntN(3) == 0 {
		g.r.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
	}
	res := cbResults[g.r.IntN(len(cbResults))]
	name := fmt.Sprintf("g_cb%d", n)
	fmt.Fprintf(&g.src, "typedef %s (*%s)(%s);\n", res, name, strings.Join(ps, ", "))

	kinds := make([]string, len(ps))
	for i, p := range ps {
		kinds[i] = "int"
		if !strings.Contains(p, "*") && (strings.HasPrefix(p, "float ") || strings.HasPrefix(p, "double ")) {
			kinds[i] = "float"
		}
	}
	if ok, _ := cbOracle(kinds, map[bool]string{true: "void", false: "int"}[res == "void"]); !ok || g.r.IntN(5) == 0 {
		return // refused shapes take no entry; some admitted ones have none
	}
	var entry *CallbackRule
	switch {
	case used["user_data"] && (!used["context_id"] || g.r.IntN(2) == 0):
		entry = &CallbackRule{Key: KeyRule{Params: []string{"user_data"}, Source: "user_data"}}
		fmt.Fprintf(&g.src, "g_status g_reg%d(g_h *h, void *user_data, %s cb);\n", n, name)
	case used["context_id"]:
		entry = &CallbackRule{Key: KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}}
		// The registering functions of a handle key: one, one that takes
		// two positions (one key for both), two (one key for two
		// registrations), or one without the owner-thread handle.
		switch g.r.IntN(6) {
		case 0:
			fmt.Fprintf(&g.src, "g_status g_reg%d(g_h *h, const g_node *node, %s cb);\n", n, name)
		case 1, 4:
			fmt.Fprintf(&g.src, "g_status g_reg%d(g_h *h, const g_node *node, %s a, %s b);\n", n, name, name)
		case 2, 5:
			fmt.Fprintf(&g.src, "g_status g_reg%d(g_h *h, const g_node *node, %s cb);\n", n, name)
			fmt.Fprintf(&g.src, "g_status g_set%d(g_h *h, const g_node *node, %s cb);\n", n, name)
		case 3:
			fmt.Fprintf(&g.src, "g_status g_reg%d(const g_node *node, %s cb);\n", n, name)
		}
	default:
		return
	}
	entry.Typedef, entry.Scope, entry.Why = name, ScopeCall, "generated"
	if g.r.IntN(2) == 0 {
		entry.Scope, entry.Owner = ScopeRegistered, "g_h"
	}
	switch res {
	case "void":
	case "bool":
		entry.Default = []byte("false")
	default:
		entry.Default = []byte("0")
	}
	g.c.Callbacks = append(g.c.Callbacks, *entry)
	for _, p := range ps {
		pn := cbParamName(p)
		if slices.Contains(entry.Key.Params, pn) {
			continue
		}
		switch {
		case (pn == "out_size" || pn == "out_value" || pn == "buf") && g.r.IntN(3) == 0:
			g.c.Exceptions = append(g.c.Exceptions, Exception{Callback: name, Position: pn, Class: ClassOutArray,
				ArrayLength: int64(1 + g.r.IntN(4)), Why: "generated"})
		case g.r.IntN(12) == 0:
			g.c.Exceptions = append(g.c.Exceptions, Exception{Callback: name, Position: pn, Class: ClassRaw, Why: "generated"})
		}
	}
}

// function makes a function that takes one or two of the callbacks, and
// some of the key sources and owners.
func (g *cbGen) function(n, cbs int) {
	var ps []string
	if g.r.IntN(4) != 0 {
		ps = append(ps, "g_h *h")
	}
	for _, p := range []string{"const g_node *node", "void *user_data", "void *other", "uint32_t n"} {
		if g.r.IntN(2) == 0 {
			ps = append(ps, p)
		}
	}
	first := g.r.IntN(cbs)
	ps = append(ps, fmt.Sprintf("g_cb%d cb0", first))
	if g.r.IntN(2) == 0 {
		// Often two positions of one typedef: one key source serves both.
		second := g.r.IntN(cbs)
		if g.r.IntN(2) == 0 {
			second = first
		}
		ps = append(ps, fmt.Sprintf("g_cb%d cb1", second))
	}
	g.r.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
	fmt.Fprintf(&g.src, "g_status g_f%d(%s);\n", n, strings.Join(ps, ", "))
}

// TestCallbackClassificationTotality: for generated callback typedefs,
// contracts and the functions that take them, every callback parameter
// and every function position gets exactly one class, equal to the one
// the table above computes; zero or two matches make the typedef (or the
// function) raw with that reason.
func TestCallbackClassificationTotality(t *testing.T) {
	stats := map[string]int{}
	defer func() { t.Logf("%d cases: %v", *propCases, stats) }()
	for n := 0; n < *propCases; n++ {
		seed := *propSeed + uint64(n)
		g := &cbGen{r: rand.New(rand.NewPCG(seed, 0xcbc1))}
		g.src.WriteString(cbPrelude)
		g.c = cbContract("")
		g.c.Buffers.LengthNames, g.c.Buffers.ByteLengthNames = []string{}, []string{}
		// The first template of each pool comes often: together they give
		// byte_count two units.
		for i, l := range cbLengthPool {
			if g.r.IntN(4) < 1+2*min(1, 1-i) {
				g.c.Buffers.LengthNames = append(g.c.Buffers.LengthNames, l)
			}
		}
		for i, l := range cbBytePool {
			if g.r.IntN(4) < 1+2*min(1, 1-i) {
				g.c.Buffers.ByteLengthNames = append(g.c.Buffers.ByteLengthNames, l)
			}
		}
		// Sometimes g_h is not confined to an owner thread: then no function
		// can bind a call-scoped callback with a handle key, and a registered
		// handle-keyed callback owned by g_h is raw.
		if g.r.IntN(4) == 0 {
			g.c.Handles[0].Thread = "any"
		}
		cbs := 1 + g.r.IntN(4)
		for i := 0; i < cbs; i++ {
			g.typedef(i)
		}
		if g.r.IntN(2) == 0 {
			// A registered handle-keyed typedef in an admitted shape with one
			// function that registers it: bound when g_h has thread owner,
			// raw otherwise (its registry key holds the owner's thread).
			g.src.WriteString("typedef void (*g_kept)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);\n" +
				"g_status g_keep(g_h *h, const g_node *node, g_kept cb);\n")
			g.c.Callbacks = append(g.c.Callbacks, CallbackRule{Typedef: "g_kept", Scope: ScopeRegistered, Owner: "g_h",
				Key: KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}, Why: "generated"})
		}
		for i := 0; i < 1+g.r.IntN(4); i++ {
			g.function(i, cbs)
		}
		src := g.src.String()
		c := cbContract(src)
		c.Buffers, c.Callbacks, c.Exceptions, c.Handles = g.c.Buffers, g.c.Callbacks, g.c.Exceptions, g.c.Handles
		b, err := cbBind(t, src, c)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\ncontract %+v\n%s", seed, fmt.Sprintf(format, args...), c, src)
		}
		if err != nil {
			fail("Bind: %v", err)
		}
		e := &tCbEnv{tEnv: &tEnv{types: map[string]TypeDecl{}, c: c, handles: map[string]HandleRule{}, creates: map[string]HandleRule{},
			exc: map[string]map[int]Exception{}}, entries: map[string]CallbackRule{}, cbExc: map[string]map[int]Exception{}}
		for _, d := range b.Model.Types {
			e.types[d.Name] = d
		}
		for _, h := range c.Handles {
			e.handles[h.Type], e.creates[h.Create] = h, h
		}
		for _, r := range c.Callbacks {
			e.entries[r.Typedef] = r
		}
		for _, x := range c.Exceptions {
			for i, p := range e.types[x.Callback].Params {
				if p.Name == x.Position {
					if e.cbExc[x.Callback] == nil {
						e.cbExc[x.Callback] = map[int]Exception{}
					}
					e.cbExc[x.Callback][i] = x
				}
			}
		}

		bound := map[string]bool{}
		for _, bc := range b.Callbacks {
			d := *bc.Decl
			if _, has := e.entries[d.Name]; !has {
				if bc.Bound() || len(bc.Positions) != 0 {
					fail("%s has no entry, but it is bound or classified", d.Name)
				}
				continue
			}
			want, ok := e.expectCallback(d)
			if len(bc.Positions) != len(want) {
				fail("%s: %d positions, the table has %d", d.Name, len(bc.Positions), len(want))
			}
			for i, q := range bc.Positions {
				w := want[i]
				if q.Param != i || q.Class != w.class {
					fail("%s param %d: class %s, the table says %s (%s)", d.Name, i, q.Class, w.class, w.why)
				}
				if q.Class != ClassRaw && (q.Length != w.length || q.Bytes != w.bytes || q.Array != w.array) {
					fail("%s param %d: length %d bytes %v array %d, the table says %d %v %d", d.Name, i, q.Length, q.Bytes, q.Array, w.length, w.bytes, w.array)
				}
				switch w.why {
				case "two":
					if !strings.Contains(bc.RawOnly, positionName(callbackFunc(&d), i)+": rules ") || !strings.Contains(bc.RawOnly, "both cover it") {
						fail("%s param %d: two rules match, but the reason is %q", d.Name, i, bc.RawOnly)
					}
				case "none":
					if !strings.Contains(bc.RawOnly, positionName(callbackFunc(&d), i)+": ") {
						fail("%s param %d: no rule matches, but the reason is %q", d.Name, i, bc.RawOnly)
					}
				}
				stats["callback class "+string(q.Class)]++
				if w.why != "" {
					stats["callback by "+w.why]++
				}
			}
			// A registered callback with a handle key and two functions
			// that register it: the key does not say which one C's
			// invocation comes from.
			if r := e.entries[d.Name]; ok && r.Scope == ScopeRegistered && !e.token(d, r) && e.registrars(d, r, b.Model.Functions) > 1 {
				ok = false
				stats["callback two registrars"]++
				if !strings.Contains(bc.RawOnly, "functions that register it") {
					fail("%s: two functions register it with a handle key, but the reason is %q", d.Name, bc.RawOnly)
				}
			} else if ok && r.Scope == ScopeRegistered && !e.token(d, r) && e.handles[r.Owner].Thread != "owner" {
				// The registry key holds the owner's thread.
				ok = false
				stats["callback owner without thread"]++
				if !strings.Contains(bc.RawOnly, "has a handle key, and its owner "+r.Owner+" has thread "+e.handles[r.Owner].Thread) {
					fail("%s: a registered handle key whose owner has thread %s, but the reason is %q", d.Name, e.handles[r.Owner].Thread, bc.RawOnly)
				}
			}
			if bc.Bound() != ok {
				fail("%s: bound %v (%s), the table says %v", d.Name, bc.Bound(), bc.RawOnly, ok)
			}
			bound[d.Name] = ok
			if ok {
				stats["bound"]++
			} else {
				stats["raw callback"]++
			}
		}
		for _, bf := range b.Funcs {
			want, typed, why := e.expectFunc(bf.C, bound)
			switch why {
			case "call handle without owner thread":
				if !strings.Contains(bf.RawOnly, "needs a parameter `H *` of a handle with thread owner") {
					fail("%s: a call-scoped handle key without an owner-thread handle, but the reason is %q", bf.C.Name, bf.RawOnly)
				}
			case "two handle positions":
				if !strings.Contains(bf.RawOnly, "its handle key") || !strings.Contains(bf.RawOnly, "also serves") {
					fail("%s: one handle key serves two positions, but the reason is %q", bf.C.Name, bf.RawOnly)
				}
			}
			if why != "" {
				stats["function "+why]++
			}
			if len(bf.Positions) != len(want) {
				fail("%s: %d positions, the table has %d", bf.C.Name, len(bf.Positions), len(want))
			}
			for i, q := range bf.Positions {
				if q.Class != want[i] {
					fail("%s: position %d: class %s, the table says %s", bf.C.Name, q.Param, q.Class, want[i])
				}
				if q.Class == ClassRaw && q.Param >= 0 && bf.C.Params[q.Param].Type.Kind == TypeNamed &&
					!strings.Contains(bf.RawOnly, positionName(bf.C, q.Param)+": callback parameter: ") {
					fail("%s: a raw callback parameter, but the reason is %q", bf.C.Name, bf.RawOnly)
				}
				stats["function class "+string(q.Class)]++
			}
			if typed != (bf.RawOnly == "") {
				fail("%s: raw-only %q, the table says typed=%v", bf.C.Name, bf.RawOnly, typed)
			}
			if typed && slices.Contains(want, ClassCallback) {
				stats["typed with a callback"]++
			}
		}
	}
	if !testingShort() {
		for _, k := range []string{"callback by two", "callback by none", "callback by exception", "callback class key",
			"callback class scalar", "callback class length", "callback class in_buffer", "callback class value",
			"callback class out", "callback class out_array", "bound", "raw callback", "function class callback",
			"function class key", "typed with a callback", "callback two registrars", "callback owner without thread",
			"function call handle without owner thread", "function two handle positions"} {
			if stats[k] < *propCases/20 {
				t.Errorf("the generator makes %d of %q in %d cases; want at least %d", stats[k], k, *propCases, *propCases/20)
			}
		}
	}
}
