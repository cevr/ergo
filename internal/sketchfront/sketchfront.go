// Package sketchfront is Ergo's one front end: a thin adapter
// that parses with internal/sketch/syntax, checks with internal/sketch/check,
// and formats with internal/hostsyntax. It maps every diagnostic to a
// report diagnostic with a UTF-8 byte span, so every surface projects it
// with the shared report code. The CLI checks and formats sources only
// through Check and Format, so it reports one identity.
//
// Build lowers a checked program from the checker's own Package and Info
// (internal/sketch/lower) into the content-addressed emission cache
// (internal/sketch/build) and links it with the go command; in test
// mode it lowers the test binary instead (lower.LowerTests), a distinct
// module of the same checked program. There is no second checker: a program the checker accepts and the lowering cannot
// lower yet is refused with a located S109 that names the stage owning the
// form, and nothing is published.
//
// No runtime or generated-code package may import it.
package sketchfront

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/stage"
)

// Analysis is the 2026 front-end's outcome for one source text (Check) or
// one package folder and the packages it imports (CheckDir, load.go). It
// keeps the parsed files and the checker's packages, so building reads the
// same checked program and never parses or checks again.
type Analysis struct {
	// Diagnostics are the checker's diagnostics in source order, with UTF-8
	// byte spans into the analysed text; for a program read from a folder,
	// into the text of the diagnostic's File, package by package in
	// dependency order.
	Diagnostics []report.Diagnostic
	source      string
	file        *syntax.File
	pkg         *check.Package
	// program is the program read from a folder; nil for one source text.
	program *program
}

// Check parses source and checks it as one package with the 2026 checker.
// A syntax error is the only diagnostic of a source that does not parse.
func Check(source string) Analysis {
	return checkSource(source, nil, check.Options{})
}

// CheckOptions checks source like Check, in the checker modes opts selects.
func CheckOptions(source string, opts check.Options) Analysis {
	return checkSource(source, nil, opts)
}

func checkSource(source string, imp check.Importer, opts check.Options) Analysis {
	file, err := syntax.Parse(source)
	stage.Add(stage.FilesParsed, 1)
	stage.Add(stage.BytesParsed, int64(len(source)))
	if err != nil {
		return Analysis{Diagnostics: []report.Diagnostic{syntaxDiagnostic(err)}, source: source}
	}
	analysis := Analysis{source: source, file: file}
	prepareStd(nil)
	pkg, diagnostics := check.CheckOptions("main", []*syntax.File{file}, imp, opts)
	stage.Add(stage.PackagesChecked, 1)
	stage.Add(stage.DeclsChecked, int64(len(file.Decls)))
	analysis.pkg = pkg
	for _, d := range diagnostics {
		analysis.Diagnostics = append(analysis.Diagnostics, report.Diagnostic{
			Code:    d.Code,
			Message: d.Message,
			Span:    report.Span{Offset: d.Offset, Length: d.Length, Line: d.Line, Column: d.Column},
			Fix:     reportFix(d.Fix),
		})
	}
	return analysis
}

// refusals maps the lowering's refusals to located S109 diagnostics: the
// checker accepted the program, and its form waits for the stage the
// message names.
func (a Analysis) refusals(refused *lower.Refused) []report.Diagnostic {
	var out []report.Diagnostic
	for _, r := range refused.Refusals {
		d := report.Diagnostic{Code: check.CodeOutside, Message: r.Message}
		if a.program == nil {
			d.Span = span(a.source, lineStarts(a.source), r.Line, r.Column)
		} else if f, ok := a.program.file(r.Path); ok {
			d.File = f.rel
			d.Span = span(f.text, lineStarts(f.text), r.Line, r.Column)
		}
		out = append(out, d)
	}
	return out
}

// BuildError is a build refusal: the diagnostics that stop the build, with
// their positions. Nothing is written.
type BuildError struct{ Diagnostics []report.Diagnostic }

func (e *BuildError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "the 2026 front-end refused the build"
	}
	d := e.Diagnostics[0]
	switch {
	case d.Span.Line > 0 && d.File != "":
		return fmt.Sprintf("%s at %s:%d:%d: %s", d.Code, d.File, d.Span.Line, d.Span.Column, d.Message)
	case d.Span.Line > 0:
		return fmt.Sprintf("%s at %d:%d: %s", d.Code, d.Span.Line, d.Span.Column, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Code, d.Message)
}

