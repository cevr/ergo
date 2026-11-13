// Package fix is ergo fix: it rewrites source that an older rule of the
// language accepted into the spelling the language now requires, so a
// migration changes no verdict.
//
// It writes the declared implementations (audit CSX §4.10; types.md,
// Interface assignability 1): for every pair the old method-set rule
// admitted (a conversion, a constraint, a law provider, a release by the
// name Close), the clause `implements I` on the type, or the include
// `..I` in the operation type. The checker reports the pairs
// (check.RecordLegacy); this package locates each declaration in its
// source, spells the operation type as the declaring file sees it, and
// edits the text. A pair whose operation type the declaring package does
// not import is not written: it is reported, with its repair (import the
// operation type's package, or wrap the value in a record that implements
// it). The fix is idempotent: a declared pair is never reported again.
package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// Options select what Run reads and writes.
type Options struct {
	// Paths are files and folders. A folder is walked: each module (a
	// folder with ergo.mod) is read package by package, and each other
	// .ergo file is read as a program of its own. Files under a gostd
	// folder (generated Go bindings) are never edited.
	Paths []string
	// StdRoot, when set, is the source folder of the embedded standard
	// library (internal/sketch/std): every std package is checked, and
	// its pairs are written into its files there when StdRoot is under
	// one of Paths.
	StdRoot string
	// Write writes the edited files; else Run only reports (a dry run).
	Write bool
}

// Written is one clause or include the fix writes.
type Written struct {
	File string // the edited file
	Type string // the declaring type
	Text string // `implements I` or `..I`
	Site string // the old rule's readers that used the pair
}

// Unwritten is a pair the fix could not write, with why.
type Unwritten struct {
	Pair string
	Why  string
}

// Report is what Run found and did.
type Report struct {
	Written   []Written
	Unwritten []Unwritten
	// Files are the edited files' new texts.
	Files map[string]string
}

type pairKey struct {
	pkg, typ, op string
	include      bool
	file         string // the program's file, for a program read alone
}

type pairRec struct {
	pair  check.LegacyPair
	sites []string
	// where is the folder of the type's package, or the one file of a
	// program read alone.
	where string
	alone bool
}

