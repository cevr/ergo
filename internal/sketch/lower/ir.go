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
	// Memo counts the loop-invariant pure calls the IR bodies compute
	// once per entry of their loop (ir_memo.go).
	Memo int `json:"memo,omitempty"`
	// Merge counts the pure calls that read an equal call's value
	// (ir_merge.go).
	Merge int `json:"merge,omitempty"`
	// Fold counts the pure Go calls on constants computed at compile
	// time (ir_fold.go).
	Fold int    `json:"fold,omitempty"`
	Hash string `json:"hash"`
	// Forms counts the IR forms the bodies held before erasure, by
	// kind and layout ("match/flat"; ir_forms.go): in memory only.
	Forms map[string]int `json:"-"`
	// FormSites are the decisions of the passes over the IR bodies, by
	// position (ir_forms_report.go): ergo inspect forms.
	FormSites []FormSite `json:"formSites,omitempty"`
	// ConstFolds counts the operations of constants folded into one
	// (ir_constfold.go).
	ConstFolds int `json:"-"`
}

// LowerIR is Lower with the IR bodies. The report's Hash names the IR
// file's content. A build keys the main package's compile on the IR
// through one key: the direct path digests the IR file into the compile's
// action key, go build's -ergoir flag names a file whose path names its
// content (batch-18 verify M2: main.go carries no hash of its own).
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
	return lowerIRForms(u, tests, false)
}

// LowerIRReference is LowerIR with no IR form built (irFormLowering "off",
// ir_forms.go): the reference TestFormsEraseToTheReference compares with.
func LowerIRReference(u Unit, tests bool) ([]File, IRReport, error) {
	var tl *testLowering
	if tests {
		tl = &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}
	}
	return lowerIRForms(u, tl, true)
}

func lowerIRForms(u Unit, tests *testLowering, formsOff bool) ([]File, IRReport, error) {
	files, _, report, _, err := lowerIRModule(u, tests, formsOff)
	if err != nil {
		return nil, IRReport{}, err
	}
	data, err := json.Marshal(report.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	report.Hash = hex.EncodeToString(sum[:])
	files = append(files, File{Path: IRFileName, Data: data})
	return files, report.IRReport, nil
}

// LowerIRSplit is LowerIR (LowerIRTests when tests is set) with the
// package split (LowerSplit): one Go package per Ergo package and std
// package, each with the IR file of its own bodies (pkgsplit_ir.go). A
// program the split does not split keeps LowerIR's one package and its
// one IR file, with the reason in the report.
func LowerIRSplit(u Unit, tests bool) ([]File, IRReport, SplitReport, error) {
	return lowerIRSplit(u, tests, programModule)
}

func lowerIRSplit(u Unit, tests bool, base string) ([]File, IRReport, SplitReport, error) {
	var tl *testLowering
	if tests {
		tl = &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}
	}
	files, origins, report, l, err := lowerIRModule(u, tl, false)
	if err != nil {
		return nil, IRReport{}, SplitReport{}, err
	}
	i := slices.IndexFunc(files, func(f File) bool { return isMainGo(f.Path) })
	var out []File
	rep := SplitReport{Reason: "no main.go"}
	if i >= 0 {
		out, rep = splitMain(u, files[i].Data, base, origins, &l.ir.out)
	}
	res := files
	if out == nil {
		if rep.Packages == 0 {
			rep.Packages = 1
		}
		data, err := json.Marshal(report.out)
		if err != nil {
			return nil, IRReport{}, SplitReport{}, err
		}
		res = append(slices.Clone(files), File{Path: IRFileName, Data: data})
	} else {
		res = append(slices.Clone(files[:i]), out...)
		res = append(res, files[i+1:]...)
	}
	report.Hash = irHash(res)
	return res, report.IRReport, rep, nil
}

// irModule is what lowerIRModule made: the report and the IR file.
type irModule struct {
	IRReport
	out *irFile
}

