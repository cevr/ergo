package residue_test

import (
	"bufio"
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

// TestCompilerReadsNoStdMembership holds the rule "the standard library
// gets no special treatment" (NORTH_STAR.md, Owner rules; owner,
// 2026-11-12) for the compiler's source: no checker rule, lowering,
// optimizer, cache or tool decides by a std name or by std membership,
// except the rows of testdata/std-privileges.txt.
//
// It reads the non-test Go files of stdScanDirs and reports each function
// (a method as Recv.Name; a package-level var by its name) that:
//
//  1. calls .Std() or, in the checker, reads the field std (membership);
//  2. uses std.Package, std.Packages, std.Prelude, std.PreludePath,
//     std.Ledger or std.CoveredBy of internal/sketch/std;
//  3. compares a .Name or .Path selector with a string literal, or with a
//     string constant of its package, that is a std package path or a
//     name std declares (== or !=, or a switch case);
//  4. holds a string "P.Name", P a std package path, as a switch case or a
//     comparison operand, or keys a map literal by such strings only (a
//     table keyed by std names; a table of Go's library, strings.Split
//     beside bytes.Split, is the Go boundary);
//  5. calls v1.BodyOf, v1.StdRecord, v1.StructFields, v1.MovesOnly,
//     v1.StopsRunning, v1.MovingBody, v1.CallbackKeeper, v1.DrainsLends or
//     v1.Inert, outside bodyContractFile: the one file that reads a bound
//     body's facts by the body the declaration names (NP4b);
//  6. keys a map literal by the name of a runtime/v1 Go body (a table
//     keyed by a body name).
//
// Each such function must be a row `category<TAB>file:function<TAB>reason<TAB>unit`.
// Categories: seam (a file of docs/spec/stdlib.md's seam list: it finds,
// ships, caches or names std and decides nothing), language (a reader of a
// predeclared name on that section's list), debt (anything else: the unit
// names who removes it). A hit with no row fails; a row with no hit fails
// (stale); a debt row outside stdPrivilegesBaseline fails, so the debt
// only shrinks; a seam row outside the seam files, or a language row
// outside the readers list, fails.
//
// The rule reads source: a decision made through a flag derived from
// membership elsewhere passes it. The copy property (TestStdCopyAgrees in
// internal/sketch/std) is the proof; this rule is the early warning.
func TestCompilerReadsNoStdMembership(t *testing.T) {
	repo := specRoot(t)
	hits, err := stdPrivilegeHits(repo, stdScanDirs)
	if err != nil {
		t.Fatal(err)
	}
	rows := readStdPrivileges(t, filepath.Join(repo, "internal/residue/testdata/std-privileges.txt"))
	seam, readers := readStdOrdinary(t, repo)
	for _, p := range stdPrivilegeProblems(hits, rows, seam, readers, stdPrivilegesBaseline) {
		t.Error(p)
	}
}

// stdScanDirs are the compiler's packages the rule reads, with their
// subpackages. A directory that does not exist is skipped (the lint
// package lands on its own branch).
var stdScanDirs = []string{
	"internal/sketch/check", "internal/sketch/lower", "internal/sketch/eval",
	"internal/sketch/lint", "internal/sketch/derive", "internal/sketch/build",
	"internal/sketch/format", "internal/sketchfront", "internal/tsproj",
	"internal/rpcgen", "cmd/ergo",
}

// stdPrivilegeRow is one row of testdata/std-privileges.txt.
type stdPrivilegeRow struct{ category, reason, unit string }

// stdPrivilegeHits scans the tracked non-test Go files under dirs of the
// checkout at repo and returns, per `file:function`, the kinds of hit
// (1 to 6 of TestCompilerReadsNoStdMembership).
func stdPrivilegeHits(repo string, dirs []string) (map[string][]int, error) {
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--")
	for _, d := range dirs {
		ls.Args = append(ls.Args, d+"/*.go")
	}
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v", err)
	}
	stdPaths, stdNames, err := stdPackagePaths(repo)
	if err != nil {
		return nil, err
	}
	bodies, err := runtimeBodyNames(repo)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	byDir := map[string]map[string]*ast.File{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || slices.Contains(strings.Split(name, "/"), "testdata") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return nil, err
		}
		dir := path.Dir(name)
		if byDir[dir] == nil {
			byDir[dir] = map[string]*ast.File{}
		}
		byDir[dir][name] = f
	}
	out := map[string][]int{}
	for dir, files := range byDir {
		consts := stringConsts(files)
		for name, f := range files {
			s := stdScan{checker: dir == "internal/sketch/check", consts: consts, stdPaths: stdPaths, stdNames: stdNames, bodies: bodies, imports: fileImports(f)}
			for fn, kinds := range s.file(f) {
				if name == bodyContractFile || name == lowerContractFile {
					kinds = slices.DeleteFunc(kinds, func(k int) bool { return k == 5 })
					if len(kinds) == 0 {
						continue
					}
				}
				out[name+":"+fn] = kinds
			}
		}
	}
	return out, nil
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

