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
// the module's source, and Go records all three. The check is static: it
// reads the module's source, every Go file of a package directory whatever
// its build constraints, so a package must be hermetic on every platform.
package hermetic

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Package is a package of a test binary's dependency closure, as
// `go list -deps -test -json` reports it in the environment the binary is
// built in.
type Package struct {
	ImportPath string
	Dir        string
	Standard   bool
	Module     *struct{ Path string }
	Imports    []string
	CgoFiles   []string
}

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

// Closure checks the packages of the test binary of the package tested.
// module is the module's path: its packages are checked by their source, the
// others by their imports and cgo files. The tested package may hold one
// helper that runs the test binary; a package of the module it uses may run
// the test binary where it needs to (testinputs observes the tests that way).
func Closure(packages []Package, module, tested string) ([]Violation, error) {
	var violations []Violation
	testedDir := ""
	for _, p := range packages {
		if p.ImportPath == tested {
			testedDir = p.Dir
		}
	}
	if testedDir == "" {
		return nil, fmt.Errorf("the packages do not include %s", tested)
	}
	checked := map[string]bool{}
	for _, p := range packages {
		if p.ImportPath == "plugin" {
			violations = append(violations, Violation{p.ImportPath, "loads plugins, shared libraries that Go's test cache does not key"})
		}
		if len(p.CgoFiles) > 0 {
			violations = append(violations, Violation{p.ImportPath, "uses cgo, so the test binary loads shared libraries that Go's test cache does not key"})
		}
		switch {
		case p.Module != nil && p.Module.Path == module:
			if checked[p.Dir] {
				continue
			}
			checked[p.Dir] = true
			found, err := Directory(p.Dir, p.Dir == testedDir)
			if err != nil {
				return nil, err
			}
			violations = append(violations, found...)
		case p.Standard:
			if slices.Contains(p.Imports, "os/exec") && !slices.Contains(startsPrograms, p.ImportPath) {
				violations = append(violations, Violation{p.ImportPath, "imports os/exec, so it can run a program that Go's test cache does not key"})
			}
		default:
			violations = append(violations, Violation{p.ImportPath, "is outside the module and the standard library, so the check cannot read what it runs"})
		}
	}
	return violations, nil
}

// Directory checks every Go file in a package directory, whatever its build
// constraints: of a tested package, its test files too, and it may hold only
// one helper that runs the test binary; of a package the binary only uses,
// the files that are not tests, which are the ones linked in.
func Directory(dir string, tested bool) ([]Violation, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || !tested && strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return Files(fset, files, tested), nil
}

// program is a function that runs a program, and the index of its argument
// that names the program.
type program struct {
	path, name string
	argument   int
}

// programs are the functions that run a program, by import path and name.
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
var refused = map[string][]string{
	"os/exec": {"LookPath"},
}

// rawCalls make a system call by number, which runs a program when the
// number is execve's or execveat's.
var rawCalls = []string{"Syscall", "Syscall6", "RawSyscall", "RawSyscall6", "AllThreadsSyscall", "AllThreadsSyscall6"}

// execNumbers are the numbers of execve and execveat on the architectures
// the module makes raw system calls for (Linux amd64 and arm64).
var execNumbers = map[string][]int64{"amd64": {59, 322}, "arm64": {221, 281}}

// architectures are the GOARCH values, which a file name may end in.
var architectures = []string{"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle", "ppc64", "ppc64le", "riscv64", "s390x", "wasm"}

// Files checks the parsed files of one package directory. A tested package
// may hold only one helper that runs the test binary.
func Files(fset *token.FileSet, files []*ast.File, tested bool) []Violation {
	c := &checker{fset: fset, constants: map[string][]constant{}}
	for _, file := range files {
		c.collectConstants(file)
	}
	for _, file := range files {
		c.file(file)
	}
	if tested && len(c.helpers) > 1 {
		for _, helper := range c.helpers[1:] {
			c.report(helper.Pos(), fmt.Sprintf("%s is a second function that runs the test binary (the first is %s): keep one helper", helper.Name.Name, c.helpers[0].Name.Name))
		}
	}
	return c.violations
}

type checker struct {
	fset       *token.FileSet
	violations []Violation
	// helpers are the functions that run the test binary, in file order.
	helpers []*ast.FuncDecl
	// constants are the package's integer constants by name, one for each
	// file that defines the name.
	constants map[string][]constant
}

