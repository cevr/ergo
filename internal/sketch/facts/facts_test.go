package facts_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval/evalgen"
	"ergo.local/prototype/internal/sketch/facts"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// TestTablesAgreeWithScan is SEM0's acceptance: for generated programs,
// the fact tables give every symbol exactly the sites a plain scan of the
// checker's maps gives it (Defs, Uses, Calls, Instances, Patterns, and the
// types of selected and keyed records), with the same kinds; every handle
// names one symbol, and the handle of a symbol another package declares
// names the same symbol, of the same kind, in that package's table; the
// build visits each node once and appends one row per name it resolves; a
// references read costs the symbol's rows plus a logarithm per table; and
// the tables encode to the same bytes at one thread and at the test's and
// decode back.
//
// A program is a library package and a root package. The root holds an
// evalgen program (every value and control form that generator reaches)
// and the planted controls: a second package named lib imported under an
// alias, a root function with a library function's name, a local that
// shadows it, a type alias of a library record, a library union named
// like one of the root's, fields read, written, keyed and matched, an
// operation type with a member law whose operations two providers
// implement with functions of one overloaded name, two contexts, a catch
// arm, and fails rows.
//
// ERGO_FACTS_SEED=N replays one seed; a failure prints the sources, both
// lists and that command.
func TestTablesAgreeWithScan(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if testing.Short() {
		// The fast tier runs one seed: its CPU budget has no room for
		// more (scripts/gate/tier_test.go, fastCPUKnownGap).
		seeds = seeds[:1]
	}
	if s := os.Getenv("ERGO_FACTS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		files := generate(seed)
		dir := t.TempDir()
		for name, text := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		fail := func(format string, args ...any) {
			t.Helper()
			var b strings.Builder
			names := mapKeys(files)
			slices.Sort(names)
			for _, name := range names {
				fmt.Fprintf(&b, "--- %s\n%s\n", name, files[name])
			}
			t.Fatalf("seed %d: %s\n%s\nreplay: ERGO_FACTS_SEED=%d go test -run TestTablesAgreeWithScan ./internal/sketch/facts",
				seed, fmt.Sprintf(format, args...), b.String(), seed)
		}
		agree(t, dir, fail)
	}
}

// TestTablesAgreeWithScanOnExamples holds the tables to the scan on every
// example program (full tier: the examples are 1.5 MB of source).
func TestTablesAgreeWithScanOnExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier")
	}
	root := filepath.Join("..", "..", "..", "examples")
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "cmd", "*"))
	if len(dirs) == 0 {
		t.Fatal("no example programs")
	}
	for _, d := range dirs {
		if src, _ := filepath.Glob(filepath.Join(d, "*.ergo")); len(src) == 0 {
			continue // a Go command of an example (lintpack's policy-lint)
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			t.Fatal(err)
		}
		agree(t, abs, func(format string, args ...any) {
			t.Helper()
			t.Fatalf("%s: %s", d, fmt.Sprintf(format, args...))
		})
	}
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	var out []K
	for k := range m {
		out = append(out, k)
	}
	return out
}

// agree checks the program of dir and holds its tables to the scan.
func agree(t *testing.T, dir string, fail func(string, ...any)) {
	t.Helper()
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		fail("load: %v", err)
	}
	if len(a.Diagnostics) > 0 {
		fail("diagnostics: %v", a.Diagnostics)
	}
	var tables []*facts.Table
	var pkgs []sketchfront.CheckedPackage
	for _, p := range a.Packages() {
		var files []facts.File
		nodes := 0
		for _, f := range p.Files {
			files = append(files, facts.File{Name: f.Rel, Syntax: f.Syntax})
			for _, d := range f.Syntax.Decls {
				if facts.Written(d) {
					syntax.Inspect(d, func(syntax.Node) bool { nodes++; return true })
				}
			}
		}
		tab, n := facts.Build(p.Package, files)
		if n.Nodes != nodes {
			fail("%s: the build visited %d nodes; the files have %d", p.Path, n.Nodes, nodes)
		}
		if n.Rows != n.Names {
			fail("%s: %d rows for %d resolved names", p.Path, n.Rows, n.Names)
		}
		if len(tab.Sites) != n.Rows {
			fail("%s: %d sites, %d rows appended", p.Path, len(tab.Sites), n.Rows)
		}
		if err := sameAsScan(tab, scan(p.Package, files)); err != "" {
			fail("%s: %s", p.Path, err)
		}
		tables = append(tables, tab)
		pkgs = append(pkgs, p)
	}
	handlesAgree(tables, fail)
	readsAreBounded(tables, fail)
	encodesAlike(dir, tables, fail)
}

