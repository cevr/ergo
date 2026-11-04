// Package migrate196 is the one-off translator from the Go-shaped syntax of
// 92fac236 to the decided syntax of #196 (D5). It parses old syntax with a
// frozen copy of that parser (oldsyntax), rewrites the source by edits the
// old tree locates, and prints the result with internal/sketch/format, so
// its output is canonical and keeps every comment. A form with no decided
// spelling of the same meaning is left as it was and reported as a note for
// a hand edit; the translator never emits a form whose meaning differs.
// It is deleted after v1 (#196).
package migrate196

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/migrate196/oldsyntax"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Note is a place the translator left for a hand edit.
type Note struct {
	Line int
	Text string
}

func (n Note) String() string { return fmt.Sprintf("%d: %s", n.Line, n.Text) }

// Translate rewrites one old-syntax source into the decided syntax. The
// error is non-nil only when src does not parse as old syntax. The
// translator reads its own output back: a statement whose translation does
// not parse becomes a note, never an error (guard). With notes the output
// still holds the noted old forms and may not parse; then out is the
// edited text before formatting.
func Translate(src string) (out string, notes []Note, err error) {
	t, err := newTranslator(src)
	if err != nil {
		return "", nil, err
	}
	t.run()
	t.addImports()
	out, notes = t.finish()
	return out, notes, nil
}

func newTranslator(src string) (*translator, error) {
	file, err := oldsyntax.Parse(src)
	if err != nil {
		return nil, err
	}
	return &translator{src: src, file: file, parent: map[oldsyntax.Node]oldsyntax.Node{}, imports: map[string][]oldsyntax.Pos{}}, nil
}

// finish applies the edits, guards the result and formats it.
func (t *translator) finish() (string, []Note) {
	text := t.apply()
	if len(t.notes) == 0 {
		text = t.guard(text)
	}
	formatted, ferr := format.Source(text)
	if ferr != nil && len(t.notes) == 0 {
		t.notes = append(t.notes, Note{Line: 1, Text: fmt.Sprintf("the translation cannot be formatted (%v); finish it by hand", ferr)})
	}
	sort.SliceStable(t.notes, func(i, j int) bool { return t.notes[i].Line < t.notes[j].Line })
	if len(t.notes) > 0 && ferr != nil {
		return text, t.notes
	}
	return formatted, t.notes
}

// guard parses the translation as decided syntax (round-3 review, m1: the
// translator never prints text it cannot read back). Where it does not
// parse, the innermost statement or declaration that holds the failure
// and that no rewrite crosses is written as a comment holding its old
// text, with a note at its line; then the rest is parsed again.
func (t *translator) guard(text string) string {
	units := t.units()
	for range len(units) + 1 {
		_, err := syntax.Parse(text)
		var e *syntax.Error
		if err == nil || !errors.As(err, &e) {
			return text
		}
		at := t.origin(e.Offset)
		// An error at a line break ("found newline") belongs to the text
		// before it.
		for at > 0 && at < len(t.src) && strings.ContainsRune(" \t\r\n", rune(t.src[at])) {
			at--
		}
		u := t.unitAt(units, at)
		if u == nil {
			t.note(oldsyntax.Pos(at), "the translation does not parse here (%s); finish it by hand", e.Message)
			return text
		}
		t.note(u.Pos(), "the translation of this %s does not parse (%s); its old text is left in a comment: finish it by hand", strings.TrimPrefix(kindOf(u), "the "), e.Message)
		t.textOf(u.Pos(), u.End()) // the rewrites inside it are dropped
		t.dropImports(u.Pos(), u.End())
		t.replace(u.Pos(), u.End(), t.commented(u))
		text = t.apply()
	}
	return text
}

// importWhat names the edit that imports a library package.
func importWhat(name string) string { return "the import of " + name }

// dropImports removes the import of each library package whose every use
// lies in the span the guard left by hand (round-4 review O2).
func (t *translator) dropImports(start, end oldsyntax.Pos) {
	for name, uses := range t.imports {
		if len(uses) == 0 {
			continue
		}
		uses = slices.DeleteFunc(uses, func(p oldsyntax.Pos) bool { return p >= start && p < end })
		t.imports[name] = uses
		if len(uses) == 0 {
			t.edits = slices.DeleteFunc(t.edits, func(e edit) bool { return e.what == importWhat(name) })
		}
	}
}

// units are the nodes the guard can leave by hand: the statements of a
// block or a case, and the file's declarations.
func (t *translator) units() []oldsyntax.Node {
	var units []oldsyntax.Node
	for _, d := range t.file.Decls {
		units = append(units, d)
	}
	oldsyntax.Inspect(t.file, func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.BlockStmt:
			units = append(units, nodesOf(n.List)...)
		case *oldsyntax.CaseClause:
			units = append(units, nodesOf(n.Body)...)
		case *oldsyntax.CommClause:
			units = append(units, nodesOf(n.Body)...)
		}
		return true
	})
	return units
}

// unitAt is the smallest unit that holds the source offset at and that no
// edit crosses, or nil.
func (t *translator) unitAt(units []oldsyntax.Node, at int) oldsyntax.Node {
	var best oldsyntax.Node
	for _, u := range units {
		s, e := int(u.Pos()), int(u.End())
		if at < s || at >= e && at != s {
			continue
		}
		if best != nil && e-s >= int(best.End()-best.Pos()) {
			continue
		}
		crossed := false
		for _, ed := range t.edits {
			if ed.start < s && ed.end > s || ed.start < e && ed.end > e {
				crossed = true
				break
			}
		}
		if !crossed {
			best = u
		}
	}
	return best
}

// origin maps an offset of the applied text back to the source: a place
// inside a rewrite maps to the start of the rewritten span. The edits are
// in the order apply left them.
func (t *translator) origin(off int) int {
	last, out := 0, 0
	for _, e := range t.edits {
		if e.start < last {
			continue // an overlapping edit apply left out
		}
		if n := e.start - last; off < out+n {
			return last + off - out
		} else {
			out += n
		}
		if off < out+len(e.text) {
			return e.start
		}
		out += len(e.text)
		last = e.end
	}
	return min(last+off-out, len(t.src))
}

// commented is the old text of u as line comments, after a line that
// names the hand edit.
func (t *translator) commented(u oldsyntax.Node) string {
	indent := t.indentOf(u.Pos())
	var b strings.Builder
	b.WriteString("// migrate196: finish by hand; the old text:")
	for _, line := range strings.Split(t.text(u), "\n") {
		b.WriteString("\n" + indent + "// " + strings.TrimPrefix(line, indent))
	}
	rest := t.src[u.End():]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "//") {
		b.WriteString("\n" + indent)
	}
	return b.String()
}

type edit struct {
	start, end int
	text       string
	seq        int
	what       string // the construct whose rewrite made the edit
}

type translator struct {
	src    string
	file   *oldsyntax.File
	edits  []edit
	notes  []Note
	parent map[oldsyntax.Node]oldsyntax.Node

	// methods are the names declared with a receiver; types the type names
	// declared in the file.
	methods map[string]bool
	types   map[string]bool
	ops     map[string]bool            // operation names of the file's interfaces
	fields  map[string]bool            // field names of the file's structs
	structs map[string]bool            // struct type names of the file
	fresh   map[string]bool            // names the translator added (freshName)
	imports map[string][]oldsyntax.Pos // library packages the rewrites use (task, scope), with the place of each use
	cur     string                     // the construct being rewritten, for overlap notes
	seq     int                        // the next edit's sequence number
}

// ---------------------------------------------------------------- edits and notes

func (t *translator) replace(start, end oldsyntax.Pos, text string) {
	t.edits = append(t.edits, edit{int(start), int(end), text, t.seq, t.cur})
	t.seq++
}

func (t *translator) insert(at oldsyntax.Pos, text string) { t.replace(at, at, text) }

func (t *translator) note(pos oldsyntax.Pos, format string, args ...any) {
	t.notes = append(t.notes, Note{Line: t.file.Line(pos), Text: fmt.Sprintf(format, args...)})
}

func (t *translator) text(n oldsyntax.Node) string { return t.src[n.Pos():n.End()] }

// apply performs the edits from the end of the source backwards. Edits that
// overlap are a translator bug: the later one becomes a note.
func (t *translator) apply() string {
	return t.applyEdits(0, len(t.src), t.edits)
}