// constant is an integer constant, and the architecture of the file that
// defines it ("" when the file is for every architecture).
type constant struct {
	value int64
	known bool
	arch  string
}

// collectConstants records the constants a file defines.
func (c *checker) collectConstants(file *ast.File) {
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(c.fset.File(file.Pos()).Name()), ".go"), "_test")
	arch := base[strings.LastIndex(base, "_")+1:]
	if !slices.Contains(architectures, arch) {
		arch = ""
	}
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			values := spec.(*ast.ValueSpec)
			for i, name := range values.Names {
				defined := constant{arch: arch}
				if i < len(values.Values) {
					if literal, ok := values.Values[i].(*ast.BasicLit); ok && literal.Kind == token.INT {
						if value, err := strconv.ParseInt(literal.Value, 0, 64); err == nil {
							defined.value, defined.known = value, true
						}
					}
				}
				c.constants[name.Name] = append(c.constants[name.Name], defined)
			}
		}
	}
}

// rawCall returns why a raw system call with this number may run a program,
// or "" when it cannot: the number is a syscall.SYS_ constant other than
// execve's and execveat's, or the package's own integer constant whose every
// definition differs from them on its file's architecture.
func (c *checker) rawCall(number ast.Expr, imported map[string]string) string {
	if path, name, ok := qualified(number, imported); ok && path == "syscall" && strings.HasPrefix(name, "SYS_") {
		if name == "SYS_EXECVE" || name == "SYS_EXECVEAT" {
			return "its number is " + name
		}
		return ""
	}
	ident, ok := number.(*ast.Ident)
	if !ok || len(c.constants[ident.Name]) == 0 {
		return "its number is not a named constant the check can read"
	}
	for _, defined := range c.constants[ident.Name] {
		if !defined.known {
			return fmt.Sprintf("%s is not an integer literal", ident.Name)
		}
		arches := []string{defined.arch}
		if defined.arch == "" {
			arches = slices.Sorted(maps.Keys(execNumbers))
		}
		for _, arch := range arches {
			numbers, ok := execNumbers[arch]
			if !ok {
				return fmt.Sprintf("%s is defined for %s, whose exec numbers the check does not know", ident.Name, arch)
			}
			if slices.Contains(numbers, defined.value) {
				return fmt.Sprintf("%s is %d, which runs a program on %s", ident.Name, defined.value, arch)
			}
		}
	}
	return ""
}

func (c *checker) report(pos token.Pos, reason string) {
	c.violations = append(c.violations, Violation{c.fset.Position(pos).String(), reason})
}

// file checks one file: its imports, its directives, and every use of the
// functions that run a program.
func (c *checker) file(file *ast.File) {
	imported := map[string]string{} // local name -> import path
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		switch path {
		case "C":
			c.report(spec.Pos(), `imports "C": cgo links shared libraries that Go's test cache does not key`)
			continue
		case "plugin":
			c.report(spec.Pos(), "imports plugin: it loads shared libraries that Go's test cache does not key")
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." && (path == "os" || path == "os/exec" || path == "syscall") {
			c.report(spec.Pos(), fmt.Sprintf("dot-imports %s, which hides the functions that run a program", path))
		}
		imported[name] = path
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:linkname") {
				c.report(comment.Pos(), "a go:linkname directive can reach a function that runs a program past this check")
			}
		}
	}
	execImported := slices.Contains(slices.Collect(maps.Values(imported)), "os/exec")
	for _, decl := range file.Decls {
		function, _ := decl.(*ast.FuncDecl)
		c.uses(decl, function, imported, execImported)
	}
}

