package lower

// The typed-IR build path (wf10 IR1 spike, then plan-tier: IR is the build
// path, owner 2026-11-12). LowerIR lowers a unit as Lower does, except
// that each function whose body the IR builder can express gets a typed
// IR body instead of a Go body: its Go declaration has no body, and the IR
// file carries the body for the patched compiler's -ergoir entry
// (toolchain/ir). Every other function keeps its source body, and the
// reason is recorded. Source emission is the test-time reference: Lower
// never reads any of this.
//
// The builder is one file per construct family, so two lanes can grow it
// at once: ir.go (format, entry, report), ir_types.go (types),
// ir_stmt.go (statements), ir_expr.go (expressions), ir_call.go (calls).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// IRFileName is the IR file LowerIR adds to the module.
const IRFileName = "ergoir.json"

// IRReport is what LowerIR did: the functions given IR bodies, and why the
// others kept their source bodies (reason -> count).
type IRReport struct {
	IR       []string       `json:"ir"`
	Fallback map[string]int `json:"fallback"`
	Hash     string         `json:"hash"`
}

// LowerIR is Lower with the experimental IR bodies. The report's Hash
// names the IR file's content. The build keys the main package's compile
// on the IR through one key only: the -ergoir flag names a copy of the IR
// file named by its content (irspike's stableIR), so main.go carries no
// hash of its own (batch-18 verify M2: a second key was redundant).
func LowerIR(u Unit) ([]File, IRReport, error) { return lowerIR(u, nil) }

// LowerIRTests is LowerTests with the IR bodies: the test build's IR
// path, which the agreement property compares with LowerTests.
func LowerIRTests(u Unit) ([]File, IRReport, error) {
	return lowerIR(u, &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}})
}

// IRCensus is every reason the IR builder cannot lower each function of a
// lowering, not only the first: the work list of the IR coverage units.
type IRCensus struct {
	Funcs []IRCensusFunc `json:"funcs"`
}

// IRCensusFunc is one lowered body. Reasons are distinct, in the order the
// builder met them; none means the builder lowers it.
type IRCensusFunc struct {
	Pkg     string   `json:"pkg"`
	Name    string   `json:"name"`
	File    string   `json:"file,omitempty"` // the source file, for a sourced body
	Std     bool     `json:"std,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

// CensusIR lowers u (its test build with tests) and walks every body the
// lowering emits with the IR builder, recording every reason it cannot be
// lowered: a reason stops only its statement, condition or parameter,
// and the walk goes on. No IR is emitted.
func CensusIR(u Unit, tests bool) (IRCensus, error) {
	return censusIR(u, tests, nil)
}

// CensusStd is the census of every given std function's body, lowered as
// library bodies within u (a program that need not call them): each is
// reached as a call would reach it. A function the lowering cannot reach
// alone is recorded with the reason "std body not reachable alone".
func CensusStd(u Unit, funcs []*check.Func) (IRCensus, error) {
	return censusIR(u, false, funcs)
}

func censusIR(u Unit, tests bool, reach []*check.Func) (IRCensus, error) {
	if err := u.Validate(); err != nil {
		return IRCensus{}, err
	}
	var tl *testLowering
	if tests {
		tl = &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}
	}
	l := newBuildLowerer(u, tl)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, census: &IRCensus{}, reach: reach}
	if _, err := l.program(); err != nil {
		return IRCensus{}, err
	}
	return *l.ir.census, nil
}

// reachAll queues a census's extra bodies (CensusStd); program calls it
// once the package's own declarations are lowered.
func (st *irState) reachAll(l *lowerer) {
	st.reached = append(st.reached, st.reach...)
	for _, f := range st.reach {
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: f.Pkg.Path, Name: f.Name, Std: f.Pkg.Std(), Reasons: []string{"std body not reachable alone"}})
				}
			}()
			l.useFunc(f, f.Decl.Name)
		}()
	}
	st.reach = nil
}

func lowerIR(u Unit, tests *testLowering) ([]File, IRReport, error) {
	if err := u.Validate(); err != nil {
		return nil, IRReport{}, err
	}
	l := newBuildLowerer(u, tests)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}}
	code, err := l.program()
	if err != nil {
		return nil, IRReport{}, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	data, err := json.Marshal(&l.ir.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	l.ir.report.Hash = hex.EncodeToString(sum[:])
	for _, decl := range slices.Sorted(maps.Keys(l.ir.keep)) {
		code = append(code, []byte("\n"+decl+" // an import an IR body names\n")...)
	}
	files := append(moduleFiles(u.Go, code), l.assetFiles()...)
	files = append(files, File{Path: IRFileName, Data: data})
	sort.Strings(l.ir.report.IR)
	return files, l.ir.report, nil
}

// irState is the IR a LowerIR run collects.
type irState struct {
	out    irFile
	files  map[string]int
	report IRReport
	// census, when set, collects every reason of every body (CensusIR);
	// then no body gets IR.
	census *IRCensus
	reach  []*check.Func
	// keep are declarations that keep the imports IR bodies name used
	// (keepImport).
	keep map[string]bool
	// reached are the census bodies reachAll has queued: a restart
	// queues them again.
	reached []*check.Func
}

// restart empties st for a lowering that runs again from the start
// (lowerAgain, after a flat union bailed): the bodies the abandoned run
// built, and their reasons, are dropped, since every function is lowered
// again.
func (st *irState) restart() *irState {
	if st == nil {
		return nil
	}
	reach := append(st.reached, st.reach...)
	census := st.census
	if census != nil {
		census = &IRCensus{}
	}
	*st = irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, census: census, reach: reach}
	return st
}

// The IR format; the derivative's noder/ergoir.go reads the same shape.
type irFile struct {
	V     int      `json:"v"`
	Files []string `json:"files"`
	Funcs []irFunc `json:"funcs"`
}

type irFunc struct {
	Name   string    `json:"name"`
	Pos    irPos     `json:"pos"`
	End    irPos     `json:"end"`
	Locals []irLocal `json:"locals,omitempty"`
	Body   []*irStmt `json:"body"`
}

type irLocal struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Pos  irPos  `json:"pos"`
}

type irPos [3]int

type irStmt struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	Local int       `json:"local,omitempty"`
	Key   *int      `json:"key,omitempty"`
	Val   *int      `json:"val,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Then  []*irStmt `json:"then,omitempty"`
	Else  []*irStmt `json:"else,omitempty"`
	Body  []*irStmt `json:"body,omitempty"`
	End   irPos     `json:"end"`
	// ElsePos and ElseEnd bound an if's else block (DWARF scopes must
	// increase).
	ElsePos irPos `json:"epos"`
	ElseEnd irPos `json:"eend"`
	// Op is an op-assignment's operator (set: x op= X).
	Op string `json:"op,omitempty"`
	// Cases are a "tswitch"'s clauses (ir_fail.go); Else is its default.
	Cases []*irCase `json:"cases,omitempty"`
	// Label is a loop's or a block's label, or the one a break, continue
	// or goto names (ir_tree.go).
	Label string `json:"label,omitempty"`
}

