package check

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The source-provenance guard (stage E2b). A diagnostic lies in the file
// that holds the node it is reported at; a node the checker makes is in no
// file's tree, so it records its origin (made, clone, owner.go) and lies in
// its origin's file. E2 round 3 asserted ownership at every report but left
// the made nodes out of its domain: a variant's type parameter, made from
// the union's, reported S108 at an identifier of no file, and the assertion
// turned the diagnostic into an internal compiler error (E2 round 3 M1).
// The tests missed it because no program reported at a made node: a clean
// program made ownerless nodes and nothing looked at them.
//
// Four checks hold the class, none of them waiting for a diagnostic:
//   - TestCheckerSyntaxSynthesisHasOrigins inventories, with go/types, every
//     value of a syntax node type in the checker's source: each must be a
//     composite literal passed straight to made, or a copy through clone,
//     at a site the manifest lists with a program that reaches it; no type
//     other than a syntax node type has a node's layout, so no conversion
//     can make a node (E2c); and the parser is referenced only from the
//     functions that parse and check a whole file;
//   - TestSyntheticNodesHaveSourceOwners runs those programs, clean, in
//     both file slots and with an early use, and resolves every made node
//     against an index of the trees the test builds itself;
//   - TestOwnerResolution checks the same on two files whose nodes share
//     their offsets, for every tree node and every made node, and holds the
//     fault controls: no origin, a cycle, two origins, a nil node, a node
//     made of two files, a tree node recorded as made, the wrong file;
//   - TestInlineVariantParameterDiagnostics holds E2 round 3 M1's four
//     programs and a matrix of the parameter rules over inline variants,
//     each diagnostic expected at the union's own parameter, computed from
//     the source text, with the union's Info.Defs intact.
// TestErrorInSpan holds errorIn's whole-span bound with direct controls
// and the tail and catch programs whose spans end at a brace.
//
// Known gaps. The inventory reads syntax, not dataflow, so these stay open;
// unit E2d, a go/analysis pass over SSA that replaces the inventory, closes
// them (E2c round 1 review):
//   - m2: a type that exists only by substitution in a generic body is not
//     seen by the layout ban: func inner[T any](p T) any { return
//     &wrap[T]{NamePos: p} }, called as inner(id.NamePos), makes a
//     *wrap[syntax.Pos] with Ident's layout that no Types, Defs or Instances
//     entry holds. Not exploitable today (inferred): a conversion from it
//     must type-check in the generic body, where T is not syntax.Pos, and
//     reaching it outside the body spells the type, which is flagged.
//   - m3: a syntax method that returns a node, called through a checker-local
//     interface (type copier interface{ Copy() *syntax.Ident }), resolves to
//     a method of package check, so syntaxNodeCallees never sees it. Latent:
//     no exported syntax function or method returns a node except Parse.
//   - o1: (syntax.Parse)(src) at a listed site is reported as a value, a
//     harmless false positive; the same holds for (made)(...) and (clone)(...).
//   - o2: nodeTerm does not see a named pointer term (P interface{ ref }
//     with type ref *syntax.Ident). Harmless: the operand of the conversion
//     must already be a node pointer.
//   - a listed parser site can still parse text other than its input
//     without any closure; only dataflow tells the two apart.

const (
	provenanceSyntax = "ergo.local/prototype/internal/sketch/syntax"
	provenanceSketch = "ergo.local/prototype/internal/sketch/"
)

// checkerImports is the checker's import allow-list (E2b round 3 review
// M2). The inventory sees only package check's own source, so a node built
// in another package, or filled in by a reflective decoder, never meets a
// scanned construction. The guard closes the package's boundary instead:
// each listed package was reviewed as unable to build a value of a caller's
// syntax node type, and any other import fails the guard until it is
// reviewed and listed here. reflect and unsafe are never listed.
var checkerImports = map[string]bool{
	"cmp": true, "container/heap": true, "encoding/binary": true, "fmt": true,
	"go/constant": true, "go/token": true, "maps": true, "math": true,
	"math/bits": true, "path": true, "regexp": true, "runtime": true,
	"slices": true, "sort": true, "strconv": true, "strings": true,
	"sync": true, "unicode": true, "unicode/utf8": true,
	provenanceSketch + "std": true, provenanceSyntax: true,
}

// syntaxNodeCallees lists the syntax package's functions, methods and
// function-valued variables whose results hold a node type that the checker
// may use (the parser entry point), each with the checker functions allowed
// to reference it. The syntax package itself is outside the inventory, so any
// other node-returning callee fails the guard; a syntax function that builds
// a node for the checker must go through made or clone. A listed callee is
// pinned to its sites, not admitted at any (E2b round 4 review M2): each site
// parses a whole file and checks it as a package, where a parse elsewhere
// (a snippet the checker writes) makes nodes of no checked file. A site only
// calls the callee; a function value of it would carry the parser elsewhere.
// A site is the innermost enclosing function, closures by their Go-style
// names (scanSynthesisWith): prelude parses inside its preludeOnce.Do
// closure, prelude.func1.
var syntaxNodeCallees = map[string][]string{
	"Parse": {"CheckSource", "prelude.func1", "(*stdImporter).Import"},
}

// sourceEntries are the checker's own functions that parse text a caller
// passes in, with the checker functions allowed to reference them: none, so
// a pass cannot reach the parser on a snippet through the package's entry.
// prelude and the std importer parse only the embedded sources.
var sourceEntries = map[string][]string{"CheckSource": nil}

// constructionSite is one place the checker makes a syntax node: the
// function and the node's type.
type constructionSite struct{ fn, kind string }

// siteName names a site twice: fn, for the construction manifest, and site,
// with its receiver, for the parser manifests.
type siteName struct{ fn, site string }

// The variant fixtures: a union whose inline variant uses its parameter,
// unconstrained and constrained, and one used before the union.
const (
	variantFixture            = "type U[T] = | V{X T} | W{}\n"
	variantConstrainedFixture = "type U[T number] = | V{X T} | W{}\n"
	variantEarlyFixture       = "type A { S V[int] }\ntype U[T, S] = | V{X T} | Y{Z S, Q T} | W{}\n"
	compoundFixture           = "func f() int {\n\tlet mut x = 1\n\tx += 2\n\tx\n}\n"
	patternFixture            = "type P { A int }\nfunc f(p P) int {\n\tlet P{A: a} = p\n\ta\n}\n"
)

// constructionManifest lists every construction site of the checker, named
// by its innermost enclosing function as syntaxNodeCallees names its sites,
// with a clean program that reaches it. A new site fails the inventory until
// it is listed here with its program.
var constructionManifest = map[constructionSite]string{
	{"declareVariant", "TypeDecl"}:   variantFixture,
	{"declareVariant", "StructType"}: variantFixture,
	{"variantParams", "Ident"}:       variantFixture,
	{"variantParams", "Field"}:       variantConstrainedFixture,
	{"variantParams", "FieldList"}:   variantEarlyFixture,
	{"varDecl", "VarSpec"}:           patternFixture,
	{"assignStmt", "BinaryExpr"}:     compoundFixture,
}

// provenanceFixtures are the clean programs the sweep runs: the manifest's
// and the remaining shapes of the variant rules.
func provenanceFixtures() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, src := range constructionManifest {
		add(src)
	}
	for _, src := range []string{
		variantFixture, variantConstrainedFixture, variantEarlyFixture, compoundFixture, patternFixture,
		"type U[T, S] = | V{X T} | W{Y S} | Z{}\n",
		"type U[T, S number] = | V{X S} | W{}\n",
		"type U[R, T] = | V{X T} | W{}\ntype A { S V[int] }\n",
		"type A { S V[int, string] }\ntype U[T, S] = | V{X T, Y S} | W{}\nfunc f() int {\n\tlet mut n = 1\n\tn *= 3\n\tlet (a, b) = (n, 2)\n\ta + b\n}\n",
	} {
		add(src)
	}
	sort.Strings(out)
	return out
}

// synthesisScan is the inventory of the checker's source: each site that
// makes a syntax node through made or clone, and each value of a syntax
// node type that does not.
type synthesisScan struct {
	sites map[constructionSite]int
	// parses counts each listed parser reference by its callee and site.
	parses     map[constructionSite]int
	violations []string
}

var (
	provenanceFset     = token.NewFileSet()
	provenanceImporter = importer.ForCompiler(provenanceFset, "source", nil)
	provenanceMu       sync.Mutex
)

// fixtureImporter serves the controls' packages outside check from source
// text, and every other import from the source importer, so a fixture and
// the checker share one syntax package.
type fixtureImporter map[string][]*ast.File

func (fx fixtureImporter) Import(path string) (*types.Package, error) {
	if files, ok := fx[path]; ok {
		conf := types.Config{Importer: provenanceImporter}
		return conf.Check(path, provenanceFset, files, nil)
	}
	return provenanceImporter.Import(path)
}

