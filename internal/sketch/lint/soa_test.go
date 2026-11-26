package lint

import (
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

func loadSOATestInputs(t *testing.T) []Input {
	t.Helper()
	dirs := []string{
		filepath.Join("..", "..", "..", "compiler", "syntax"),
		filepath.Join("..", "..", "..", "examples", "jobs"),
		filepath.Join("..", "..", "..", "examples", "confparse"),
		filepath.Join("..", "..", "..", "examples", "bookmarks"),
	}
	var out []Input
	for _, dir := range dirs {
		targets, err := sketchfront.DiscoverPackages(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			a, err := sketchfront.CheckDir(target.Dir)
			if err != nil {
				t.Fatal(err)
			}
			in := InputOf(a)
			if in.Checked {
				out = append(out, in)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("loaded 0 checked packages")
	}
	return out
}

// TestSOAFileIndexMatchesInspect verifies that the single-pass flat columnar
// fileIndex matches syntax.Inspect and lineAt
// node-for-node, flag-for-flag, and offset-for-offset across the corpus.
func TestSOAFileIndexMatchesInspect(t *testing.T) {
	inputs := loadSOATestInputs(t)
	checkedFiles := 0
	for _, in := range inputs {
		for _, u := range in.Units {
			if u.Package == nil || u.Package.Info == nil {
				continue
			}
			for _, f := range u.Files {
				if f.Syntax == nil {
					continue
				}
				checkedFiles++
				verifyFileIndex(t, f.Rel, f.Syntax)
			}
		}
	}
	if checkedFiles == 0 {
		t.Fatal("verified 0 files")
	}
}

func verifyFileIndex(t *testing.T, rel string, f *syntax.File) {
	t.Helper()
	fi := buildFileIndex(f)

	var wantNodes []syntax.Node
	syntax.Inspect(f, func(n syntax.Node) bool {
		wantNodes = append(wantNodes, n)
		return true
	})

	var (
		wantIdents         []*syntax.Ident
		wantCalls          []*syntax.CallExpr
		wantAssigns        []*syntax.AssignStmt
		wantLits           []*syntax.CompositeLit
		wantBinaries       []*syntax.BinaryExpr
		wantSelectors      []*syntax.SelectorExpr
		wantFields         []*syntax.Field
		wantDeclStmts      []*syntax.DeclStmt
		wantSwitches       []syntax.Node
		wantCatches        []*syntax.CatchExpr
		wantRanges         []*syntax.RangeStmt
		wantBlocks         []*syntax.BlockStmt
		wantIndices        []*syntax.IndexExpr
		wantFuncLits       []*syntax.FuncLit
		wantContextEntries []*syntax.ContextEntry
		rawBodies          [][2]syntax.Pos
	)
	for _, n := range wantNodes {
		switch x := n.(type) {
		case *syntax.Ident:
			wantIdents = append(wantIdents, x)
		case *syntax.CallExpr:
			wantCalls = append(wantCalls, x)
		case *syntax.AssignStmt:
			wantAssigns = append(wantAssigns, x)
		case *syntax.CompositeLit:
			wantLits = append(wantLits, x)
		case *syntax.BinaryExpr:
			wantBinaries = append(wantBinaries, x)
		case *syntax.SelectorExpr:
			wantSelectors = append(wantSelectors, x)
		case *syntax.Field:
			wantFields = append(wantFields, x)
		case *syntax.DeclStmt:
			wantDeclStmts = append(wantDeclStmts, x)
		case *syntax.SwitchStmt:
			wantSwitches = append(wantSwitches, x)
		case *syntax.TypeSwitchStmt:
			wantSwitches = append(wantSwitches, x)
		case *syntax.CatchExpr:
			wantSwitches = append(wantSwitches, x)
			wantCatches = append(wantCatches, x)
		case *syntax.RangeStmt:
			wantRanges = append(wantRanges, x)
		case *syntax.BlockStmt:
			wantBlocks = append(wantBlocks, x)
		case *syntax.IndexExpr:
			wantIndices = append(wantIndices, x)
		case *syntax.FuncLit:
			wantFuncLits = append(wantFuncLits, x)
			if x.Body != nil {
				rawBodies = append(rawBodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
			}
		case *syntax.ContextEntry:
			wantContextEntries = append(wantContextEntries, x)
		case *syntax.FuncDecl:
			if x.Body != nil {
				rawBodies = append(rawBodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
			}
		case *syntax.TestDecl:
			if x.Body != nil {
				rawBodies = append(rawBodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
			}
		case *syntax.LawDecl:
			if x.Body != nil {
				rawBodies = append(rawBodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
			}
		}
	}
	if !slices.Equal(fi.idents, wantIdents) {
		t.Fatalf("%s: idents mismatch (%d vs %d)", rel, len(fi.idents), len(wantIdents))
	}
	if !slices.Equal(fi.calls, wantCalls) {
		t.Fatalf("%s: calls mismatch (%d vs %d)", rel, len(fi.calls), len(wantCalls))
	}
	if !slices.Equal(fi.assigns, wantAssigns) {
		t.Fatalf("%s: assigns mismatch (%d vs %d)", rel, len(fi.assigns), len(wantAssigns))
	}
	if !slices.Equal(fi.lits, wantLits) {
		t.Fatalf("%s: lits mismatch (%d vs %d)", rel, len(fi.lits), len(wantLits))
	}
	if !slices.Equal(fi.binaries, wantBinaries) {
		t.Fatalf("%s: binaries mismatch (%d vs %d)", rel, len(fi.binaries), len(wantBinaries))
	}
	if !slices.Equal(fi.selectors, wantSelectors) {
		t.Fatalf("%s: selectors mismatch (%d vs %d)", rel, len(fi.selectors), len(wantSelectors))
	}
	if !slices.Equal(fi.fields, wantFields) {
		t.Fatalf("%s: fields mismatch (%d vs %d)", rel, len(fi.fields), len(wantFields))
	}
	if !slices.Equal(fi.declStmts, wantDeclStmts) {
		t.Fatalf("%s: declStmts mismatch (%d vs %d)", rel, len(fi.declStmts), len(wantDeclStmts))
	}
	if !slices.Equal(fi.switches, wantSwitches) {
		t.Fatalf("%s: switches mismatch (%d vs %d)", rel, len(fi.switches), len(wantSwitches))
	}
	if !slices.Equal(fi.catches, wantCatches) {
		t.Fatalf("%s: catches mismatch (%d vs %d)", rel, len(fi.catches), len(wantCatches))
	}
	if !slices.Equal(fi.ranges, wantRanges) {
		t.Fatalf("%s: ranges mismatch (%d vs %d)", rel, len(fi.ranges), len(wantRanges))
	}
	if !slices.Equal(fi.blocks, wantBlocks) {
		t.Fatalf("%s: blocks mismatch (%d vs %d)", rel, len(fi.blocks), len(wantBlocks))
	}
	if !slices.Equal(fi.indices, wantIndices) {
		t.Fatalf("%s: indices mismatch (%d vs %d)", rel, len(fi.indices), len(wantIndices))
	}
	if !slices.Equal(fi.funcLits, wantFuncLits) {
		t.Fatalf("%s: funcLits mismatch (%d vs %d)", rel, len(fi.funcLits), len(wantFuncLits))
	}
	if !slices.Equal(fi.contextEntries, wantContextEntries) {
		t.Fatalf("%s: contextEntries mismatch (%d vs %d)", rel, len(fi.contextEntries), len(wantContextEntries))
	}

	// 3. Verify callFlags (nodeInTestLaw) and assignLoops against reference walk.
	inTestLawCalls := map[*syntax.CallExpr]bool{}
	for _, d := range f.Decls {
		switch d.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			syntax.Inspect(d, func(n syntax.Node) bool {
				if c, ok := n.(*syntax.CallExpr); ok {
					inTestLawCalls[c] = true
				}
				return true
			})
		case *syntax.TypeDecl:
			syntax.Inspect(d, func(n syntax.Node) bool {
				if l, ok := n.(*syntax.LawDecl); ok {
					syntax.Inspect(l, func(m syntax.Node) bool {
						if c, ok := m.(*syntax.CallExpr); ok {
							inTestLawCalls[c] = true
						}
						return true
					})
					return false
				}
				return true
			})
		}
	}
	for i, c := range fi.calls {
		got := fi.callFlags[i]&nodeInTestLaw != 0
		if got != inTestLawCalls[c] {
			t.Fatalf("%s: call[%d] nodeInTestLaw=%v, want %v", rel, i, got, inTestLawCalls[c])
		}
	}

	wantLoop := map[*syntax.AssignStmt]syntax.Stmt{}
	var walkLoops func(n syntax.Node, loop syntax.Stmt)
	walkLoops = func(n syntax.Node, loop syntax.Stmt) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			switch x := m.(type) {
			case *syntax.FuncLit:
				walkLoops(x.Body, nil)
				return false
			case *syntax.ForStmt:
				walkLoops(x.Cond, x)
				walkLoops(x.Body, x)
				return false
			case *syntax.RangeStmt:
				walkLoops(x.Key, loop)
				walkLoops(x.Value, loop)
				walkLoops(x.X, loop)
				walkLoops(x.Body, x)
				return false
			case *syntax.AssignStmt:
				wantLoop[x] = loop
			}
			return true
		})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				walkLoops(d.Body, nil)
			}
		case *syntax.TestDecl:
			if d.Body != nil {
				walkLoops(d.Body, nil)
			}
		case *syntax.LawDecl:
			if d.Body != nil {
				walkLoops(d.Body, nil)
			}
		}
	}
	for i, a := range fi.assigns {
		if fi.assignInBody[i] && fi.assignLoops[i] != wantLoop[a] {
			t.Fatalf("%s: assign[%d] loop mismatch: got %v, want %v", rel, i, fi.assignLoops[i], wantLoop[a])
		}
	}

	// 4. Verify per-decl contiguous ranges.
	if len(fi.decls) != len(f.Decls) {
		t.Fatalf("%s: decls len %d != %d", rel, len(fi.decls), len(f.Decls))
	}
	for k, d := range f.Decls {
		var di []*syntax.Ident
		var dc []*syntax.CallExpr
		syntax.Inspect(d, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.Ident:
				di = append(di, x)
			case *syntax.CallExpr:
				dc = append(dc, x)
			}
			return true
		})
		dr := fi.decls[k]
		if !slices.Equal(fi.idents[dr.identStart:dr.identEnd], di) {
			t.Fatalf("%s: decl[%d] idents mismatch", rel, k)
		}
		if !slices.Equal(fi.calls[dr.callStart:dr.callEnd], dc) {
			t.Fatalf("%s: decl[%d] calls mismatch", rel, k)
		}
	}

	if want := docComments(f); !maps.Equal(fi.docComments, want) {
		t.Fatalf("%s: %d doc comments, want %d", rel, len(fi.docComments), len(want))
	}

	// 5. Verify merged bodySpans and inBody against naive linear scan over rawBodies.
	naiveInBody := func(pos syntax.Pos) bool {
		for _, s := range rawBodies {
			if pos > s[0] && pos < s[1] {
				return true
			}
		}
		return false
	}
	for _, s := range rawBodies {
		for _, p := range []syntax.Pos{s[0] - 1, s[0], s[0] + 1, (s[0] + s[1]) / 2, s[1] - 1, s[1], s[1] + 1} {
			if p < 0 {
				continue
			}
			if got, want := fi.inBody(p), naiveInBody(p); got != want {
				t.Fatalf("%s: inBody(%d)=%v, want %v (spans=%v)", rel, p, got, want, fi.bodySpans)
			}
		}
	}
	for _, g := range f.Comments {
		for _, c := range g.List {
			if got, want := fi.inBody(c.Pos()), naiveInBody(c.Pos()); got != want {
				t.Fatalf("%s: comment inBody(%d)=%v, want %v", rel, c.Pos(), got, want)
			}
		}
	}

	// 6. Verify lineBounds against lineAt at line boundaries and comments.
	src := f.Src
	checkLine := func(pos int) {
		gs, ge := fi.lineBounds(pos)
		ws, we := lineAt(src, pos)
		if gs != ws || ge != we {
			t.Fatalf("%s: lineBounds(%d)=(%d,%d), want (%d,%d)", rel, pos, gs, ge, ws, we)
		}
	}
	checkLine(0)
	if len(src) > 0 {
		checkLine(len(src) - 1)
		checkLine(len(src))
	}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			if i > 0 {
				checkLine(i - 1)
			}
			checkLine(i)
			if i+1 < len(src) {
				checkLine(i + 1)
			}
		}
	}
}