// applyEdits returns src[start:end] with edits applied, in source order.
// Two edits that overlap leave the later one out, with a note that names
// both constructs (round-2 review, m3).
func (t *translator) applyEdits(start, end int, edits []edit) string {
	sort.SliceStable(edits, func(i, j int) bool {
		a, b := edits[i], edits[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if ai, bi := a.start == a.end, b.start == b.end; ai != bi {
			return ai
		}
		return a.seq < b.seq
	})
	var b strings.Builder
	last := start
	var prev edit
	for _, e := range edits {
		if e.start < last {
			line := t.file.Line(oldsyntax.Pos(e.start))
			col := e.start - strings.LastIndexByte(t.src[:e.start], '\n')
			t.note(oldsyntax.Pos(e.start), "two rewrites overlap at %d:%d (%s and %s); finish this by hand", line, col, prev.what, e.what)
			continue
		}
		b.WriteString(t.src[last:e.start])
		b.WriteString(e.text)
		last = e.end
		prev = e
	}
	b.WriteString(t.src[last:end])
	return b.String()
}

// textOf returns the translated text of src[start:end]: every edit that
// lies inside it is applied to it and leaves the file's edits. A rewrite
// that replaces a span whole takes the text of its parts this way, so the
// renames and rewrites inside them are kept, not overlapped.
func (t *translator) textOf(start, end oldsyntax.Pos) string {
	// Filtered in place: a generated source of thousands of declarations
	// calls this once per declaration, so a fresh slice each time was
	// most of the translation's cost.
	var inner []edit
	rest := t.edits[:0]
	for _, e := range t.edits {
		if e.start >= int(start) && e.end <= int(end) {
			inner = append(inner, e)
		} else {
			rest = append(rest, e)
		}
	}
	t.edits = rest
	return t.applyEdits(int(start), int(end), inner)
}

// translated rewrites the subtree n on its own, then returns its text with
// those rewrites (textOf), and marks it skipped so that the walk does not
// rewrite it again.
func (t *translator) translated(n oldsyntax.Node) string {
	if !t.isSkipped(n) {
		saved := t.cur
		oldsyntax.Inspect(n, func(c oldsyntax.Node) bool {
			t.node(c)
			return true
		})
		t.cur = saved
		t.skip(n)
	}
	return t.textOf(n.Pos(), n.End())
}

// ---------------------------------------------------------------- the walk

func (t *translator) run() {
	t.methods, t.types, t.ops, t.fields, t.structs = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	var stack []oldsyntax.Node
	oldsyntax.Inspect(t.file, func(n oldsyntax.Node) bool {
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if top.Pos() <= n.Pos() && n.End() <= top.End() {
				break
			}
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			t.parent[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	for _, d := range t.file.Decls {
		switch d := d.(type) {
		case *oldsyntax.FuncDecl:
			if d.Recv != nil {
				t.methods[d.Name.Name] = true
			}
		case *oldsyntax.TypeDecl:
			t.types[d.Name.Name] = true
			if st, ok := d.Spec.(*oldsyntax.StructType); ok {
				t.structs[d.Name.Name] = true
				for _, f := range st.Fields.List {
					for _, n := range f.Names {
						t.fields[n.Name] = true
					}
				}
			}
			if it, ok := d.Spec.(*oldsyntax.InterfaceType); ok {
				for _, m := range it.Methods {
					if len(m.Names) > 0 {
						t.ops[m.Names[0].Name] = true
					}
				}
			}
		}
	}
	t.renameKeywords()
	if t.file.Name != nil {
		end := t.file.Name.End()
		for int(end) < len(t.src) && (t.src[end] == ' ' || t.src[end] == '\t') {
			end++
		}
		if int(end) < len(t.src) && t.src[end] == '\n' {
			end++
		}
		t.replace(t.file.Package, end, "")
	}
	for _, d := range t.file.Decls {
		t.decl(d)
	}
	oldsyntax.Inspect(t.file, func(n oldsyntax.Node) bool {
		t.node(n)
		return true
	})
}

// kindOf names a construct for an overlap note: "a switch statement" from
// *oldsyntax.SwitchStmt.
func kindOf(n oldsyntax.Node) string {
	name := strings.TrimPrefix(fmt.Sprintf("%T", n), "*oldsyntax.")
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return "the" + strings.NewReplacer(" stmt", " statement", " expr", " expression", " decl", " declaration", " lit", " literal").Replace(" "+b.String())
}

func (t *translator) decl(d oldsyntax.Decl) {
	t.cur = kindOf(d)
	switch d := d.(type) {
	case *oldsyntax.ImportDecl:
		t.importDecl(d)
	case *oldsyntax.VarDecl:
		t.varDecl(d, true)
	case *oldsyntax.TypeDecl:
		t.typeDecl(d)
	case *oldsyntax.FuncDecl:
		t.funcDecl(d)
	case *oldsyntax.WireDecl:
		t.replace(d.Pos(), d.Name.Pos(), "let ")
		t.insert(d.Name.End(), " = context")
		t.contextBody(d.Body)
	}
}

func (t *translator) importDecl(d *oldsyntax.ImportDecl) {
	if d.Lparen.IsValid() {
		t.replace(d.Import, d.Lparen+1, "")
		t.replace(d.Rparen, d.Rparen+1, "")
		for _, s := range d.Specs {
			t.insert(s.Pos(), "import ")
			t.importSpec(s)
		}
		return
	}
	t.importSpec(d.Specs[0])
}

// addImports writes an import for each library package a rewrite uses
// (task for task.Go, scope for scope.OnExit): they are not in the prelude
// (round-1 decision 8). An import the file already has is kept.
func (t *translator) addImports() {
	var names []string
	for name := range t.imports {
		names = append(names, name)
	}
	sort.Strings(names)
	type spec struct {
		path string
		end  oldsyntax.Pos // the end of the spec's line
	}
	var specs []spec
	var first *oldsyntax.ImportDecl
	for _, d := range t.file.Decls {
		imp, ok := d.(*oldsyntax.ImportDecl)
		if !ok {
			continue
		}
		if first == nil {
			first = imp
		}
		for _, s := range imp.Specs {
			v, err := strconv.Unquote(s.Path.Value)
			if err != nil {
				continue
			}
			if s.Name == nil && len(t.imports[v]) > 0 {
				names = slices.DeleteFunc(names, func(n string) bool { return n == v })
			}
			end := s.End()
			for int(end) < len(t.src) && t.src[end] != '\n' {
				end++
			}
			specs = append(specs, spec{v, end})
		}
	}
	if len(names) == 0 {
		return
	}
	// Each import is its own edit, named for the guard, which drops it
	// when the guard leaves every use of the package by hand.
	defer func(saved string) { t.cur = saved }(t.cur)
	if first != nil {
		// Each import goes after the last import whose path sorts before
		// it, or before the first import.
		for _, name := range names {
			t.cur = importWhat(name)
			var after *spec
			for i := range specs {
				if specs[i].path < name {
					after = &specs[i]
				}
			}
			if after != nil {
				t.insert(after.end, "\nimport "+name)
			} else {
				t.insert(first.Import, "import "+name+"\n")
			}
		}
		return
	}
	var at oldsyntax.Pos
	switch {
	case t.file.Name != nil:
		at = t.file.Name.End()
		for int(at) < len(t.src) && t.src[at] != '\n' {
			at++
		}
		if int(at) < len(t.src) {
			at++
		}
	case len(t.file.Decls) > 0:
		at = 0
	default:
		return
	}
	for _, name := range names {
		t.cur = importWhat(name)
		t.insert(at, "import "+name+"\n")
	}
	if t.file.Name == nil {
		t.cur = "the line after the imports"
		t.insert(at, "\n")
	}
}

// stableCall reports whether a call that go or defer starts reads the same
// callee and arguments later, in a block, as it reads at once: the callee
// is a plain function name (not a local or package-level variable), and
// each argument is a literal or a local that nothing in its function,
// function literals included, writes (round-1 decisions 1 and 2).
func (t *translator) stableCall(at oldsyntax.Node, c *oldsyntax.CallExpr) bool {
	fun, ok := c.Fun.(*oldsyntax.Ident)
	if !ok || c.Ellipsis.IsValid() || t.declaredBefore(at, fun.Name) || t.packageVar(fun.Name) {
		return false
	}
	fn := t.outerFunc(at)
	for _, a := range c.Args {
		switch a := a.(type) {
		case *oldsyntax.BasicLit:
		case *oldsyntax.Ident:
			if !t.declaredBefore(at, a.Name) || t.written(fn, a.Name) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (t *translator) importSpec(s *oldsyntax.ImportSpec) {
	path, err := strconv.Unquote(s.Path.Value)
	if err != nil {
		t.note(s.Path.Pos(), "import path %s is not a string", s.Path.Value)
		return
	}
	switch s.Kind {
	case oldsyntax.ImportDot:
		t.note(s.Pos(), "dot import of %s: Ergo imports are qualified", path)
		return
	case oldsyntax.ImportNamed:
		if last := path[strings.LastIndexByte(path, '/')+1:]; last == s.Name.Name {
			t.replace(s.Name.Pos(), s.Path.Pos(), "")
		}
	}
	t.replace(s.Path.Pos(), s.Path.End(), path)
}

// ---------------------------------------------------------------- declarations

func (t *translator) varDecl(d *oldsyntax.VarDecl, top bool) {
	if d.Lparen.IsValid() {
		t.replace(d.Var, d.Lparen+1, "")
		t.replace(d.Rparen, d.Rparen+1, "")
		for _, s := range d.Specs {
			t.insert(s.Pos(), t.letWord(s.Names, top, s))
		}
		return
	}
	s := d.Specs[0]
	t.replace(d.Var, s.Pos(), t.letWord(s.Names, top, s))
	if len(s.Values) > 1 {
		t.note(s.Pos(), "a var with several values: write one let per name")
	}
}

// letWord is `let ` or `let mut ` for names declared in a function: mut
// when any is assigned or has its address taken in the function.
func (t *translator) letWord(names []*oldsyntax.Ident, top bool, at oldsyntax.Node) string {
	for _, n := range names {
		t.keywordName(n)
	}
	if top {
		return "let "
	}
	fn := t.enclosingFunc(at)
	for _, n := range names {
		if n.Name != "_" && fn != nil && t.written(fn, n.Name) {
			return "let mut "
		}
	}
	return "let "
}

// keywordName is kept for the declaration sites; renameKeywords renames
// every name the decided grammar made a keyword.
func (t *translator) keywordName(*oldsyntax.Ident) {}

// renameKeywords renames each name that the decided grammar made a keyword
// (`with`, `in`, `test`, …) to a fresh numbered name (freshName: `with2`,
// as the prototype refuses a trailing `_`), everywhere in
// the file: a consistent renaming keeps the meaning of a one-file program.
// The with form's own `with` and the `go` qualifier are not names.
func (t *translator) renameKeywords() {
	t.cur = "a keyword rename"
	var hits []*oldsyntax.Ident
	oldsyntax.Inspect(t.file, func(n oldsyntax.Node) bool {
		id, ok := n.(*oldsyntax.Ident)
		if !ok {
			return true
		}
		if !syntax.IsKeyword(id.Name) || id == t.file.Name {
			return true
		}
		switch p := t.parent[id].(type) {
		case *oldsyntax.CallExpr:
			if p.Fun == oldsyntax.Expr(id) && id.Name == "with" {
				return true
			}
		case *oldsyntax.SelectorExpr:
			if p.X == oldsyntax.Expr(id) && id.Name == "go" {
				return true
			}
		}
		hits = append(hits, id)
		return true
	})
	renamed := map[string]string{}
	for _, id := range hits {
		name, ok := renamed[id.Name]
		if !ok {
			name = t.freshName(id.Name)
		}
		renamed[id.Name] = name
		t.replace(id.Pos(), id.End(), name)
	}
}

func (t *translator) enclosingFunc(n oldsyntax.Node) oldsyntax.Node {
	for p := t.parent[n]; p != nil; p = t.parent[p] {
		switch p.(type) {
		case *oldsyntax.FuncDecl, *oldsyntax.FuncLit:
			return p
		}
	}
	return nil
}

// outerFunc is the outermost function around n: the declaration whose body
// holds every function literal that may write a local n can see.
func (t *translator) outerFunc(n oldsyntax.Node) oldsyntax.Node {
	var out oldsyntax.Node
	for f := t.enclosingFunc(n); f != nil; f = t.enclosingFunc(f) {
		out = f
	}
	return out
}

// packageVar reports whether name is declared by a package-level var.
func (t *translator) packageVar(name string) bool {
	for _, d := range t.file.Decls {
		if v, ok := d.(*oldsyntax.VarDecl); ok {
			for _, s := range v.Specs {
				for _, id := range s.Names {
					if id.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// written reports whether name is assigned, incremented or has its address
// taken anywhere in fn (a conservative test: any write to the name).
func (t *translator) written(fn oldsyntax.Node, name string) bool {
	found := false
	root := func(x oldsyntax.Expr) bool {
		for {
			switch e := x.(type) {
			case *oldsyntax.Ident:
				return e.Name == name
			case *oldsyntax.SelectorExpr:
				x = e.X
			case *oldsyntax.IndexExpr:
				x = e.X
			case *oldsyntax.ParenExpr:
				x = e.X
			default:
				return false
			}
		}
	}
	oldsyntax.Inspect(fn, func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.AssignStmt:
			if n.Tok != oldsyntax.DEFINE {
				for _, l := range n.Lhs {
					found = found || root(l)
				}
			}
		case *oldsyntax.IncDecStmt:
			found = found || root(n.X)
		case *oldsyntax.UnaryExpr:
			found = found || n.Op == oldsyntax.AND && root(n.X)
		case *oldsyntax.RangeStmt:
			if n.Tok == oldsyntax.ASSIGN {
				found = found || n.Key != nil && root(n.Key) || n.Value != nil && root(n.Value)
			}
		}
		return !found
	})
	return found
}

func (t *translator) typeDecl(d *oldsyntax.TypeDecl) {
	t.typeParams(d.TypeParams)
	if d.TypeParams == nil {
		if implicit := t.implicitParams(d.Spec); len(implicit) > 0 {
			t.insert(d.Name.End(), "["+strings.Join(implicit, ", ")+"]")
		}
	}
	switch s := d.Spec.(type) {
	case *oldsyntax.StructType:
		t.members(s.Struct, s.Fields.Opening, s.Fields.Closing, s.Fields.List)
	case *oldsyntax.InterfaceType:
		if s.Union != nil {
			parts := make([]string, len(s.Union))
			for i, x := range s.Union {
				parts[i] = t.translated(x)
			}
			t.replace(s.Interface, s.Rbrace+1, "= "+strings.Join(parts, " | "))
			t.skip(s)
			return
		}
		t.members(s.Interface, s.Lbrace, s.Rbrace, s.Methods)
	case *oldsyntax.MachineType:
		for _, e := range s.Entries {
			t.insert(e.NamePos+oldsyntax.Pos(len(e.Name)), ":")
		}
		t.separate(s.Lbrace, s.Rbrace, nodesOf(s.Entries))
	}
}

// skipped holds nodes whose text a replacement already rewrote whole.
func (t *translator) skip(n oldsyntax.Node) {
	oldsyntax.Inspect(n, func(c oldsyntax.Node) bool {
		t.parent[c] = skipped{}
		return true
	})
}

type skipped struct{ oldsyntax.Node }

func (t *translator) isSkipped(n oldsyntax.Node) bool {
	_, ok := t.parent[n].(skipped)
	return ok
}

// members turns `struct { … }` or `interface { … }` into `{ m, m }`: the
// keyword goes and the members are separated by commas.
func (t *translator) members(keyword, lbrace, rbrace oldsyntax.Pos, fields []*oldsyntax.Field) {
	t.replace(keyword, lbrace, "")
	for _, f := range fields {
		if len(f.Names) == 0 {
			t.note(f.Pos(), "an embedded member: Ergo has no embedding")
		}
	}
	t.separate(lbrace, rbrace, nodesOf(fields))
}

// separate writes a comma between consecutive items of a braced list whose
// old separators are semicolons or line breaks.
func (t *translator) separate(lbrace, rbrace oldsyntax.Pos, items []oldsyntax.Node) {
	for i, it := range items {
		next := rbrace
		if i+1 < len(items) {
			next = items[i+1].Pos()
		}
		gap := t.src[it.End():next]
		if j := strings.IndexByte(stripComments(gap), ';'); j >= 0 {
			t.replace(it.End()+oldsyntax.Pos(j), it.End()+oldsyntax.Pos(j)+1, ",")
		} else if i+1 < len(items) || strings.Contains(gap, "\n") {
			t.insert(it.End(), ",")
		}
	}
}

// stripComments blanks the comments of a gap, keeping offsets.
func stripComments(gap string) string {
	b := []byte(gap)
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '/' && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		} else if b[i] == '/' && b[i+1] == '*' {
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				b[i] = ' '
				i++
			}
		}
	}
	return string(b)
}

func nodesOf[T oldsyntax.Node](list []T) []oldsyntax.Node {
	out := make([]oldsyntax.Node, len(list))
	for i, x := range list {
		out[i] = x
	}
	return out
}

func (t *translator) typeParams(l *oldsyntax.FieldList) {
	if l == nil {
		return
	}
	isAny := func(f *oldsyntax.Field) bool {
		id, ok := f.Type.(*oldsyntax.Ident)
		return ok && id.Name == "any"
	}
	for i, f := range l.List {
		if isAny(f) {
			// `[T any]` is written `[T]`: the constraint is a
			// constraint, not `any` as a type. A group that a constrained
			// group follows would join it (`[T any, P I[T]]` read as
			// `[T, P I[T]]` constrains T too), so it is a note.
			later := false
			for _, g := range l.List[i+1:] {
				later = later || g.Type != nil && !isAny(g)
			}
			id := f.Type.(*oldsyntax.Ident)
			t.skip(id)
			if later {
				t.note(id.Pos(), "an unconstrained type parameter before a constrained one: the decided list would give it the later constraint; reorder the parameters by hand")
				continue
			}
			t.replace(f.Names[len(f.Names)-1].End(), id.End(), "")
		}
		if s, ok := f.Type.(*oldsyntax.SelectorExpr); ok && t.text(s) == "cmp.Ordered" {
			t.replace(s.Pos(), s.End(), "ordered")
		}
	}
}

// implicitParams returns the one-letter upper-case names that n uses as
// types without a declaration (the old implicit type parameters), in order.
func (t *translator) implicitParams(n oldsyntax.Node) []string {
	var out []string
	seen := map[string]bool{}
	oldsyntax.Inspect(n, func(c oldsyntax.Node) bool {
		switch c := c.(type) {
		case *oldsyntax.SelectorExpr:
			return false
		case *oldsyntax.Ident:
			if len(c.Name) == 1 && unicode.IsUpper(rune(c.Name[0])) && !t.types[c.Name] && !seen[c.Name] && t.isTypePosition(c) {
				seen[c.Name] = true
				out = append(out, c.Name)
			}
		}
		return true
	})
	return out
}

// isTypePosition reports whether an identifier stands where a type does: a
// field or parameter type, a type argument, an error row element.
func (t *translator) isTypePosition(id *oldsyntax.Ident) bool {
	switch p := t.parent[id].(type) {
	case *oldsyntax.Field:
		return p.Type == oldsyntax.Expr(id)
	case *oldsyntax.IndexExpr, *oldsyntax.ArrayType, *oldsyntax.StarExpr, *oldsyntax.MapType, *oldsyntax.ErrorRow, *oldsyntax.ChanType, *oldsyntax.TupleExpr, *oldsyntax.Ellipsis, *oldsyntax.NeedsClause:
		return true
	}
	return false
}

func (t *translator) funcDecl(d *oldsyntax.FuncDecl) {
	t.keywordName(d.Name)
	if d.Body == nil {
		t.externDecl(d)
	}
	if strings.HasPrefix(d.Name.Name, "Test") && t.testDecl(d) {
		return
	}
	t.lawDecl(d)
	t.typeParams(d.TypeParams)
	// The old checker made every otherwise unbound one-letter type name an
	// implicit parameter, beside a bracket list too; the decided list
	// declares each one.
	implicit := t.implicitParams(d.Type)
	if d.Recv != nil {
		implicit = append(t.implicitParams(d.Recv), implicit...)
	}
	implicit = unique(implicit)
	if d.TypeParams != nil {
		declared := map[string]bool{}
		for _, f := range d.TypeParams.List {
			for _, n := range f.Names {
				declared[n.Name] = true
			}
		}
		var rest []string
		for _, name := range implicit {
			if !declared[name] {
				rest = append(rest, name)
			}
		}
		implicit = rest
	}
	if d.Recv != nil {
		r := d.Recv.List[0]
		// The receiver moves; its name and type keep their renames.
		recv := t.textOf(r.Type.Pos(), r.Type.End())
		// A parameter list is all named or all unnamed: an unnamed
		// receiver before unnamed parameters stays unnamed, and a named
		// one names them _.
		params := d.Type.Params.List
		unnamed := len(params) > 0 && len(params[0].Names) == 0
		switch {
		case len(r.Names) > 0:
			recv = t.textOf(r.Names[0].Pos(), r.Names[0].End()) + " " + recv
			if unnamed {
				// The first one shares the receiver's insertion point.
				recv += ", _"
				for _, p := range params[1:] {
					t.insert(p.Type.Pos(), "_ ")
				}
			}
		case !unnamed:
			recv = "_ " + recv
		}
		t.textOf(d.Recv.Opening, d.Name.Pos())
		// `func(r R)M()` keeps a blank between func and the name.
		gap := ""
		if o := int(d.Recv.Opening); o > 0 && t.src[o-1] != ' ' && t.src[o-1] != '\t' {
			gap = " "
		}
		t.replace(d.Recv.Opening, d.Name.Pos(), gap)
		sep := ", "
		if len(d.Type.Params.List) == 0 {
			sep = ""
		}
		if len(r.Names) > 0 && unnamed {
			sep = " "
		}
		t.insert(d.Type.Params.Opening+1, recv+sep)
	}
	switch {
	case len(implicit) == 0:
	case d.TypeParams == nil:
		t.insert(d.Name.End(), "["+strings.Join(implicit, ", ")+"]")
	default:
		t.insert(d.TypeParams.Closing, ", "+strings.Join(implicit, ", "))
	}
}

func unique(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// externDecl turns `//ef:go pkg.F` and a bodiless function into `extern go
// pkg.F func …`.
func (t *translator) externDecl(d *oldsyntax.FuncDecl) {
	var directive *oldsyntax.Comment
	for _, g := range t.file.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//ef:go ") && c.EndPos <= d.Func && t.file.Line(c.Slash) >= t.file.Line(d.Func)-1 {
				directive = c
			}
		}
	}
	if directive == nil {
		t.note(d.Pos(), "a function without a body: give it one, or bind it with extern go")
		return
	}
	target := strings.TrimSpace(strings.TrimPrefix(directive.Text, "//ef:go "))
	end := directive.EndPos
	if int(end) < len(t.src) && t.src[end] == '\n' {
		end++
	}
	t.replace(directive.Slash, end, "")
	t.insert(d.Func, "extern go "+target+" ")
}

// grouping returns the parentheses an operand needs to stay one operand
// next to a binary operator of precedence prec: a pair for a binary
// expression that binds no tighter, and for a try or catch expression
// (each covers everything to its right); none otherwise.
func grouping(x oldsyntax.Expr, prec int) (string, string) {
	switch x := x.(type) {
	case *oldsyntax.BinaryExpr:
		if x.Op.Precedence() <= prec {
			return "(", ")"
		}
	case *oldsyntax.TryExpr, *oldsyntax.CatchExpr:
		return "(", ")"
	}
	return "", ""
}

// lawDecl notes a law function, `func LawX(…) bool`: a decided law lives in
// its operation type as `law X(…) { … }`, ends in a bool expression instead
// of a return, and the prototype refuses laws until #162 (round-1 review,
// M13). The move changes the declaration's home and its body, so it is a
// hand edit.
func (t *translator) lawDecl(d *oldsyntax.FuncDecl) {
	name := d.Name.Name
	if len(name) <= 3 || !strings.HasPrefix(name, "Law") || !unicode.IsUpper(rune(name[3])) || d.Recv != nil {
		return
	}
	r := d.Type.Results
	if r == nil || len(r.List) != 1 || len(r.List[0].Names) != 0 {
		return
	}
	if id, ok := r.List[0].Type.(*oldsyntax.Ident); !ok || id.Name != "bool" {
		return
	}
	t.note(d.Pos(), "a law function %s: move it into its operation type as law %s(…) { … } by hand (laws come with #162)", name, strings.TrimPrefix(name, "Law"))
}

// testDecl turns `func TestX() needs testing.T { … }` into `test X { … }`
// when every use of T is T.Equal(a, b) or T.True(c).
func (t *translator) testDecl(d *oldsyntax.FuncDecl) bool {
	ty := d.Type
	if d.Recv != nil || d.TypeParams != nil || len(ty.Params.List) != 0 || ty.Needs == nil || len(ty.Needs.Names) != 1 || t.text(ty.Needs.Names[0]) != "testing.T" {
		return false
	}
	ok := true
	var edits []edit
	oldsyntax.Inspect(d.Body, func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.CallExpr:
			sel, isSel := n.Fun.(*oldsyntax.SelectorExpr)
			if !isSel || t.text(sel.X) != "T" {
				return true
			}
			switch {
			case sel.Sel.Name == "Equal" && len(n.Args) == 2:
				// Each operand keeps its own tree: `T.Equal(a, b || c)`
				// is `assert a == (b || c)`, never `a == b || c` (M3).
				o0, c0 := grouping(n.Args[0], oldsyntax.EQL.Precedence())
				o1, c1 := grouping(n.Args[1], oldsyntax.EQL.Precedence())
				edits = append(edits, edit{int(n.Pos()), int(n.Args[0].Pos()), "assert " + o0, 0, ""}, edit{int(n.Args[0].End()), int(n.Args[1].Pos()), c0 + " == " + o1, 0, ""}, edit{int(n.Rparen), int(n.Rparen) + 1, c1, 0, ""})
			case sel.Sel.Name == "True" && len(n.Args) == 1:
				edits = append(edits, edit{int(n.Pos()), int(n.Args[0].Pos()), "assert ", 0, ""}, edit{int(n.Rparen), int(n.Rparen) + 1, "", 0, ""})
			default:
				ok = false
			}
			return false
		case *oldsyntax.Ident:
			if n.Name == "T" {
				ok = false
			}
		}
		return true
	})
	if !ok {
		t.note(d.Pos(), "test %s uses testing.T beyond T.Equal and T.True: rewrite it with assert", d.Name.Name)
		return false
	}
	if ty.Results != nil {
		// A test with only a failure row: a failure that escapes the body
		// fails the test, in Go (a returned error) and in Ergo alike.
		if _, row := ty.Results.List[0].Type.(*oldsyntax.ErrorRow); !row || len(ty.Results.List) != 1 {
			t.note(d.Pos(), "test %s has a result: a test fails through assert", d.Name.Name)
			return false
		}
	}
	t.replace(d.Func, d.Name.Pos(), "test ")
	t.replace(d.Name.Pos(), d.Name.End(), strings.TrimPrefix(d.Name.Name, "Test"))
	t.replace(ty.Params.Opening, ty.Needs.End(), "")
	t.skip(ty)
	for _, e := range edits {
		t.replace(oldsyntax.Pos(e.start), oldsyntax.Pos(e.end), e.text)
	}
	return true
}

// ---------------------------------------------------------------- nodes

func (t *translator) node(n oldsyntax.Node) {
	if t.isSkipped(n) {
		return
	}
	t.cur = kindOf(n)
	switch n := n.(type) {
	case *oldsyntax.Field:
		for _, id := range n.Names {
			t.keywordName(id)
		}
	case *oldsyntax.FuncType:
		t.results(n)
	case *oldsyntax.NeedsClause:
		t.row(n.Lparen, n.Names, n.Rparen)
	case *oldsyntax.ProvidesClause:
		t.row(oldsyntax.NoPos, n.Names, oldsyntax.NoPos)
	case *oldsyntax.MapType:
		// Go parses `map[K]V{…}` in a control clause header; the decided
		// `Map[K, V]{…}` is a type name there, so the literal needs
		// parentheses (round-3 review, m1).
		lit, isLit := t.parent[n].(*oldsyntax.CompositeLit)
		paren := isLit && lit.Type == n && t.inHeader(lit)
		if paren {
			t.insert(lit.Pos(), "(")
		}
		t.replace(n.Map, n.Key.Pos(), "Map[")
		t.replace(n.Key.End(), n.Value.Pos(), ", ")
		t.insert(n.Value.End(), "]")
		if paren {
			t.insert(lit.End(), ")")
		}
	case *oldsyntax.StructType:
		if _, isDecl := t.parent[n].(*oldsyntax.TypeDecl); !isDecl {
			t.note(n.Pos(), "an anonymous struct: declare a named record type")
		}
	case *oldsyntax.InterfaceType:
		if _, isDecl := t.parent[n].(*oldsyntax.TypeDecl); !isDecl {
			t.note(n.Pos(), "an anonymous interface: declare a named type")
		}
	case *oldsyntax.ChanType:
		t.note(n.Pos(), "a channel: Ergo has no channels; use a typed queue")
	case *oldsyntax.ArrayType:
		if n.Len != nil {
			t.note(n.Pos(), "a fixed array: use a slice")
		}
	case *oldsyntax.Ellipsis:
		t.note(n.Pos(), "a variadic parameter: take a slice")
	case *oldsyntax.SelectStmt:
		t.note(n.Pos(), "select: race tasks with task.Race")
	case *oldsyntax.SendStmt:
		t.note(n.Pos(), "a channel send: use a typed queue")
	case *oldsyntax.LabeledStmt:
		t.note(n.Pos(), "a label: Ergo has no labels")
	case *oldsyntax.BranchStmt:
		t.branch(n)
	case *oldsyntax.UnaryExpr:
		if n.Op == oldsyntax.ARROW {
			t.note(n.Pos(), "a channel receive: use a typed queue")
		}
		t.tryOperands(n)
	case *oldsyntax.Ident:
		if n.Name == "any" && t.isTypePosition(n) {
			t.note(n.Pos(), "any as a type: name the type")
		}
	case *oldsyntax.SelectorExpr:
		t.methodValue(n)
	case *oldsyntax.CallExpr:
		t.call(n)
	case *oldsyntax.WireLit:
		// The old keyword is four bytes long; the decided one is context.
		t.replace(n.Pos(), n.Pos()+4, "context")
		t.contextBody(n.Body)
	case *oldsyntax.BinaryExpr:
		t.tryOperands(n)
	case *oldsyntax.CatchExpr:
		t.catch(n)
	case *oldsyntax.GoExpr:
		// `go { … }` and `task.Go() { … }` both run the block as a task.
		// `go f(args)` evaluates the arguments at once and the block
		// later, so the call is rewritten only when that cannot differ.
		switch {
		case n.Block != nil:
			t.replace(n.Go, n.Block.Pos(), "task.Go() ")
		case t.stableCall(n, n.Call):
			t.replace(n.Go, n.Call.Pos(), "task.Go() { ")
			t.insert(n.Call.End(), " }")
		default:
			t.note(n.Pos(), "go with arguments that may change before the task runs: start it with task.Go() { … } by hand")
			return
		}
		t.imports["task"] = append(t.imports["task"], n.Pos())
	case *oldsyntax.DeferStmt:
		// scope.OnExit at a function's top level runs when the function
		// returns, last registered first, as Go's defer does; its block
		// reads the arguments then, so they must not change before.
		call, isCall := n.Stmt.(*oldsyntax.ExprStmt)
		block, inBlock := t.parent[n].(*oldsyntax.BlockStmt)
		top := false
		if inBlock {
			switch fn := t.parent[block].(type) {
			case *oldsyntax.FuncDecl:
				top = fn.Body == block
			case *oldsyntax.FuncLit:
				top = fn.Body == block
			}
		}
		var c *oldsyntax.CallExpr
		if isCall {
			c, _ = call.X.(*oldsyntax.CallExpr)
		}
		if !top || c == nil || !t.stableCall(n, c) {
			t.note(n.Pos(), "a defer that is not a call with stable arguments at a function's top level: register it with scope.OnExit() { … } by hand")
			return
		}
		t.replace(n.Defer, n.Stmt.Pos(), "scope.OnExit() { ")
		t.insert(n.Stmt.End(), " }")
		t.imports["scope"] = append(t.imports["scope"], n.Pos())
	case *oldsyntax.DeclStmt:
		t.varDecl(n.Decl, false)
	case *oldsyntax.AssignStmt:
		t.assign(n)
	case *oldsyntax.IncDecStmt:
		if _, isFor := t.parent[n].(*oldsyntax.ForStmt); !isFor {
			t.replace(n.TokPos, n.TokPos+2, map[oldsyntax.Token]string{oldsyntax.INC: " += 1", oldsyntax.DEC: " -= 1"}[n.Tok])
		}
	case *oldsyntax.ReturnStmt:
		if len(n.Results) > 1 {
			t.insert(n.Results[0].Pos(), "(")
			t.insert(n.Results[len(n.Results)-1].End(), ")")
		}
	case *oldsyntax.IfStmt:
		if n.Init != nil {
			if parent, ok := t.parent[n].(*oldsyntax.IfStmt); ok && parent.Else == oldsyntax.Stmt(n) {
				// The init of an else-if runs only when the earlier
				// conditions fail; a let before the chain would run it
				// always (round-1 review, m1).
				t.note(n.Init.Pos(), "an else-if with an init statement: nest the if inside the else by hand")
			} else {
				t.hoistInit(n.If, n.Init, n.Cond.Pos())
			}
		}
	case *oldsyntax.ForStmt:
		t.forStmt(n)
	case *oldsyntax.RangeStmt:
		t.rangeStmt(n)
	case *oldsyntax.SwitchStmt:
		t.switchStmt(n)
	case *oldsyntax.TypeSwitchStmt:
		t.typeSwitch(n)
	}
}

// results rewrites `(T, error[E])` to `T fails E` and `error[E]` to
// `fails E`, and notes named results.
func (t *translator) results(ft *oldsyntax.FuncType) {
	r := ft.Results
	if r == nil {
		return
	}
	var row *oldsyntax.ErrorRow
	var types []oldsyntax.Expr
	for _, f := range r.List {
		if len(f.Names) > 0 {
			t.note(f.Pos(), "a named result: return the value")
			return
		}
		if e, ok := f.Type.(*oldsyntax.ErrorRow); ok {
			row = e
			continue
		}
		types = append(types, f.Type)
	}
	if row == nil {
		return
	}
	rowText := t.rowText(row)
	switch {
	case !r.Opening.IsValid():
		t.replace(row.Pos(), row.End(), rowText)
	case len(types) == 0:
		t.replace(r.Opening, r.Closing+1, rowText)
	case len(types) == 1:
		t.replace(r.Opening, types[0].Pos(), "")
		t.replace(types[0].End(), r.Closing+1, " "+rowText)
	default:
		last := types[len(types)-1]
		t.replace(last.End(), r.Closing, ")")
		t.replace(r.Closing, r.Closing+1, " "+rowText)
	}
}

// row respells an old capability row, its names separated by commas and
// maybe parenthesised, as the decided union `needs A | B` (196n). It edits
// only the comma and parenthesis tokens, so the comments between the names
// stay where they were (TestRowCommentsKept); the formatter sets the spaces.
func (t *translator) row(lparen oldsyntax.Pos, names []oldsyntax.Expr, rparen oldsyntax.Pos) {
	if lparen.IsValid() {
		t.replace(lparen, lparen+1, " ")
		t.replace(rparen, rparen+1, " ")
	}
	for i := 1; i < len(names); i++ {
		from := names[i-1].End()
		comma := from + oldsyntax.Pos(strings.IndexByte(stripComments(t.src[from:names[i].Pos()]), ','))
		t.replace(comma, comma+1, " | ")
	}
}

func (t *translator) rowText(row *oldsyntax.ErrorRow) string {
	parts := make([]string, len(row.Types))
	for i, x := range row.Types {
		parts[i] = t.text(x)
	}
	return "fails " + strings.Join(parts, " | ")
}

// methodValue notes a method named without a call: a method value has no
// decided spelling of the same meaning.
func (t *translator) methodValue(s *oldsyntax.SelectorExpr) {
	if !t.methods[s.Sel.Name] || t.fields[s.Sel.Name] {
		return
	}
	if c, ok := t.parent[s].(*oldsyntax.CallExpr); ok && c.Fun == oldsyntax.Expr(s) {
		return
	}
	if id, ok := s.X.(*oldsyntax.Ident); ok && t.types[id.Name] {
		return
	}
	t.note(s.Pos(), "a method value %s: write a function literal", t.text(s))
}

func (t *translator) call(c *oldsyntax.CallExpr) {
	if c.Ellipsis.IsValid() {
		t.note(c.Ellipsis, "a spread argument: pass the slice")
		return
	}
	if s, ok := c.Fun.(*oldsyntax.SelectorExpr); ok && t.text(s) == "Console.Println" {
		t.println(c)
		return
	}
	id, ok := c.Fun.(*oldsyntax.Ident)
	if !ok {
		return
	}
	switch id.Name {
	case "with":
		// A context written as a composite literal keeps parentheses: in
		// the decided header its brace would open the body (D1.8).
		contexts := c.Args
		if c.Trailing == nil && len(c.Args) >= 2 {
			contexts = c.Args[:len(c.Args)-1]
		}
		for _, w := range contexts {
			if bareLiteral(w) {
				t.insert(w.Pos(), "(")
				t.insert(w.End(), ")")
			}
		}
		switch {
		case c.Trailing != nil:
			t.replace(c.Lparen, c.Lparen+1, " ")
			t.replace(c.Rparen, c.Rparen+1, "")
		case len(c.Args) >= 2:
			// with(W, f) runs f under W: `with W { f() }`.
			n := len(c.Args)
			t.replace(c.Lparen, c.Lparen+1, " ")
			t.replace(c.Args[n-2].End(), c.Args[n-1].Pos(), " { ")
			if _, lit := c.Args[n-1].(*oldsyntax.FuncLit); lit {
				t.insert(c.Args[n-1].Pos(), "(")
				t.replace(c.Rparen, c.Rparen+1, ")() }")
			} else {
				t.replace(c.Rparen, c.Rparen+1, "() }")
			}
		default:
			t.note(c.Pos(), "with without a body")
		}
	case "append":
		if len(c.Args) < 2 {
			t.note(c.Pos(), "append without elements")
			return
		}
		t.replace(c.Pos(), c.Args[0].Pos(), "")
		for i := 1; i < len(c.Args); i++ {
			sep := ").Append("
			if i == 1 {
				sep = ".Append("
			}
			t.replace(c.Args[i-1].End(), c.Args[i].Pos(), sep)
		}
	case "delete":
		if len(c.Args) != 2 {
			return
		}
		t.replace(c.Pos(), c.Args[0].Pos(), "")
		t.replace(c.Args[0].End(), c.Args[1].Pos(), ".Delete(")
	case "make", "new", "cap", "copy", "close", "print", "println":
		t.note(c.Pos(), "%s has no decided form with the same meaning", id.Name)
	}
}

func (t *translator) contextBody(b *oldsyntax.WireBody) {
	for _, e := range b.Entries {
		if !e.Assign.IsValid() {
			continue
		}
		if t.src[e.Assign-1] == ' ' {
			t.replace(e.Assign-1, e.Assign+1, ":")
		} else {
			t.replace(e.Assign, e.Assign+1, ":")
		}
	}
	// A decided comma run of names ended by `:` is one entry's key list,
	// whatever the layout (grammar.md, Parser rule 9), so an embed written
	// just before an entry would join its keys. Entry order has no
	// meaning: every embed that precedes an entry moves after the last
	// entry (round-1 review, M11).
	lastEntry := -1
	for i, e := range b.Entries {
		if e.Kind != oldsyntax.WireEmbed {
			lastEntry = i
		}
	}
	var kept []*oldsyntax.WireEntry
	var moved []string
	for i, e := range b.Entries {
		if e.Kind != oldsyntax.WireEmbed || i > lastEntry {
			kept = append(kept, e)
			continue
		}
		next := b.Entries[i+1]
		end := next.Pos()
		if next.Doc != nil {
			end = next.Doc.Pos()
		}
		if e.Doc != nil || strings.Contains(t.src[e.End():end], "/") {
			t.note(e.Pos(), "an embed with a comment before an entry: move %s after the entries by hand", t.text(e.Key))
			kept = append(kept, e)
			continue
		}
		t.replace(e.Pos(), end, "")
		moved = append(moved, t.text(e.Key))
	}
	t.separate(b.Lbrace, b.Rbrace, nodesOf(kept))
	if len(moved) == 0 {
		return
	}
	tail := t.src[b.Entries[len(b.Entries)-1].End():b.Rbrace]
	if nl := strings.LastIndexByte(tail, '\n'); nl >= 0 {
		at := b.Entries[len(b.Entries)-1].End() + oldsyntax.Pos(nl) + 1
		t.insert(at, strings.Join(moved, ",\n")+",\n")
	} else {
		t.insert(b.Rbrace, ", "+strings.Join(moved, ", ")+" ")
	}
}

// tryOperands moves a try in operand position to the start of the binary
// expression: old try covered the rest of the binary expression to its
// right, decided try covers the whole expression (sixth round).
func (t *translator) tryOperands(b oldsyntax.Expr) {
	switch t.parent[b].(type) {
	case *oldsyntax.BinaryExpr, *oldsyntax.UnaryExpr:
		return
	}
	var found []*oldsyntax.TryExpr
	var walk func(x oldsyntax.Expr)
	walk = func(x oldsyntax.Expr) {
		switch x := x.(type) {
		case *oldsyntax.BinaryExpr:
			walk(x.X)
			walk(x.Y)
		case *oldsyntax.TryExpr:
			found = append(found, x)
		case *oldsyntax.UnaryExpr:
			walk(x.X)
		}
	}
	walk(b)
	if len(found) == 0 {
		return
	}
	if _, already := t.parent[b].(*oldsyntax.TryExpr); !already {
		t.insert(b.Pos(), "try ")
	}
	for _, x := range found {
		t.replace(x.Try, x.X.Pos(), "")
	}
}

// catch drops the catch binding: a single-type arm that reads it binds the
// narrowed value with `as`.
func (t *translator) catch(c *oldsyntax.CatchExpr) {
	if c.Bind == nil {
		return
	}
	name := t.textOf(c.Bind.Pos(), c.Bind.End()) // the binding keeps a rename
	t.replace(c.Bind.Pos()-1, c.Bind.End(), "")
	for _, a := range c.Arms {
		if !t.reads(a.Body, c.Bind.Name) {
			continue
		}
		if len(a.Types) != 1 {
			t.note(a.Case, "a catch arm with several types reads %s", c.Bind.Name)
			continue
		}
		t.insert(a.Types[0].End(), " as "+name)
	}
}

// reads reports whether name occurs in the statements.
func (t *translator) reads(list []oldsyntax.Stmt, name string) bool {
	found := false
	for _, s := range list {
		oldsyntax.Inspect(s, func(n oldsyntax.Node) bool {
			if id, ok := n.(*oldsyntax.Ident); ok && id.Name == name && t.identRole(id) != "field" {
				found = true
			}
			return !found
		})
	}
	return found
}

// identRole tells what an identifier is where it stands: "field" for a
// selector's field or a key of a literal of a struct type the file
// declares; "read" for a map or slice literal's key and any other use;
// "ambiguous" for a key of a literal whose type the file does not show (an
// elided type, an imported or a defined type), which may be a field or a
// map key read.
func (t *translator) identRole(id *oldsyntax.Ident) string {
	switch p := t.parent[id].(type) {
	case *oldsyntax.SelectorExpr:
		if p.Sel == id {
			return "field"
		}
	case *oldsyntax.KeyValueExpr:
		lit, ok := t.parent[p].(*oldsyntax.CompositeLit)
		if !ok || p.Key != oldsyntax.Expr(id) {
			break
		}
		ty := lit.Type
		if ix, ok := ty.(*oldsyntax.IndexExpr); ok {
			ty = ix.X
		}
		switch ty := ty.(type) {
		case *oldsyntax.MapType, *oldsyntax.ArrayType:
			return "read"
		case *oldsyntax.Ident:
			if t.structs[ty.Name] {
				return "field"
			}
		}
		return "ambiguous"
	}
	return "read"
}

// renameReads makes the reads of a type-switch binding in a multi-type or
// default arm read the subject instead (M7). It proves the two equal or
// writes a note: the subject must be a local or parameter that nothing in
// its function writes (the binding is a snapshot taken at the switch), the
// arm must not declare either name (a read would resolve to the new one),
// and no read may be a key of a literal whose type the file does not show.
func (t *translator) renameReads(sw oldsyntax.Node, at oldsyntax.Pos, list []oldsyntax.Stmt, from, to, toText string) {
	if !t.declaredBefore(sw, to) || t.written(t.outerFunc(sw), to) {
		t.note(at, "an arm with several types reads %s: bind the subject with let first", from)
		return
	}
	for _, s := range list {
		if t.written(s, from) || t.declares(s, from) || t.declares(s, to) {
			t.note(at, "an arm with several types reads %s: bind the subject with let first", from)
			return
		}
	}
	var ids []*oldsyntax.Ident
	for _, s := range list {
		oldsyntax.Inspect(s, func(n oldsyntax.Node) bool {
			if id, ok := n.(*oldsyntax.Ident); ok && id.Name == from {
				ids = append(ids, id)
			}
			return true
		})
	}
	for _, id := range ids {
		if t.identRole(id) == "ambiguous" {
			t.note(at, "an arm with several types reads %s: bind the subject with let first", from)
			return
		}
	}
	for _, id := range ids {
		if t.identRole(id) == "read" {
			t.textOf(id.Pos(), id.End()) // a rename of the binding gives way
			t.replace(id.Pos(), id.End(), toText)
		}
	}
}

// declares reports whether n declares name: a define, a var, a range
// variable, a function literal's parameter or a nested switch binding.
func (t *translator) declares(n oldsyntax.Node, name string) bool {
	found := false
	is := func(x oldsyntax.Expr) {
		if id, ok := x.(*oldsyntax.Ident); ok && id.Name == name {
			found = true
		}
	}
	oldsyntax.Inspect(n, func(m oldsyntax.Node) bool {
		switch m := m.(type) {
		case *oldsyntax.AssignStmt:
			if m.Tok == oldsyntax.DEFINE {
				for _, l := range m.Lhs {
					is(l)
				}
			}
		case *oldsyntax.VarSpec:
			for _, id := range m.Names {
				is(id)
			}
		case *oldsyntax.RangeStmt:
			is(m.Key)
			is(m.Value)
		case *oldsyntax.FuncLit:
			for _, f := range m.Type.Params.List {
				for _, id := range f.Names {
					is(id)
				}
			}
		case *oldsyntax.TypeSwitchStmt:
			for _, id := range m.Binds {
				is(id)
			}
		case *oldsyntax.CatchExpr:
			if m.Bind != nil {
				is(m.Bind)
			}
		}
		return !found
	})
	return found
}
func (t *translator) branch(b *oldsyntax.BranchStmt) {
	if b.Label != nil {
		t.note(b.Pos(), "a labelled %s: Ergo has no labels", b.Tok)
		return
	}
	if b.Tok != oldsyntax.BREAK {
		return
	}
	for p := t.parent[b]; p != nil; p = t.parent[p] {
		switch p.(type) {
		case *oldsyntax.ForStmt, *oldsyntax.RangeStmt, *oldsyntax.FuncLit, *oldsyntax.FuncDecl:
			return
		case *oldsyntax.SwitchStmt, *oldsyntax.TypeSwitchStmt, *oldsyntax.SelectStmt:
			t.note(b.Pos(), "break inside a switch arm leaves the switch in Go and the loop in Ergo")
			return
		}
	}
}

// assign rewrites `x := e` to `let [mut] x = e`, and notes the forms with
// no decided spelling: redeclaration, comma-ok, several assignments.
func (t *translator) assign(a *oldsyntax.AssignStmt) {
	switch p := t.parent[a].(type) {
	case *oldsyntax.ForStmt:
		if p.Init == oldsyntax.Stmt(a) || p.Post == oldsyntax.Stmt(a) {
			return
		}
	case *oldsyntax.IfStmt, *oldsyntax.SwitchStmt, *oldsyntax.TypeSwitchStmt:
		return // hoisted by the statement
	}
	if a.Tok != oldsyntax.DEFINE {
		if len(a.Lhs) > 1 {
			t.note(a.Pos(), "an assignment of several values: assign one name per statement")
		}
		return
	}
	t.define(a)
}

func (t *translator) define(a *oldsyntax.AssignStmt) {
	var names []*oldsyntax.Ident
	for _, l := range a.Lhs {
		id, ok := l.(*oldsyntax.Ident)
		if !ok {
			t.note(a.Pos(), "a := with a non-name on the left")
			return
		}
		names = append(names, id)
	}
	for _, id := range names {
		if id.Name != "_" && t.declaredBefore(a, id.Name) && !t.renameShadow(a, id) {
			t.note(id.Pos(), ":= redeclares %s: Ergo has no shadowing; pick a new name", id.Name)
			return
		}
	}
	if len(names) == 2 && len(a.Rhs) == 1 {
		switch r := a.Rhs[0].(type) {
		case *oldsyntax.IndexExpr, *oldsyntax.TypeGuard:
			t.note(a.Pos(), "a comma-ok form: match on the value")
			return
		case *oldsyntax.UnaryExpr:
			if r.Op == oldsyntax.ARROW {
				return
			}
		}
	}
	t.insert(a.Lhs[0].Pos(), t.letWord(names, false, a))
	t.replace(a.TokPos, a.TokPos+2, "=")
	if len(a.Rhs) > 1 {
		t.insert(a.Rhs[0].Pos(), "(")
		t.insert(a.Rhs[len(a.Rhs)-1].End(), ")")
	}
}

// renameShadow renames a := that shadows a name of an enclosing block: the
// new name replaces the declared one in the rest of its block, which is
// exactly its scope. It refuses (false) a redeclaration in the same block,
// which Go reads as an assignment, and a block that declares the name again.
func (t *translator) renameShadow(a *oldsyntax.AssignStmt, id *oldsyntax.Ident) bool {
	var list []oldsyntax.Stmt
	switch p := t.parent[a].(type) {
	case *oldsyntax.BlockStmt:
		list = p.List
	case *oldsyntax.CaseClause:
		list = p.Body
	case *oldsyntax.CatchArm:
		list = p.Body
	default:
		return false
	}
	k := -1
	for i, s := range list {
		if s == oldsyntax.Stmt(a) {
			k = i
		}
	}
	if k < 0 {
		return false
	}
	declares := func(s oldsyntax.Node) bool {
		found := false
		oldsyntax.Inspect(s, func(n oldsyntax.Node) bool {
			switch n := n.(type) {
			case *oldsyntax.AssignStmt:
				if n.Tok == oldsyntax.DEFINE {
					for _, l := range n.Lhs {
						if x, ok := l.(*oldsyntax.Ident); ok && x.Name == id.Name && x != id {
							found = true
						}
					}
				}
			case *oldsyntax.VarSpec:
				for _, x := range n.Names {
					found = found || x.Name == id.Name
				}
			case *oldsyntax.Field:
				for _, x := range n.Names {
					found = found || x.Name == id.Name
				}
			}
			return !found
		})
		return found
	}
	for _, s := range list[:k] {
		if declares(s) {
			return false
		}
	}
	for _, s := range list[k+1:] {
		if declares(s) {
			return false
		}
	}
	// The new name comes from the one table of fresh names, so it never
	// meets a name another rewrite added (round-2 review, M2).
	saved := t.cur
	t.cur = "a shadow rename"
	defer func() { t.cur = saved }()
	name := t.freshName(id.Name)
	t.replace(id.Pos(), id.End(), name)
	for _, s := range list[k+1:] {
		oldsyntax.Inspect(s, func(n oldsyntax.Node) bool {
			x, ok := n.(*oldsyntax.Ident)
			if !ok || x.Name != id.Name {
				return true
			}
			switch p := t.parent[x].(type) {
			case *oldsyntax.SelectorExpr:
				if p.Sel == x {
					return true
				}
			case *oldsyntax.KeyValueExpr:
				if _, lit := t.parent[p].(*oldsyntax.CompositeLit); lit && p.Key == oldsyntax.Expr(x) {
					return true
				}
			}
			t.replace(x.Pos(), x.End(), name)
			return true
		})
	}
	return true
}

// declaredBefore reports whether name is declared in the function before
// at: a parameter, receiver, earlier := or var, or a range variable.
func (t *translator) declaredBefore(at oldsyntax.Node, name string) bool {
	fn := t.enclosingFunc(at)
	if fn == nil {
		return false
	}
	found := false
	for f := fn; f != nil && !found; f = t.enclosingFunc(f) {
		oldsyntax.Inspect(f, func(n oldsyntax.Node) bool {
			if n.Pos() >= at.Pos() {
				return false
			}
			switch n := n.(type) {
			case *oldsyntax.Field:
				for _, id := range n.Names {
					found = found || id.Name == name
				}
			case *oldsyntax.AssignStmt:
				if n.Tok == oldsyntax.DEFINE {
					for _, l := range n.Lhs {
						if id, ok := l.(*oldsyntax.Ident); ok && id.Name == name {
							found = true
						}
					}
				}
			case *oldsyntax.VarSpec:
				for _, id := range n.Names {
					found = found || id.Name == name
				}
			case *oldsyntax.RangeStmt:
				for _, x := range []oldsyntax.Expr{n.Key, n.Value} {
					if id, ok := x.(*oldsyntax.Ident); ok && id.Name == name && n.Tok == oldsyntax.DEFINE {
						found = true
					}
				}
			}
			return !found
		})
	}
	return found
}

// hoistInit writes an if or switch init statement as a let on the line
// before the statement.
func (t *translator) hoistInit(stmtPos oldsyntax.Pos, init oldsyntax.Stmt, next oldsyntax.Pos) {
	a, ok := init.(*oldsyntax.AssignStmt)
	if !ok || a.Tok != oldsyntax.DEFINE || len(a.Rhs) != 1 {
		t.note(init.Pos(), "an init statement: write it on the line before")
		return
	}
	for _, l := range a.Lhs {
		if id, ok := l.(*oldsyntax.Ident); !ok || t.declaredBefore(a, id.Name) || t.usedAfter(t.parent[a], id.Name) {
			t.note(init.Pos(), "an init statement whose name clashes: write the let by hand")
			return
		}
	}
	if _, isIndex := a.Rhs[0].(*oldsyntax.IndexExpr); isIndex && len(a.Lhs) == 2 {
		t.note(init.Pos(), "a comma-ok form: match on the value")
		return
	}
	names := make([]*oldsyntax.Ident, len(a.Lhs))
	for i, l := range a.Lhs {
		names[i] = l.(*oldsyntax.Ident)
	}
	// The names and the value keep their own rewrites (round-2 review,
	// m2): the init's span is replaced whole.
	let := t.letWord(names, false, a)
	value := t.translated(a.Rhs[0])
	lhs := t.textOf(a.Lhs[0].Pos(), a.TokPos)
	t.skip(init)
	t.textOf(init.Pos(), next)
	indent := t.indentOf(stmtPos)
	t.insert(stmtPos, let+lhs+"= "+value+"\n"+indent)
	t.replace(init.Pos(), next, "")
}

// usedAfter reports whether name is used after the statement s in its
// function: a hoisted let would then be in scope where the init's was not.
func (t *translator) usedAfter(s oldsyntax.Node, name string) bool {
	fn := t.enclosingFunc(s)
	if fn == nil {
		return false
	}
	found := false
	oldsyntax.Inspect(fn, func(n oldsyntax.Node) bool {
		if id, ok := n.(*oldsyntax.Ident); ok && id.Name == name && id.Pos() >= s.End() {
			found = true
		}
		return !found
	})
	return found
}

func (t *translator) indentOf(pos oldsyntax.Pos) string {
	start := strings.LastIndexByte(t.src[:pos], '\n') + 1
	line := t.src[start:pos]
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// forStmt rewrites a three-clause for (round-1 review, M5). Go evaluates
// the condition before every iteration and runs the post statement after
// every iteration, `continue` included.
//
//   - `for i := a; i < b; i++` becomes `for i in a..b` when a and b are
//     literals or locals the body never writes, neither has a call, and
//     the body never writes i: then the range has the same bounds on
//     every iteration.
//   - Any other `for i := a; cond; post` becomes `let mut i = a` before a
//     `for cond { … post }` loop, when no `continue` targets the loop
//     (Go would run the post statement on it) and no later code names i
//     (the let now outlives the loop).
//   - Everything else is a note.
func (t *translator) forStmt(f *oldsyntax.ForStmt) {
	if f.Init == nil && f.Post == nil {
		return
	}
	init, ok := f.Init.(*oldsyntax.AssignStmt)
	if !ok || init.Tok != oldsyntax.DEFINE || len(init.Lhs) != 1 || len(init.Rhs) != 1 || f.Cond == nil {
		t.note(f.Pos(), "a three-clause for: write for i in a..b or a for cond loop")
		return
	}
	id, _ := init.Lhs[0].(*oldsyntax.Ident)
	if id == nil {
		t.note(f.Pos(), "a three-clause for: write for i in a..b or a for cond loop")
		return
	}
	// A bound is stable when it is an integer literal, or a local that
	// neither the body nor a function literal (which the body may call)
	// writes.
	stable := func(x oldsyntax.Expr) bool {
		switch x := x.(type) {
		case *oldsyntax.BasicLit:
			return x.Kind == oldsyntax.INT
		case *oldsyntax.Ident:
			return x.Name != id.Name && t.declaredBefore(f, x.Name) && !t.written(f.Body, x.Name) && !t.writtenByLiteral(t.outerFunc(f), x.Name)
		}
		return false
	}
	cond, _ := f.Cond.(*oldsyntax.BinaryExpr)
	post, _ := f.Post.(*oldsyntax.IncDecStmt)
	if cond != nil && post != nil && cond.Op == oldsyntax.LSS && post.Tok == oldsyntax.INC {
		cx, _ := cond.X.(*oldsyntax.Ident)
		px, _ := post.X.(*oldsyntax.Ident)
		if cx != nil && px != nil && cx.Name == id.Name && px.Name == id.Name && !t.written(f.Body, id.Name) && stable(init.Rhs[0]) && stable(cond.Y) {
			// The header is replaced whole; its parts keep their renames.
			name := t.textOf(id.Pos(), id.End())
			from, to := t.translated(init.Rhs[0]), t.translated(cond.Y)
			t.skip(f.Init)
			t.skip(f.Cond)
			t.skip(f.Post)
			t.textOf(f.Init.Pos(), f.Body.Pos())
			t.replace(f.Init.Pos(), f.Body.Pos(), name+" in "+from+".."+to+" ")
			return
		}
	}
	stepped := false
	switch p := f.Post.(type) {
	case *oldsyntax.IncDecStmt:
		x, ok := p.X.(*oldsyntax.Ident)
		stepped = ok && x.Name == id.Name
	case *oldsyntax.AssignStmt:
		x, ok := p.Lhs[0].(*oldsyntax.Ident)
		stepped = ok && len(p.Lhs) == 1 && len(p.Rhs) == 1 && x.Name == id.Name && p.Tok != oldsyntax.DEFINE
	}
	// Go gives each iteration its own i, so a function literal or a task
	// in the body that names i sees that iteration's value; one `let mut`
	// outside the loop would be shared by all of them.
	if !stepped || t.continuesLoop(f) || t.usedAfter(f, id.Name) || t.declaredBefore(f, id.Name) || t.capturedIn(f.Body, id.Name) {
		t.note(f.Pos(), "a three-clause for whose bounds may change, or with continue: write for i in a..b or a for cond loop by hand")
		return
	}
	// The init and the post statement move; each keeps its rewrites.
	name := t.textOf(id.Pos(), id.End())
	value := t.translated(init.Rhs[0])
	var step string
	switch p := f.Post.(type) {
	case *oldsyntax.IncDecStmt:
		step = t.textOf(p.X.Pos(), p.X.End()) + map[oldsyntax.Token]string{oldsyntax.INC: " += 1", oldsyntax.DEC: " -= 1"}[p.Tok]
	case *oldsyntax.AssignStmt:
		step = t.translated(p)
	}
	t.skip(f.Init)
	t.skip(f.Post)
	t.textOf(f.Init.Pos(), f.Cond.Pos())
	t.textOf(f.Cond.End(), f.Body.Pos())
	indent := t.indentOf(f.For)
	t.insert(f.For, "let mut "+name+" = "+value+"\n"+indent)
	t.replace(f.Init.Pos(), f.Cond.Pos(), "")
	t.replace(f.Cond.End(), f.Body.Pos(), " ")
	if strings.Contains(t.src[f.Body.Lbrace:f.Body.Rbrace], "\n") {
		t.insert(f.Body.Rbrace, "\t"+step+"\n"+indent)
	} else {
		t.insert(f.Body.Rbrace, "\n"+indent+"\t"+step+"\n"+indent)
	}
	t.skip(f.Init)
	t.skip(f.Post)
}

// writtenByLiteral reports whether a function literal or a go block in fn
// writes name.
func (t *translator) writtenByLiteral(fn oldsyntax.Node, name string) bool {
	found := false
	oldsyntax.Inspect(fn, func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.FuncLit:
			found = found || t.written(n, name)
		case *oldsyntax.GoExpr:
			found = found || t.written(n, name)
		}
		return !found
	})
	return found
}

// capturedIn reports whether a function literal, a go statement or a defer
// in n names name.
func (t *translator) capturedIn(n oldsyntax.Node, name string) bool {
	found := false
	names := func(m oldsyntax.Node) {
		oldsyntax.Inspect(m, func(k oldsyntax.Node) bool {
			if id, ok := k.(*oldsyntax.Ident); ok && id.Name == name {
				found = true
			}
			return !found
		})
	}
	oldsyntax.Inspect(n, func(m oldsyntax.Node) bool {
		switch m.(type) {
		case *oldsyntax.FuncLit, *oldsyntax.GoExpr, *oldsyntax.DeferStmt:
			names(m)
			return false
		}
		return !found
	})
	return found
}

// continuesLoop reports whether a continue in f's body targets f: one
// outside every nested loop and function literal, or any labelled one.
func (t *translator) continuesLoop(f *oldsyntax.ForStmt) bool {
	found := false
	var walk func(n oldsyntax.Node) bool
	walk = func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.ForStmt, *oldsyntax.RangeStmt, *oldsyntax.FuncLit:
			oldsyntax.Inspect(n, func(m oldsyntax.Node) bool {
				if b, ok := m.(*oldsyntax.BranchStmt); ok && b.Tok == oldsyntax.CONTINUE && b.Label != nil {
					found = true
				}
				return !found
			})
			return false
		case *oldsyntax.BranchStmt:
			found = found || n.Tok == oldsyntax.CONTINUE
		}
		return !found
	}
	for _, s := range f.Body.List {
		oldsyntax.Inspect(s, walk)
	}
	return found
}