// scanSynthesis checks the non-test checker source by method set, not by
// package or spelling. Only made and clone may construct/copy nodes. A
// literal passed straight to made is registered there; generic operations
// cannot hide copies behind a type parameter. Checker-local node types,
// reflect, unsafe and imports off checkerImports are forbidden even when no
// program reaches them. extra adds files to package check, or replaces the
// file of the same name; a name with a directory is a file of package
// provenanceSketch+dir, which the checker's source may import.
func scanSynthesis(t *testing.T, extra map[string]string) synthesisScan {
	return scanSynthesisWith(t, extra, syntaxNodeCallees)
}

func scanSynthesisWith(t *testing.T, extra map[string]string, nodeCallees map[string][]string) synthesisScan {
	t.Helper()
	provenanceMu.Lock()
	defer provenanceMu.Unlock()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if _, replaced := extra[p]; replaced || strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(provenanceFset, p, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	fixtures := fixtureImporter{}
	for _, name := range names {
		f, err := parser.ParseFile(provenanceFset, name, extra[name], 0)
		if err != nil {
			t.Fatal(err)
		}
		if dir := filepath.Dir(name); dir != "." {
			fixtures[provenanceSketch+dir] = append(fixtures[provenanceSketch+dir], f)
			continue
		}
		files = append(files, f)
	}
	info := &types.Info{
		Types:     map[ast.Expr]types.TypeAndValue{},
		Defs:      map[*ast.Ident]types.Object{},
		Uses:      map[*ast.Ident]types.Object{},
		Instances: map[*ast.Ident]types.Instance{},
	}
	conf := types.Config{Importer: fixtures}
	pkg, err := conf.Check("check", provenanceFset, files, info)
	if err != nil {
		t.Fatalf("type-check the checker: %v", err)
	}
	var node *types.Interface
	var syntaxPkg *types.Package
	for _, imp := range pkg.Imports() {
		if imp.Path() == provenanceSyntax {
			syntaxPkg = imp
			node = imp.Scope().Lookup("Node").Type().Underlying().(*types.Interface)
		}
	}
	if node == nil {
		t.Fatal("the checker does not import the syntax package")
	}
	isNode := func(typ types.Type) (string, bool) {
		if typ == nil {
			return "", false
		}
		typ = types.Unalias(typ)
		if p, ok := typ.(*types.Pointer); ok {
			typ = types.Unalias(p.Elem())
		}
		kind := types.TypeString(typ, func(*types.Package) string { return "" })
		return kind, types.Implements(typ, node) || types.Implements(types.NewPointer(typ), node)
	}
	isParameter := func(typ types.Type) bool {
		if typ == nil {
			return false
		}
		typ = types.Unalias(typ)
		if p, ok := typ.(*types.Pointer); ok {
			typ = types.Unalias(p.Elem())
		}
		_, ok := typ.(*types.TypeParam)
		return ok
	}
	// isNodeValue reports a node struct type itself: a value of it is a node
	// of its own, where a pointer or an interface only refers to one.
	isNodeValue := func(typ types.Type) (string, bool) {
		kind, ok := isNode(typ)
		if !ok {
			return "", false
		}
		switch typ.Underlying().(type) {
		case *types.Pointer, *types.Interface:
			return "", false
		}
		return kind, true
	}
	// containsNode reports whether a value of typ can hold or yield a node.
	var containsNode func(typ types.Type, seen map[types.Type]bool) bool
	containsNode = func(typ types.Type, seen map[types.Type]bool) bool {
		if typ == nil || seen[typ] {
			return false
		}
		seen[typ] = true
		if tuple, ok := typ.(*types.Tuple); ok {
			for i := range tuple.Len() {
				if containsNode(tuple.At(i).Type(), seen) {
					return true
				}
			}
			return false
		}
		if _, ok := isNode(typ); ok {
			return true
		}
		switch u := typ.Underlying().(type) {
		case *types.Pointer:
			return containsNode(u.Elem(), seen)
		case *types.Slice:
			return containsNode(u.Elem(), seen)
		case *types.Array:
			return containsNode(u.Elem(), seen)
		case *types.Chan:
			return containsNode(u.Elem(), seen)
		case *types.Map:
			return containsNode(u.Key(), seen) || containsNode(u.Elem(), seen)
		case *types.Struct:
			for i := range u.NumFields() {
				if containsNode(u.Field(i).Type(), seen) {
					return true
				}
			}
		case *types.Signature:
			return containsNode(u.Results(), seen)
		}
		return false
	}
	scan := synthesisScan{sites: map[constructionSite]int{}, parses: map[constructionSite]int{}}
	scanLayouts(&scan, pkg, syntaxPkg, info, isNode)
	madeFn := pkg.Scope().Lookup("made")
	cloneFn := pkg.Scope().Lookup("clone")
	callee := func(call *ast.CallExpr) (types.Object, *ast.Ident) {
		fun := call.Fun
		if ix, ok := fun.(*ast.IndexExpr); ok {
			fun = ix.X
		}
		if ix, ok := fun.(*ast.IndexListExpr); ok {
			fun = ix.X
		}
		id, ok := fun.(*ast.Ident)
		if !ok {
			return nil, nil
		}
		return info.Uses[id], id
	}
	// pinned reports whether a reference to callee from the enclosing function
	// site is listed, and counts the listed ones.
	pinned := func(callee, site string, sites []string) bool {
		if !slices.Contains(sites, site) {
			return false
		}
		scan.parses[constructionSite{site, callee}]++
		return true
	}
	// literals counts the function literals directly inside each enclosing
	// function, by its site name.
	literals := map[string]int{}
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			at := provenanceFset.Position(imp.Pos())
			switch {
			case path == "reflect" || path == "unsafe":
				scan.violations = append(scan.violations, fmt.Sprintf("%s:%d: %s import permits unrecorded construction", filepath.Base(at.Filename), at.Line, path))
			case !checkerImports[path]:
				scan.violations = append(scan.violations, fmt.Sprintf("%s:%d: import %q is not in checkerImports, the reviewed import allow-list", filepath.Base(at.Filename), at.Line, path))
			}
		}
		for _, decl := range f.Decls {
			// A site is named by its innermost enclosing function, as Go
			// names closures (E2c round 1 review M1): a declared function by
			// its name, the Nth function literal directly inside an enclosing
			// function F by F.funcN, counted in source order, so a literal
			// nested in CheckSource.func1 is CheckSource.func1.func1 (Go
			// itself spells that one CheckSource.func1.1). A literal outside
			// any function is package.funcN, counted over the package. fn
			// names a site in the construction manifest; site names it with
			// its receiver, as the parser manifest does. A function literal
			// at a listed site is therefore a site of its own: it is not
			// listed, so it cannot carry the parser out of its site.
			names := []siteName{{"package", "package"}}
			trusted := false
			if fd, ok := decl.(*ast.FuncDecl); ok {
				names[0] = siteName{fd.Name.Name, strings.ReplaceAll(info.Defs[fd.Name].(*types.Func).FullName(), pkg.Path()+".", "")}
				trusted = info.Defs[fd.Name] == madeFn || info.Defs[fd.Name] == cloneFn
			}
			var stack []ast.Node
			ast.Inspect(decl, func(n ast.Node) bool {
				if n == nil {
					if _, ok := stack[len(stack)-1].(*ast.FuncLit); ok {
						names = names[:len(names)-1]
					}
					stack = stack[:len(stack)-1]
					return true
				}
				parent := func(i int) ast.Node {
					if len(stack) >= i {
						return stack[len(stack)-i]
					}
					return nil
				}
				stack = append(stack, n)
				if _, ok := n.(*ast.FuncLit); ok {
					outer := names[len(names)-1]
					literals[outer.site]++
					suffix := fmt.Sprintf(".func%d", literals[outer.site])
					names = append(names, siteName{outer.fn + suffix, outer.site + suffix})
				}
				fn, site := names[len(names)-1].fn, names[len(names)-1].site
				at := provenanceFset.Position(n.Pos())
				where := fmt.Sprintf("%s:%d %s", filepath.Base(at.Filename), at.Line, fn)
				if spec, ok := n.(*ast.TypeSpec); ok {
					if obj, ok := info.Defs[spec.Name].(*types.TypeName); ok && !obj.IsAlias() {
						if _, ok := obj.Type().(*types.Named); ok {
							if _, ok := isNode(obj.Type()); ok {
								scan.violations = append(scan.violations, where+": local node type "+obj.Name()+" is outside the parser's trees")
							}
						}
					} else if ok {
						// An alias of a named node type only renames a syntax
						// type; an alias of an unnamed one declares a node type.
						target := types.Unalias(obj.Type())
						if p, ok := target.(*types.Pointer); ok {
							target = types.Unalias(p.Elem())
						}
						if _, named := target.(*types.Named); !named {
							if kind, ok := isNode(target); ok {
								scan.violations = append(scan.violations, where+": alias "+obj.Name()+" declares the unnamed node type "+kind)
							}
						}
					}
				}
				// A function, method or function value of the syntax package
				// that yields a node is the parser, referenced only from its
				// listed sites; a reference, not only a call, can hand its node
				// over. The checker's own caller-text entry is pinned the same way.
				if id, ok := n.(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == provenanceSyntax {
						_, isFunc := obj.(*types.Func)
						_, isVar := obj.(*types.Var)
						if sig, ok := obj.Type().Underlying().(*types.Signature); ok && (isFunc || isVar) && containsNode(sig.Results(), map[types.Type]bool{}) {
							name := obj.Name()
							if fn, ok := obj.(*types.Func); ok {
								name = strings.ReplaceAll(fn.FullName(), provenanceSyntax+".", "")
							}
							// A listed site only calls the callee: a function value
							// stored there would reach any other site.
							var fun ast.Node = id
							if sel, ok := parent(2).(*ast.SelectorExpr); ok && sel.Sel == id {
								fun = sel
							}
							call, _ := parent(2).(*ast.CallExpr)
							if fun != ast.Node(id) {
								call, _ = parent(3).(*ast.CallExpr)
							}
							if sites, ok := nodeCallees[name]; !ok {
								scan.violations = append(scan.violations, where+": syntax callee "+name+" yields a node and is not in syntaxNodeCallees")
							} else if call == nil || ast.Node(call.Fun) != fun {
								scan.violations = append(scan.violations, where+": syntax callee "+name+" is used as a value in "+site+"; its listed sites may only call it")
							} else if !pinned(name, site, sites) {
								scan.violations = append(scan.violations, where+": syntax callee "+name+" is referenced in "+site+", which syntaxNodeCallees does not list for it")
							}
						}
					}
					if fn, ok := info.Uses[id].(*types.Func); ok && fn.Pkg() == pkg {
						name := strings.ReplaceAll(fn.FullName(), pkg.Path()+".", "")
						if sites, ok := sourceEntries[name]; ok && !pinned(name, site, sites) {
							scan.violations = append(scan.violations, where+": "+name+" parses caller text and is referenced in "+site+", which sourceEntries does not list for it")
						}
					}
					// made and clone are only called (E2c round 1 review m1): the
					// manifest counts a site at each call, so a function value of
					// either would construct at a site it never sees. The callee
					// may carry explicit type arguments.
					if obj := info.Uses[id]; obj != nil && (obj == madeFn || obj == cloneFn) {
						var fun ast.Node = id
						call, _ := parent(2).(*ast.CallExpr)
						switch ix := parent(2).(type) {
						case *ast.IndexExpr:
							if ix.X == ast.Expr(id) {
								fun = ix
								call, _ = parent(3).(*ast.CallExpr)
							}
						case *ast.IndexListExpr:
							if ix.X == ast.Expr(id) {
								fun = ix
								call, _ = parent(3).(*ast.CallExpr)
							}
						}
						if call == nil || ast.Node(call.Fun) != fun {
							scan.violations = append(scan.violations, where+": "+obj.Name()+" is used as a value in "+site+"; it may only be called, so the manifest sees each site")
						}
					}
				}
				if trusted {
					return true
				}
				if id, ok := n.(*ast.Ident); ok {
					// A type argument that is a node struct copies or makes nodes
					// in the generic body; a pointer or an interface only refers.
					if inst, ok := info.Instances[id]; ok && info.Uses[id] != madeFn && info.Uses[id] != cloneFn {
						for i := range inst.TypeArgs.Len() {
							if kind, ok := isNodeValue(inst.TypeArgs.At(i)); ok {
								scan.violations = append(scan.violations, where+": generic instantiation "+id.Name+" with node argument "+kind)
								break
							}
						}
					}
				}
				if call, ok := n.(*ast.CallExpr); ok {
					obj, id := callee(call)
					if b, ok := obj.(*types.Builtin); ok && b.Name() == "new" && len(call.Args) == 1 {
						if kind, ok := isNode(info.Types[call.Args[0]].Type); ok {
							scan.violations = append(scan.violations, where+": new("+kind+") makes a node with no origin")
						}
					}
					// A conversion to a node struct copies one; a conversion to a
					// node pointer may retype memory no made call registered
					// (type raw syntax.Ident; (*syntax.Ident)(&raw{})). A
					// conversion to an interface only refers to its operand. A type
					// parameter is not an interface here: its underlying type is
					// its constraint, and a conversion to it converts to every
					// term of its type set (E2b round 4 review M1).
					if info.Types[call.Fun].IsType() {
						target := types.Unalias(info.TypeOf(call))
						if tp, ok := target.(*types.TypeParam); ok {
							if kind, ok := nodeTerm(tp.Constraint(), isNode); ok {
								scan.violations = append(scan.violations, where+": conversion to type parameter "+tp.Obj().Name()+", whose type set holds the node "+kind+", outside made and clone")
							}
						} else if _, ok := target.Underlying().(*types.Interface); !ok {
							if kind, ok := isNode(target); ok {
								scan.violations = append(scan.violations, where+": conversion produces a "+kind+" node outside made and clone")
							}
						}
					}
					if obj != nil && obj == cloneFn {
						inst := info.Instances[id]
						if inst.TypeArgs == nil || inst.TypeArgs.Len() == 0 {
							scan.violations = append(scan.violations, where+": a clone whose copied type is unknown")
						} else if kind, ok := isNode(inst.TypeArgs.At(0)); ok {
							scan.sites[constructionSite{fn, kind}]++
						}
					}
				}
				e, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				tv, ok := info.Types[e]
				if !ok || !tv.IsValue() {
					return true
				}
				_, isLit := e.(*ast.CompositeLit)
				_, isDeref := e.(*ast.StarExpr)
				if (isLit || isDeref) && isParameter(tv.Type) {
					scan.violations = append(scan.violations, where+": construction or copy of a type parameter outside made and clone")
					return true
				}
				kind, ok := isNode(tv.Type)
				if !ok {
					return true
				}
				// Reading a node pointer/interface does not copy its node.
				// Literals, dereferences, new and conversions are checked above
				// or below; a concrete node value also copies on an ordinary use.
				if _, ok := types.Unalias(tv.Type).(*types.Pointer); ok && !isLit && !isDeref {
					return true
				}
				if _, ok := tv.Type.Underlying().(*types.Interface); ok && !isLit && !isDeref {
					return true
				}
				lit, isLit := e.(*ast.CompositeLit)
				addr, isAddr := parent(2).(*ast.UnaryExpr)
				call, isCall := parent(3).(*ast.CallExpr)
				if isLit && isAddr && addr.Op == token.AND && addr.X == ast.Expr(lit) && isCall && len(call.Args) == 3 && call.Args[1] == ast.Expr(addr) {
					if obj, _ := callee(call); obj != nil && obj == madeFn {
						scan.sites[constructionSite{fn, kind}]++
						return true
					}
				}
				scan.violations = append(scan.violations, fmt.Sprintf("%s: a %s value (%T) the checker makes or copies outside made and clone", where, kind, e))
				return true
			})
		}
	}
	sort.Strings(scan.violations)
	return scan
}

