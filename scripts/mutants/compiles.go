package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"ergo.local/prototype/scripts/mutants/anchor"
)

// A mutant that does not compile is invalid, never killed (GT-COMPILE).
// Before it judged such a row killed whenever a test failed on the compile
// error: a killer that builds a program (lower's tests build the lowered
// Go against runtime/v1; examples' cases check their own .ergo) fails on a
// mutated file that no longer compiles, as the suite's own build does not
// when the file is outside its closure. Three lanes found ledger rows
// killed only so (UC's H6b rows, killed by an S110 of the example they
// edit; WK's work rows). So after the mutant is applied, a Go file's
// package is compiled in the copy: go build of its package, and go test -c
// when it is a test file, in its module (the checkout's or a nested one).
// A file of a kind a source check validates (Config.Sources: every .ergo
// file, source.go) is judged by that check: the formatter's and the
// compiler's refusals the mutated file adds (FK7a, FK12a).
//
// A failure whose unmutated file fails the same way (a fixture that never
// compiles) is the baseline, not the mutant: the row is judged as before.
// Files of other kinds are not compiled. Limits: a package that imports
// the mutated one is not compiled (a mutant that changes an exported
// signature breaks its importers, which a test may then report); a Go
// file that a build constraint leaves out on this platform compiles as
// nothing.

// goError is a compiler's error line: FILE.go:LINE[:COL]: message.
var goError = regexp.MustCompile(`(?m)^\S+\.go:\d+(:\d+)?: .*$`)

// compiles returns why the mutated file at path (m.File, applied) does not
// compile, or "" when it compiles, when its kind is not compiled, or when
// the unmutated file fails too. It leaves the mutant applied.
func (w *worker) compiles(ctx context.Context, m *Mutant, path string, original []byte) string {
	if c := w.e.sourceCheck(m.File); c != nil {
		return w.sourceRefusal(ctx, c, m, path, original)
	}
	detail := w.compile(ctx, m.File)
	if detail == "" || ctx.Err() != nil {
		return ""
	}
	mutated, err := os.ReadFile(path)
	if err != nil {
		w.err = errors.Join(w.err, err)
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		w.err = errors.Join(w.err, err)
		return ""
	}
	if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
		w.err = errors.Join(w.err, err)
		return ""
	}
	base := w.compile(ctx, m.File)
	if err := os.WriteFile(path, mutated, info.Mode().Perm()); err != nil {
		w.err = errors.Join(w.err, err)
	}
	if base != "" {
		return "" // the unmutated file fails too: the baseline, not the mutant
	}
	return "does not compile: " + detail
}

// compile compiles what file (relative to the copy's root) belongs to and
// returns the first error line, or "".
func (w *worker) compile(ctx context.Context, file string) string {
	if strings.HasSuffix(file, ".go") {
		return w.compileGo(ctx, file)
	}
	return ""
}

func (w *worker) compileGo(ctx context.Context, file string) string {
	dir := path.Dir(file)
	mod := nestedModule(w.root, dir)
	pkg := "./" + strings.TrimPrefix(strings.TrimPrefix(dir, mod), "/")
	if dir == mod || dir == "." {
		pkg = "."
	}
	// -o os.DevNull: go build writes nothing, for a main package too, and
	// does not refuse a package that is not main ("-o DIR/" does).
	argv := []string{"go", "build", "-o", os.DevNull, pkg}
	if strings.HasSuffix(file, "_test.go") {
		argv = []string{"go", "test", "-c", "-o", os.DevNull, pkg}
	}
	o := w.e.exec(ctx, filepath.Join(w.root, filepath.FromSlash(mod)), argv, w.e.env(), buildFloor)
	if o.reason == "" {
		return ""
	}
	// Only a compiler's error is a compile failure; another (no Go files,
	// a directory go ignores) says nothing of the mutant.
	return goError.FindString(o.tail)
}

// sourceRefusal is why the mutated file at path (m's edit applied) is
// invalid by the source check c: the formatter would rewrite it, or the
// compiler gives a diagnostic the unmutated file does not (sourceState).
// "" when neither, or when the row declares the refusal its point
// (Mutant.Refused). It leaves the mutant applied.
func (w *worker) sourceRefusal(ctx context.Context, c *SourceCheck, m *Mutant, path string, original []byte) string {
	if m.Refused != "" {
		return ""
	}
	if _, ok := w.unmutated[path]; !ok {
		// The unmutated state is measured first, which builds c's tool from
		// the unmutated copy.
		info, err := os.Stat(path)
		if err != nil {
			return err.Error()
		}
		mutated, err := os.ReadFile(path)
		if err != nil {
			return err.Error()
		}
		if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
			w.err = errors.Join(w.err, err)
			return err.Error()
		}
		_, err = w.source(ctx, c, path, false)
		if werr := os.WriteFile(path, mutated, info.Mode().Perm()); werr != nil {
			w.err = errors.Join(w.err, werr)
			return werr.Error()
		}
		if err != nil {
			return err.Error()
		}
	}
	before, err := w.source(ctx, c, path, false)
	if err != nil {
		return err.Error()
	}
	after, err := w.source(ctx, c, path, true)
	if err != nil {
		return err.Error()
	}
	return before.refusal(after)
}