// A ref is one name the scan resolved.
type ref struct {
	file uint16
	off  uint32
	kind facts.SiteKind
	flag uint8
}

func (r ref) String() string { return fmt.Sprintf("%d:%d %s/%d", r.file, r.off, r.kind, r.flag) }

// sameAsScan compares the table's partition of the sites into symbols, and
// each site's kind and flag, with the scan's.
func sameAsScan(tab *facts.Table, want map[any][]ref) string {
	type pos struct {
		file uint16
		off  uint32
	}
	keyAt := map[pos]any{}
	n := 0
	for k, rs := range want {
		for _, r := range rs {
			if old, dup := keyAt[pos{r.file, r.off}]; dup {
				return fmt.Sprintf("the scan resolves %d:%d twice (%v, %v)", r.file, r.off, old, k)
			}
			keyAt[pos{r.file, r.off}] = k
			n++
		}
	}
	if n != len(tab.Sites) {
		var missing []string
		have := map[pos]bool{}
		for _, s := range tab.Sites {
			have[pos{s.File, s.Off}] = true
		}
		for p, k := range keyAt {
			if !have[p] {
				missing = append(missing, fmt.Sprintf("%d:%d %v", p.file, p.off, k))
			}
		}
		slices.Sort(missing)
		return fmt.Sprintf("the scan resolves %d names, the table has %d sites; not in the table: %v", n, len(tab.Sites), missing)
	}
	for id := range tab.Symbols {
		rows := tab.Refs(uint32(id))
		if len(rows) == 0 {
			continue
		}
		k, ok := keyAt[pos{rows[0].File, rows[0].Off}]
		if !ok {
			return fmt.Sprintf("symbol %q: site %d:%d is not in the scan", tab.Symbols[id].Handle, rows[0].File, rows[0].Off)
		}
		var got []ref
		for _, s := range rows {
			got = append(got, ref{s.File, s.Off, s.Kind, s.Flag})
		}
		exp := slices.Clone(want[k])
		slices.SortFunc(exp, func(a, b ref) int {
			if a.file != b.file {
				return int(a.file) - int(b.file)
			}
			return int(a.off) - int(b.off)
		})
		if !slices.Equal(got, exp) {
			return fmt.Sprintf("symbol %q (%s %s):\n table %v\n scan  %v", tab.Symbols[id].Handle, tab.Symbols[id].Kind, tab.Symbols[id].Name, got, exp)
		}
	}
	return ""
}