// nodeTerm reports a term of a constraint's type set that is a node type or
// a pointer to one, at any depth, walking unions, ~ terms and embedded
// interfaces. isNode(P) is false for interface{ *syntax.VarSpec | *raw }.
func nodeTerm(constraint types.Type, isNode func(types.Type) (string, bool)) (string, bool) {
	iface, ok := constraint.Underlying().(*types.Interface)
	if !ok {
		return "", false
	}
	for i := range iface.NumEmbeddeds() {
		var terms []types.Type
		if u, ok := types.Unalias(iface.EmbeddedType(i)).(*types.Union); ok {
			for j := range u.Len() {
				terms = append(terms, u.Term(j).Type())
			}
		} else {
			terms = append(terms, iface.EmbeddedType(i))
		}
		for _, term := range terms {
			if _, ok := term.Underlying().(*types.Interface); ok {
				if kind, ok := nodeTerm(term, isNode); ok {
					return kind, true
				}
				continue
			}
			term = types.Unalias(term)
			for {
				p, ok := term.(*types.Pointer)
				if !ok {
					break
				}
				term = types.Unalias(p.Elem())
			}
			if kind, ok := isNode(term); ok {
				return kind, true
			}
		}
	}
	return "", false
}

// scanLayouts holds the type-level condition of every conversion (E2c). Go
// converts a value to a node type only from a type whose underlying type is
// identical to the node's, ignoring struct tags, or from a pointer to one
// (spec: Conversions). So no type the checker's source spells, or infers
// outside a generic body, named or unnamed, at any pointer depth, other than
// the syntax node types themselves, may have the underlying type of a syntax
// node: the type alone fails, with or without a conversion, however it is
// spelled. A type that exists only by substitution inside a generic body is
// not seen (E2c round 1 review m2; Known gaps in the header).
func scanLayouts(scan *synthesisScan, pkg, syntaxPkg *types.Package, info *types.Info, isNode func(types.Type) (string, bool)) {
	type layout struct {
		name string
		typ  types.Type
	}
	var layouts []layout
	for _, name := range syntaxPkg.Scope().Names() {
		tn, ok := syntaxPkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		if _, ok := isNode(tn.Type()); !ok {
			continue
		}
		if _, ok := tn.Type().Underlying().(*types.Interface); !ok {
			layouts = append(layouts, layout{name, tn.Type().Underlying()})
		}
	}
	isSyntaxNode := func(n *types.Named) bool {
		if n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != provenanceSyntax {
			return false
		}
		_, ok := isNode(n)
		return ok
	}
	qualifier := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
	reported := map[string]bool{}
	check := func(t types.Type, at token.Pos) {
		for _, l := range layouts {
			if !types.IdenticalIgnoreTags(t.Underlying(), l.typ) {
				continue
			}
			p := provenanceFset.Position(at)
			v := fmt.Sprintf("%s:%d: type %s has the layout of syntax.%s; a conversion from it makes a node outside made and clone", filepath.Base(p.Filename), p.Line, types.TypeString(t, qualifier), l.name)
			if !reported[v] {
				reported[v] = true
				scan.violations = append(scan.violations, v)
			}
			return
		}
	}
	seen := map[types.Type]bool{}
	var walk, parts func(t types.Type, at token.Pos)
	walk = func(t types.Type, at token.Pos) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch n := t.(type) {
		case *types.Alias:
			walk(types.Unalias(n), at)
			return
		case *types.Named:
			if isSyntaxNode(n) {
				return
			}
			if n.Obj().Pkg() == pkg {
				at = n.Obj().Pos()
			}
			check(n, at)
			for i := range n.TypeArgs().Len() {
				walk(n.TypeArgs().At(i), at)
			}
			// The underlying type is the named type's own layout, checked
			// above (and not again at its type literal); its parts are walked.
			seen[n.Underlying()] = true
			parts(n.Underlying(), at)
			return
		}
		check(t, at)
		parts(t, at)
	}
	parts = func(t types.Type, at token.Pos) {
		switch u := t.(type) {
		case *types.Pointer:
			walk(u.Elem(), at)
		case *types.Slice:
			walk(u.Elem(), at)
		case *types.Array:
			walk(u.Elem(), at)
		case *types.Chan:
			walk(u.Elem(), at)
		case *types.Map:
			walk(u.Key(), at)
			walk(u.Elem(), at)
		case *types.Struct:
			for i := range u.NumFields() {
				walk(u.Field(i).Type(), at)
			}
		case *types.Tuple:
			for i := range u.Len() {
				walk(u.At(i).Type(), at)
			}
		case *types.Signature:
			for _, tuple := range []*types.Tuple{u.Params(), u.Results()} {
				if tuple != nil {
					parts(tuple, at)
				}
			}
			for i := range u.TypeParams().Len() {
				walk(u.TypeParams().At(i), at)
			}
		case *types.Interface:
			for i := range u.NumExplicitMethods() {
				walk(u.ExplicitMethod(i).Type(), at)
			}
			for i := range u.NumEmbeddeds() {
				walk(u.EmbeddedType(i), at)
			}
		case *types.Union:
			for i := range u.Len() {
				walk(u.Term(i).Type(), at)
			}
		case *types.TypeParam:
			walk(u.Constraint(), at)
		}
	}
	type ref struct {
		at  token.Pos
		typ types.Type
	}
	var refs []ref
	for e, tv := range info.Types {
		refs = append(refs, ref{e.Pos(), tv.Type})
	}
	for id, obj := range info.Defs {
		if obj != nil {
			refs = append(refs, ref{id.Pos(), obj.Type()})
		}
	}
	for id, inst := range info.Instances {
		refs = append(refs, ref{id.Pos(), inst.Type})
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].at < refs[j].at })
	for _, r := range refs {
		walk(r.typ, r.at)
	}
}

