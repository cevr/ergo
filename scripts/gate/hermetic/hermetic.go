// Package hermetic checks that a Go test binary is hermetic: that it runs no
// program and loads no library which Go's test cache does not key.
//
// Go's test cache keys a package's result on the test binary, its flags, the
// environment variables the test process reads and the files inside the
// module that it opens. It records no program the tests run and no shared
// library a process loads. A test that runs seq, sh or git, a program chosen
// at run time, or a shell that finds a program on its own PATH, can replay a
// pass after that program or a library it loads has changed. No list of
// program names closes that class. A hermetic test binary excludes it by
// construction:
//
//   - The module's own packages in the binary run no program, except the test
//     binary itself, named by os.Executable() in one helper function per
//     package (a mode switch in the environment selects what it does).
//   - No other package in the binary imports os/exec, except the fuzzing
//     engine (internal/fuzz), which starts workers only under -test.fuzz.
//     Every standard-library package that starts a program does so through
//     os/exec; only os and syscall define the primitives.
//   - No package uses cgo or plugins, and the binary is built with
//     CGO_ENABLED=0, so it loads no shared library.
//
// Every input of a hermetic test is then a file, an environment variable or
// the module's source, and Go records all three.
//
// The check is static, and it decides what a name denotes from type
// information, never from its spelling: a local value named os, a dot import
// or a renamed import cannot pass for the os package. It type-checks the
// module's packages from source in each build configuration it is given
// (Build), with the standard library's types of that configuration, and every
// file of a module package must be type-checked in at least one of them, so a
// package must be hermetic on every platform the gate runs on. What the check
// cannot prove, it refuses.
package hermetic

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Package is a package of a test binary's dependency closure, as
// `go list -deps -test -json` reports it in the configuration the binary is
// built in.
type Package struct {
	ImportPath   string
	Dir          string
	Standard     bool
	Module       *struct{ Path string }
	Imports      []string
	CgoFiles     []string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
}

// Build is the dependency closure of a test binary in one build
// configuration.
type Build struct {
	GOOS, GOARCH string
	Packages     []Package
	// Std imports a standard-library package of this configuration by its
	// path.
	Std types.Importer
}

func (b Build) String() string { return b.GOOS + "/" + b.GOARCH }

// Violation is a construct that makes a test binary depend on a program or a
// library that Go's test cache does not key.
type Violation struct {
	// Where is a source position, or the import path of a package.
	Where  string
	Reason string
}

func (v Violation) String() string { return v.Where + ": " + v.Reason }

// startsPrograms are the importers of os/exec that a hermetic binary may
// hold. os/exec is the package itself; internal/fuzz runs the test binary as
// a fuzzing worker, and only under -test.fuzz, which the gate never passes.
var startsPrograms = []string{"os/exec", "internal/fuzz"}

// program is a function that runs a program, and the index of its argument
// that names the program.
type program struct {
	path, name string
	argument   int
}

// programs are the functions that run a program.
var programs = []program{
	{"os/exec", "Command", 0},
	{"os/exec", "CommandContext", 1},
	{"os", "StartProcess", 0},
	{"syscall", "Exec", 0},
	{"syscall", "ForkExec", 0},
	{"syscall", "StartProcess", 0},
}

// refused are functions a hermetic package never refers to: a lookup of a
// program on the host's PATH.
var refused = []program{{path: "os/exec", name: "LookPath"}}

// rawCalls make a system call by number, which runs a program when the
// number is one of execNames.
var rawCalls = []string{"Syscall", "Syscall6", "RawSyscall", "RawSyscall6", "AllThreadsSyscall", "AllThreadsSyscall6"}

// execNames are the constants of package syscall, in the configurations that
// define them, whose system call runs a program.
var execNames = []string{"SYS_EXECVE", "SYS_EXECVEAT", "SYS_POSIX_SPAWN"}