type irExpr struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	I     int       `json:"i,omitempty"`
	T     string    `json:"t,omitempty"`
	V     string    `json:"v,omitempty"`
	Op    string    `json:"op,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Y     *irExpr   `json:"y,omitempty"`
	Name  string    `json:"name,omitempty"`
	Args  []*irExpr `json:"args,omitempty"`
	Elems []*irExpr `json:"elems,omitempty"`
	Keys  []string  `json:"keys,omitempty"`
}

// irUnsupported stops an IR body: the function keeps its source body.
type irUnsupported struct{ reason string }

func irNo(format string, args ...any) { panic(irUnsupported{fmt.Sprintf(format, args...)}) }

// irBuilder builds one function's IR body.
type irBuilder struct {
	fc     *funcCtx
	l      *lowerer
	st     *irState
	fn     *irFunc
	locals map[*check.Var]int
	next   int
	env    int // the environment's local, or -1 in a pure function
	result check.Type
	// reasons are a census's reasons for this body, distinct, in order.
	reasons []string
	// loops is the depth of loops around the statement being built.
	loops int
	// tries counts the try expressions being built (ir_fail.go): inside
	// one, a fallible call fails the frame too.
	tries int
	// admitted are the unions admitted so far (ir_union.go): one that
	// holds itself is admitted once.
	admitted map[*check.TypeName]bool
	// switches is the depth of Go switches (a match's, a catch's) inside
	// the innermost loop: a break there would leave the switch, not the
	// loop, so it names the loop's label (ir_match.go).
	switches int
	// label is the innermost loop's label, "" until a break inside a
	// switch needs one; labels counts the function's labels (ir_tree.go).
	label  string
	labels int
	// scalars are the locals in the scalar form (ir_scalar.go).
	scalars map[*check.Var]*irScalar
	// names allocates the temporaries' names (ir_fail.go, tempNames).
	names *namer
	// given are the names the body's locals and temporaries have: a
	// source local keeps its spelling unless Go's scope or one of these
	// has it (ir_stmt.go, localName).
	given map[string]bool
}

// gate stops the body for reason; a census records it and goes on.
func (b *irBuilder) gate(reason string) {
	if b.st.census == nil {
		irNo("%s", reason)
	}
	b.note(reason)
}

func (b *irBuilder) note(reason string) {
	for _, r := range b.reasons {
		if r == reason {
			return
		}
	}
	b.reasons = append(b.reasons, reason)
}

// part runs one part of a body (a statement, a condition, a parameter).
// Outside a census a reason stops the body; in a census the part's reason
// is recorded and the walk goes on with the next part.
func (b *irBuilder) part(f func()) {
	if b.st.census == nil {
		f()
		return
	}
	defer func() {
		if r := recover(); r != nil {
			u, isNo := r.(irUnsupported)
			if !isNo {
				panic(r)
			}
			b.note(u.reason)
		}
	}()
	f()
}

// body tries an IR body for fd. params are the Ergo parameters in order;
// gates are the emitter's reasons this body cannot take an IR body (a
// frame, a variant, owned or generic parameters). It reports whether fd
// got one. The caller has written fd's Go head.
func (st *irState) body(fc *funcCtx, fd *funcDecl, params []*check.Var, gates []string) (ok bool) {
	f := fd.fn
	b := &irBuilder{fc: fc, l: fc.l, st: st, locals: map[*check.Var]int{}, env: -1, result: fc.frame.result}
	name := fd.name
	if fd.method != nil {
		name = fd.method.name + "." + fd.name
	}
	defer func() {
		if r := recover(); r != nil {
			u, isNo := r.(irUnsupported)
			if !isNo {
				panic(r)
			}
			st.report.Fallback[u.reason]++
			b.note(u.reason)
			ok = false
		}
		if st.census != nil {
			pkg := ""
			if f.Pkg != nil {
				pkg = f.Pkg.Path
			}
			file := ""
			if _, sourced := b.l.sourceOf(f.Decl); sourced {
				_, file, _, _ = b.l.at(f.Decl, f.Decl.Pos())
			}
			st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: pkg, Name: name, File: file, Std: f.Pkg.Std(), Reasons: b.reasons})
			ok = false
		}
	}()
	if !fc.directives {
		b.gate("library body")
	}
	for _, g := range gates {
		b.gate(g)
	}
	if fd.ptr {
		b.gate("pointer receiver")
	}
	if len(fc.witness) > 0 {
		b.gate("witness parameter")
	}
	if f.Decl.Body == nil {
		irNo("no body")
	}
	// Go parameter order: receiver, environment, parameters (emitBody).
	for i, v := range params {
		if v == nil {
			// An unnamed parameter (`_ T`) has its slot; no body reads it.
			if i == 0 && !fd.pure && fd.method == nil {
				b.env = b.next
				b.next++
			}
			b.next++
			if i == 0 && fd.method != nil && !fd.pure {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && fd.method != nil {
			b.locals[v] = b.next
			b.next++
			if !fd.pure {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && !fd.pure {
			b.env = b.next
			b.next++
		}
		b.locals[v] = b.next
		b.next++
		b.part(func() { b.goType(v.Type, f.Decl) })
	}
	if len(params) == 0 && !fd.pure {
		b.env = b.next
		b.next++
	}
	if b.result != nil {
		b.part(func() { b.goType(b.result, f.Decl) })
	}
	b.fn = &irFunc{Name: name, Pos: b.pos(f.Decl, f.Decl.Pos()), End: b.pos(f.Decl, f.Decl.Body.Rbrace)}
	b.names = b.tempNames(f.Decl.Body) // ir_fail.go
	if b.env >= 0 && frameFootprint(f.Pkg.Info, f.Decl).EntryPoll && !b.l.knownClosed(f) && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(f.Decl))
	}
	b.fn.Body = append(b.fn.Body, b.bodyStmts(f.Decl.Body.List)...) // ir_fail.go
	b.fn.Body = append(b.fn.Body, b.bodyEnd(b.fn.Body, b.fn.End)...)
	b.fn.Body = irUseLocals(b.fn.Body) // ir_end.go
	if st.census != nil {
		return false
	}
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// intrinsic records f, whose body is the lowerer's own Go text (an extern
// body), in a census.
func (st *irState) intrinsic(f *check.Func) {
	if st.census == nil {
		return
	}
	pkg := ""
	if f.Pkg != nil {
		pkg = f.Pkg.Path
	}
	st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: pkg, Name: f.Name, Std: f.Pkg.Std(), Reasons: []string{"intrinsic body"}})
}

// pos is n's position as the IR spells it. A census also walks bodies
// outside the program's sources (std); their positions are not needed.
func (b *irBuilder) pos(n syntax.Node, p syntax.Pos) irPos {
	if _, ok := b.l.sourceOf(n); !ok {
		if b.st.census != nil {
			return irPos{}
		}
		irNo("node outside the program's sources")
	}
	_, path, line, col := b.l.at(n, p)
	i, ok := b.st.files[path]
	if !ok {
		i = len(b.st.out.Files)
		b.st.files[path] = i
		b.st.out.Files = append(b.st.out.Files, path)
	}
	return irPos{i, line, col}
}