func (t *translator) rangeStmt(r *oldsyntax.RangeStmt) {
	if r.Tok == oldsyntax.ASSIGN {
		t.note(r.Pos(), "a range assigning existing names: bind new names with for x in xs")
		return
	}
	key, _ := r.Key.(*oldsyntax.Ident)
	val, _ := r.Value.(*oldsyntax.Ident)
	for _, id := range []*oldsyntax.Ident{key, val} {
		if id != nil {
			t.keywordName(id)
		}
	}
	// Two variables need a slice, array, map or string, so their form is
	// fixed; with one variable or none the meaning depends on the kind
	// of r.X, which only syntax that proves it may decide (M4).
	// A variable keeps its rename: the head is replaced whole.
	keyed := func(x oldsyntax.Expr) string { return t.textOf(x.Pos(), x.End()) }
	var head string
	kind := t.rangeKind(r.X)
	switch {
	case r.Value != nil && key != nil && key.Name == "_":
		head = keyed(r.Value) + " in "
	case r.Value != nil:
		head = keyed(r.Key) + ", " + keyed(r.Value) + " in "
	case kind == "":
		t.note(r.Pos(), "a range over %s: its kind (count, sequence or channel) is not written; choose the for form by hand", t.text(r.X))
		return
	case r.Key == nil && kind == "count":
		head = "_ in 0.."
	case r.Key == nil:
		head = "_ in "
	case kind == "count":
		head = keyed(r.Key) + " in 0.."
	case kind == "chan":
		head = keyed(r.Key) + " in "
	default:
		head = keyed(r.Key) + ", _ in "
	}
	t.textOf(r.For+3, r.X.Pos())
	t.replace(r.For+3, r.X.Pos(), " "+head)
}