// Closure checks the packages of the test binary of the package tested, in
// every build configuration. module is the module's path: its packages are
// checked by their source, the others by their imports and cgo files. The
// tested package may hold one helper that runs the test binary; a package of
// the module it uses may run the test binary where it needs to (testinputs
// observes the tests that way).
func Closure(builds []Build, module, tested string) ([]Violation, error) {
	if len(builds) == 0 {
		return nil, errors.New("no build configuration to check")
	}
	var violations []Violation
	seen := map[string]bool{}
	add := func(found ...Violation) {
		for _, v := range found {
			if !seen[v.String()] {
				seen[v.String()] = true
				violations = append(violations, v)
			}
		}
	}
	covered := map[string]bool{}
	dirs := map[string]bool{} // module package directory -> tested
	for _, b := range builds {
		found, err := b.check(module, tested, covered, dirs)
		if err != nil {
			return nil, err
		}
		add(found...)
	}
	names := make([]string, len(builds))
	for i, b := range builds {
		names[i] = b.String()
	}
	for _, dir := range slices.Sorted(func(yield func(string) bool) {
		for dir := range dirs {
			if !yield(dir) {
				return
			}
		}
	}) {
		found, err := uncovered(dir, dirs[dir], covered, strings.Join(names, ", "))
		if err != nil {
			return nil, err
		}
		add(found...)
	}
	return violations, nil
}

// uncovered refuses every Go file of a module package directory that no
// configuration type-checked: of a tested package, its test files too; of a
// package the binary only uses, the files that are not tests.
func uncovered(dir string, tested bool, covered map[string]bool, configurations string) ([]Violation, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var violations []Violation
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || !tested && strings.HasSuffix(name, "_test.go") || covered[path] {
			continue
		}
		reason := "is built in none of the configurations the check type-checks (" + configurations + "), so the check cannot resolve what it runs"
		if file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly); err == nil {
			for _, spec := range file.Imports {
				if spec.Path.Value == `"C"` {
					reason = `imports "C": cgo links shared libraries that Go's test cache does not key`
				}
			}
		}
		violations = append(violations, Violation{path, reason})
	}
	return violations, nil
}

// basePath is a package's import path without go list's test variant
// suffix (" [pkg.test]").
func basePath(importPath string) string {
	if i := strings.Index(importPath, " ["); i >= 0 {
		return importPath[:i]
	}
	return importPath
}

// check checks one configuration's closure. It records the files it
// type-checks in covered and the module's package directories in dirs.
func (b Build) check(module, tested string, covered map[string]bool, dirs map[string]bool) ([]Violation, error) {
	var violations []Violation
	modulePackages := map[string]Package{}
	var testedPackage *Package
	for _, p := range b.Packages {
		if p.ImportPath == "plugin" {
			violations = append(violations, Violation{p.ImportPath, "loads plugins, shared libraries that Go's test cache does not key"})
		}
		if len(p.CgoFiles) > 0 {
			violations = append(violations, Violation{p.ImportPath, "uses cgo, so the test binary loads shared libraries that Go's test cache does not key"})
		}
		path := basePath(p.ImportPath)
		switch {
		case p.Module != nil && p.Module.Path == module:
			// The generated test main and the external test package are
			// checked through the tested package's own entry.
			if path == tested+".test" || path == tested+"_test" {
				continue
			}
			if _, ok := modulePackages[path]; ok && p.ImportPath != path {
				continue // keep the plain entry over a test variant
			}
			modulePackages[path] = p
			if path == tested {
				copied := p
				testedPackage = &copied
			}
		case p.Standard:
			if slices.Contains(p.Imports, "os/exec") && !slices.Contains(startsPrograms, p.ImportPath) {
				violations = append(violations, Violation{p.ImportPath, "imports os/exec, so it can run a program that Go's test cache does not key"})
			}
		default:
			violations = append(violations, Violation{p.ImportPath, "is outside the module and the standard library, so the check cannot read what it runs"})
		}
	}
	if testedPackage == nil {
		return nil, fmt.Errorf("the packages of %s do not include %s", b, tested)
	}
	c := &checker{
		build:   b,
		fset:    token.NewFileSet(),
		module:  modulePackages,
		checked: map[string]*types.Package{},
		covered: covered,
	}
	if execs, err := c.execNumbers(); err != nil {
		violations = append(violations, Violation{"syscall", fmt.Sprintf("in %s: %v, so the check cannot tell which raw system calls run a program", b, err)})
	} else {
		c.execs = execs
	}
	for path, p := range modulePackages {
		dirs[p.Dir] = dirs[p.Dir] || path == tested
	}
	// The tested package with its internal tests, then its external tests;
	// then every other package of the module the binary holds.
	// The external tests import the package with its internal tests, as go
	// test builds it.
	c.checked[tested] = nil
	if checked, err := c.checkPackage(*testedPackage, append(slices.Clone(testedPackage.GoFiles), testedPackage.TestGoFiles...), true); err != nil {
		violations = append(violations, Violation{tested, err.Error()})
		delete(c.checked, tested)
	} else {
		c.checked[tested] = checked
	}
	if len(testedPackage.XTestGoFiles) > 0 {
		external := *testedPackage
		external.ImportPath = tested + "_test"
		if _, err := c.checkPackage(external, testedPackage.XTestGoFiles, true); err != nil {
			violations = append(violations, Violation{tested + "_test", err.Error()})
		}
	}
	for _, path := range slices.Sorted(func(yield func(string) bool) {
		for path := range modulePackages {
			if !yield(path) {
				return
			}
		}
	}) {
		if path == tested {
			continue
		}
		if _, err := c.importModule(path); err != nil {
			violations = append(violations, Violation{path, err.Error()})
		}
	}
	if len(c.helpers) > 1 {
		for _, helper := range c.helpers[1:] {
			c.report(helper.Pos(), fmt.Sprintf("%s is a second function that runs the test binary (the first is %s): keep one helper", helper.Name.Name, c.helpers[0].Name.Name))
		}
	}
	return append(violations, c.violations...), nil
}

