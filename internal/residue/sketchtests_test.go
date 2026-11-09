package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The test rule (AGENTS.md, Tests; docs/decisions.md 2026-11-08, "Tests
// earn their keep") holds two more residue checks over the tests of
// internal/sketch: a split pins no count or digest of its generated
// domain, and a test that builds Go says why an in-process check is not
// enough. The existing offenders are rows of an allow list, each naming
// the unit that removes it (T5 for lower, T6 for check); a row that admits
// nothing fails, so the lists only shrink.
const (
	splitPinAllow  = "testdata/split-pin-allow.txt"
	buildsGoAllow  = "testdata/builds-go-allow.txt"
	buildsGoReason = "builds Go because"
)

// sketchTestFiles returns the tracked test files of internal/sketch,
// outside testdata, by repository path, with their sources.
func sketchTestFiles(t *testing.T) map[string][]byte {
	t.Helper()
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", sketchTests+"*_test.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := map[string][]byte{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = src
	}
	if len(files) < 100 {
		t.Fatalf("read %d tracked sketch test files; the listing is broken", len(files))
	}
	return files
}

// sketchFinding is one place a rule refuses: the position, and the name
// the allow list keys it by (a split's name, or a function's).
type sketchFinding struct {
	at, name string
}

// checkSketchRule runs find over every sketch test file and fails each
// finding that no row of the allow file admits, and each row that admits
// none.
func checkSketchRule(t *testing.T, allowFile string, find func(*testing.T, string, []byte) []sketchFinding, refuse func(sketchFinding) string) {
	t.Helper()
	allow := readAllow(t, allowFile)
	used := map[string]bool{}
	for name, src := range sketchTestFiles(t) {
		for _, f := range find(t, name, src) {
			key := name + "\t" + f.name
			if _, ok := allow[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s: %s, or add `%s<TAB>%s<TAB>reason` to %s", f.at, refuse(f), name, f.name, allowFile)
		}
	}
	for _, key := range staleRows(allow, used) {
		t.Errorf("%s: the row %q admits nothing; remove it", allowFile, strings.ReplaceAll(key, "\t", " "))
	}
}

// TestSplitsPinNoDomain: no split in internal/sketch pins the count or the
// digest of its generated domain. The split harness already checks that
// the parts run every case of the domain once; a pin only restates the
// generator, and each rule change that touched a generator cost a re-pin
// and caught nothing (test-speed plan 2.2).
func TestSplitsPinNoDomain(t *testing.T) {
	checkSketchRule(t, splitPinAllow, splitPins, func(f sketchFinding) string {
		return "the split " + f.name + " pins its domain's count or digest; the harness checks that the parts run each case once (AGENTS.md, Tests)"
	})
}

// TestSketchBuildsSayWhy: a test function in internal/sketch that calls
// buildAll or ergoCLI, or runs the go command, says in its doc comment why
// an in-process check (the checker's result, the lowered text, go/types)
// is not enough, by a line with "builds Go because".
func TestSketchBuildsSayWhy(t *testing.T) {
	checkSketchRule(t, buildsGoAllow, unexplainedBuilds, func(f sketchFinding) string {
		return f.name + " builds Go with no `" + buildsGoReason + "` line in its doc comment (AGENTS.md, Tests)"
	})
}

// pinFields are the fields of a split that pin its domain: check.Split's
// and lower's split's.
var pinFields = map[string]bool{"Count": true, "Digest": true, "count": true, "digest": true}

// isSplitType reports whether e names a split type: Split, split, a
// package's Split, an instance of one, or a pointer to one.
func isSplitType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.StarExpr:
		return isSplitType(x.X)
	case *ast.IndexExpr:
		return isSplitType(x.X)
	case *ast.IndexListExpr:
		return isSplitType(x.X)
	case *ast.Ident:
		return x.Name == "Split" || x.Name == "split"
	case *ast.SelectorExpr:
		return x.Sel.Name == "Split"
	}
	return false
}

