package sketchfront

// The loader (196b U3): how source on disk becomes a program of packages.
//
//   - A file named ergo.mod marks the module root. Its one line,
//     `module NAME`, names the module; NAME is an import path whose first
//     element is not a std package path or prelude (Q1).
//   - A package is a folder: every *.ergo file directly in it, in byte
//     order of the names; a name that starts with `.` or `_` is ignored.
//     Subfolders are other packages (Q2).
//   - A local package is imported by the module name and its folder path
//     from the module root (`import app/lib`). A std path always names std.
//     A program with no ergo.mod above it imports std only.
//   - An import cycle is S108 at the import that closes it, found before
//     anything is checked (Q4).
//   - Each package is checked once, in import dependency order, through
//     the loader's Importer; a package whose dependency has diagnostics is
//     not checked, as the go command does.
//   - Every diagnostic names its file relative to the module root (Q10).
//
// Prior art: Go's go.mod `module` path and its folder packages; Gleam's
// gleam.toml names the project and a module path is its file path under
// src/. Both refuse import cycles.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	sourcefile "ergo.local/prototype/internal/source"
)

// ModFile is the file that marks a module root (196b Q1).
const ModFile = "ergo.mod"

// FS is the file system the loader reads. Production reads the host's
// (CheckDir); a test can list a directory's entries in another order.
type FS interface {
	// ReadDir lists a directory's entries, in any order.
	ReadDir(dir string) ([]fs.DirEntry, error)
	// ReadFile reads a regular file.
	ReadFile(name string) ([]byte, error)
	// Stat describes a file, following links.
	Stat(name string) (fs.FileInfo, error)
}

// hostFS is the host file system. A source is read only when it is a
// regular file, before and after it is opened (internal/source).
type hostFS struct{}

func (hostFS) ReadDir(dir string) ([]fs.DirEntry, error) { return os.ReadDir(dir) }
func (hostFS) ReadFile(name string) ([]byte, error)      { return sourcefile.ReadRegularFile(name, 0) }
func (hostFS) Stat(name string) (fs.FileInfo, error)     { return os.Stat(name) }

// loadedFile is one source file of a loaded package.
type loadedFile struct {
	// rel is the slash-separated path relative to the module root: the
	// File of its diagnostics.
	rel string
	// abs is the admitted absolute path: //line directives and the build
	// key name it.
	abs  string
	text string
	// syntax is nil when the file does not parse.
	syntax *syntax.File
}

// localImport is the first import of a local package in a package, by
// file order then source order.
type localImport struct {
	path string
	file int
	spec *syntax.ImportSpec
}

// loadedPackage is one package folder of the program.
type loadedPackage struct {
	path    string // the import path
	dir     string // absolute
	files   []loadedFile
	imports []localImport
	pkg     *check.Package
	// broken is a package with diagnostics, or one that imports such a
	// package: it is not checked, and nothing it reaches is built.
	broken bool
}

// program is the loaded program of a directory: the root package and the
// local packages it reaches, in import dependency order.
type program struct {
	root    *loadedPackage
	imports []*loadedPackage
	// texts holds the text of every file a diagnostic can name, by its
	// path relative to the module root, ergo.mod included.
	texts map[string]string
}

// loader reads and checks one program.
type loader struct {
	fsys   FS
	root   string // the module root, or the root package's folder
	module string // the module name; empty with no ergo.mod
	pkgs   map[string]*loadedPackage
	// missing holds the reason of each local import path with no package.
	missing map[string]string
	state   map[*loadedPackage]int
	order   []*loadedPackage
	diags   []report.Diagnostic
	texts   map[string]string
}

const (
	unvisited = iota
	visiting
	visited
)

// CheckDir reads the package in the folder dir, an absolute path, and the
// local packages it imports, and checks them. An error is a failure to
// read the program, not a diagnostic: a folder with no .ergo file, or an
// unreadable file.
func CheckDir(dir string) (Analysis, error) {
	return checkDir(hostFS{}, dir)
}