// rangeKind is what a range expression ranges over, as far as syntax
// proves it (round-1 review, M4): "count" for an integer, "seq" for a
// slice, array, map or string, "chan" for a channel, "" when the syntax
// does not show it. A call never proves its result's kind, except len.
func (t *translator) rangeKind(x oldsyntax.Expr) string {
	switch x := x.(type) {
	case *oldsyntax.BasicLit:
		switch x.Kind {
		case oldsyntax.INT:
			return "count"
		case oldsyntax.STRING:
			return "seq"
		}
	case *oldsyntax.CallExpr:
		if id, ok := x.Fun.(*oldsyntax.Ident); ok && id.Name == "len" && !t.declaredBefore(x, "len") {
			return "count"
		}
	case *oldsyntax.CompositeLit:
		return typeKind(x.Type)
	case *oldsyntax.Ident:
		if ty, ok := t.declaredType(x); ok {
			return typeKind(ty)
		}
	}
	return ""
}

// typeKind is the range kind of a written type.
func typeKind(ty oldsyntax.Expr) string {
	switch ty := ty.(type) {
	case *oldsyntax.Ident:
		switch ty.Name {
		case "string":
			return "seq"
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
			return "count"
		}
	case *oldsyntax.ArrayType, *oldsyntax.MapType:
		return "seq"
	case *oldsyntax.ChanType:
		return "chan"
	}
	return ""
}

