package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/experimental/irspike"
)

// declRow is the DECL receipt of one program (plan-tier q134): the work of
// the IR path's Go declaration file in an edit build, against the whole
// edit build.
type declRow struct {
	Program string `json:"program"`
	// Edit is the IR path's body-edit build: the Ergo stage and the go
	// build (warm cache).
	Edit int64 `json:"edit_instructions"`
	// ProgramCompile is that build's compile of the program package.
	ProgramCompile int64 `json:"program_compile_instructions"`
	// Decl is the compile of the program package's Go text with every
	// function body removed: parse, types2, export data and the compile
	// of the package-level initializers.
	Decl      int64   `json:"decl_instructions"`
	Share     float64 `json:"decl_share_of_edit"`
	IRFuncs   int     `json:"ir_funcs"`
	Fallbacks int     `json:"fallback_funcs"`
	// Link is the first line of the declaration build's expected failure.
	Link string `json:"link_failure,omitempty"`
}

// decl measures the DECL share of PROGRAM: the IR path builds MODULE cold,
// then again after the -body edit (the edit build); then the edit build's
// main.go, its bodies removed, is compiled alone by the source toolchain
// (its link fails: the bodies are missing, which the compile allows when
// the package has an assembly file).
func decl(args []string) error {
	fs := flag.NewFlagSet("decl", flag.ContinueOnError)
	body := fs.String("body", "", "FILE:OLD=>NEW, an edit inside a function body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 || *body == "" {
		return fmt.Errorf("usage: irspike decl -body E WORK MODULE PROGRAM")
	}
	work, module, program := fs.Arg(0), fs.Arg(1), fs.Arg(2)
	src, ir, ok := irspike.Toolchains()
	if !ok {
		return irspike.ErrNoToolchains
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	tree := filepath.Join(work, "src")
	if err := copyTree(module, tree); err != nil {
		return err
	}
	ir.GOCACHE = filepath.Join(work, "gocache-ir")
	if _, err := measure(self, ir, irspike.IR, filepath.Join(tree, program), filepath.Join(work, "0-cold")); err != nil {
		return fmt.Errorf("cold: %w", err)
	}
	if err := applyEdit(tree, *body); err != nil {
		return err
	}
	editDir := filepath.Join(work, "1-edit")
	sc, err := measure(self, ir, irspike.IR, filepath.Join(tree, program), editDir)
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}
	row := declRow{Program: program, IRFuncs: sc.IRFuncs, Fallbacks: sc.Fallback}
	row.Edit = sc.Stages[0].Cost.Instructions + sc.Stages[1].Cost.Instructions
	if row.ProgramCompile, err = programCompile(filepath.Join(editDir, "tools.jsonl")); err != nil {
		return err
	}
	dmod := filepath.Join(work, "decl")
	if err := copyTree(filepath.Join(editDir, "module"), dmod); err != nil {
		return err
	}
	if err := stripBodies(filepath.Join(dmod, "main.go")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dmod, "decl.s"), nil, 0o644); err != nil {
		return err
	}
	log := filepath.Join(work, "decl-tools.jsonl")
	src.GOCACHE = filepath.Join(work, "gocache-source")
	// The link fails (no bodies); the compile ran and was counted. A
	// compile error (a Go position in the output) would count a compile
	// that stopped early.
	_, berr := irspike.Build(src, irspike.Source, dmod, filepath.Join(work, "decl.bin"), irspike.BuildOptions{Toolexec: self + " tool " + log})
	if berr != nil && strings.Contains(berr.Error(), ".go:") {
		return fmt.Errorf("the declaration file does not compile: %w", berr)
	}
	if berr != nil {
		msg := berr.Error()
		if i := strings.Index(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = strings.Join(strings.Fields(msg), " ")
		row.Link = msg[:min(len(msg), 200)]
	}
	if row.Decl, err = programCompile(log); err != nil {
		return fmt.Errorf("declaration compile: %w", err)
	}
	row.Share = float64(row.Decl) / float64(row.Edit)
	line, _ := json.Marshal(row)
	fmt.Println(string(line))
	return nil
}

// programCompile is the instructions of the program package's compile in a
// meter log.
func programCompile(log string) (int64, error) {
	f, err := os.Open(log)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		var t irspike.ToolStep
		if json.Unmarshal(s.Bytes(), &t) == nil && t.Tool == "compile" && pkgClass(t.Pkg) == "program" {
			return t.Cost.Instructions, nil
		}
	}
	return 0, fmt.Errorf("%s: no compile of the program package", log)
}

// generic reports a function with type parameters, or a method of a
// generic type.
func generic(fd *ast.FuncDecl) bool {
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		return true
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch t.(type) {
	case *ast.IndexExpr, *ast.IndexListExpr:
		return true
	}
	return false
}

// stripBodies removes every function body of the Go file (a generic one
// keeps a panic, init an empty body), and the imports
// only bodies used.
func stripBodies(name string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch {
		case fd.Name.Name == "init" && fd.Recv == nil:
			// init must have a body: an empty one.
			fd.Body = &ast.BlockStmt{}
		case generic(fd):
			// A generic function must have a body: a panic, the least
			// a body can be.
			fd.Body = &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "0"}}}}}}
		default:
			fd.Body = nil
		}
	}
	f.Comments = nil
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		var keep []ast.Spec
		for _, s := range gd.Specs {
			is := s.(*ast.ImportSpec)
			path, _ := strconv.Unquote(is.Path.Value)
			name := filepath.Base(path)
			if len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
				name = filepath.Base(filepath.Dir(path)) // math/rand/v2
			}
			if is.Name != nil {
				name = is.Name.Name
			}
			if name == "_" || used[name] {
				keep = append(keep, s)
			}
		}
		gd.Specs = keep
	}
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, f); err != nil {
		return err
	}
	return os.WriteFile(name, b.Bytes(), 0o644)
}