type checker struct {
	build      Build
	fset       *token.FileSet
	violations []Violation
	module     map[string]Package
	// checked are the module's packages type-checked so far, without their
	// tests; a nil entry is one being checked.
	checked map[string]*types.Package
	covered map[string]bool
	// execs are the raw system call numbers that run a program in this
	// configuration.
	execs []int64
	// helpers are the functions of the tested package that run the test
	// binary, in the order the check found them.
	helpers []*ast.FuncDecl
}

func (c *checker) report(pos token.Pos, reason string) {
	c.violations = append(c.violations, Violation{c.fset.Position(pos).String(), reason})
}

// execNumbers reads the numbers of execNames from this configuration's
// package syscall.
func (c *checker) execNumbers() ([]int64, error) {
	syscallPackage, err := c.build.Std.Import("syscall")
	if err != nil {
		return nil, err
	}
	var numbers []int64
	for _, name := range execNames {
		if number, ok := syscallPackage.Scope().Lookup(name).(*types.Const); ok {
			if value, exact := constant.Int64Val(number.Val()); exact {
				numbers = append(numbers, value)
			}
		}
	}
	if len(numbers) == 0 {
		return nil, fmt.Errorf("package syscall defines none of %s", strings.Join(execNames, ", "))
	}
	return numbers, nil
}

// Import imports a package for the type checker: the module's from source,
// the others from the configuration's standard library.
func (c *checker) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if _, ok := c.module[path]; ok {
		return c.importModule(path)
	}
	return c.build.Std.Import(path)
}

// importModule type-checks and checks a package of the module the binary
// holds, without its tests, once.
func (c *checker) importModule(path string) (*types.Package, error) {
	if checked, ok := c.checked[path]; ok {
		if checked == nil {
			return nil, fmt.Errorf("import cycle through %s", path)
		}
		return checked, nil
	}
	c.checked[path] = nil
	p := c.module[path]
	checked, err := c.checkPackage(p, p.GoFiles, false)
	if err != nil {
		delete(c.checked, path)
		return nil, err
	}
	c.checked[path] = checked
	return checked, nil
}

// checkPackage parses and type-checks files of package p, then checks every
// file. tested says whether they belong to the tested package, which may hold
// only one helper that runs the test binary.
func (c *checker) checkPackage(p Package, names []string, tested bool) (*types.Package, error) {
	var files []*ast.File
	for _, name := range names {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, name)
		}
		file, err := parser.ParseFile(c.fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("does not parse in %s, so the check cannot resolve what it runs: %v", c.build, err)
		}
		files = append(files, file)
		// A file of a package that fails to type-check is refused with the
		// package, so it counts as checked.
		c.covered[path] = true
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	var first error
	config := types.Config{
		Importer: c,
		// A file that imports "C" is refused by file below; faking the import
		// lets the rest of the package be checked too.
		FakeImportC: true,
		Sizes:       types.SizesFor("gc", c.build.GOARCH),
		Error: func(err error) {
			if first == nil {
				first = err
			}
		},
	}
	checked, _ := config.Check(basePath(p.ImportPath), c.fset, files, info)
	if first != nil {
		return nil, fmt.Errorf("does not type-check in %s, so the check cannot resolve what it runs: %v", c.build, first)
	}
	pkg := &packageCheck{checker: c, pkg: checked, info: info, tested: tested, parents: map[ast.Node]ast.Node{}, defs: map[types.Object][]*ast.Ident{}, uses: map[types.Object][]*ast.Ident{}}
	for _, file := range files {
		ast.Inspect(file, pkg.recordParents(file))
	}
	for id, object := range info.Defs {
		if object != nil {
			pkg.defs[object] = append(pkg.defs[object], id)
		}
	}
	for id, object := range info.Uses {
		pkg.uses[object] = append(pkg.uses[object], id)
	}
	for _, file := range files {
		pkg.file(file)
	}
	return checked, nil
}

