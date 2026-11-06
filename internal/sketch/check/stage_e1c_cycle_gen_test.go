package check_test

import (
	"fmt"
	"iter"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// field is the full path from a declaration's field (or defined base)
// through its tuple elements to the position that generates the edge.
type e1cEdge struct {
	to              int
	value, argument bool
	field           string
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
	groups, finite, _ = e1cOracleCase(ds)
	return
}

// kind records which R2' branches contributed distinct diagnostic groups:
// 0=finite, 1=intrinsic, 2=orphan, 3=mixed.
func e1cOracleCase(ds []e1cDecl) (groups []e1cGroup, finite bool, kind int) {
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
		// In the one-level alphabet, at most nine argument atoms (identity
		// parameters, int and non-generic declarations) can reach each of
		// four declarations with at most two arguments: 4*9*9 instances.
		// Larger terms cover expanding instantiation outside that alphabet.
		if len(key) > 256 || len(nodes) >= 4*9*9 {
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
		var hold func(e1cType, e1cType, bool, string)
		hold = func(written, typ e1cType, value bool, field string) {
			switch typ.kind {
			case 'n':
				to := intern(typ)
				edges[n] = append(edges[n], e1cEdge{to, value, written.kind == 'p', field})
			case 'q':
				child := written
				if written.kind == 'q' {
					child = written.args[0]
				}
				hold(child, typ.args[0], true, field+".0")
			}
		}
		for i, f := range decl.fields {
			typ := e1cSub(f, x.args)
			discover(typ) // breakers still instantiate their element types
			// Carry the written type to the leaf, including through tuples
			// supplied as arguments. Only a literal written leaf is authored
			// by this declaration; a substituted parameter is an argument.
			hold(f, typ, decl.form != 'd', strconv.Itoa(i))
		}
	}
	if expanding {
		return nil, false, 0
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
	// R2' step 3: a field of an intrinsic declaration is intrinsic when its
	// edge at the identity instance stays in the declaration's home. Its
	// edges go at every instantiation; every other edge stays.
	type fieldPath struct {
		decl int
		path string
	}
	intrinsicField := map[fieldPath]bool{}
	for d, c := range home {
		if c < 0 {
			continue
		}
		for _, e := range edges[identity[d]] {
			if slices.Contains(cycles[c], e.to) {
				intrinsicField[fieldPath{d, e.field}] = true
			}
		}
	}
	pruned := make([][]e1cEdge, len(edges))
	for n, next := range edges {
		for _, e := range next {
			if !intrinsicField[fieldPath{nodes[n].d, e.field}] {
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
		// overlaps. An intrinsic declaration may author an orphan too.
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
	// An orphan group with the same members and kind as an intrinsic group
	// (D0[T]{F0 D0[T], F1 D0[int]}) is the same diagnostic: one report.
	if len(groups) > 0 {
		kind = 1
	}
	for _, o := range orphans {
		duplicate := false
		for _, g := range groups {
			duplicate = duplicate || g.value == o.value && e1cSameMembers(g.members, o.members)
		}
		if !duplicate {
			groups = append(groups, o)
			kind |= 2
		}
	}
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
	return groups, true, kind
}

func e1cSameMembers(a, b []int) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
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
			methodParams, receiver := "", name
			if decl.params > 0 {
				methodParams = "[" + strings.Join([]string{"T", "U"}[:decl.params], ", ") + "]"
				receiver = fmt.Sprintf("D%d%s", d, methodParams)
			}
			lines = append(lines, fmt.Sprintf("func Use%d%s(x %s) %s { x }", d, methodParams, receiver, receiver))
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
			if slices.IsSorted(order) {
				return // source order was checked above
			}
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

// Fixed alphabet: int, each available parameter, and every declaration with
// independently chosen arguments from int, available parameters and
// non-generic D_k (one level deep). Each atom also has Option and
// slice forms. A declaration has one/two alphabet fields (unordered pairs
// with repetition), a named/basic defined base, or a defined (atom,int)
// tuple. Thus both containment kinds, both authorships, all arities and
// breakers occur in the exhaustive one/two-declaration Cartesian product.
func e1cAlphabet(arities []int, d int) []e1cType {
	atoms := []e1cType{{kind: 'i'}}
	for p := 0; p < arities[d]; p++ {
		atoms = append(atoms, e1cType{kind: 'p', p: p})
	}
	arguments := slices.Clone(atoms)
	for k, arity := range arities {
		if arity == 0 {
			arguments = append(arguments, e1cType{kind: 'n', d: k})
		}
	}
	for j, arity := range arities {
		args := make([]e1cType, arity)
		var eachArgument func(int)
		eachArgument = func(i int) {
			if i == arity {
				atoms = append(atoms, e1cType{kind: 'n', d: j, args: slices.Clone(args)})
				return
			}
			for _, a := range arguments {
				args[i] = a
				eachArgument(i + 1)
			}
		}
		eachArgument(0)
	}
	return atoms
}

// Check the declared domain independently of the alphabet builder. In
// particular, each argument position may choose a different valid atom.
func TestE1cCycleAlphabetComplete(t *testing.T) {
	for n := 1; n <= 2; n++ {
		combinations := 3
		if n == 2 {
			combinations = 9
		}
		for combo := 0; combo < combinations; combo++ {
			arities := make([]int, n)
			for d, rest := 0, combo; d < n; d, rest = d+1, rest/3 {
				arities[d] = rest % 3
			}
			for d, params := range arities {
				t.Run(fmt.Sprintf("%v/D%d", arities, d), func(t *testing.T) {
					want := e1cRequiredAtoms(arities, params)
					got := map[string]bool{}
					for _, atom := range e1cAlphabet(arities, d) {
						name := atom.text()
						if got[name] || !want[name] {
							t.Errorf("duplicate or invalid atom %s", name)
						}
						got[name] = true
					}
					for name := range want {
						if !got[name] {
							t.Errorf("section 3.2 requires %s; absent from alphabet", name)
						}
					}
				})
			}
		}
	}
}

// Sampled tables retain their alphabet, not every possible pair of fields.
// The index order is the same as the materialized exhaustive choices.
type e1cChoiceSet struct {
	params int
	fields []e1cType
	bases  []e1cDecl
}

func e1cChoiceSetOf(arities []int, d int) e1cChoiceSet {
	atoms := e1cAlphabet(arities, d)
	fields := slices.Clone(atoms)
	for _, a := range atoms {
		for _, k := range []byte{'o', 's'} {
			fields = append(fields, e1cType{kind: k, args: []e1cType{a}})
		}
	}
	set := e1cChoiceSet{params: arities[d], fields: fields}
	// Plain atoms suffice for defined/tuple bases: Option and slice bases
	// break containment, and their element instances are reached by fields.
	for _, a := range atoms {
		set.bases = append(set.bases, e1cDecl{arities[d], 'q', []e1cType{{kind: 'q', args: []e1cType{a}}}})
		if a.kind != 'p' { // a defined type over a parameter is a separate error
			set.bases = append(set.bases, e1cDecl{arities[d], 'd', []e1cType{a}})
		}
	}
	return set
}

func (set e1cChoiceSet) count() int {
	n := len(set.fields)
	return n*(n+3)/2 + len(set.bases)
}

func (set e1cChoiceSet) at(index int) e1cDecl {
	n := len(set.fields)
	records := n * (n + 3) / 2
	if index >= records {
		return set.bases[index-records]
	}
	for i, a := range set.fields {
		if index == 0 {
			return e1cDecl{set.params, 'r', []e1cType{a}}
		}
		index--
		if index < n-i {
			return e1cDecl{set.params, 'r', []e1cType{a, set.fields[i+index]}}
		}
		index -= n - i
	}
	panic("invalid cycle declaration choice")
}

func e1cChoices(arities []int, d int) []e1cDecl {
	set := e1cChoiceSetOf(arities, d)
	out := make([]e1cDecl, set.count())
	for i := range out {
		out[i] = set.at(i)
	}
	return out
}

// e1cFull is the acceptance batch: the whole two-declaration product and
// the full three- and four-declaration sample, instead of the gate slice.
func e1cFull() bool { return os.Getenv("ERGO_CYCLE_GEN") == "full" && !testing.Short() }

// e1cCase is one program of the generator's domain, with its ordinal. Its
// declarations are valid only while it is yielded.
type e1cCase struct {
	ordinal int
	ds      []e1cDecl
}

// e1cDomain yields the section 3.2 domain in ordinal order: the gate slice,
// or the acceptance batch when full.
func e1cDomain(full bool) iter.Seq[e1cCase] {
	return func(yield func(e1cCase) bool) {
		e1cEachProgram(full, func(ordinal int, ds []e1cDecl) bool {
			return yield(e1cCase{ordinal, ds})
		})
	}
}

// The domain is a split (gate-fast T1): part k checks every fourth program
// from program k, as a top-level test of its own, dealt to four parallel
// subtests. Under -short a part keeps every eighth program of its share.
// TestE1cCycleGeneratorDomain checks the domain's size, finiteness and
// slice coverage from the oracle alone. The pin is the gate slice's; the
// acceptance batch (ERGO_CYCLE_GEN=full) runs the same parts over its own
// domain.
var e1cCycleGenerator = check.NewSplit(check.Split[e1cCase]{
	Name: "TestE1cCycleGenerator", Parts: 4, Short: 8,
	Count: 14358, Digest: "d08b4ade23f478f9eb73978e3b8e02c3b0578d623b2bbf598fdd15d7c93469ca",
	Pinned: func() bool { return !e1cFull() },
	Cases:  func(yield func(e1cCase) bool) { e1cDomain(e1cFull())(yield) },
	Key: func(c e1cCase) string {
		order := make([]int, len(c.ds))
		for i := range order {
			order[i] = i
		}
		return fmt.Sprintf("ordinal=%d\n%s", c.ordinal, e1cSource(c.ds, order, -1, false))
	},
	Run: runE1cCycleGenerator,
})

func TestE1cCycleGeneratorPart0(t *testing.T) { e1cCycleGenerator.Part(t, 0) }
func TestE1cCycleGeneratorPart1(t *testing.T) { e1cCycleGenerator.Part(t, 1) }
func TestE1cCycleGeneratorPart2(t *testing.T) { e1cCycleGenerator.Part(t, 2) }
func TestE1cCycleGeneratorPart3(t *testing.T) { e1cCycleGenerator.Part(t, 3) }

// e1cWorkers is the number of parallel subtests a part deals its programs to.
const e1cWorkers = 4

func runE1cCycleGenerator(t *testing.T, cases iter.Seq[e1cCase]) {
	start := time.Now()
	if e1cFull() {
		// Only the acceptance batch changes GC pacing; the gate slice uses
		// the caller's policy. Restore it before any other test runs.
		runtime.GC()
		previousGC := debug.SetGCPercent(1000)
		defer debug.SetGCPercent(previousGC)
	}
	var programs, calls [e1cWorkers]int
	t.Run("domain", func(t *testing.T) {
		for worker := range e1cWorkers {
			t.Run(fmt.Sprint(worker), func(t *testing.T) {
				t.Parallel()
				position := 0
				for c := range cases {
					position++
					if (position-1)%e1cWorkers != worker {
						continue
					}
					groups, finite, _ := e1cOracleCase(c.ds)
					if !finite {
						t.Fatalf("one-level argument alphabet must be finite: program %d expands", c.ordinal)
					}
					programs[worker]++
					calls[worker] += e1cProperties(t, c.ds, groups)
				}
			})
		}
	})
	var total [2]int
	for w := range e1cWorkers {
		total[0] += programs[w]
		total[1] += calls[w]
	}
	t.Logf("P1-P7: full=%v programs=%d CheckSource-calls=%d elapsed=%s", e1cFull(), total[0], total[1], time.Since(start))
}

// TestE1cCycleGeneratorDomain: the generator's domain has the size section
// 3.2 counts, every program of it is finite, and the gate slice keeps every
// atom position and every R2' case kind; then the hand controls.
func TestE1cCycleGeneratorDomain(t *testing.T) {
	full := e1cFull()
	var coverage e1cSliceCoverage
	coverage.positions = map[string]bool{}
	domain, filtered := 0, 0
	e1cEachProgram(full, func(ordinal int, ds []e1cDecl) bool {
		domain++
		_, finite, kind := e1cOracleCase(ds)
		if !finite {
			filtered++
			return true
		}
		if !full && len(ds) == 2 {
			coverage.observe(ds, kind)
		}
		return true
	})
	if !full {
		e1cAssertSliceCoverage(t, []e1cSliceCoverage{coverage})
	}
	if wantDomain := e1cExpectedDomain(full); domain != wantDomain {
		t.Fatalf("incomplete section 3.2 domain: %d programs, want %d", domain, wantDomain)
	}
	if filtered != 0 {
		t.Fatalf("one-level argument alphabet must be finite: filtered %d programs", filtered)
	}
	t.Run("alphabet", TestE1cCycleAlphabetComplete)
	t.Run("oracle-controls", TestE1cCycleOracle)
	t.Run("rule-controls", TestE1cCycleRuleExamples)
	t.Run("repair-controls", TestE1cCycleMonotoneRepair)
	t.Run("permutation-controls", TestE1CycleDeclarationPermutations)
	t.Logf("full=%v domain=%d expanding-filtered=%d (plus hand controls)", full, domain, filtered)
}

type e1cChoiceKey struct {
	arities [4]int
	n, d    int
}

// e1cTables are the 426 read-only alphabets, built once and shared by
// every enumeration: pairs are materialized only for the exhaustive
// one/two-declaration domain. Sampling needs only indices; retaining every
// three/four-declaration pair would burden each GC.
var e1cTables = sync.OnceValues(func() (map[e1cChoiceKey][]e1cDecl, map[e1cChoiceKey]e1cChoiceSet) {
	tables := map[e1cChoiceKey][]e1cDecl{}
	sampledTables := map[e1cChoiceKey]e1cChoiceSet{}
	for n := 1; n <= 4; n++ {
		arities := make([]int, n)
		for combo := 0; combo < [5]int{1, 3, 9, 27, 81}[n]; combo++ {
			for d, rest := 0, combo; d < n; d, rest = d+1, rest/3 {
				arities[d] = rest % 3
			}
			for d := range n {
				k := e1cChoiceKey{n: n, d: d}
				copy(k.arities[:], arities)
				if n <= 2 {
					tables[k] = e1cChoices(arities, d)
				} else {
					sampledTables[k] = e1cChoiceSetOf(arities, d)
				}
			}
		}
	}
	return tables, sampledTables
})

// e1cEachProgram calls f on every program of the domain in ordinal order
// (from 0), until f returns false. The declarations are reused between
// calls.
func e1cEachProgram(full bool, f func(ordinal int, ds []e1cDecl) bool) {
	tables, sampledTables := e1cTables()
	choicesOf := func(arities []int, d int) []e1cDecl {
		k := e1cChoiceKey{n: len(arities), d: d}
		copy(k.arities[:], arities)
		return tables[k]
	}
	sampleOf := func(arities []int, d int, rng *rand.Rand) e1cDecl {
		k := e1cChoiceKey{n: len(arities), d: d}
		copy(k.arities[:], arities)
		set := sampledTables[k]
		return set.at(rng.Intn(set.count()))
	}
	ordinal, stopped := 0, false
	run := func(ds []e1cDecl) {
		if stopped {
			return
		}
		ordinal++
		stopped = !f(ordinal-1, ds)
	}
	sliceRNG := rand.New(rand.NewSource(196_3))
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
				choices[i] = choicesOf(arities, i)
			}
			ds := make([]e1cDecl, n)
			if !full && n == 2 {
				// Stratify by arities, declaration position and every
				// declaration choice. Pair each with a seeded partner.
				// This covers all atoms and argument tuples at both
				// field positions without enumerating the product.
				for focal := range n {
					for _, decl := range choices[focal] {
						ds[focal] = decl
						other := 1 - focal
						ds[other] = choices[other][sliceRNG.Intn(len(choices[other]))]
						run(ds)
					}
				}
				return
			}
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
			eachDecl(0)
		}
		eachArity(0)
	}
	rng := rand.New(rand.NewSource(196_2))
	for n := 3; n <= 4; n++ {
		for i := 0; i < e1cFullSample; i++ {
			arities := make([]int, n)
			for d := range arities {
				arities[d] = rng.Intn(3)
			}
			ds := make([]e1cDecl, n)
			for d := range ds {
				ds[d] = sampleOf(arities, d, rng)
			}
			// Still draw the remaining programs so the four-
			// declaration prefix is identical to the full run.
			if full || i < e1cSliceSample {
				run(ds)
			}
		}
	}
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

func TestE1cCycleRuleExamples(t *testing.T) {
	p := e1cType{kind: 'p'}
	u := e1cType{kind: 'p', p: 1}
	integer := e1cType{kind: 'i'}
	n := func(d int, args ...e1cType) e1cType { return e1cType{kind: 'n', d: d, args: args} }
	o := func(x e1cType) e1cType { return e1cType{kind: 'o', args: []e1cType{x}} }
	s := func(x e1cType) e1cType { return e1cType{kind: 's', args: []e1cType{x}} }
	q := func(x e1cType) e1cType { return e1cType{kind: 'q', args: []e1cType{x}} }
	r := func(params int, fs ...e1cType) e1cDecl { return e1cDecl{params, 'r', fs} }
	for _, tc := range []struct {
		name    string
		ds      []e1cDecl
		members []string
	}{
		{"q30", []e1cDecl{r(0, n(0)), r(1, p, n(0)), r(1, n(1, n(2, p)))}, []string{"D0", "D2"}},
		{"q31", []e1cDecl{r(1, p, n(0, p)), r(1, n(0, n(1, p)))}, []string{"D0", "D1"}},
		{"zrec-int-cascade", []e1cDecl{r(1, p, n(1, integer)), r(1, n(0, n(1, p)))}, []string{"D1"}},
		{"mutual-generic", []e1cDecl{r(1, n(1, p)), r(1, n(0, p))}, []string{"D0,D1"}},
		{"concrete-self-instance", []e1cDecl{r(1, p), r(1, n(0, n(1, integer)))}, []string{"D1"}},
		{"concrete-self-direct", []e1cDecl{r(1, p, n(0, integer))}, []string{"D0"}},
		{"mixed-param-concrete", []e1cDecl{r(2, n(0, integer, p))}, []string{"D0"}},
		// The mixed argument sample can visit more than 96 finite instances.
		{"finite-mixed-sample", []e1cDecl{
			r(2, n(1, integer, p), n(3, u, p)),
			r(2, n(3, p, integer), o(n(2, u, p))),
			r(2, s(n(1, u, p)), s(n(3, p, u))),
			r(2, o(n(0, p, u)), s(n(1, u, u))),
		}, nil},
		{"nested-param-tuple", []e1cDecl{{params: 1, form: 'q', fields: []e1cType{q(q(p))}}, r(1, n(0, n(1, integer)))}, []string{"D1"}},
		{"tuple-self", []e1cDecl{{form: 'q', fields: []e1cType{{kind: 'q', args: []e1cType{n(0)}}}}}, []string{"D0"}},
		// The R2/P5 counterexample (E1c round 1): D2[int]'s closing edge is
		// not intrinsic, so the orphan {D1[int], D2[int]} is reported
		// beside the intrinsic group, and repairing D0 reveals nothing.
		{"r2-p5-three", []e1cDecl{r(1, n(2, p)), r(1, n(2, integer)), r(1, n(0, p), n(1, integer))}, []string{"D0,D2", "D1,D2"}},
		// The generator's original find (seed 196_2, four declarations).
		{"r2-p5-four", []e1cDecl{
			r(0, n(2, n(0)), o(integer)),
			r(2, n(3, p, u), s(n(3, p, u))),
			r(1, n(3, integer, integer), o(n(1, integer, integer))),
			r(2, n(1, p, u), n(2, integer)),
		}, []string{"D1,D3", "D2,D3"}},
		// One declaration in an intrinsic group and in an orphan group with
		// the same members and kind: one diagnostic.
		{"intrinsic-and-orphan-self", []e1cDecl{r(1, n(0, p), n(0, integer))}, []string{"D0"}},
		// The same declaration in both kinds of group, with different
		// members: two diagnostics.
		{"intrinsic-and-orphan-distinct", []e1cDecl{r(1, n(0, p), n(1, integer)), r(1, n(0, integer))}, []string{"D0", "D0,D1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, finite := e1cOracle(tc.ds)
			var want, got []string
			for _, members := range tc.members {
				want = append(want, e1cMessage(strings.Split(members, ","), true))
			}
			for _, g := range groups {
				var names []string
				for _, d := range g.members {
					names = append(names, fmt.Sprintf("D%d", d))
				}
				got = append(got, e1cMessage(names, g.value))
			}
			slices.Sort(want)
			slices.Sort(got)
			if !finite || !slices.Equal(want, got) {
				t.Fatalf("R2 case table: want %v, oracle %v, finite=%v", want, got, finite)
			}
			e1cProperties(t, tc.ds, groups)
		})
	}
}

// P5 by hand, with the exact outputs on both sides of each repair: q31 (Zz
// was hidden behind Aa in E1 round 3) and the R2/P5 counterexample, whose
// orphan {D1, D2} R2 hid behind the intrinsic group {D0, D2}.
func TestE1cCycleMonotoneRepair(t *testing.T) {
	value := func(names ...string) string { return e1cMessage(names, true) }
	for _, tc := range []struct {
		name, before, from, to string
		want, after            []string
	}{
		{"q31", "type Aa[T] { V T, N Aa[T] }\ntype Zz[T] { V Aa[Zz[T]] }\n", "N Aa[T]", "N Option[Aa[T]]",
			[]string{value("Aa"), value("Zz")}, []string{value("Zz")}},
		{"r2-p5-three", "type D0[T] { F0 D2[T] }\ntype D1[T] { F0 D2[int] }\ntype D2[T] { F0 D0[T], F1 D1[int] }\n", "F0 D2[T]", "F0 Option[D2[T]]",
			[]string{value("D0", "D2"), value("D1", "D2")}, []string{value("D1", "D2")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e1cObserve(t, tc.before)
			after := strings.Replace(tc.before, tc.from, tc.to, 1)
			out := e1cObserve(t, after)
			if !slices.Equal(got, tc.want) || !slices.Equal(out, tc.after) {
				t.Fatalf("before %v, want %v; after repair %v, want %v\n%s", got, tc.want, out, tc.after, tc.before)
			}
			for _, name := range e1cMembers(out) {
				if !slices.Contains(e1cMembers(got), name) {
					t.Fatalf("P5 repair reveals %s; before %v\n%s", name, got, tc.before)
				}
			}
		})
	}
}
