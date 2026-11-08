package replay

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// DeclaredFile is the file that holds the closure rule's table of declared
// inputs (the variable declared), relative to the module root.
const DeclaredFile = "internal/closure/closure_test.go"

// ParseDeclared reads the declared inputs of each test package from the
// source of DeclaredFile: the composite literal of the package variable
// declared, as internal/closure writes it. It evaluates the forms that file
// uses (an allowance literal {path, grant, reason}, a []allowance literal,
// and the helpers glob, exactly and declare), by the rules of those
// helpers; any other form is an error, so the gate keys no package from a
// table it cannot read.
func ParseDeclared(source []byte) (map[string][]Allowance, error) {
	file, err := parser.ParseFile(token.NewFileSet(), DeclaredFile, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var table *ast.CompositeLit
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "declared" || len(value.Values) != 1 {
				continue
			}
			literal, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("%s: declared is not a composite literal", DeclaredFile)
			}
			table = literal
		}
	}
	if table == nil {
		return nil, fmt.Errorf("%s: no variable declared", DeclaredFile)
	}
	out := map[string][]Allowance{}
	for _, element := range table.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("%s: an element of declared has no key", DeclaredFile)
		}
		dir, err := stringValue(pair.Key)
		if err != nil {
			return nil, err
		}
		allowances, err := allowancesOf(pair.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", DeclaredFile, dir, err)
		}
		out[dir] = allowances
	}
	return out, nil
}

func allowancesOf(expr ast.Expr) ([]Allowance, error) {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		var out []Allowance
		for _, element := range e.Elts {
			a, err := allowanceOf(element)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
		return out, nil
	case *ast.CallExpr:
		name, ok := e.Fun.(*ast.Ident)
		if !ok || e.Ellipsis.IsValid() {
			return nil, fmt.Errorf("unknown form %T", e.Fun)
		}
		// Each helper's reason is text the key never reads: glob's last
		// argument, exactly's first.
		var args []string
		for i, arg := range e.Args {
			if name.Name == "declare" || name.Name == "glob" && i == 1 || name.Name == "exactly" && i == 0 {
				args = append(args, "")
				continue
			}
			s, err := stringValue(arg)
			if err != nil {
				return nil, err
			}
			args = append(args, s)
		}
		switch name.Name {
		case "declare":
			var out []Allowance
			for _, arg := range e.Args {
				part, err := allowancesOf(arg)
				if err != nil {
					return nil, err
				}
				for _, a := range part {
					if !slices.Contains(out, a) {
						out = append(out, a)
					}
				}
			}
			return out, nil
		case "glob":
			if len(args) != 2 {
				return nil, fmt.Errorf("glob takes a pattern and a reason")
			}
			return globAllowances(args[0])
		case "exactly":
			if len(args) < 1 {
				return nil, fmt.Errorf("exactly takes a reason first")
			}
			var out []Allowance
			for _, p := range args[1:] {
				out = append(out, Allowance{Path: p, Grant: GrantFile})
			}
			return out, nil
		}
		return nil, fmt.Errorf("unknown helper %s", name.Name)
	}
	return nil, fmt.Errorf("unknown form %T", expr)
}

// globAllowances is internal/closure's glob: a listing of the directory
// above the first element with a pattern, a listing of each further
// element but the last, and the files the whole pattern matches.
func globAllowances(pattern string) ([]Allowance, error) {
	elems := strings.Split(pattern, "/")
	first := slices.IndexFunc(elems, func(e string) bool { return strings.ContainsAny(e, `*?[\`) })
	if first < 1 {
		return nil, fmt.Errorf("glob %s: want a pattern below a directory", pattern)
	}
	out := []Allowance{{strings.Join(elems[:first], "/"), GrantListing}}
	for j := first; j < len(elems)-1; j++ {
		out = append(out, Allowance{strings.Join(elems[:j+1], "/"), GrantListing})
	}
	return append(out, Allowance{pattern, GrantFiles}), nil
}

func allowanceOf(expr ast.Expr) (Allowance, error) {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok || len(literal.Elts) != 3 {
		return Allowance{}, fmt.Errorf("an allowance is not {path, grant, reason}")
	}
	p, err := stringValue(literal.Elts[0])
	if err != nil {
		return Allowance{}, err
	}
	grant, ok := literal.Elts[1].(*ast.Ident)
	if !ok {
		return Allowance{}, fmt.Errorf("%s: the grant is not a name", p)
	}
	switch Grant(grant.Name) {
	case GrantFile, GrantListing, GrantFiles, GrantSubtree, GrantMembers, GrantProgram:
		return Allowance{Path: p, Grant: Grant(grant.Name)}, nil
	}
	return Allowance{}, fmt.Errorf("%s: unknown grant %s", p, grant.Name)
}

func stringValue(expr ast.Expr) (string, error) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", fmt.Errorf("not a string literal: %T", expr)
	}
	return strconv.Unquote(literal.Value)
}