// Run checks every program of opts.Paths under the old rule, and writes or
// reports each pair it admitted that no clause declares.
func Run(opts Options) (Report, error) {
	rep := Report{Files: map[string]string{}}
	pairs := map[pairKey]*pairRec{}
	var order []pairKey
	where, alone := "", false
	pkgDirs := map[string]string{}
	restore := check.RecordLegacy(func(p check.LegacyPair) {
		op := "?"
		if p.Op != nil {
			op = p.Op.String()
		}
		w, a := where, alone
		if p.Pkg != "main" || !alone {
			w, a = "", false
		}
		k := pairKey{p.Pkg, p.Type.Name, op, p.Include, w}
		r := pairs[k]
		if r == nil {
			r = &pairRec{pair: p, where: w, alone: a}
			pairs[k] = r
			order = append(order, k)
		}
		if !slices.Contains(r.sites, p.Site) {
			r.sites = append(r.sites, p.Site)
		}
	})
	defer restore()
	var roots []string
	for _, p := range opts.Paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return rep, err
		}
		roots = append(roots, abs)
	}
	stdRoot := ""
	if opts.StdRoot != "" {
		abs, err := filepath.Abs(opts.StdRoot)
		if err != nil {
			return rep, err
		}
		stdRoot = abs
		for _, path := range std.Packages() {
			if _, err := check.Std.Import(path); err != nil {
				return rep, fmt.Errorf("std %s: %w", path, err)
			}
		}
	}
	for _, root := range roots {
		if err := walk(root, func(file string) {
			src, err := os.ReadFile(file)
			if err != nil {
				return
			}
			where, alone = file, true
			sketchfront.Check(string(src))
			where, alone = "", false
		}, func(module string) {
			targets, err := sketchfront.DiscoverPackages(module)
			if err != nil {
				return
			}
			for _, t := range targets {
				a, err := sketchfront.CheckDir(t.Dir)
				if err != nil {
					continue
				}
				for _, p := range a.Packages() {
					pkgDirs[p.Path] = p.Dir
				}
			}
		}); err != nil {
			return rep, err
		}
	}
	restore()
	under := func(file string) bool {
		if strings.Contains(filepath.ToSlash(file), "/gostd/") {
			return false
		}
		for _, r := range roots {
			if file == r || strings.HasPrefix(file, r+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	ed := &editor{texts: map[string]string{}, parsed: map[string]*syntax.File{}}
	for _, k := range order {
		r := pairs[k]
		p := r.pair
		desc := p.String()
		if p.Op == nil {
			rep.Unwritten = append(rep.Unwritten, Unwritten{desc, "no instance of the operation type at the type's own parameters (generic), or an include whose operations differ: write it by hand"})
			continue
		}
		if p.Type.Pkg != nil && p.Type.Pkg.Host() {
			rep.Unwritten = append(rep.Unwritten, Unwritten{desc, "a Go-bound type: wrap it in a record that implements the operation type"})
			continue
		}
		var files []string
		switch {
		case r.alone:
			files = []string{r.where}
		case p.Pkg == std.PreludePath && stdRoot != "":
			files = []string{filepath.Join(stdRoot, "prelude.ergo")}
		case pkgDirs[p.Pkg] != "":
			files = ergoFiles(pkgDirs[p.Pkg])
		case stdRoot != "" && slices.Contains(std.Packages(), p.Pkg):
			files = ergoFiles(filepath.Join(stdRoot, filepath.FromSlash(p.Pkg)))
		}
		file, decl := ed.find(files, p.Type.Name)
		if decl == nil {
			rep.Unwritten = append(rep.Unwritten, Unwritten{desc, "its declaration was not found under the fixed paths"})
			continue
		}
		if !under(file) {
			continue
		}
		pkgFiles := files
		if r.alone {
			pkgFiles = []string{file}
		}
		text, imp, why := ed.spell(file, pkgFiles, p.Pkg, p.Op)
		if why != "" {
			rep.Unwritten = append(rep.Unwritten, Unwritten{desc, why})
			continue
		}
		if p.Include {
			if err := ed.include(file, decl, text, p.Op); err != nil {
				rep.Unwritten = append(rep.Unwritten, Unwritten{desc, err.Error()})
				continue
			}
			rep.Written = append(rep.Written, Written{file, p.Type.Name, ".." + text, strings.Join(r.sites, ", ")})
		} else {
			ed.clause(file, decl, text)
			rep.Written = append(rep.Written, Written{file, p.Type.Name, "implements " + text, strings.Join(r.sites, ", ")})
		}
		if imp != "" {
			ed.addImport(file, imp)
		}
	}
	for file := range ed.edits {
		out, err := ed.apply(file)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", file, err)
		}
		rep.Files[file] = out
		if opts.Write {
			if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
				return rep, err
			}
		}
	}
	sort.Slice(rep.Written, func(i, j int) bool {
		if rep.Written[i].File != rep.Written[j].File {
			return rep.Written[i].File < rep.Written[j].File
		}
		return rep.Written[i].Type < rep.Written[j].Type
	})
	return rep, nil
}

// walk calls file for each .ergo file under root that no module holds, and
// module for each module folder. A folder named gostd or starting with a
// dot is skipped.
func walk(root string, file func(string), module func(string)) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if strings.HasSuffix(root, ".ergo") {
			file(root)
		}
		return nil
	}
	packaged := map[string]bool{}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "gostd") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, sketchfront.ModFile)); err == nil {
				module(path)
				if targets, err := sketchfront.DiscoverPackages(path); err == nil {
					for _, t := range targets {
						packaged[t.Dir] = true
					}
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".ergo") && !packaged[filepath.Dir(path)] && !packaged[evalDir(filepath.Dir(path))] {
			file(path)
		}
		return nil
	})
}

func evalDir(dir string) string {
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		return d
	}
	return dir
}

// ergoFiles are the .ergo files of a package folder, in name order.
func ergoFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// Text prints the report: each written clause, then each pair not written
// with why.
func (r Report) Text() string {
	var b strings.Builder
	for _, w := range r.Written {
		fmt.Fprintf(&b, "wrote %s: %s %s (%s)\n", w.File, w.Type, w.Text, w.Site)
	}
	for _, u := range r.Unwritten {
		fmt.Fprintf(&b, "not written %s: %s\n", u.Pair, u.Why)
	}
	fmt.Fprintf(&b, "%d written in %d files, %d not written\n", len(r.Written), len(r.Files), len(r.Unwritten))
	return b.String()
}