// handlesAgree: within a table a handle names one symbol; a declared
// symbol's handle starts with its package's path; the handle of a symbol
// declared in another package of the program names a declared symbol of
// the same kind in that package's table.
func handlesAgree(tables []*facts.Table, fail func(string, ...any)) {
	byPath := map[string]*facts.Table{}
	for _, t := range tables {
		byPath[t.Path] = t
	}
	for _, t := range tables {
		// The operation edges come from the checker's maps; the table
		// holds them in the order of their ends' handles.
		for i := 1; i < len(t.Ops); i++ {
			a, b := t.Ops[i-1], t.Ops[i]
			ka := t.Symbols[a.From].Handle + "\x00" + t.Symbols[a.To].Handle
			kb := t.Symbols[b.From].Handle + "\x00" + t.Symbols[b.To].Handle
			if ka >= kb {
				fail("%s: the operation edges %d and %d are out of handle order", t.Path, i-1, i)
			}
		}
		// A local has a handle only when no other local of its declaration
		// has its name.
		type declName struct {
			encl uint32
			name string
		}
		locals := map[declName]int{}
		enclOf := func(id int) uint32 {
			for _, s := range t.Refs(uint32(id)) {
				if s.Kind == facts.Def {
					return s.Encl
				}
			}
			return facts.NoSym
		}
		for id, s := range t.Symbols {
			if s.Kind == facts.Local {
				locals[declName{enclOf(id), s.Name}]++
			}
		}
		for id, s := range t.Symbols {
			if s.Kind == facts.Local && s.Handle != "" && locals[declName{enclOf(id), s.Name}] > 1 {
				fail("%s: the local %q has a handle, but its declaration binds the name more than once", t.Path, s.Handle)
			}
		}
		seen := map[string]bool{}
		for id, s := range t.Symbols {
			if s.Handle == "" {
				if s.Kind != facts.Local && s.Kind != facts.Field && s.Kind != facts.Operation {
					fail("%s: a %s %q has no handle", t.Path, s.Kind, s.Name)
				}
				continue
			}
			if seen[s.Handle] {
				fail("%s: two symbols have the handle %q", t.Path, s.Handle)
			}
			seen[s.Handle] = true
			if got, _, ok := t.Lookup(s.Handle); !ok || got != uint32(id) {
				fail("%s: Lookup(%q) = %d, %v; want %d", t.Path, s.Handle, got, ok, id)
			}
			if s.Declared && s.Kind != facts.Package && !strings.HasPrefix(s.Handle, t.Path+".") {
				fail("%s: the declared %s %q has the handle %q", t.Path, s.Kind, s.Name, s.Handle)
			}
			if s.Declared || s.Kind == facts.Package {
				continue
			}
			for path, owner := range byPath {
				if owner == t || !strings.HasPrefix(s.Handle, path+".") || strings.Contains(s.Handle[len(path)+1:], "/") {
					continue
				}
				oid, _, ok := owner.Lookup(s.Handle)
				if !ok || !owner.Symbols[oid].Declared {
					fail("%s names %q, which %s does not declare", t.Path, s.Handle, path)
				}
				if o := owner.Symbols[oid]; o.Kind != s.Kind {
					fail("%s names %q as a %s; %s declares a %s", t.Path, s.Handle, s.Kind, path, o.Kind)
				}
			}
		}
	}
}

// readsAreBounded: References of each handle gives the symbol's rows in
// every table and reads no more than those rows plus a logarithm of each
// table's handle index.
func readsAreBounded(tables []*facts.Table, fail func(string, ...any)) {
	bound := 0
	for _, t := range tables {
		bound += int(math.Ceil(math.Log2(float64(len(t.Handles)+1)))) + 1
	}
	for _, t := range tables {
		for _, id := range t.Handles {
			h := t.Symbols[id].Handle
			refs, read := facts.References(tables, h)
			want := 0
			for _, u := range tables {
				if uid, _, ok := u.Lookup(h); ok {
					want += len(u.Refs(uid))
				}
			}
			if len(refs) != want {
				fail("References(%q) gave %d sites; the tables hold %d", h, len(refs), want)
			}
			if read > len(refs)+bound {
				fail("References(%q) read %d for %d sites (bound %d)", h, read, len(refs), len(refs)+bound)
			}
		}
	}
}