// inScope reports whether the declaration decl stands before x in a block
// that holds x, so that x may resolve to it.
func (t *translator) inScope(decl, x oldsyntax.Node) bool {
	if decl.Pos() >= x.Pos() {
		return false
	}
	for p := t.parent[decl]; p != nil; p = t.parent[p] {
		switch p.(type) {
		case *oldsyntax.BlockStmt, *oldsyntax.CaseClause, *oldsyntax.CatchArm, *oldsyntax.IfStmt, *oldsyntax.ForStmt, *oldsyntax.SwitchStmt, *oldsyntax.TypeSwitchStmt:
			return p.Pos() <= x.Pos() && x.End() <= p.End()
		}
	}
	return false
}

// declaredType returns the written type of the local or parameter x: the
// one declaration of its name in the enclosing function, when that
// declaration writes a type (a parameter, `var x T`, or a composite
// literal value). Several declarations of the name, or one without a
// written type, prove nothing.
func (t *translator) declaredType(x *oldsyntax.Ident) (oldsyntax.Expr, bool) {
	var ft *oldsyntax.FuncType
	var body oldsyntax.Node
	switch fn := t.enclosingFunc(x).(type) {
	case *oldsyntax.FuncDecl:
		ft, body = fn.Type, fn.Body
	case *oldsyntax.FuncLit:
		ft, body = fn.Type, fn.Body
	default:
		return nil, false
	}
	var found []oldsyntax.Expr
	for _, f := range ft.Params.List {
		for _, n := range f.Names {
			if n.Name == x.Name {
				found = append(found, f.Type)
			}
		}
	}
	oldsyntax.Inspect(body, func(n oldsyntax.Node) bool {
		switch n := n.(type) {
		case *oldsyntax.AssignStmt:
			if n.Tok != oldsyntax.DEFINE {
				return true
			}
			for i, l := range n.Lhs {
				if id, ok := l.(*oldsyntax.Ident); ok && id.Name == x.Name {
					var ty oldsyntax.Expr
					if len(n.Rhs) == len(n.Lhs) {
						if lit, ok := n.Rhs[i].(*oldsyntax.CompositeLit); ok && t.inScope(n, x) {
							ty = lit.Type
						}
					}
					found = append(found, ty)
				}
			}
		case *oldsyntax.VarSpec:
			for i, id := range n.Names {
				if id.Name != x.Name {
					continue
				}
				ty := n.Type
				if ty == nil && len(n.Values) == len(n.Names) {
					if lit, ok := n.Values[i].(*oldsyntax.CompositeLit); ok {
						ty = lit.Type
					}
				}
				if !t.inScope(n, x) {
					ty = nil
				}
				found = append(found, ty)
			}
		case *oldsyntax.RangeStmt:
			for _, k := range []oldsyntax.Expr{n.Key, n.Value} {
				if id, ok := k.(*oldsyntax.Ident); ok && id.Name == x.Name {
					found = append(found, nil)
				}
			}
		case *oldsyntax.FuncLit:
			for _, f := range n.Type.Params.List {
				for _, p := range f.Names {
					if p.Name == x.Name {
						found = append(found, nil)
					}
				}
			}
		}
		return true
	})
	if len(found) != 1 || found[0] == nil {
		return nil, false
	}
	return found[0], true
}