// uses checks every reference in one top-level declaration. function is the
// declaration when it is a function, so a call there may run the test
// binary it names through os.Executable().
func (c *checker) uses(decl ast.Node, function *ast.FuncDecl, imported map[string]string, execImported bool) {
	called := map[ast.Expr]*ast.CallExpr{}
	ast.Inspect(decl, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			called[call.Fun] = call
		}
		return true
	})
	helper := false
	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if path, name, ok := qualified(n.Type, imported); ok && path == "os/exec" && name == "Cmd" {
				c.report(n.Pos(), "builds an exec.Cmd by hand, which can name any program")
			}
		case *ast.AssignStmt:
			if execImported {
				for _, lhs := range n.Lhs {
					if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "Path" {
						c.report(lhs.Pos(), "sets a Path field, which can change the program a command runs")
					}
				}
			}
		case *ast.UnaryExpr:
			if selector, ok := n.X.(*ast.SelectorExpr); execImported && ok && n.Op == token.AND && selector.Sel.Name == "Path" {
				c.report(n.Pos(), "takes the address of a Path field, which can change the program a command runs")
			}
		case *ast.SelectorExpr:
			path, name, ok := qualified(n, imported)
			if !ok {
				return true
			}
			if slices.Contains(refused[path], name) {
				c.report(n.Pos(), fmt.Sprintf("refers to %s.%s: a hermetic package looks up no program on the host", path, name))
				return true
			}
			if path == "syscall" && slices.Contains(rawCalls, name) {
				call := called[n]
				reason := "it is not called, so the check cannot read its number"
				if call != nil && len(call.Args) > 0 {
					reason = c.rawCall(call.Args[0], imported)
				}
				if reason != "" {
					c.report(n.Pos(), fmt.Sprintf("syscall.%s may run a program: %s", name, reason))
				}
				return true
			}
			index := slices.IndexFunc(programs, func(p program) bool { return p.path == path && p.name == name })
			if index < 0 {
				return true
			}
			call := called[n]
			if call == nil {
				c.report(n.Pos(), fmt.Sprintf("refers to %s.%s without calling it, so the check cannot see the program it runs", path, name))
				return true
			}
			if reason := runsSelf(function, call, programs[index].argument, imported); reason != "" {
				c.report(call.Pos(), fmt.Sprintf("%s.%s runs a program that Go's test cache does not key: %s", path, name, reason))
				return true
			}
			helper = true
		}
		return true
	})
	if helper {
		c.helpers = append(c.helpers, function)
	}
}

// qualified reports the import path and name of a reference pkg.Name.
func qualified(expression ast.Expr, imported map[string]string) (string, string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	path, ok := imported[ident.Name]
	return path, selector.Sel.Name, ok
}

// runsSelf returns why call does not run the test binary, or "" when it
// does: the program argument is a variable of the enclosing function bound
// once, by `name, err := os.Executable()`, and written nowhere else in it.
func runsSelf(function *ast.FuncDecl, call *ast.CallExpr, argument int, imported map[string]string) string {
	if function == nil {
		return "it is outside a function, so no os.Executable() names its program"
	}
	if argument >= len(call.Args) {
		return "it names no program"
	}
	ident, ok := call.Args[argument].(*ast.Ident)
	if !ok {
		return "its program is not a variable bound to os.Executable()"
	}
	name := ident.Name
	bindings, writes := 0, 0
	written := func(expression ast.Expr) {
		if id, ok := expression.(*ast.Ident); ok && id.Name == name {
			writes++
		}
	}
	fields := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			for _, id := range field.Names {
				written(id)
			}
		}
	}
	fields(function.Recv)
	ast.Inspect(function, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncType:
			fields(n.TypeParams)
			fields(n.Params)
			fields(n.Results)
		case *ast.AssignStmt:
			if len(n.Lhs) > 0 && len(n.Rhs) == 1 {
				if lhs, ok := n.Lhs[0].(*ast.Ident); ok && lhs.Name == name && executable(n.Rhs[0], imported) {
					bindings++
					for _, other := range n.Lhs[1:] {
						written(other)
					}
					return true
				}
			}
			for _, lhs := range n.Lhs {
				written(lhs)
			}
		case *ast.ValueSpec:
			if len(n.Values) > 0 {
				for _, id := range n.Names {
					written(id)
				}
			}
		case *ast.RangeStmt:
			if n.Key != nil {
				written(n.Key)
			}
			if n.Value != nil {
				written(n.Value)
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				written(n.X)
			}
		}
		return true
	})
	switch {
	case bindings == 0:
		return fmt.Sprintf("%s is not bound to os.Executable() in %s", name, function.Name.Name)
	case bindings > 1 || writes > 0:
		return fmt.Sprintf("%s is written again in %s, so it may name another program", name, function.Name.Name)
	}
	return ""
}

// executable reports whether expression is a call of os.Executable().
func executable(expression ast.Expr, imported map[string]string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	path, name, ok := qualified(call.Fun, imported)
	return ok && path == "os" && name == "Executable"
}