// encodesAlike: the tables encode to the same bytes when the program is
// checked again at one thread, built again from that check, and decode to
// equal tables.
func encodesAlike(dir string, tables []*facts.Table, fail func(string, ...any)) {
	var first [][]byte
	for _, t := range tables {
		b := facts.Encode(t)
		d, err := facts.Decode(b)
		if err != nil {
			fail("%s: decode: %v", t.Path, err)
		}
		if !reflect.DeepEqual(d, t) {
			fail("%s: the decoded table differs", t.Path)
		}
		first = append(first, b)
	}
	// The first check ran at the test's GOMAXPROCS (four in the gate);
	// check again at one, and build each table three times from that
	// check, so a map's order cannot hide behind one lucky build.
	old := runtime.GOMAXPROCS(1)
	a, err := sketchfront.CheckDir(dir)
	runtime.GOMAXPROCS(old)
	if err != nil {
		fail("load: %v", err)
	}
	for i, p := range a.Packages() {
		var files []facts.File
		for _, f := range p.Files {
			files = append(files, facts.File{Name: f.Rel, Syntax: f.Syntax})
		}
		for range 3 {
			t, _ := facts.Build(p.Package, files)
			if b := facts.Encode(t); string(b) != string(first[i]) {
				fail("%s: the table's bytes differ at GOMAXPROCS=1 from GOMAXPROCS=%d", p.Path, old)
			}
		}
	}
}

// ---------------------------------------------------------------- the scan

// scan is the reference: every name the checker resolved, read from its
// maps, grouped by the object it names, with its kind. It reads the tree
// only to find each name's file and parent.
func scan(pkg *check.Package, files []facts.File) map[any][]ref {
	info := pkg.Info
	out := map[any][]ref{}
	fileOf := map[*syntax.Ident]uint16{}
	parent := map[syntax.Node]syntax.Node{}
	var specs []struct {
		file uint16
		spec *syntax.ImportSpec
	}
	var tests []struct {
		file uint16
		decl *syntax.TestDecl
	}
	for fi, f := range files {
		for _, d := range f.Syntax.Decls {
			if !facts.Written(d) {
				continue // a derived declaration: no name of it is in the text
			}
			syntax.Inspect(d, func(n syntax.Node) bool {
				// The children of n are the nodes an inspection of n meets
				// first below it.
				syntax.Inspect(n, func(c syntax.Node) bool {
					if c == n {
						return true
					}
					parent[c] = n
					return false
				})
				switch x := n.(type) {
				case *syntax.Ident:
					fileOf[x] = uint16(fi)
				case *syntax.ImportSpec:
					specs = append(specs, struct {
						file uint16
						spec *syntax.ImportSpec
					}{uint16(fi), x})
				case *syntax.TestDecl:
					tests = append(tests, struct {
						file uint16
						decl *syntax.TestDecl
					}{uint16(fi), x})
				}
				return true
			})
		}
	}
	add := func(k any, id *syntax.Ident, kind facts.SiteKind) {
		out[k] = append(out[k], ref{fileOf[id], uint32(id.NamePos), kind, instFlag(info, parent, id)})
	}
	for id, o := range info.Defs {
		if _, written := fileOf[id]; !written {
			continue
		}
		if k, ok := objKey(info, o, id, parent); ok {
			add(k, id, facts.Def)
		}
	}
	for id, o := range info.Uses {
		if _, written := fileOf[id]; !written || info.Defs[id] != nil {
			continue
		}
		if k, ok := objKey(info, o, id, parent); ok {
			add(k, id, kindOf(info, parent, id, o))
		}
	}
	// Names the maps do not hold: selected and keyed members, test names,
	// import specs.
	for id := range fileOf {
		if info.Defs[id] != nil || info.Uses[id] != nil {
			continue
		}
		switch p := parent[id].(type) {
		case *syntax.SelectorExpr:
			if p.Sel != id {
				continue
			}
			if q, ok := p.X.(*syntax.Ident); ok {
				if pn, ok := info.Uses[q].(*check.PkgName); ok {
					if o := qualifiedObj(info, parent, pn, p); o != nil {
						if k, ok := objKey(info, o, id, parent); ok {
							add(k, id, kindOf(info, parent, id, o))
						}
					}
					continue
				}
			}
			if k, ok := selected(info, parent, p); ok {
				add(k, id, kindOf(info, parent, id, nil))
			}
		case *syntax.KeyValueExpr:
			lit, _ := parent[p].(*syntax.CompositeLit)
			if lit == nil || p.Key != id {
				continue
			}
			kind, t := facts.Construct, info.Types[lit]
			if pt := info.Patterns[lit]; pt != nil {
				kind = facts.Match
				if pt.Type != nil {
					t = pt.Type
				}
			}
			if f := fieldOf(t, id.Name); f != nil {
				add(f.Origin(), id, kind)
			}
		case *syntax.ImportSpec:
			if p.Name == id {
				add("pkg:"+strings.Trim(p.Path.Value, "\"`"), id, facts.Import)
			}
		}
	}
	for _, s := range specs {
		if s.spec.Name == nil {
			k := "pkg:" + strings.Trim(s.spec.Path.Value, "\"`")
			out[k] = append(out[k], ref{s.file, uint32(s.spec.Path.ValuePos), facts.Import, 0})
		}
	}
	for _, d := range tests {
		k := "test:" + d.decl.Name.Name
		out[k] = append(out[k], ref{d.file, uint32(d.decl.Name.NamePos), facts.Def, 0})
	}
	return out
}