// syntaxDiagnostic is the report diagnostic of a parse error, located by
// the parser's own byte span.
func syntaxDiagnostic(err error) report.Diagnostic {
	var e *syntax.Error
	if !errors.As(err, &e) {
		return report.Diagnostic{Code: syntax.CodeSyntax, Message: err.Error()}
	}
	return report.Diagnostic{Code: e.Code, Message: e.Message, Span: report.Span{Offset: e.Offset, Length: e.Length, Line: e.Line, Column: e.Column}}
}

// Result is the diagnostics-only report result of the analysis.
func (a Analysis) Result(source, target string) *report.Result {
	return report.NewResult(source, target, a.Diagnostics)
}

// FormatterIdentity names the 2026 formatter on every surface.
const FormatterIdentity = format.Identity

// Format formats a 2026 source with the syntax-only formatter, with the
// legacy formatter's result shape and errors: a report.FormatFailure
// holding the syntax diagnostic, or a report.FormatLimitError when the
// output would exceed a positive maxOutputBytes.
func Format(source string, maxOutputBytes int) (report.FormatResult, error) {
	if maxOutputBytes < 0 {
		return report.FormatResult{}, fmt.Errorf("formatter output limit must not be negative")
	}
	inputDigest := report.FormatDigest(source)
	partial := report.FormatResult{SchemaVersion: report.FormatterSchemaVersion, InputDigest: inputDigest}
	// The bound is checked while rendering, before the text is built.
	text, err := format.SourceBounded(source, maxOutputBytes)
	var limit *format.LimitError
	switch {
	case errors.As(err, &limit):
		return partial, report.FormatLimitError{Limit: maxOutputBytes}
	case err != nil:
		return partial, report.FormatFailure{Diagnostics: []report.Diagnostic{syntaxDiagnostic(err)}}
	}
	return report.FormatResult{
		SchemaVersion: report.FormatterSchemaVersion,
		InputDigest:   inputDigest,
		OutputDigest:  report.FormatDigest(text),
		Changed:       text != source,
		Text:          text,
	}, nil
}

// FailureMessage is the refusal text of a 2026 formatting failure: it names
// the first diagnostic's code and position.
func FailureMessage(failure report.FormatFailure) string {
	if len(failure.Diagnostics) == 0 {
		return failure.Error()
	}
	d := failure.Diagnostics[0]
	if d.Span.Line > 0 {
		return fmt.Sprintf("source cannot be formatted: %s at %d:%d: %s", d.Code, d.Span.Line, d.Span.Column, d.Message)
	}
	return fmt.Sprintf("source cannot be formatted: %s: %s", d.Code, d.Message)
}

