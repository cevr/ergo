package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Match trees (M1). A pattern match lowers to a decision tree over its
// clause matrix: one row per pattern alternative, in source order, and one
// column per value that a row can test. Each node tests one column with
// one Go switch: a type switch for a column of an interface type (a union,
// an operation type, a row value), an expression switch for a constant
// column. A row whose cell does not test the column goes into every
// branch and into the default branch, so first-arm-wins holds on every
// path. After a type switch the narrowed value and its field reads are the
// new columns, so the tree never repeats a test, never calls the ergoIs
// helper and never asserts a type again. A record or tuple cell on a value
// that it does not test only expands its fields as columns: a field read
// has no effect and cannot panic, so the changed test order is not
// observable.
//
// A catch with record patterns, `case err:` or `case _` (M1b) lowers
// through the same tree, inside its `if err != nil` frame: one column, the
// caught Go error of the row type, and one row per alternative of its arms
// (treeArm). A default branch that no row reaches passes the same error
// value on: it leaves the frame under try and re-panics in a total catch
// (rows.md, Catch 5 and 6), as the flat catch does (treeExit).
//
// The column to test is one the first row tests (Maranget's necessary
// columns): the one that the most rows test, then the one with the fewest
// distinct constructors, then the leftmost. This is the checker's
// heuristic (rows.md, Totality 5) and Jacobs' (2021, §4). The choice
// changes only the size of the tree, never the arm that runs.
//
// Each arm body is emitted once. An arm that one leaf reaches is written
// at that leaf. An arm that several leaves reach (an or-pattern, or a row
// that the tree copied into several branches) is written after the tree,
// in a Go block, as `armK: { body }`; its leaves assign its bindings to
// variables declared before the tree and jump to it with goto. This is the
// static exception of Le Fessant and Maranget (2001) and a shared basic
// block in Rust's MIR. A closure per arm is not used: a body can return
// from the function and break or continue a loop.
//
// The tree can copy a row into many branches. The builder counts nodes;
// past max(64, 4 × the match's pattern nodes) it gives up, and the match
// uses the flat lowering (flatPatternMatch), so the emitted size stays
// linear in the source. The flat lowering also takes the rare shapes the
// tree does not model: a type test of an interface type, a constant on an
// interface value, and an arm that no leaf reaches.

// mcol is a column of the clause matrix: a Go value and its checked type.
type mcol struct {
	val string
	t   check.Type
}

// mrow is a row: an alternative of the arm arm, its remaining cells, and
// the bindings it made in the columns already consumed.
type mrow struct {
	arm   int
	cells []*check.Pattern
	binds []patBind
}

// headKind is what a cell does to its column.
type headKind int

const (
	headWild  headKind = iota // no test
	headCtor                  // a type test of a variant: key is its Go type
	headConst                 // a constant: key is its caseKey
	headSplit                 // a record or tuple whose value needs no test
)

// mhead is a cell with its binds and as patterns peeled off.
type mhead struct {
	kind headKind
	key  string
	p    *check.Pattern
	// vars bind the column's value; as bind the value narrowed to the
	// cell's constructor (only a constructor has them).
	vars, as []*check.Var
}

// mnode is a node of the tree: a leaf, a failure (no row matches), or a
// switch on one column.
type mnode struct {
	leaf *mleaf
	fail bool

	typeSwitch bool
	operand    string     // the column's value
	t          check.Type // the column's type
	bound      string     // a type switch's narrowed variable
	cases      []mcase
	def        *mnode
}

// mcase is one case of a switch node: a Go type or a constant.
type mcase struct {
	label string
	child *mnode
}

// mleaf runs the arm arm with the bindings binds.
type mleaf struct {
	arm   int
	binds []patBind
}

// treeArm is an arm of a match or of a catch (M1b) as the tree sees it:
// its patterns, one per alternative (none for a clause that matches every
// value), and the writer of its body, which reports whether the body's Go
// form terminates. A match arm is a syntax.CaseClause and a catch arm a
// syntax.CatchArm; each caller makes its arms (matchArms, patternCatch).
type treeArm struct {
	alts []*check.Pattern
	body func() bool
}

