package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// This source model is independent of checker types, substitution, interning
// and Tarjan. Its components come from transitive closure. Only observe calls
// the public checker; no oracle decision uses an object returned by CheckSource.
type e1cType struct {
	kind byte // i=int, p=parameter, n=named, o=Option, s=slice, q=tuple
	d, p int
	args []e1cType
}

type e1cDecl struct {
	params int
	form   byte // r=record, d=defined base, q=defined tuple
	fields []e1cType
}

type e1cEdge struct {
	to              int
	value, argument bool
}

type e1cGroup struct {
	members []int
	value   bool
}

func (x e1cType) key() string {
	s := fmt.Sprintf("%c%d:%d", x.kind, x.d, x.p)
	for _, a := range x.args {
		s += "[" + a.key() + "]"
	}
	return s
}

func (x e1cType) text() string {
	switch x.kind {
	case 'i':
		return "int"
	case 'p':
		return []string{"T", "U"}[x.p]
	case 's':
		return "[]" + x.args[0].text()
	case 'o':
		return "Option[" + x.args[0].text() + "]"
	case 'q':
		return "(" + x.args[0].text() + ", int)"
	}
	s := fmt.Sprintf("D%d", x.d)
	if len(x.args) != 0 {
		var args []string
		for _, a := range x.args {
			args = append(args, a.text())
		}
		s += "[" + strings.Join(args, ", ") + "]"
	}
	return s
}

func e1cSub(x e1cType, args []e1cType) e1cType {
	if x.kind == 'p' {
		return args[x.p]
	}
	x.args = slices.Clone(x.args)
	for i, a := range x.args {
		x.args[i] = e1cSub(a, args)
	}
	return x
}

// e1cComponents computes reachability independently for each node. It returns
// only infinite components. The same relation works for the small K graph.
func e1cComponents(edges [][]e1cEdge) [][]int {
	reach := make([][]bool, len(edges))
	for i := range edges {
		reach[i] = make([]bool, len(edges))
		var visit func(int)
		visit = func(n int) {
			for _, e := range edges[n] {
				if !reach[i][e.to] {
					reach[i][e.to] = true
					visit(e.to)
				}
			}
		}
		visit(i)
	}
	var out [][]int
	seen := make([]bool, len(edges))
	for i := range edges {
		if seen[i] || !reach[i][i] {
			continue
		}
		var component []int
		for j := range edges {
			if reach[i][j] && reach[j][i] {
				seen[j] = true
				component = append(component, j)
			}
		}
		out = append(out, component)
	}
	return out
}