// makesSplit reports whether e gives a split value: a split literal, its
// address, or a call of NewSplit or newSplit.
func makesSplit(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return makesSplit(x.X)
	case *ast.UnaryExpr:
		return x.Op == token.AND && makesSplit(x.X)
	case *ast.CompositeLit:
		return isSplitType(x.Type)
	case *ast.CallExpr:
		switch fun := x.Fun.(type) {
		case *ast.Ident:
			return fun.Name == "newSplit" || fun.Name == "NewSplit"
		case *ast.SelectorExpr:
			return fun.Sel.Name == "NewSplit"
		case *ast.IndexExpr:
			return makesSplit(&ast.CallExpr{Fun: fun.X})
		}
	}
	return false
}

// isZero reports whether e is the zero literal 0 or "".
func isZero(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && (lit.Value == "0" || lit.Value == `""`)
}

// declName is the name a top-level declaration is known by: a function's
// name (Type.Method for a method), or its first declared name.
func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			return recvName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) > 0 {
				return v.Names[0].Name
			}
		}
	}
	return "?"
}

func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

// splitPins returns each pin in src: a Count or Digest field (or count,
// digest) set to a nonzero value in a split literal, or assigned to a
// split: a parameter, a receiver or a variable of split type, or a
// variable given a split, in the function or at the top of the file. A
// literal's pin is named by the split's Name; an assigned one by the
// declaration that assigns it.
func splitPins(t *testing.T, name string, src []byte) []sketchFinding {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var out []sketchFinding
	add := func(n ast.Node, split string) {
		out = append(out, sketchFinding{at: fset.Position(n.Pos()).String(), name: split})
	}
	// splits names the variables that hold a split, at the top of the
	// file and in each declaration.
	top := map[string]bool{}
	note := func(names map[string]bool, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.Field:
				if isSplitType(s.Type) {
					for _, id := range s.Names {
						names[id.Name] = true
					}
				}
			case *ast.ValueSpec:
				for i, id := range s.Names {
					if s.Type != nil && isSplitType(s.Type) || i < len(s.Values) && makesSplit(s.Values[i]) {
						names[id.Name] = true
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && len(s.Rhs) == len(s.Lhs) && makesSplit(s.Rhs[i]) {
						names[id.Name] = true
					}
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.VAR {
			note(top, g)
		}
	}
	for _, decl := range file.Decls {
		owner := declName(decl)
		names := map[string]bool{}
		for k := range top {
			names[k] = true
		}
		note(names, decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.CompositeLit:
				if !isSplitType(s.Type) {
					return true
				}
				split := owner
				for _, elt := range s.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Name" || key.Name == "name") {
						if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								split = v
							}
						}
					}
				}
				for _, elt := range s.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && pinFields[key.Name] && !isZero(kv.Value) {
							add(kv, split)
						}
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range s.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || !pinFields[sel.Sel.Name] {
						continue
					}
					if x, ok := sel.X.(*ast.Ident); !ok || !names[x.Name] {
						continue
					}
					if len(s.Rhs) == len(s.Lhs) && isZero(s.Rhs[i]) {
						continue
					}
					add(s, owner)
				}
			}
			return true
		})
	}
	return out
}

// buildsGo reports whether call builds Go: a call of buildAll or ergoCLI,
// or exec.Command or exec.CommandContext of "go".
func buildsGo(call *ast.CallExpr) bool {
	if fun, ok := call.Fun.(*ast.Ident); ok {
		return fun.Name == "buildAll" || fun.Name == "ergoCLI"
	}
	if !isExecCommand(call.Fun) {
		return false
	}
	i := 0
	if call.Fun.(*ast.SelectorExpr).Sel.Name == "CommandContext" {
		i = 1
	}
	return len(call.Args) > i && isString(call.Args[i], "go")
}

// unexplainedBuilds returns each top-level declaration of src (a
// function, a method, or a variable whose value holds a function literal)
// whose own body builds Go and whose own doc comment has no "builds Go
// because" line. The doc comment of another declaration does not count.
func unexplainedBuilds(t *testing.T, name string, src []byte) []sketchFinding {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var out []sketchFinding
	check := func(owner string, doc *ast.CommentGroup, body ast.Node) {
		var at ast.Node
		ast.Inspect(body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && at == nil && buildsGo(call) {
				at = call
			}
			return at == nil
		})
		if at != nil && !strings.Contains(doc.Text(), buildsGoReason) {
			out = append(out, sketchFinding{at: fset.Position(at.Pos()).String(), name: owner})
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				check(declName(d), d.Doc, d.Body)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				v, ok := spec.(*ast.ValueSpec)
				if !ok || len(v.Names) == 0 {
					continue
				}
				doc := v.Doc
				if doc == nil {
					doc = d.Doc
				}
				check(v.Names[0].Name, doc, v)
			}
		}
	}
	return out
}