func TestCheckerSyntaxSynthesisHasOrigins(t *testing.T) {
	scan := scanSynthesis(t, nil)
	for _, v := range scan.violations {
		t.Errorf("unrecorded construction: %s", v)
	}
	var sites []string
	for site, n := range scan.sites {
		sites = append(sites, fmt.Sprintf("%s %s x%d", site.fn, site.kind, n))
		if _, ok := constructionManifest[site]; !ok {
			t.Errorf("construction site %s makes a %s and has no program in constructionManifest", site.fn, site.kind)
		}
	}
	for site := range constructionManifest {
		if scan.sites[site] == 0 {
			t.Errorf("constructionManifest lists %s %s, which the checker no longer makes", site.fn, site.kind)
		}
	}
	sort.Strings(sites)
	t.Logf("construction sites: %s", strings.Join(sites, "; "))
	for _, manifest := range []map[string][]string{syntaxNodeCallees, sourceEntries} {
		for callee, fns := range manifest {
			for _, fn := range fns {
				if scan.parses[constructionSite{fn, callee}] == 0 {
					t.Errorf("the parser manifest lists %s in %s, which no longer references it", callee, fn)
				}
			}
		}
	}

	// The controls: an eighth allocation, a shallow copy that escapes, a
	// new, a literal nested in a made one, and a recorded site the
	// manifest does not list each fail the inventory.
	controls := scanSynthesis(t, map[string]string{"e2b_control.go": `package check

import "ergo.local/prototype/internal/sketch/syntax"

func e2bControlAlloc() syntax.Node { return &syntax.Ident{Name: "x"} }

func e2bControlCopy(s *syntax.VarSpec) syntax.Node {
	v := *s
	return &v
}

func e2bControlNew() syntax.Node { return new(syntax.BasicLit) }

func e2bControlNested(c *checker, vs *syntax.VariantSpec) syntax.Node {
	return made(c, &syntax.TypeDecl{Spec: &syntax.StructType{}}, vs)
}

func e2bControlValue(f syntax.Field) syntax.Node { return f.Type }
`, "e2b_unsafe.go": `package check; import ("unsafe"; "ergo.local/prototype/internal/sketch/syntax"); func e2bControlUnsafe(s *syntax.VarSpec) syntax.Node { return unsafe.SliceData(append([]syntax.VarSpec(nil), unsafe.Slice(s, 1)...)) }`})
	for _, fn := range []string{"e2bControlAlloc", "e2bControlCopy", "e2bControlNew", "e2bControlNested", "e2bControlValue"} {
		hit := false
		for _, v := range controls.violations {
			hit = hit || strings.Contains(v, " "+fn+":")
		}
		if !hit {
			t.Errorf("control %s: the inventory does not see its construction", fn)
		}
	}
	// The unsafe copy makes no literal, new, dereference or instantiation;
	// only its import can be seen (E2b round 3 review M1).
	unsafeHit := false
	for _, v := range controls.violations {
		unsafeHit = unsafeHit || strings.HasPrefix(v, "e2b_unsafe.go:1: unsafe import")
	}
	if !unsafeHit {
		t.Errorf("control e2bControlUnsafe: the inventory does not see its unsafe import: %v", controls.violations)
	}
	if controls.sites[constructionSite{"e2bControlNested", "TypeDecl"}] != 1 {
		t.Errorf("control e2bControlNested: its made TypeDecl is not a site: %v", controls.sites)
	}
	if extra := len(controls.violations) - len(scan.violations); extra < 7 {
		t.Errorf("the controls add %d violations, want at least 7: %v", extra, controls.violations)
	}
}

