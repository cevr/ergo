package lower

// The Go text of the IR bodies (plan-tier §2, TXT; M7). IRGoText prints
// each function body an IR file holds as Go, into the Go file that
// declares the function without a body: what `ergo build -go-text`
// writes, so go vet, gopls and a reader see the IR bodies as Go at their
// .ergo positions. The text is a view, never an input: the build compiles
// the IR. Each IR form is spelled as the derivative's decoder
// (toolchain/ir, cmd/compile/internal/ergoir) builds its typed syntax, so
// the text compiles to the program the IR compiles to; the IR's types are
// the decoder's, resolved by go/types against the module's Go source
// (ir_gotext_expr.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"slices"
	"strings"
)

// IRGoText is the module files with each IR body printed into the Go file
// that declares its function, each statement at its .ergo position (a
// /*line*/ directive), each file formatted as gofmt formats it, and
// without the IR files. imp imports every package the IR packages import
// that is not itself an IR package (an IR package is type-checked from
// its Go source here).
func IRGoText(files []File, imp types.Importer) ([]File, error) {
	m := &irTextModule{fset: token.NewFileSet(), imp: imp, pkgs: map[string]*irTextPkg{}, byPath: map[string]*irTextPkg{}}
	for _, f := range files {
		if path.Base(f.Path) == IRFileName {
			dir := path.Dir(f.Path)
			ip := programModule
			if dir != "." {
				ip += "/" + dir
			}
			p := &irTextPkg{dir: dir, path: ip, ir: f.Data}
			m.pkgs[dir], m.byPath[ip] = p, p
		}
	}
	if len(m.pkgs) == 0 {
		return files, nil
	}
	for i, f := range files {
		p := m.pkgs[path.Dir(f.Path)]
		if p == nil || !strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		// The file's own name, without its directory: a relative //line
		// filename is the file's directory's (go/scanner), as the
		// emitter's directives are.
		af, err := parser.ParseFile(m.fset, path.Base(f.Path), f.Data, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f.Path, err)
		}
		p.files = append(p.files, &irTextFile{index: i, src: f.Data, ast: af})
	}
	out := slices.Clone(files)
	for _, p := range m.sorted() {
		if _, err := m.check(p); err != nil {
			return nil, err
		}
		if err := m.print(p, out); err != nil {
			return nil, err
		}
	}
	return slices.DeleteFunc(out, func(f File) bool { return path.Base(f.Path) == IRFileName }), nil
}

type irTextModule struct {
	fset   *token.FileSet
	imp    types.Importer
	pkgs   map[string]*irTextPkg // by directory
	byPath map[string]*irTextPkg // by import path
}

// irTextPkg is a package of the module that holds IR bodies.
type irTextPkg struct {
	dir, path string
	ir        []byte
	files     []*irTextFile
	types     *types.Package
	info      *types.Info
	checking  bool
}

type irTextFile struct {
	index int // in the module's files
	src   []byte
	ast   *ast.File
	// imports are the file's local names of its imports, by path.
	imports map[string]string
	splices []irSplice
}

// irSplice replaces src[from:to] by text.
type irSplice struct {
	from, to int
	text     string
}

func (m *irTextModule) sorted() []*irTextPkg {
	var out []*irTextPkg
	for _, p := range m.pkgs {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *irTextPkg) int { return strings.Compare(a.dir, b.dir) })
	return out
}

// Import is the module's importer: an IR package from its Go source,
// every other package by imp.
func (m *irTextModule) Import(path string) (*types.Package, error) {
	if p := m.byPath[path]; p != nil {
		return m.check(p)
	}
	return m.imp.Import(path)
}

// check type-checks an IR package's Go source, whose IR functions have no
// body (or a generic one's placeholder, ir_placeholder.go).
func (m *irTextModule) check(p *irTextPkg) (*types.Package, error) {
	if p.types != nil {
		return p.types, nil
	}
	if p.checking {
		return nil, fmt.Errorf("%s: an import cycle", p.path)
	}
	p.checking = true
	var list []*ast.File
	for _, f := range p.files {
		list = append(list, f.ast)
	}
	var errs []string
	conf := types.Config{Importer: m, GoVersion: "go" + goVersion, Error: func(err error) { errs = append(errs, err.Error()) }}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, _ := conf.Check(p.path, m.fset, list, info)
	if len(errs) > 0 {
		return nil, fmt.Errorf("the Go source of %s does not type-check: %s", p.path, strings.Join(errs[:min(len(errs), 5)], "; "))
	}
	p.types, p.info = pkg, info
	return pkg, nil
}