// TestSplitPinRule pins the split rule on fixed sources.
func TestSplitPinRule(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
		name string
	}{
		{`var s = check.NewSplit(check.Split[int]{Name: "TestA", Parts: 2, Count: 3, Digest: "ab"})`, []string{"TestA", "TestA"}, "an exported split with both pins"},
		{`var s = newSplit(split[int]{name: "TestB", parts: 2, digest: "ab"})`, []string{"TestB"}, "lower's split with a digest"},
		{`var s = newSplit(split[int]{name: "TestC", parts: 2, unpinned: true})`, nil, "an unpinned split"},
		{`var s = newSplit(split[int]{name: "TestD", count: 0, digest: ""})`, nil, "zero pins"},
		{`func pinned[C any](s *split[C], n int, d string) *split[C] { s.count, s.digest = n, d; return s }`, []string{"pinned", "pinned"}, "a pin set through a helper"},
		{`func init() { i1Split.count = 17131 }
var i1Split = newSplit(split[int]{name: "TestE"})`, []string{"init"}, "a pin assigned to a split of the file"},
		{`func f() { s := &check.Split[int]{Name: "TestF"}; s.Count = 4; _ = s }`, []string{"f"}, "a local split, then its pin"},
		{`func f(r result) { r.count = 4 }`, nil, "a count of something that is no split"},
		{`type Split[C any] struct { Count int; Digest string }`, nil, "the harness's own type"},
	} {
		src := "package p\n\n" + c.src + "\n"
		var got []string
		for _, f := range splitPins(t, "internal/sketch/p_test.go", []byte(src)) {
			got = append(got, f.name)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %s: pins %q, want %q", c.name, c.src, got, c.want)
		}
	}
}

// TestBuildsGoRule pins the build rule on fixed sources.
func TestBuildsGoRule(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
		name string
	}{
		{"func TestA(t *testing.T) { buildAll(t, programs, true) }", []string{"TestA"}, "buildAll with no doc"},
		{"// TestA runs a program.\nfunc TestA(t *testing.T) { _ = ergoCLI(t) }", []string{"TestA"}, "ergoCLI with a doc that gives no reason"},
		{"// TestA builds Go because only gc sees it.\nfunc TestA(t *testing.T) { buildAll(t, programs, true) }", nil, "a reason"},
		{"// TestA builds Go because it must.\nfunc TestA(t *testing.T) {}\n\nfunc TestB(t *testing.T) { buildAll(t, programs, false) }", []string{"TestB"}, "a reason in another function's doc"},
		{"func TestA(t *testing.T) {\n\t// builds Go because a comment in the body is no doc\n\texec.Command(\"go\", \"build\").Run()\n}", []string{"TestA"}, "a reason inside the body"},
		{`func TestA(t *testing.T) { exec.CommandContext(ctx, "go", "test").Run() }`, []string{"TestA"}, "the go command with a context"},
		{`func (h *harness) run() { exec.Command("go", "vet").Run() }`, []string{"harness.run"}, "a method"},
		{`var s = newSplit(split[int]{run: func(t *testing.T, _ iter.Seq[int]) { buildAll(t, nil, true) }})`, []string{"s"}, "a split's run"},
		{`func TestA(t *testing.T) { exec.Command("git", "status").Run(); exec.Command(goTool, "build").Run() }`, nil, "another program, or go by a name (a known gap)"},
	} {
		src := "package p\n\n" + c.src + "\n"
		var got []string
		for _, f := range unexplainedBuilds(t, "internal/sketch/p_test.go", []byte(src)) {
			got = append(got, f.name)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %q: refused %q, want %q", c.name, c.src, got, c.want)
		}
	}
}