func checkDir(fsys FS, dir string) (Analysis, error) {
	if !filepath.IsAbs(dir) {
		return Analysis{}, fmt.Errorf("sketchfront: the package folder %s is not absolute", dir)
	}
	dir = filepath.Clean(dir)
	l := &loader{fsys: fsys, root: dir, pkgs: map[string]*loadedPackage{}, missing: map[string]string{}, state: map[*loadedPackage]int{}, texts: map[string]string{}}
	found, err := l.findModule(dir)
	if err != nil {
		return Analysis{}, err
	}
	rootPath := "main"
	if found && l.module != "" {
		rootPath = l.module
		if rel, _ := filepath.Rel(l.root, dir); rel != "." {
			rootPath += "/" + filepath.ToSlash(rel)
		}
	}
	if !found || l.module != "" {
		root, err := l.read(rootPath, dir)
		if err != nil {
			return Analysis{}, err
		}
		if root == nil {
			return Analysis{}, fmt.Errorf("%s: %s", dir, l.missing[rootPath])
		}
		if err := l.visit(root, nil); err != nil {
			return Analysis{}, err
		}
		if len(l.diags) == 0 {
			l.checkAll()
		}
	}
	p := &program{texts: l.texts}
	if len(l.order) > 0 {
		p.root = l.order[len(l.order)-1]
		p.imports = l.order[:len(l.order)-1]
	}
	a := Analysis{Diagnostics: l.diags, program: p}
	if p.root != nil && !p.root.broken && len(l.diags) == 0 {
		a.pkg = p.root.pkg
	}
	return a, nil
}

// findModule finds the nearest ergo.mod at or above dir and reads the
// module name. A malformed ergo.mod is a diagnostic in it, and the module
// name stays empty.
func (l *loader) findModule(dir string) (bool, error) {
	for d := dir; ; {
		name := filepath.Join(d, ModFile)
		info, err := l.fsys.Stat(name)
		switch {
		case err == nil && info.Mode().IsRegular():
			data, err := l.fsys.ReadFile(name)
			if err != nil {
				return false, err
			}
			l.root = d
			text := string(data)
			l.texts[ModFile] = text
			module, diagnostic := parseModule(text)
			if diagnostic != nil {
				diagnostic.File = ModFile
				l.diags = append(l.diags, *diagnostic)
				return true, nil
			}
			l.module = module
			return true, nil
		case err == nil:
			return false, fmt.Errorf("%s is not a regular file", name)
		case !errors.Is(err, fs.ErrNotExist):
			return false, err
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false, nil
		}
		d = parent
	}
}

// parseModule reads an ergo.mod: blank lines and one line `module NAME`.
func parseModule(text string) (string, *report.Diagnostic) {
	lines := lineStarts(text)
	module, at := "", -1
	for i, start := range lines {
		end := len(text)
		if i+1 < len(lines) {
			end = lines[i+1] - 1
		}
		line := text[start:end]
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if at >= 0 || len(fields) != 2 || fields[0] != "module" {
			return "", modDiagnostic(text, lines, i+1, 1, "ergo.mod holds one line, module NAME, and nothing else")
		}
		module, at = fields[1], i
		col := strings.Index(line, module) + 1
		if reason := modulePathProblem(module); reason != "" {
			return "", modDiagnostic(text, lines, i+1, col, reason)
		}
	}
	if at < 0 {
		return "", modDiagnostic(text, lines, 1, 1, "ergo.mod holds one line, module NAME; it names no module")
	}
	return module, nil
}

func modDiagnostic(text string, lines []int, line, col int, message string) *report.Diagnostic {
	return &report.Diagnostic{Code: check.CodeType, Message: message, Span: span(text, lines, line, col)}
}

// modulePathProblem is why a module name is not a module path: an import
// path (grammar.md, Literals: nonempty elements of letters, digits, `_`
// and `-`, each starting with a letter or `_`, separated by `/` or `.`)
// whose first element is not a std package path or prelude, so a local
// package never shadows std.
func modulePathProblem(module string) string {
	for _, element := range strings.FieldsFunc(module, func(r rune) bool { return r == '/' || r == '.' }) {
		for i, r := range element {
			if !(r == '_' || unicode.IsLetter(r) || i > 0 && (unicode.IsDigit(r) || r == '-')) {
				return fmt.Sprintf("the module path %s is not an import path: each element starts with a letter or _ and holds letters, digits, _ and -", module)
			}
		}
	}
	if strings.HasPrefix(module, "/") || strings.HasSuffix(module, "/") || strings.HasPrefix(module, ".") || strings.HasSuffix(module, ".") ||
		strings.Contains(module, "//") || strings.Contains(module, "..") || strings.Contains(module, "./") || strings.Contains(module, "/.") {
		return fmt.Sprintf("the module path %s is not an import path: its elements are not empty", module)
	}
	first, _, _ := strings.Cut(module, "/")
	if _, isStd := std.Package(first); isStd || first == std.PreludePath {
		return fmt.Sprintf("the module path %s starts with the std package %s, which a std import always names; choose another module name", module, first)
	}
	return ""
}

