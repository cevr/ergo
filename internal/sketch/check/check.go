package check

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Importer resolves an import path to a checked package (Go types2's seam).
type Importer interface {
	Import(path string) (*Package, error)
}

// Diagnostic is a located checker diagnostic. Offset and Length are a UTF-8
// byte span into the source of the file at index File; Line and Column are
// 1-based, and Column counts bytes.
type Diagnostic struct {
	Code    string
	Message string
	File    int
	Offset  int
	Length  int
	Line    int
	Column  int
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%d:%d: %s %s", d.Line, d.Column, d.Code, d.Message)
}

// Check checks the parsed files of one package with the import path path.
// It returns the package and every diagnostic, sorted by file and position.
// A nil importer resolves the standard library only.
func Check(path string, files []*syntax.File, imp Importer) (*Package, []Diagnostic) {
	if imp == nil {
		imp = Std
	}
	c := newChecker(path, imp, prelude().scope)
	pkg := c.run(files)
	return pkg, c.sorted()
}

// CheckSource parses and checks one source text as a package.
func CheckSource(src string) (*Package, []Diagnostic) {
	file, err := syntax.Parse(src)
	if err != nil {
		var e *syntax.Error
		if se, ok := err.(*syntax.Error); ok {
			e = se
			return nil, []Diagnostic{{Code: e.Code, Message: e.Message, Offset: e.Offset, Length: e.Length, Line: e.Line, Column: e.Column}}
		}
		return nil, []Diagnostic{{Code: CodeSyntax, Message: err.Error()}}
	}
	return Check("main", []*syntax.File{file}, nil)
}

type checker struct {
	pkg      *Package
	imp      Importer
	universe *Scope // the universe and the prelude
	files    []*fileInfo
	diags    []Diagnostic
	seen     map[string]bool
	silent   int
	// refusedBinders holds the binders the reporting pass has already
	// refused by name (a pattern name that names a package object, a
	// prelude name), so the binder rules add no second diagnostic (D3).
	refusedBinders map[binderKey]bool
	// aliases are the resolved `import name path` specs, read by
	// checkImportAliases once every package-level name is declared (D-D7).
	aliases []importAlias
	// writeTarget is the assignment target lhs is typing, so a Map entry
	// there is a write (admitted) and anywhere else a read (S108; D2).
	writeTarget syntax.Expr
	// refusedMembers holds the class G members the reporting pass refused,
	// with which of their inferred rows (errors, capabilities) the body
	// exceeded, so a reference from a later component has those rows
	// unknown and the others as they are (groupRef, unknownSig).
	refusedMembers map[*Func][2]bool
	// dupNames holds the package names declared more than once (declare):
	// which declaration is kept depends on the order, so a decision that
	// reads the kept one is a follow-on of the duplicate (collectSelfFuncs).
	dupNames map[string]bool

	cur *fileInfo
	// instance caches the instances of generic named types by their
	// interned id: (origin, type argument ids), looked up before building.
	instance map[int32]*Named
	// inferInstances and inferSubjects hold the instances and switch
	// subjects whose ids depend on an unbound row variable: the current
	// component's inference context, discarded when it is final (class F).
	inferInstances map[int32]*Named
	inferSubjects  map[any]*switchUnion
	// settled marks the functions of a component that converged, whose
	// reporting pass reads the published solutions; widened records a
	// reporting pass that computed a wider row (an invariant the tests
	// assert).
	settled map[*Func]bool
	widened []string
	// refs holds each body's lexical references to package functions and
	// vars (refs.go, class H): the body graph's edges. valueRefs holds each
	// body's class G value uses, also lexical (refWalker).
	refs      map[any]map[any]bool
	valueRefs map[any]map[*Func][2]useSite
	// Class G (rows.go pinGroup): group holds the functions of the
	// component being checked. groupValue records which rows (errors,
	// capabilities) of each member with an inferred row have a value use
	// inside the component; such a row is pinned empty, and grew records
	// which of them its body exceeds.
	group      map[*Func]bool
	groupValue map[*Func][2]groupSite
	lits       []*litFrame // the literal frames being checked, innermost last
	// walkOps counts the reference walk's literal elements, an operation
	// count for the linear-growth tests (round 17 M2).
	walkOps int
	// wires meters the wire summaries' work against the package's budget
	// (wiresum.go; nil when the package has no wire).
	wires *wireMeter
	// wiresDone is set once wireSummaries has run.
	wiresDone bool
	// wireLimit, set only by tests, replaces the budget of a package of
	// the given wire size.
	wireLimit func(size int) int
	// wireComp maps each named wire to its embedding component (wireSummaries).
	wireComp map[*Wire]*wireComp
	// wireBodies holds each wire body's summary, decided once (wireSummaries,
	// finished by bodySummary).
	wireBodies map[*syntax.WireBody]*bodySum
	// wireEntries holds the Info record of each named wire's provider
	// entry, made once by the wire pass (wirePass.own) and filled in when
	// the wire's body is checked (wireBody).
	wireEntries map[*syntax.WireEntry]*WireEntry
	// nodeCheck replaces checkNode in converge for the synthetic oscillation
	// test (nil otherwise); convergeChecks counts converge's checks.
	nodeCheck      func(*bodyNode) bool
	convergeChecks int
	grew           map[*Func][2]bool
	// paramIdents maps a parameter's position to its declaring name, so a
	// body's binding of it is its Defs entry.
	paramIdents map[syntax.Pos]*syntax.Ident
	// asNames holds each as pattern's binding name until finishInfo.
	asNames map[*Pattern]*syntax.Ident
	// literals keeps isLiteral's answer for each operation it has seen.
	literals map[syntax.Expr]bool
	// callee is the selector a call is about to check as its callee: only
	// there does x.f reach a package function on x's type (196m rule 4).
	callee *syntax.SelectorExpr
	// calleeIndex is the index around callee, x.f[…](…), or nil: its
	// brackets are type arguments only when f has bracket type parameters
	// x does not bind (selfCall; round 8 review m1).
	calleeIndex *syntax.IndexExpr
	// selfRes, set while resolveSelfCalls types the bodies with pending
	// calls x.f(…), receives what each such call resolves to: a 196m
	// function, or nil for another member (selector, selfCall).
	selfRes map[*syntax.SelectorExpr]*Func
	// selfFinal, set only by a test, receives the function each x.f(…) of
	// a 196m function resolves to in the reporting pass.
	selfFinal map[*syntax.SelectorExpr]*Func
	kinds     declKinds // solved bracket kinds of the package's generic types
	init      initDeps  // references of each package-level body (initialization order)
	// totalitySteps records each match or irrefutable-pattern proof reported
	// (not silent), in check order: the deterministic cost the tests bound.
	totalitySteps []int
	// usefulSteps records reachability steps (zero for irrefutable patterns).
	usefulSteps []int
	// totalityBudgets records each totality solver's budget (solverFor).
	totalityBudgets []int
	patternCosts    []patternCost // work and allocation receipts at the source boundary
	// bodyEdges counts the edges of the body graph: linear in the source
	// (round-3 M5), and bounded by a test.
	bodyEdges int
	// bodyNodes counts the bodies of the body graph; selfChecks counts the
	// body checks of settling and worklistSteps the bodies and edges the
	// worklists of settling and converge visit (round 8 review M1: linear).
	bodyNodes, selfChecks, worklistSteps int
	// workNodes counts the work of the body checks in syntax nodes: each
	// check of a body adds the body's size (bodySize), in every pass
	// (round 8 review M1: linear in the source, bounded by a test).
	workNodes int
	// settleOnly, set only by a test, is selfSettleOnly for one check.
	settleOnly bool
	// Typing's record of the callback frames it checks (round 8 review M2):
	// frameGen holds, for each function literal, trailing block and with
	// block, the body check that last typed it, frameInv and frameRef the
	// body check that last skipped it as invalid-diagnosed or as
	// refused-by-class-G (provisional.go), and bodyGen each body's latest
	// check (beginBodyCheck). A class G use the walk finds inside a frame
	// counts unless the frame is invalid-diagnosed in the body's latest
	// check (liveUses, reportedUses), so the walk never decides by itself
	// which callee is invalid. guardTrace, set only by a test, receives the
	// verdict liveUses first read for each frame and the check it read
	// (RefWalkProblems).
	frameGen       map[syntax.Node]int
	frameInv       map[syntax.Node]int
	frameRef       map[syntax.Node]int
	bodyGen        map[any]int
	curGen, genSeq int
	guardTrace     map[syntax.Node]frameVerdict
	// pinKept holds, for each body of the component being checked, the
	// class G uses inside callback frames that the pin counted
	// (pinGroup), which reportGroup filters with the reporting pass.
	pinKept map[any][]guardedUse
	// Undiagnosed reads (provisional.go; round 9 review M1): rowsOpen is
	// set while a cyclic component's rows are still being inferred,
	// undiagSeq counts the reads of an undiagnosed value, and stmtMark and
	// callMark are undiagSeq at the start of the current statement and call.
	rowsOpen                      int
	undiagSeq, stmtMark, callMark int
	// use is how the expression use.x, checked next, is used (tails.go,
	// exprAs): a with, try or catch reads it to know whether its value is
	// discarded (D-C1).
	use useAt
	// Row reads (provisional.go readRow; round 11 review): openRowSeq
	// counts the reads of a row that holds an unsolved variable of a
	// member of the component being checked.
	openRowSeq int
	// useGens, set only by a test, receives the body checks in which typing
	// resolved each identifier (RefWalkProblems: an independent witness
	// of which frames a check typed).
	useGens map[*syntax.Ident][]int
	// guardedRefs holds each body's class G uses inside callback frames
	// (refWalker.guarded), beside valueRefs.
	guardedRefs map[any][]guardedUse
	// variantOps counts the variant-index work that resolves type-switch
	// cases: a constructor inserted, a node visited, a type argument
	// compared, a wildcard candidate checked. Linear in the source
	// (round-5 M2, round-7 M4), and bounded by tests.
	variantOps int
	// instancesMade counts the generic instances created: a default-only
	// switch over generic unions creates none (round-6 M5), bounded by a
	// test.
	instancesMade int
	// inferOps counts the constraint-guard work of generic calls: linear
	// in each signature (round-8 M4), bounded by a test.
	inferOps int
	// types is the check's hash-consing table (intern.go): the one identity
	// for comparing, caching and indexing types. vindex holds the variant
	// index of each union this check switches on (variantindex.go).
	// subjects shares one switchUnion per union instance, so its caches are
	// built once per instance, not once per switch subject (round-9 M1).
	types    *typeTable
	vindex   map[*Union]*variantIndex
	subjects map[any]*switchUnion
	mono     monoState // the instantiation graph (instantiation cycles)

	errorTypes map[*Named]syntax.Pos // origin error types named in an error row of this package
	funcs      []*Func               // every package function and method, in source order
	vars       []*pkgVar
	machines   []*TypeName
	guarded    map[*Func]bool // steps of exported machines: rows written (export guard)

	// The reach graph for wire cycles (rows.md, Wire 5).
	reach     map[any]*reachNode
	providers map[*Named][]*Named // capability → provider types bound to it anywhere

	// totalSwitch holds the matches that are total (proved, or reported
	// S103): such a match terminates when every case does (D8).
	totalSwitch map[syntax.Stmt]bool

	// variants holds the record each inline variant `Name{…}` of a union
	// declares (spec D-B1), and synthetic the identifiers the checker made
	// for those records' type parameters: they are not in the tree, so
	// Info.Defs does not list them.
	variants  map[*syntax.VariantSpec]*TypeName
	synthetic map[*syntax.Ident]bool
	// pkgPatterns holds the package-level lets that bind several names
	// from one value (a pattern, or `let q, r = e`), each checked once.
	pkgPatterns map[*syntax.VarSpec]*pkgPattern

	fn *funcCtx // the body being checked
}

type pkgVar struct {
	obj      *Var
	spec     *syntax.VarSpec
	idx      int
	file     *fileInfo
	done     bool // checked with diagnostics
	checking bool // being checked: a self-reference stops here
	// initCycle: the var is on a reported initialization cycle; its type
	// is invalid, so no use of it cascades (D3).
	initCycle bool
	// size is the initializer's syntax nodes (bodySize), once known.
	size int
	// name is the declaring identifier; group is set for a let that binds
	// several names from one value (its first member checks it for all).
	name  *syntax.Ident
	group *pkgPattern
}

// pkgPattern is a package-level let that binds several names from one
// value: `let (a, b) = e`, `let Order{Total: t} = e` or `let q, r = e`.
type pkgPattern struct {
	spec    *syntax.VarSpec
	members []*pkgVar
}

var nextID struct {
	sync.Mutex
	n int
}

func newID() int {
	nextID.Lock()
	defer nextID.Unlock()
	nextID.n++
	return nextID.n
}

func newChecker(path string, imp Importer, universe *Scope) *checker {
	pkg := &Package{Path: path, Scope: newScope(universe), Wires: map[string]*Wire{}, Info: newInfo()}
	c := &checker{
		pkg:            pkg,
		paramIdents:    map[syntax.Pos]*syntax.Ident{},
		imp:            imp,
		universe:       universe,
		seen:           map[string]bool{},
		instance:       map[int32]*Named{},
		types:          newTypeTable(),
		inferInstances: map[int32]*Named{},
		inferSubjects:  map[any]*switchUnion{},
		settled:        map[*Func]bool{},
		errorTypes:     map[*Named]syntax.Pos{},
		guarded:        map[*Func]bool{},
		reach:          map[any]*reachNode{},
		providers:      map[*Named][]*Named{},

		totalSwitch: map[syntax.Stmt]bool{},
		variants:    map[*syntax.VariantSpec]*TypeName{},
		synthetic:   map[*syntax.Ident]bool{},
		pkgPatterns: map[*syntax.VarSpec]*pkgPattern{},

		refusedBinders: map[binderKey]bool{},
		refusedMembers: map[*Func][2]bool{},
		dupNames:       map[string]bool{},
	}
	c.types.substRow = c.substRow
	return c
}

// errorf reports a diagnostic at the node's span in the current file.
func (c *checker) errorf(n syntax.Node, code, format string, args ...any) {
	if n == nil {
		c.errorAt(syntax.NoPos, syntax.NoPos, code, format, args...)
		return
	}
	c.errorAt(n.Pos(), n.End(), code, format, args...)
}

func (c *checker) errorAt(pos, end syntax.Pos, code, format string, args ...any) {
	if c.silent > 0 {
		return
	}
	d := Diagnostic{Code: code, Message: fmt.Sprintf(format, args...)}
	if c.cur != nil {
		f := c.cur.syntax
		d.File = c.cur.index
		if pos.IsValid() {
			if int(pos) > len(f.Src) {
				pos = syntax.Pos(len(f.Src))
			}
			d.Offset = int(pos)
			d.Line, d.Column = f.Position(pos)
			length := 0
			if end.IsValid() && end > pos {
				length = int(end - pos)
			}
			// A span stays on its first line, so every surface underlines
			// the construct where the diagnostic is reported.
			if nl := strings.IndexByte(f.Src[pos:min(int(pos)+length, len(f.Src))], '\n'); nl >= 0 {
				length = nl
				// A construct that goes on past its first line is cut
				// before a comment that ends that line, and before the
				// blanks in front of it.
				i := sort.Search(len(f.Comments), func(i int) bool { return f.Comments[i].Pos() > pos })
				if i < len(f.Comments) && int(f.Comments[i].Pos()) < int(pos)+length {
					length = int(f.Comments[i].Pos() - pos)
				}
				for length > 0 && (f.Src[int(pos)+length-1] == ' ' || f.Src[int(pos)+length-1] == '\t') {
					length--
				}
			}
			for length > 0 && int(pos)+length < len(f.Src) && !utf8.RuneStart(f.Src[int(pos)+length]) {
				length++
			}
			d.Length = length
		}
	}
	key := fmt.Sprintf("%d:%d:%s:%s", d.File, d.Offset, d.Code, d.Message)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.diags = append(c.diags, d)
}

func (c *checker) sorted() []Diagnostic {
	out := append([]Diagnostic(nil), c.diags...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Offset != b.Offset {
			return a.Offset < b.Offset
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	return out
}

func isExported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// ---------------------------------------------------------------- universe

var universeOnce struct {
	sync.Once
	scope      *Scope
	errorName  *TypeName
	anyType    Type
	comparable Type
	goPkg      *Package
}

// universe is the predeclared block: the basic types, comparable, true,
// false and len, the one built-in function (#196, fourth round, item 2;
// removedBuiltins names the Ergo form of each Go built-in it drops), and
// with, the construct (grammar.md, Reserved names 2).
func universe() *Scope {
	universeOnce.Do(func() {
		s := newScope(nil)
		u := &Package{Path: "", Name: "", Scope: s}
		basic := func(name string, k BasicKind) {
			s.insert(name, &TypeName{Name: name, Pkg: u, Type: Typ[k], id: newID()})
		}
		for _, k := range []BasicKind{Bool, String, Int, Int8, Int16, Int32, Int64, Uint, Uint8, Uint16, Uint32, Uint64, Uintptr, Float32, Float64, Complex64, Complex128, Duration} {
			basic(Typ[k].Name, k)
		}
		basic("byte", Uint8)
		basic("rune", Int32)
		universeOnce.anyType = &Interface{}
		universeOnce.comparable = &Interface{Comparable: true}
		s.insert("any", &TypeName{Name: "any", Pkg: u, Type: universeOnce.anyType, id: newID()})
		s.insert("comparable", &TypeName{Name: "comparable", Pkg: u, Type: universeOnce.comparable, id: newID()})
		errName := &TypeName{Name: "error", Pkg: u, id: newID()}
		errName.Type = &Named{Obj: errName, under: &Interface{}, state: 2}
		universeOnce.errorName = errName
		s.insert("error", errName)
		s.insert("true", &Const{Name: "true", Type: Typ[Bool], Val: true})
		s.insert("false", &Const{Name: "false", Type: Typ[Bool], Val: false})
		for _, name := range []string{"len", "with"} {
			s.insert(name, &Builtin{Name: name})
		}
		// go.Foreign (a capability) and go.Error (an error type) are
		// predeclared for extern go bindings (grammar.md, Declarations 3).
		goPkg := &Package{Path: "go", Name: "go", Scope: newScope(nil), qualify: true}
		foreign := &TypeName{Name: "Foreign", Pkg: goPkg, id: newID()}
		foreign.Type = &Named{Obj: foreign, under: &Interface{}, state: 2, foreign: true}
		goErr := &TypeName{Name: "Error", Pkg: goPkg, id: newID()}
		goErr.Type = &Named{Obj: goErr, under: &Struct{Fields: []*Field{{Name: "Message", Type: Typ[String]}}}, state: 2}
		goPkg.Scope.insert("Foreign", foreign)
		goPkg.Scope.insert("Error", goErr)
		universeOnce.goPkg = goPkg
		s.insert("go", &PkgName{Name: "go", Pkg: goPkg, used: true})
		universeOnce.scope = s
	})
	return universeOnce.scope
}

// ---------------------------------------------------------------- prelude and std

type preludeResult struct {
	scope *Scope
	pkg   *Package
	diags []Diagnostic
}

var preludeOnce struct {
	sync.Once
	r preludeResult
}

// prelude checks the embedded prelude once per process. Its exported
// declarations form the scope above the universe for every other package.
func prelude() preludeResult {
	preludeOnce.Do(func() {
		u := universe()
		var files []*syntax.File
		for _, name := range sortedKeys(std.Prelude()) {
			f, err := syntax.Parse(std.Prelude()[name])
			if err != nil {
				panic("check: the prelude does not parse: " + err.Error())
			}
			files = append(files, f)
		}
		c := newChecker(std.PreludePath, nil, u)
		pkg := c.run(files)
		pkg.qualify = false
		scope := newScope(u)
		coll := map[Object]bool{}
		for _, f := range pkg.collFuncs {
			coll[f] = true
		}
		for name, o := range pkg.Scope.names {
			// A function on slices or Maps is an operation of that type,
			// reached by x.f(…), not a name in scope (D-D8; the decided
			// preview's func Get on a local type keeps its name).
			if isExported(name) && !coll[o] {
				scope.insert(name, o)
			}
		}
		preludeOnce.r = preludeResult{scope: scope, pkg: pkg, diags: c.sorted()}
	})
	return preludeOnce.r
}

// PreludeDiagnostics returns the prelude's own diagnostics (none when the
// embedded source is valid; a test pins it).
func PreludeDiagnostics() []Diagnostic { return prelude().diags }

// stdImporter imports the embedded standard library, checking each package
// once per process.
type stdImporter struct {
	mu   sync.Mutex
	pkgs map[string]*Package
	errs map[string]error
}

// Std is the importer of the embedded standard library (internal/sketch/std).
var Std Importer = &stdImporter{pkgs: map[string]*Package{}, errs: map[string]error{}}

func (s *stdImporter) Import(path string) (*Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pkgs[path]; ok {
		return p, s.errs[path]
	}
	sources, ok := std.Package(path)
	if !ok {
		return nil, fmt.Errorf("no package %q", path)
	}
	var files []*syntax.File
	for _, name := range sortedKeys(sources) {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			return nil, fmt.Errorf("std package %q does not parse: %v", path, err)
		}
		files = append(files, f)
	}
	c := newChecker(path, s, prelude().scope)
	pkg := c.run(files)
	var err error
	if len(c.diags) > 0 {
		err = fmt.Errorf("std package %q has diagnostics: %s", path, c.sorted()[0])
	}
	s.pkgs[path], s.errs[path] = pkg, err
	return pkg, err
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