// Every escape has its own source-only control: no corpus program needs to
// call it, and no other forbidden construction can account for its rejection.
func TestSynthesisInventoryControls(t *testing.T) {
	const (
		dupPkg   = `package e2bdup; import "ergo.local/prototype/internal/sketch/syntax"; func Dup(s *syntax.VarSpec) *syntax.VarSpec { return &syntax.VarSpec{Names: s.Names} }`
		makerPkg = `package e2bmaker; import "ergo.local/prototype/internal/sketch/syntax"; type Maker interface{ Make(*syntax.VarSpec) *syntax.VarSpec }`
	)
	for _, ctl := range []struct {
		name, src, want string
		// fixtures are packages outside check; callees, when set, replaces
		// syntaxNodeCallees.
		fixtures map[string]string
		callees  map[string][]string
	}{
		{name: "generic-copy", src: `package check; func dupNode[T any](p *T) *T { v := *p; return &v }`, want: "of a type parameter"},
		{name: "generic-pointer-copy", src: `package check; func copyPointer[T any](p **T) *T { return *p }`, want: "of a type parameter"},
		{name: "generic-literal", src: `package check; func literal[T ~struct{ X int }]() T { return T{} }`, want: "of a type parameter"},
		{name: "generic-value-instance", src: `package check; func identity[T any](p T) T { return p }; var nodeIdentity = identity[syntax.Ident]`, want: "generic instantiation"},
		{name: "local-node", src: `package check; type spanAt struct{ p, e syntax.Pos }; func (s spanAt) Pos() syntax.Pos { return s.p }; func (s spanAt) End() syntax.Pos { return s.e }`, want: "local node type spanAt"},
		{name: "local-pointer-node", src: `package check; type spanAt struct{ p, e syntax.Pos }; func (s *spanAt) Pos() syntax.Pos { return s.p }; func (s *spanAt) End() syntax.Pos { return s.e }`, want: "local node type spanAt"},
		{name: "pointer-conversion", src: `package check; func converted(p *syntax.Ident) syntax.Node { return (*syntax.Ident)(p) }`, want: "conversion"},
		{name: "pointer-retype", src: `package check; type raw syntax.Ident; func retyped() syntax.Node { return (*syntax.Ident)(&raw{Name: "x"}) }`, want: "conversion produces a Ident"},
		{name: "unnamed-value-copy", src: `package check; func copied(p struct{ *syntax.Ident }) syntax.Node { return p }`, want: "outside made and clone"},
		{name: "unnamed-alias", src: `package check; type nodeAlias = struct{ *syntax.Ident }`, want: "alias nodeAlias declares the unnamed node type"},
		{name: "reflection", src: `package check; import "reflect"; var _ = reflect.TypeOf`, want: "reflect import"},
		// E2b round 3 review M1 and M2: unsafe, a module package's function,
		// an interface method declared in another package, a reflective
		// decoder, and a syntax callee off the list (here Parse, delisted).
		{name: "unsafe", src: `package check; import "unsafe"; func copied(s *syntax.VarSpec) syntax.Node { return unsafe.SliceData(append([]syntax.VarSpec(nil), unsafe.Slice(s, 1)...)) }`, want: "unsafe import"},
		{name: "module-package", src: `package check; import "ergo.local/prototype/internal/sketch/e2bdup"; func copied(s *syntax.VarSpec) syntax.Node { return e2bdup.Dup(s) }`, want: `import "ergo.local/prototype/internal/sketch/e2bdup" is not in checkerImports`, fixtures: map[string]string{"e2bdup/dup.go": dupPkg}},
		{name: "foreign-interface-method", src: `package check; import "ergo.local/prototype/internal/sketch/e2bmaker"; func copied(m e2bmaker.Maker, s *syntax.VarSpec) syntax.Node { return m.Make(s) }`, want: `import "ergo.local/prototype/internal/sketch/e2bmaker" is not in checkerImports`, fixtures: map[string]string{"e2bmaker/maker.go": makerPkg}},
		{name: "reflective-decoder", src: `package check; import "encoding/json"; func decoded() syntax.Node { var v *syntax.VarSpec; _ = json.Unmarshal([]byte("{}"), &v); return v }`, want: `import "encoding/json" is not in checkerImports`},
		{name: "syntax-callee", src: `package check; func parsed() syntax.Node { parse := syntax.Parse; f, _ := parse(""); return f }`, want: "syntax callee Parse yields a node", callees: map[string][]string{}},
		// E2b round 4 review M1, generic retypes: a conversion to a type
		// parameter whose type set holds a node, exact, ~, inferred, union,
		// through an embedded constraint, and by value.
		{name: "generic-retype", src: `package check; type raw syntax.VarSpec; func retype[P interface{ *syntax.VarSpec }](r *raw) P { return P(r) }; func retyped() syntax.Node { return retype[*syntax.VarSpec](&raw{}) }`, want: "conversion to type parameter P, whose type set holds the node VarSpec"},
		{name: "generic-retype-tilde", src: `package check; type raw syntax.VarSpec; func retype[P interface{ ~*syntax.VarSpec }](r *raw) P { return P(r) }`, want: "conversion to type parameter P, whose type set holds the node VarSpec"},
		{name: "generic-retype-inferred", src: `package check; type raw syntax.VarSpec; func retype[P interface{ *syntax.VarSpec }](r *raw) P { return P(r) }; func retyped() syntax.Node { return retype(&raw{}) }`, want: "conversion to type parameter P, whose type set holds the node VarSpec"},
		{name: "generic-retype-union", src: `package check; type raw syntax.VarSpec; func retype[P interface{ *syntax.VarSpec | *raw }](r *raw) P { return P(r) }`, want: "conversion to type parameter P, whose type set holds the node VarSpec"},
		{name: "generic-retype-embedded", src: `package check; type raw syntax.VarSpec; type nodePointer interface{ *syntax.VarSpec }; func retype[P interface{ nodePointer }](r *raw) P { return P(r) }`, want: "conversion to type parameter P, whose type set holds the node VarSpec"},
		{name: "generic-retype-value", src: `package check; type raw syntax.VarSpec; func retype[E interface{ syntax.VarSpec }](r raw) E { return E(r) }`, want: "conversion to type parameter E, whose type set holds the node VarSpec"},
		// E2c: the layout-identical type alone fails, with no conversion. (A
		// want starts with ": " so the lowerer's corpus harvest, which keeps
		// strings that start with "type ", skips it.)
		{name: "layout-type", src: `package check; type raw syntax.Ident`, want: ": type raw has the layout of syntax.Ident"},
		{name: "layout-unnamed", src: `package check; func held(p **struct{ NamePos syntax.Pos; Name string }) {}`, want: ": type struct{NamePos syntax.Pos; Name string} has the layout of syntax.Ident"},
		{name: "layout-tagged", src: `package check; type tagged struct{ NamePos syntax.Pos "pos"; Name string }`, want: ": type tagged has the layout of syntax.Ident"},
		{name: "layout-instance", src: `package check; type rawOf[T any] struct{ NamePos syntax.Pos; Name T }; var _ *rawOf[string]`, want: ": type rawOf[string] has the layout of syntax.Ident"},
		{name: "layout-local", src: `package check; func local() int { type raw syntax.Ident; return len(raw{}.Name) }`, want: ": type raw has the layout of syntax.Ident"},
		// E2b round 4 review M2: the parser and the caller-text entry are
		// pinned to their sites; a pass or a package variable is none.
		{name: "parse-in-pass", src: `package check; func (c *checker) passParsed() syntax.Node { f, _ := syntax.Parse("type T int\n"); return f.Decls[0] }`, want: "syntax callee Parse is referenced in (*checker).passParsed, which syntaxNodeCallees does not list"},
		{name: "parse-package-value", src: `package check; var parseText = syntax.Parse`, want: "syntax callee Parse is used as a value in package"},
		{name: "parse-value-at-site", src: `package check; var parseHook func(string) (*syntax.File, error); func leaked() { parseHook = syntax.Parse }`, want: "syntax callee Parse is used as a value in leaked", callees: map[string][]string{"Parse": {"leaked"}}},
		{name: "check-source-in-pass", src: `package check; func (c *checker) reparsed() *Package { p, _ := CheckSource("type T int\n"); return p }`, want: "CheckSource parses caller text and is referenced in (*checker).reparsed, which sourceEntries does not list"},
		// E2c round 1 review M1: a function literal is a site of its own,
		// named as Go names closures, so one at a listed site that calls the
		// parser is not listed and cannot carry it to a pass.
		{name: "closure-at-site", src: `package check; var parseHook func(string) (*syntax.File, error); func leaked() { parseHook = func(s string) (*syntax.File, error) { return syntax.Parse(s) } }`, want: "syntax callee Parse is referenced in leaked.func1, which syntaxNodeCallees does not list", callees: map[string][]string{"Parse": {"leaked"}}},
		{name: "closure-returned-at-site", src: `package check; func leaked() func(string) (*syntax.File, error) { _ = func() {}; return func(s string) (*syntax.File, error) { return syntax.Parse(s) } }`, want: "syntax callee Parse is referenced in leaked.func2, which syntaxNodeCallees does not list", callees: map[string][]string{"Parse": {"leaked"}}},
		{name: "closure-nested-at-site", src: `package check; func leaked() { func() { _ = func() (*syntax.File, error) { return syntax.Parse("") } }() }`, want: "syntax callee Parse is referenced in leaked.func1.func1, which syntaxNodeCallees does not list", callees: map[string][]string{"Parse": {"leaked.func1"}}},
		{name: "closure-in-method-at-site", src: `package check; func (c *checker) leaked() func() { return func() { syntax.Parse("") } }`, want: "syntax callee Parse is referenced in (*checker).leaked.func1, which syntaxNodeCallees does not list", callees: map[string][]string{"Parse": {"(*checker).leaked"}}},
		{name: "closure-check-source-at-site", src: `package check; func leaked() func() { return func() { CheckSource("") } }`, want: "CheckSource parses caller text and is referenced in leaked.func1, which sourceEntries does not list"},
		// E2c round 1 review m1: made and clone are only called.
		{name: "clone-value", src: `package check; func cloned(c *checker, p *syntax.Ident) syntax.Node { f := clone[syntax.Ident, *syntax.Ident]; return f(c, p) }`, want: "clone is used as a value in cloned; it may only be called"},
		{name: "clone-value-inferred", src: `package check; var cloneIdent func(*checker, *syntax.Ident) *syntax.Ident = clone`, want: "clone is used as a value in package; it may only be called"},
		{name: "made-value", src: `package check; func maker() func(*checker, *syntax.Ident, syntax.Node) *syntax.Ident { return made[*syntax.Ident] }`, want: "made is used as a value in maker; it may only be called"},
	} {
		t.Run(ctl.name, func(t *testing.T) {
			extra := map[string]string{"e2b_control.go": strings.Replace(ctl.src, "package check;", "package check; import \""+provenanceSyntax+"\";", 1) + "; var _ syntax.Node"}
			for name, src := range ctl.fixtures {
				extra[name] = src
			}
			callees := syntaxNodeCallees
			if ctl.callees != nil {
				callees = ctl.callees
			}
			scan := scanSynthesisWith(t, extra, callees)
			for _, v := range scan.violations {
				if strings.HasPrefix(v, "e2b_control.go:") && strings.Contains(v, ctl.want) {
					return
				}
			}
			t.Fatalf("inventory missed %s: want %q in %v", ctl.name, ctl.want, scan.violations)
		})
	}
}