func e1cOracle(ds []e1cDecl) (groups []e1cGroup, finite bool) {
	var nodes []e1cType
	var edges [][]e1cEdge
	keys := map[string]int{}
	identity := make([]int, len(ds))
	expanding := false
	var intern func(e1cType) int
	intern = func(x e1cType) int {
		key := x.key()
		if n, ok := keys[key]; ok {
			return n
		}
		// A finite universe is required here. Larger terms are an expanding
		// instantiation, owned by the earlier monomorphisation check.
		if len(key) > 256 || len(nodes) >= 96 {
			expanding = true
			return 0
		}
		n := len(nodes)
		keys[key] = n
		nodes = append(nodes, x)
		edges = append(edges, nil)
		for _, a := range x.args {
			var discover func(e1cType)
			discover = func(t e1cType) {
				if t.kind == 'n' {
					intern(t)
				} else {
					for _, a := range t.args {
						discover(a)
					}
				}
			}
			discover(a)
		}
		return n
	}
	for d, decl := range ds {
		x := e1cType{kind: 'n', d: d}
		for p := 0; p < decl.params; p++ {
			x.args = append(x.args, e1cType{kind: 'p', d: d, p: p})
		}
		identity[d] = intern(x)
	}
	for n := 0; n < len(nodes) && !expanding; n++ {
		x := nodes[n]
		decl := ds[x.d]
		var discover func(e1cType)
		discover = func(t e1cType) {
			if t.kind == 'n' {
				intern(t)
			} else {
				for _, a := range t.args {
					discover(a)
				}
			}
		}
		var hold func(e1cType, bool, bool)
		hold = func(t e1cType, value, argument bool) {
			switch t.kind {
			case 'n':
				to := intern(t)
				edges[n] = append(edges[n], e1cEdge{to, value, argument})
			case 'q':
				hold(t.args[0], true, argument)
			}
		}
		for _, f := range decl.fields {
			typ := e1cSub(f, x.args)
			discover(typ) // breakers still instantiate their element types
			hold(typ, decl.form != 'd', f.kind == 'p')
		}
	}
	if expanding {
		return nil, false
	}
	cycles := e1cComponents(edges)
	home := make([]int, len(ds))
	for d := range home {
		home[d] = -1
		for c, cyc := range cycles {
			if slices.Contains(cyc, identity[d]) {
				home[d] = c
			}
		}
	}
	relation := make([][]e1cEdge, len(ds))
	for d, c := range home {
		if c < 0 {
			continue
		}
		for _, n := range cycles[c] {
			e := nodes[n].d
			if home[e] >= 0 {
				relation[d] = append(relation[d], e1cEdge{to: e})
			}
		}
	}
	for _, members := range e1cComponents(relation) {
		g := e1cGroup{members: members}
		for _, d := range members {
			cyc := cycles[home[d]]
			for _, n := range cyc {
				for _, e := range edges[n] {
					g.value = g.value || e.value && slices.Contains(cyc, e.to)
				}
			}
		}
		groups = append(groups, g)
	}
	pruned := make([][]e1cEdge, len(edges))
	for n, next := range edges {
		for _, e := range next {
			if e.argument || home[nodes[n].d] < 0 {
				pruned[n] = append(pruned[n], e)
			}
		}
	}
	var orphans []e1cGroup
	for _, cyc := range e1cComponents(pruned) {
		g := e1cGroup{}
		for _, n := range cyc {
			for _, e := range pruned[n] {
				if slices.Contains(cyc, e.to) {
					g.value = g.value || e.value
					if !e.argument && !slices.Contains(g.members, nodes[n].d) {
						g.members = append(g.members, nodes[n].d)
					}
				}
			}
		}
		// Merge orphan components by common authors, including transitive
		// overlaps. Intrinsic authors have already been removed.
		for i := 0; i < len(orphans); {
			o := orphans[i]
			shared := false
			for _, d := range o.members {
				shared = shared || slices.Contains(g.members, d)
			}
			if !shared {
				i++
				continue
			}
			for _, d := range o.members {
				if !slices.Contains(g.members, d) {
					g.members = append(g.members, d)
				}
			}
			g.value = g.value || o.value
			orphans = slices.Delete(orphans, i, i+1)
			i = 0
		}
		if len(g.members) > 0 {
			orphans = append(orphans, g)
		}
	}
	groups = append(groups, orphans...)
	// P3's model-side partition check: every original infinite component
	// contains a reported origin, or can be reached from its instances.
	for _, cyc := range cycles {
		covered := false
		for _, g := range groups {
			for n, x := range nodes {
				if !slices.Contains(g.members, x.d) {
					continue
				}
				seen := map[int]bool{}
				var visit func(int)
				visit = func(v int) {
					if seen[v] {
						return
					}
					seen[v] = true
					covered = covered || slices.Contains(cyc, v)
					for _, e := range edges[v] {
						visit(e.to)
					}
				}
				visit(n)
			}
		}
		if !covered {
			panic("R2 oracle left an infinite component unclassified")
		}
	}
	return groups, true
}

