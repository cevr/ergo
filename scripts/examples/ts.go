package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The ts leg runs the programs tagged ts: an Ergo program with a
// TypeScript side in its ts/ folder, which reads the program's .ergo
// contracts through tools/ts (the content mapper and the Bun loader). It
// needs Bun on PATH and a Bun cache that holds tools/ts's locked
// dependencies; a host without either fails the program, it never skips.
//
// The dependencies are installed once per run in the runner's scratch,
// outside the checkout (tools/ts/README.md, "Installing"), with
// --frozen-lockfile --offline, so the leg reads no network and no lock
// but tools/ts/bun.lock. Each case runs in a fresh copy of the program
// folder under that install, so a setup.sh may edit the copy.

// tsPackage is the package.json of a case's copy: without one, Bun's
// resolution from the copy reaches the install's own package.json, which
// is ergo-ts itself, and `ergo-ts/bun` would name a file of the install.
const tsPackage = `{ "name": "ergo-example-case", "private": true, "type": "module" }` + "\n"

// tsDeps is the scratch install of tools/ts's dependencies, made by the
// first ts program of the run, or why it could not be made.
func (r *runner) tsDeps() (string, error) {
	if r.tsDir != "" || r.tsErr != nil {
		return r.tsDir, r.tsErr
	}
	r.tsDir, r.tsErr = r.installTS()
	return r.tsDir, r.tsErr
}

func (r *runner) installTS() (string, error) {
	if _, err := exec.LookPath("bun"); err != nil {
		return "", errors.New("bun is not on PATH; the ts leg needs Bun and a warmed Bun cache (tools/ts/README.md)")
	}
	tools := filepath.Join(r.cfg.root, "tools", "ts")
	dir, err := r.live.makeDir(r.scratch, "ts-")
	if err != nil {
		return "", err
	}
	for _, file := range []string{"package.json", "bun.lock"} {
		if err := copyFile(filepath.Join(tools, file), filepath.Join(dir, file)); err != nil {
			return "", err
		}
	}
	o, err := r.live.execute([]string{"bun", "install", "--frozen-lockfile", "--offline", "--ignore-scripts"}, dir, os.Environ(), "", r.cfg.timeout, 0)
	if err != nil {
		return "", fmt.Errorf("bun install could not start: %v", err)
	}
	if !o.passed() {
		var out strings.Builder
		for _, line := range lines(append(o.stdout, o.stderr...)) {
			fmt.Fprintf(&out, "\n    %s", line)
		}
		return "", fmt.Errorf("bun install --frozen-lockfile --offline failed (exit %s%s); warm the Bun cache as tools/ts/README.md says%s", o.statusText(), o.why(r.cfg.timeout), out.String())
	}
	// ergo-ts itself: its loaders, beside the dependencies they import.
	pkg := filepath.Join(dir, "node_modules", "ergo-ts")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(tools)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".mjs") || name == "package.json" {
			if err := copyFile(filepath.Join(tools, name), filepath.Join(pkg, name)); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// tsCopy copies the program folder, without its cases, into a fresh
// folder under the install, and gives it its package.json.
func (r *runner) tsCopy(p *program, deps string) (string, error) {
	dir, err := r.live.makeDir(deps, "case-")
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(p.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(p.dir, path)
		if rel == "cases" {
			return filepath.SkipDir
		}
		target := filepath.Join(dir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		return copyFile(path, target)
	})
	if err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "package.json"), []byte(tsPackage), 0o644)
}

// tsRun is the TypeScript run of one program: the install its cases run
// under and the program the runner built.
type tsRun struct {
	deps   string
	binary string
}

// env is a case's environment for its TypeScript: the checkout's ergo
// first on PATH, for the content mapper, and as ERGO, for the loader; and
// the built program as ERGO_BIN, which the case's TypeScript starts.
func (t *tsRun) env(ergo string, env []string) []string {
	out := []string{}
	path := filepath.Dir(ergo)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path += string(os.PathListSeparator) + v
			continue
		}
		out = append(out, kv)
	}
	return append(append([]string{"PATH=" + path}, out...), "ERGO="+ergo, "ERGO_BIN="+t.binary)
}

// tsSources checks a program's ts/ folder: regular files and folders,
// and no .ergo file, which belongs to a package folder.
func tsSources(dir string) []string {
	var problems []string
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(filepath.Dir(dir), path)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case entry.IsDir():
		case !entry.Type().IsRegular():
			problems = append(problems, fmt.Sprintf("%s is not a regular file", filepath.ToSlash(rel)))
		case strings.HasSuffix(path, ".ergo"):
			problems = append(problems, fmt.Sprintf("unexpected %s; ts/ holds the TypeScript side, and .ergo files belong to package folders", filepath.ToSlash(rel)))
		}
		return nil
	})
	return problems
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}