// lineStarts are the byte offsets where the source's lines start: the
// syntax package counts LF only.
func lineStarts(source string) []int {
	starts := []int{0}
	for offset := 0; offset < len(source); offset++ {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

// span maps a line and column to a byte span. The column is one-based and
// counts bytes (syntax.File.Position), so the offset is the line start plus
// the column minus one. The span covers
// the identifier or number at that offset, else one character, and is empty
// at the end of the text. A position outside the text has no location.
func span(source string, lines []int, line, col int) report.Span {
	if line < 1 || line > len(lines) || col < 1 {
		return report.Span{}
	}
	offset := lines[line-1] + col - 1
	end := len(source)
	if line < len(lines) {
		end = lines[line] - 1
	}
	if offset > end || offset > len(source) {
		return report.Span{}
	}
	length := 0
	for offset+length < len(source) {
		r, width := utf8.DecodeRuneInString(source[offset+length:])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			length += width
			continue
		}
		if length == 0 && r != '\n' {
			length = width
		}
		break
	}
	return report.Span{Offset: offset, Length: length, Line: line, Column: col}
}

// LanguageRevision is the fixed language revision every emission names: the
// The v1 language v1 contract of 2026-10-28. There is no user edition selector.
const LanguageRevision = "ergo-v1-2026-10-28"

// emitterRevision names the lowering that builds use: checked-tree lowering
// from Info (lower-v1 stage 2).
const emitterRevision = "lower/2"

// LowerSingleEnv selects the reference lowering, one Go package for the
// whole program, when it is "single" (compile-speed CS4), and the package
// split without shards when it is "split", and shards in every build when
// it is "shards": the default splits the program
// into one Go package per Ergo package and per std package
// (lower.LowerSplit), and in a test build also cuts each big package into
// declaration shards (lower.LowerShards, test-speed FT9). The choice is
// part of the emission key.
const LowerSingleEnv = "ERGO_LOWER"

// The lowerings LowerSingleEnv selects.
const (
	lowerSingle = "single"
	lowerSplit  = "split"
	lowerShards = "shards"
)

// loweringOf is the lowering a build of mode uses.
func loweringOf(mode build.Mode) string {
	switch v := os.Getenv(LowerSingleEnv); v {
	case lowerSingle, lowerSplit, lowerShards:
		return v
	}
	if mode == build.ModeTest {
		return lowerAuto
	}
	return lowerSplit
}

// lowerAuto is the default of a test build: the split for the first build
// of a program, whose cold compile shards would make dearer (each shard
// pays the compile's start, its imports and its instantiations again;
// docs/receipts/compile-speed.md, "Declaration shards"), and shards for
// every build after an edit of it, the edit loop, where a body edit then
// compiles one shard. An unchanged build after the split one keeps the
// split, so it names the same module and reuses it (NG-Q8): the module of
// unchanged inputs does not depend on how many builds came before.
const lowerAuto = "auto"

// shardsSeen is the cache key that marks a program built before in mode.
// The mark holds the emission key of that split build: a later test build
// of the same inputs keeps the split, and one of other inputs shards.
func shardsSeen(req BuildRequest) string {
	sum := sha256.Sum256([]byte("ergo-shards\x00" + string(req.Mode) + "\x00" + req.Source))
	return "shards-" + hex.EncodeToString(sum[:])
}

// BuildRequest is one build of a checked analysis.
type BuildRequest struct {
	// Mode is the root the module runs: build.ModeBuild, main (ergo build
	// and ergo run), or build.ModeTest, the tests (ergo test). It is part
	// of the emission key, so the two builds of one source are distinct
	// modules.
	Mode build.Mode
	// Source is the admitted absolute path of the analysed source. //line
	// directives and the emission key name it.
	Source string
	// Output is the executable to write.
	Output string
	// Reuse, when set, is asked with the module before its link; when it
	// reports true (the caller has what the link would serve, such as a
	// cached test result), Build returns the module unlinked, and runs no
	// go command.
	Reuse func(build.Module) bool
	// Cache holds the generated modules; Toolchain links them.
	Cache     build.Cache
	Toolchain build.Toolchain
	// IR lowers the bodies the IR builder expresses as typed IR
	// (lower.LowerIR, lower.LowerIRTests), which only ergo's patched
	// compile reads: the Toolchain must run it (internal/toolchain,
	// Patch). Every ergo command's build sets it (plan-tier §1.1); the
	// source lowering stays the test-time reference.
	IR bool
	// Rewrites is ergo build's -rewrites switch (lower.Unit.Rewrites): on
	// (""), proved or off.
	Rewrites string
	// Stderr receives the go command's output.
	Stderr io.Writer
	// Ledger is the path of the law ledger, ergo.laws beside ergo.mod
	// (lower/ledger.go); "" or a missing file is an empty ledger. The run
	// build reads it; UpdateLedger writes it.
	Ledger string
	// Locked refuses a run build that needs a stale or missing ledger
	// entry (ergo build --locked; owner q122).
	Locked bool
	// NoViews is ergo build -views=off (lower.Unit.NoViews).
	NoViews bool
	// NoFallback is ergo build -fallback=off (lower.Unit.NoFallback).
	NoFallback bool
	// Inputs, when set, is the file system the build reads the ledger
	// through (a warm request's Recorder); nil reads the host.
	Inputs FS
	// Report, when set, receives how the toolchain built the executable
	// (build.Report: the direct path, or go build and why).
	Report *build.Report
	// Laws is the store of law verdicts (lawcache) the ledger's check
	// runs read and write; nil runs every law.
	Laws *lawcache.Store
}

// compilerIdentity is the compiler's producer identity for the emission
// key. Tests replace it with a process-scoped identity, which Linux and
// signed darwin hosts never report, to exercise private builds.
var compilerIdentity = build.CompilerIdentity

// Library lowers the checked program as the Go library package name of the
// Go module module (lower.LowerLibrary; IG11), anchored at source for a
// program of one source text: the files of a Go module, runtime included,
// that a Go program requires and replaces.
func Library(a Analysis, source, module, name string) ([]lower.File, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit := lower.Unit{Package: a.pkg, Files: []*syntax.File{a.file}, Sources: []lower.Source{{Path: source, Text: a.source}}, Root: filepath.Dir(source)}
	if a.program != nil {
		unit = a.program.unit()
	}
	files, err := lower.LowerLibrary(unit, module, name)
	if err != nil {
		return nil, err
	}
	return append(files, lower.Runtime()...), nil
}

// Build emits the checked analysis into the emission cache, or reuses its
// published module, and links the module to the request's output with the
// go command. The request's mode chooses the lowering: lower.Lower for a
// build of main, lower.LowerTests for the test binary. It returns the
// module whenever one is published, also when the link fails, so the
// caller can name it. An analysis with diagnostics, one whose source path
// cannot be anchored, or one the lowering refuses is refused before
// anything is published; a refusal is a *BuildError.
func Build(a Analysis, req BuildRequest) (build.Module, error) {
	unit, lowering, how, err := emission(a, req)
	if err != nil {
		return build.Module{}, err
	}
	split := how != lowerSingle
	input, err := unitInput(unit, analysisDependencies(a), req)
	if err != nil {
		return build.Module{}, err
	}
	switch how {
	case lowerSingle:
		input.Emitter += "+single"
	case lowerShards:
		input.Emitter += "+shards"
	}
	var ledgered []lower.File
	if req.Mode == build.ModeBuild {
		ev, files, err := ledgerLowered(a, unit, req, split)
		if err != nil {
			return build.Module{}, err
		}
		if ev != nil {
			// The entries the lowering read are an input of it: the key
			// holds them, and no other entry of the module's ledger.
			input.Dependencies = append(input.Dependencies, sourceDependency(lower.LedgerFile, map[string]string{"read": ev.Read()}))
			ledgered = files
		}
	}
	module, err := req.Cache.Module(input, func() ([]build.File, error) {
		if req.Toolchain.Stored {
			// A build from the store lowers nothing either.
			return nil, build.ErrNotStored
		}
		files := ledgered
		if files == nil {
			var err error
			files, err = lowerUnit(a, unit, lowering)
			if err != nil {
				return nil, err
			}
			countLowered(files)
		}
		out := make([]build.File, len(files))
		for i, f := range files {
			out[i] = build.File{Path: f.Path, Data: f.Data}
		}
		return out, nil
	})
	if err != nil {
		return build.Module{}, err
	}
	if module.Reused {
		stage.Add(stage.ModulesReused, 1)
	}
	if loweringOf(req.Mode) == lowerAuto && how == lowerSplit && req.Cache.Root != "" {
		// The next test build of this program shards. A mark that cannot
		// be written leaves it on the split: the error is not the build's.
		_ = req.Cache.PutWarmManifest(shardsSeen(req), []byte(module.Key+"\n"))
	}
	return module, link(module, req)
}

// link links the published module to the request's output with the go
// command; work counts each link (TEST-one's guard).
func link(module build.Module, req BuildRequest) error {
	// The build holds a private module's root until its link ends, on every
	// path; then a long-lived host holds nothing between builds.
	defer module.Release()
	if req.Reuse != nil && req.Reuse(module) {
		return nil
	}
	absolute, err := filepath.Abs(req.Output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return err
	}
	work.links.Add(1)
	report, err := req.Toolchain.Build(req.Cache, module, absolute, req.Stderr)
	if req.Report != nil {
		*req.Report = report
	}
	if err == nil {
		// Each executable by its module's lowering (TEQb): every ergo
		// build lowers through IR, the source text being the reference.
		if req.IR {
			stage.Add(stage.BinariesIR, 1)
		} else {
			stage.Add(stage.BinariesText, 1)
		}
	}
	return err
}

// withoutReport is an IR lowering without its report.
func withoutReport(lowering func(lower.Unit) ([]lower.File, lower.IRReport, error)) func(lower.Unit) ([]lower.File, error) {
	return func(u lower.Unit) ([]lower.File, error) {
		files, _, err := lowering(u)
		return files, err
	}
}

// buildInput is the unit of a checked analysis and its emission key's
// input for req's mode, as Build lowers and keys it (emission, unitInput).
func buildInput(a Analysis, req BuildRequest) (lower.Unit, build.Input, error) {
	unit, _, _, err := emission(a, req)
	if err != nil {
		return lower.Unit{}, build.Input{}, err
	}
	// A coverage build (build.CoverVariable) lowers std bodies with cover
	// marks; its key says so, so no other build reuses that module.
	unit.CoverMarks = req.Toolchain.Cover != ""
	input, err := unitInput(unit, analysisDependencies(a), req)
	if unit.CoverMarks {
		input.Flags = append(input.Flags, lower.CoverMarkFlag)
	}
	return unit, input, err
}

// rawFlag is the emission flag of a module lowered without go/format
// (lower.Unit.Raw), the dev profile's.
const rawFlag = "go/format=off"

// analysisUnit is the validated unit of a checked analysis; source
// anchors a program of one source text.
func analysisUnit(a Analysis, source string) (lower.Unit, error) {
	unit := lower.Unit{Package: a.pkg, Files: []*syntax.File{a.file}, Sources: []lower.Source{{Path: source, Text: a.source}}, Root: filepath.Dir(source)}
	if a.program != nil {
		unit = a.program.unit()
	}
	return unit, unit.Validate()
}

// analysisDependencies are the dependency summaries of the analysis's
// emission key: std, and every local package the program reaches, so an
// edit of an imported package builds again.
func analysisDependencies(a Analysis) []build.Dependency {
	dependencies := stdDependencies()
	if a.program != nil {
		dependencies = append(dependencies, a.program.dependencies()...)
	}
	return dependencies
}

// unitInput is the emission key's input of unit with its dependency
// summaries, for req's mode.
func unitInput(unit lower.Unit, dependencies []build.Dependency, req BuildRequest) (build.Input, error) {
	compiler := preparedCompilerIdentity()
	if compiler == "" {
		var err error
		compiler, err = compilerIdentity()
		if err != nil {
			return build.Input{}, err
		}
	}
	emitter := emitterRevision
	if req.IR {
		emitter += "+ir"
	}
	if req.NoViews {
		// -views=off lowers other Go (LF2b): its module is another key.
		emitter += "+noviews"
	}
	if req.NoFallback {
		// -fallback=off refuses what an IR build would link: a cached
		// module never answers it (FALLBACK-OFF).
		emitter += "+nofallback"
	}
	if unit.Rewrites != "" && unit.Rewrites != "on" {
		// The switch decides the lowered module (lower.Unit.Rewrites), so
		// it keys it: an off build never reuses an on build's module.
		emitter += "+rewrites=" + unit.Rewrites
	}
	input := build.Input{
		Dependencies: dependencies,
		Compiler:     compiler,
		Emitter:      emitter,
		Language:     LanguageRevision,
		Mode:         req.Mode,
	}
	for _, f := range lower.Runtime() {
		input.Runtime = append(input.Runtime, build.File{Path: f.Path, Data: f.Data})
	}
	for _, s := range unit.Sources {
		input.Sources = append(input.Sources, build.Source{Path: s.Path, Text: []byte(s.Text)})
	}
	if req.Toolchain.Profile == build.Dev {
		// The dev profile's module is emitted without go/format: other
		// bytes, so another emission key (test-speed FT5).
		input.Flags = append(input.Flags, rawFlag)
	}
	return input, nil
}

// lowerWithLedger lowers the run build of unit, a's, with the evidence of the
// ledger at req.Ledger and the std ledger (rewrite.go), and prints one
// line on req.Stderr for each entry a site needs that is stale or
// missing; such a site keeps the reference lowering. A locked request
// refuses instead (*LedgerError), and every request refuses a current
// refuted entry a site met. A package with no rewrite law that a
// provider's run checks reads no ledger: ev and files are nil, and the
// caller lowers it as before.
func lowerWithLedger(a Analysis, unit lower.Unit, req BuildRequest, split bool) (*lower.LedgerEvidence, []lower.File, error) {
	text, err := ledgerText(req.Inputs, req.Ledger)
	if err != nil {
		return nil, nil, err
	}
	ev, err := ledgerEvidence(a, unit, text, req.Inputs)
	if err != nil {
		return nil, nil, err
	}
	if !lower.HasRewriteLaws(unit) {
		return nil, nil, nil
	}
	unit.Rewrite = ev
	var files []lower.File
	if split {
		files, err = splitLowerer(false, req.IR, false)(unit)
	} else {
		lowering := lower.Lower
		if req.IR {
			lowering = withoutReport(lower.LowerIR)
		}
		files, err = lowering(unit)
	}
	if err != nil {
		return nil, nil, err
	}
	countLowered(files)
	if refused := ev.Refusals(); len(refused) > 0 {
		return nil, nil, &LedgerError{Lines: refused, Refuted: true}
	}
	notes := ev.Notes()
	if req.Locked && len(notes) > 0 {
		return nil, nil, &LedgerError{Lines: notes}
	}
	if req.Stderr != nil {
		for _, n := range notes {
			fmt.Fprintln(req.Stderr, n)
		}
	}
	return ev, files, nil
}

// emission is the unit a build of req lowers, the lowering it uses and
// which of LowerSingleEnv's lowerings that is (buildLowering; an IR build
// splits too, CS10): Build's and EmittedDigest's one reading of a request.
func emission(a Analysis, req BuildRequest) (lower.Unit, func(lower.Unit) ([]lower.File, error), string, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return lower.Unit{}, nil, "", &BuildError{Diagnostics: a.Diagnostics}
	}
	lowering := lower.Lower
	switch req.Mode {
	case build.ModeBuild:
		if req.IR {
			lowering = withoutReport(lower.LowerIR)
		}
	case build.ModeTest:
		lowering = lower.LowerTests
		if req.IR {
			lowering = withoutReport(lower.LowerIRTests)
		}
	default:
		return lower.Unit{}, nil, "", fmt.Errorf("sketchfront: unknown build mode %q", req.Mode)
	}
	unit, err := analysisUnit(a, req.Source)
	if err != nil {
		return lower.Unit{}, nil, "", err
	}
	// The dev profile's module skips go/format (test-speed FT5); unitInput
	// keys it apart.
	unit.Raw = req.Toolchain.Profile == build.Dev
	unit.NoViews = req.NoViews
	unit.NoFallback = req.NoFallback
	unit.Rewrites = req.Rewrites
	how := buildLowering(req, func() string {
		in, err := unitInput(unit, analysisDependencies(a), req)
		if err != nil {
			return ""
		}
		s, err := in.Summarize()
		if err != nil {
			return ""
		}
		return s.Key()
	})
	if how != lowerSingle {
		lowering = splitLowerer(req.Mode == build.ModeTest, req.IR, how == lowerShards)
	}
	return unit, lowering, how, nil
}