// packageCheck checks the files of one type-checked package.
type packageCheck struct {
	*checker
	pkg     *types.Package
	info    *types.Info
	tested  bool
	parents map[ast.Node]ast.Node
	// defs and uses are the identifiers that define and that use each object.
	defs, uses map[types.Object][]*ast.Ident
}

// recordParents returns an inspector that records each node's parent.
func (p *packageCheck) recordParents(file *ast.File) func(ast.Node) bool {
	var stack []ast.Node
	return func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			p.parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	}
}

// parent returns the node that holds n, past any parentheses around it, and
// the outermost of n and those parentheses.
func (p *packageCheck) parent(n ast.Node) (ast.Node, ast.Node) {
	parent := p.parents[n]
	for {
		paren, ok := parent.(*ast.ParenExpr)
		if !ok {
			return parent, n
		}
		n, parent = paren, p.parents[paren]
	}
}

// file checks one file: its imports, its directives, and every use of a
// function that runs a program and of exec.Cmd.
func (p *packageCheck) file(file *ast.File) {
	for _, spec := range file.Imports {
		switch spec.Path.Value {
		case `"C"`:
			p.report(spec.Pos(), `imports "C": cgo links shared libraries that Go's test cache does not key`)
		case `"plugin"`:
			p.report(spec.Pos(), "imports plugin: it loads shared libraries that Go's test cache does not key")
		}
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:linkname") {
				p.report(comment.Pos(), "a go:linkname directive can reach a function that runs a program past this check")
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if isExecCmd(p.info.Types[n].Type) {
				p.report(n.Pos(), "builds an exec.Cmd by hand, which can name any program")
			}
		case *ast.Ident:
			p.ident(file, n)
		}
		return true
	})
}

// isExecCmd reports whether t is os/exec's Cmd.
func isExecCmd(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	object := named.Obj()
	return object.Pkg() != nil && object.Pkg().Path() == "os/exec" && object.Name() == "Cmd"
}

// function returns the package-level function and its import path that
// object is, or nil: never a method, a variable or a local name.
func function(object types.Object) (*types.Func, string) {
	fn, ok := object.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
		return nil, ""
	}
	return fn, fn.Pkg().Path()
}

// ident checks one identifier by the object it denotes.
func (p *packageCheck) ident(file *ast.File, id *ast.Ident) {
	object := p.info.Uses[id]
	if field, ok := object.(*types.Var); ok && field.IsField() && field.Name() == "Path" && field.Pkg() != nil && field.Pkg().Path() == "os/exec" && p.written(id) {
		p.report(id.Pos(), "sets a Path field, which can change the program a command runs")
		return
	}
	fn, path := function(object)
	if fn == nil {
		return
	}
	name := fn.Name()
	if slices.ContainsFunc(refused, func(r program) bool { return r.path == path && r.name == name }) {
		p.report(id.Pos(), fmt.Sprintf("refers to %s.%s: a hermetic package looks up no program on the host", path, name))
		return
	}
	if path == "syscall" && slices.Contains(rawCalls, name) {
		call := p.callOf(id)
		reason := "it is not called, so the check cannot read its number"
		if call != nil && len(call.Args) > 0 {
			reason = p.rawCall(call.Args[0])
		}
		if reason != "" {
			p.report(id.Pos(), fmt.Sprintf("syscall.%s may run a program: %s", name, reason))
		}
		return
	}
	index := slices.IndexFunc(programs, func(r program) bool { return r.path == path && r.name == name })
	if index < 0 {
		return
	}
	call := p.callOf(id)
	if call == nil {
		p.report(id.Pos(), fmt.Sprintf("refers to %s.%s without calling it, so the check cannot see the program it runs", path, name))
		return
	}
	enclosing := enclosingFunction(file, call.Pos())
	if reason := p.runsSelf(enclosing, call, programs[index].argument); reason != "" {
		p.report(call.Pos(), fmt.Sprintf("%s.%s runs a program that Go's test cache does not key: %s", path, name, reason))
		return
	}
	if p.tested && !slices.Contains(p.helpers, enclosing) {
		p.helpers = append(p.helpers, enclosing)
	}
}