// treeExit is what a default branch that no row reaches does: needed
// reports, from whether every arm body terminates, whether the branch is
// written; write writes its statements after its `default:` line. A match
// panics with noCase where Go needs a default; a catch always passes the
// error on (rows.md, Catch 5 and 6).
type treeExit struct {
	needed func(allTerm bool) bool
	write  func()
}

// matchTreeTest is the test hook of lowerer.matchTest: flat forces the
// flat lowering; splits counts, per headKind, the columns that the trees of
// the program split on (a switch for headCtor and headConst, a field
// expansion for headSplit), with tuple and record expansions apart;
// fallbacks counts the matches that took the flat lowering after a tree
// build began, by cause: "bail" (treeBail) or "unreached" (an arm no leaf
// reaches).
type matchTreeTest struct {
	flat      bool
	splits    map[string]int
	fallbacks map[string]int
}

// matchLowering, when "flat", makes every match of the build take the
// flat lowering, the reference of the tree. Only a build for a test sets
// it, with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.matchLowering=flat
// (a go test of this package, or a bin/ergo), so the examples' tests and
// the conformance corpus run under both lowerings.
var matchLowering string

// fallback records a fallback of the cause why in the test hook.
func (fc *funcCtx) fallback(why string) {
	if t := fc.l.matchTest; t != nil {
		if t.fallbacks == nil {
			t.fallbacks = map[string]int{}
		}
		t.fallbacks[why]++
	}
}

// count records a split of the kind k; matchTree hands the counts of a
// tree it keeps to the test hook.
func (b *treeBuilder) count(k string) {
	if b.splits == nil {
		b.splits = map[string]int{}
	}
	b.splits[k]++
}

// treeBail stops a tree build; the match takes the flat lowering.
type treeBail struct{}

// treeBuilder builds one match's tree.
type treeBuilder struct {
	fc     *funcCtx
	site   syntax.Node
	nodes  int
	limit  int
	wild   *check.Pattern
	consts map[string]string // caseKey to the Go text of its first constant
	splits map[string]int    // the splits of this tree, by kind (count)
}

func (b *treeBuilder) bail() { panic(treeBail{}) }

// matchTree builds the tree of a match (or a catch, on its one error
// value) on the subject values vals of the checked types types, or returns
// nil when it takes the flat lowering.
func (fc *funcCtx) matchTree(s syntax.Node, vals []string, types []check.Type, arms []treeArm) (tree *mnode) {
	if t := fc.l.matchTest; matchLowering == "flat" || t != nil && t.flat {
		return nil
	}
	b := &treeBuilder{fc: fc, site: s, wild: &check.Pattern{Kind: check.PatWildcard}, consts: map[string]string{}}
	cols := make([]mcol, len(vals))
	for i := range vals {
		cols[i] = mcol{vals[i], types[i]}
	}
	var rows []mrow
	size := 0
	for arm, a := range arms {
		if len(a.alts) == 0 {
			rows = append(rows, mrow{arm: arm, cells: b.wilds(len(cols))})
			size++
			continue
		}
		for _, p := range a.alts {
			size += patternSize(p)
			switch {
			case len(cols) == 1:
				rows = append(rows, mrow{arm: arm, cells: []*check.Pattern{p}})
			case p.Kind == check.PatTuple && len(p.Elems) == len(cols):
				rows = append(rows, mrow{arm: arm, cells: append([]*check.Pattern(nil), p.Elems...)})
			default:
				return nil // the flat lowering names the defect
			}
		}
	}
	b.limit = max(64, 4*size)
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(treeBail); !ok {
				panic(r)
			}
			fc.fallback("bail")
			tree = nil
		}
	}()
	tree = b.compile(cols, rows)
	reached := make([]bool, len(arms))
	tree.walk(func(n *mnode) {
		if n.leaf != nil {
			reached[n.leaf.arm] = true
		}
	})
	for _, r := range reached {
		if !r {
			fc.fallback("unreached")
			return nil
		}
	}
	if t := fc.l.matchTest; t != nil {
		if t.splits == nil {
			t.splits = map[string]int{}
		}
		for k, n := range b.splits {
			t.splits[k] += n
		}
	}
	return tree
}

// patternSize counts a pattern's nodes.
func patternSize(p *check.Pattern) int {
	n := 1
	for _, e := range p.Elems {
		n += patternSize(e)
	}
	for _, f := range p.Fields {
		n += patternSize(f.Pattern)
	}
	return n
}