type methodKey struct {
	owner *check.TypeName
	name  string
}

// objKey is the identity of the object o that the name id resolves to.
func objKey(info *check.Info, o check.Object, id *syntax.Ident, parent map[syntax.Node]syntax.Node) (any, bool) {
	switch o := o.(type) {
	case nil, *check.Builtin, *check.Const:
		return nil, false
	case *check.Field:
		return o.Origin(), true
	case *check.Method:
		if sel, ok := parent[id].(*syntax.SelectorExpr); ok {
			if n := namedOf(info.Types[sel.X]); n != nil {
				return methodKey{n.Obj, o.Name}, true
			}
			// An operation called through a type parameter is its
			// constraint's (vt's Apply[S Screen](s mut S): s.Print is
			// Screen.Print), as the table keys it by the declaring method.
			if n := constraintOf(info.Types[sel.X]); n != nil {
				return methodKey{n.Obj, o.Name}, true
			}
		}
		if owner := enclosingType(info, parent, id); owner != nil {
			return methodKey{owner, o.Name}, true
		}
		return o, true
	case *check.PkgName:
		if o.Pkg == nil {
			return nil, false
		}
		return "pkg:" + o.Pkg.Path, true
	case *check.TypeName:
		if tp, ok := o.Type.(*check.TypeParam); ok {
			return tp, true
		}
		return o, true
	case *check.Func, *check.Var, *check.Law, *check.Context, *check.TypeParam:
		return o, true
	}
	if n := namedOf(info.Types[id]); n != nil && n.Obj != nil {
		return n.Obj, true
	}
	return nil, false
}

// qualifiedObj is the member of the package pn that sel names: a context
// when sel is a with's operand or an embedded context, else the exported
// member of that name.
func qualifiedObj(info *check.Info, parent map[syntax.Node]syntax.Node, pn *check.PkgName, sel *syntax.SelectorExpr) check.Object {
	if pn.Pkg == nil {
		return nil
	}
	ctx := false
	switch p := parent[sel].(type) {
	case *syntax.CallExpr:
		ctx = info.Withs[p] != nil && slices.Contains(p.Args, syntax.Expr(sel))
	case *syntax.ContextEntry:
		ctx = p.Kind == syntax.ContextEmbed && p.Key == syntax.Expr(sel)
	}
	if ctx {
		if c := pn.Pkg.Contexts[sel.Sel.Name]; c != nil {
			return c
		}
		return nil
	}
	for _, o := range pn.Pkg.Exported() {
		var name string
		switch o := o.(type) {
		case *check.TypeName:
			name = o.Name
		case *check.Func:
			name = o.Name
		case *check.Var:
			name = o.Name
		case *check.Context:
			name = o.Name
		}
		if name == sel.Sel.Name {
			return o
		}
	}
	return nil
}

