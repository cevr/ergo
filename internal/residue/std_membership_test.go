package residue_test

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var writePrivileges = flag.Bool("write-privileges", false, "rewrite testdata/std-privileges.txt and std_privileges_baseline_test.go: drop the rows no key names and the admissions no row uses, sort the rest (it adds none)")

// TestCompilerReadsNoStdMembership holds the rule "the standard library
// gets no special treatment" (NORTH_STAR.md, Owner rules; owner,
// 2026-11-12) for the compiler's source: no checker rule, lowering,
// optimizer, cache or tool decides by a std name or by std membership,
// except the rows of testdata/std-privileges.txt.
//
// It reads the non-test Go files of stdScanDirs (but not a file ergo
// lowered from an Ergo program), which must hold every package that
// imports internal/sketch/check, and reports each key a
// function (a method as Recv.Name; a package-level var by its name) holds,
// as `file:function kind key`. Kinds:
//
//   - mark: a call of .Std() or .Trusted(); in the checker, a read of the
//     field std or trusted; a use of std.Package, std.Packages,
//     std.Prelude, std.PreludePath, std.Ledger or std.CoveredBy of
//     internal/sketch/stdlib. Host() is no mark: it tells an import go
//     package from an Ergo one, which every module reaches alike.
//   - flag: a read of a derived flag: a bool function, field or variable
//     whose value comes from a mark or a flag, computed to a fixed point
//     from the marks and declaredFlags. The key is the flag's name.
//   - name: a comparison (== or !=, a switch case, or strings.HasPrefix,
//     HasSuffix, TrimPrefix, TrimSuffix, Cut, CutPrefix, CutSuffix,
//     EqualFold) of a declaration's .Name or .Path, or of an extern's
//     Target.Value, with a string constant of its package that is a std
//     package path, a name std declares or a path of this module; and a
//     constant "P.Name" of a std package P compared with anything.
//   - built: a concatenation of a declaration's name with a string
//     constant passed to runtimeBody, runtimeCall, opBody or a function of
//     runtime/v1 or runtime/rt: a body name built by a convention. The key
//     is the constant.
//   - runtime: any selector of runtime/v1 or runtime/rt, outside the two
//     body-contract files. The key is the member.
//   - table: an entry of a map literal, or of a list literal that is a
//     package-level var or holds qualified names only, whose keys all name
//     something: "P.Name" (std's or Go's library), a runtime/v1 body or a
//     std package path; and, in any map literal, a key that is a body name.
//
// Each key is a row of testdata/std-privileges.txt,
// `category<TAB>file:function<TAB>kind<TAB>key<TAB>reason<TAB>unit`, one
// row per key, sorted (go test -run TestCompilerReadsNoStdMembership
// -write-privileges drops the stale rows and sorts; it adds none).
// Categories: seam (a file of docs/spec/stdlib.md's seam list: it finds,
// ships, caches or names std and decides nothing), language (a reader of a
// predeclared name on that section's list), go-boundary (a fact about a
// declaration of Go's standard library: the key names one, read from
// GOROOT, and goBoundaryKeys admits it), debt (anything else: the unit
// names who removes it). A key with no row fails; a row with no key fails
// (stale); a debt row outside stdPrivilegesBaseline fails, so the debt
// only shrinks; a go-boundary row outside goBoundaryKeys, or whose key
// Go's library does not declare, fails; an admission no row uses fails;
// a seam row outside the seam files, or a language row outside the
// readers list, fails.
//
// The rule reads source: a decision through a non-bool value derived from
// a mark, or a name passed through a parameter, passes it. The copy
// property (TestStdCopyAgrees in internal/sketch/std) is the proof; this
// rule is the early warning.
func TestCompilerReadsNoStdMembership(t *testing.T) {
	repo := specRoot(t)
	scan, err := stdPrivilegeScan(repo, stdScanDirs)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range scan.outside {
		t.Errorf("%s imports internal/sketch/check and is not under stdScanDirs: every package that reads the checker is scanned", d)
	}
	t.Logf("derived flags: %s", strings.Join(scan.flags, " "))
	file := filepath.Join(repo, stdPrivilegesFile)
	if *writePrivileges {
		if err := writeStdPrivileges(repo, scan.hits); err != nil {
			t.Fatal(err)
		}
	}
	rows, sorted := readStdPrivileges(t, file)
	if !sorted {
		t.Errorf("%s: the rows are out of order: run go test ./internal/residue -run '^TestCompilerReadsNoStdMembership$' -write-privileges", stdPrivilegesFile)
	}
	seam, readers := readStdOrdinary(t, repo)
	goLib := goLibrary(t)
	for _, p := range stdPrivilegeProblems(scan.hits, rows, seam, readers, stdPrivilegesBaseline, goBoundaryKeys, goLib) {
		t.Error(p)
	}
}