// walk visits n and every node below it, cases before the default.
func (n *mnode) walk(f func(*mnode)) {
	f(n)
	for _, c := range n.cases {
		c.child.walk(f)
	}
	if n.def != nil {
		n.def.walk(f)
	}
}

func (b *treeBuilder) wilds(n int) []*check.Pattern {
	cells := make([]*check.Pattern, n)
	for i := range cells {
		cells[i] = b.wild
	}
	return cells
}

// head peels a cell's binds and as patterns and classifies what is left
// against its column c.
func (b *treeBuilder) head(p *check.Pattern, c mcol) mhead {
	fc := b.fc
	var h mhead
	for p.Kind == check.PatAs || p.Kind == check.PatBind {
		if p.Kind == check.PatBind {
			if p.Var != nil {
				h.vars = append(h.vars, p.Var)
			}
			p = b.wild
			break
		}
		if len(p.Elems) != 1 {
			b.bail()
		}
		if p.Var != nil {
			h.as = append(h.as, p.Var)
		}
		p = p.Elems[0]
	}
	h.p = p
	// A flat union's column splits by variant as an interface's does
	// (flatunion.go); the switch reads its tag (node).
	iface := fc.isSum(c.t)
	switch p.Kind {
	case check.PatWildcard:
		h.kind = headWild
	case check.PatTypeTest, check.PatRecord:
		switch {
		case !iface || fc.goType(p.Type) == fc.goType(c.t):
			h.kind = headWild
			if p.Kind == check.PatRecord {
				h.kind = headSplit
			}
		case fc.isSum(p.Type):
			b.bail() // a test of an interface type can overlap a variant
		default:
			h.kind, h.key = headCtor, fc.goType(p.Type)
		}
	case check.PatConst:
		if iface {
			b.bail()
		}
		h.kind, h.key = headConst, caseKey(p.Type, p.Value)
		if _, ok := b.consts[h.key]; !ok {
			b.consts[h.key] = fc.constant(p.Type, p.Value, b.site)
		}
	case check.PatTuple:
		h.kind = headSplit
	default:
		b.bail()
	}
	if h.kind != headCtor {
		for _, v := range h.as {
			if iface && fc.goType(v.Type) != fc.goType(c.t) && (!fc.isInterface(v.Type) || !fc.isInterface(c.t)) {
				b.bail()
			}
		}
		h.vars, h.as = append(h.vars, h.as...), nil
	}
	return h
}

// bindAll appends the bindings of vars to the value val.
func bindAll(binds []patBind, vars []*check.Var, val string) []patBind {
	out := append([]patBind(nil), binds...)
	for _, v := range vars {
		out = append(out, patBind{v, val})
	}
	return out
}

// splice is cells with the cell at i replaced by mid.
func splice[T any](cells []T, i int, mid []T) []T {
	out := make([]T, 0, len(cells)-1+len(mid))
	out = append(out, cells[:i]...)
	out = append(out, mid...)
	return append(out, cells[i+1:]...)
}

// compile builds the tree of the matrix cols × rows.
func (b *treeBuilder) compile(cols []mcol, rows []mrow) *mnode {
	b.nodes++
	if b.nodes > b.limit {
		b.bail()
	}
	if len(rows) == 0 {
		return &mnode{fail: true}
	}
	for i := 0; i < len(cols); i++ {
		for _, r := range rows {
			if b.head(r.cells[i], cols[i]).kind == headSplit {
				cols, rows = b.expand(cols, rows, i)
				i--
				break
			}
		}
	}
	first := rows[0]
	best, bestRows, bestCtors := -1, 0, 0
	for i, c := range cols {
		if b.head(first.cells[i], c).kind == headWild {
			continue
		}
		tested, keys := 0, map[string]bool{}
		for _, r := range rows {
			if h := b.head(r.cells[i], c); h.kind != headWild {
				tested++
				keys[h.key] = true
			}
		}
		if best < 0 || tested > bestRows || tested == bestRows && len(keys) < bestCtors {
			best, bestRows, bestCtors = i, tested, len(keys)
		}
	}
	if best < 0 {
		binds := first.binds
		for i, c := range cols {
			binds = bindAll(binds, b.head(first.cells[i], c).vars, c.val)
		}
		return &mnode{leaf: &mleaf{arm: first.arm, binds: binds}}
	}
	return b.specialize(cols, rows, best)
}