// apply writes m's edit into path (original is its text). It returns the
// function that restores the file, or nil and why the edit was not
// written.
func (w *worker) apply(m *Mutant, path string, original []byte) (func(), string) {
	info, err := os.Stat(path)
	if err != nil {
		w.err = errors.Join(w.err, err)
		return nil, err.Error()
	}
	mutated, err := anchor.Apply(m.File, original, m.Decl, m.From, m.To)
	if err != nil {
		return nil, err.Error()
	}
	w.touched[m.File] = true
	if err := os.WriteFile(path, mutated, info.Mode().Perm()); err != nil {
		w.err = errors.Join(w.err, err)
		return nil, err.Error()
	}
	return func() {
		if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("restore %s: %w", m.File, err))
		}
	}, ""
}

// applyChecked applies m and compiles it (compiles, with a .ergo row's
// source check). It returns nil and why when the edit was not written, and
// the restore with a detail when the mutated file does not compile.
func (w *worker) applyChecked(ctx context.Context, m *Mutant, path string, original []byte) (func(), string) {
	restore, detail := w.apply(m, path, original)
	if restore == nil {
		return nil, detail
	}
	if detail := w.compiles(ctx, m, path, original); detail != "" {
		return restore, detail
	}
	return w.regenerated(ctx, m, restore)
}

// regenerated runs a generator row's regenerate step on the mutated copy
// (MUT-REGEN), after its edit was applied with restore; the restore it
// returns also puts back what the step wrote. A row with no step, or a
// -compiles sweep, returns restore as it is.
func (w *worker) regenerated(ctx context.Context, m *Mutant, restore func()) (func(), string) {
	g := m.Regenerate
	if g == nil || w.e.compileOnly {
		return restore, ""
	}
	undo := func() {
		restore()
		if err := w.restoreUnder(g.Dir); err != nil {
			w.err = errors.Join(w.err, err)
		}
	}
	return undo, w.regenerate(ctx, g)
}

// regenerate runs the row's regenerate step in the worker's copy. It
// returns why it failed, or "".
func (w *worker) regenerate(ctx context.Context, g *Regenerate) string {
	if len(g.Argv) == 0 || g.Dir == "" || filepath.IsAbs(g.Dir) || strings.HasPrefix(path.Clean(g.Dir), "..") {
		return fmt.Sprintf("regenerate %q in %q: want a command and a directory inside the checkout", g.Argv, g.Dir)
	}
	o := w.e.exec(ctx, filepath.Join(w.root, filepath.FromSlash(g.Dir)), g.Argv, w.e.env(), w.e.cfg.CoverDeadline)
	if o.reason != "" {
		return fmt.Sprintf("regenerate %q in %s: %s: %s", g.Argv, g.Dir, o.reason, lastLines(o.tail, 20))
	}
	// verifyCopy checks that restoreUnder put back what the step wrote.
	for _, f := range w.e.files {
		if under(f.path, g.Dir) {
			w.touched[f.path] = true
		}
	}
	return ""
}

// under reports whether the checkout path p is in the folder dir: every
// path is in the module root ".", the folder of `go run ./scripts/linked`.
func under(p, dir string) bool {
	dir = path.Clean(dir)
	return dir == "." || strings.HasPrefix(p, dir+"/")
}

// restoreUnder puts every file of the checkout under dir back as the
// checkout has it, and removes the files a step wrote there that the
// checkout does not have.
func (w *worker) restoreUnder(dir string) error {
	// It removes files: only ever in a copy under the work directory.
	if !inside(w.root, w.e.cfg.Work) {
		return fmt.Errorf("restore %s: %s is not a copy under the work directory %s", dir, w.root, w.e.cfg.Work)
	}
	known := map[string]bool{}
	var errs []error
	for _, f := range w.e.files {
		if !under(f.path, dir) {
			continue
		}
		known[f.path] = true
		p := filepath.Join(w.root, filepath.FromSlash(f.path))
		if f.mode&fs.ModeSymlink != 0 {
			// A link of the checkout stays a link with its target: the
			// walk below removed every one it did not know (PREVIEW-TEST,
			// conformance/upstream/foldkit-d21db423/CLAUDE.md).
			if target, err := os.Readlink(p); err == nil && target == string(f.data) {
				continue
			}
			os.Remove(p)
			if err := os.Symlink(string(f.data), p); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if data, err := os.ReadFile(p); err == nil && bytes.Equal(data, f.data) {
			continue
		}
		if err := os.WriteFile(p, f.data, f.mode.Perm()|0o200); err != nil {
			errs = append(errs, err)
		}
	}
	base := filepath.Join(w.root, filepath.FromSlash(dir))
	errs = append(errs, filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		// The copy's own repository (commitCopy) is no file of the
		// checkout's, and stays.
		if err == nil && d.IsDir() && p == filepath.Join(w.root, ".git") {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(w.root, p)
		if err == nil && !known[filepath.ToSlash(rel)] {
			err = os.Remove(p)
		}
		return err
	}))
	return errors.Join(errs...)
}

// CompileRows applies each selected row in a copy and compiles it, as judge
// does before it runs a test, and runs no test and no coverage pass
// (-compiles): a sweep of every ledger for rows that do not compile.
func CompileRows(ctx context.Context, cfg Config, jobs []Job) ([]Result, error) {
	e, err := newEngine(cfg)
	if err != nil {
		return nil, err
	}
	e.compileOnly = true
	var ledgers []string
	for _, j := range jobs {
		ledgers = append(ledgers, j.Path)
	}
	if err := e.load(ledgers); err != nil {
		return nil, err
	}
	return e.judgeAll(ctx, jobs)
}