const (
	stdImportPath = "ergo.local/prototype/internal/sketch/std"
	v1ImportPath  = "ergo.local/prototype/runtime/v1"
	// bodyContractFile reads the facts of the body an extern names, for
	// an extern of any package (check.BodyOf): its reads of runtime/v1's
	// fact tables (kind 5) key on the declaration's target, not on std.
	bodyContractFile = "internal/sketch/check/body.go"
	// lowerContractFile is the lowering's read of the same facts
	// (lower/bodycontract.go): the calling policy marked on a body.
	lowerContractFile = "internal/sketch/lower/bodycontract.go"
)

var (
	stdMembers = []string{"Package", "Packages", "Prelude", "PreludePath", "Ledger", "CoveredBy"}
	v1Facts    = []string{"BodyOf", "StdRecord", "StructFields", "MovesOnly", "StopsRunning", "MovingBody", "CallbackKeeper", "DrainsLends", "Inert"}
	stdNameRE  = regexp.MustCompile(`^([a-z][a-z0-9/]*)\.([A-Z][A-Za-z0-9_]*)(\.[A-Za-z0-9_]+)?$`)
)

// stdScan scans one file.
type stdScan struct {
	checker  bool
	consts   map[string]*ast.BasicLit
	stdPaths map[string]bool
	stdNames map[string]bool
	bodies   map[string]bool
	imports  map[string]string
}

// file returns the hit kinds per function of f.
func (s stdScan) file(f *ast.File) map[string][]int {
	out := map[string][]int{}
	add := func(fn string, kind int) {
		if !slices.Contains(out[fn], kind) {
			out[fn] = append(out[fn], kind)
			slices.Sort(out[fn])
		}
	}
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
			s.node(d.Body, func(k int) { add(fn, k) })
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						s.node(vs.Values[i], func(k int) { add(n.Name, k) })
					}
				}
			}
		}
	}
	return out
}

// node reports each hit kind under n.
func (s stdScan) node(n ast.Node, hit func(int)) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Std" && len(n.Args) == 0 {
					hit(1)
				}
				if s.pkgSel(sel, v1ImportPath) && slices.Contains(v1Facts, sel.Sel.Name) {
					hit(5)
				}
			}
		case *ast.SelectorExpr:
			if s.checker && n.Sel.Name == "std" {
				hit(1)
			}
			if s.pkgSel(n, stdImportPath) && slices.Contains(stdMembers, n.Sel.Name) {
				hit(2)
			}
		case *ast.BinaryExpr:
			if n.Op == token.EQL || n.Op == token.NEQ {
				if declNameSel(n.X) && s.lit(n.Y) || declNameSel(n.Y) && s.lit(n.X) {
					hit(3)
				}
				if s.stdName(n.X) || s.stdName(n.Y) {
					hit(4)
				}
			}
		case *ast.SwitchStmt:
			for _, c := range n.Body.List {
				for _, e := range c.(*ast.CaseClause).List {
					if n.Tag != nil && declNameSel(n.Tag) && s.lit(e) {
						hit(3)
					}
					if s.stdName(e) {
						hit(4)
					}
				}
			}
		case *ast.CompositeLit:
			if _, ok := n.Type.(*ast.MapType); !ok {
				return true
			}
			// A table of std names has only std names for keys: a table of
			// Go's library (strings.Split beside bytes.Split) is the Go
			// boundary, which every module's import go reads.
			keys, std := 0, 0
			for _, e := range n.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if _, ok := s.str(kv.Key); ok {
					keys++
				}
				if s.stdName(kv.Key) {
					std++
				}
				if v, ok := s.str(kv.Key); ok && s.bodies[v] {
					hit(6)
				}
			}
			if std > 0 && std == keys {
				hit(4)
			}
		}
		return true
	})
}