// enclosingType is the type declaration that holds id.
func enclosingType(info *check.Info, parent map[syntax.Node]syntax.Node, id *syntax.Ident) *check.TypeName {
	for n := parent[id]; n != nil; n = parent[n] {
		if d, ok := n.(*syntax.TypeDecl); ok {
			tn, _ := info.Defs[d.Name].(*check.TypeName)
			return tn
		}
	}
	return nil
}

// selected is the identity of the member a selector names that Uses does
// not hold: the call's operation or function, or a field or operation of
// the operand's type.
func selected(info *check.Info, parent map[syntax.Node]syntax.Node, sel *syntax.SelectorExpr) (any, bool) {
	top := syntax.Node(sel)
	for {
		p := parent[top]
		if pe, ok := p.(*syntax.ParenExpr); ok {
			top = pe
			continue
		}
		if ix, ok := p.(*syntax.IndexExpr); ok && ix.X == top {
			top = ix
			continue
		}
		break
	}
	if call, ok := parent[top].(*syntax.CallExpr); ok && call.Fun == top {
		if c := info.Calls[call]; c != nil {
			switch {
			case c.Op != nil:
				if n := namedOf(info.Types[sel.X]); n != nil {
					return methodKey{n.Obj, c.Op.Name}, true
				}
				// A receiver of a type parameter: the operation is its
				// constraint's (vt's Apply[S Screen] calls s.Print).
				if tp, ok := info.Types[sel.X].(*check.TypeParam); ok {
					if n := namedOf(tp.Constraint); n != nil {
						return methodKey{n.Obj, c.Op.Name}, true
					}
				}
				return c.Op, true
			case c.Func != nil && c.Recv != nil:
				return c.Func, true
			}
		}
	}
	t := info.Types[sel.X]
	if f := fieldOf(t, sel.Sel.Name); f != nil {
		return f.Origin(), true
	}
	if n := namedOf(t); n != nil {
		if i, ok := n.Underlying().(*check.Interface); ok {
			for _, m := range i.Methods {
				if m.Name == sel.Sel.Name {
					return methodKey{n.Obj, m.Name}, true
				}
			}
		}
	}
	return nil, false
}

func namedOf(t check.Type) *check.Named {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, _ := t.(*check.Named)
	if n != nil && n.Orig != nil {
		n = n.Orig
	}
	return n
}

// constraintOf is the operation type that constrains the type parameter t
// (through a pointer), or nil.
func constraintOf(t check.Type) *check.Named {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	if tp, ok := t.(*check.TypeParam); ok {
		return namedOf(tp.Constraint)
	}
	return nil
}

