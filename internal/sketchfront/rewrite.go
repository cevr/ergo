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
	"sort"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The law ledger (runtime.md, Tests 8; lower/ledger.go): ergo test writes
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

// ledgerEvidence is the run build's evidence from the module ledger's text
// and the std ledger, with the producer record's digests.
func ledgerEvidence(unit lower.Unit, text string) (*lower.LedgerEvidence, error) {
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
	ev := lower.NewLedgerEvidence(unit.Package, entries)
	ev.Ledgers = []string{lower.LedgerFile + " sha256:" + sha256Hex(text), "std " + lower.LedgerFile + " sha256:" + sha256Hex(std.Ledger())}
	return ev, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// lawChecks builds the check files of a ledger run and runs each law of
// laws in them alone (lower.RewriteChecks); a test replaces it.
var lawChecks = lawCheckRun

// UpdateLedger is ergo test's writer of the ledger at req.Ledger: it runs
// the laws and site instances the run build's lowering asks for, and
// writes their entries over the file's, adding new ones; other entries
// stay. It returns the count of entries changed, and writes nothing when
// none did. A program with no rewrite law that a provider's run checks
// changes nothing. A reachable refutation is a *lower.RefutedRewriteError,
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
	if !lower.NewLedgerEvidence(unit.Package, nil).HasRules() {
		return 0, nil
	}
	ev, err := lower.RewriteChecks(unit, lawChecks(req, input))
	if err != nil {
		return 0, err
	}
	text, err := ledgerText(req.Ledger)
	if err != nil {
		return 0, err
	}
	old, err := lower.ParseLedger(text)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, e := range lower.LedgerEntries(unit, ev) {
		if prev, ok := old[e.Key()]; !ok || prev.Line() != e.Line() {
			changed++
			old[e.Key()] = e
		}
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, writeFileAtomic(req.Ledger, []byte(lower.FormatLedger(old)))
}

// StdLedgerHeader is the first line of the std ledger.
const StdLedgerHeader = "# std law ledger (runtime.md, Tests 8). Written by go run ./scripts/stdlaws; do not edit.\n"

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
		unit := lower.Unit{Package: pkg, Files: files, Sources: sources}
		if !lower.NewLedgerEvidence(pkg, nil).HasRules() {
			continue
		}
		test := req
		test.Mode = build.ModeTest
		input := func() (build.Input, error) { return unitInput(unit, stdDependencies(), test) }
		ev, err := lower.RewriteChecks(unit, lawChecks(req, input))
		if err != nil {
			return "", fmt.Errorf("std %s: %w", path, err)
		}
		for _, e := range lower.LedgerEntries(unit, ev) {
			entries[e.Key()] = e
		}
	}
	return StdLedgerHeader + lower.FormatLedger(entries), nil
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