// buildLowering is the lowering a build of req uses (loweringOf), with
// lowerAuto resolved: shards for a program a build of this mode met
// before (its shardsSeen mark in the cache) with other inputs, else the
// split. splitKey is the emission key the split build of req would have;
// it is computed only when the mark is there.
func buildLowering(req BuildRequest, splitKey func() string) string {
	how := loweringOf(req.Mode)
	if how == lowerAuto {
		how = lowerSplit
		if mark, ok := req.Cache.WarmManifest(shardsSeen(req)); ok && req.Cache.Root != "" {
			if key := splitKey(); key == "" || string(mark) != key+"\n" {
				how = lowerShards
			}
		}
	}
	return how
}

// splitLowerer is the package split (CS4) of a build or test lowering, of
// IR bodies when ir (CS10), cut into declaration shards when shards (FT9).
func splitLowerer(test, ir, shards bool) func(lower.Unit) ([]lower.File, error) {
	return func(u lower.Unit) ([]lower.File, error) {
		switch {
		case shards && ir:
			files, _, _, err := lower.LowerIRShards(u, test)
			return files, err
		case shards:
			files, _, err := lower.LowerShards(u, test)
			return files, err
		}
		if ir {
			files, _, _, err := lower.LowerIRSplit(u, test)
			return files, err
		}
		files, _, err := lower.LowerSplit(u, test)
		return files, err
	}
}