// The inventory flags what makes or copies a node struct, not what only
// refers to one (E2b round 3 review m1): pointer and interface type
// arguments, conversions to an interface, an alias of a named node type,
// and the syntax package's read-only helpers stay accepted.
func TestSynthesisInventoryAccepts(t *testing.T) {
	for _, ctl := range []struct{ name, src string }{
		{"pointer-instance", `package check; func identity[T any](p T) T { return p }; var nodeIdentity = identity[*syntax.Ident]`},
		{"interface-instance", `package check; import "slices"; func has(xs []syntax.Expr, x syntax.Expr) bool { return slices.Contains(xs, x) }`},
		{"pointer-key-clone", `package check; import "maps"; func cloned(m map[*syntax.SelectorExpr]int) map[*syntax.SelectorExpr]int { return maps.Clone(m) }`},
		{"interface-conversion", `package check; func widened(x *syntax.Ident) (syntax.Node, syntax.Expr) { return syntax.Node(x), syntax.Expr(x) }`},
		{"named-alias", `package check; type ident = syntax.Ident; type identRef = *syntax.Ident`},
		{"read-only-helpers", `package check; func walked(n syntax.Node) (k int) { syntax.Inspect(n, func(syntax.Node) bool { k++; return !syntax.IsNil(n) }); return k }`},
		// E2c: a type-parameter conversion with no node term, a layout one
		// field off a node's, and a named pointer to a node type.
		{"type-parameter-conversion", `package check; func widen[T ~int | ~int64](x int) T { return T(x) }`},
		{"near-layout", `package check; type nameAt struct{ NamePos syntax.Pos; name string }; type posName struct{ Name string; NamePos syntax.Pos }`},
		{"node-pointer-type", `package check; type identRef *syntax.Ident; func ref(x *syntax.Ident) identRef { return identRef(x) }`},
		// E2c round 2: made and clone called with explicit type arguments,
		// and a function literal that only calls them.
		{"explicit-instance-calls", `package check; func cloned(c *checker, p *syntax.Ident) (syntax.Node, syntax.Node) { return clone[syntax.Ident, *syntax.Ident](c, p), made[*syntax.Ident](c, &syntax.Ident{}, p) }`},
		{"closure-calls", `package check; func cloned(c *checker, p *syntax.Ident) func() syntax.Node { return func() syntax.Node { return clone(c, p) } }`},
	} {
		t.Run(ctl.name, func(t *testing.T) {
			src := strings.Replace(ctl.src, "package check;", "package check; import \""+provenanceSyntax+"\";", 1) + "; var _ syntax.Node"
			scan := scanSynthesis(t, map[string]string{"e2b_control.go": src})
			for _, v := range scan.violations {
				if strings.HasPrefix(v, "e2b_control.go:") {
					t.Errorf("inventory rejects %s, which makes or copies no node: %s", ctl.name, v)
				}
			}
		})
	}
}

// treeIndex is the test's own index from each node of the files' trees to
// the file's slot, built without the checker.
func treeIndex(files []*syntax.File) map[syntax.Node]int {
	index := map[syntax.Node]int{}
	for i, f := range files {
		syntax.Inspect(f, func(n syntax.Node) bool {
			index[n] = i
			return true
		})
	}
	return index
}

// expectedOwner follows a made node's origins in the checker's registry to
// a tree node of the test's index and returns its slot.
func expectedOwner(index map[syntax.Node]int, origins map[syntax.Node]syntax.Node, n syntax.Node) (int, error) {
	m := n
	for range len(origins) + 1 {
		origin, ok := origins[m]
		if !ok || origin == nil {
			return -1, fmt.Errorf("a %T with no recorded origin", m)
		}
		if slot, ok := index[origin]; ok {
			return slot, nil
		}
		m = origin
	}
	return -1, fmt.Errorf("a %T whose origins form a cycle", n)
}

func parseOrFatal(t testing.TB, src string) *syntax.File {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f
}

const unrelatedFile = "func unrelated() {}\n"

func TestSyntheticNodesHaveSourceOwners(t *testing.T) {
	reached := map[string]map[string]bool{}
	for _, src := range provenanceFixtures() {
		for slot := range 2 {
			t.Run(fmt.Sprintf("%q/slot=%d", src, slot), func(t *testing.T) {
				files := []*syntax.File{parseOrFatal(t, unrelatedFile), parseOrFatal(t, unrelatedFile)}
				files[slot] = parseOrFatal(t, src)
				c := newChecker("main", Std, prelude().scope)
				c.run(files)
				if ds := c.sorted(); len(ds) != 0 {
					t.Fatalf("a provenance fixture must be clean: %v", ds)
				}
				index := treeIndex(files)
				origins := c.madeNodes()
				if reached[src] == nil {
					reached[src] = map[string]bool{}
				}
				for n := range origins {
					kind := strings.TrimPrefix(fmt.Sprintf("%T", n), "*syntax.")
					reached[src][kind] = true
					if _, ok := index[n]; ok {
						t.Errorf("a %s of the tree is recorded as made", kind)
						continue
					}
					want, err := expectedOwner(index, origins, n)
					if err != nil {
						t.Errorf("%v", err)
						continue
					}
					if want != slot {
						t.Errorf("a made %s comes from file %d, not the fixture's file %d", kind, want, slot)
					}
					if got := c.fileOf(n); got == nil || got.index != want {
						t.Errorf("a made %s at %d..%d: the checker gives %s, its origin is in file %d", kind, n.Pos(), n.End(), fileName(got, "no file"), want)
					}
				}
				for id := range c.synthetic {
					if _, ok := origins[id]; !ok {
						t.Errorf("synthetic identifier %s at %d has no recorded origin", id.Name, id.Pos())
					}
				}
			})
		}
	}
	for site, src := range constructionManifest {
		if !reached[src][site.kind] {
			t.Errorf("the program of %s %s makes no %s: %q", site.fn, site.kind, site.kind, src)
		}
	}
}

