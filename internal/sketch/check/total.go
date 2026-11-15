package check

import (
	"encoding/binary"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The usefulness solver of match (196p; rows.md, Totality 2 and 5): totality
// and reachability over the case matrix, never by enumerating the values.
//
// Prior art: Maranget's usefulness algorithm, as in OCaml's Parmatch, Rust
// and Gleam's exhaustiveness checker. A pattern is a constructor at a
// column with sub-patterns at the column's child columns: a union's
// variants (their fields are the children), bool's two constants, a
// tuple's or a record's one shape (its elements or fields), and the
// constants of an integer, rune or string type, which never complete
// their column. Specializing a column by a constructor replaces its cell
// by the constructor's sub-patterns, so nested patterns need no other
// rule.

// matchLen is the length of the keyword a totality diagnostic spans: the
// decided parser builds every match from `match` (#196).
const matchLen = syntax.Pos(len("match"))

// totalityBudget is the minimum budget of each match's two solvers, one
// for reachability and one for totality. They share one source budget.
// Steps count written cells scanned during construction,
// specialization, subsumption and memo-key construction; sorting identities
// is charged before it runs. D16 bounds every superlinear proof pass.
// Deciding totality over several subjects is NP-complete in the worst case
// (a case row can encode a clause, so totality encodes unsatisfiability;
// Maranget 2007, Sekar et al.), so no exact checker is linear on every
// input. A match whose proof needs more steps is refused (S103, "too many
// cases"), never accepted unproven. The budget is deterministic for a
// given source: steps, not time. Like rustc's limit it depends on the
// order of the subjects, so reordering them may change whether a match
// near the budget is refused, never a proved verdict. Every match over at
// most 6 subjects of at most 3 variants stays far below it (at most 3^6 =
// 729 residual matrices; TestTotalityPermutations holds the odd cycles of
// 31 subjects under an eighth of their source budget).
//
// Prior art: rustc's pattern_complexity_limit, GHC's -fmax-pmcheck-models.
const totalityBudget = 200_000

// cellBudget is per written pattern node or subject, never per expanded
// matrix cell. Reachability and totality share each source limit.
const cellBudget = 512

// Storage counts scalar slots (a row reference alone is three). The
// carried 16-subject CNF corpus needs more storage credits than steps.
// Both limits remain under max(totalityBudget, 1024*W); the floor is fixed.
const allocationBudget = 1024

// ctorOther is a witness constructor of a column whose constructors never
// complete it (constants): a value no listed constant equals.
const ctorOther = -1

// cell is one constructor cell of a case row: the pattern pat (an interned
// patNode) at column col. A row lists only its constructor cells, sorted
// by column; a column absent from the row is a wildcard there.
type cell struct{ col, pat int }

type solverRow []cell

// patNode is an interned pattern: its constructor at its column and the
// cells of its sub-patterns at the column's child columns (wildcards
// omitted), sorted by column.
type patNode struct {
	ctor int
	kids solverRow
}

// column is one column of a match's matrices: a subject, or a child of a
// constructor at a parent column (a field of a variant or record, an
// element of a tuple). size is its number of constructors, or -1 when
// they never complete it (constants) or it has none (only wildcards).
// empty holds the constructors that have no value (a variant or record
// that holds a Never; types.md, Never): no value is missing for want of
// one, so they complete a column without a row.
type column struct {
	size                int
	empty               map[int]bool
	parent, ctor, field int
	consts              map[string]int // a constant column's constructors, by exact value
	label               func(w witness, col int, out *strings.Builder)
	children            map[int][]int // ctor → its child columns, in the order patterns name them
}

// matrixSolver decides usefulness and totality over one match's case
// matrix (Maranget, "Warnings for pattern matching", 2007), with:
//
//   - column choice by Maranget's necessity heuristic, taken over the
//     shortest rows first: the column needed (a constructor there) by the
//     most of the rows with the fewest constructors. A row with one
//     constructor left is unit, as in DPLL: splitting its column covers or
//     drops it at once, so forced choices are followed first;
//   - memoization of residual matrices under a canonical form that does not
//     depend on column order: columns keep their identity, a row is its set
//     of constructor cells, and the matrix is the sorted set of its rows
//     with unwritten columns implicit, so two orders of splitting that reach the
//     same residual question share one answer;
//   - the cheap shortcuts of OCaml's Parmatch: a row with no constructor
//     left covers everything; equal rows collapse; the case matrix keeps no
//     row a later case subsumes (rowSet.addRow);
//   - one pass that splits a complete column into every constructor's
//     residual (so a union of n variants matched by n arms costs O(n));
//   - a source-weighted step budget (solverFor).
type matrixSolver struct {
	*patSpace
	memo    map[string]missingResult
	rowIDs  map[string]int // residual rows keyed once, then compared by identity
	rowRefs map[rowRef]int // immutable row storage already given an identity
	steps   int
	cells   int  // allocated proof cells (including container/key storage)
	budget  int  // the step bound (solverFor)
	over    bool // the budget ran out: nothing more is proved
	meter   *patternMeter
}

type patternMeter struct {
	steps, cells, budget int
	cellBudget           int
	over                 bool
}

type rowRef struct {
	first *cell
	len   int
}

type patternCost struct{ Written, Budget, CellBudget, TotalSteps, UsefulSteps, TotalCells, UsefulCells int }

// patSpace holds a match's columns and interned patterns, shared by its
// two solvers (reachability and totality), which keep their own memo and
// step count.
type patSpace struct {
	cols    []column
	child   map[[3]int]int // (parent column, constructor, field) → child column
	pats    []patNode
	patKey  map[string]int
	work    int           // columns, local pattern keys and the initial flattening
	cells   int           // source-pattern storage, before either proof
	written int           // syntax nodes in patterns, plus subjects
	meter   *patternMeter // shared by the two proofs of this source match
}

func newPatSpace() *patSpace {
	return &patSpace{child: map[[3]int]int{}, patKey: map[string]int{}, cells: 64}
}

// solver returns a fresh solver over the space with the fixed budget.
func (sp *patSpace) solver() *matrixSolver {
	return &matrixSolver{patSpace: sp, memo: map[string]missingResult{}, rowIDs: map[string]int{}, rowRefs: map[rowRef]int{}, budget: totalityBudget, meter: &patternMeter{budget: totalityBudget, cellBudget: totalityBudget}}
}

// solverFor returns a solver sharing the match's source budget. Only the
// first one charges the shared source-pattern construction. W counts syntax
// nodes in patterns plus subjects, including explicitly written wildcards;
// an unwritten field contributes neither a node nor an index entry.
func (sp *patSpace) solverFor() *matrixSolver {
	s := sp.solver()
	if sp.meter == nil {
		sp.meter = &patternMeter{budget: max(totalityBudget, cellBudget*sp.written), cellBudget: max(totalityBudget, allocationBudget*sp.written)}
		s.meter, s.budget = sp.meter, sp.meter.budget
		s.charge(sp.work)
		s.allocate(sp.cells)
	} else {
		s.meter, s.budget, s.over = sp.meter, sp.meter.budget, sp.meter.over
	}
	return s
}

type missingResult struct {
	witness witness // column → constructor of a value no row matches
	found   bool
}

// witness maps a column to the constructor of a missing value there.
type witness map[int]int

// newMatrixSolver returns a solver over flat subject columns of the given
// sizes (the tests' constructor rows).
func newMatrixSolver(sizes []int) *matrixSolver {
	sp := newPatSpace()
	for _, n := range sizes {
		sp.addColumn(column{size: n})
	}
	return sp.solver()
}

// subjects lists the subject columns.
func (s *patSpace) subjects() []int {
	var out []int
	for j, col := range s.cols {
		if col.parent < 0 {
			out = append(out, j)
		}
	}
	return out
}

// addColumn adds a subject column of size n and returns it.
func (s *patSpace) addColumn(col column) int {
	s.work++
	s.cells += 48 // column, label capture, and amortized slice storage
	col.parent = -1
	s.cols = append(s.cols, col)
	return len(s.cols) - 1
}

// childColumn returns the child column of constructor ctor at parent for
// its field (or element) field, made on first use by mk.
func (s *patSpace) childColumn(parent, ctor, field int, mk func() column) int {
	s.work++
	k := [3]int{parent, ctor, field}
	if j, ok := s.child[k]; ok {
		return j
	}
	col := mk()
	s.cells += 80 // column, child index and the parent's child list
	col.parent, col.ctor, col.field = parent, ctor, field
	s.cols = append(s.cols, col)
	j := len(s.cols) - 1
	s.child[k] = j
	p := &s.cols[parent]
	if p.children == nil {
		p.children = map[int][]int{}
	}
	// Children keep the order patterns first name them: the solvers do not
	// depend on it, and a witness prints its fields in field order
	// (recordText sorts the few it prints).
	p.children[ctor] = append(p.children[ctor], j)
	return j
}

// node interns ctor(kids), with the immediate children sorted by column.
// Its key names child patterns by id, never by an expanded subtree. A
// nested record therefore costs its own fields once, rather than copying
// all descendant cells into every ancestor's key.
func (s *patSpace) node(ctor int, kids solverRow) int {
	s.work += len(kids) + 1
	s.cells += 40 * (len(kids) + 1) // key bytes, pattern and amortized child storage
	var b []byte
	b = strconv.AppendInt(b, int64(ctor), 10)
	if len(kids) > 0 {
		b = append(b, '(')
		for i, k := range kids {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, int64(k.col), 10)
			b = append(b, ':')
			b = strconv.AppendInt(b, int64(k.pat), 10)
		}
		b = append(b, ')')
	}
	key := string(b)
	if id, ok := s.patKey[key]; ok {
		return id
	}
	s.pats = append(s.pats, patNode{ctor: ctor, kids: kids})
	s.patKey[key] = len(s.pats) - 1
	return len(s.pats) - 1
}

