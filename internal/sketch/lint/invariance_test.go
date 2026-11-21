package lint_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// wordReaders are the rules that read words of names or comments, by the
// transform whose meaning-preserving change they are declared sensitive to
// (LINT-HYGIENE §[R2] 4): their findings may change under it, and the
// test prints how many did instead of failing. TestWordReadersListed
// keeps the table equal to the rules that call the word helpers.
var wordReaders = map[string][]string{
	"rename":     {"banned-words"},
	"paraphrase": {},
}

// transform is one meaning-preserving change of one file: it returns the
// new text, or ok false when the file offers no place for it.
type transform struct {
	name  string
	apply func(r *rand.Rand, f *syntax.File, info *check.Info) (string, bool)
}

// edit is a replacement of src[from:to].
type edit struct {
	from, to int
	text     string
}

// applyEdits applies non-overlapping edits to src.
func applyEdits(src string, edits []edit) string {
	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	for _, e := range edits {
		src = src[:e.from] + e.text + src[e.to:]
	}
	return src
}

// swap exchanges two non-overlapping spans a before b.
func swap(src string, a, b [2]int) string {
	return applyEdits(src, []edit{{a[0], a[1], src[b[0]:b[1]]}, {b[0], b[1], src[a[0]:a[1]]}})
}

var transforms = []transform{
	{"fmt", func(r *rand.Rand, f *syntax.File, _ *check.Info) (string, bool) {
		// ergo fmt, and its inverses on formatted input: trailing spaces,
		// an extra tab of indentation and blank lines between lines.
		lines := strings.Split(f.Src, "\n")
		kind := r.IntN(3)
		for i := range lines {
			switch {
			case kind == 0 && lines[i] != "":
				lines[i] += "  \t"
			case kind == 1 && lines[i] != "":
				lines[i] = "\t" + lines[i]
			case kind == 2 && strings.TrimSpace(lines[i]) == "}":
				lines[i] += "\n"
			}
		}
		out := strings.Join(lines, "\n")
		if r.IntN(2) == 0 {
			formatted, err := format.Source(out)
			if err != nil {
				return "", false
			}
			out = formatted
		}
		return out, true
	}},
	{"rename", func(r *rand.Rand, f *syntax.File, info *check.Info) (string, bool) {
		var locals []*syntax.Ident
		syntax.Inspect(f, func(n syntax.Node) bool {
			if s, ok := n.(*syntax.DeclStmt); ok {
				if v := s.Decl; v != nil {
					for _, id := range v.Specs[0].Names {
						if _, isVar := info.Defs[id].(*check.Var); isVar && id.Name != "_" {
							locals = append(locals, id)
						}
					}
				}
			}
			return true
		})
		if len(locals) == 0 {
			return "", false
		}
		def := locals[r.IntN(len(locals))]
		obj := info.Defs[def]
		name := fmt.Sprintf("zq%d", r.IntN(1000))
		edits := []edit{{int(def.NamePos), int(def.NamePos) + len(def.Name), name}}
		for id, o := range info.Uses {
			if o == obj && int(id.NamePos) < len(f.Src) && strings.HasPrefix(f.Src[id.NamePos:], id.Name) {
				edits = append(edits, edit{int(id.NamePos), int(id.NamePos) + len(id.Name), name})
			}
		}
		return applyEdits(f.Src, edits), true
	}},
	{"reorder-decls", func(r *rand.Rand, f *syntax.File, _ *check.Info) (string, bool) {
		var spans [][2]int
		for _, d := range f.Decls {
			if _, isImport := d.(*syntax.ImportDecl); isImport {
				continue
			}
			from := int(d.Pos())
			if doc := docOf(d); doc != nil {
				from = int(doc.Pos())
			}
			spans = append(spans, [2]int{from, int(d.End())})
		}
		if len(spans) < 2 {
			return "", false
		}
		i := r.IntN(len(spans) - 1)
		return swap(f.Src, spans[i], spans[i+1]), true
	}},
	{"reorder-arms", func(r *rand.Rand, f *syntax.File, _ *check.Info) (string, bool) {
		var pairs [][2][2]int
		syntax.Inspect(f, func(n syntax.Node) bool {
			s, ok := n.(*syntax.TypeSwitchStmt)
			if !ok {
				return true
			}
			cs := s.Clauses
			for i := 0; i+2 < len(cs); i++ {
				if simpleArm(f.Src, cs[i]) && simpleArm(f.Src, cs[i+1]) {
					pairs = append(pairs, [2][2]int{{int(cs[i].Case), int(cs[i+1].Case)}, {int(cs[i+1].Case), int(cs[i+2].Case)}})
				}
			}
			return true
		})
		if len(pairs) == 0 {
			return "", false
		}
		p := pairs[r.IntN(len(pairs))]
		return swap(f.Src, p[0], p[1]), true
	}},
	{"fields-parens", func(r *rand.Rand, f *syntax.File, _ *check.Info) (string, bool) {
		var edits []edit
		syntax.Inspect(f, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.CompositeLit:
				if len(n.Elts) >= 2 && plainField(n.Elts[0]) && plainField(n.Elts[1]) && r.IntN(2) == 0 &&
					!strings.Contains(f.Src[n.Elts[0].Pos():n.Elts[1].End()], "\n") {
					a, b := n.Elts[0], n.Elts[1]
					edits = append(edits, edit{int(a.Pos()), int(b.End()), f.Src[b.Pos():b.End()] + ", " + f.Src[a.Pos():a.End()]})
					return false
				}
			case *syntax.BinaryExpr:
				// a && b || c is (a && b) || c, since && binds tighter, and
				// a || b || c is (a || b) || c, since both group left.
				if x, ok := n.X.(*syntax.BinaryExpr); ok && (n.Op == syntax.LOR || n.Op == syntax.LAND) &&
					(x.Op == n.Op || x.Op == syntax.LAND && n.Op == syntax.LOR) && r.IntN(2) == 0 {
					edits = append(edits, edit{int(x.Pos()), int(x.End()), "(" + f.Src[x.Pos():x.End()] + ")"})
					return false
				}
			case *syntax.IfStmt:
				if n.Cond != nil && r.IntN(2) == 0 {
					c := n.Cond
					edits = append(edits, edit{int(c.Pos()), int(c.End()), "(" + f.Src[c.Pos():c.End()] + ")"})
					return false
				}
			case *syntax.VarSpec:
				if len(n.Values) == 1 && r.IntN(3) == 0 {
					v := n.Values[0]
					edits = append(edits, edit{int(v.Pos()), int(v.End()), "(" + f.Src[v.Pos():v.End()] + ")"})
					return false
				}
			}
			return true
		})
		if len(edits) == 0 {
			return "", false
		}
		return applyEdits(f.Src, edits), true
	}},
	{"paraphrase", func(r *rand.Rand, f *syntax.File, _ *check.Info) (string, bool) {
		synonyms := map[string]string{"the": "that", "is": "stays", "of": "from", "value": "result", "returns": "gives", "first": "initial", "each": "every", "a": "one"}
		var edits []edit
		for _, g := range f.Comments {
			for _, c := range g.List {
				text := f.Src[c.Pos():c.End()]
				if strings.Contains(text, lint.DirectiveWord) || strings.Contains(text, "Deprecated") || strings.Contains(text, "Unbounded") {
					continue
				}
				words := strings.Split(text, " ")
				changed := false
				for i, w := range words {
					if s, ok := synonyms[w]; ok && r.IntN(2) == 0 {
						words[i], changed = s, true
					}
				}
				if changed {
					edits = append(edits, edit{int(c.Pos()), int(c.End()), strings.Join(words, " ")})
				}
			}
		}
		if len(edits) == 0 {
			return "", false
		}
		return applyEdits(f.Src, edits), true
	}},
}