// icePanic runs f and returns its panic, or "".
func icePanic(f func()) (ice string) {
	defer func() {
		if r := recover(); r != nil {
			ice = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func TestOwnerResolution(t *testing.T) {
	// Two files whose nodes share their offsets: an owner found by
	// position, or by the current file, names the wrong one.
	src := func(i int) string {
		return fmt.Sprintf("type U%d[T] = | V%d{X T} | W%d{}\nfunc f%d() int {\n\tlet mut x = 1\n\tx += 2\n\tlet P%d{A: a} = P%d{A: x}\n\ta\n}\ntype P%d { A int }\n", i, i, i, i, i, i, i)
	}
	files := []*syntax.File{parseOrFatal(t, src(0)), parseOrFatal(t, src(1))}
	c := newChecker("main", Std, prelude().scope)
	c.run(files)
	if ds := c.sorted(); len(ds) != 0 {
		t.Fatalf("the two files must be clean: %v", ds)
	}
	index := treeIndex(files)
	origins := c.madeNodes()
	kinds := map[string]int{}
	check := func(n syntax.Node, want int) {
		t.Helper()
		if got := c.fileOf(n); got == nil || got.index != want {
			t.Errorf("a %T at %d..%d: the checker gives %s, want file %d", n, n.Pos(), n.End(), fileName(got, "no file"), want)
		}
		for slot, fi := range c.files {
			c.cur = fi
			ice := icePanic(func() { c.assertOwner(n, "S108", "control", nil) })
			if (ice == "") != (slot == want) {
				t.Errorf("a %T of file %d with file %d current: ice %q", n, want, slot, ice)
			}
		}
	}
	for n, slot := range index {
		check(n, slot)
	}
	for n := range origins {
		want, err := expectedOwner(index, origins, n)
		if err != nil {
			t.Fatal(err)
		}
		kinds[fmt.Sprintf("%T", n)]++
		check(n, want)
	}
	if len(kinds) != 7 {
		t.Errorf("the two files make %d kinds of node, want all 7: %v", len(kinds), kinds)
	}

	// An origin chain resolves to the tree node at its end.
	f0, f1 := c.files[0], c.files[1]
	d0 := files[0].Decls[1].(*syntax.FuncDecl)
	d1 := files[1].Decls[1].(*syntax.FuncDecl)
	x0 := d0.Body.List[1].(*syntax.AssignStmt)
	x1 := d1.Body.List[1].(*syntax.AssignStmt)
	inner := made(c, &syntax.Ident{NamePos: x1.Pos(), Name: "x"}, x1)
	outer := made(c, &syntax.Ident{NamePos: x1.Pos(), Name: "x"}, inner)
	if got := c.fileOf(outer); got != f1 {
		t.Errorf("an origin chain into file 1 gives %s", fileName(got, "no file"))
	}

	// The fault controls: each is an internal compiler error with the
	// current file selected, in every build.
	c.cur = f0
	report := func(n syntax.Node) func() { return func() { c.assertOwner(n, "S108", "control", nil) } }
	cycleA := &syntax.Ident{NamePos: x0.Pos(), Name: "a"}
	cycleB := &syntax.Ident{NamePos: x0.Pos(), Name: "b"}
	made(c, cycleA, cycleB)
	made(c, cycleB, cycleA)
	conflict := made(c, &syntax.Ident{NamePos: x0.Pos(), Name: "c"}, x0)
	treeMade := d0.Body.List[0]
	made(c, treeMade, x0)
	viaTreeMade := made(c, &syntax.Ident{NamePos: treeMade.Pos(), Name: "d"}, treeMade)
	mixed := made(c, &syntax.BinaryExpr{X: x0.Lhs[0], OpPos: x0.TokPos, Op: syntax.ADD, Y: x1.Rhs[0]}, x0)
	mixedMade := made(c, &syntax.BinaryExpr{X: x0.Lhs[0], OpPos: x0.TokPos, Op: syntax.ADD, Y: outer}, x0)
	var typedNil *syntax.Ident
	for _, ctl := range []struct {
		name, want string
		f          func()
	}{
		{"no origin", "with no recorded origin", report(&syntax.Ident{NamePos: x0.Pos(), Name: "x"})},
		{"wrong file at the same offset", "file 1", report(outer)},
		{"cycle", "cycle", report(cycleA)},
		{"two origins", "two origins", func() { made(c, conflict, x1) }},
		{"tree node recorded as made", "recorded as made", report(treeMade)},
		{"made from a tree node recorded as made", "recorded as made", report(viaTreeMade)},
		{"made of two files", "holds a *syntax.BasicLit of file 1", report(mixed)},
		{"made of two files through a made child", "made from file 1", report(mixedMade)},
		{"nil", "a nil node", report(nil)},
		{"typed nil", "a nil node", report(typedNil)},
		{"made nil", "is nil", func() { made(c, typedNil, x0) }},
		{"made from nil", "has no origin", func() { made(c, &syntax.Ident{}, syntax.Node(typedNil)) }},
		{"made from itself", "its own origin", func() { id := &syntax.Ident{}; made(c, id, id) }},
	} {
		ice := icePanic(ctl.f)
		if !strings.Contains(ice, "internal compiler error") || !strings.Contains(ice, ctl.want) {
			t.Errorf("control %s: got %q, want an internal compiler error with %q", ctl.name, ice, ctl.want)
		}
	}
}

// ownerDiag is an expected diagnostic: the source text it underlines, by
// its byte offset in the file at slot File.
type ownerDiag struct {
	File, Offset, Length int
	Code, Message        string
}

// inlineVariantCase is one program of the parameter matrix: its files and
// the one diagnostic each rule gives at the union's own parameter.
type inlineVariantCase struct {
	name  string
	files []string
	want  []ownerDiag
}

// inlineVariantCases builds E2 round 3 M1's four programs and the matrix:
// the three parameter rules (declared twice, a prelude name, a type and a
// row) over unconstrained and constrained parameters, a parameter the
// variant uses or not, one or two variants that use it, no use, an early
// use before or after the union in its file, the use in another file in
// either order, and the union beside an unrelated file in either slot.
// Each expected diagnostic is the rule's message at the offending
// parameter of the union, found in the source text.
func inlineVariantCases() []inlineVariantCase {
	// after is the offset just after the first marker in src.
	after := func(src, marker string) int { return strings.Index(src, marker) + len(marker) }
	diag := func(off, length int, msg string) []ownerDiag {
		return []ownerDiag{{File: 0, Offset: off, Length: length, Code: CodeType, Message: msg}}
	}
	dupMsg := "type parameter T is declared twice"
	preludeMsg := "string is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"
	conflictMsg := "type parameter T is used both as a type and as a row (types.md, Generics 1)"
	m1A := "type U[T, T] = | V{X T} | W{}\n"
	m1B := "type A { S V[int, int] }\ntype U[T, T] = | V{X T} | W{}\n"
	m1C := "type A { S V[int] }\ntype U[string] = | V{X string} | W{}\n"
	m1D := "type U[T] = | V{X T, F func() needs T} | W{}\n"
	out := []inlineVariantCase{
		{"M1 duplicate", []string{m1A}, diag(after(m1A, "U[T, "), 1, dupMsg)},
		{"M1 duplicate, early use", []string{m1B}, diag(after(m1B, "U[T, "), 1, dupMsg)},
		{"M1 prelude, early use", []string{m1C}, diag(after(m1C, "U["), 6, preludeMsg)},
		{"M1 type and row", []string{m1D}, diag(after(m1D, "U["), 1, conflictMsg)},
	}
	type rule struct {
		name, params, consParams, arg, msg, marker string
		markerSkip, length                         int
		conflict                                   bool
	}
	rules := []rule{
		{name: "duplicate", params: "T, T", consParams: "T, T number", arg: "int, int", msg: dupMsg, marker: "T", markerSkip: 2, length: 1},
		{name: "prelude", params: "string", consParams: "string number", arg: "int", msg: preludeMsg, marker: "string", markerSkip: 1, length: 6},
		{name: "type and row", params: "T", arg: "int", msg: conflictMsg, marker: "T", markerSkip: 1, length: 1, conflict: true},
	}
	for _, r := range rules {
		param := "T"
		if r.name == "prelude" {
			param = "string"
		}
		for _, cons := range []bool{false, true} {
			if cons && r.conflict {
				continue // a constrained parameter has its constraint's kind
			}
			params := r.params
			if cons {
				params = r.consParams
			}
			for _, shape := range []string{"used", "unused", "two variants"} {
				if shape == "unused" && r.conflict {
					continue // a parameter no variant uses has no kind to conflict
				}
				var variants string
				switch shape {
				case "used":
					variants = "| V{X " + param + "} | W{}"
				case "unused":
					variants = "| V{X int} | W{}"
				case "two variants":
					variants = "| V{X " + param + "} | Y{Z " + param + "} | W{}"
				}
				if r.conflict {
					variants = strings.Replace(variants, "V{X T}", "V{X T, F func() needs T}", 1)
				}
				union := "type U[" + params + "] = " + variants + "\n"
				use := "type A { S V[" + r.arg + "] }\n"
				uses := shape != "unused" && !r.conflict
				name := fmt.Sprintf("%s/constrained=%v/%s", r.name, cons, shape)
				expect := func(file int, src string) []ownerDiag {
					off := strings.Index(src, "type U[") + len("type U[")
					list := src[off:]
					rel := -1
					for range r.markerSkip {
						rel += 1 + strings.Index(list[rel+1:], r.marker)
					}
					return []ownerDiag{{File: file, Offset: off + rel, Length: r.length, Code: CodeType, Message: r.msg}}
				}
				for slot := range 2 {
					files := []string{unrelatedFile, unrelatedFile}
					files[slot] = union
					out = append(out, inlineVariantCase{fmt.Sprintf("%s/alone/slot=%d", name, slot), files, expect(slot, union)})
				}
				if !uses {
					continue
				}
				before, after := use+union, union+use
				out = append(out,
					inlineVariantCase{name + "/use before", []string{before}, expect(0, before)},
					inlineVariantCase{name + "/use after", []string{after}, expect(0, after)},
					inlineVariantCase{name + "/use in file 0", []string{use, union}, expect(1, union)},
					inlineVariantCase{name + "/use in file 1", []string{union, use}, expect(0, union)},
				)
			}
		}
	}
	return out
}

// OwnerGuardPrograms returns this guard's one-file programs by name, for
// the file-placement guard (filesplit_e2_test.go): the provenance fixtures,
// the parameter matrix and the tail programs.
func OwnerGuardPrograms() map[string]string {
	out := map[string]string{}
	for i, src := range provenanceFixtures() {
		out[fmt.Sprintf("provenance fixture %02d", i)] = src
	}
	for _, tc := range inlineVariantCases() {
		var own []string
		for _, f := range tc.files {
			if f != unrelatedFile {
				own = append(own, f)
			}
		}
		if len(own) == 1 {
			out["inline variant: "+tc.name] = own[0]
		}
	}
	for _, p := range tailPrograms {
		out["tail: "+p.name] = p.src
	}
	return out
}

func lineCol(src string, off int) (int, int) {
	line := 1 + strings.Count(src[:off], "\n")
	return line, off - strings.LastIndex(src[:off], "\n")
}

func TestInlineVariantParameterDiagnostics(t *testing.T) {
	cases := inlineVariantCases()
	if len(cases) < 40 {
		t.Fatalf("the matrix has %d programs", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]*syntax.File, len(tc.files))
			for i, src := range tc.files {
				files[i] = parseOrFatal(t, src)
			}
			c := newChecker("main", Std, prelude().scope)
			var got []Diagnostic
			if ice := icePanic(func() { c.run(files); got = c.sorted() }); ice != "" {
				t.Fatalf("internal compiler error: %s", ice)
			}
			var want []Diagnostic
			for _, w := range tc.want {
				line, col := lineCol(tc.files[w.File], w.Offset)
				want = append(want, Diagnostic{Code: w.Code, Message: w.Message, File: w.File, Offset: w.Offset, Length: w.Length, Line: line, Column: col})
			}
			if fmt.Sprint(got) != fmt.Sprint(want) || len(got) != len(want) {
				t.Fatalf("diagnostics\n\tgot  %+v\n\twant %+v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("diagnostic %d\n\tgot  %+v\n\twant %+v", i, got[i], want[i])
				}
			}
			// The union's parameters keep their Defs; no made identifier
			// has one.
			var union *Named
			for _, tn := range c.pkg.Types {
				if tn.Name == "U" {
					union = tn.Type.(*Named)
				}
			}
			if union == nil {
				t.Fatal("no union U")
			}
			var declared []*syntax.Ident
			for _, f := range union.Obj.Decl.TypeParams.List {
				declared = append(declared, f.Names...)
			}
			if len(declared) != len(union.TParams) {
				t.Fatalf("U has %d parameters for %d names", len(union.TParams), len(declared))
			}
			for i, id := range declared {
				if c.pkg.Info.Defs[id] != Object(union.TParams[i]) {
					t.Errorf("Defs of U's parameter %s at %d is %v, not U's parameter %d", id.Name, id.Pos(), c.pkg.Info.Defs[id], i)
				}
			}
			for id := range c.synthetic {
				if _, ok := c.pkg.Info.Defs[id]; ok {
					t.Errorf("made identifier %s at %d has a Defs entry", id.Name, id.Pos())
				}
			}
		})
	}
}