func fieldOf(t check.Type, name string) *check.Field {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	s, _ := t.(*check.Struct)
	if s == nil {
		return nil
	}
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// naming climbs from a name to the expression that names a thing: through
// parentheses, a selector's member, and the operand of an index (a type
// argument list).
func naming(parent map[syntax.Node]syntax.Node, id *syntax.Ident) []syntax.Node {
	chain := []syntax.Node{id}
	for {
		cur := chain[len(chain)-1]
		switch p := parent[cur].(type) {
		case *syntax.ParenExpr, *syntax.StarExpr:
			chain = append(chain, p)
			continue
		case *syntax.SelectorExpr:
			if p.Sel == cur {
				chain = append(chain, p)
				continue
			}
		case *syntax.IndexExpr:
			if p.X == cur {
				chain = append(chain, p)
				continue
			}
		}
		return chain
	}
}

// kindOf is the kind of the use id: from the nearest parent of its naming
// chain that gives one, else Match when an expression of the chain is a
// pattern, else a type use or a read.
func kindOf(info *check.Info, parent map[syntax.Node]syntax.Node, id *syntax.Ident, o check.Object) facts.SiteKind {
	if written(parent, id) {
		return facts.Write
	}
	chain := naming(parent, id)
	for i := len(chain) - 1; i >= 0; i-- {
		e := chain[i]
		if k := parentKind(info, parent, e); k != 0 {
			return k
		}
		if ex, ok := e.(syntax.Expr); ok && info.Patterns[ex] != nil {
			return facts.Match
		}
	}
	switch o.(type) {
	case *check.TypeName, *check.TypeParam:
		return facts.TypeUse
	}
	return facts.Read
}

// written reports a name in the place an assignment writes: the place
// itself, the member it selects, or any name of its operand chain.
func written(parent map[syntax.Node]syntax.Node, id *syntax.Ident) bool {
	var place func(e syntax.Expr) bool
	place = func(e syntax.Expr) bool {
		switch x := e.(type) {
		case *syntax.Ident:
			return x == id
		case *syntax.SelectorExpr:
			return x.Sel == id || place(x.X)
		case *syntax.IndexExpr:
			return place(x.X)
		case *syntax.ParenExpr:
			return place(x.X)
		case *syntax.StarExpr:
			return place(x.X)
		}
		return false
	}
	for n := parent[id]; n != nil; n = parent[n] {
		if as, ok := n.(*syntax.AssignStmt); ok {
			for _, l := range as.Lhs {
				if place(l) {
					return true
				}
			}
			return false
		}
	}
	return false
}

// parentKind is the kind e's parent gives it.
func parentKind(info *check.Info, parent map[syntax.Node]syntax.Node, e syntax.Node) facts.SiteKind {
	in := func(list []syntax.Expr) bool {
		return slices.ContainsFunc(list, func(x syntax.Expr) bool { return x == e })
	}
	switch p := parent[e].(type) {
	case *syntax.CallExpr:
		if p.Fun == e {
			return facts.Call
		}
	case *syntax.CompositeLit:
		if p.Type == e {
			if info.Patterns[p] != nil {
				return facts.Match
			}
			return facts.Construct
		}
	case *syntax.NeedsClause:
		if in(p.Names) {
			return facts.Needs
		}
	case *syntax.ErrorRow:
		if in(p.Types) {
			return facts.Fails
		}
	case *syntax.ProvidesClause:
		if in(p.Names) {
			return facts.Provides
		}
	case *syntax.CatchArm:
		if in(p.Types) {
			return facts.Handles
		}
	case *syntax.ContextEntry:
		if p.Kind != syntax.ContextEmbed && (p.Key == e || in(p.Keys)) {
			return facts.Binds
		}
	}
	return 0
}

// instFlag is Inst when a call or an index expression of id's naming
// chain instantiates a generic function.
func instFlag(info *check.Info, parent map[syntax.Node]syntax.Node, id *syntax.Ident) uint8 {
	chain := naming(parent, id)
	top := chain[len(chain)-1]
	for _, e := range chain {
		if ix, ok := e.(*syntax.IndexExpr); ok && info.Instances[ix] != nil {
			return facts.Inst
		}
	}
	if call, ok := parent[top].(*syntax.CallExpr); ok && call.Fun == top && info.Instances[call] != nil {
		return facts.Inst
	}
	return 0
}

// ---------------------------------------------------------------- the generator

// generate writes a two-package program: ergo.mod, lib/lib.ergo, the
// root's plant.ergo and the root's evalgen program.
func generate(seed uint64) map[string]string {
	r := rand.New(rand.NewPCG(seed, 0x5e30))
	// A second package named lib is imported under an alias (an alias is
	// only for a name clash), and its Helper is called beside lib's.
	alias := r.IntN(2) == 0
	q := "lib"
	imp := "import gen/lib\n"
	if alias {
		imp += "import lx gen/x/lib\n"
	}
	lib := `type NotFound { Name string }

type Greeter {
	Greet(name string) string fails NotFound,
	Wave() string,

	law WaveIsStable(n int) {
		let w = Wave()
		w == Wave() || n < 0
	},
}

type Rec { A int, B string }

type Shape = Circle{R int} | Square{S int}

func NewRec(a int) Rec { Rec{A: a, B: "x"} }

func Area(s Shape) int {
	match s {
	case Circle as c:
		c.R * c.R
	case Square{S: n}:
		n * n
	}
}

func Helper(x int) int { x + 1 }

let Limit = 10
`
	var b strings.Builder
	b.WriteString(imp)
	fmt.Fprintf(&b, `
type Friends {} implements %[1]s.Greeter

type Strangers {} implements %[1]s.Greeter

type Point = %[1]s.Rec

func Greet(_ Friends, name string) string fails %[1]s.NotFound {
	if name == "Ada" {
		return "hello, " + name
	}
	return %[1]s.NotFound{Name: name}
}

func Wave(_ Friends) string { "hi" }

func Greet(_ Strangers, name string) string fails %[1]s.NotFound {
	return %[1]s.NotFound{Name: name}
}

func Wave(_ Strangers) string { "..." }

let Live = context {
	%[1]s.Greeter: Friends,
}

let Cold = context {
	%[1]s.Greeter: Strangers,
}

func Helper(x int) int { x * 2 }

func greet(name string) string {
	with Live { %[1]s.Greeter.Greet(name) } catch {
	case %[1]s.NotFound:
		"none"
	}
}

func wave() string {
	with Cold { %[1]s.Greeter.Wave() }
}
`, q)
	n := 1 + r.IntN(4)
	for i := range n {
		fmt.Fprintf(&b, "\nfunc plant%d(n int) int {\n", i)
		fmt.Fprintf(&b, "\tlet mut rec = %s.NewRec(n)\n", q)
		// Two locals of one name in one declaration: neither has a handle.
		fmt.Fprintf(&b, "\tif n > %d {\n\t\tlet v = n\n\t\trec.A = v\n\t} else {\n\t\tlet v = 1\n\t\trec.A = v\n\t}\n", r.IntN(5))
		var tail []string
		for _, s := range r.Perm(7) {
			switch s {
			case 0:
				fmt.Fprintf(&b, "\trec = %s.Rec{A: rec.A + %d, B: rec.B}\n", q, r.IntN(9))
			case 1:
				fmt.Fprintf(&b, "\trec.A = %d\n", r.IntN(9))
			case 2:
				fmt.Fprintf(&b, "\tlet p = Point{A: n, B: \"p\"}\n")
				tail = append(tail, "p.A")
			case 3:
				fmt.Fprintf(&b, "\tlet Helper = %s.Helper(rec.A)\n", q)
				tail = append(tail, "Helper")
			case 4:
				fmt.Fprintf(&b, "\tlet s %s.Shape = %s.Square{S: n}\n", q, q)
				tail = append(tail, q+".Area(s)")
			case 5:
				tail = append(tail, q+".Limit")
			case 6:
				fmt.Fprintf(&b, "\tlet g = len(greet(\"Ada\"))\n")
				tail = append(tail, "g")
			}
		}
		if !slices.Contains(tail, "Helper") {
			tail = append(tail, "Helper(rec.A)")
		}
		if alias {
			tail = append(tail, "lx.Helper(n)")
		}
		fmt.Fprintf(&b, "\t%s\n}\n", strings.Join(append(tail, "rec.A"), " + "))
	}
	files := map[string]string{
		"ergo.mod":     "module gen\n",
		"lib/lib.ergo": lib,
		"plant.ergo":   b.String(),
		"eval.ergo":    evalgen.Generate(seed).Source,
	}
	if alias {
		files["x/lib/lib.ergo"] = "func Helper(x int) int { x - 1 }\n"
	}
	return files
}
