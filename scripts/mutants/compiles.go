package main

import (
	"context"
	"encoding/json"
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
// edit; WK's work rows). So after the mutant is applied, the package of
// the mutated file is compiled in the copy:
//   - a Go file: go build of its package, and go test -c when it is a test
//     file, in its module (the checkout's or a nested one);
//   - a std .ergo file (stdDir): stdcheck, built from the copy, checks the
//     prelude and every std package;
//   - another .ergo file in an Ergo module (an ergo.mod above it): the
//     copy's ergo check of its directory.
//
// A failure whose unmutated file fails the same way (a fixture that never
// compiles) is the baseline, not the mutant: the row is judged as before.
// Files of other kinds are not compiled. Limits: a package that imports
// the mutated one is not compiled (a mutant that changes an exported
// signature breaks its importers, which a test may then report); a Go
// file that a build constraint leaves out on this platform compiles as
// nothing.

// stdDir holds std's .ergo sources, embedded into the checker.
const stdDir = "internal/sketch/std/"

// goError is a compiler's error line: FILE.go:LINE[:COL]: message.
var goError = regexp.MustCompile(`(?m)^\S+\.go:\d+(:\d+)?: .*$`)

// compiles returns why the mutated file at path (m.File, applied) does not
// compile, or "" when it compiles, when its kind is not compiled, or when
// the unmutated file fails too. It leaves the mutant applied.
func (w *worker) compiles(ctx context.Context, m *Mutant, path string, original []byte) string {
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
	switch {
	case strings.HasSuffix(file, ".go"):
		return w.compileGo(ctx, file)
	case strings.HasSuffix(file, ".ergo") && strings.HasPrefix(file, stdDir):
		return w.compileStd(ctx)
	case strings.HasSuffix(file, ".ergo"):
		return w.compileErgo(ctx, file)
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

func (w *worker) compileStd(ctx context.Context) string {
	if _, err := os.Stat(filepath.Join(w.root, "scripts", "mutants", "stdcheck")); err != nil {
		return ""
	}
	bin := filepath.Join(w.bin, "stdcheck")
	if o := w.e.exec(ctx, w.root, []string{"go", "build", "-buildvcs=false", "-o", bin, "./scripts/mutants/stdcheck"}, w.e.env(), buildFloor); o.reason != "" {
		return goError.FindString(o.tail)
	}
	o := w.e.exec(ctx, w.root, []string{bin}, w.e.env(), buildFloor)
	if o.reason == "" {
		return ""
	}
	return firstLine(strings.TrimSpace(o.tail))
}

func (w *worker) compileErgo(ctx context.Context, file string) string {
	dir := path.Dir(file)
	found := false
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(d), "ergo.mod")); err == nil {
			found = true
			break
		}
	}
	if !found {
		return ""
	}
	if _, err := os.Stat(filepath.Join(w.root, "cmd", "ergo")); err != nil {
		return ""
	}
	bin := filepath.Join(w.bin, "ergo-compile")
	if o := w.e.exec(ctx, w.root, []string{"go", "build", "-buildvcs=false", "-o", bin, "./cmd/ergo"}, w.e.env(), buildFloor); o.reason != "" {
		return goError.FindString(o.tail)
	}
	o := w.e.exec(ctx, w.root, []string{bin, "check", "./" + dir}, w.e.env(), buildFloor)
	if o.reason == "" {
		return ""
	}
	return firstDiagnostic(o.tail)
}

// firstDiagnostic is the first diagnostic of ergo check's JSON report in
// out, as FILE:LINE:COL: CODE MESSAGE (FILE as ergo check names it), or
// "" when it names none: ergo check also fails when the checker's
// evaluation refutes a law or an assertion of a program that checks
// (x2-cancel-ended), which is a test's verdict, not a compile error.
func firstDiagnostic(out string) string {
	for _, line := range strings.Split(out, "\n") {
		var report struct {
			Diagnostics []struct {
				Code, File, Message string
				Span                struct{ Line, Column int }
			}
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "{") || json.Unmarshal([]byte(line), &report) != nil || len(report.Diagnostics) == 0 {
			continue
		}
		d := report.Diagnostics[0]
		return fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message)
	}
	// A report longer than the output's tail keeps its start.
	if d := ergoDiagnostic.FindStringSubmatch(out); d != nil {
		return fmt.Sprintf("%s: %s %s", d[2], d[1], d[3])
	}
	return ""
}

// ergoDiagnostic is the start of one diagnostic in ergo check's JSON.
var ergoDiagnostic = regexp.MustCompile(`"code":"([^"]*)","file":"([^"]*)","message":"((?:[^"\\]|\\.)*)"`)

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