// stdScanDirs are the packages the rule reads, with their subpackages:
// every directory that imports internal/sketch/check, and the compiler's
// packages that do not.
var stdScanDirs = []string{
	"cmd/ergo",
	"docs/research/pattern-bench/gen",
	"internal/apigen",
	"internal/bindc",
	"internal/cligen",
	"internal/experimental/irspike",
	"internal/gobind",
	"internal/hostsyntax",
	"internal/linked",
	"internal/llmgen",
	"internal/mcpgen",
	"internal/rpcgen",
	"internal/sketch/api",
	"internal/sketch/build",
	"internal/sketch/check",
	"internal/sketch/derive",
	"internal/sketch/eval",
	"internal/sketch/exportgen",
	"internal/sketch/facts",
	"internal/sketch/fix",
	"internal/sketch/graphrows",
	"internal/sketch/lawfix",
	"internal/sketch/lint",
	"internal/sketch/lower",
	"internal/sketch/machinegraph",
	"internal/sketch/testplan",
	"internal/sketchfront",
	"internal/tsproj",
	"scripts/checkfloor",
	"scripts/ergotests",
	"scripts/examples",
	"scripts/stdlaws",
}

// ergoGenerated begins a Go file ergo lowered from an Ergo program (the
// linked tools under internal/linked): its runtime/v1 reads are the
// program's, as in every lowered program, not a decision of the compiler.
const ergoGenerated = "// Code generated by ergo from "

const (
	stdPrivilegesFile = "internal/residue/testdata/std-privileges.txt"
	stdBaselineFile   = "internal/residue/std_privileges_baseline_test.go"
	checkImportPath   = "ergo.local/prototype/internal/sketch/check"
	stdImportPath     = "ergo.local/prototype/internal/sketch/stdlib" // std's API (GS25)
	v1ImportPath      = "ergo.local/prototype/runtime/v1"
	rtImportPath      = "ergo.local/prototype/runtime/rt"
	// bodyContractFile reads the facts of the body an extern names, for
	// an extern of any package (check.BodyOf): its reads of runtime/v1
	// key on the declaration's target, not on std.
	bodyContractFile = "internal/sketch/check/body.go"
	// lowerContractFile is the lowering's read of the same facts
	// (lower/bodycontract.go): the calling policy marked on a body.
	lowerContractFile = "internal/sketch/lower/bodycontract.go"
)

var (
	stdMembers  = []string{"Package", "Packages", "Prelude", "PreludePath", "Ledger", "CoveredBy"}
	markCalls   = []string{"Std", "Trusted"}
	bodySinks   = []string{"runtimeBody", "runtimeCall", "opBody"}
	stringsTest = []string{"HasPrefix", "HasSuffix", "TrimPrefix", "TrimSuffix", "Cut", "CutPrefix", "CutSuffix", "EqualFold"}
	// qualifiedRE matches "P.Name" or "P.Type.Op": a std or a Go library
	// name.
	qualifiedRE = regexp.MustCompile(`^([a-z][a-z0-9]*(?:/[a-z0-9]+)*)\.([A-Z][A-Za-z0-9_]*)(\.[A-Za-z0-9_]+)?$`)
)

// declaredFlags are the flags whose tie to std the source does not show:
// each says "this package has no source in the unit", which only a package
// the compiler ships has (lower/canon.go, sources). The closure starts
// from them and the marks.
var declaredFlags = map[string]string{
	"internal/sketch/lower:isSourced": flagFunc,
	"internal/sketch/lower:sourced":   flagField,
	"internal/sketch/lower:stdLets":   flagField,
}

const (
	flagFunc  = "func"
	flagField = "field"
	flagVar   = "var"
)

// privKey is one key: `file:function`, its kind and the key.
type privKey struct{ fn, kind, key string }

// String is the key as the admission lists spell it.
func (k privKey) String() string { return k.fn + " " + k.kind + " " + strconv.Quote(k.key) }

// stdScanResult is what stdPrivilegeScan finds.
type stdScanResult struct {
	hits    map[privKey]bool
	flags   []string
	outside []string
}

// stdScanner holds the scan's facts.
type stdScanner struct {
	stdPaths, stdNames, bodies map[string]bool
	flags                      map[string]string
	hits                       map[privKey]bool
}

