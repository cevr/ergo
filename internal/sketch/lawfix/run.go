package lawfix

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// Options is one ergo fix run of the law pass.
type Options struct {
	// Paths are package folders; a path ending in /... is every package
	// of its module below it, dependencies first.
	Paths []string
	// StdRoot is the folder of the standard library's source
	// (internal/sketch/std): each of its packages is checked from there
	// as std, and edited there. Empty for none.
	StdRoot string
	// Write writes the edits; false reports them only (--dry-run).
	Write bool
}

// Report lists what a run renamed, moved, deleted and could not place.
type Report struct {
	Renames  []string
	Moves    []string
	Deleted  []string
	Unplaced []string
}

// Text is the report as ergo fix prints it.
func (r Report) Text() string {
	var b strings.Builder
	for _, m := range r.Renames {
		fmt.Fprintln(&b, m)
	}
	for _, m := range r.Moves {
		fmt.Fprintln(&b, m)
	}
	for _, d := range r.Deleted {
		fmt.Fprintf(&b, "deleted %s: no declaration is left in it\n", d)
	}
	for _, u := range r.Unplaced {
		fmt.Fprintln(&b, u)
	}
	return b.String()
}

// checked is one package checked from disk: its folder, package, files and
// the diagnostics other than S115.
type checked struct {
	dir   string
	pkg   *check.Package
	files []*syntax.File
	diags int
}