// local is the folder of an import path inside the module, if it names
// one.
func (l *loader) local(path string) (string, bool) {
	if l.module == "" {
		return "", false
	}
	if path == l.module {
		return l.root, true
	}
	rel, ok := strings.CutPrefix(path, l.module+"/")
	if !ok {
		return "", false
	}
	for _, element := range strings.Split(rel, "/") {
		if element == "" || element == "." || element == ".." || strings.ContainsAny(element, `\`) {
			return "", false
		}
	}
	return filepath.Join(l.root, filepath.FromSlash(rel)), true
}

// nestedModule is why the folder dir is not in the program's module: a
// folder below the module root, dir itself or one above it, that holds its
// own ergo.mod ends the module there, as a nested go.mod ends a Go module
// (go.dev/ref/mod). Imports across modules are S104 in v1 (196b Q1). It is
// empty for a folder of the module.
func (l *loader) nestedModule(dir string) string {
	for d := dir; d != l.root && strings.HasPrefix(d, l.root+string(filepath.Separator)); d = filepath.Dir(d) {
		name := filepath.Join(d, ModFile)
		info, err := l.fsys.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		rel := l.rel(d)
		if data, err := l.fsys.ReadFile(name); err == nil {
			if module, problem := parseModule(string(data)); problem == nil {
				return fmt.Sprintf("the folder %s is module %s (%s/%s); a program imports std and its own module only", rel, module, rel, ModFile)
			}
		}
		return fmt.Sprintf("the folder %s holds its own %s, so it is not in module %s; a program imports std and its own module only", rel, ModFile, l.module)
	}
	return ""
}

// read reads the package folder dir as the package path: its .ergo files
// in byte order of their names, each parsed and named, and its local
// imports. A folder with no .ergo file is no package: nil, and the reason
// is recorded for the importer.
func (l *loader) read(path, dir string) (*loadedPackage, error) {
	if p, ok := l.pkgs[path]; ok {
		return p, nil
	}
	relDir := l.rel(dir)
	entries, err := l.fsys.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		l.missing[path] = fmt.Sprintf("no folder %s in module %s", relDir, l.module)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".ergo") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		l.missing[path] = fmt.Sprintf("the folder %s holds no .ergo file", relDir)
		return nil, nil
	}
	// The listing's order is the host's; the package's is the names'.
	slices.Sort(names)
	p := &loadedPackage{path: path, dir: dir}
	l.pkgs[path] = p
	seen := map[string]bool{}
	for i, name := range names {
		abs := filepath.Join(dir, name)
		data, err := l.fsys.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		f := loadedFile{rel: l.rel(abs), abs: abs, text: string(data)}
		l.texts[f.rel] = f.text
		parsed, err := syntax.Parse(f.text)
		if err != nil {
			d := syntaxDiagnostic(err)
			d.File = f.rel
			l.diags = append(l.diags, d)
			p.broken = true
		} else {
			parsed.Name = name
			f.syntax = parsed
			for _, spec := range importSpecs(parsed) {
				target := importPath(spec)
				if _, ok := l.local(target); ok && !seen[target] {
					seen[target] = true
					p.imports = append(p.imports, localImport{path: target, file: i, spec: spec})
				}
			}
		}
		p.files = append(p.files, f)
	}
	return p, nil
}

// rel is a path relative to the module root, slash-separated.
func (l *loader) rel(name string) string {
	rel, err := filepath.Rel(l.root, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(rel)
}

// importSpecs are a file's import specs, in source order.
func importSpecs(f *syntax.File) []*syntax.ImportSpec {
	var out []*syntax.ImportSpec
	for _, d := range f.Decls {
		if d, ok := d.(*syntax.ImportDecl); ok {
			for _, s := range d.Specs {
				if s.Path != nil && s.Kind != syntax.ImportGo {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// importPath is the path an import spec names; "" for a malformed one,
// which the checker reports.
func importPath(s *syntax.ImportSpec) string {
	if s.Path.Kind == syntax.PATH {
		return s.Path.Value
	}
	p, err := strconv.Unquote(s.Path.Value)
	if err != nil {
		return ""
	}
	return p
}

// visit reads the local packages p imports, depth first, and appends p to
// the dependency order after them. An import of a package on the stack
// closes a cycle: S108 at that import.
func (l *loader) visit(p *loadedPackage, stack []*loadedPackage) error {
	l.state[p] = visiting
	stack = append(stack, p)
	for _, imp := range p.imports {
		dir, _ := l.local(imp.path)
		if reason := l.nestedModule(dir); reason != "" {
			l.missing[imp.path] = reason
			continue
		}
		q, err := l.read(imp.path, dir)
		if err != nil {
			return err
		}
		if q == nil {
			continue
		}
		switch l.state[q] {
		case visiting:
			l.cycle(p, imp, q, stack)
		case unvisited:
			if err := l.visit(q, stack); err != nil {
				return err
			}
		}
	}
	l.state[p] = visited
	l.order = append(l.order, p)
	return nil
}

// cycle reports the import imp of p that closes a cycle back to q.
func (l *loader) cycle(p *loadedPackage, imp localImport, q *loadedPackage, stack []*loadedPackage) {
	start := slices.Index(stack, q)
	var names []string
	for _, s := range stack[start:] {
		names = append(names, s.path)
	}
	names = append(names, q.path)
	f := p.files[imp.file]
	line, col := f.syntax.Position(imp.spec.Path.Pos())
	l.diags = append(l.diags, report.Diagnostic{
		Code:    check.CodeType,
		File:    f.rel,
		Message: "import cycle: " + strings.Join(names, " imports "),
		Span:    span(f.text, lineStarts(f.text), line, col),
	})
	p.broken = true
}

// checkAll checks each package once, in dependency order. A package that
// imports a broken package is broken and not checked.
func (l *loader) checkAll() {
	for i, p := range l.order {
		for _, imp := range p.imports {
			if q := l.pkgs[imp.path]; q != nil && q.broken {
				p.broken = true
			}
		}
		if p.broken {
			continue
		}
		files := make([]*syntax.File, len(p.files))
		for k, f := range p.files {
			files[k] = f.syntax
		}
		path := p.path
		if i == len(l.order)-1 {
			path = "main" // the root package is the program's main package
		}
		pkg, ds := check.Check(path, files, l)
		p.pkg = pkg
		for _, d := range ds {
			l.diags = append(l.diags, report.Diagnostic{
				Code:    d.Code,
				File:    p.files[d.File].rel,
				Message: d.Message,
				Span:    report.Span{Offset: d.Offset, Length: d.Length, Line: d.Line, Column: d.Column},
			})
		}
		if len(ds) > 0 {
			p.broken = true
		}
	}
}

// Import is the loader's check.Importer: std, then the checked local
// packages; any other path is refused with its reason.
func (l *loader) Import(path string) (*check.Package, error) {
	if _, isStd := std.Package(path); isStd {
		return check.Std.Import(path)
	}
	if p := l.pkgs[path]; p != nil && p.pkg != nil {
		return p.pkg, nil
	}
	if reason, ok := l.missing[path]; ok {
		return nil, &check.ImportError{Path: path, Reason: reason}
	}
	if l.module == "" {
		return nil, &check.ImportError{Path: path, Reason: "no " + ModFile + " above the program's folder declares a module, so the program imports std only"}
	}
	return nil, &check.ImportError{Path: path, Reason: fmt.Sprintf("it is not std and not in module %s; a program imports std and its own module only", l.module)}
}

// Text is the analysed text: the source of a program of one file, or for
// a program read from a folder a canonical encoding of every file it read
// (each relative path and text, length-prefixed, by path), so its revision
// identifies the program's sources.
func (a Analysis) Text() string {
	if a.program == nil {
		return a.source
	}
	var b strings.Builder
	for _, name := range sortedNames(a.program.texts) {
		fmt.Fprintf(&b, "%d:%s%d:", len(name), name, len(a.program.texts[name]))
		b.WriteString(a.program.texts[name])
	}
	return b.String()
}

// Files is the text of every file a diagnostic of a program read from a
// folder can name, by its path relative to the module root; nil for a
// program of one file.
func (a Analysis) Files() map[string]string {
	if a.program == nil {
		return nil
	}
	return a.program.texts
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// unit is the lowering's input of a program read from a folder: the root
// package and every local package it reaches.
func (p *program) unit() lower.Unit {
	u := packageUnit(p.root)
	for _, q := range p.imports {
		u.Imports = append(u.Imports, packageUnit(q))
	}
	return u
}

func packageUnit(p *loadedPackage) lower.Unit {
	u := lower.Unit{Package: p.pkg}
	for _, f := range p.files {
		u.Files = append(u.Files, f.syntax)
		u.Sources = append(u.Sources, lower.Source{Path: f.abs, Text: f.text})
	}
	return u
}

// dependencies summarise the local packages of the program for the build
// key, each by the digest of its files' admitted paths and bytes, as std
// packages are summarised.
func (p *program) dependencies() []build.Dependency {
	var out []build.Dependency
	for _, q := range p.imports {
		files := map[string]string{}
		for _, f := range q.files {
			files[f.abs] = f.text
		}
		out = append(out, sourceDependency(q.path, files))
	}
	return out
}

// file is the loaded file at an admitted path.
func (p *program) file(abs string) (loadedFile, bool) {
	for _, q := range append([]*loadedPackage{p.root}, p.imports...) {
		for _, f := range q.files {
			if f.abs == abs {
				return f, true
			}
		}
	}
	return loadedFile{}, false
}