// lowerIRModule lowers u with IR bodies: the module's files without an IR
// file, the std origins of its names (the split's), and the IR.
func lowerIRModule(u Unit, tests *testLowering, formsOff bool) ([]File, map[string]*check.Package, irModule, *lowerer, error) {
	if err := u.Validate(); err != nil {
		return nil, nil, irModule{}, nil, err
	}
	l := newBuildLowerer(u, tests)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, formsOff: formsOff}
	code, err := l.program()
	if err != nil {
		return nil, nil, irModule{}, nil, err
	}
	if err := irNoForms(&l.ir.out); err != nil { // ir_forms_erase.go
		return nil, nil, irModule{}, nil, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	for _, decl := range slices.Sorted(maps.Keys(l.ir.keep)) {
		code = append(code, []byte("\n"+decl+" // an import an IR body names\n")...)
	}
	files := l.withRecord(u, append(moduleFiles(u.Go, code), l.assetFiles()...))
	sort.Strings(l.ir.report.IR)
	sortFormSites(l.ir.report.FormSites) // ir_forms_report.go
	return files, l.stdOrigins(), irModule{IRReport: l.ir.report, out: &l.ir.out}, l, nil
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
	// funcApplied counts the rewrites over functions the IR bodies
	// applied, by level (ir_rewrite.go).
	funcApplied map[string]int
	// formsOff builds no IR form (LowerIRReference; ir_forms.go).
	formsOff bool
	ops      *opFacts // the capability bindings (ir_forms_op.go)
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
	census, formsOff := st.census, st.formsOff
	if census != nil {
		census = &IRCensus{}
	}
	*st = irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, census: census, reach: reach, formsOff: formsOff}
	return st
}