// expand replaces column i, whose cells destructure a record or a tuple
// without a test, with the columns of the fields some cell reads.
func (b *treeBuilder) expand(cols []mcol, rows []mrow, i int) ([]mcol, []mrow) {
	c := cols[i]
	var fields []mcol
	var names []string // record field names; nil for a tuple
	tuple := false
	for _, r := range rows {
		h := b.head(r.cells[i], c)
		if h.kind != headSplit {
			continue
		}
		if h.p.Kind == check.PatTuple {
			tuple = true
			continue
		}
		for _, f := range h.p.Fields {
			if !containsString(names, f.Name) {
				names = append(names, f.Name)
			}
		}
	}
	var recordType check.Type
	if tuple {
		b.count("split tuple")
		tup, ok := underlying(c.t).(*check.Tuple)
		if !ok || len(names) > 0 {
			ice(b.site, "a tuple pattern against %s", c.t)
		}
		for k, t := range tup.Types {
			fields = append(fields, mcol{c.val + "." + tupleField(k), t})
		}
	} else {
		for _, r := range rows {
			if h := b.head(r.cells[i], c); h.kind == headSplit {
				recordType = h.p.Type
				break
			}
		}
		fields, names = b.recordColumns(recordType, c.val, names)
		b.count("split record")
	}
	var out []mrow
	for _, r := range rows {
		h := b.head(r.cells[i], c)
		mid := b.wilds(len(fields))
		switch h.kind {
		case headSplit:
			if h.p.Kind == check.PatTuple {
				if len(h.p.Elems) != len(fields) {
					ice(b.site, "a tuple pattern of %d elements against %s", len(h.p.Elems), c.t)
				}
				copy(mid, h.p.Elems)
			} else {
				b.fieldCells(h.p, names, mid)
			}
		case headWild:
		default:
			b.bail()
		}
		out = append(out, mrow{arm: r.arm, cells: splice(r.cells, i, mid), binds: bindAll(r.binds, h.vars, c.val)})
	}
	return splice(cols, i, fields), out
}

// recordColumns orders the field names a record pattern of the type t
// reads by the record's declaration and returns their columns over val.
func (b *treeBuilder) recordColumns(t check.Type, val string, names []string) ([]mcol, []string) {
	named, ok := t.(*check.Named)
	if !ok {
		ice(b.site, "a record pattern of the type %s", t)
	}
	st, ok := named.Underlying().(*check.Struct)
	if !ok {
		ice(b.site, "a record pattern of the non-record %s", t)
	}
	td := b.fc.l.typeDecl(named.Obj, b.site)
	var cols []mcol
	var ordered []string
	for _, sf := range st.Fields {
		if !containsString(names, sf.Name) {
			continue
		}
		goName, ok := td.fieldName[sf.Name]
		if !ok {
			ice(b.site, "%s has no field %s", t, sf.Name)
		}
		cols = append(cols, mcol{val + "." + goName, sf.Type})
		ordered = append(ordered, sf.Name)
	}
	if len(ordered) != len(names) {
		ice(b.site, "a record pattern of %s names a field it does not have", t)
	}
	return cols, ordered
}