// stdPrivilegeScan scans the tracked non-test Go files under dirs of the
// checkout at repo, and lists the directories outside dirs that import
// the checker.
func stdPrivilegeScan(repo string, dirs []string) (stdScanResult, error) {
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		return stdScanResult{}, fmt.Errorf("git ls-files: %v", err)
	}
	s := &stdScanner{flags: map[string]string{}, hits: map[privKey]bool{}}
	if s.stdPaths, s.stdNames, err = stdPackagePaths(repo); err != nil {
		return stdScanResult{}, err
	}
	if s.bodies, err = runtimeBodyNames(repo); err != nil {
		return stdScanResult{}, err
	}
	fset := token.NewFileSet()
	byDir := map[string]map[string]*ast.File{}
	outside := map[string]bool{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || slices.Contains(strings.Split(name, "/"), "testdata") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			return stdScanResult{}, err
		}
		if strings.HasPrefix(string(src), ergoGenerated) {
			continue
		}
		if !slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(name, d+"/") }) {
			f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
			if err != nil {
				return stdScanResult{}, err
			}
			for _, p := range fileImports(f) {
				if p == checkImportPath {
					outside[path.Dir(name)] = true
				}
			}
			continue
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return stdScanResult{}, err
		}
		dir := path.Dir(name)
		if byDir[dir] == nil {
			byDir[dir] = map[string]*ast.File{}
		}
		byDir[dir][name] = f
	}
	s.deriveFlags(byDir)
	for dir, files := range byDir {
		consts := stringConsts(files)
		for name, f := range files {
			s.file(name, dir, consts, f)
		}
	}
	return stdScanResult{hits: s.hits, flags: sortedKeys(s.flags), outside: sortedKeys(outside)}, nil
}

// stdPackagePaths are the import paths of the std packages (the
// directories under internal/sketch/std that hold .ergo files, and the
// prelude), and the names std declares: each exported top-level
// declaration and each exported operation of a type's body.
func stdPackagePaths(repo string) (paths, names map[string]bool, err error) {
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "internal/sketch/std/*.ergo")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("git ls-files: %v", err)
	}
	paths, names = map[string]bool{"prelude": true}, map[string]bool{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.Contains(name, "/testdata/") {
			continue
		}
		if rel := strings.TrimPrefix(path.Dir(name), "internal/sketch/std"); rel != "" {
			paths[strings.TrimPrefix(rel, "/")] = true
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			return nil, nil, err
		}
		for _, m := range stdDeclRE.FindAllStringSubmatch(string(src), -1) {
			names[m[1]+m[2]] = true
		}
	}
	return paths, names, nil
}

// stdDeclRE matches the name of an exported declaration of an Ergo source:
// a top-level func (or method), type, let, context or extern, and an
// operation in a type's body.
var stdDeclRE = regexp.MustCompile(`(?m)^(?:extern go \S+ )?(?:func (?:\([^)]*\) )?|type |let |context )([A-Z][A-Za-z0-9_]*)|^\t([A-Z][A-Za-z0-9_]*)\(`)

// runtimeBodyNames are the names a runtime/v1 Go body is bound by: each
// exported function, and each exported method as Recv.Method.
func runtimeBodyNames(repo string) (map[string]bool, error) {
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "runtime/v1/*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v", err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || strings.Contains(strings.TrimPrefix(name, "runtime/v1/"), "/") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(repo, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !fd.Name.IsExported() {
				continue
			}
			if recv := methodRecv(fd); recv != "" {
				out[recv+"."+fd.Name.Name] = true
			} else {
				out[fd.Name.Name] = true
			}
		}
	}
	return out, nil
}

// methodRecv is the receiver type's name of a method, or "".
func methodRecv(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	typ := fd.Recv.List[0].Type
	if s, ok := typ.(*ast.StarExpr); ok {
		typ = s.X
	}
	if ix, ok := typ.(*ast.IndexExpr); ok {
		typ = ix.X
	}
	if ix, ok := typ.(*ast.IndexListExpr); ok {
		typ = ix.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// fileImports maps each import's local name in f to its path.
func fileImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, s := range f.Imports {
		p, _ := strconv.Unquote(s.Path.Value)
		name := path.Base(p)
		if s.Name != nil {
			name = s.Name.Name
		}
		out[name] = p
	}
	return out
}

// privFuncs calls visit for each function body of f (a method as
// Recv.Name) and each package-level var's value (by the var's name).
func privFuncs(f *ast.File, visit func(fn string, body ast.Node)) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			fn := d.Name.Name
			if r := methodRecv(d); r != "" {
				fn = r + "." + fn
			}
			visit(fn, d.Body)
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						visit(n.Name, vs.Values[i])
					}
				}
			}
		}
	}
}