// switchStmt rewrites a value switch: all-literal cases become a match,
// anything else an if chain.
func (t *translator) switchStmt(s *oldsyntax.SwitchStmt) {
	if s.Init != nil {
		next := s.Lbrace
		if s.Tag != nil {
			next = s.Tag.Pos()
		}
		t.hoistInit(s.Switch, s.Init, next)
	}
	literal := s.Tag != nil
	for _, c := range s.Clauses {
		for _, x := range c.List {
			if u, ok := x.(*oldsyntax.UnaryExpr); ok && u.Op == oldsyntax.SUB {
				x = u.X
			}
			if _, ok := x.(*oldsyntax.BasicLit); !ok {
				literal = false
			}
		}
	}
	if literal {
		t.replace(s.Switch, s.Switch+6, "match")
		for _, c := range s.Clauses {
			if c.List == nil {
				t.replace(c.Case, c.Case+7, "case _")
			}
		}
		return
	}
	tag := ""
	if s.Tag != nil {
		if _, isName := s.Tag.(*oldsyntax.Ident); !isName {
			t.note(s.Pos(), "a switch on an expression with non-literal cases: bind the subject with let, then write an if chain")
			return
		}
		tag = t.translated(s.Tag)
	}
	for i, c := range s.Clauses {
		if c.List == nil && i != len(s.Clauses)-1 {
			t.note(c.Case, "default before the last case: move it last")
			return
		}
	}
	if len(s.Clauses) == 0 {
		t.note(s.Pos(), "an empty switch")
		return
	}
	if s.Clauses[0].List == nil {
		t.note(s.Pos(), "a switch with only a default arm: write its statements without the switch")
		return
	}
	// Go reads the tag once; an if chain reads it in every condition. A
	// case expression with a call may change it in between, so then the
	// chain reads a snapshot taken where the switch stood (M6).
	if tag != "" && casesCall(s.Clauses) {
		snap := t.freshName(tag + "Subject")
		t.insert(s.Switch, "let "+snap+" = "+tag+"\n"+t.indentOf(s.Switch))
		tag = snap
	}
	start := s.Switch
	if s.Init != nil {
		t.replace(s.Switch, s.Init.Pos(), "")
		start = s.Lbrace
		if s.Tag != nil {
			start = s.Tag.Pos()
		}
	}
	// The header goes with the line break after it, so the chain starts
	// where the switch stood.
	end := s.Lbrace + 1
	if first := s.Clauses[0].Case; strings.TrimSpace(t.src[end:first]) == "" {
		end = first
	}
	t.replace(start, end, "")
	for i, c := range s.Clauses {
		var head string
		switch {
		case c.List == nil:
			head = "} else {"
		default:
			conds := make([]string, len(c.List))
			for j, x := range c.List {
				// Each case expression stays one operand (M6):
				// `case a || b` is `tag == (a || b)`; in a tagless
				// switch, `case a, b` is `a || b`. The case
				// expression is translated on its own, so its renames
				// and rewrites are kept (round-2 review, m2).
				conds[j] = t.translated(x)
				if tag != "" {
					op, cp := groupingText(x, conds[j], oldsyntax.EQL.Precedence())
					conds[j] = tag + " == " + op + conds[j] + cp
				} else if len(c.List) > 1 || bareLiteral(x) {
					op, cp := groupingText(x, conds[j], oldsyntax.LOR.Precedence())
					conds[j] = op + conds[j] + cp
				}
			}
			head = "if " + strings.Join(conds, " || ") + " {"
			if i > 0 {
				head = "} else " + head
			}
		}
		t.textOf(c.Case, c.Colon+1) // nothing of the header is left to rewrite
		t.replace(c.Case, c.Colon+1, head)
	}
}

