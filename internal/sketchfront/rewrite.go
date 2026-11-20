package sketchfront

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/toolchain"
)

// The law ledger (runtime.md, Tests 10; lower/ledger.go): ergo test writes
// ergo.laws from the check runs of the laws the run build's sites use
// (UpdateLedger); ergo build, run and inspect only read it and the std
// ledger (ledgerEvidence). The run build never runs a law.

// LedgerError is the refusal of a build by its law ledger: of a locked
// build (BuildRequest.Locked), when an entry the build needs is stale or
// missing; of any build, when a current entry the build met is refuted
// (Refuted; S108, LAWFFI r2 §1: a refuted entry refuses the build for
// every level).
type LedgerError struct {
	Lines   []string
	Refuted bool
}

func (e *LedgerError) Error() string {
	msg := "ergo build --locked: the law ledger is not current"
	if e.Refuted {
		msg = "error[S108]: the law ledger refutes a law this build would apply"
	}
	for _, l := range e.Lines {
		msg += "\n" + l
	}
	return msg
}

// ledgerText reads the ledger at path; a missing file is an empty ledger.
func ledgerText(fsys FS, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if fsys == nil {
		fsys = hostFS{}
	}
	data, err := fsys.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// ledgerNames are the module-wide names of a's packages: the root, a
// binary's main package, is named by its folder's import path.
func ledgerNames(a Analysis) lower.LedgerNames {
	if a.program == nil || a.program.root == nil || a.program.root.pkg == nil {
		return nil
	}
	return lower.LedgerNames{a.program.root.pkg: a.program.root.path}
}

// ledgerEvidence is the run build's evidence from the module ledger's
// text: the evidence for the laws of the module's own packages. A package
// another module declares takes its own module's ledger (lower's
// evidenceFor), so this ledger never answers for it (STD-NOPRIV NP2). The
// foreign keys are read through fsys (foreignkeys.go).
func ledgerEvidence(a Analysis, unit lower.Unit, text string, fsys FS) (*lower.LedgerEvidence, error) {
	entries, err := lower.ParseLedger(text)
	if err != nil {
		return nil, err
	}
	return lower.NewLedgerEvidence(unit.Package, ledgerNames(a), entries, foreignKeys(a, fsys)), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// lawChecks builds the check files of a ledger run and runs each law of
// laws in them alone (lower.RewriteChecks); a test replaces it.
var lawChecks = lawCheckRun

// UpdateLedger is ergo test's writer of the module ledger at req.Ledger,
// which every binary of the module shares, as go.sum is: it runs the
// laws and site instances the run build's lowering asks for and adds or
// replaces their entries. It removes an entry only when the module's
// sources no longer declare its law, its provider or their package
// (ledgerSource), never because this run did not take it: another
// binary's sites may need it. So the file after ergo test of several
// binaries does not depend on their order. It returns the count of
// entries added, changed and removed, and writes nothing when it is 0 (a
// missing file stays missing). The package's top-level rewrite laws in
// scope take the licence run when their entry is not current
// (licenceEntries); a library (no main) has no run build and writes only
// those. A reachable refutation is a *lower.RefutedRewriteError,
// and writes nothing.
func UpdateLedger(a Analysis, req BuildRequest) (int, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return 0, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit, err := analysisUnit(a, req.Source)
	if err != nil {
		return 0, err
	}
	input := func() (build.Input, error) { return unitInput(unit, analysisDependencies(a), req) }
	names := ledgerNames(a)
	keys := foreignKeys(a, req.Inputs)
	text, err := ledgerText(req.Inputs, req.Ledger)
	if err != nil {
		return 0, err
	}
	old, err := lower.ParseLedger(text)
	if err != nil {
		return 0, err
	}
	// The root's top-level rewrite laws take their licence run, a
	// library's too: their entries are the package's, not a site's.
	ctext, err := ledgerText(req.Inputs, corpusPath(req.Ledger))
	if err != nil {
		return 0, err
	}
	cases, err := parseCorpus(ctext)
	if err != nil {
		return 0, err
	}
	before := cases.format()
	fresh, skipped, err := licenceEntries(unit.Package, names, old, keys, req.Stderr, cases, req.Toolchain)
	if err != nil {
		return 0, err
	}
	// A skipped licence fails the test once the ledger is written
	// (AC10): the entry records it, and the error names the fix.
	var skip error
	if len(skipped) > 0 {
		skip = &LicenceSkippedError{Skipped: skipped}
	}
	if HasMain(unit.Package) {
		// RewriteChecks runs nothing when neither the root nor an import
		// has a rewrite law: an import's provider is checked at the
		// root's sites. A library has no run build, so no site of its
		// own.
		ev, err := lower.RewriteChecksCached(unit, lawChecks(req, input), req.Laws)
		if err != nil {
			return 0, err
		}
		fresh = append(fresh, lower.LedgerEntries(unit, names, ev, keys)...)
	}
	next := map[string]lower.LedgerEntry{}
	changed := 0
	src := ledgerSource{a: a, names: names, pkgs: map[string]*check.Package{}}
	for k, e := range old {
		if src.declares(e) {
			next[k] = e
		} else {
			changed++ // pruned
		}
	}
	for _, e := range fresh {
		if prev, ok := next[e.Key()]; !ok || prev.Line() != e.Line() {
			changed++
		}
		next[e.Key()] = e
	}
	// An entry whose key this run recomputed into another form is never
	// read again (LEDGER-SUPERSEDE): it goes, and an entry of another
	// build stays.
	changed += lower.DropSuperseded(next, fresh, buildTags(a))
	// The corpus keeps a law's cases while its package declares it in
	// the rewrite's scope, by the ledger's pruning rule.
	for k := range cases {
		if !src.declares(lower.LedgerEntry{Law: k}) {
			delete(cases, k)
		}
	}
	if after := cases.format(); after != before {
		changed++
		if err := writeFileAtomic(corpusPath(req.Ledger), []byte(after)); err != nil {
			return 0, err
		}
	}
	if changed == 0 {
		return 0, skip
	}
	if err := writeFileAtomic(req.Ledger, []byte(lower.FormatLedger(next))); err != nil {
		return 0, err
	}
	return changed, skip
}

// ledgerSource answers whether the module's sources still declare a
// ledger entry's law, provider and their packages: the writer's pruning
// rule. A package is found by its module-wide name: the analysed root,
// std, a package the program imports, or the module's folder of that
// import path, checked. A folder with no .ergo file declares nothing; a
// package that does not check, or a name outside the module, is not
// known, and its entries stay.
type ledgerSource struct {
	a     Analysis
	names lower.LedgerNames
	pkgs  map[string]*check.Package
}

func (s ledgerSource) declares(e lower.LedgerEntry) bool {
	if e.Fact != "" {
		// A fact line is its guard run's (LF2): this writer keeps it.
		return true
	}
	lawPkg, law := e.LawPackage()
	provPkg, prov := e.ProviderPackage()
	if p, known := s.pkg(lawPkg); known && (p == nil || !slices.ContainsFunc(p.Laws, func(l *check.Law) bool {
		if e.Provider == "" {
			// A top-level law's entry (LF6b): the law, in the
			// rewrite's scope still.
			return l.Owner == nil && l.Machine == nil && l.Name == law && l.TopRewrite() != nil
		}
		return l.Owner != nil && l.Owner.Name+"."+l.Name == law
	})) {
		return false
	}
	if e.Provider == "" {
		return true
	}
	if p, known := s.pkg(provPkg); known && (p == nil || !slices.ContainsFunc(p.Types, func(t *check.TypeName) bool { return t.Name == prov })) {
		return false
	}
	return true
}

// pkg is the package named name, and whether the module's sources tell;
// nil when they tell there is none.
func (s ledgerSource) pkg(name string) (*check.Package, bool) {
	if p, ok := s.pkgs[name]; ok {
		return p, true
	}
	p, known := s.find(name)
	if known {
		s.pkgs[name] = p
	}
	return p, known
}

func (s ledgerSource) find(name string) (*check.Package, bool) {
	if s.a.pkg != nil && s.names.Of(s.a.pkg) == name {
		return s.a.pkg, true
	}
	if p, shipped, err := shippedImport(name); shipped {
		return p, err == nil
	}
	prog := s.a.program
	if prog == nil || prog.module == "" {
		return nil, false
	}
	for _, q := range prog.imports {
		if q.path == name && q.pkg != nil {
			return q.pkg, true
		}
	}
	rel, ok := strings.CutPrefix(name, prog.module)
	if !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
		return nil, false
	}
	dir := filepath.Join(prog.modRoot, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	if !slices.ContainsFunc(entries, func(d fs.DirEntry) bool { return !d.IsDir() && filepath.Ext(d.Name()) == ".ergo" }) {
		return nil, true
	}
	a, err := CheckDir(dir)
	if err != nil || a.pkg == nil {
		return nil, false
	}
	return a.pkg, true
}

// StdLedgerHeader is the first line of the std ledger.
const StdLedgerHeader = "# std law ledger (runtime.md, Tests 10). Written by go run ./scripts/stdlaws; do not edit.\n"

// FreshStdLedger is the std ledger a fresh run gives: the entries of the
// check runs of every std package with a rewrite law that a provider's run
// checks, each package checked as the root of its own unit, as a module's
// ergo test writes them. A package with no such law runs nothing, so a std
// with none needs no Go tool. host are the guard lines of the reviewed
// tables in runtime/v1 (internal/gofacts/hostguard.Entries, LAWFFI LF2d),
// which the caller runs: the compiler does not link their guards.
func FreshStdLedger(req BuildRequest, host []gofacts.Entry) (string, error) {
	entries := map[string]lower.LedgerEntry{}
	for _, path := range stdlib.Packages() {
		texts, _ := stdlib.Package(path)
		names := make([]string, 0, len(texts))
		for n := range texts {
			names = append(names, n)
		}
		sort.Strings(names)
		var files []*syntax.File
		var sources []lower.Source
		rewrites := false
		for _, n := range names {
			f, err := syntax.Parse(texts[n])
			if err != nil {
				return "", fmt.Errorf("std %s/%s: %w", path, n, err)
			}
			f.Name = n
			files = append(files, f)
			syntax.Inspect(f, func(x syntax.Node) bool {
				if l, ok := x.(*syntax.LawDecl); ok && l.Rewrite != nil {
					rewrites = true
				}
				return !rewrites
			})
		}
		pkg, err := check.Std.Import(path)
		if err != nil {
			return "", err
		}
		// A top-level rewrite law's licence run is the evaluator's, so
		// it needs no Go tool (licenceEntries): a std Go body runs by its
		// evaluator native. Std holds no skipped licence, as a module's
		// test does not (AC10), so a skip fails the fresh ledger.
		lic, skipped, err := licenceEntries(pkg, nil, nil, lower.ShippedForeignKeys, req.Stderr, nil, build.Toolchain{})
		if err != nil {
			return "", fmt.Errorf("std %s: %w", path, err)
		}
		if len(skipped) > 0 {
			return "", fmt.Errorf("std %s: %w", path, &LicenceSkippedError{Skipped: skipped})
		}
		for _, e := range lic {
			entries[e.Key()] = e
		}
		if !rewrites {
			continue
		}
		files, sources = pkg.Files(), nil
		for _, f := range files {
			sources = append(sources, lower.Source{Path: "/ergo-std/" + path + "/" + f.Name, Text: f.Src})
		}
		unit := lower.Unit{Package: pkg, Files: files, Sources: sources, AllLaws: true}
		if !lower.NewLedgerEvidence(pkg, nil, nil, nil).HasRules() {
			continue
		}
		test := req
		test.Mode = build.ModeTest
		input := func() (build.Input, error) { return unitInput(unit, stdDependencies(), test) }
		ev, err := lower.RewriteChecksCached(unit, lawChecks(req, input), req.Laws)
		if err != nil {
			return "", fmt.Errorf("std %s: %w", path, err)
		}
		for _, e := range lower.LedgerEntries(unit, nil, ev, lower.ShippedForeignKeys) {
			entries[e.Key()] = e
		}
	}
	for _, e := range GuardFactEntries() {
		entries[e.Key()] = e
	}
	for _, h := range host {
		e := lower.LedgerEntry{Fact: h.Key, Verdict: h.Verdict, By: h.By, With: h.With, Digest: h.Digest}
		entries[e.Key()] = e
	}
	return StdLedgerHeader + lower.FormatLedger(entries), nil
}

// GuardFactEntries runs the guard of every row of the reviewed fact table
// (internal/gofacts; LAWFFI LF2) and gives its ledger line: validated by
// guard when the guard passes, refuted when it shows the function writing
// the operand; with the pinned toolchain, and the row's digest at it.
func GuardFactEntries() []lower.LedgerEntry {
	var out []lower.LedgerEntry
	for _, r := range gofacts.Rows {
		verdict := "validated"
		if r.Guard(gofacts.GuardSeed) != nil {
			verdict = "refuted"
		}
		out = append(out, lower.LedgerEntry{Fact: r.Key(), Verdict: verdict, By: "guard", With: toolchain.Version, Digest: r.Digest(toolchain.Version)})
	}
	return out
}

// HasMain reports whether pkg declares main, the run build's entry. Only
// such a package's ledger takes a build (UpdateLedger's RewriteChecks), so
// only its writer needs a toolchain.
func HasMain(pkg *check.Package) bool {
	for _, f := range pkg.Funcs {
		if f.Name == "main" {
			return true
		}
	}
	return false
}

// writeFileAtomic writes data to path by a rename, so a reader sees the
// old file or the new one.
func writeFileAtomic(path string, data []byte) error {
	part, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, werr := part.Write(data)
	cerr := part.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(part.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(part.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(part.Name())
	}
	return werr
}

// lawCheckRun is the check run of a ledger update: it links the check
// files as a test module of the emission cache (inputOf gives the key's
// input), for the host, and runs
// each law with --seed lower.RewriteCheckSeed --law TITLE.
func lawCheckRun(req BuildRequest, inputOf func() (build.Input, error)) func(files []lower.File, laws []string) (string, error) {
	return func(files []lower.File, laws []string) (string, error) {
		input, err := inputOf()
		if err != nil {
			return "", err
		}
		hash := sha256.New()
		for _, f := range files {
			fmt.Fprintf(hash, "%d:%s%d:", len(f.Path), f.Path, len(f.Data))
			hash.Write(f.Data)
		}
		test := input
		test.Mode = build.ModeTest
		test.Dependencies = append(append([]build.Dependency(nil), input.Dependencies...), sourceDependency("law ledger check", map[string]string{"key": hex.EncodeToString(hash.Sum(nil))}))
		module, err := req.Cache.Module(test, func() ([]build.File, error) {
			out := make([]build.File, len(files))
			for i, f := range files {
				out[i] = build.File{Path: f.Path, Data: f.Data}
			}
			return out, nil
		})
		if err != nil {
			return "", err
		}
		defer module.Release()
		tmp, err := os.MkdirTemp("", "ergo-law-check-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		// The check runs here, so it is built for the host.
		host := req.Toolchain
		host.GOOS, host.GOARCH, host.Tags = "", "", nil
		exe := filepath.Join(tmp, "check")
		child := host.Command(module, exe)
		stderr := req.Stderr
		if stderr == nil {
			stderr = io.Discard
		}
		child.Stdout, child.Stderr = stderr, stderr
		stage.Add(stage.GoCommands, 1)
		stage.Add(stage.Processes, 1)
		if err := child.Run(); err != nil {
			return "", fmt.Errorf("the law ledger's check build failed: %w", err)
		}
		var out bytes.Buffer
		for _, law := range laws {
			run := exec.Command(exe, "--seed", lower.RewriteCheckSeed, "--law", law)
			run.Stdout, run.Stderr = &out, io.Discard
			// A refuted law exits non-zero; its line is its verdict.
			stage.Add(stage.Processes, 1)
			_ = run.Run()
		}
		return out.String(), nil
	}
}