// markOf is the mark x reads, or "".
func (s *stdScanner) markOf(dir string, imports map[string]string, x ast.Node) string {
	switch x := x.(type) {
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && len(x.Args) == 0 && slices.Contains(markCalls, sel.Sel.Name) {
			return sel.Sel.Name + "()"
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && imports[id.Name] == stdImportPath && slices.Contains(stdMembers, x.Sel.Name) {
			return "stdlib." + x.Sel.Name
		}
		if dir == "internal/sketch/check" && (x.Sel.Name == "std" || x.Sel.Name == "trusted") {
			return "." + x.Sel.Name
		}
	}
	return ""
}

// flagOf is the derived flag x reads, or "": a function or method (read
// by a call), a struct field (read by a selector) or a package-level
// variable (read by its name).
func (s *stdScanner) flagOf(dir string, imports map[string]string, x ast.Node) string {
	switch x := x.(type) {
	case *ast.CallExpr:
		switch f := x.Fun.(type) {
		case *ast.Ident:
			if s.flags[flagKey(dir, f.Name)] == flagFunc {
				return f.Name
			}
		case *ast.SelectorExpr:
			if id, ok := f.X.(*ast.Ident); ok && imports[id.Name] != "" && imports[id.Name] != checkImportPath {
				return ""
			}
			if s.flags[flagKey(dir, f.Sel.Name)] == flagFunc {
				return f.Sel.Name
			}
		}
	case *ast.Ident:
		if s.flags[flagKey(dir, x.Name)] == flagVar {
			return x.Name
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && imports[id.Name] != "" {
			return ""
		}
		if s.flags[flagKey(dir, x.Sel.Name)] == flagField {
			return x.Sel.Name
		}
	}
	return ""
}

// flagKey names a flag: an exported name across the scan, an unexported
// one in its directory.
func flagKey(dir, name string) string {
	if ast.IsExported(name) {
		return name
	}
	return dir + ":" + name
}

// deriveFlags computes the derived flags to a fixed point.
func (s *stdScanner) deriveFlags(byDir map[string]map[string]*ast.File) {
	// A field is read by its name alone, with no type, so only a name that
	// every struct of its scope declares bool can be a flag.
	fieldKinds := map[string]int{}
	type boolFunc struct {
		name, dir string
		imports   map[string]string
		body      *ast.BlockStmt
	}
	var boolFuncs []boolFunc
	for dir, files := range byDir {
		for _, f := range files {
			imports := fileImports(f)
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.StructType:
					for _, fl := range n.Fields.List {
						kind := 2
						if id, ok := fl.Type.(*ast.Ident); ok && id.Name == "bool" {
							kind = 1
						}
						for _, nm := range fl.Names {
							fieldKinds[flagKey(dir, nm.Name)] |= kind
						}
					}
				case *ast.FuncDecl:
					r := n.Type.Results
					if n.Body != nil && r != nil && len(r.List) == 1 && len(r.List[0].Names) <= 1 {
						if id, ok := r.List[0].Type.(*ast.Ident); ok && id.Name == "bool" && !slices.Contains(markCalls, n.Name.Name) {
							boolFuncs = append(boolFuncs, boolFunc{n.Name.Name, dir, imports, n.Body})
						}
					}
				}
				return true
			})
		}
	}
	for k, kind := range declaredFlags {
		s.flags[k] = kind
	}
	for changed := true; changed; {
		changed = false
		add := func(dir, n, kind string) {
			if slices.Contains(markCalls, n) || dir == "internal/sketch/check" && (n == "std" || n == "trusted") {
				return // a mark, not a flag
			}
			if n = flagKey(dir, n); s.flags[n] == "" {
				s.flags[n] = kind
				changed = true
			}
		}
		for _, bf := range boolFuncs {
			if s.flags[flagKey(bf.dir, bf.name)] != "" {
				continue
			}
			tainted := s.taintedLocals(bf.dir, bf.imports, bf.body)
			ast.Inspect(bf.body, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				if r, ok := n.(*ast.ReturnStmt); ok {
					for _, x := range r.Results {
						if s.derives(bf.dir, bf.imports, tainted, x) {
							add(bf.dir, bf.name, flagFunc)
						}
					}
				}
				return true
			})
		}
		for dir, files := range byDir {
			for _, f := range files {
				imports := fileImports(f)
				privFuncs(f, func(_ string, body ast.Node) {
					tainted := s.taintedLocals(dir, imports, body)
					ast.Inspect(body, func(n ast.Node) bool {
						switch n := n.(type) {
						case *ast.AssignStmt:
							if len(n.Lhs) != len(n.Rhs) {
								return true
							}
							for i, l := range n.Lhs {
								if sel, ok := l.(*ast.SelectorExpr); ok && fieldKinds[flagKey(dir, sel.Sel.Name)] == 1 && s.derives(dir, imports, tainted, n.Rhs[i]) {
									add(dir, sel.Sel.Name, flagField)
								}
							}
						case *ast.KeyValueExpr:
							if id, ok := n.Key.(*ast.Ident); ok && fieldKinds[flagKey(dir, id.Name)] == 1 && s.derives(dir, imports, tainted, n.Value) {
								add(dir, id.Name, flagField)
							}
						}
						return true
					})
				})
				for _, d := range f.Decls {
					gd, ok := d.(*ast.GenDecl)
					if !ok || gd.Tok != token.VAR {
						continue
					}
					for _, spec := range gd.Specs {
						vs := spec.(*ast.ValueSpec)
						for i, nm := range vs.Names {
							if i < len(vs.Values) && s.derives(dir, imports, nil, vs.Values[i]) {
								if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "bool" || vs.Type == nil && boolExpr(vs.Values[i]) {
									add(dir, nm.Name, flagVar)
								}
							}
						}
					}
				}
			}
		}
	}
}

// boolExpr reports an expression whose value is a bool by its form.
func boolExpr(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.ParenExpr:
		return boolExpr(x.X)
	case *ast.UnaryExpr:
		return x.Op == token.NOT
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND, token.LOR, token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
			return true
		}
	}
	return false
}