// declNameSel reports x.Name or x.Path of a declaration (nameSel), not of
// a call's result: reflect's Field(i).Name names a Go field.
func declNameSel(x ast.Expr) bool {
	s, ok := x.(*ast.SelectorExpr)
	if !ok || !nameSel(x) {
		return false
	}
	_, call := s.X.(*ast.CallExpr)
	return !call
}

// pkgSel reports a selector x.Name whose x is the import of path.
func (s stdScan) pkgSel(sel *ast.SelectorExpr, importPath string) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && s.imports[id.Name] == importPath
}

// str is the value of a string literal or of a string constant of the
// package.
func (s stdScan) str(x ast.Expr) (string, bool) {
	if id, ok := x.(*ast.Ident); ok && s.consts[id.Name] != nil {
		x = s.consts[id.Name]
	}
	b, ok := x.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(b.Value)
	return v, err == nil
}

// lit reports a string, or a string constant of the package, that is a
// std package path or a name std declares.
func (s stdScan) lit(x ast.Expr) bool {
	v, ok := s.str(x)
	return ok && (s.stdPaths[v] || s.stdNames[v])
}

// stdName reports a string "P.Name" (or "P.Type.Op") whose P is a std
// package path.
func (s stdScan) stdName(x ast.Expr) bool {
	v, ok := s.str(x)
	if !ok {
		return false
	}
	m := stdNameRE.FindStringSubmatch(v)
	return m != nil && s.stdPaths[m[1]]
}

// readStdPrivileges reads the inventory, by `file:function`.
func readStdPrivileges(t *testing.T, file string) map[string]stdPrivilegeRow {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]stdPrivilegeRow{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) != 4 || !slices.Contains([]string{"seam", "language", "debt"}, p[0]) || p[1] == "" || strings.TrimSpace(p[2]) == "" || strings.TrimSpace(p[3]) == "" {
			t.Errorf("%s:%d: want `seam|language|debt<TAB>file:function<TAB>reason<TAB>unit`, got %q", file, n, line)
			continue
		}
		if _, dup := out[p[1]]; dup {
			t.Errorf("%s:%d: %s is listed twice", file, n, p[1])
		}
		out[p[1]] = stdPrivilegeRow{category: p[0], reason: p[2], unit: p[3]}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
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