func e1cSource(ds []e1cDecl, order []int, repaired int, uses bool) string {
	var lines []string
	for _, d := range order {
		decl := ds[d]
		name := fmt.Sprintf("D%d", d)
		if decl.params > 0 {
			name += "[" + strings.Join([]string{"T", "U"}[:decl.params], ", ") + "]"
		}
		var fs []string
		for i, f := range decl.fields {
			typ := f.text()
			if decl.form == 'r' {
				if repaired == d {
					typ = "Option[" + typ + "]"
				}
				typ = fmt.Sprintf("F%d %s", i, typ)
			}
			fs = append(fs, typ)
		}
		body := fs[0]
		switch decl.form {
		case 'r':
			body = "{ " + strings.Join(fs, ", ") + " }"
		case 'q':
			body = "(" + body + ", int)"
		}
		if repaired == d && decl.form != 'r' {
			body = "[]" + body
		}
		lines = append(lines, "type "+name+" "+body)
	}
	if uses {
		for d, decl := range ds {
			x := e1cType{kind: 'n', d: d}
			for p := 0; p < decl.params; p++ {
				x.args = append(x.args, e1cType{kind: 'i'})
			}
			name := x.text()
			lines = append(lines, fmt.Sprintf("func Use%d(x %s) %s { x }", d, name, name))
			if decl.form == 'r' {
				var fs []string
				for i := range decl.fields {
					fs = append(fs, fmt.Sprintf("F%d: x.F%d", i, i))
				}
				lines = append(lines, fmt.Sprintf("func Literal%d(x %s) %s { %s{%s} }", d, name, name, name, strings.Join(fs, ", ")))
				lines = append(lines, fmt.Sprintf("func Call%d(x %s) %s { x.Use%d() }", d, name, name, d))
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func e1cMessage(names []string, value bool) string {
	slices.Sort(names)
	subject := names[0]
	with := ""
	if len(names) > 1 {
		with = " with " + strings.Join(names[1:], ", ")
	}
	if value {
		return subject + " contains itself by value" + with + ", through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"
	}
	return subject + " is defined over itself" + with + " (types.md, Defined types)"
}

func e1cObserve(t *testing.T, src string) []string {
	t.Helper()
	_, ds := check.CheckSource(src)
	var out []string
	for _, d := range ds {
		if d.Code != "S108" || !(strings.Contains(d.Message, "contains itself by value") || strings.Contains(d.Message, "is defined over itself")) {
			t.Fatalf("P6 non-cycle diagnostic: %v\n%s", ds, src)
		}
		name := strings.Fields(d.Message)[0]
		if d.Offset != strings.Index(src, "type "+name)+5 || d.Length != len(name) {
			t.Fatalf("cycle diagnostic name span: %+v\n%s", d, src)
		}
		out = append(out, d.Message)
	}
	slices.Sort(out)
	return out
}

func e1cMembers(messages []string) []string {
	var out []string
	for _, msg := range messages {
		out = append(out, strings.Fields(msg)[0])
		if _, with, ok := strings.Cut(msg, " with "); ok {
			with, _, _ = strings.Cut(with, ", through")
			with, _, _ = strings.Cut(with, " (types.md")
			out = append(out, strings.Split(with, ", ")...)
		}
	}
	return out
}

func e1cProperties(t *testing.T, ds []e1cDecl, groups []e1cGroup) (calls int) {
	t.Helper()
	order := make([]int, len(ds))
	for i := range order {
		order[i] = i
	}
	var want []string
	for _, g := range groups {
		var names []string
		for _, d := range g.members {
			names = append(names, fmt.Sprintf("D%d", d))
		}
		want = append(want, e1cMessage(names, g.value))
	}
	slices.Sort(want)
	src := e1cSource(ds, order, -1, false)
	got := e1cObserve(t, src)
	calls++
	for _, name := range e1cMembers(got) {
		if !slices.Contains(e1cMembers(want), name) {
			t.Fatalf("P2 unsound blame %s; oracle %v, checker %v\n%s", name, want, got, src)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("P3/P4 missing or duplicate groups; oracle %v, checker %v\n%s", want, got, src)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("P4/P7 group members or edge kind; oracle %v, checker %v\n%s", want, got, src)
	}
	var permute func(int)
	permute = func(i int) {
		if i == len(order) {
			out := e1cObserve(t, e1cSource(ds, order, -1, false))
			calls++
			if !slices.Equal(got, out) {
				t.Fatalf("P1 order %v: %v != %v\n%s", order, got, out, src)
			}
			return
		}
		for j := i; j < len(order); j++ {
			order[i], order[j] = order[j], order[i]
			permute(i + 1)
			order[i], order[j] = order[j], order[i]
		}
	}
	permute(0)
	for _, g := range groups {
		for _, d := range g.members {
			out := e1cObserve(t, e1cSource(ds, order, d, false))
			calls++
			for _, name := range e1cMembers(out) {
				if !slices.Contains(e1cMembers(got), name) {
					t.Fatalf("P5 repairing D%d reveals %s; before %v, after %v\n%s", d, name, got, out, src)
				}
			}
		}
	}
	if out := e1cObserve(t, e1cSource(ds, order, -1, true)); !slices.Equal(out, got) {
		t.Fatalf("P6 uses add a diagnostic: %v != %v\n%s", got, out, src)
	}
	return calls + 1
}

// Fixed alphabet: int, each available parameter, every declaration with
// identity-position arguments, repeated T/U, reversed T/U, concrete int,
// and repeated non-generic D_k arguments. Each atom also has Option and
// slice forms. A declaration has one/two alphabet fields (unordered pairs
// with repetition), a named/basic defined base, or a defined (atom,int)
// tuple. Thus both containment kinds, both authorships, all arities and
// breakers occur in the exhaustive one/two-declaration Cartesian product.
func e1cAlphabet(arities []int, d int) []e1cType {
	atoms := []e1cType{{kind: 'i'}}
	for p := 0; p < arities[d]; p++ {
		atoms = append(atoms, e1cType{kind: 'p', p: p})
	}
	for j, arity := range arities {
		if arity == 0 {
			atoms = append(atoms, e1cType{kind: 'n', d: j})
			continue
		}
		var patterns [][]e1cType
		for _, a := range []e1cType{{kind: 'i'}, {kind: 'p', p: 0}, {kind: 'p', p: 1}} {
			if a.kind == 'p' && a.p >= arities[d] {
				continue
			}
			patterns = append(patterns, slices.Repeat([]e1cType{a}, arity))
		}
		if arities[d] == 2 && arity == 2 {
			patterns = append(patterns, []e1cType{{kind: 'p'}, {kind: 'p', p: 1}}, []e1cType{{kind: 'p', p: 1}, {kind: 'p'}})
		}
		for k, kp := range arities {
			if kp == 0 {
				patterns = append(patterns, slices.Repeat([]e1cType{{kind: 'n', d: k}}, arity))
			}
		}
		for _, args := range patterns {
			atoms = append(atoms, e1cType{kind: 'n', d: j, args: args})
		}
	}
	return atoms
}

func e1cChoices(arities []int, d int) []e1cDecl {
	atoms := e1cAlphabet(arities, d)
	fields := slices.Clone(atoms)
	for _, a := range atoms {
		for _, k := range []byte{'o', 's'} {
			fields = append(fields, e1cType{kind: k, args: []e1cType{a}})
		}
	}
	var out []e1cDecl
	for i, a := range fields {
		out = append(out, e1cDecl{arities[d], 'r', []e1cType{a}})
		for _, b := range fields[i:] {
			out = append(out, e1cDecl{arities[d], 'r', []e1cType{a, b}})
		}
	}
	for _, a := range atoms {
		out = append(out, e1cDecl{arities[d], 'q', []e1cType{{kind: 'q', args: []e1cType{a}}}})
		if a.kind != 'p' { // a defined type over a parameter is a separate error
			out = append(out, e1cDecl{arities[d], 'd', []e1cType{a}})
		}
	}
	return out
}

func TestE1cCycleGenerator(t *testing.T) {
	// The generator lands before the checker change. E1C_RED records its
	// failing baseline; the next commit removes this temporary opt-in.
	if os.Getenv("E1C_RED") == "" {
		t.Skip("R2 checker comparison enabled in the next commit")
	}
	start := time.Now()
	programs, filtered, calls := 0, 0, 0
	run := func(ds []e1cDecl) {
		groups, finite := e1cOracle(ds)
		if !finite {
			filtered++
			return
		}
		programs++
		calls += e1cProperties(t, ds, groups)
	}
	for n := 1; n <= 2; n++ {
		arities := make([]int, n)
		var eachArity func(int)
		eachArity = func(d int) {
			if d < n {
				for arities[d] = 0; arities[d] <= 2; arities[d]++ {
					eachArity(d + 1)
				}
				return
			}
			choices := make([][]e1cDecl, n)
			for i := range choices {
				choices[i] = e1cChoices(arities, i)
			}
			ds := make([]e1cDecl, n)
			var eachDecl func(int)
			eachDecl = func(i int) {
				if i == n {
					run(ds)
					return
				}
				for _, decl := range choices[i] {
					ds[i] = decl
					eachDecl(i + 1)
				}
			}
			t.Logf("exhaustive n=%d arities=%v choices=%v", n, arities, func() []int {
				var ns []int
				for _, c := range choices {
					ns = append(ns, len(c))
				}
				return ns
			}())
			eachDecl(0)
		}
		eachArity(0)
	}
	rng := rand.New(rand.NewSource(196_2))
	sample := 20_000
	if testing.Short() {
		sample = 2_000
	}
	for n := 3; n <= 4; n++ {
		for i := 0; i < sample; i++ {
			arities := make([]int, n)
			for d := range arities {
				arities[d] = rng.Intn(3)
			}
			ds := make([]e1cDecl, n)
			for d := range ds {
				choices := e1cChoices(arities, d)
				ds[d] = choices[rng.Intn(len(choices))]
			}
			run(ds)
		}
	}
	t.Logf("P1-P7: programs=%d expanding-filtered=%d CheckSource-calls=%d elapsed=%s sample-per-size=%d", programs, filtered, calls, time.Since(start), sample)
}

func TestE1cCycleOracle(t *testing.T) {
	self := e1cType{kind: 'n'}
	for _, tc := range []struct {
		name string
		ds   []e1cDecl
		want string
	}{
		{"record-self", []e1cDecl{{form: 'r', fields: []e1cType{self}}}, "D0 contains itself by value, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"},
		{"defined-self", []e1cDecl{{form: 'd', fields: []e1cType{self}}}, "D0 is defined over itself (types.md, Defined types)"},
		{"tuple-self", []e1cDecl{{form: 'q', fields: []e1cType{{kind: 'q', args: []e1cType{self}}}}}, "D0 contains itself by value, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"},
		{"slice-break", []e1cDecl{{form: 'r', fields: []e1cType{{kind: 's', args: []e1cType{self}}}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, finite := e1cOracle(tc.ds)
			if !finite || (tc.want == "" && len(groups) != 0) || (tc.want != "" && (len(groups) != 1 || e1cMessage([]string{"D0"}, groups[0].value) != tc.want)) {
				t.Fatalf("independent R2 calibration: %v, finite=%v; want %q", groups, finite, tc.want)
			}
		})
	}
}