// taintedLocals are the locals of body assigned from a mark or a flag. A
// local any assignment of which is not derived is not tainted: a name
// such as ok is reused.
func (s *stdScanner) taintedLocals(dir string, imports map[string]string, body ast.Node) map[string]bool {
	out, clean := map[string]bool{}, map[string]bool{}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(n ast.Node) bool {
			a, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, l := range a.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok || clean[id.Name] {
					continue
				}
				if len(a.Lhs) == len(a.Rhs) && s.derives(dir, imports, out, a.Rhs[i]) {
					if !out[id.Name] {
						out[id.Name], changed = true, true
					}
				} else {
					clean[id.Name], changed = true, true
					delete(out, id.Name)
				}
			}
			return true
		})
	}
	return out
}

// derives reports that x reads a mark, a flag or a tainted local.
func (s *stdScanner) derives(dir string, imports map[string]string, tainted map[string]bool, x ast.Node) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if s.markOf(dir, imports, n) != "" || s.flagOf(dir, imports, n) != "" {
			found = true
		}
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// file adds the keys of f.
func (s *stdScanner) file(name, dir string, consts map[string]*ast.BasicLit, f *ast.File) {
	w := &privWalk{s: s, dir: dir, imports: fileImports(f), consts: consts, contract: name == bodyContractFile || name == lowerContractFile}
	privFuncs(f, func(fn string, body ast.Node) {
		w.fn, w.top = name+":"+fn, body
		ast.Inspect(body, w.visit)
	})
}

// privWalk scans one function.
type privWalk struct {
	s        *stdScanner
	dir      string
	imports  map[string]string
	consts   map[string]*ast.BasicLit
	contract bool
	fn       string
	// top is the function body, or a package-level var's value.
	top ast.Node
}

func (w *privWalk) hit(kind, key string) { w.s.hits[privKey{w.fn, kind, key}] = true }

func (w *privWalk) visit(n ast.Node) bool {
	if n == nil {
		return false
	}
	if m := w.s.markOf(w.dir, w.imports, n); m != "" {
		w.hit("mark", m)
	}
	if fl := w.s.flagOf(w.dir, w.imports, n); fl != "" {
		w.hit("flag", fl)
	}
	switch n := n.(type) {
	case *ast.SelectorExpr:
		if id, ok := n.X.(*ast.Ident); ok && !w.contract {
			if p := w.imports[id.Name]; p == v1ImportPath || p == rtImportPath {
				w.hit("runtime", path.Base(p)+"."+n.Sel.Name)
			}
		}
	case *ast.BinaryExpr:
		if n.Op == token.EQL || n.Op == token.NEQ {
			w.compare(n.X, n.Y)
			w.compare(n.Y, n.X)
		}
	case *ast.SwitchStmt:
		for _, c := range n.Body.List {
			for _, e := range c.(*ast.CaseClause).List {
				if n.Tag != nil {
					w.compare(e, n.Tag)
				} else if v, ok := w.str(e); ok && w.stdQualified(v) {
					w.hit("name", v)
				}
			}
		}
	case *ast.CallExpr:
		w.call(n)
	case *ast.CompositeLit:
		w.table(n)
	}
	return true
}

// compare reports c, a constant, compared with other.
func (w *privWalk) compare(c, other ast.Expr) {
	v, ok := w.str(c)
	if !ok {
		return
	}
	if w.stdQualified(v) || w.nameText(other) && w.keyConst(v) {
		w.hit("name", v)
	}
}

// call reports a prefix test of a name and a body name built from one.
func (w *privWalk) call(n *ast.CallExpr) {
	if name, ok := w.pkgFunc(n.Fun, "strings"); ok && slices.Contains(stringsTest, name) && len(n.Args) == 2 && w.nameText(n.Args[0]) {
		if v, ok := w.str(n.Args[1]); ok && w.keyConst(v) {
			w.hit("name", v)
		}
	}
	sink := false
	switch f := n.Fun.(type) {
	case *ast.Ident:
		sink = slices.Contains(bodySinks, f.Name)
	case *ast.SelectorExpr:
		sink = slices.Contains(bodySinks, f.Sel.Name)
		if id, ok := f.X.(*ast.Ident); ok && (w.imports[id.Name] == v1ImportPath || w.imports[id.Name] == rtImportPath) {
			sink = true
		}
	}
	if sink {
		for _, a := range n.Args {
			for _, k := range w.builtKeys(a) {
				w.hit("built", k)
			}
		}
	}
}

// pkgFunc is the name of x, a selector on the import of importPath.
func (w *privWalk) pkgFunc(x ast.Expr, importPath string) (string, bool) {
	sel, ok := x.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	return sel.Sel.Name, ok && w.imports[id.Name] == importPath
}

