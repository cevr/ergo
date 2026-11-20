package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The closure key of a Go function (LFL; orchestrator ruling on owner q1,
// 2026-11-15: "key by the transitive closure of what the law reaches inside
// runtime/v1 (and any trusted Go package) … computed with go/types/the call
// graph"). A function of runtime/v1 or of a trusted module's own Go is
// keyed by the source of its declaration and of every declaration it uses,
// transitively, in its package and in every package of the same source it
// imports: the functions it names, the methods its calls resolve to
// (go/types' selections), the types it names, each with all its methods
// (code outside the walk, such as fmt, may call one through an interface),
// the package vars and consts with their declarations, and the init
// functions of every package it enters.
//
// A call the types do not resolve to one method is taken by its name: an
// interface method call, a method value or a selector whose operand has no
// type (a value of Go's standard library, which the walk does not type)
// takes every method of that name in every package the walk enters. So the
// walk takes more than the call graph, never less: an edit that can change
// the function's behaviour changes the key, and an edit of a declaration it
// cannot reach does not.
//
// What the walk cannot see widens the key to the whole package's files
// (its Go and assembly files, tests excluded): a reached function with no
// body (assembly, or a linkname that pulls a symbol in), a file with a
// //go:linkname that pushes one out (any but a pull onto a function of
// its file with no body, which widens when reached), or a dot import, a reached var with //go:embed, a file that does not parse, and a
// name the package does not declare. A function value passed in from
// another package calls only what that package reaches, which its own key
// covers.
//
// Prior art: Go's build cache keys a compile action by the hashes of its
// inputs, a whole package; Bazel's and Buck's fine-grained targets key by
// the declarations a target reads; Go's own deadcode tool (x/tools RTA)
// keeps every method of a name an interface call may reach. Here the
// coarse key is the fallback and the fine one the rule, because a false
// current entry is a wrong rewrite and a needless stale one only a rerun
// of ergo test.

// GoSourceFile is a file of a Go package: Name is its base name.
type GoSourceFile struct {
	Name string
	Data []byte
}

// goPackages loads the files of a Go package by import path; ok is false
// for a package outside the walk (Go's standard library, another module).
type goPackages func(importPath string) (files []GoSourceFile, ok bool)

type goDecl struct {
	file, text string
	node       ast.Node
	widen      bool
}

type goPkg struct {
	path    string
	files   []GoSourceFile
	ast     []*ast.File
	decls   map[string][]goDecl // "F", "T", "T.M", a var or const name
	methods map[string][]string // method name -> "T.M" keys
	ofType  map[string][]string // type name -> "T.M" keys
	inits   []string
	imports map[ast.Node]map[string]string // decl -> its file's alias -> import path
	widen   bool                           // a file the walk cannot read
	types   *types.Package
	info    *types.Info
}

// goWorld is the parsed and typed packages of one source (the runtime the
// compiler embeds, or one module's folders): each package once.
type goWorld struct {
	mu   sync.Mutex
	load goPackages
	fset *token.FileSet
	pkgs map[string]*goPkg // nil: outside the walk
	// std types Go's standard library as the world's target compiles it
	// (gostd.go); nil leaves std's packages empty.
	std *stdTypes
}

func newGoWorld(load goPackages) *goWorld {
	return &goWorld{load: load, fset: token.NewFileSet(), pkgs: map[string]*goPkg{}}
}

// Import is the types.Importer of the world's packages: a package outside
// the walk is an empty one, so a name of it has no type and is taken by
// name (and the type errors that follow are ignored).
func (w *goWorld) Import(p string) (*types.Package, error) {
	if g := w.get(p); g != nil && g.types != nil {
		return g.types, nil
	}
	if isStdPath(p) {
		if t := w.std.imported(p); t != nil {
			return t, nil
		}
	}
	name := path.Base(p)
	if strings.HasPrefix(name, "v") && strings.Trim(name[1:], "0123456789") == "" && strings.Contains(p, "/") {
		name = path.Base(path.Dir(p))
	}
	t := types.NewPackage(p, name)
	t.MarkComplete()
	return t, nil
}

func (w *goWorld) get(p string) *goPkg {
	if g, ok := w.pkgs[p]; ok {
		return g
	}
	files, ok := w.load(p)
	if !ok {
		w.pkgs[p] = nil
		return nil
	}
	g := w.parse(p, files)
	w.pkgs[p] = g
	g.info = &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: w, Error: func(error) {}, FakeImportC: true}
	g.types, _ = conf.Check(p, w.fset, g.ast, g.info)
	return g
}

