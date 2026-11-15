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

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The law ledger (runtime.md, Tests 10; lower/ledger.go): ergo test writes
// ergo.laws from the check runs of the laws the run build's sites use
// (UpdateLedger); ergo build, run and inspect only read it and the std
// ledger (ledgerEvidence). The run build never runs a law.

// LedgerError is the refusal of a locked build (BuildRequest.Locked): an
// entry the build needs is stale or missing.
type LedgerError struct{ Lines []string }

func (e *LedgerError) Error() string {
	msg := "ergo build --locked: the law ledger is not current"
	for _, l := range e.Lines {
		msg += "\n" + l
	}
	return msg
}

// ledgerText reads the ledger at path; a missing file is an empty ledger.
func ledgerText(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
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

// ledgerEvidence is the run build's evidence from the module ledger's text
// and the std ledger.
func ledgerEvidence(a Analysis, unit lower.Unit, text string) (*lower.LedgerEvidence, error) {
	entries, err := lower.ParseLedger(std.Ledger())
	if err != nil {
		return nil, fmt.Errorf("the std law ledger: %w", err)
	}
	mod, err := lower.ParseLedger(text)
	if err != nil {
		return nil, err
	}
	for k, e := range mod {
		entries[k] = e
	}
	return lower.NewLedgerEvidence(unit.Package, ledgerNames(a), entries), nil
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
// missing file stays missing). A library (no main) has no run build and
// writes nothing. A reachable refutation is a *lower.RefutedRewriteError,
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
	if !hasMain(unit.Package) {
		// A library has no run build, so no site of its own.
		return 0, nil
	}
	names := ledgerNames(a)
	var fresh []lower.LedgerEntry
	if lower.NewLedgerEvidence(unit.Package, names, nil).HasRules() {
		ev, err := lower.RewriteChecksCached(unit, lawChecks(req, input), req.Laws)
		if err != nil {
			return 0, err
		}
		fresh = lower.LedgerEntries(unit, names, ev)
	}
	text, err := ledgerText(req.Ledger)
	if err != nil {
		return 0, err
	}
	old, err := lower.ParseLedger(text)
	if err != nil {
		return 0, err
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
	if changed == 0 {
		return 0, nil
	}
	return changed, writeFileAtomic(req.Ledger, []byte(lower.FormatLedger(next)))
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
	lawPkg, law := e.LawPackage()
	provPkg, prov := e.ProviderPackage()
	if p, known := s.pkg(lawPkg); known && (p == nil || !slices.ContainsFunc(p.Laws, func(l *check.Law) bool {
		return l.Owner != nil && l.Owner.Name+"."+l.Name == law
	})) {
		return false
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
	if _, ok := std.Package(name); ok {
		p, err := check.Std.Import(name)
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
// with none needs no Go tool.
func FreshStdLedger(req BuildRequest) (string, error) {
	entries := map[string]lower.LedgerEntry{}
	for _, path := range std.Packages() {
		texts, _ := std.Package(path)
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
		if !rewrites {
			continue
		}
		pkg, err := check.Std.Import(path)
		if err != nil {
			return "", err
		}
		files, sources = pkg.Files(), nil
		for _, f := range files {
			sources = append(sources, lower.Source{Path: "/ergo-std/" + path + "/" + f.Name, Text: f.Src})
		}
		unit := lower.Unit{Package: pkg, Files: files, Sources: sources, AllLaws: true}
		if !lower.NewLedgerEvidence(pkg, nil, nil).HasRules() {
			continue
		}
		test := req
		test.Mode = build.ModeTest
		input := func() (build.Input, error) { return unitInput(unit, stdDependencies(), test) }
		ev, err := lower.RewriteChecksCached(unit, lawChecks(req, input), req.Laws)
		if err != nil {
			return "", fmt.Errorf("std %s: %w", path, err)
		}
		for _, e := range lower.LedgerEntries(unit, nil, ev) {
			entries[e.Key()] = e
		}
	}
	return StdLedgerHeader + lower.FormatLedger(entries), nil
}

// hasMain reports whether pkg declares main, the run build's entry.
func hasMain(pkg *check.Package) bool {
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
		if err := child.Run(); err != nil {
			return "", fmt.Errorf("the law ledger's check build failed: %w", err)
		}
		var out bytes.Buffer
		for _, law := range laws {
			run := exec.Command(exe, "--seed", lower.RewriteCheckSeed, "--law", law)
			run.Stdout, run.Stderr = &out, io.Discard
			// A refuted law exits non-zero; its line is its verdict.
			_ = run.Run()
		}
		return out.String(), nil
	}
}
