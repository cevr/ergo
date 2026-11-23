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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

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
	Fold int `json:"fold,omitempty"`
	// Seed counts the slice literals the IR bodies seed with room
	// (seedcap.go).
	Seed int `json:"seed,omitempty"`
	// StrBuf counts the string locals the IR bodies build in a buffer
	// (strbuf.go).
	StrBuf int    `json:"strbuf,omitempty"`
	Hash   string `json:"hash"`
	// Folds counts the build-time folds of the IR bodies (ir_constfold.go).
	Folds FoldCounts `json:"folds"`
	// CodecText counts the codec functions that keep their text body,
	// by "name\treason": a cell writer the IR builder refused
	// (ir_wire.go), an encoder on the reference Writer and a decoder of
	// a form no IR builder takes yet (the text emitter's). In memory only.
	CodecText map[string]int `json:"-"`
	// CodecRead names the decoders the IR builds (ir_wire_read.go). In
	// memory only.
	CodecRead []string `json:"-"`
	// Forms counts the IR forms the bodies held before erasure, by
	// kind and layout ("match/flat"; ir_forms.go): in memory only.
	Forms map[string]int `json:"-"`
	// FormSites are the decisions of the passes over the IR bodies, by
	// position (ir_forms_report.go): ergo inspect forms.
	FormSites []FormSite `json:"formSites,omitempty"`
	// ConstFolds counts the operations of constants folded into one
	// (ir_constfold.go).
	ConstFolds int `json:"-"`
	// MapRounds is the number of lowerings the int map choice ran
	// (intmap.go; 1 when no Map type conflicts), and MapConflicts counts
	// the Map types each cause made Go's map, over every round.
	MapRounds    int            `json:"-"`
	MapConflicts map[string]int `json:"-"`
	// Inlined counts the calls whose callee's IR body the pass copied to
	// their site (ir_forms_inline.go).
	Inlined int `json:"-"`
	// KnownVariants counts the matches whose union the known-variant pass
	// stopped building (ir_forms_variant.go).
	KnownVariants int `json:"-"`
	// Dropped counts the discarded calls that name the variant with no
	// result (ir_forms_drop.go).
	Dropped int `json:"-"`
	// SeqRanges counts the for-in loops over a Seq that range over Go's
	// range-over-func form (PORT-FORIN, ir_seq.go).
	SeqRanges int `json:"-"`
	// CatchDefects counts the full catches whose default panics with the
	// unreachable defect, naming no failure (ir_catch_defect.go).
	CatchDefects int `json:"-"`
	// StoreFields counts the stored record literals written field by
	// field (ir_forms_store.go).
	StoreFields int `json:"-"`
	// StdInlined counts the calls in library bodies the IR inlined
	// (IR-STDINL, ir_forms_inline.go).
	StdInlined int `json:"-"`
	// DropBodies counts the variants with no result the IR built
	// (IR-DROPBODY, ir_forms_drop.go).
	DropBodies int `json:"-"`
	// FailJumps counts the inlined failures that jump to their catch arm
	// (FAIL-INLINE, ir_forms_failjump.go).
	FailJumps int `json:"-"`
	// InlinedReturns counts the inlined calls in return position, whose
	// copy's returns and failures are the caller's own (INLINE-RETURN,
	// ir_forms_inline.go); Inlined counts them too.
	InlinedReturns int `json:"-"`
	// TailPairs counts the returns of a fallible call's value and error
	// in an inlined fallible body, bound at the site's value and error
	// (INLINE-RETURN, ir_forms_inline.go irRets).
	TailPairs int `json:"-"`
	// FailLocalErrors and FailLocalValues count the failures of inlined
	// fallible bodies that are a local or a parameter, set as the site's
	// error: one of type error as it is (a try's error passed on), one of
	// a failure type converted (INLINE-FAIL-LOCAL, ir_forms_inline.go
	// irRets.failValue).
	FailLocalErrors int `json:"-"`
	FailLocalValues int `json:"-"`
	// FailArgs counts the parameters used only as a failure that stand
	// for their variant-literal argument in a copy, so the failure is
	// built in place (INLINE-FAILARG, ir_forms_inline.go).
	FailArgs int `json:"-"`
	// FailThrough counts the failures built in place that a copy's try
	// passes on as they are, not through its error local
	// (INLINE-FAILARG, ir_forms_failjump.go failThrough).
	FailThrough int `json:"-"`
	// InlinedSlots counts the inlined calls of functions with a Slot row,
	// bound at the call (INLINE-SLOT, rowslot.go).
	InlinedSlots int `json:"-"`
	// FailFields counts the failures of inlined fallible bodies that are
	// a field of a local, set as the site's error (INLINE-SLOT,
	// ir_forms_inline.go irFailField).
	FailFields int `json:"-"`
	// FailTyped counts the inlined failures read from a local or a field
	// that jump to their arm by their struct type (FAILJUMP-TYPED,
	// ir_forms_failjump.go).
	FailTyped int `json:"-"`
	// CatchTreeDefects counts the full catches with patterns whose tree's
	// exit panics with the unreachable defect (FAIL-ELIDE-PAT, ir_fail.go).
	CatchTreeDefects int `json:"-"`
	// FailLeaves counts the fallible leaves copied only for their
	// failures' jumps (FAIL-INLINE, ir_forms_inline.go).
	FailLeaves int `json:"-"`
	// DeadZeros counts the zero value sets dropped before an overwrite
	// (ir_forms_zero.go).
	DeadZeros int `json:"-"`
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
	return lowerIR(u, testLoweringOf(u))
}