// lowerUnit lowers unit, a refusal as the analysis's diagnostics.
func lowerUnit(a Analysis, unit lower.Unit, lowering func(lower.Unit) ([]lower.File, error)) ([]lower.File, error) {
	files, err := lowering(unit)
	var refused *lower.Refused
	if errors.As(err, &refused) {
		return nil, &BuildError{Diagnostics: a.refusals(refused)}
	}
	return files, err
}

// ledgerLowered is lowerWithLedger, a refusal as the analysis's
// diagnostics: a run build's files when its module has rewrite laws, else
// no evidence and no files.
func ledgerLowered(a Analysis, unit lower.Unit, req BuildRequest, split bool) (*lower.LedgerEvidence, []lower.File, error) {
	ev, files, err := lowerWithLedger(a, unit, req, split)
	var refused *lower.Refused
	if errors.As(err, &refused) {
		return nil, nil, &BuildError{Diagnostics: a.refusals(refused)}
	}
	return ev, files, err
}

// EmittedDigest is the SHA-256 of the Go module a Build of req would link:
// the runtime packages it imports and the lowered files, by path and bytes, and the mode. Two
// compilers that emit the same module for a program give the same digest,
// so a cache keyed on it replays across a compiler edit that does not
// change that program's Go (the examples runner, gate-speed GS8). It writes
// nothing and runs no Go tool.
func EmittedDigest(a Analysis, req BuildRequest) (string, error) {
	files, err := emittedFiles(a, req)
	if err != nil {
		return "", err
	}
	return emittedDigest(req.Mode, lower.Runtime(), files), nil
}