// constCtor returns the constructor of a constant at a constant column.
func (s *patSpace) constCtor(col int, exact string) int {
	c := &s.cols[col]
	if c.consts == nil {
		c.consts = map[string]int{}
	}
	if k, ok := c.consts[exact]; ok {
		return k
	}
	k := len(c.consts)
	c.consts[exact] = k
	return k
}

// row converts a full row of constructors (a constructor or -1 for a
// wildcard per subject column) into a solver row of patterns with no
// sub-patterns.
func (s *patSpace) row(r []int) solverRow {
	var out solverRow
	for j, p := range r {
		if p >= 0 {
			out = append(out, cell{j, s.node(p, nil)})
		}
	}
	return s.flat(out)
}

// at finds r's cell at col by binary search: a row is sorted by column.
func (r solverRow) at(col int) (int, bool) {
	i := sort.Search(len(r), func(i int) bool { return r[i].col >= col })
	if i < len(r) && r[i].col == col {
		return r[i].pat, true
	}
	return 0, false
}

// mergeRows merges rows sorted by column whose columns are disjoint.
func mergeRows(a, b solverRow) solverRow {
	out := make(solverRow, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if j == len(b) || i < len(a) && a[i].col < b[j].col {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	return out
}

// replace returns r with its cell at col replaced by kids, by one merge.
func (r solverRow) replace(col int, kids solverRow) solverRow {
	rest := make(solverRow, 0, len(r))
	for _, c := range r {
		if c.col != col {
			rest = append(rest, c)
		}
	}
	if len(kids) == 0 {
		return rest
	}
	return mergeRows(rest, kids)
}

// subsumesPat reports whether every value pattern b matches, a matches.
func (s *matrixSolver) subsumesPat(a, b int) bool {
	if a == b {
		return true
	}
	pa, pb := s.pats[a], s.pats[b]
	if pa.ctor != pb.ctor {
		return false
	}
	return s.subsumes(pa.kids, pb.kids)
}

// subsumes reports whether every value row b matches, row a matches: each
// cell of a has a cell of b at its column that it subsumes. One merge walk
// over the two sorted rows: O(|a|+|b|) at each level.
func (s *matrixSolver) subsumes(a, b solverRow) bool {
	if !s.charge(cost(a, b)) {
		return false
	}
	j := 0
	for _, ca := range a {
		for j < len(b) && b[j].col < ca.col {
			j++
		}
		if j == len(b) || b[j].col != ca.col || !s.subsumesPat(ca.pat, b[j].pat) {
			return false
		}
	}
	return true
}

// compatible reports whether r may match a value q matches: at each
// column of q, r is a wildcard or has q's constructor. One merge walk.
func (s *patSpace) compatible(r, q solverRow) bool {
	j := 0
	for _, cq := range q {
		for j < len(r) && r[j].col < cq.col {
			j++
		}
		if j < len(r) && r[j].col == cq.col && s.pats[r[j].pat].ctor != s.pats[cq.pat].ctor {
			return false
		}
	}
	return true
}

// cost is the work of scanning rows: their cells, and one per row.
func cost(rows ...solverRow) int {
	n := 0
	for _, r := range rows {
		n += len(r) + 1
	}
	return n
}

// flat expands one-shape columns once at the boundary of the case matrix.
// Pattern nodes retain only their immediate children. Construction stays
// linear through nested records and tuples; expansion during a proof is
// charged to that solver instead.
func (s *patSpace) flat(r solverRow) solverRow {
	return s.flatten(r, func(n int) bool { s.work += n; return true }, func(n int) bool { s.cells += n; return true })
}

func (s *matrixSolver) flat(r solverRow) solverRow {
	return s.flatten(r, s.charge, s.allocate)
}

func (s *patSpace) flatten(r solverRow, charge, allocate func(int) bool) solverRow {
	out := solverRow{} // An empty row covers; nil denotes a refused pattern.
	var walk func(solverRow) bool
	walk = func(row solverRow) bool {
		if !charge(len(row) + 1) {
			return false
		}
		for _, c := range row {
			if s.cols[c.col].size == 1 {
				if !walk(s.pats[c.pat].kids) {
					return false
				}
			} else {
				if !allocate(8) {
					return false
				}
				out = append(out, c)
			}
		}
		return true
	}
	if !walk(r) {
		return nil
	}
	// Child columns are created in the order patterns name them. Flattening
	// may combine different branches, so sort once, with comparisons counted.
	if !charge(len(out) * bits.Len(uint(len(out)))) {
		return nil
	}
	sort.Slice(out, func(a, b int) bool { return out[a].col < out[b].col })
	return out
}

// complete fills in a witness the constructor of each column of one
// constructor above a witnessed column, which flat left implicit.
func (s *patSpace) complete(w witness) {
	for j := range w {
		for p := s.cols[j].parent; p >= 0; p = s.cols[p].parent {
			if _, ok := w[p]; ok || s.cols[p].size != 1 {
				break
			}
			w[p] = 0
		}
	}
}

// rowSet is a case matrix with its rows indexed by their constructor cells,
// so a case finds the rows it may meet without scanning the others: a
// union of n variants matched by n arms costs O(n), not O(n²). Every scan
// of a candidate is charged to the solver.
type rowSet struct {
	s        *matrixSolver
	rows     []solverRow
	dead     []bool // dropped: a later row subsumed it
	byCell   map[[2]int][]int
	byFirst  map[[2]int][]int
	byColumn map[int]int // written occurrences; wildcards are never stored
	empty    []int       // rows with no cell: they cover everything
	cover    int         // row that alone covered the last query, or -1
}

func newRowSet(s *matrixSolver) *rowSet {
	if !s.allocate(48) {
		return &rowSet{s: s}
	}
	return &rowSet{s: s, byCell: map[[2]int][]int{}, byFirst: map[[2]int][]int{}, byColumn: map[int]int{}}
}

func (m *rowSet) key(c cell) [2]int { return [2]int{c.col, m.s.pats[c.pat].ctor} }

// alive filters an index list in place, dropping the rows a later row
// subsumed, and charges the scan; false when the budget ran out.
func (m *rowSet) alive(list *[]int) ([]int, bool) {
	if !m.s.charge(len(*list)) {
		return nil, false
	}
	out := (*list)[:0]
	for _, i := range *list {
		if !m.dead[i] {
			out = append(out, i)
		}
	}
	*list = out
	return out, true
}

// all returns the rows in source order.
func (m *rowSet) all() []solverRow {
	if !m.s.charge(len(m.rows)) || !m.s.allocate(12*len(m.rows)) {
		return nil
	}
	var out []solverRow
	for i, r := range m.rows {
		if !m.dead[i] {
			out = append(out, r)
		}
	}
	return out
}

func (m *rowSet) add(r solverRow) {
	if !m.s.charge(len(r)+1) || !m.s.allocate(32+32*len(r)) {
		return
	}
	i := len(m.rows)
	m.rows = append(m.rows, r)
	m.dead = append(m.dead, false)
	if len(r) == 0 {
		m.empty = append(m.empty, i)
	} else {
		k := m.key(r[0])
		m.byFirst[k] = append(m.byFirst[k], i)
	}
	for _, c := range r {
		k := m.key(c)
		m.byCell[k] = append(m.byCell[k], i)
		m.byColumn[c.col]++
	}
}

// compatibleRows returns the rows that may meet q (s.compatible), in
// source order; false when the budget ran out.
func (m *rowSet) compatibleRows(q solverRow) ([]solverRow, bool) {
	if len(q) == 0 {
		rows := m.all()
		return rows, m.s.charge(len(rows))
	}
	// The cell of q with the fewest candidates: the rows with its
	// constructor and the rows with no cell at its column.
	best, bestN := -1, -1
	if !m.s.charge(len(q)) {
		return nil, false
	}
	for i, c := range q {
		n := len(m.byCell[m.key(c)]) + len(m.rows) - m.byColumn[c.col]
		if best < 0 || n < bestN {
			best, bestN = i, n
		}
	}
	bc := m.key(q[best])
	l0 := m.byCell[bc]
	with, ok := m.alive(&l0)
	m.byCell[bc] = with
	if !ok {
		return nil, false
	}
	var without []int
	col := q[best].col
	if m.byColumn[col] != len(m.rows) {
		if !m.s.allocate(4 * (len(m.rows) - m.byColumn[col])) {
			return nil, false
		}
		for i, r := range m.rows {
			if !m.s.charge(1 + bits.Len(uint(len(r)))) {
				return nil, false
			}
			if _, has := r.at(col); !m.dead[i] && !has {
				without = append(without, i)
			}
		}
	}
	if !m.s.allocate(13 * (len(with) + len(without))) {
		return nil, false
	}
	idx := mergeSorted(with, without)
	var out []solverRow
	for _, i := range idx {
		if !m.s.charge(cost(m.rows[i]) + len(q)) {
			return nil, false
		}
		if r := m.rows[i]; m.s.compatible(r, q) {
			out = append(out, r)
		}
	}
	return out, true
}

func mergeSorted(a, b []int) []int {
	out := make([]int, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || i < len(a) && a[i] < b[j]:
			out = append(out, a[i])
			i++
		default:
			out = append(out, b[j])
			j++
		}
	}
	return out
}

// addRow appends a useful case row and drops the rows it subsumes, so the
// matrix never holds a subsumed row. Dropping one changes neither
// usefulness nor totality: every value it matches, the subsuming row
// matches too. Past the budget nothing more is proved, so the matrix
// stops growing.
func (m *rowSet) addRow(q solverRow) {
	if m.s.over {
		return
	}
	if len(q) == 0 {
		if !m.s.charge(len(m.rows)) {
			return
		}
		for i := range m.rows {
			m.dead[i] = true
		}
	} else {
		k := m.key(q[0])
		l := m.byCell[k]
		cand, ok := m.alive(&l)
		m.byCell[k] = cand
		if !ok {
			return
		}
		for _, i := range cand {
			if m.s.subsumes(q, m.rows[i]) {
				m.dead[i] = true
			}
			if m.s.over {
				return
			}
		}
	}
	m.add(q)
}

// addMaximal adds q to a set of maximal rows: nothing when a row of the
// set already subsumes it, else q in place of the rows it subsumes. The
// result is the maximal rows of everything added, in any order of adding.
func (m *rowSet) addMaximal(q solverRow) {
	if m.s.over {
		return
	}
	// A row that subsumes q has each of its cells in q: its first cell is
	// one of q's, or it has none.
	e, ok := m.alive(&m.empty)
	if !ok {
		return
	}
	if len(e) > 0 {
		return
	}
	for _, c := range q {
		k := m.key(c)
		l := m.byFirst[k]
		cand, ok := m.alive(&l)
		m.byFirst[k] = cand
		if !ok {
			return
		}
		for _, i := range cand {
			if m.s.subsumes(m.rows[i], q) {
				return
			}
			if m.s.over {
				return
			}
		}
	}
	m.addRow(q)
}

// subsumer returns the index of a live row that alone covers q, or -1
// (the rows cover q only together, or the budget ran out). A dead row was
// subsumed by a live one, so the live rows suffice. A row that subsumes q
// has its first cell among q's, or no cell.
func (m *rowSet) subsumer(q solverRow) int {
	e, ok := m.alive(&m.empty)
	if !ok {
		return -1
	}
	if len(e) > 0 {
		return e[0]
	}
	for _, c := range q {
		k := m.key(c)
		l := m.byFirst[k]
		cand, ok := m.alive(&l)
		m.byFirst[k] = cand
		if !ok {
			return -1
		}
		for _, i := range cand {
			if m.s.subsumes(m.rows[i], q) {
				return i
			}
			if m.s.over {
				return -1
			}
		}
	}
	return -1
}

// caseUseful reports whether case q matches a value the rows of m do not.
// It specializes the rows q may meet by q's constructors, so what remains
// is a usefulness question over q's sub-patterns, and at the end a
// totality question over q's wildcard columns, answered with the shared
// memo. ok is false when the budget ran out.
func (s *matrixSolver) caseUseful(m *rowSet, q solverRow) (useful, ok bool) {
	m.cover = m.subsumer(q)
	if s.over {
		return false, false
	}
	if m.cover >= 0 {
		return false, true
	}
	rows, ok := m.compatibleRows(q)
	if !ok {
		return false, false
	}
	res := s.useful(rows, q, true)
	return res, !s.over
}

// useful is U(rows, q): rows are compatible with q
// when known is set. Every row scanned is charged by its cells.
func (s *matrixSolver) useful(rows []solverRow, q solverRow, known bool) bool {
	for len(q) > 0 {
		if s.over {
			return false
		}
		if !known {
			if !s.allocate(12 * len(rows)) {
				return false
			}
			kept := rows[:0:0]
			for _, r := range rows {
				if !s.charge(cost(r) + len(q)) {
					return false
				}
				if s.compatible(r, q) {
					kept = append(kept, r)
				}
			}
			rows = kept
		}
		known = false
		// Specialize the rows and q by q's constructors: a fixed cell
		// becomes its sub-patterns (flat already), merged in by column.
		if !s.charge(len(q)) || !s.allocate(24*len(q)+3*len(rows)) {
			return false
		}
		fixed := make(map[int]bool, len(q))
		var next solverRow
		for _, cq := range q {
			fixed[cq.col] = true
			if !s.charge(len(s.pats[cq.pat].kids)) || !s.allocate(8*len(s.pats[cq.pat].kids)) {
				return false
			}
			next = append(next, s.pats[cq.pat].kids...)
		}
		next = s.flat(next)
		residual := make([]solverRow, 0, len(rows))
		for _, r := range rows {
			if !s.charge(cost(r)) || !s.allocate(8*len(r)) {
				return false
			}
			var keep, kids solverRow
			for _, c := range r {
				if fixed[c.col] {
					if !s.charge(len(s.pats[c.pat].kids)) || !s.allocate(8*len(s.pats[c.pat].kids)) {
						return false
					}
					kids = append(kids, s.pats[c.pat].kids...)
				} else {
					keep = append(keep, c)
				}
			}
			if len(kids) == 0 {
				residual = append(residual, keep)
				continue
			}
			kids = s.flat(kids)
			if s.over || !s.allocate(2*(len(keep)+len(kids))) {
				return false
			}
			residual = append(residual, mergeRows(keep, kids))
		}
		rows, q = residual, next
	}
	return s.missing(rows).found
}

// sortRow sorts a pattern's immediate child cells once during resolution.
func (s *patSpace) sortRow(r solverRow) {
	s.work += len(r) * bits.Len(uint(len(r)))
	sort.Slice(r, func(a, b int) bool { return r[a].col < r[b].col })
}

// total decides whether the case rows cover every value of the subjects.
// It returns a witness the rows miss, and ok false when the budget ran
// out.
func (s *matrixSolver) total(rows []solverRow) (w witness, missing, ok bool) {
	if !s.charge(len(s.cols)) || !s.allocate(4*len(s.cols)) {
		return nil, false, false
	}
	cols := s.subjects()
	// A subject of no variant (its variants were all reported unknown) has
	// no value, so no value vector exists and none is missing (D3).
	for _, j := range cols {
		if s.cols[j].size == 0 || s.cols[j].size > 0 && len(s.cols[j].empty) == s.cols[j].size {
			return nil, false, true
		}
	}
	res := s.missing(rows)
	if s.over {
		return nil, false, false
	}
	if !res.found {
		return nil, false, true
	}
	if !s.charge(len(res.witness)+len(s.cols)) || !s.allocate(8*(len(res.witness)+len(s.cols))) {
		return nil, false, false
	}
	w = make(witness, len(res.witness))
	for k, v := range res.witness {
		w[k] = v
	}
	s.complete(w)
	return w, true, true
}

// missing finds a value no row matches. Columns absent from every row are
// unconstrained and cannot change the answer. The memo key needs only the
// sparse row identities, never the remaining column set.
func (s *matrixSolver) missing(rows []solverRow) missingResult {
	if !s.charge(len(rows)) {
		return missingResult{}
	}
	for _, r := range rows {
		if len(r) == 0 {
			return missingResult{} // a row of wildcards covers everything
		}
	}
	if len(rows) == 0 {
		return missingResult{witness: witness{}, found: true}
	}
	if !s.charge(cost(rows...)) {
		return missingResult{}
	}
	rows, key := s.canonical(rows)
	if s.over {
		return missingResult{}
	}
	if r, ok := s.memo[key]; ok {
		return r
	}
	res := s.split(rows)
	if !s.over && s.allocate(8) {
		s.memo[key] = res
	}
	return res
}

// charge counts work of the given weight against the budget and reports
// whether the budget still holds.
func (s *matrixSolver) charge(w int) bool {
	if s.over || s.meter.over {
		s.over = true
		return false
	}
	if w > s.budget-s.meter.steps {
		s.steps += s.budget - s.meter.steps
		s.meter.steps = s.budget
		s.meter.over = true
		s.over = true
		return false
	}
	s.steps += w
	s.meter.steps += w
	return true
}

// allocate reserves proof storage before allocating it. A cell is a
// scalar slot; byte keys count in word-sized chunks. Dynamic containers
// include a constant allowance for growth, copies and index entries.
func (s *matrixSolver) allocate(n int) bool {
	if s.over || s.meter.over || n > s.meter.cellBudget-s.meter.cells {
		s.over, s.meter.over = true, true
		return false
	}
	s.cells += n
	s.meter.cells += n
	return true
}

// split chooses a column and decides the matrix by its constructors.
func (s *matrixSolver) split(rows []solverRow) missingResult {
	col := s.chooseColumn(rows)
	if s.over {
		return missingResult{}
	}
	present := map[int]bool{}
	wild := 0
	for _, r := range rows {
		if !s.charge(1 + bits.Len(uint(len(r)))) {
			return missingResult{}
		}
		if p, ok := r.at(col); ok {
			if !present[s.pats[p].ctor] && !s.allocate(16) {
				return missingResult{}
			}
			present[s.pats[p].ctor] = true
		} else {
			wild++
		}
	}
	size := s.cols[col].size
	empty := s.cols[col].empty
	covered := len(present)
	for k := range empty {
		if !present[k] {
			covered++
		}
	}
	if size >= 0 && covered == size {
		// Every constructor appears: one pass puts each row in the
		// residual of its constructor, a wildcard row in every one.
		if !s.charge(cost(rows...)+wild*size) || !s.allocate(3*size+12*(len(rows)+wild*size)) {
			return missingResult{}
		}
		buckets := make([][]solverRow, size)
		for _, r := range rows {
			if p, ok := r.at(col); ok {
				k := s.pats[p].ctor
				kids := s.flat(s.pats[p].kids)
				if s.over || !s.charge(len(r)+len(kids)) || !s.allocate(4*len(r)+2*len(kids)) {
					return missingResult{}
				}
				buckets[k] = append(buckets[k], r.replace(col, kids))
				continue
			}
			for k := range buckets {
				buckets[k] = append(buckets[k], r)
			}
		}
		for ctor := 0; ctor < size; ctor++ {
			if empty[ctor] {
				continue // no value has it
			}
			res := s.missing(buckets[ctor])
			if res.found {
				return s.withCell(res, col, ctor)
			}
			if s.over {
				return missingResult{}
			}
		}
		return missingResult{}
	}
	// An incomplete column: a value missing from the default matrix,
	// with a constructor no row names here, is missing from the whole.
	res := s.missing(s.defaultCol(rows, col))
	if !res.found {
		return res
	}
	if size < 0 {
		return s.withCell(res, col, ctorOther)
	}
	for ctor := 0; ctor < size; ctor++ {
		if !present[ctor] && !empty[ctor] {
			return s.withCell(res, col, ctor)
		}
	}
	return missingResult{}
}

func (s *matrixSolver) withCell(res missingResult, col, ctor int) missingResult {
	if !s.charge(len(res.witness)+1) || !s.allocate(16*(len(res.witness)+1)) {
		return missingResult{}
	}
	w := make(witness, len(res.witness)+1)
	for k, v := range res.witness {
		w[k] = v
	}
	w[col] = ctor
	return missingResult{witness: w, found: true}
}

// chooseColumn applies the necessity heuristic over the shortest rows: of
// the rows with the fewest constructors, the column most of them need; a
// tie goes to the lowest column, so the choice is deterministic.
func (s *matrixSolver) chooseColumn(rows []solverRow) int {
	if !s.charge(2 * len(rows)) {
		return -1
	}
	shortest := -1
	for _, r := range rows {
		if shortest < 0 || len(r) < shortest {
			shortest = len(r)
		}
	}
	need := map[int]int{}
	for _, r := range rows {
		if len(r) != shortest {
			continue
		}
		if !s.charge(len(r)) {
			return -1
		}
		for _, c := range r {
			if need[c.col] == 0 && !s.allocate(16) {
				return -1
			}
			need[c.col]++
		}
	}
	best, bestN := -1, -1
	if !s.charge(len(need)) {
		return -1
	}
	for col, n := range need {
		if n > bestN || n == bestN && col < best {
			best, bestN = col, n
		}
	}
	return best
}

func (s *matrixSolver) defaultCol(rows []solverRow, col int) []solverRow {
	var out []solverRow
	for _, r := range rows {
		if !s.charge(1 + bits.Len(uint(len(r)))) {
			return nil
		}
		if _, ok := r.at(col); !ok {
			if !s.allocate(12) {
				return nil
			}
			out = append(out, r)
		}
	}
	return out
}

// canonical interns each row's cells once and sorts row identities, so a
// comparison never rescans a wide row's string key. The matrix key names
// the sparse rows as a set, independently of column order. Immutable row
// storage is interned once; its later uses do not serialize its cells again.
func (s *matrixSolver) canonical(rows []solverRow) ([]solverRow, string) {
	// Sorting fixed-width identities is bounded before it runs. Keying the
	// cells and building the final key were charged by missing's matrix scan.
	if !s.charge(len(rows)*bits.Len(uint(len(rows)))) || !s.allocate(12*len(rows)) {
		return nil, ""
	}
	type indexedRow struct {
		id  int
		row solverRow
	}
	ids := make([]indexedRow, len(rows))
	for i, r := range rows {
		ref := rowRef{&r[0], len(r)}
		if id, known := s.rowRefs[ref]; known {
			ids[i] = indexedRow{id, r}
			continue
		}
		if !s.allocate(4*len(r) + 16) {
			return nil, ""
		}
		b := make([]byte, 0, 16*len(r))
		for _, c := range r {
			b = binary.LittleEndian.AppendUint64(b, uint64(c.col))
			b = binary.LittleEndian.AppendUint64(b, uint64(c.pat))
		}
		key := string(b)
		id, ok := s.rowIDs[key]
		if !ok {
			id = len(s.rowIDs)
			s.rowIDs[key] = id
		}
		s.rowRefs[ref] = id
		ids[i] = indexedRow{id, r}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].id < ids[j].id })
	b := make([]byte, 0, 8*len(ids))
	out := make([]solverRow, 0, len(ids))
	prev := -1
	for _, entry := range ids {
		id := entry.id
		if id == prev {
			continue
		}
		prev = id
		b = binary.LittleEndian.AppendUint64(b, uint64(id))
		out = append(out, entry.row)
	}
	return out, string(b)
}

// witnessText prints the value a witness names at column col: a variant
// with the fields the witness constrains, a tuple, a constant, or `_`.
func (s *patSpace) witnessText(w witness, col int) string {
	var out strings.Builder
	s.writeWitness(w, col, &out)
	return out.String()
}

func (s *patSpace) writeWitness(w witness, col int, out *strings.Builder) {
	if s.cols[col].label != nil {
		s.cols[col].label(w, col, out)
		return
	}
	out.WriteByte('_')
}