// IRCensus is every reason the IR builder cannot lower each function of a
// lowering, not only the first: the work list of the IR coverage units.
type IRCensus struct {
	Funcs []IRCensusFunc `json:"funcs"`
}

// IRCensusFunc is one lowered body. Reasons are distinct, in the order the
// builder met them; none means the builder lowers it.
type IRCensusFunc struct {
	Pkg  string `json:"pkg"`
	Name string `json:"name"`
	File string `json:"file,omitempty"` // the source file, for a sourced body
	Std  bool   `json:"std,omitempty"`
	// Codec marks a codec function that keeps its text body (codecText):
	// no Ergo body, Name its Go name.
	Codec   bool     `json:"codec,omitempty"`
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
		tl = testLoweringOf(u)
	}
	l := newBuildLowerer(u, tl)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, census: &IRCensus{}, reach: reach}
	if _, err := l.program(); err != nil {
		return IRCensus{}, err
	}
	return *l.ir.census, nil
}

// reachAll queues a census's extra bodies (CensusStd, and
// LowerIRSplitReaching's); program calls it once the package's own
// declarations are lowered.
func (st *irState) reachAll(l *lowerer) {
	st.reached = append(st.reached, st.reach...)
	for _, f := range st.reach {
		func() {
			defer func() {
				if r := recover(); r != nil {
					if st.census == nil {
						st.report.Fallback["std body not reachable alone"]++
						return
					}
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
		tl = testLoweringOf(u)
	}
	return lowerIRForms(u, tl, true)
}

func lowerIRForms(u Unit, tests *testLowering, formsOff bool) ([]File, IRReport, error) {
	files, _, report, _, err := lowerIRModule(u, tests, formsOff, false)
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
	return irKeyed(files), report.IRReport, nil
}

// irKeyed is files with the digest of each IR file in a comment at the end
// of a Go file of its package. go build keys a package's compile on its Go
// files and flags and never reads the file -ergoir names, so its flag
// names the IR by a path relative to the module (build.Toolchain.Command),
// the same in every module an edit writes, and the IR's content reaches
// the key through the Go file. An absolute path changed with the module's
// folder, so go build compiled every package of a split program again on
// each edit, each unchanged (GP-SPLIT-EDIT).
func irKeyed(files []File) []File {
	out := slices.Clone(files)
	for _, ir := range files {
		if path.Base(ir.Path) != IRFileName {
			continue
		}
		dir := path.Dir(ir.Path)
		at := slices.IndexFunc(out, func(f File) bool {
			return path.Dir(f.Path) == dir && strings.HasSuffix(f.Path, ".go") && !strings.HasSuffix(f.Path, "_test.go")
		})
		if at < 0 {
			continue
		}
		sum := sha256.Sum256(ir.Data)
		out[at].Data = slices.Concat(out[at].Data, []byte("\n// "+IRFileName+" sha256 "+hex.EncodeToString(sum[:])+"\n"))
	}
	return out
}

// irUnkeyed is data without the digest line irKeyed appended: the Go
// text (IRGoText) holds the IR bodies and no IR file, so the line would
// name a file the text does not have, and the linked packages' checked-in
// text would change with every IR edit's digest.
func irUnkeyed(data []byte) []byte {
	const tail = len("\n// "+IRFileName+" sha256 ") + 2*sha256.Size + 1
	if len(data) < tail {
		return data
	}
	line := data[len(data)-tail:]
	if !bytes.HasPrefix(line, []byte("\n// "+IRFileName+" sha256 ")) || line[tail-1] != '\n' {
		return data
	}
	return data[:len(data)-tail]
}

// LowerIRSplit is LowerIR (LowerIRTests when tests is set) with the
// package split (LowerSplit): one Go package per Ergo package and std
// package, each with the IR file of its own bodies (pkgsplit_ir.go). A
// program the split does not split keeps LowerIR's one package and its
// one IR file, with the reason in the report.
func LowerIRSplit(u Unit, tests bool) ([]File, IRReport, SplitReport, error) {
	return lowerIRSplit(u, tests, programModule, nil)
}

// LowerIRShards is LowerIRSplit with declaration shards (pkgshard.go).
func LowerIRShards(u Unit, tests bool) ([]File, IRReport, SplitReport, error) {
	return lowerIRSplit(u, tests, programModule, devShards)
}

// splitFormatLowering selects the split's main.go: "" (the lowering's own
// text, which the split parses once) or "format" (go/format's, the
// reference before SPLIT-AST), set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.splitFormatLowering=format.
// TestSplitTextKeepsPositions compares the two.
var splitFormatLowering string

// LowerIRSplitReaching is LowerIRSplit (of the test build with tests) with
// every function of funcs lowered too, as a library body within u that
// its program need not call (CensusStd's reach), each with the IR body it
// gets. The agreement of the self-hosted IR builder (internal/selfhostcheck,
// TestCompilerIRAgreesWithBuilder) compares every function it builds,
// reached by a program or not. A function the lowering cannot reach alone
// is a fallback "std body not reachable alone".
func LowerIRSplitReaching(u Unit, tests bool, funcs []*check.Func) ([]File, IRReport, SplitReport, error) {
	return lowerIRSplitReaching(u, tests, programModule, nil, funcs)
}

func lowerIRSplit(u Unit, tests bool, base string, sh *shardOpts) ([]File, IRReport, SplitReport, error) {
	return lowerIRSplitReaching(u, tests, base, sh, nil)
}

func lowerIRSplitReaching(u Unit, tests bool, base string, sh *shardOpts, reach []*check.Func) ([]File, IRReport, SplitReport, error) {
	var tl *testLowering
	if tests {
		tl = testLoweringOf(u)
	}
	files, origins, report, l, err := lowerIRModuleReaching(u, tl, false, splitFormatLowering != "format", reach)
	if err != nil {
		return nil, IRReport{}, SplitReport{}, err
	}
	i := slices.IndexFunc(files, func(f File) bool { return isMainGo(f.Path) })
	var out []File
	rep := SplitReport{Reason: "no main.go"}
	if i >= 0 {
		out, rep = splitMain(u, files[i].Data, base, origins, l.harnessNames(), &l.ir.out, sh)
		if rep.err != nil {
			return nil, IRReport{}, SplitReport{}, rep.err
		}
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
		if i >= 0 && l.unformattedText != nil {
			// The one package's main.go is go/format's, as LowerIR's.
			if res[i].Data, err = l.formatMain(); err != nil {
				return nil, IRReport{}, SplitReport{}, err
			}
		}
	} else {
		res = append(slices.Clone(files[:i]), out...)
		res = append(res, files[i+1:]...)
	}
	res = irKeyed(res)
	report.Hash = irHash(res)
	return res, report.IRReport, rep, nil
}

// withKeeps is main.go's code with the declarations that keep the imports
// IR bodies name used.
func (l *lowerer) withKeeps(code []byte) []byte {
	for _, decl := range slices.Sorted(maps.Keys(l.ir.keep)) {
		code = append(code, []byte("\n"+decl+" // an import an IR body names\n")...)
	}
	return code
}

// formatMain is main.go as an unsplit lowering writes it, go/format's,
// from the unformatted lowering's text (lowerer.unformatted): a refused
// split's one package.
func (l *lowerer) formatMain() ([]byte, error) {
	l.formats++
	formatted, err := format.Source(l.unformattedText)
	if err != nil {
		return nil, fmt.Errorf("lower: the emitted Go does not parse (a compiler defect): %w", err)
	}
	return l.withKeeps(anchorLines(formatted)), nil
}

// irModule is what lowerIRModule made: the report and the IR file.
type irModule struct {
	IRReport
	out *irFile
}

// lowerIRTestParts is lowerTestParts with the IR bodies, as LowerIRTests
// lowers them: the test build's main.go, its assets and its IR file, for
// the Go package in the module's folder dir (TEST-one). The compile reads
// an IR source path relative to its working directory, the module's
// folder, where a //line name is read from main.go's folder: so each path
// is written under dir, and a frame of an IR body names the file a frame
// of the same function's source body names (runtime/v1, sourceDir).
func lowerIRTestParts(u Unit, dir string) ([]byte, []File, []byte, error) {
	files, _, report, l, err := lowerIRModule(u, testLoweringOf(u), false, false)
	if err != nil {
		return nil, nil, nil, err
	}
	i := slices.IndexFunc(files, func(f File) bool { return isMainGo(f.Path) })
	if i < 0 {
		return nil, nil, nil, fmt.Errorf("lower: an IR test build has no main.go (a compiler defect)")
	}
	for i, f := range report.out.Files {
		if !path.IsAbs(f) {
			report.out.Files[i] = path.Join(dir, f)
		}
	}
	data, err := json.Marshal(report.out)
	if err != nil {
		return nil, nil, nil, err
	}
	return files[i].Data, l.assetFiles(), data, nil
}

// lowerIRModule lowers u with IR bodies: the module's files without an IR
// file, the std origins of its names (the split's), and the IR.
func lowerIRModule(u Unit, tests *testLowering, formsOff, unformatted bool) ([]File, map[string]*check.Package, irModule, *lowerer, error) {
	return lowerIRModuleReaching(u, tests, formsOff, unformatted, nil)
}

// lowerIRModuleReaching is lowerIRModule with reach's bodies lowered too
// (LowerIRSplitReaching).
func lowerIRModuleReaching(u Unit, tests *testLowering, formsOff, unformatted bool, reach []*check.Func) ([]File, map[string]*check.Package, irModule, *lowerer, error) {
	if err := u.Validate(); err != nil {
		return nil, nil, irModule{}, nil, err
	}
	l, code, err := lowerIRRounds(u, tests, formsOff, unformatted, reach)
	if err != nil {
		return nil, nil, irModule{}, nil, err
	}
	// The last round's bodies alone: an earlier round's fallbacks were
	// the conflicts it lowered again for (intmap.go).
	if err := l.ir.refuseFallbacks(); err != nil { // ir_fallback.go
		return nil, nil, irModule{}, nil, err
	}
	if err := irNoForms(&l.ir.out); err != nil { // ir_forms_erase.go
		return nil, nil, irModule{}, nil, err
	}
	if err := irPlaced(&l.ir.out); err != nil { // ir_placed.go
		return nil, nil, irModule{}, nil, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	files := l.withRecord(u, append(moduleFiles(u.Go, l.withKeeps(code)), l.assetFiles()...))
	sort.Strings(l.ir.report.IR)
	sortFormSites(l.ir.report.FormSites) // ir_forms_report.go
	return files, l.stdOrigins(), irModule{IRReport: l.ir.report, out: &l.ir.out}, l, nil
}

// lowerIRRounds is the IR lowering of u in rounds: a round whose int maps
// meet a conflict lowers the whole program again with those maps Go's
// (intmap.go). Each round starts from nothing but the unit and the maps'
// facts: its own lowerer, and a fresh record of the test helpers it
// emitted (testLowering.fresh), so the last round's output is what one
// round with the last round's facts emits (LAW-GEN-HELPERS: a record
// shared across rounds left the last round calling generators only an
// earlier, discarded round had written).
func lowerIRRounds(u Unit, tests *testLowering, formsOff, unformatted bool, reach []*check.Func) (*lowerer, []byte, error) {
	var (
		l    *lowerer
		code []byte
		err  error
		ims  intMapState
		why  = map[string]int{}
	)
	for round := 0; ; round++ {
		if round == intMapRounds {
			ims = intMapState{allGo: true}
		}
		l, code, err = lowerIRRound(u, tests.fresh(), formsOff, unformatted, ims, reach)
		if err != nil {
			return nil, nil, err
		}
		for k, n := range l.ir.maps.why {
			why[k] += n
		}
		next, again := l.ir.maps.newRound() // intmap.go: a conflict lowers again
		if !again || round == intMapRounds {
			l.ir.report.MapRounds, l.ir.report.MapConflicts = round+1, why
			return l, code, nil
		}
		ims = next
	}
}

// lowerIRRound is one round of lowerIRRounds, its int maps decided by ims.
func lowerIRRound(u Unit, tests *testLowering, formsOff, unformatted bool, ims intMapState, reach []*check.Func) (*lowerer, []byte, error) {
	l := newBuildLowerer(u, tests)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, formsOff: formsOff, noFallback: u.NoFallback, maps: ims, reach: reach}
	l.unformatted = unformatted
	code, err := l.program()
	return l, code, err
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
	ops      *opFacts    // the capability bindings (ir_forms_op.go)
	inl      *irInline   // the inliner's marks and frames (ir_forms_inline.go)
	maps     intMapState // MI-2: the int map choice (intmap.go)
	// noFallback is Unit.NoFallback; fallbacks are the bodies the
	// builder stopped at, with their reasons (ir_fallback.go).
	noFallback bool
	fallbacks  []IRCensusFunc
	// opFuncs are the implementing functions of closed types' operations
	// (ir_instance.go staticOpFunc), filled on first use.
	opFuncs map[opFuncKey]*check.WitnessOp
	// totals are the functions decided total so far (ir_sink.go).
	totals map[*check.Func]bool
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
	census, formsOff, noFallback := st.census, st.formsOff, st.noFallback
	if census != nil {
		census = &IRCensus{}
	}
	// The round's int map choice stays (its off types and allGo): the
	// run's conflicts are dropped with its bodies (intmap.go).
	maps := intMapState{off: st.maps.off, allGo: st.maps.allGo}
	*st = irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}, census: census, reach: reach, formsOff: formsOff, noFallback: noFallback, maps: maps}
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
	// FailT is the Go type of a "fail"'s failure (or of the "set" of a
	// copy's error that a fail became) when it is a struct, in memory
	// only: a failure read from a local or a field then jumps to the arm
	// of that type as one built in place does (FAILJUMP-TYPED,
	// ir_forms_failjump.go). FailTP is the Slot row whose Go type the
	// failure is, set by a copy that binds it to a struct.
	FailT  string           `json:"-"`
	FailTP *check.TypeParam `json:"-"`
	// F is a "match" form's facts, in memory only (ir_forms.go).
	F *irMatch `json:"-"`
	// Was is the facts of the match form this switch was erased from, in
	// memory only (ir_forms_variant.go); an arm's Bind is only whether
	// its case's body starts with the binding.
	Was *irMatch `json:"-"`
	// Pair marks a "ret" of a fallible Ergo call's value and error as
	// they are (ir_fail.go retOf), in memory only: an inlined copy binds
	// both at its site (ir_forms_inline.go irRets).
	Pair bool `json:"-"`
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
	// Was is the facts of the variant form this expression was erased
	// from, in memory only, for the known-variant pass that runs after
	// erasure (ir_forms_variant.go).
	Was *irVariant `json:"-"`
	// FT are a record literal's field Go types, one per element, in memory
	// only: set when the literal names every field of an Ergo record
	// (ir_expr.go), for the store pass (ir_forms_store.go).
	FT []string `json:"-"`
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

// newBuilder is the one way to start an IR body. root is the declaration
// whose source statements the body lowers (a function, a test, a law),
// and the value rule must have analyzed it first (analyzeValues): the
// builder reads the rule's copies as facts (valueCopy, copyOf), and a body
// built before them shares what the rule copies, silently (KMEANS-LAW: a
// law's IR body was built before its analysis, so `let mut y = x` shared
// x and a write through y reached x). A body built from an unanalyzed root
// stops the lowering here, so no path can get the order wrong. root is nil
// only for a body with no source statements of its own: a machine value's
// adapter, a codec cell, an owned wrapper and the release helpers of an
// analyzed body.
func (st *irState) newBuilder(fc *funcCtx, root syntax.Node) *irBuilder {
	if root != nil && !isNilNode(root) {
		if _, ok := fc.l.valueFacts().roots[root]; !ok {
			unreachable(root, ruleValuesFirst, "the IR body of %T built before the value rule analyzed it", root)
		}
	}
	return &irBuilder{fc: fc, l: fc.l, st: st, locals: map[*check.Var]int{}, env: -1}
}

// irBuilder builds one function's IR body.
type irBuilder struct {
	fc     *funcCtx
	l      *lowerer
	st     *irState
	fn     *irFunc
	locals map[*check.Var]int
	// letInits are the values the body's lets bound to names that keep
	// their value, for unfoldAt (ir_unfold.go).
	letInits map[*check.Var]syntax.Expr
	next     int
	slots    int                      // the Go parameter slots, witnesses too (ir_forms_inline.go)
	env      int                      // the environment's local, or -1 in a pure function
	wits     map[*check.TypeParam]int // each witness parameter's local (ir_witness.go)
	// raw is the operand whose implicit conversion is not applied: a
	// direct reader passed as itself (directDecode, ir_codec_direct.go).
	raw    syntax.Expr
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
	// seedSites counts the body's seeded slice literals (seedcap.go).
	seedSites int
	// foldSites counts the body's calls the fold pass replaced
	// (ir_fold.go).
	foldSites int
	// forms are the body's form sites (ir_forms_report.go), kept with its
	// IR body.
	forms []FormSite
	// amort is the counter of the quiet loop nest being built, nil
	// outside one; pollSites are the polls of each loop being built,
	// innermost last: its back edge's and its continues' (ir_poll.go).
	amort     *pollAmort
	pollSites [][]*irStmt
	// tries counts the try expressions being built (ir_fail.go): inside
	// one, a fallible call fails the frame too.
	tries int
	// rawErr is the Go call whose error a plain catch takes unread
	// (catch_plain.go; ir_fail.go catch): bind leaves it Go's error, not
	// go.Error (GOERR-BOX).
	rawErr *syntax.CallExpr
	// tupleBind is the fallible call bind is binding, whose std body may
	// return several values and an error (A, B, error); tupleBound is
	// that body's Go call, which bind binds with its error (tupleErrBind;
	// EXTERN-MULTI-RESULT).
	tupleBind  *syntax.CallExpr
	tupleBound *irExpr
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
	// strVars are the locals in the built string's form, strBufs their
	// StrBuf locals once declared, and strSites the forms built
	// (strbuf.go).
	strVars  map[*check.Var]bool
	strBufs  map[*check.Var]int
	strSites int
	// names allocates the temporaries' names (ir_fail.go, tempNames).
	names *namer
	// given are the names the body's locals and temporaries have: a
	// source local keeps its spelling unless Go's scope or one of these
	// has it (ir_stmt.go, localName).
	given map[string]bool
	// decl and pkg are the declaration whose body is built and its
	// package, which a build-time fold evaluates in (ir_constfold.go); nil in a
	// law's or a test's body, which never folds.
	decl syntax.Decl
	pkg  *check.Package
	// foldCounts are this body's folds, which count once the body is kept.
	foldCounts FoldCounts
	// tmplHoles and tmplTypes are the holes of the template being built
	// and their types, as constOf places them (ir_template.go).
	tmplHoles []*irExpr
	tmplTypes []check.Type
	// law is the law whose body is built (ir_assert.go), nil in any
	// other body and inside a function literal: each value it returns is
	// checked where it is returned.
	law *check.Law
	// lawOwn is the local of the provider under test that a law of a
	// generic operation type takes after its environment (the emitter's
	// lawDecl `own`), which its operation calls call; 0 in any other body
	// (a law's environment is its local 0, so own is never 0).
	lawOwn int
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
	// rwBound is the binding of the law over function parameters whose
	// sides are being lowered at a site (ir_fusion.go withBinding).
	rwBound    rwBinding
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
	// dropping is the call a statement discards, while it is built
	// (ir_forms_drop.go).
	dropping *syntax.CallExpr
	// voidTail marks the branches of a body with no value whose last
	// statement ends the body: a tail of an error value fails the frame
	// (ir_fail.go bodyStmts, ir_stmt.go tailTo).
	voidTail bool
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
	// fuse is the fused match whose callee body is being built: its
	// results jump to the match's arms (ir_forms_match.go).
	fuse *fuseSite
	// preset are the arguments of a component call evaluated once into a
	// local, which the call in its group's body reads (ir_uigroup.go).
	preset map[syntax.Expr]int
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
	var root syntax.Node
	if f != nil && f.Decl != nil {
		root = f.Decl
	}
	b := st.newBuilder(fc, root)
	b.result = fc.frame.result
	b.factLocals() // ir_bounds.go
	// A body that falls back is the emitter's, whose walk starts with no
	// facts: none of this walk's stays (deferred first, so it runs after
	// finish has recovered the reason).
	defer func() { fc.bounds, fc.factLocal = nil, nil }()
	if f != nil && f.Decl != nil {
		b.decl, b.pkg = f.Decl, f.Pkg
	}
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
	} else if b.fc.mono && b.copyName(f) == "" && !slices.Contains(gates, "copy for an instance") {
		// A copy whose name is unknown would take f's (irInstanceCopy).
		gates = append(gates, "copy for an instance")
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
	if drop {
		defer func() {
			if ok {
				st.report.DropBodies++ // runs before finish (LIFO), with ok as finish will see it
			}
		}()
	}
	// A library body (a std body the program reaches, written with no
	// //line directive) takes an IR body as a sourced one does: its
	// positions name its std file (pos), so its lines and its defects'
	// locations are its .ergo source's (G5).
	for _, g := range gates {
		b.gate(g)
	}
	if len(fc.witness) > 0 {
		b.witnessGate() // ir_witness.go
	}
	if f.Decl.Body == nil {
		irNo("no body")
	}
	// Go parameter order: receiver, environment, parameters (emitBody). A
	// monomorphic copy of a method is a plain function: environment,
	// receiver, parameters (mono.go).
	method := fd.method != nil && b.monoName(f) == ""
	for i, v := range params {
		if v == nil {
			// An unnamed parameter (`_ T`) has its slot; no body reads it.
			if i == 0 && !fd.envFree && !method {
				b.env = b.next
				b.next++
			}
			if i == 0 && fd.method == nil {
				b.witnessSlots(f) // after the environment (ir_witness.go)
			}
			b.next++
			if i == 0 && method && !fd.envFree {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && method {
			b.locals[v] = b.next
			b.next++
			if !fd.envFree {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && !fd.envFree {
			b.env = b.next
			b.next++
		}
		if i == 0 {
			b.witnessSlots(f) // after the environment (ir_witness.go)
		}
		b.locals[v] = b.next
		b.next++
		b.part(func() { b.goType(v.Type, f.Decl) })
	}
	if len(params) == 0 && !fd.envFree {
		b.env = b.next
		b.next++
	}
	if len(params) == 0 {
		b.witnessSlots(f) // ir_witness.go
	}
	b.slots = b.next
	if b.result != nil {
		b.part(func() { b.goType(b.result, f.Decl) })
	}
	b.ops = &irOpBody{f: f, env: b.env} // ir_forms_op.go
	b.fn = &irFunc{Name: name, Pos: b.pos(f.Decl, f.Decl.Pos()), End: b.pos(f.Decl, f.Decl.Body.Rbrace)}
	b.names = b.tempNames(f.Decl.Body)            // ir_fail.go
	b.strVars = strBufVars(b.l.info, f.Decl.Body) // strbuf.go
	b.params = map[int]bool{}                     // ir_hoist.go: the parameters' locals, before openOwn's
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
	b.part(func() { b.fn.Body = b.closeMasked(f, b.fn.Body) })     // ir_own.go
	b.fn.Body = b.deferExits(b.fn.Body)                            // ir_exits.go (STATIC-EXIT2)
	b.fn.Body = irScopesIncrease(irUseLocals(b.fn.Body), b.fn.Pos) // ir_end.go
	if st.census != nil {
		return false
	}
	b.ownDone(name) // ir_own.go
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.inlineCallee(b, fd, params) // ir_forms_inline.go
	st.report.IR = append(st.report.IR, name)
	st.report.Folds.add(b.foldCounts) // ir_constfold.go
	st.report.Memo += b.memoSites
	st.report.Merge += b.mergeSites
	st.report.Fold += b.foldSites
	st.report.Seed += b.seedSites
	st.report.StrBuf += b.strSites
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
		b.l.intMapBody(b.l.info, decl, u.reason) // intmap.go: its emitter body writes Go's map operations
		*ok = false
		if st.noFallback {
			st.fallbacks = append(st.fallbacks, IRCensusFunc{Pkg: pkg, Name: name, File: st.fileOf(b, decl), Std: std, Reasons: []string{u.reason}})
		}
		st.rewritesDone(b, false, u.reason) // ir_rewrite.go
	} else if *ok {
		st.rewritesDone(b, true, "")
	}
	if st.census != nil {
		st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: pkg, Name: name, File: st.fileOf(b, decl), Std: std, Reasons: b.reasons})
		*ok = false
	}
}

// fileOf is decl's source file for a sourced body, else "".
func (st *irState) fileOf(b *irBuilder, decl syntax.Node) string {
	file := ""
	if _, sourced := b.l.sourceOf(decl); sourced {
		_, file, _, _ = b.l.at(decl, decl.Pos())
	}
	return file
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