// print prints p's IR bodies into its Go files, which it replaces in out.
func (m *irTextModule) print(p *irTextPkg, out []File) error {
	var f irFile
	if err := json.Unmarshal(p.ir, &f); err != nil {
		return fmt.Errorf("%s: %v", path.Join(p.dir, IRFileName), err)
	}
	type declAt struct {
		decl *ast.FuncDecl
		file *irTextFile
	}
	decls := map[string]declAt{}
	for _, file := range p.files {
		file.imports = map[string]string{}
		for _, spec := range file.ast.Imports {
			ip := strings.Trim(spec.Path.Value, `"`)
			switch {
			case spec.Name != nil:
				file.imports[ip] = spec.Name.Name
			default:
				for _, imp := range p.types.Imports() {
					if imp.Path() == ip {
						file.imports[ip] = imp.Name()
					}
				}
			}
		}
		for _, d := range file.ast.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && (fd.Body == nil || irTextPlaceholder(fd.Body)) {
				if obj, ok := p.info.Defs[fd.Name].(*types.Func); ok {
					decls[irTextKey(obj)] = declAt{fd, file}
				}
			}
		}
	}
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		at, ok := decls[fn.Name]
		if !ok {
			return fmt.Errorf("%s: the IR function %s is not declared without a body in the Go source", p.path, fn.Name)
		}
		delete(decls, fn.Name)
		obj := p.info.Defs[at.decl.Name].(*types.Func)
		pr := &irPrinter{pkg: p.types, file: at.file, files: f.Files}
		body, err := pr.function(obj, fn)
		if err != nil {
			return fmt.Errorf("%s: %s: %v", p.path, fn.Name, err)
		}
		sp, err := m.splice(at.file, at.decl, body)
		if err != nil {
			return fmt.Errorf("%s: %s: %v", p.path, fn.Name, err)
		}
		at.file.splices = append(at.file.splices, sp)
	}
	for _, file := range p.files {
		if len(file.splices) == 0 {
			continue
		}
		slices.SortFunc(file.splices, func(a, b irSplice) int { return a.from - b.from })
		var b strings.Builder
		last := 0
		for _, s := range file.splices {
			b.Write(file.src[last:s.from])
			b.WriteString(s.text)
			last = s.to
		}
		b.Write(file.src[last:])
		text, err := irFormat([]byte(b.String()))
		if err != nil {
			return fmt.Errorf("%s: %v", out[file.index].Path, err)
		}
		out[file.index] = File{Path: out[file.index].Path, Data: text}
	}
	return nil
}

// irFormat is src as gofmt formats it, to its fixed point: one pass can
// leave a /*line*/ directive before a one-line declaration that the next
// pass moves to a line of its own, and the text is to be gofmt's output
// of itself.
func irFormat(src []byte) ([]byte, error) {
	for range 4 {
		out, err := format.Source(src)
		if err != nil {
			return nil, fmt.Errorf("the printed Go text does not parse: %v", err)
		}
		if bytes.Equal(out, src) {
			return out, nil
		}
		src = out
	}
	return nil, fmt.Errorf("gofmt finds no fixed point of the printed Go text")
}

// splice is the splice that gives decl the body text (its statements,
// then its closing brace): after its signature, which ends its line, or
// over its placeholder. The line after the body gets back the position
// it has in the Go source.
func (m *irTextModule) splice(file *irTextFile, decl *ast.FuncDecl, body string) (irSplice, error) {
	tf := m.fset.File(decl.Pos())
	src := file.src
	var from, to int
	if decl.Body != nil {
		// The placeholder, from its { to its }: what follows its } on that
		// line (a comment) moves to the signature's line.
		from, to = tf.Offset(decl.Body.Lbrace), tf.Offset(decl.Body.Rbrace)+1
		for from > 0 && (src[from-1] == ' ' || src[from-1] == '\t') {
			from--
		}
	} else {
		from = tf.Offset(decl.Type.End())
		to = from
	}
	eol := to
	for eol < len(src) && src[eol] != '\n' {
		eol++
	}
	rest := string(src[to:eol]) // spaces and a comment, if any
	if strings.TrimSpace(rest) == "" {
		rest = ""
	}
	if eol >= len(src) {
		return irSplice{}, fmt.Errorf("the declaration ends the file")
	}
	// The line after the declaration's: its position as the source has it.
	next := m.fset.PositionFor(tf.Pos(eol+1), true)
	return irSplice{from: from, to: eol, text: " " + body[:strings.IndexByte(body, '\n')] + rest + body[strings.IndexByte(body, '\n'):] + fmt.Sprintf("\n//line %s:%d", next.Filename, next.Line)}, nil
}

// irTextKey is the IR's name of a function: NAME, or TYPE.NAME for a
// method (the decoder's ergoKey).
func irTextKey(obj *types.Func) string {
	sig := obj.Type().(*types.Signature)
	if sig.Recv() == nil {
		return obj.Name()
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Obj().Name() + "." + obj.Name()
	}
	return "?." + obj.Name()
}

// irTextPlaceholder reports a generic declaration's placeholder body
// (ir_placeholder.go): panic("ergoir: no IR body").
func irTextPlaceholder(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	st, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := st.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	lit, ok2 := call.Args[0].(*ast.BasicLit)
	return ok && ok2 && fun.Name == "panic" && lit.Value == `"ergoir: no IR body"`
}