// emittedFiles are the lowered files of the module a Build of req links,
// without the runtime.
func emittedFiles(a Analysis, req BuildRequest) ([]lower.File, error) {
	unit, lowering, how, err := emission(a, req)
	if err != nil {
		return nil, err
	}
	split := how != lowerSingle
	var files []lower.File
	if req.Mode == build.ModeBuild {
		if _, files, err = ledgerLowered(a, unit, req, split); err != nil {
			return nil, err
		}
	}
	if files == nil {
		if files, err = lowerUnit(a, unit, lowering); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// emittedDigest is EmittedDigest of the lowered files: the mode, the files
// and the part of runtime they can link (lower.LinkedRuntime, CS-CUTOFF),
// by path and bytes.
func emittedDigest(mode build.Mode, runtime, files []lower.File) string {
	all := append(lower.LinkedRuntime(runtime, files), files...)
	slices.SortFunc(all, func(x, y lower.File) int { return strings.Compare(x.Path, y.Path) })
	hash := sha256.New()
	fmt.Fprintf(hash, "mode %s\n", mode)
	for _, f := range all {
		fmt.Fprintf(hash, "%d:%s%d:", len(f.Path), f.Path, len(f.Data))
		hash.Write(f.Data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// stdDependencies summarises the bundled standard library: the prelude and
// every importable package. Until package summaries exist (196b), each
// summary is the conservative digest of the package's whole source, for
// its interface and its implementation alike.
func stdDependencies() []build.Dependency {
	deps := []build.Dependency{sourceDependency(stdlib.PreludePath, stdlib.Prelude())}
	for _, path := range stdlib.Packages() {
		files, _ := stdlib.Package(path)
		deps = append(deps, sourceDependency(path, files))
	}
	return deps
}

// countLowered counts one lowered module and its Go bytes (stage).
func countLowered(files []lower.File) {
	stage.Add(stage.ModulesLowered, 1)
	for _, f := range files {
		stage.Add(stage.GoBytesLowered, int64(len(f.Data)))
	}
}

// sourceDependency summarises a source package by the digest of its file
// names and bytes, each length-prefixed so no two packages encode alike.
func sourceDependency(name string, files map[string]string) build.Dependency {
	names := make([]string, 0, len(files))
	for file := range files {
		names = append(names, file)
	}
	slices.Sort(names)
	hash := sha256.New()
	for _, file := range names {
		fmt.Fprintf(hash, "%d:%s%d:", len(file), file, len(files[file]))
		hash.Write([]byte(files[file]))
		stage.Add(stage.BytesHashed, int64(len(files[file])))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	return build.Dependency{Name: name, Interface: digest, Implementation: digest}
}