// Run fixes the law placement of every package Options names. Each
// package is checked, its exported law helper candidates that no other
// package names are unexported (Unexport) when that adds no diagnostic
// (never in std, whose surface every program sees),
// and its laws are moved (Plan).
func Run(opts Options) (Report, error) {
	var rep Report
	var scan []string
	if opts.StdRoot != "" {
		scan = append(scan, repoRoot(opts.StdRoot))
	}
	var dirs []string
	for _, path := range opts.Paths {
		if filepath.Base(path) == "..." {
			pkgs, err := sketchfront.DiscoverPackages(filepath.Dir(path))
			if err != nil {
				return rep, err
			}
			for _, p := range pkgs {
				dirs = append(dirs, p.Dir)
			}
			scan = append(scan, moduleRoot(filepath.Dir(path)))
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return rep, err
		}
		dirs = append(dirs, abs)
		scan = append(scan, moduleRoot(abs))
	}
	names, err := selectorNames(scan)
	if err != nil {
		return rep, err
	}
	if opts.StdRoot != "" {
		for _, p := range stdlib.Packages() {
			dir, err := filepath.Abs(filepath.Join(opts.StdRoot, filepath.FromSlash(p)))
			if err != nil {
				return rep, err
			}
			load := func() (checked, bool, error) {
				files, err := readDir(dir)
				if err != nil || len(files) == 0 {
					return checked{}, false, err
				}
				pkg, ds := check.StdFixture(p, files)
				n := 0
				for _, d := range ds {
					if d.Code != check.CodeLawPlace {
						n++
					}
				}
				return checked{dir, pkg, files, n}, true, nil
			}
			if err := rep.fix(load, names, opts.Write, false); err != nil {
				return rep, err
			}
		}
	}
	stdAbs := ""
	if opts.StdRoot != "" {
		stdAbs, _ = filepath.Abs(opts.StdRoot)
	}
	for _, dir := range dirs {
		if stdAbs != "" && (dir == stdAbs || strings.HasPrefix(dir, stdAbs+string(filepath.Separator))) {
			continue
		}
		load := func() (checked, bool, error) {
			a, err := sketchfront.CheckDir(dir)
			if err != nil {
				return checked{}, false, err
			}
			root, pkg, files, ok := a.RootPackage()
			if !ok {
				rep.Unplaced = append(rep.Unplaced, fmt.Sprintf("skipped %s: it was not checked (a parse error or a broken import)", dir))
				return checked{}, false, nil
			}
			n := 0
			for _, d := range a.Diagnostics {
				if d.Code != check.CodeLawPlace {
					n++
				}
			}
			return checked{root, pkg, files, n}, true, nil
		}
		if err := rep.fix(load, names, opts.Write, true); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// fix renames, then moves, in one package. Std never unexports: every
// program may name its exported functions.
func (r *Report) fix(load func() (checked, bool, error), names map[string]map[string]bool, write, unexport bool) error {
	c, ok, err := load()
	if err != nil || !ok {
		return err
	}
	var cands []*syntax.FuncDecl
	fileOf := map[syntax.Decl]string{}
	for _, f := range c.files {
		for _, d := range f.Decls {
			fileOf[d] = f.Name
		}
	}
	for _, d := range c.pkg.LawExportedHelpers {
		// Only a laws file's exported helpers were written for its laws
		// (laws.ergo, tree_laws.ergo); elsewhere a function only tests use
		// is still the package's surface.
		if !strings.Contains(strings.ToLower(filepath.Base(fileOf[d])), "law") {
			continue
		}
		outside := false
		for dir := range names[d.Name.Name] {
			if dir != c.dir {
				outside = true
			}
		}
		if !outside {
			cands = append(cands, d)
		}
	}
	if len(cands) > 0 && unexport {
		texts, renames := Unexport(c.pkg, c.files, cands)
		if len(texts) > 0 {
			if !write {
				for _, rn := range renames {
					r.Renames = append(r.Renames, fmt.Sprintf("would unexport %s in %s", rn, c.dir))
				}
			} else {
				old := map[string]string{}
				for _, f := range c.files {
					old[f.Name] = f.Src
				}
				if err := writeFiles(c.dir, texts); err != nil {
					return err
				}
				n, ok, err := load()
				if err != nil {
					return err
				}
				if !ok || n.diags > c.diags {
					// The rename added a diagnostic (a local of the new
					// name, say): put the files back and keep the names.
					back := map[string]string{}
					for name := range texts {
						back[name] = old[name]
					}
					if err := writeFiles(c.dir, back); err != nil {
						return err
					}
					r.Unplaced = append(r.Unplaced, fmt.Sprintf("kept the names of %s in %s: unexporting them adds a diagnostic", strings.Join(renames, ", "), c.dir))
				} else {
					for _, rn := range renames {
						r.Renames = append(r.Renames, fmt.Sprintf("unexported %s in %s", rn, c.dir))
					}
					c = n
				}
			}
		}
	}
	return r.apply(c.dir, c.pkg, c.files, write)
}

func writeFiles(dir string, texts map[string]string) error {
	names := make([]string, 0, len(texts))
	for name := range texts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(texts[name]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (r *Report) apply(dir string, pkg *check.Package, files []*syntax.File, write bool) error {
	res, err := Plan(pkg, files)
	if err != nil {
		return fmt.Errorf("%s: %v", dir, err)
	}
	for _, m := range res.Moves {
		line := fmt.Sprintf("moved law %s from %s to %s", m.Law, filepath.Join(dir, m.From), filepath.Join(dir, m.To))
		if m.From == m.To {
			line = fmt.Sprintf("law %s in %s: moved its helpers beside it", m.Law, filepath.Join(dir, m.From))
		}
		if len(m.Helpers) > 0 {
			line += " with " + strings.Join(m.Helpers, ", ")
		}
		r.Moves = append(r.Moves, line)
	}
	for _, h := range res.Helpers {
		r.Moves = append(r.Moves, fmt.Sprintf("moved %s (%s)", h, dir))
	}
	// A renamed file's old name is removed too (Result.Deleted), but its
	// declarations live on under the new one: the report says renamed.
	renamed := map[string]bool{}
	for _, rn := range res.Renamed {
		r.Moves = append(r.Moves, fmt.Sprintf("renamed %s (%s)", rn, dir))
		from, _, _ := strings.Cut(rn, " -> ")
		renamed[from] = true
	}
	for _, u := range res.Unplaced {
		r.Unplaced = append(r.Unplaced, fmt.Sprintf("law %s in %s names nothing in its package; state it in the package it is about", u, dir))
	}
	if write {
		if err := writeFiles(dir, res.Files); err != nil {
			return err
		}
	}
	for _, name := range res.Deleted {
		path := filepath.Join(dir, name)
		if !renamed[name] {
			r.Deleted = append(r.Deleted, path)
		}
		if write {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

var selector = regexp.MustCompile(`\.([A-Z][A-Za-z0-9_]*)`)

// selectorNames maps each exported name that follows a dot in a .ergo or
// .go file under roots to the folders of those files: a name another
// package may use. It over-approximates (a field or a method of that
// name counts), which only keeps a name exported.
func selectorNames(roots []string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if n := d.Name(); path != root && (n == ".git" || n == "node_modules" || n == "tmp" || n == "dist") {
					return filepath.SkipDir
				}
				return nil
			}
			if ext := filepath.Ext(path); ext != ".ergo" && ext != ".go" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			dir := filepath.Dir(path)
			if filepath.Ext(path) == ".go" {
				dir += "#go" // a Go file is never the package's own source
			}
			for _, m := range selector.FindAllSubmatch(data, -1) {
				n := string(m[1])
				if out[n] == nil {
					out[n] = map[string]bool{}
				}
				out[n][dir] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// moduleRoot is the nearest folder at or above dir that holds an ergo.mod,
// or dir.
func moduleRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, sketchfront.ModFile)); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return abs
		}
	}
}

// repoRoot is the nearest folder above dir that holds a go.mod, or dir.
func repoRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return abs
		}
	}
}

// readDir parses the .ergo files of dir, by name, as the loader reads a
// package.
func readDir(dir string) ([]*syntax.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*syntax.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".ergo") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		f, err := syntax.Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", filepath.Join(dir, name), err)
		}
		f.Name = name
		files = append(files, f)
	}
	return files, nil
}