// TestShapeOfMatchesReflectionReference verifies the zero-reflection shaper
// produces byte-for-byte identical output to the reflection-based reference
// across corpus functions and operator edge cases.
func TestShapeOfMatchesReflectionReference(t *testing.T) {
	inputs := loadSOATestInputs(t)
	checked := 0
	for _, in := range inputs {
		for _, u := range in.Units {
			if u.Package == nil || u.Package.Info == nil {
				continue
			}
			info := u.Package.Info
			for _, f := range u.Files {
				if f.Syntax == nil {
					continue
				}
				for _, d := range f.Syntax.Decls {
					fn, ok := d.(*syntax.FuncDecl)
					if !ok || fn.Body == nil {
						continue
					}
					checked++
					got := shapeOf(info, fn.Body)
					want := refShapeOf(info, false, fn.Body)
					if got != want {
						t.Fatalf("%s %s: shapeOf mismatch:\ngot:  %s\nwant: %s", f.Rel, fn.Name.Name, got, want)
					}
					if len(fn.Body.List) > 0 {
						gotEdit := shapeOfEdit(info, fn.Body.List[0])
						wantEdit := refShapeOf(info, true, fn.Body.List[0])
						if gotEdit != wantEdit {
							t.Fatalf("%s %s: shapeOfEdit mismatch:\ngot:  %s\nwant: %s", f.Rel, fn.Name.Name, gotEdit, wantEdit)
						}
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked 0 functions")
	}
}

func refShapeOf(info *check.Info, free bool, nodes ...syntax.Node) string {
	var b strings.Builder
	locals := map[check.Object]int{}
	var writeNode func(n syntax.Node)
	writeNode = func(n syntax.Node) {
		switch x := n.(type) {
		case *syntax.Ident:
			if o := info.Defs[x]; o != nil {
				if _, seen := locals[o]; !seen {
					locals[o] = len(locals)
				}
				fmt.Fprintf(&b, "(def %d)", locals[o])
				return
			}
			if o := info.Uses[x]; o != nil {
				if i, local := locals[o]; local {
					fmt.Fprintf(&b, "(local %d)", i)
				} else if _, isVar := o.(*check.Var); isVar && free {
					locals[o] = len(locals)
					fmt.Fprintf(&b, "(local %d)", locals[o])
				} else {
					fmt.Fprintf(&b, "(obj %p)", o)
				}
				return
			}
			fmt.Fprintf(&b, "(name %q)", x.Name)
			return
		case *syntax.BasicLit:
			if v, ok := info.Values[x]; ok && v != nil {
				fmt.Fprintf(&b, "(lit %s)", v.ExactString())
			} else {
				fmt.Fprintf(&b, "(lit %d %q)", x.Kind, x.Value)
			}
			return
		}
		v := reflect.ValueOf(n)
		if !v.IsValid() || v.IsNil() {
			return
		}
		rt := v.Type().Elem()
		b.WriteByte('(')
		b.WriteString(rt.Name())
		s := v.Elem()
		for i := range rt.NumField() {
			ft := rt.Field(i)
			if !ft.IsExported() {
				continue
			}
			fv := s.Field(i)
			if fv.Type() == reflect.TypeFor[syntax.Pos]() {
				continue
			}
			if fv.Type() == reflect.TypeFor[syntax.Token]() {
				fmt.Fprintf(&b, " %s=%s", ft.Name, fv.Interface())
				continue
			}
			switch fv.Kind() {
			case reflect.Bool, reflect.String, reflect.Int:
				fmt.Fprintf(&b, " %s=%v", ft.Name, fv.Interface())
			}
		}
		syntax.Inspect(n, func(c syntax.Node) bool {
			if c == n {
				return true
			}
			b.WriteByte(' ')
			writeNode(c)
			return false
		})
		b.WriteByte(')')
	}
	for _, n := range nodes {
		writeNode(n)
		b.WriteByte(';')
	}
	return b.String()
}

// TestLooksLikeProseMatchesParser verifies that the fast prose filter in
// parsesAsCode agrees 100% with the unguarded parser on every comment across
// the corpus and on keyword/code edge cases.
func TestLooksLikeProseMatchesParser(t *testing.T) {
	refParses := func(text string) bool {
		if f, err := syntax.Parse("func commented() {\n" + text + "\n}\n"); err == nil && len(f.Decls) == 1 {
			if fn, ok := f.Decls[0].(*syntax.FuncDecl); ok && fn.Body != nil {
				for _, s := range fn.Body.List {
					if !loneOperand(s) {
						return true
					}
				}
			}
		}
		f, err := syntax.Parse(text + "\n")
		return err == nil && len(f.Decls) > 0
	}
	samples := []string{
		"",
		"   \n\t ",
		"let x = 1",
		"return x + 1",
		"x = x + 1",
		"x catch { _ => 0 }",
		"entry func main() {}",
		"func f(x int) int { x }",
		"always x > 0",
		"die \"boom\"",
		"this is ordinary english prose",
		"TODO fix the parser later",
		"singleword",
	}
	for _, s := range samples {
		if got, want := parsesAsCode(s), refParses(s); got != want {
			t.Errorf("parsesAsCode(%q)=%v, want %v", s, got, want)
		}
	}
	inputs := loadSOATestInputs(t)
	checked := 0
	for _, in := range inputs {
		for _, u := range in.Units {
			for _, f := range u.Files {
				if f.Syntax == nil {
					continue
				}
				for _, g := range f.Syntax.Comments {
					text := commentBody(g)
					checked++
					if got, want := parsesAsCode(text), refParses(text); got != want {
						t.Errorf("%s: parsesAsCode(%q)=%v, want %v", f.Rel, text, got, want)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked 0 comments")
	}
}

// docComments is the reference of fileIndex.docComments: the comment
// groups the file's declarations, fields, context entries and machine
// entries carry as their doc comments.
func docComments(f *syntax.File) map[*syntax.CommentGroup]bool {
	out := map[*syntax.CommentGroup]bool{}
	add := func(g *syntax.CommentGroup) {
		if g != nil {
			out[g] = true
		}
	}
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ImportDecl:
			add(n.Doc)
		case *syntax.VarDecl:
			add(n.Doc)
		case *syntax.TypeDecl:
			add(n.Doc)
		case *syntax.FuncDecl:
			add(n.Doc)
		case *syntax.ContextDecl:
			add(n.Doc)
		case *syntax.ContextEntry:
			add(n.Doc)
		case *syntax.Field:
			add(n.Doc)
		case *syntax.MachineEntry:
			add(n.Doc)
		case *syntax.TestDecl:
			add(n.Doc)
		case *syntax.LawDecl:
			add(n.Doc)
		}
		return true
	})
	return out
}
