package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
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
	w.touched[m.File] = true
	if err := os.WriteFile(path, []byte(strings.Replace(string(original), m.From, m.To, 1)), info.Mode().Perm()); err != nil {
		w.err = errors.Join(w.err, err)
		return nil, err.Error()
	}
	return func() {
		if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("restore %s: %w", m.File, err))
		}
	}, ""
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