// fieldCells writes the record pattern p's field patterns into the cells
// of the columns names.
func (b *treeBuilder) fieldCells(p *check.Pattern, names []string, cells []*check.Pattern) {
	for _, f := range p.Fields {
		for k, n := range names {
			if n == f.Name {
				cells[k] = f.Pattern
			}
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// specialize switches on column i: one branch per constructor or constant
// the rows test, and a default branch of the rows that test nothing there.
func (b *treeBuilder) specialize(cols []mcol, rows []mrow, i int) *mnode {
	fc, c := b.fc, cols[i]
	var keys []string
	ctorType := map[string]check.Type{}
	ctorFields := map[string][]string{}
	kind := b.head(rows[0].cells[i], c).kind
	for _, r := range rows {
		h := b.head(r.cells[i], c)
		if h.kind == headWild {
			continue
		}
		if h.kind != kind {
			b.bail() // a column of constants and type tests
		}
		if _, ok := ctorType[h.key]; !ok {
			keys = append(keys, h.key)
			ctorType[h.key] = h.p.Type
		}
		if h.p.Kind == check.PatRecord {
			for _, f := range h.p.Fields {
				if !containsString(ctorFields[h.key], f.Name) {
					ctorFields[h.key] = append(ctorFields[h.key], f.Name)
				}
			}
		}
	}
	n := &mnode{operand: c.val, t: c.t, typeSwitch: kind == headCtor}
	if n.typeSwitch {
		n.bound = fc.temp("n")
		b.count("ctor")
	} else {
		b.count("const")
	}
	for _, key := range keys {
		var fields []mcol
		var names []string
		label := b.consts[key]
		if n.typeSwitch {
			label = key
			if len(ctorFields[key]) > 0 {
				fields, names = b.recordColumns(ctorType[key], n.bound, ctorFields[key])
			}
		}
		var sub []mrow
		for _, r := range rows {
			h := b.head(r.cells[i], c)
			mid := b.wilds(len(fields))
			binds := bindAll(r.binds, h.vars, c.val)
			switch {
			case h.kind == headWild:
			case h.key != key:
				continue
			default:
				if h.p.Kind == check.PatRecord {
					b.fieldCells(h.p, names, mid)
				}
				for _, v := range h.as {
					switch {
					case fc.goType(v.Type) == key:
						binds = append(binds, patBind{v, n.bound})
					case fc.isInterface(v.Type) && fc.isInterface(c.t) || fc.goType(v.Type) == fc.goType(c.t):
						binds = append(binds, patBind{v, c.val})
					default:
						b.bail()
					}
				}
			}
			sub = append(sub, mrow{arm: r.arm, cells: splice(r.cells, i, mid), binds: binds})
		}
		n.cases = append(n.cases, mcase{label: label, child: b.compile(splice(cols, i, fields), sub)})
	}
	var rest []mrow
	for _, r := range rows {
		if h := b.head(r.cells[i], c); h.kind == headWild {
			rest = append(rest, mrow{arm: r.arm, cells: splice(r.cells, i, nil), binds: bindAll(r.binds, h.vars, c.val)})
		}
	}
	n.def = b.compile(splice(cols, i, nil), rest)
	return n
}

// treeEmit writes one tree.
type treeEmit struct {
	fc    *funcCtx
	s     syntax.Node
	arms  []treeArm
	label map[int]string // the label of each shared arm
	// inlineTerm: every arm body written at a leaf terminates; allTerm:
	// every arm body does.
	inlineTerm, allTerm bool
	// fails are the depths of the default branches no row reaches, each
	// written as a placeholder until the bodies decide their text.
	fails []int
}

// treeMark is the placeholder line of fail k or, for -1, of the jump after
// the tree. NUL never occurs in emitted Go: string literals are quoted.
func treeMark(k int) string { return "\x00match " + strconv.Itoa(k) + "\x00" }

// emitMatchTree writes the tree of a match or a catch, its default
// branches that no row reaches as exit says, and reports whether its Go
// form is a terminating statement.
func (fc *funcCtx) emitMatchTree(s syntax.Node, tree *mnode, arms []treeArm, exit treeExit) bool {
	e := &treeEmit{fc: fc, s: s, arms: arms, label: map[int]string{}, inlineTerm: true, allTerm: true}
	leaves := make([]int, len(arms))
	firstLeaf := make([]*mleaf, len(arms))
	tree.walk(func(n *mnode) {
		if n.leaf != nil {
			if leaves[n.leaf.arm] == 0 {
				firstLeaf[n.leaf.arm] = n.leaf
			}
			leaves[n.leaf.arm]++
		}
	})
	var shared []int
	for arm, k := range leaves {
		if k > 1 {
			shared = append(shared, arm)
			e.label[arm] = fc.temp("arm")
		}
	}
	saved, depth := fc.out, fc.depth
	fc.out = &strings.Builder{}
	if len(shared) > 0 {
		fc.line("{")
		fc.depth++
		for _, arm := range shared {
			for _, bd := range firstLeaf[arm].binds {
				name := fc.declare(bd.v)
				fc.line("var " + name + " " + fc.goType(bd.v.Type))
				if !fc.l.reads[bd.v] {
					fc.line("_ = " + name)
				}
			}
		}
	}
	e.node(tree)
	end, jumps := "", false
	if len(shared) > 0 {
		end = fc.temp("end")
		fc.line(treeMark(-1))
		for k, arm := range shared {
			fc.line(e.label[arm] + ":")
			fc.line("{")
			fc.depth++
			t := arms[arm].body()
			fc.depth--
			fc.line("}")
			e.allTerm = e.allTerm && t
			if !t && k < len(shared)-1 {
				fc.line("goto " + end)
				jumps = true
			}
		}
	}
	panics := exit.needed(e.allTerm)
	treeTerm := e.inlineTerm && (len(e.fails) == 0 || panics)
	text := fc.out.String()
	fc.out = saved
	for k, at := range e.fails {
		repl := ""
		if panics {
			fc.out, fc.depth = &strings.Builder{}, at
			fc.line("default:")
			exit.write()
			repl = fc.out.String()
			fc.out = saved
		}
		text = strings.Replace(text, strings.Repeat("\t", at)+treeMark(k)+"\n", repl, 1)
	}
	fc.depth = depth
	if len(shared) > 0 {
		repl := ""
		if !treeTerm {
			repl = strings.Repeat("\t", depth+1) + "goto " + end + "\n"
			jumps = true
		}
		text = strings.Replace(text, strings.Repeat("\t", depth+1)+treeMark(-1)+"\n", repl, 1)
		if jumps {
			text += strings.Repeat("\t", depth+1) + end + ":\n"
		}
		text += strings.Repeat("\t", depth) + "}\n"
	}
	fc.out.WriteString(text)
	return treeTerm && e.allTerm
}

// node writes one node of the tree.
func (e *treeEmit) node(n *mnode) {
	fc := e.fc
	switch {
	case n.leaf != nil:
		e.leaf(n.leaf)
		return
	case n.fail:
		ice(e.s, "a match tree fails outside a default branch")
	}
	// In a machine's flat step, a type switch on the flat state switches on
	// its tag (machinerun.go, flatTags): each case binds the variant's
	// field.
	// A flat union's switch reads its tag too (flatunion.go).
	var ts *tagSwitch
	if n.typeSwitch {
		labels := make([]string, len(n.cases))
		for k, c := range n.cases {
			labels[k] = c.label
		}
		ts = fc.tagSwitchOf(n.operand, n.t, labels, e.s)
	}
	flat := ts != nil
	switch {
	case flat:
		fc.line(ts.head)
	case n.typeSwitch:
		fc.line("switch " + n.bound + " := " + n.operand + ".(type) {")
	default:
		fc.line("switch " + n.operand + " {")
	}
	fc.enterSwitch()
	for k, c := range n.cases {
		if flat {
			fc.line("case " + strconv.Itoa(ts.tags[k]) + ":")
			fc.depth++
			fc.line(n.bound + " := " + ts.variant(ts.tags[k]))
			fc.line("_ = " + n.bound)
			e.node(c.child)
			fc.depth--
			continue
		}
		fc.line("case " + c.label + ":")
		fc.depth++
		if k == 0 && n.typeSwitch {
			fc.line("_ = " + n.bound)
		}
		e.node(c.child)
		fc.depth--
	}
	if n.def.fail {
		e.fails = append(e.fails, fc.depth)
		fc.line(treeMark(len(e.fails) - 1))
	} else {
		fc.line("default:")
		fc.depth++
		e.node(n.def)
		fc.depth--
	}
	fc.leaveSwitch()
	fc.line("}")
}

// leaf writes a leaf: its arm's body in place, or the assignments of a
// shared arm's bindings and the jump to it.
func (e *treeEmit) leaf(lf *mleaf) {
	fc := e.fc
	if label, ok := e.label[lf.arm]; ok {
		for _, bd := range lf.binds {
			fc.line(fc.locals[bd.v] + " = " + bd.val)
		}
		fc.line("goto " + label)
		return
	}
	for _, bd := range lf.binds {
		name := fc.declare(bd.v)
		fc.line(name + " := " + bd.val)
		if !fc.l.reads[bd.v] {
			fc.line("_ = " + name)
		}
	}
	t := e.arms[lf.arm].body()
	e.inlineTerm = e.inlineTerm && t
	e.allTerm = e.allTerm && t
}