func (w *goWorld) parse(p string, files []GoSourceFile) *goPkg {
	g := &goPkg{path: p, files: files, decls: map[string][]goDecl{}, methods: map[string][]string{}, ofType: map[string][]string{}, imports: map[ast.Node]map[string]string{}}
	sorted := append([]GoSourceFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, f := range sorted {
		if !strings.HasSuffix(f.Name, ".go") || strings.HasSuffix(f.Name, "_test.go") {
			continue
		}
		src := string(f.Data)
		file, err := parser.ParseFile(w.fset, path.Join(p, f.Name), src, parser.ParseComments)
		if err != nil {
			g.widen = true
			continue
		}
		g.ast = append(g.ast, file)
		if strings.Contains(src, "//go:linkname") && !onlyPullLinknames(file) {
			g.widen = true
		}
		names := map[string]string{}
		for _, im := range file.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			alias := path.Base(ip)
			if im.Name != nil {
				alias = im.Name.Name
			}
			if alias == "." {
				g.widen = true
			}
			names[alias] = ip
		}
		text := func(n ast.Node) string {
			return src[w.fset.Position(n.Pos()).Offset:w.fset.Position(n.End()).Offset]
		}
		for _, d := range file.Decls {
			g.imports[d] = names
			switch d := d.(type) {
			case *ast.FuncDecl:
				key := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					t := recvType(d.Recv.List[0].Type)
					key = t + "." + key
					g.methods[d.Name.Name] = append(g.methods[d.Name.Name], key)
					g.ofType[t] = append(g.ofType[t], key)
				} else if key == "init" {
					key = "init#" + strconv.Itoa(len(g.inits))
					g.inits = append(g.inits, key)
				}
				g.decls[key] = append(g.decls[key], goDecl{file: f.Name, text: text(d), node: d, widen: d.Body == nil})
			case *ast.GenDecl:
				embed := d.Doc != nil && strings.Contains(commentText(d.Doc), "go:embed")
				for _, s := range d.Specs {
					var ids []*ast.Ident
					switch s := s.(type) {
					case *ast.TypeSpec:
						ids = []*ast.Ident{s.Name}
					case *ast.ValueSpec:
						ids = s.Names
						if s.Doc != nil && strings.Contains(commentText(s.Doc), "go:embed") {
							embed = true
						}
					}
					for _, id := range ids {
						g.decls[id.Name] = append(g.decls[id.Name], goDecl{file: f.Name, text: text(d), node: d, widen: embed})
					}
				}
			}
		}
	}
	return g
}