// The IR format; the derivative's cmd/compile/internal/ergoir reads the same shape.
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
	// Y is a "count" loop's bound (ir_pkg.go countLoop), a store's
	// value: X = Y, X a field (ir_with.go), or a "mapget"'s key (ir_get.go).
	Y *irExpr `json:"y,omitempty"`
	// Ls are a "tdef"'s locals, one per result of its Go call, in order:
	// l0, l1, … := X (ir_tuple.go).
	Ls []int `json:"ls,omitempty"`
	// F is a "match" form's facts, in memory only (ir_forms.go).
	F *irMatch `json:"-"`
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
	// Params, Body and End are a function literal's ("func",
	// ir_closure.go): its parameters' locals, its statements, its brace.
	Params []int     `json:"params,omitempty"`
	Body   []*irStmt `json:"body,omitempty"`
	End    *irPos    `json:"end,omitempty"`
	// F is a "variant" form's facts, in memory only (ir_forms.go).
	F *irVariant `json:"-"`
	// C is a pure call's facts, in memory only (ir_merge.go).
	C *irCall `json:"-"`
	// CF are a call's facts for the rewrite passes, in memory only
	// (ir_rewrite_pass.go): the source call it lowers and whether it is
	// kept as written. (I-FLIP: LF6w's C, renamed beside LF3's C.)
	CF *irCallFacts `json:"-"`
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
	// memos are the loops around the statement being built whose
	// pre-headers take memoized calls, innermost last; memoSites counts
	// the body's memoized calls (ir_memo.go).
	memos     []*irMemo
	memoSites int
	// mergeSites counts the body's calls the merge pass replaced
	// (ir_merge.go).
	mergeSites int
	// foldSites counts the body's calls the fold pass replaced
	// (ir_fold.go).
	foldSites int
	// forms are the body's form sites (ir_forms_report.go), kept with its
	// IR body.
	forms []FormSite
	// tries counts the try expressions being built (ir_fail.go): inside
	// one, a fallible call fails the frame too.
	tries int
	// rawErr is the Go call whose error a plain catch takes unread
	// (catch_plain.go; ir_fail.go catch): bind leaves it Go's error, not
	// go.Error (GOERR-BOX).
	rawErr *syntax.CallExpr
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
	// law is the law whose body is built (ir_assert.go), nil in any
	// other body and inside a function literal: each value it returns is
	// checked where it is returned.
	law *check.Law
	// keep is the depth of AsWritten call trees, kept as written
	// (ir_rewrite.go). lawArgs are a law's parameters bound to the site's
	// operands while a side is lowered; lawDepth counts the sides being
	// lowered, lawKeep those a check reads (a canary's or a shadow's),
	// which the rewrite pass leaves as they are (ir_rewrite_pass.go).
	keep     int
	lawArgs  map[*check.Var]*irExpr
	lawDepth int
	lawKeep  int
	// rwPatterns are the left sides of the laws in the rewrite's scope as
	// IR patterns, built once per body; localTypes the Go type of each
	// local declared (ir_rewrite_pass.go).
	rwPatterns map[string][]*rwPattern
	localTypes map[int]string
	// rwRows are the rewrite sites this body applied (their report rows),
	// rwLevels the rewrites applied by level (ir_rewrite.go).
	rwRows   []string
	rwLevels map[string]int
	own      *irOwn // the static frame of owned values (ir_own.go), or nil
	// pending collects the statements an operand inside an expression
	// runs first (ir_order.go); nil where no value collects them.
	pending *[]*irStmt
	// dropsErr marks the calls whose Go error the site drops: a row
	// empty there (ir_call.go, dropErr).
	dropsErr map[*irExpr]bool
	// voidTail marks the branches of a body with no value whose last
	// statement ends the body: a tail of an error value fails the frame
	// (ir_fail.go bodyStmts, ir_stmt.go tailTo).
	voidTail bool
	// inPlaceTail is the last value of the in-place literal being built,
	// s or (x, s) (ir_refown.go): its return gives x alone, or nothing.
	inPlaceTail syntax.Expr
	// at, when set, is the position of every node built: a view's body
	// built at its match site takes the site's (ir_view.go).
	at *irPos
	// params are the parameters' locals; hoisted and entry are the fields
	// of record parameters read once at entry (ir_hoist.go).
	params  map[int]bool
	hoisted map[hoistKey]int
	entry   []*irStmt
	// folds are the collects whose slice is only folded, by their collect
	// loop and their fold loop (ir_collectfold.go).
	folds map[*syntax.RangeStmt]*foldPlan
	ops   *irOpBody // the body's known capability providers (ir_forms_op.go)
	// held maps the held local of an in-place owning literal to its
	// pointer parameter's local: the local is *p (ir_ownlit.go).
	held map[*check.Var]int
	// frame is 1 + the local of the body's frame scope (ir_own.go
	// openScope), or 0; frameArg reads it (GP-FRAME; OPC-TASK's hook).
	frame int
	// exits marks a frame in the static exit form (ir_exits.go); exitsFC
	// is its own function's context, exitRegs its registrations, and
	// exitsSlots marks one made off that function or in a loop.
	exits      bool
	exitsFC    *funcCtx
	exitRegs   []irExitReg
	exitsSlots bool
	consts     irConsts // each IR constant's value (ir_constfold.go)
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
// got one. The caller has written fd's Go head. drop is set for fd's
// variant with no result (drop.go), which the census names by its own Go
// name (a gate keeps it a Go body).
func (st *irState) body(fc *funcCtx, fd *funcDecl, params []*check.Var, gates []string, drop bool) (ok bool) {
	f := fd.fn
	b := &irBuilder{fc: fc, l: fc.l, st: st, locals: map[*check.Var]int{}, env: -1, result: fc.frame.result}
	name := fd.name
	if drop {
		name = fd.dropped
	}
	name = b.ownedBodyName(fd, name, drop) // ir_own.go
	if n := b.knownVariantName(fd); n != "" {
		name = n // a known-callback variant (ir_kcb.go)
	}
	if fd.method != nil {
		name = fd.method.name + "." + name
	}
	if n := b.copyName(f); n != "" {
		name = n // a copy for a known record (ir_known.go)
	}
	if n := b.monoName(f); n != "" {
		name = n // a monomorphic copy (mono.go; ir_golib.go)
	}
	pkg := ""
	if f.Pkg != nil {
		pkg = f.Pkg.Path
	}
	census := name
	for _, g := range gates {
		switch g {
		case "variant with no result", "copy for an instance":
			// A variant is a body of its own beside the function's: the
			// census names it apart, so the function's IR body and the
			// variant's fallback are two records, not one function with
			// both (IR-RED-2).
			census = name + " (" + g + ")"
		}
	}
	defer st.finish(b, pkg, census, f.Decl, f.Pkg.Std(), &ok)
	// A library body (a std body the program reaches, written with no
	// //line directive) takes an IR body as a sourced one does: its
	// positions name its std file (pos), so its lines and its defects'
	// locations are its .ergo source's (G5).
	for _, g := range gates {
		b.gate(g)
	}
	if len(fc.witness) > 0 {
		b.gate("witness parameter")
	}
	if b.l.hasGoRow(f.Sig) {
		b.gate("Go row parameter") // rowslot.go: F is a Go type parameter the IR spells error (LY-FIX)
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
	b.ops = &irOpBody{f: f, env: b.env} // ir_forms_op.go
	b.fn = &irFunc{Name: name, Pos: b.pos(f.Decl, f.Decl.Pos()), End: b.pos(f.Decl, f.Decl.Body.Rbrace)}
	b.names = b.tempNames(f.Decl.Body) // ir_fail.go
	b.params = map[int]bool{}          // ir_hoist.go: the parameters' locals, before openOwn's
	for _, l := range b.locals {
		b.params[l] = true
	}
	b.part(func() { b.fn.Body = append(b.fn.Body, b.openOwn(f, params)...) }) // ir_own.go
	// A known variant has no entry poll (known.go): fc.plain marks one.
	if b.env >= 0 && frameFootprint(f.Pkg.Info, f.Decl).EntryPoll && !b.l.knownClosed(f) && fc.frame.polls && fc.plain == nil {
		b.fn.Body = append(b.fn.Body, b.poll(f.Decl))
	}
	n := len(b.fn.Body)
	b.fn.Body = append(b.fn.Body, b.bodyStmts(f.Decl.Body.List)...) // ir_fail.go
	b.fn.Body = b.rewritePass(b.fn.Body)                            // ir_rewrite_pass.go (LF6w)
	b.fn.Body = b.foldCalls(b.fn.Body)                              // ir_fold.go (LF3)
	b.fn.Body = b.mergeCalls(b.fn.Body)                             // ir_merge.go (LF3)
	b.fn.Body = eraseForms(b.fn.Body, b.st.sawForm)                 // ir_forms_erase.go
	b.part(b.ownVisited)                                            // ir_own.go
	b.fn.Body = b.withEntry(b.fn.Body, n)                           // ir_hoist.go
	b.fn.Body = append(b.fn.Body, b.bodyEnd(b.fn.Body, b.fn.End)...)
	b.part(func() { b.fn.Body = b.closeMasked(f, b.fn.Body) }) // ir_own.go
	b.fn.Body = b.deferExits(b.fn.Body)                        // ir_exits.go (STATIC-EXIT2)
	b.fn.Body = irScopesIncrease(irUseLocals(b.fn.Body))       // ir_end.go
	if st.census != nil {
		return false
	}
	b.ownDone(name) // ir_own.go
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	st.report.Memo += b.memoSites
	st.report.Merge += b.mergeSites
	st.report.Fold += b.foldSites
	st.report.FormSites = append(st.report.FormSites, b.forms...) // ir_forms_report.go
	return true
}

// finish ends a body's build, deferred: a reason the builder stopped at
// is its fallback, and a census records the body with every reason.
func (st *irState) finish(b *irBuilder, pkg, name string, decl syntax.Node, std bool, ok *bool) {
	if r := recover(); r != nil {
		u, isNo := r.(irUnsupported)
		if !isNo {
			panic(r)
		}
		st.report.Fallback[u.reason]++
		b.note(u.reason)
		*ok = false
		st.rewritesDone(b, false, u.reason) // ir_rewrite.go
	} else if *ok {
		st.rewritesDone(b, true, "")
	}
	if st.census != nil {
		file := ""
		if _, sourced := b.l.sourceOf(decl); sourced {
			_, file, _, _ = b.l.at(decl, decl.Pos())
		}
		st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: pkg, Name: name, File: file, Std: std, Reasons: b.reasons})
		*ok = false
	}
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

// pos is n's position as the IR spells it: its file in the program's
// sources, or, in a std body, its embedded std file (std/<pkg>/<file>,
// the path a std body's defect names; locate).
func (b *irBuilder) pos(n syntax.Node, p syntax.Pos) irPos {
	if b.at != nil {
		return *b.at
	}
	if _, ok := b.l.sourceOf(n); !ok {
		if _, ok := b.l.stdFileOf(n); !ok {
			irNo("node outside the program's and std's sources")
		}
	}
	path, line, col := b.l.locate(n, p)
	i, ok := b.st.files[path]
	if !ok {
		i = len(b.st.out.Files)
		b.st.files[path] = i
		b.st.out.Files = append(b.st.out.Files, path)
	}
	return irPos{i, line, col}
}