// dropBlankLines is text without its empty lines.
func dropBlankLines(text string) string {
	return strings.Join(slices.DeleteFunc(strings.Split(text, "\n"), func(l string) bool { return l == "" }), "\n")
}

// docOf is a top-level declaration's doc comment, nil without one.
func docOf(d syntax.Decl) *syntax.CommentGroup {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Doc
	case *syntax.TypeDecl:
		return d.Doc
	case *syntax.VarDecl:
		return d.Doc
	case *syntax.ContextDecl:
		return d.Doc
	case *syntax.LawDecl:
		return d.Doc
	case *syntax.TestDecl:
		return d.Doc
	}
	return nil
}

// simpleArm is an arm of one pattern naming a variant, with or without a
// binding: arms of such patterns over a union are disjoint, so their order
// is not meaning.
func simpleArm(src string, c *syntax.CaseClause) bool {
	if len(c.List) != 1 || len(c.Written) > 0 {
		return false
	}
	text := src[c.List[0].Pos():c.List[0].End()]
	return text != "_" && !strings.ContainsAny(text, "(){}[],\"'")
}

// plainField is a keyed field whose value has no effect to reorder.
func plainField(e syntax.Expr) bool {
	kv, ok := e.(*syntax.KeyValueExpr)
	if !ok {
		return false
	}
	switch kv.Value.(type) {
	case *syntax.BasicLit, *syntax.Ident:
		return true
	}
	return false
}