// callOf returns the call whose function is the reference id, or nil.
func (p *packageCheck) callOf(id *ast.Ident) *ast.CallExpr {
	var reference ast.Node = id
	if selector, ok := p.parents[id].(*ast.SelectorExpr); ok && selector.Sel == id {
		reference = selector
	}
	parent, outer := p.parent(reference)
	if call, ok := parent.(*ast.CallExpr); ok && call.Fun == outer {
		return call
	}
	return nil
}

// enclosingFunction returns the top-level function declaration of file that
// holds pos, or nil.
func enclosingFunction(file *ast.File, pos token.Pos) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Pos() <= pos && pos < fn.End() {
			return fn
		}
	}
	return nil
}

// rawCall returns why a raw system call with this number may run a program,
// or "" when it cannot: the number is a constant, and not one whose call runs
// a program in this configuration.
func (p *packageCheck) rawCall(number ast.Expr) string {
	value := p.info.Types[number].Value
	if value == nil {
		return "its number is not a constant the check can read"
	}
	n, exact := constant.Int64Val(constant.ToInt(value))
	if !exact {
		return "its number is not an integer constant"
	}
	if p.execs == nil {
		return "the check does not know which numbers run a program in " + p.build.String()
	}
	if slices.Contains(p.execs, n) {
		return fmt.Sprintf("its number is %d, which runs a program in %s", n, p.build)
	}
	return ""
}

// written reports whether the use id of a variable writes it or takes its
// address.
func (p *packageCheck) written(id *ast.Ident) bool {
	var reference ast.Node = id
	if selector, ok := p.parents[id].(*ast.SelectorExpr); ok && selector.Sel == id {
		reference = selector
	}
	parent, outer := p.parent(reference)
	switch parent := parent.(type) {
	case *ast.AssignStmt:
		return slices.Contains(parent.Lhs, outer.(ast.Expr))
	case *ast.IncDecStmt:
		return parent.X == outer
	case *ast.UnaryExpr:
		return parent.Op == token.AND && parent.X == outer
	case *ast.RangeStmt:
		return parent.Key == outer || parent.Value == outer
	}
	return false
}

// runsSelf returns why call does not run the test binary, or "" when it
// does: its program argument is a local variable defined once, by
// `self, err := os.Executable()` (or var), where os.Executable is the os
// package's function, and written or address-taken nowhere.
func (p *packageCheck) runsSelf(enclosing *ast.FuncDecl, call *ast.CallExpr, argument int) string {
	if enclosing == nil {
		return "it is outside a function, so no os.Executable() names its program"
	}
	if argument >= len(call.Args) {
		return "it names no program"
	}
	id, ok := ast.Unparen(call.Args[argument]).(*ast.Ident)
	if !ok {
		return "its program is not a variable bound to os.Executable()"
	}
	self, ok := p.info.Uses[id].(*types.Var)
	if !ok || self.IsField() {
		return "its program is not a variable bound to os.Executable()"
	}
	name := id.Name
	if self.Parent() == nil || self.Parent() == p.pkg.Scope() {
		return fmt.Sprintf("%s is a package variable, so any function may write it", name)
	}
	defs := p.defs[self]
	if len(defs) != 1 || !p.boundToExecutable(defs[0]) {
		return fmt.Sprintf("%s is not bound to os.Executable() in %s", name, enclosing.Name.Name)
	}
	for _, use := range p.uses[self] {
		if p.written(use) {
			return fmt.Sprintf("%s is written again in %s, so it may name another program", name, enclosing.Name.Name)
		}
	}
	return ""
}

// boundToExecutable reports whether def is the first name defined by
// `def, err := os.Executable()` or `var def, err = os.Executable()`.
func (p *packageCheck) boundToExecutable(def *ast.Ident) bool {
	var value ast.Expr
	switch parent := p.parents[def].(type) {
	case *ast.AssignStmt:
		if parent.Tok != token.DEFINE || len(parent.Rhs) != 1 || len(parent.Lhs) == 0 || parent.Lhs[0] != def {
			return false
		}
		value = parent.Rhs[0]
	case *ast.ValueSpec:
		if len(parent.Values) != 1 || parent.Names[0] != def {
			return false
		}
		value = parent.Values[0]
	default:
		return false
	}
	call, ok := ast.Unparen(value).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	var reference *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		reference = fun
	case *ast.SelectorExpr:
		reference = fun.Sel
	default:
		return false
	}
	fn, path := function(p.info.Uses[reference])
	return fn != nil && path == "os" && fn.Name() == "Executable"
}