// str is the value of a string literal or of a string constant of the
// package.
func (w *privWalk) str(x ast.Expr) (string, bool) {
	if id, ok := x.(*ast.Ident); ok && w.consts[id.Name] != nil {
		x = w.consts[id.Name]
	}
	b, ok := x.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(b.Value)
	return v, err == nil
}

// keyConst reports a constant that names std or this module: a std
// package path (with a trailing slash for a prefix), a name std declares,
// or a path of the module.
func (w *privWalk) keyConst(v string) bool {
	return w.s.stdPaths[strings.TrimSuffix(v, "/")] || w.s.stdNames[v] || strings.HasPrefix(v, "ergo.local/")
}

// stdQualified reports "P.Name" (or "P.Type.Op") whose P is a std
// package path.
func (w *privWalk) stdQualified(v string) bool {
	m := qualifiedRE.FindStringSubmatch(v)
	return m != nil && w.s.stdPaths[m[1]]
}

// nameText reports a declaration's .Name or .Path (not of a call's
// result: reflect's Field(i).Name names a Go field), or an extern's
// Target.Value.
func (w *privWalk) nameText(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if inner, ok := sel.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Value" && inner.Sel.Name == "Target" {
		return true
	}
	return declNameSel(x)
}

// declNameSel reports x.Name or x.Path of a declaration (nameSel), not of
// a call's result.
func declNameSel(x ast.Expr) bool {
	s, ok := x.(*ast.SelectorExpr)
	if !ok || !nameSel(x) {
		return false
	}
	_, call := s.X.(*ast.CallExpr)
	return !call
}

// builtKeys are the constants of a body name built from a declaration's
// name: a concatenation that holds a name text and a constant.
func (w *privWalk) builtKeys(x ast.Expr) []string {
	b, ok := x.(*ast.BinaryExpr)
	if !ok || b.Op != token.ADD {
		return nil
	}
	var consts []string
	named := false
	var flat func(x ast.Expr)
	flat = func(x ast.Expr) {
		switch y := x.(type) {
		case *ast.ParenExpr:
			flat(y.X)
			return
		case *ast.BinaryExpr:
			if y.Op == token.ADD {
				flat(y.X)
				flat(y.Y)
				return
			}
		}
		if v, ok := w.str(x); ok {
			consts = append(consts, v)
		} else if w.nameText(x) {
			named = true
		}
	}
	flat(b)
	if !named {
		return nil
	}
	return consts
}

// table adds the entries of a literal table keyed by names.
func (w *privWalk) table(lit *ast.CompositeLit) {
	_, isMap := lit.Type.(*ast.MapType)
	if _, isList := lit.Type.(*ast.ArrayType); !isMap && !isList {
		return
	}
	var keys, bodies []string
	all, qualified := true, true
	for _, e := range lit.Elts {
		k := e
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			k = kv.Key
		}
		v, ok := w.str(k)
		if ok && w.s.bodies[v] {
			bodies = append(bodies, v)
		}
		if !ok || !(qualifiedRE.MatchString(v) || w.s.bodies[v] || w.s.stdPaths[v]) {
			all = false
			continue
		}
		qualified = qualified && qualifiedRE.MatchString(v)
		keys = append(keys, v)
	}
	if !all || len(keys) == 0 || !isMap && lit != w.top && (!qualified || len(keys) < 2) {
		// A list a function builds (an import list a generator writes) is
		// no table; a map keyed by a body name is one, whatever else it
		// holds.
		keys = nil
		if isMap {
			keys = bodies
		}
	}
	for _, k := range keys {
		w.hit("table", k)
	}
}

// stdPrivilegeRow is one row of testdata/std-privileges.txt.
type stdPrivilegeRow struct{ category, reason, unit string }

var stdPrivilegeCategories = []string{"seam", "language", "go-boundary", "debt"}

// readStdPrivileges reads the inventory, by key, and reports whether its
// rows are in order.
func readStdPrivileges(t *testing.T, file string) (map[privKey]stdPrivilegeRow, bool) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	rows, order, errs := parseStdPrivileges(string(data))
	for _, e := range errs {
		t.Errorf("%s:%s", file, e)
	}
	return rows, slices.IsSortedFunc(order, comparePrivKeys)
}

// parseStdPrivileges reads the rows of the inventory, in file order.
func parseStdPrivileges(text string) (rows map[privKey]stdPrivilegeRow, order []privKey, errs []string) {
	rows = map[privKey]stdPrivilegeRow{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		var key string
		ok := len(p) == 6 && slices.Contains(stdPrivilegeCategories, p[0]) && strings.Contains(p[1], ".go:") && p[2] != "" && strings.TrimSpace(p[4]) != "" && strings.TrimSpace(p[5]) != ""
		if ok {
			var err error
			key, err = strconv.Unquote(p[3])
			ok = err == nil
		}
		if !ok {
			errs = append(errs, fmt.Sprintf("%d: want `seam|language|go-boundary|debt<TAB>file:function<TAB>kind<TAB>\"key\"<TAB>reason<TAB>unit`, got %q", n, line))
			continue
		}
		k := privKey{p[1], p[2], key}
		if _, dup := rows[k]; dup {
			errs = append(errs, fmt.Sprintf("%d: %s is listed twice", n, k))
		}
		rows[k] = stdPrivilegeRow{category: p[0], reason: p[4], unit: p[5]}
		order = append(order, k)
	}
	return rows, order, errs
}