// commentText is a comment group's raw text, directives included (Text
// drops //go: lines).
func commentText(c *ast.CommentGroup) string {
	var b strings.Builder
	for _, l := range c.List {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func recvType(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// filesHash is the hash of a package's Go and assembly files, tests
// excluded, by name and content: the key the walk widens to.
func filesHash(files []GoSourceFile) string {
	files = append([]GoSourceFile(nil), files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	h := sha256.New()
	for _, f := range files {
		if strings.HasSuffix(f.Name, "_test.go") || !strings.HasSuffix(f.Name, ".go") && !strings.HasSuffix(f.Name, ".s") {
			continue
		}
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write(f.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GoClosure is the work of one closure key: the declarations it hashed and
// the packages it widened to their files.
type GoClosure struct {
	Hash     string
	Decls    []string // "<import path> <key>", sorted
	Widened  []string // import paths, sorted
	Unloaded bool     // the entry package could not be loaded
}

// closure is the closure key of name (F, T or T.M) of the package at
// entry.
func (w *goWorld) closure(entry, name string) GoClosure {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.get(entry) == nil {
		return GoClosure{Unloaded: true}
	}
	type item struct{ pkg, key string }
	seen := map[item]bool{}
	byName := map[string]bool{}
	entered := map[string]bool{}
	widened := map[string]bool{}
	var work []item
	push := func(p, key string) {
		it := item{p, key}
		if !seen[it] {
			seen[it] = true
			work = append(work, it)
		}
	}
	enter := func(p string) {
		if entered[p] {
			return
		}
		entered[p] = true
		g := w.pkgs[p]
		if g.widen {
			widened[p] = true
		}
		for _, k := range g.inits {
			push(p, k)
		}
		for m := range byName {
			for _, k := range g.methods[m] {
				push(p, k)
			}
		}
	}
	// named takes every method of the name m in every package entered, now
	// and later: a call the types do not resolve to one method.
	named := func(m string) {
		if byName[m] {
			return
		}
		byName[m] = true
		for p := range entered {
			for _, k := range w.pkgs[p].methods[m] {
				push(p, k)
			}
		}
	}
	// object takes a resolved object of a package of the world.
	object := func(obj types.Object) {
		if obj == nil || obj.Pkg() == nil {
			return
		}
		// An interface method, of the world or of std (io.Writer.Write),
		// may run any method of its name: by name, wherever it is declared.
		if fn, ok := obj.(*types.Func); ok {
			if recv := fn.Type().(*types.Signature).Recv(); recv != nil && types.IsInterface(recv.Type()) {
				named(fn.Name())
				return
			}
		}
		if w.pkgs[obj.Pkg().Path()] == nil {
			return
		}
		q := obj.Pkg().Path()
		if fn, ok := obj.(*types.Func); ok {
			if recv := fn.Type().(*types.Signature).Recv(); recv != nil {
				t := recv.Type()
				if p, ok := t.(*types.Pointer); ok {
					t = p.Elem()
				}
				if n, ok := types.Unalias(t).(*types.Named); ok && !types.IsInterface(n) {
					enter(q)
					push(q, n.Origin().Obj().Name()+"."+fn.Name())
					return
				}
				named(fn.Name())
				return
			}
		}
		if v, ok := obj.(*types.Var); ok && v.IsField() {
			return
		}
		if obj.Pkg().Scope().Lookup(obj.Name()) == nil || obj.Parent() != nil && obj.Parent() != obj.Pkg().Scope() {
			return // a local, a parameter, a type parameter
		}
		enter(q)
		push(q, obj.Name())
	}
	enter(entry)
	push(entry, name)
	for len(work) > 0 {
		it := work[len(work)-1]
		work = work[:len(work)-1]
		g := w.pkgs[it.pkg]
		decls, ok := g.decls[it.key]
		if !ok {
			if t, _, isMethod := strings.Cut(it.key, "."); isMethod && g.decls[t] != nil {
				// A promoted method or a field: the type covers it.
				push(it.pkg, t)
				continue
			}
			widened[it.pkg] = true
			continue
		}
		if !strings.Contains(it.key, ".") {
			for _, k := range g.ofType[it.key] {
				push(it.pkg, k)
			}
		}
		for _, d := range decls {
			if d.widen {
				widened[it.pkg] = true
			}
			imports := g.imports[d.node]
			ast.Inspect(d.node, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if sel, ok := g.info.Selections[n]; ok {
						if sel.Kind() != types.FieldVal {
							object(sel.Obj())
						}
						return true
					}
					if obj := g.info.Uses[n.Sel]; obj != nil {
						object(obj)
						return true
					}
					// No type: by name, and as a qualified name of a
					// package of the world.
					if x, ok := n.X.(*ast.Ident); ok {
						if ip, ok := imports[x.Name]; ok && w.get(ip) != nil {
							enter(ip)
							push(ip, n.Sel.Name)
						}
					}
					named(n.Sel.Name)
				case *ast.Ident:
					if obj := g.info.Uses[n]; obj != nil {
						object(obj)
					} else if g.info.Defs[n] == nil && g.decls[n.Name] != nil {
						push(it.pkg, n.Name)
					}
				}
				return true
			})
		}
	}
	h := sha256.New()
	out := GoClosure{}
	var keys []string
	for it := range seen {
		if widened[it.pkg] {
			continue
		}
		for _, d := range w.pkgs[it.pkg].decls[it.key] {
			keys = append(keys, it.pkg+"\x00"+it.key+"\x00"+d.file+"\x00"+d.text)
		}
		out.Decls = append(out.Decls, it.pkg+" "+it.key)
	}
	for p := range widened {
		keys = append(keys, p+"\x00*\x00"+filesHash(w.pkgs[p].files))
		out.Widened = append(out.Widened, p)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(strconv.Itoa(len(k)) + ":" + k))
	}
	sort.Strings(out.Decls)
	sort.Strings(out.Widened)
	out.Hash = hex.EncodeToString(h.Sum(nil))
	return out
}

// onlyPullLinknames reports whether every //go:linkname directive of a
// file pulls a symbol into a function of the file that has no body
// (runtime/v1's syscall_syscall, runtime/rt's gopark): such a function
// widens the key when the walk reaches it, so the directive hides
// nothing. A directive on anything else may push a declaration of the
// package to code the walk does not see, and widens the package.
func onlyPullLinknames(file *ast.File) bool {
	bodyless := map[string]bool{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body == nil {
			bodyless[fd.Name.Name] = true
		}
	}
	for _, g := range file.Comments {
		for _, c := range g.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}
			if f := strings.Fields(rest); len(f) != 2 || !bodyless[f[0]] {
				return false
			}
		}
	}
	return true
}