// declName names the top-level declaration of file f that holds offset.
func declName(f *syntax.File, offset int) string {
	for _, d := range f.Decls {
		if int(d.Pos()) <= offset && offset < int(d.End()) {
			switch d := d.(type) {
			case *syntax.FuncDecl:
				return "func " + d.Name.Name
			case *syntax.TypeDecl:
				return "type " + d.Name.Name
			case *syntax.LawDecl:
				return "law " + d.Name.Name
			case *syntax.TestDecl:
				return "test " + d.Name.Name
			case *syntax.ContextDecl:
				return "context " + d.Name.Name
			case *syntax.VarDecl:
				if len(d.Specs[0].Names) > 0 {
					return "let " + d.Specs[0].Names[0].Name
				}
			}
			return fmt.Sprintf("%T", d)
		}
	}
	return "file"
}

// allRules is every built-in rule on at warning, with the complexity caps
// at 0, so every function reports its three metrics in its messages.
func allRules(t *testing.T) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	}
	for _, name := range []string{"cognitive-complexity", "cyclomatic-complexity", "halstead-difficulty"} {
		settings[name] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: []byte(`{"max": 0}`)}
	}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// findingSet is a run's findings as the sorted multiset of (file, rule,
// declaration, message): what a meaning-preserving change must keep. The
// message carries a metric's value. Positions are not in it: a reformat
// moves them.
func findingSet(in lint.Input, res lint.Result) []string {
	files := map[string]*syntax.File{}
	for _, u := range in.Units {
		for _, f := range u.Files {
			if f.Syntax != nil {
				files[f.Rel] = f.Syntax
			}
		}
	}
	var out []string
	for _, f := range res.Findings {
		where := "file"
		if s := files[f.File]; s != nil {
			where = declName(s, f.Offset)
		}
		out = append(out, f.File+"|"+f.Rule+"|"+where+"|"+f.Message)
	}
	slices.Sort(out)
	return out
}

// without drops the findings of rules from a finding set.
func without(set []string, rules []string) []string {
	return slices.DeleteFunc(slices.Clone(set), func(k string) bool {
		return slices.Contains(rules, strings.Split(k, "|")[1])
	})
}

// target is one program the property transforms: a source text, or a
// package folder whose files it rewrites through an overlay.
type target struct {
	name string
	src  string // a program of one source text, or ""
	dir  string // a package folder, or ""
	root string // the module root of dir
}

func (tg target) check(t *testing.T, overlay map[string]string) (lint.Input, bool) {
	t.Helper()
	if tg.dir == "" {
		text := tg.src
		if s, ok := overlay[""]; ok {
			text = s
		}
		in := lintInput(text)
		return in, in.Checked
	}
	a, err := sketchfront.CheckDirOverlay(tg.dir, overlay)
	if err != nil {
		return lint.Input{}, false
	}
	in := lint.InputOf(a)
	return in, in.Checked
}

// plantedForTransforms is a program the justifier and style rules report
// on: a silent catch, a let that widens a known variant, a boolean run in
// a condition, keyed fields and a match over a union.
const plantedForTransforms = `type Bad { Reason string }

type Green { N int, M int }

type Red { N int }

type Off { Since int }

type Light = Green | Red | Off

func parse(s string) int fails Bad {
	if s == "" {
		return Bad{Reason: "empty"}
	}
	1
}

func show(l Light, p bool, q bool) int {
	match l {
	case Green as g:
		if p && q || !p {
			return g.N
		}
		if p || q || !q {
			return g.M + 1
		}
		g.M
	case Red as r:
		r.N
	case Off as o:
		o.Since
	}
}

func main() {
	let a = parse("a") catch {
	case Bad:
		0
	}
	let g Light = Green{N: a, M: 2}
	let _ = show(g, true, false)
}
`