func comparePrivKeys(a, b privKey) int {
	return strings.Compare(a.fn+"\x00"+a.kind+"\x00"+a.key, b.fn+"\x00"+b.kind+"\x00"+b.key)
}

// formatStdPrivilege is a row's line.
func formatStdPrivilege(k privKey, r stdPrivilegeRow) string {
	return strings.Join([]string{r.category, k.fn, k.kind, strconv.Quote(k.key), r.reason, r.unit}, "\t")
}

// writeStdPrivileges rewrites the inventory and the admission lists: the
// rows the scan still finds, sorted, and the admissions a row still uses.
func writeStdPrivileges(repo string, hits map[privKey]bool) error {
	file := filepath.Join(repo, stdPrivilegesFile)
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	rows, _, errs := parseStdPrivileges(string(data))
	if len(errs) > 0 {
		return fmt.Errorf("%s: %s", stdPrivilegesFile, errs[0])
	}
	var head []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		head = append(head, line)
	}
	keys := make([]privKey, 0, len(rows))
	for k := range rows {
		if hits[k] {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, comparePrivKeys)
	lines := head
	var debt, boundary []string
	for _, k := range keys {
		r := rows[k]
		lines = append(lines, formatStdPrivilege(k, r))
		switch {
		case r.category == "debt" && slices.Contains(stdPrivilegesBaseline, k.String()):
			debt = append(debt, k.String())
		case r.category == "go-boundary" && slices.Contains(goBoundaryKeys, k.String()):
			boundary = append(boundary, k.String())
		}
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repo, stdBaselineFile), []byte(formatStdBaseline(debt, boundary)), 0o644)
}

// formatStdBaseline is the source of std_privileges_baseline_test.go.
func formatStdBaseline(debt, boundary []string) string {
	var b strings.Builder
	b.WriteString(`package residue_test

// Written by go test ./internal/residue -run '^TestCompilerReadsNoStdMembership$'
// -write-privileges, which only drops entries; an entry joins by an edit
// of this file, which review reads.

// stdPrivilegesBaseline are the debt keys of testdata/std-privileges.txt.
// A key may leave; none may join. The one-time admissions name their
// integration in the row's unit (B20-NP, B21-NP, B22-NP, B19-NP; PK-NP,
// the keys the per-key scan found when PRIVILEGE-KEYS landed).
var stdPrivilegesBaseline = []string{
`)
	for _, k := range debt {
		fmt.Fprintf(&b, "\t`%s`,\n", k)
	}
	b.WriteString(`}

// goBoundaryKeys are the go-boundary keys: each a fact about a declaration
// of Go's standard library, admitted here.
var goBoundaryKeys = []string{
`)
	for _, k := range boundary {
		fmt.Fprintf(&b, "\t`%s`,\n", k)
	}
	b.WriteString("}\n")
	return b.String()
}

// goLibrary reports a key that names a package of Go's standard library,
// or an exported declaration ("P.Name", or "P.Type.Op" of a type) of one,
// read from the toolchain's GOROOT.
func goLibrary(t *testing.T) func(string) bool {
	t.Helper()
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	root := filepath.Join(strings.TrimSpace(string(out)), "src")
	pkgs := map[string]map[string]bool{}
	return func(v string) bool {
		pkg, name := v, ""
		if m := qualifiedRE.FindStringSubmatch(v); m != nil {
			pkg, name = m[1], m[2]
		}
		names, ok := pkgs[pkg]
		if !ok {
			names = goPackageNames(filepath.Join(root, pkg))
			pkgs[pkg] = names
		}
		return names != nil && (name == "" || names[name])
	}
}

// goPackageNames are the exported top-level names of the Go package in
// dir, or nil when dir holds none.
func goPackageNames(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out map[string]bool
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					out[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						out[s.Name.Name] = s.Name.IsExported()
					case *ast.ValueSpec:
						for _, id := range s.Names {
							out[id.Name] = id.IsExported()
						}
					}
				}
			}
		}
	}
	return out
}

// stdOrdinaryHeading is the section of docs/spec/stdlib.md that lists the
// seam files and the readers of predeclared names.
const stdOrdinaryHeading = "## Std is ordinary"

// stdOrdinarySpan matches a code span of a Go file (a seam file) or of a
// Go file's function (a reader of a predeclared name).
var stdOrdinarySpan = regexp.MustCompile("`([a-z0-9_/]+\\.go)(?::([A-Za-z0-9_.]+))?`")