// tailPrograms are the programs whose missing value reports at a brace or
// at the end of a branch, through errorIn: nested value if, match and
// catch, empty arms and else bodies, outer missing branches. Each wants
// its diagnostics alone, as file 0 and as file 1 beside an unrelated file
// (L:C+N CODE message, measured at 2ae616d8 before the whole-span bound).
const (
	tailFunc  = "this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"
	tailValue = "this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"
	tailArm   = "this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"
)

var tailPrograms = []struct{ name, src, want string }{
	{"if-empty", "func f(b bool) int { if b {} else {1} }\n", "1:28+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"if-value-empty", "func f(b bool) int { let x = if b {} else {1}; x }\n", "1:36+1 S107 this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"},
	{"if-value-no-else", "func f(b bool) int { let x = if b {1}; x }\n", "1:30+8 S108 a value if needs an else: without one it has no value (D-C2)"},
	{"if-nested-missing", "func f(b bool) int { if b { if b {1} } else {1} }\n", "1:38+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"match-empty", "func f(b bool) int { match b { case true: case false: 1 } }\n", "1:57+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"match-value-empty", "func f(b bool) int { let x = match b { case true: case false: 1 }; x }\n", "1:65+1 S107 this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"},
	{"catch-empty", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f() int { boom() catch { case Fault: } }\n", "3:41+1 S107 this arm yields no value, and the catch yields a value of type int (rows.md, Catch 3)"},
	{"catch-if", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b {1} } }\n", "3:49+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"catch-match", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: match b { case true: case false: 1 } } }\n", "3:84+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"catch-void", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc nop() {}\nfunc f() int { boom() catch { case Fault: nop() } }\n", "4:43+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"if-else-empty", "func f(b bool) int { if b {1} else {} }\n", "1:37+1 S107 " + tailFunc},
	{"if-value-else-empty", "func f(b bool) int { let x = if b {1} else {}; x }\n", "1:45+1 S107 " + tailValue},
	{"if-value-nested", "func f(b bool) int { let x = if b { if b {1} else {} } else {1}; x }\n", "1:52+1 S107 " + tailValue},
	{"match-nested-if", "func f(b bool) int { match b { case true: if b {1}; case false: 1 } }\n", "1:67+1 S107 " + tailFunc},
	{"match-value-nested-if", "func f(b bool) int { let x = match b { case true: if b {1} else {}; case false: 1 }; x }\n", "1:66+1 S107 " + tailValue},
	{"match-value-nested-match", "func f(b bool) int { let x = match b { case true: match b { case true: 1; case false: }; case false: 1 }; x }\n", "1:87+1 S107 " + tailValue},
	{"catch-match-empty-arm", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: match b { case true: 1; case false: } } }\n", "3:85+1 S107 " + tailArm},
	{"catch-if-else-empty", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b {1} else {} } }\n", "3:64+1 S107 " + tailArm},
	{"catch-nested-if", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b { if b {1} } else {1} } }\n", "3:65+1 S107 " + tailArm},
}

func TestErrorInSpan(t *testing.T) {
	file := parseOrFatal(t, "func f() int {1}\nfunc g() int {2}\n")
	c := newChecker("main", Std, prelude().scope)
	c.run([]*syntax.File{file})
	c.cur = c.files[0]
	body := file.Decls[0].(*syntax.FuncDecl).Body
	in := func(pos, end syntax.Pos) string {
		return icePanic(func() { c.errorIn(body, pos, end, CodeType, "span control") })
	}
	for _, ok := range []struct {
		name     string
		pos, end syntax.Pos
	}{
		{"whole", body.Pos(), body.End()},
		{"brace", body.Rbrace, body.Rbrace + 1},
		{"empty at the start", body.Pos(), body.Pos()},
		{"empty at the end", body.End(), body.End()},
	} {
		if ice := in(ok.pos, ok.end); ice != "" {
			t.Errorf("%s: %s", ok.name, ice)
		}
	}
	for _, bad := range []struct {
		name     string
		pos, end syntax.Pos
	}{
		{"end past the node", body.Rbrace, body.End() + 1},
		{"end far past the node", body.Rbrace, body.End() + 50},
		{"start before the node", body.Pos() - 1, body.Pos() + 1},
		{"start past the node", body.End() + 1, body.End() + 2},
		{"end before start", body.Rbrace, body.Rbrace - 1},
		{"no start", syntax.NoPos, body.End()},
		{"no end", body.Rbrace, syntax.NoPos},
	} {
		if ice := in(bad.pos, bad.end); !strings.Contains(ice, "outside its *syntax.BlockStmt") {
			t.Errorf("%s: got %q, want an internal compiler error", bad.name, ice)
		}
	}

	for _, p := range tailPrograms {
		for _, layout := range []int{-1, 0, 1} {
			files := []*syntax.File{parseOrFatal(t, p.src)}
			slot := 0
			if layout >= 0 {
				slot = layout
				files = []*syntax.File{parseOrFatal(t, unrelatedFile), parseOrFatal(t, unrelatedFile)}
				files[slot] = parseOrFatal(t, p.src)
			}
			var ds []Diagnostic
			if ice := icePanic(func() { ds, _ = checkFiles(files) }); ice != "" {
				t.Errorf("%s in layout %d: %s", p.name, layout, ice)
				continue
			}
			var got []string
			for _, d := range ds {
				if d.File != slot {
					t.Errorf("%s in layout %d: a diagnostic in file %d: %v", p.name, layout, d.File, d)
				}
				got = append(got, fmt.Sprintf("%d:%d+%d %s %s", d.Line, d.Column, d.Length, d.Code, d.Message))
			}
			if strings.Join(got, "\n") != p.want {
				t.Errorf("%s in layout %d:\n\tgot  %s\n\twant %s", p.name, layout, strings.Join(got, "\n\t     "), p.want)
			}
		}
	}
}

func checkFiles(files []*syntax.File) ([]Diagnostic, *Package) {
	pkg, ds := Check("main", files, nil)
	return ds, pkg
}