// stdPrivilegeProblems compares the hits with the inventory, the seam
// list, the readers list and the debt baseline.
func stdPrivilegeProblems(hits map[string][]int, rows map[string]stdPrivilegeRow, seam, readers map[string]bool, baseline []string) []string {
	var out []string
	for _, key := range sortedKeys(hits) {
		if _, ok := rows[key]; !ok {
			out = append(out, fmt.Sprintf("%s decides by std membership or a std name (kinds %v) and is not in testdata/std-privileges.txt: decide by a declaration any module can write (NORTH_STAR.md, std gets no special treatment)", key, hits[key]))
		}
	}
	usedSeam, usedReader := map[string]bool{}, map[string]bool{}
	for _, key := range sortedKeys(rows) {
		row := rows[key]
		if _, ok := hits[key]; !ok {
			out = append(out, fmt.Sprintf("testdata/std-privileges.txt lists %s (%s), which no longer reads std membership or a std name: drop the row", key, row.category))
		}
		file, _, _ := strings.Cut(key, ":")
		switch row.category {
		case "seam":
			usedSeam[file] = true
			if !seam[file] {
				out = append(out, fmt.Sprintf("%s is a seam row, and %s is not on the seam list of docs/spec/stdlib.md (%s)", key, file, stdOrdinaryHeading))
			}
		case "language":
			usedReader[key] = true
			if !readers[key] {
				out = append(out, fmt.Sprintf("%s is a language row, and docs/spec/stdlib.md (%s) does not list it as a reader of a predeclared name", key, stdOrdinaryHeading))
			}
		case "debt":
			if !slices.Contains(baseline, key) {
				out = append(out, fmt.Sprintf("%s is a debt row the inventory did not hold when the rule landed: the debt may only shrink", key))
			}
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

// stdPrivilegesBaseline are the debt rows of testdata/std-privileges.txt
// as the rule landed. A row may leave; none may join.
var stdPrivilegesBaseline = []string{
	"internal/sketch/check/authority.go:sealedStdOps",
	"internal/sketch/check/drop.go:dropWalk.drain",
	"internal/sketch/check/durable.go:TakesDurableMachine",
	"internal/sketch/check/durable.go:checker.durableStep",
	"internal/sketch/check/durable.go:checker.recordDurable",
	"internal/sketch/check/embed.go:checker.embedCall",
	"internal/sketch/check/expr.go:checker.compositeLit",
	"internal/sketch/check/ownership.go:isQueue",
	"internal/sketch/check/ownership.go:ownWalk.holdsTaken",
	"internal/sketch/check/script.go:scriptArg",
	"internal/sketch/check/withown.go:isOnExit",
	"internal/sketch/check/work.go:TakesWorkSend",
	"internal/sketch/check/work.go:checker.workLit",
	"internal/sketch/check/work.go:checker.workLitForm",
	"internal/sketch/check/work.go:isWorkType",
	"internal/sketch/eval/machine.go:interp.machineLaw",
	"internal/sketch/lower/blocking.go:lowerer.hostBlockingIntrinsic",
	"internal/sketch/lower/blocking.go:runtimeBindings",
	"internal/sketch/lower/calls.go:funcCtx.buildProvider",
	"internal/sketch/lower/calls.go:funcCtx.scopeCall",
	"internal/sketch/lower/calls.go:lowerer.hostImportNames",
	"internal/sketch/lower/calls.go:lowerer.stdContext",
	"internal/sketch/lower/context.go:contextNamespace",
	"internal/sketch/lower/embed.go:funcCtx.packageEmbed",
	"internal/sketch/lower/refown.go:funcCtx.ownedRefCall",
	"internal/sketch/lower/refown.go:lowerer.ownedFormOf",
	"internal/sketch/lower/release.go:funcCtx.closeBody",
	"internal/sketch/lower/release.go:funcCtx.holdTaken",
	"internal/sketch/lower/release.go:funcCtx.ownBinding",
	"internal/sketch/lower/release.go:funcCtx.stdOp",
	"internal/sketch/lower/std.go:funcCtx.stdCall",
	"internal/sketch/lower/std.go:lowerer.runtimeType",
	"internal/sketch/lower/std.go:lowerer.stdBody",
	"internal/sketch/lower/std.go:opBody",
	"internal/sketch/lower/std.go:runtimeProtocols",
	"internal/sketch/lower/types.go:lowerer.foreignOwner",
	"internal/sketch/lower/values.go:lowerer.ownedForm",
	"internal/sketch/lower/wirefields.go:jsonWrapper",
	"internal/sketchfront/inspect.go:InspectGo",
	"internal/sketchfront/inspect.go:ergoBody",
	"internal/sketchfront/rewrite.go:FreshStdLedger",
	"internal/tsproj/http.go:httpFailureStatuses",
	"internal/tsproj/http.go:httpTables",
	"internal/tsproj/project.go:stdJSON",
}