// readStdOrdinary reads the seam files and the readers of predeclared
// names from docs/spec/stdlib.md's section "Std is ordinary".
func readStdOrdinary(t *testing.T, repo string) (seam, readers map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, "docs/spec/stdlib.md"))
	if err != nil {
		t.Fatal(err)
	}
	seam, readers = parseStdOrdinary(string(data))
	if len(seam) == 0 {
		t.Fatalf("docs/spec/stdlib.md: no section %q with a seam list", stdOrdinaryHeading)
	}
	return seam, readers
}

// parseStdOrdinary reads the lists of the section "Std is ordinary".
func parseStdOrdinary(text string) (seam, readers map[string]bool) {
	seam, readers = map[string]bool{}, map[string]bool{}
	in := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(line) == stdOrdinaryHeading
			continue
		}
		if !in {
			continue
		}
		for _, m := range stdOrdinarySpan.FindAllStringSubmatch(line, -1) {
			if m[2] == "" {
				seam[m[1]] = true
			} else {
				readers[m[1]+":"+m[2]] = true
			}
		}
	}
	return seam, readers
}

// stdPrivilegeProblems compares the keys with the inventory, the seam
// list, the readers list and the admission lists. The keys a function
// holds with no row are one problem, so a function's unlisted keys read as
// one line.
func stdPrivilegeProblems(hits map[privKey]bool, rows map[privKey]stdPrivilegeRow, seam, readers map[string]bool, baseline, boundary []string, goLib func(string) bool) []string {
	var out []string
	missing := map[string][]string{}
	for k := range hits {
		if _, ok := rows[k]; !ok {
			missing[k.fn] = append(missing[k.fn], k.kind+" "+strconv.Quote(k.key))
		}
	}
	for _, fn := range sortedKeys(missing) {
		sort.Strings(missing[fn])
		out = append(out, fmt.Sprintf("%s decides by std membership or a std name (%s) and is not in testdata/std-privileges.txt: decide by a declaration any module can write (NORTH_STAR.md, std gets no special treatment)", fn, strings.Join(missing[fn], ", ")))
	}
	keys := make([]privKey, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, comparePrivKeys)
	usedSeam, usedReader, used := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, k := range keys {
		row := rows[k]
		if !hits[k] {
			out = append(out, fmt.Sprintf("testdata/std-privileges.txt lists %s (%s), which the function no longer holds: drop the row (-write-privileges drops it)", k, row.category))
		}
		file, _, _ := strings.Cut(k.fn, ":")
		switch row.category {
		case "seam":
			usedSeam[file] = true
			if !seam[file] {
				out = append(out, fmt.Sprintf("%s is a seam row, and %s is not on the seam list of docs/spec/stdlib.md (%s)", k, file, stdOrdinaryHeading))
			}
		case "language":
			usedReader[k.fn] = true
			if !readers[k.fn] {
				out = append(out, fmt.Sprintf("%s is a language row, and docs/spec/stdlib.md (%s) does not list it as a reader of a predeclared name", k, stdOrdinaryHeading))
			}
		case "debt":
			used[k.String()] = true
			if !slices.Contains(baseline, k.String()) {
				out = append(out, fmt.Sprintf("%s is a debt row stdPrivilegesBaseline does not hold: the debt may only shrink", k))
			}
		case "go-boundary":
			used[k.String()] = true
			if !goLib(k.key) {
				out = append(out, fmt.Sprintf("%s is a go-boundary row, and %q names no declaration of Go's standard library: a std name is a std key", k, k.key))
			}
			if !slices.Contains(boundary, k.String()) {
				out = append(out, fmt.Sprintf("%s is a go-boundary row goBoundaryKeys does not admit: a Go library key joins by an edit of %s, which review reads", k, stdBaselineFile))
			}
		}
	}
	for _, a := range append(slices.Clone(baseline), boundary...) {
		if !used[a] {
			out = append(out, fmt.Sprintf("%s admits %s, which no row of its category uses: drop it (-write-privileges drops it)", stdBaselineFile, a))
		}
	}
	for _, f := range sortedKeys(seam) {
		if !usedSeam[f] {
			out = append(out, fmt.Sprintf("docs/spec/stdlib.md (%s) lists the seam file %s, which no seam row names: drop it", stdOrdinaryHeading, f))
		}
	}
	for _, r := range sortedKeys(readers) {
		if !usedReader[r] {
			out = append(out, fmt.Sprintf("docs/spec/stdlib.md (%s) lists the reader %s, which no language row names: drop it", stdOrdinaryHeading, r))
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringConsts are the package-level constants whose value is a string
// literal, by name: a comparison with one (n.Obj.Pkg.Path == jsonPackage)
// keys on the literal it names, as a comparison with the literal does.
func stringConsts(files map[string]*ast.File) map[string]*ast.BasicLit {
	out := map[string]*ast.BasicLit{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if b, ok := vs.Values[i].(*ast.BasicLit); ok && b.Kind == token.STRING {
							out[n.Name] = b
						}
					}
				}
			}
		}
	}
	return out
}