// groupingText is grouping for a translated operand: a translation that
// starts with try (a try moved to the expression's start) covers the
// whole operand, so it is parenthesised too. So is an operand with a bare
// composite literal, which an if header does not parse (D1.8, as in Go).
func groupingText(x oldsyntax.Expr, text string, prec int) (string, string) {
	if strings.HasPrefix(text, "try ") || bareLiteral(x) {
		return "(", ")"
	}
	return grouping(x, prec)
}

// println rewrites a call of the prelude's Console.Println, which takes one
// line in the decided prelude (round-6 ruling A3), to one interpolated
// string: Go's Println writes its operands as %v does, separated by one
// space. A single interpreted string literal stays as written; any other
// operand is interpolated, and a string literal among several gives its
// text.
func (t *translator) println(c *oldsyntax.CallExpr) {
	if len(c.Args) == 1 {
		if lit, ok := c.Args[0].(*oldsyntax.BasicLit); ok && lit.Kind == oldsyntax.STRING && lit.Value[0] == '"' {
			return
		}
	}
	// An operand with a block (a catch, a function literal, a trailing
	// block) prints on several lines, which an interpolation cannot hold:
	// then every operand is bound with let first, in order, so the
	// evaluation order stays.
	hoist := false
	for _, a := range c.Args {
		oldsyntax.Inspect(a, func(n oldsyntax.Node) bool {
			switch n := n.(type) {
			case *oldsyntax.CatchExpr, *oldsyntax.FuncLit:
				hoist = true
			case *oldsyntax.CallExpr:
				hoist = hoist || n.Trailing != nil
			}
			return !hoist
		})
	}
	var stmt oldsyntax.Node
	if hoist {
		es, ok := t.parent[c].(*oldsyntax.ExprStmt)
		if ok {
			switch t.parent[es].(type) {
			case *oldsyntax.BlockStmt, *oldsyntax.CaseClause:
				stmt = es
			}
		}
		if stmt == nil {
			t.note(c.Pos(), "Console.Println of an operand with a block, outside a statement: bind the operand with let and print it by hand")
			return
		}
	}
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		if lit, ok := a.(*oldsyntax.BasicLit); ok && lit.Kind == oldsyntax.STRING && lit.Value[0] == '"' {
			parts[i] = lit.Value[1 : len(lit.Value)-1]
			continue
		}
		text := t.translated(a)
		if hoist {
			name := t.freshName("line")
			t.insert(stmt.Pos(), "let "+name+" = "+text+"\n"+t.indentOf(stmt.Pos()))
			text = name
		}
		parts[i] = `\(` + text + `)`
	}
	t.textOf(c.Lparen, c.Rparen+1)
	t.replace(c.Lparen, c.Rparen+1, `("`+strings.Join(parts, " ")+`")`)
}