// TestFindingsSurviveTransforms is the invariance law of LINT-HYGIENE
// §[R2] 4 as a seeded property: a meaning-preserving change of one file
// keeps the set of findings (file, rule, declaration) of every built-in
// rule: ergo fmt and its inverses (trailing spaces, re-indent, blank
// lines), renaming a local, reordering top-level declarations, reordering
// disjoint match arms, reordering plain keyed fields and adding
// redundant parentheses, and paraphrasing comment words. A rule that reads
// words is declared sensitive to the transform that changes them
// (wordReaders); its changed share is printed, not failed. A transformed
// program must still check, and for fmt must format to the original's
// formatted text, else that case is skipped; each transform must apply at
// least once. Targets are generated programs (the complexity generator's)
// and a seeded draw of the examples' packages. Mutants:
// lhp-halstead-counts-parens, lhp-paren-breaks-run.
func TestFindingsSurviveTransforms(t *testing.T) {
	conf := allRules(t)
	var targets []target
	for _, seed := range propertySeeds(12) {
		r := rand.New(rand.NewPCG(seed, 0x1a))
		var src strings.Builder
		for i := 0; i < 1+r.IntN(3); i++ {
			src.WriteString(generateFunc(r, fmt.Sprintf("g%d", i)).text + "\n")
		}
		src.WriteString("func main() {\n}\n")
		targets = append(targets, target{name: fmt.Sprintf("generated seed %d", seed), src: src.String()})
	}
	targets = append(targets, target{name: "planted", src: plantedForTransforms})
	mods, err := filepath.Glob(filepath.Join("..", "..", "..", "examples", "*", "ergo.mod"))
	if err != nil || len(mods) == 0 {
		t.Fatalf("no example modules: %v", err)
	}
	r := rand.New(rand.NewPCG(*seedFlag+1, 0x5e))
	r.Shuffle(len(mods), func(i, j int) { mods[i], mods[j] = mods[j], mods[i] })
	for _, mod := range mods[:10] {
		root, err := filepath.Abs(filepath.Dir(mod))
		if err != nil {
			t.Fatal(err)
		}
		pkgs, err := sketchfront.DiscoverPackages(root)
		if err != nil || len(pkgs) == 0 {
			t.Fatalf("%s: %v", root, err)
		}
		p := pkgs[r.IntN(len(pkgs))]
		targets = append(targets, target{name: p.Dir, dir: p.Dir, root: root})
	}
	applied, compared := map[string]int{}, 0
	for ti, tg := range targets {
		in, ok := tg.check(t, nil)
		if !ok {
			continue
		}
		before := findingSet(in, conf.Run(in))
		var files []lint.File
		for _, u := range in.Units {
			for _, f := range u.Files {
				if f.Syntax != nil && (tg.dir == "" || filepath.Join(tg.root, f.Rel) == filepath.Join(tg.dir, filepath.Base(f.Rel))) {
					files = append(files, f)
				}
			}
		}
		info := in.Units[len(in.Units)-1].Package.Info
		for attempt := 0; attempt < 3*len(transforms) && len(files) > 0; attempt++ {
			tr := transforms[attempt%len(transforms)]
			r := rand.New(rand.NewPCG(uint64(ti)+1, uint64(attempt)))
			file := files[r.IntN(len(files))]
			text, ok := tr.apply(r, file.Syntax, info)
			if !ok || text == file.Syntax.Src {
				continue
			}
			if tr.name == "fmt" {
				a, errA := format.Source(file.Syntax.Src)
				b, errB := format.Source(text)
				// The formatter keeps a blank line, so compare without them.
				if errA != nil || errB != nil || dropBlankLines(a) != dropBlankLines(b) {
					continue
				}
			}
			key := ""
			if tg.dir != "" {
				key = filepath.Join(tg.root, file.Rel)
			}
			after, ok := tg.check(t, map[string]string{key: text})
			if !ok {
				continue
			}
			applied[tr.name]++
			compared += len(before)
			got := findingSet(after, conf.Run(after))
			sensitive := wordReaders[tr.name]
			if want, have := without(before, sensitive), without(got, sensitive); !slices.Equal(want, have) {
				t.Errorf("%s, %s of %s: the findings changed\nbefore %v\nafter  %v\nreplay: go test ./internal/sketch/lint -run TestFindingsSurviveTransforms -lint.seed=%d", tg.name, tr.name, file.Rel, want, have, *seedFlag)
			}
			if len(sensitive) > 0 && !slices.Equal(before, got) {
				t.Logf("%s, %s: a word-reading rule's findings changed (declared sensitive): %d before, %d after", tg.name, tr.name, len(before), len(got))
			}
		}
	}
	t.Logf("cases per transform: %v; findings compared: %d", applied, compared)
	for _, tr := range transforms {
		if applied[tr.name] == 0 {
			t.Errorf("transform %s applied to no target", tr.name)
		}
	}
}

// TestWordReadersListed is the residue check of the invariance table: the
// rules declared sensitive to renaming are exactly the rules whose file
// calls a word helper (nameWords, bannedWord) on names. Mutant:
// lhp-word-reader-unlisted.
func TestWordReadersListed(t *testing.T) {
	files, err := filepath.Glob("rule_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no rule files: %v", err)
	}
	var readers []string
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		calls, name := false, ""
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "nameWords" || id.Name == "bannedWord") {
					calls = true
				}
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Name" {
					if lit, ok := n.Value.(*ast.BasicLit); ok && name == "" {
						name, _ = strconv.Unquote(lit.Value)
					}
				}
			}
			return true
		})
		if calls {
			readers = append(readers, name)
		}
	}
	slices.Sort(readers)
	listed := slices.Clone(wordReaders["rename"])
	slices.Sort(listed)
	if !slices.Equal(readers, listed) {
		t.Fatalf("rules that read name words %v; wordReaders lists %v", readers, listed)
	}
}