// inHeader reports whether x stands bare in a control clause header: the
// condition of an if or for, the operand of a range or the tag of a
// switch, outside parentheses, brackets and braces.
func (t *translator) inHeader(x oldsyntax.Node) bool {
	for c := x; ; {
		switch p := t.parent[c].(type) {
		case nil, *oldsyntax.ParenExpr, *oldsyntax.FuncLit, *oldsyntax.CompositeLit, *oldsyntax.KeyValueExpr:
			return false
		case *oldsyntax.CallExpr:
			if c != p.Fun {
				return false
			}
		case *oldsyntax.IndexExpr:
			if c != p.X {
				return false
			}
		case *oldsyntax.SliceExpr:
			if c != p.X {
				return false
			}
		case *oldsyntax.IfStmt:
			return c == p.Cond
		case *oldsyntax.ForStmt:
			return c == p.Cond
		case *oldsyntax.RangeStmt:
			return c == p.X
		case *oldsyntax.SwitchStmt:
			return c == p.Tag
		case oldsyntax.Stmt, oldsyntax.Decl:
			return false
		default:
			c = p
		}
	}
}

// bareLiteral reports whether x holds a composite literal outside
// parentheses, brackets and braces: in a control clause header it would
// end the header (round-3 review, m1).
func bareLiteral(x oldsyntax.Node) bool {
	found := false
	oldsyntax.Inspect(x, func(n oldsyntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *oldsyntax.CompositeLit:
			found = true
		case *oldsyntax.ParenExpr, *oldsyntax.FuncLit:
		case *oldsyntax.CallExpr:
			found = bareLiteral(n.Fun)
		case *oldsyntax.IndexExpr:
			found = bareLiteral(n.X)
		case *oldsyntax.SliceExpr:
			found = bareLiteral(n.X)
		default:
			return true
		}
		return false
	})
	return found
}

// casesCall reports whether a case expression of clauses holds a call.
func casesCall(clauses []*oldsyntax.CaseClause) bool {
	found := false
	for _, c := range clauses {
		for _, x := range c.List {
			oldsyntax.Inspect(x, func(n oldsyntax.Node) bool {
				_, call := n.(*oldsyntax.CallExpr)
				found = found || call
				return !found
			})
		}
	}
	return found
}

// freshName returns base, or base with a number, that no identifier of the
// file uses, so a name the translator adds never captures or shadows one.
func (t *translator) freshName(base string) string {
	used := map[string]bool{}
	oldsyntax.Inspect(t.file, func(n oldsyntax.Node) bool {
		if id, ok := n.(*oldsyntax.Ident); ok {
			used[id.Name] = true
		}
		return true
	})
	name := base
	for i := 2; used[name] || t.fresh[name] || syntax.IsKeyword(name); i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	if t.fresh == nil {
		t.fresh = map[string]bool{}
	}
	t.fresh[name] = true
	return name
}

func (t *translator) skipList(list []oldsyntax.Expr) {
	for _, x := range list {
		t.skip(x)
	}
}

// tupleCase returns the tuple of a case that lists exactly one tuple.
func tupleCase(list []oldsyntax.Expr) (*oldsyntax.TupleExpr, bool) {
	if len(list) != 1 {
		return nil, false
	}
	tup, ok := list[0].(*oldsyntax.TupleExpr)
	return tup, ok
}

// typeSwitch rewrites `switch p := x.(type)` to `match x`, binding p with
// `as` in the single-type arms that read it, and each binding of a
// multi-subject switch with `as` on its element in a single-tuple arm.
func (t *translator) typeSwitch(s *oldsyntax.TypeSwitchStmt) {
	from := s.Subjects[0].Pos()
	if len(s.Binds) > 0 {
		from = s.Binds[0].Pos()
	}
	if s.Init != nil {
		t.hoistInit(s.Switch, s.Init, from)
	}
	subjects := make([]string, len(s.Subjects))
	for i, g := range s.Subjects {
		subjects[i] = t.translated(g.X)
	}
	head := "match " + subjects[0] + " "
	if len(subjects) > 1 {
		head = "match (" + strings.Join(subjects, ", ") + ") "
	}
	if s.Init == nil {
		t.replace(s.Switch, from, "")
	} else {
		t.replace(s.Switch, s.Init.Pos(), "")
	}
	// A binding keeps its renamed name; the rest of the header goes.
	binds := make([]string, len(s.Binds))
	for i, b := range s.Binds {
		binds[i] = t.textOf(b.Pos(), b.End())
	}
	t.textOf(from, s.Lbrace)
	t.replace(from, s.Lbrace, head)
	for _, g := range s.Subjects {
		t.skip(g)
	}
	// A binding no clause reads is Go's "declared and not used" (S110):
	// the first single-type arm keeps it with `as`, so the unread binding
	// and its diagnostic stay.
	if len(s.Binds) == 1 && s.Binds[0].Name != "_" {
		read := false
		for _, c := range s.Clauses {
			read = read || t.reads(c.Body, s.Binds[0].Name)
		}
		if !read {
			kept := false
			for _, c := range s.Clauses {
				if len(c.List) == 1 {
					t.insert(c.List[0].End(), " as "+binds[0])
					kept = true
					break
				}
			}
			if !kept {
				t.note(s.Switch, "no arm reads %s, which Go reports as unused: bind it in a single-type arm by hand", s.Binds[0].Name)
			}
		}
	}
	for _, c := range s.Clauses {
		if c.List == nil {
			t.replace(c.Case, c.Case+7, "case _")
		}
		for i, b := range s.Binds {
			if !t.reads(c.Body, b.Name) {
				continue
			}
			if len(c.List) == 1 && len(s.Binds) == 1 {
				t.insert(c.List[0].End(), " as "+binds[i])
				continue
			}
			// A tuple case with one pattern narrows each binding to its
			// element's type, as `as` on that element does (the subject
			// keeps its union type, so a rename would not).
			if tup, ok := tupleCase(c.List); ok && len(tup.Elts) == len(s.Binds) {
				t.insert(tup.Elts[i].End(), " as "+binds[i])
				continue
			}
			_, isName := s.Subjects[i].X.(*oldsyntax.Ident)
			if !isName {
				t.note(c.Case, "an arm with several types reads %s: bind the subject with let first", b.Name)
				continue
			}
			t.renameReads(s, c.Case, c.Body, b.Name, s.Subjects[i].X.(*oldsyntax.Ident).Name, subjects[i])
		}
	}
}
