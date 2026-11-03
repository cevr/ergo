package syntax

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestMalformed: every malformed input is a located diagnostic with its code
// (S001 unless the row names another) on one of its lines, never a
// passthrough, and a Go habit carries the message that names the Ergo form.
func TestMalformed(t *testing.T) {
	messages := map[string]string{}
	for _, h := range Habits() {
		messages[h.Spelling] = h.Message
	}
	for _, m := range sketchtest.MalformedTable {
		t.Run(m.Name, func(t *testing.T) {
			src := m.Src
			if m.Generate != nil {
				src = m.Generate()
			}
			file, err := Parse(src)
			if file != nil || err == nil {
				t.Fatal("parsed")
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("%T is not *Error", err)
			}
			code := m.Code
			if code == "" {
				code = CodeSyntax
			}
			if e.Code != code || !slices.Contains(m.Lines, e.Line) {
				t.Fatalf("%v: want %s on line %v", e, code, m.Lines)
			}
			if m.Habit != "" {
				want, ok := messages[m.Habit]
				if !ok {
					t.Fatalf("no habit %q", m.Habit)
				}
				if e.Message != want {
					t.Fatalf("message %q, want the habit's %q", e.Message, want)
				}
			}
			if e.Offset < 0 || e.Offset+e.Length > len(src) {
				t.Fatalf("span %d+%d outside the %d-byte source", e.Offset, e.Length, len(src))
			}
			line := strings.Count(src[:e.Offset], "\n") + 1
			column := e.Offset - (strings.LastIndexByte(src[:e.Offset], '\n') + 1) + 1
			if line != e.Line || column != e.Column {
				t.Fatalf("line %d column %d, want %d and %d from the offset", e.Line, e.Column, line, column)
			}
		})
	}
}

// TestEveryHabitHasAFixture: each Go habit of the message table has a
// malformed row (D7: every D2 diagnostic has a malformed fixture), and each
// message names the Ergo form as code in backticks, except the three whose
// Ergo form is a rule rather than a spelling.
func TestEveryHabitHasAFixture(t *testing.T) {
	covered := map[string]bool{}
	for _, m := range sketchtest.MalformedTable {
		covered[m.Habit] = true
	}
	for _, h := range Habits() {
		if !covered[h.Spelling] {
			t.Errorf("habit %q has no malformed fixture", h.Spelling)
		}
		switch h.Spelling {
		case "chan T", "<-ch", "L:", "goto", "fallthrough", "package p":
		default:
			if !strings.Contains(h.Message, "`") {
				t.Errorf("habit %q: message %q names no Ergo form in backticks", h.Spelling, h.Message)
			}
		}
	}
}

// TestUsesIsAnIdentifier (#217): `uses` is not a keyword and has no
// compatibility path, so the old spelling of a needs clause gets the plain
// S001 any stray identifier gets, never a dedicated hint.
func TestUsesIsAnIdentifier(t *testing.T) {
	_, err := Parse("func f() uses A {}\n")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeSyntax || e.Line != 1 || strings.Contains(e.Message, "needs") {
		t.Errorf("got %v, want a plain S001 on line 1", err)
	}
	if _, err := Parse("let uses = 1\n"); err != nil {
		t.Fatalf("uses is not a name: %v", err)
	}
}

// TestKeywordsAreNotNames: no keyword or reserved word is a name, in a let,
// a parameter, a field or a type; a contextual word is.
func TestKeywordsAreNotNames(t *testing.T) {
	words := append(Keywords(), Reserved()...)
	if n := len(Keywords()); n != 28 {
		t.Fatalf("%d keywords, want the 27 decided ones and as", n)
	}
	if n := len(Reserved()); n != 15 {
		t.Fatalf("%d reserved words, want 15", n)
	}
	for _, word := range words {
		for _, src := range []string{
			"let " + word + " = 1\n",
			"func f(" + word + " int64) {}\n",
			"type T { " + word + " int64 }\n",
			"type " + word + " {}\n",
		} {
			if _, err := Parse(src); err == nil {
				t.Errorf("%q parsed: %s is a name", src, word)
			}
		}
	}
	for _, word := range []string{"replace", "initial", "step", "uses", "Map"} {
		if _, err := Parse("func f() {\n\tlet " + word + " = 1\n\tuse(" + word + ")\n}\n"); err != nil {
			t.Errorf("%s is not a name: %v", word, err)
		}
	}
}

// TestAdmittedForms: decided forms parse into the trees of D3, the shapes
// the 196a checker and the prototype bridge read.
func TestAdmittedForms(t *testing.T) {
	parse := func(src string) *File {
		t.Helper()
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		return f
	}
	body := func(src string) []Stmt {
		t.Helper()
		return parse("func f() {\n" + src + "\n}\n").Decls[0].(*FuncDecl).Body.List
	}

	// No package clause; File.Pos falls back to the first declaration.
	f := parse("// c\nimport strings\n")
	if f.Package.IsValid() || f.Name != nil || f.Pos() != 0 {
		t.Fatalf("file %+v at %d", f, f.Pos())
	}
	// import [go] [alias] path, unquoted, one per line.
	f = parse("import strings\nimport go net/http\nimport ourjson example.com/json\nimport go gohttp net/http\n")
	kinds := []ImportKind{ImportPlain, ImportGo, ImportNamed, ImportGo}
	paths := []string{"strings", "net/http", "example.com/json", "net/http"}
	for i, d := range f.Decls {
		imp := d.(*ImportDecl)
		s := imp.Specs[0]
		if len(imp.Specs) != 1 || imp.Lparen.IsValid() || s.Kind != kinds[i] || s.Path.Kind != PATH || s.Path.Value != paths[i] {
			t.Fatalf("import %d: %+v %+v", i, imp, s)
		}
	}
	if s := f.Decls[3].(*ImportDecl).Specs[0]; s.Name == nil || s.Name.Name != "gohttp" {
		t.Fatalf("aliased go import %+v", s)
	}
	// let, let mut, a typed let without a value, a tuple and a record pattern.
	stmts := body("let x = 1\nlet mut y int64 = 2\nlet z string\nlet q, r = divide(7, 2)\nlet Point{X: px} = p")
	for i, want := range []struct {
		mut, typed bool
		names      int
		pattern    bool
	}{{false, false, 1, false}, {true, true, 1, false}, {false, true, 1, false}, {false, false, 2, false}, {false, false, 0, true}} {
		d := stmts[i].(*DeclStmt).Decl
		s := d.Specs[0]
		if len(d.Specs) != 1 || d.Lparen.IsValid() || d.Mut.IsValid() != want.mut || (s.Type != nil) != want.typed ||
			len(s.Names) != want.names || (s.Pattern != nil) != want.pattern {
			t.Fatalf("let %d: %+v %+v", i, d, s)
		}
	}
	// A package-level context is a ContextDecl; a context elsewhere is a ContextLit.
	f = parse("let App = context provides A needs B {\n\tC, D:      X{},\n\treplace E: Y{},\n\tShared,\n}\n\nfunc main() {\n\twith context { A: M{} } {\n\t\trun()\n\t}\n}\n")
	w := f.Decls[0].(*ContextDecl)
	if !w.Let.IsValid() || w.Pos() != w.Let || w.Provides == nil || w.Needs == nil || len(w.Body.Entries) != 3 {
		t.Fatalf("context %+v", w)
	}
	if e := w.Body.Entries; e[0].Kind != ContextBind || len(e[0].Keys) != 1 || e[1].Kind != ContextReplace || e[2].Kind != ContextEmbed {
		t.Fatalf("context entries %+v %+v %+v", e[0], e[1], e[2])
	}
	with := f.Decls[1].(*FuncDecl).Body.List[0].(*ExprStmt).X.(*CallExpr)
	if id, ok := with.Fun.(*Ident); !ok || id.Name != "with" || len(with.Args) != 1 || with.Trailing == nil {
		t.Fatalf("with %+v", with)
	}
	if _, ok := with.Args[0].(*ContextLit); !ok {
		t.Fatalf("with argument %T", with.Args[0])
	}
	// Records, operation types with laws, unions with inline variants,
	// machines and defined types.
	f = parse("type Card { Number string, Expiry Date }\ntype Empty {}\ntype Store {\n\tFind(id string) Order fails NotFound,\n\n\tlaw Found(id string) { true },\n}\ntype Payment = Pending | Paid{ID string}\ntype M machine[S, E] { initial: Idle{}, step: Step }\ntype Celsius float64\n")
	if s := f.Decls[0].(*TypeDecl).Spec.(*StructType); s.Struct.IsValid() || len(s.Fields.List) != 2 {
		t.Fatalf("record %+v", s)
	}
	if _, ok := f.Decls[1].(*TypeDecl).Spec.(*StructType); !ok {
		t.Fatal("empty record")
	}
	if s := f.Decls[2].(*TypeDecl).Spec.(*InterfaceType); len(s.Methods) != 1 || len(s.Laws) != 1 || s.Laws[0].Name.Name != "Found" {
		t.Fatalf("operation type %+v", s)
	}
	union := f.Decls[3].(*TypeDecl).Spec.(*InterfaceType).Union
	if _, ok := union[0].(*Ident); !ok || len(union) != 2 {
		t.Fatalf("union %+v", union)
	}
	if v, ok := union[1].(*VariantSpec); !ok || v.Name.Name != "Paid" || len(v.Fields.List) != 1 {
		t.Fatalf("variant %+v", union[1])
	}
	if m := f.Decls[4].(*TypeDecl).Spec.(*MachineType); len(m.Entries) != 2 || !m.Entries[0].Colon.IsValid() {
		t.Fatalf("machine %+v", m)
	}
	if id, ok := f.Decls[5].(*TypeDecl).Spec.(*Ident); !ok || id.Name != "float64" {
		t.Fatalf("defined type %+v", f.Decls[5].(*TypeDecl).Spec)
	}
	// R fails A | B needs S is the result plus the old row; escaping;
	// a tuple result; Map[K, V].
	fn := parse("func f(g escaping func() int64) (int64, string) fails A | B needs S {}\n").Decls[0].(*FuncDecl)
	res := fn.Type.Results
	if res.Opening.IsValid() || len(res.List) != 2 {
		t.Fatalf("results %+v", res)
	}
	if _, ok := res.List[0].Type.(*TupleExpr); !ok {
		t.Fatalf("tuple result %T", res.List[0].Type)
	}
	if row := res.List[1].Type.(*ErrorRow); len(row.Types) != 2 || !row.Error.IsValid() {
		t.Fatalf("row %+v", row)
	}
	if fn.Type.Needs == nil || !fn.Type.Params.List[0].Type.(*FuncType).Escaping.IsValid() {
		t.Fatal("needs or escaping")
	}
	if m, ok := parse("let m Map[string, int64]\n").Decls[0].(*VarDecl).Specs[0].Type.(*MapType); !ok || m.Key.(*Ident).Name != "string" {
		t.Fatal("Map[K, V] is not a MapType")
	}
	// extern, test, assert.
	f = parse("extern go example.com/json.Valid func Valid(b []byte) bool\n\ntest Works {\n\tassert 1 + 1 == 2\n}\n\ntest Prop(x int64) {\n\tassert x == x\n}\n")
	ext := f.Decls[0].(*FuncDecl)
	if path, name := ext.Extern.Split(); ext.Body != nil || ext.Pos() != 0 || path != "example.com/json" || name != "Valid" {
		t.Fatalf("extern %+v: %s %s", ext.Extern, path, name)
	}
	if td := f.Decls[1].(*TestDecl); td.Params != nil || td.Body.List[0].(*AssertStmt).X == nil {
		t.Fatalf("test %+v", td)
	}
	if td := f.Decls[2].(*TestDecl); td.Params == nil || len(td.Params.List) != 1 {
		t.Fatalf("property %+v", td)
	}
	// if as an expression; match on literals is a value switch, any other
	// match a type switch; tuple subjects; as-patterns; case _.
	stmts = body("let v = if a { 1 } else { 2 }\nmatch n {\ncase 1, -2:\ncase _:\n}\nmatch p {\ncase Declined as d:\n\tuse(d)\ncase Pending:\n}\nmatch (a, b) {\ncase (X, _):\n}")
	if _, ok := stmts[0].(*DeclStmt).Decl.Specs[0].Values[0].(*IfStmt); !ok {
		t.Fatal("if expression")
	}
	if sw := stmts[1].(*SwitchStmt); sw.Tag == nil || len(sw.Clauses) != 2 || sw.Clauses[1].List != nil || sw.Init != nil {
		t.Fatalf("value match %+v", sw)
	}
	ts := stmts[2].(*TypeSwitchStmt)
	if len(ts.Subjects) != 1 || ts.Binds != nil || ts.Subjects[0].Lparen.IsValid() || ts.Subjects[0].End() != ts.Subjects[0].X.End() {
		t.Fatalf("type match %+v", ts)
	}
	if as, ok := ts.Clauses[0].List[0].(*AsPattern); !ok || as.Name.Name != "d" {
		t.Fatalf("as-pattern %T", ts.Clauses[0].List[0])
	}
	if ts := stmts[3].(*TypeSwitchStmt); len(ts.Subjects) != 2 {
		t.Fatalf("tuple match %+v", ts)
	}
	// A match with no literal pattern (no arm, or `_` arms only) is not on
	// literal patterns: a type match.
	for _, src := range []string{"match c {\n}", "match c {\ncase _:\n}"} {
		if _, ok := body(src)[0].(*TypeSwitchStmt); !ok {
			t.Fatalf("%q is %T, want a type match", src, body(src)[0])
		}
	}
	// catch without a binding, patterns in the arms.
	c := body("let r = f() catch {\ncase NotFound as e:\n\tlog(e)\n\t0\ncase A, B:\n\t1\n}")[0].(*DeclStmt).Decl.Specs[0].Values[0].(*CatchExpr)
	if c.Bind != nil || len(c.Arms) != 2 || len(c.Arms[1].Types) != 2 {
		t.Fatalf("catch %+v", c)
	}
	// for x in xs, for i, x in xs, for i in a..b, for cond, for.
	stmts = body("for x in xs {\n}\nfor i, x in xs {\n}\nfor i in 0..n {\n}\nfor ok {\n}\nfor {\n}")
	if r := stmts[0].(*RangeStmt); r.Key != nil || r.Value == nil || r.Tok != DEFINE {
		t.Fatalf("for in %+v", r)
	}
	if r := stmts[1].(*RangeStmt); r.Key == nil || r.Value == nil {
		t.Fatalf("for i, x in %+v", r)
	}
	if r := stmts[2].(*RangeStmt); r.X.(*BinaryExpr).Op != DOTDOT {
		t.Fatalf("for range %+v", r)
	}
	if l := stmts[3].(*ForStmt); l.Cond == nil || l.Init != nil || l.Post != nil {
		t.Fatalf("for cond %+v", l)
	}
	if l := stmts[4].(*ForStmt); l.Cond != nil {
		t.Fatalf("for %+v", l)
	}
	// `..` binds looser than `||` and every other operator.
	r := body("for i in 1..len(xs)-1 {\n}")[0].(*RangeStmt).X.(*BinaryExpr)
	if r.Op != DOTDOT || r.Y.(*BinaryExpr).Op != SUB {
		t.Fatalf("1..len(xs)-1 is %+v", r)
	}
	// Untyped and typed function literals; a function type without a block.
	stmts = body("xs.Fold(0, func(acc, x) { acc + x })\nlet g = func(a, b int64) int64 { a + b }\nlet h func(x) = nil")
	lit := stmts[0].(*ExprStmt).X.(*CallExpr).Args[1].(*FuncLit)
	if ps := lit.Type.Params.List; len(ps) != 2 || ps[0].Type != nil {
		t.Fatalf("untyped literal %+v", ps)
	}
	if ps := stmts[1].(*DeclStmt).Decl.Specs[0].Values[0].(*FuncLit).Type.Params.List; len(ps) != 1 || len(ps[0].Names) != 2 {
		t.Fatalf("typed literal %+v", ps)
	}
	if ft := stmts[2].(*DeclStmt).Decl.Specs[0].Type.(*FuncType); ft.Params.List[0].Type.(*Ident).Name != "x" {
		t.Fatalf("function type %+v", ft.Params.List[0])
	}
	// Interpolation with a nested string; a float literal.
	stmts = body("let s = \"a \\(f(\"b\")) c \\(x)\"\nlet z = 2.5")
	if is := stmts[0].(*DeclStmt).Decl.Specs[0].Values[0].(*InterpolatedString); len(is.Exprs) != 2 || len(is.Texts) != 3 {
		t.Fatalf("interpolation %+v", is)
	}
	if lit := stmts[1].(*DeclStmt).Decl.Specs[0].Values[0].(*BasicLit); lit.Kind != FLOAT {
		t.Fatalf("float %+v", lit)
	}
	// A union may continue on the next line with or without a leading |.
	for _, src := range []string{"type U =\n\t| A\n\t| B\n", "type U = A\n\t| B\n", "type U = A |\n\tB\n"} {
		if u := parse(src).Decls[0].(*TypeDecl).Spec.(*InterfaceType).Union; len(u) != 2 {
			t.Fatalf("%q: union %+v", src, u)
		}
	}
}

// TestRoundTwoForms pins the round-1 review's parser findings (M9, M10,
// M11, M14): each decided form parses to the tree it means.
func TestRoundTwoForms(t *testing.T) {
	parse := func(t *testing.T, src string) *File {
		t.Helper()
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		return f
	}
	t.Run("M9 grouped tuple field", func(t *testing.T) {
		// A field group takes every decided Type, a tuple included.
		fields := parse(t, "type S { X, Y (int64, int64) }\n").Decls[0].(*TypeDecl).Spec.(*StructType).Fields.List
		if len(fields) != 1 || len(fields[0].Names) != 2 {
			t.Fatalf("grouped tuple field %+v", fields)
		}
		if _, ok := fields[0].Type.(*TupleExpr); !ok {
			t.Fatalf("grouped field type %T", fields[0].Type)
		}
	})
	t.Run("M10 function literal needs", func(t *testing.T) {
		// A literal's own row takes the declaration spelling.
		lit := parse(t, "let f = func() needs A, B {\n\twork()\n}\n").Decls[0].(*VarDecl).Specs[0].Values[0].(*FuncLit)
		if n := lit.Type.Needs; n == nil || len(n.Names) != 2 || n.Lparen.IsValid() {
			t.Fatalf("literal needs %+v", n)
		}
		// A function type inside a list keeps the parenthesised row.
		param := parse(t, "func g(h func() needs (A, B)) {}\n").Decls[0].(*FuncDecl).Type.Params.List[0].Type.(*FuncType)
		if n := param.Needs; len(n.Names) != 2 || !n.Lparen.IsValid() {
			t.Fatalf("parameter needs %+v", n)
		}
	})
	t.Run("M11 context keys without layout", func(t *testing.T) {
		// A comma run of names ended by ":" is one entry on any layout;
		// an embed after the entries stays an embed.
		for _, src := range []string{
			"let W = context { A, B: p, C }\n",
			"let W = context {\n\tA,\n\tB: p,\n\tC,\n}\n",
			"let W = context { A,\n\tB: p, C }\n",
		} {
			var body *ContextBody
			switch d := parse(t, src).Decls[0].(type) {
			case *ContextDecl:
				body = d.Body
			case *VarDecl:
				body = d.Specs[0].Values[0].(*ContextLit).Body
			}
			es := body.Entries
			if len(es) != 2 || len(es[0].Keys) != 1 || es[0].Kind == ContextEmbed || es[1].Kind != ContextEmbed {
				t.Errorf("%q: entries %+v", src, es)
			}
		}
	})
	t.Run("M14 comment in interpolation", func(t *testing.T) {
		// A block comment inside an interpolation is a comment.
		f := parse(t, "let s = \"x \\(a /* note */ + b) y\"\n")
		if is := f.Decls[0].(*VarDecl).Specs[0].Values[0].(*InterpolatedString); len(is.Exprs) != 1 {
			t.Fatalf("interpolation %+v", is)
		}
		if len(f.Comments) != 1 || f.Comments[0].List[0].Text != "/* note */" {
			t.Fatalf("comments %+v", f.Comments)
		}
	})
}

// TestRoundThreeForms pins the round-2 review's parser findings.
func TestRoundThreeForms(t *testing.T) {
	t.Run("M1 capability names take no type arguments", func(t *testing.T) {
		for _, name := range []string{
			"needs-type-args", "needs-list-type-args", "operation-needs-type-args", "provides-type-args",
			"context-key-type-args", "context-replace-type-args", "context-embed-type-args",
			"context-second-key-type-args", "context-first-key-type-args",
		} {
			i := slices.IndexFunc(sketchtest.MalformedTable, func(m sketchtest.Malformed) bool { return m.Name == name })
			if i < 0 {
				t.Fatalf("no malformed row %q", name)
			}
			src := sketchtest.MalformedTable[i].Src
			_, err := Parse(src)
			var e *Error
			if !errors.As(err, &e) {
				t.Errorf("%s: %v is not *Error", name, err)
				continue
			}
			if e.Message != "a capability name takes no type arguments" || src[e.Offset] != '[' {
				t.Errorf("%s: %v at %q, want the capability message at the [", name, e, src[e.Offset:])
			}
		}
	})
	t.Run("m1 name and paren in a type body is an operation", func(t *testing.T) {
		// `A (int64, string)` and `A(int64, string)` are the same tokens:
		// an operation (grammar.md, Parser rule 11).
		f, err := Parse("type S { A (int64, string) }\n")
		if err != nil {
			t.Fatal(err)
		}
		it, ok := f.Decls[0].(*TypeDecl).Spec.(*InterfaceType)
		if !ok || len(it.Methods) != 1 || it.Methods[0].Names[0].Name != "A" {
			t.Fatalf("spec %+v", f.Decls[0].(*TypeDecl).Spec)
		}
		// A record names the tuple type instead.
		f, err = Parse("type Pair (int64, string)\n\ntype S { X int64, A Pair }\n")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.Decls[1].(*TypeDecl).Spec.(*StructType); !ok {
			t.Fatalf("spec %T", f.Decls[1].(*TypeDecl).Spec)
		}
	})
}

// malformedError parses the malformed row name and returns its error.
func malformedError(t *testing.T, name string) (string, *Error) {
	t.Helper()
	i := slices.IndexFunc(sketchtest.MalformedTable, func(m sketchtest.Malformed) bool { return m.Name == name })
	if i < 0 {
		t.Fatalf("no malformed row %q", name)
	}
	src := sketchtest.MalformedTable[i].Src
	_, err := Parse(src)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("%s: %v is not *Error", name, err)
	}
	return src, e
}

// TestRoundFourForms: a type is never a value (round-3 review M1, m3).
func TestRoundFourForms(t *testing.T) {
	t.Run("M1 a function literal's needs clause takes no type arguments", func(t *testing.T) {
		// No fallback to a function type: the error stands at the `[`
		// in a call argument, a let and after a fails clause.
		for _, name := range []string{"funclit-needs-type-args-call", "funclit-needs-type-args-let", "funclit-fails-needs-type-args"} {
			src, e := malformedError(t, name)
			if e.Message != "a capability name takes no type arguments" || src[e.Offset] != '[' {
				t.Errorf("%s: %v at %q, want the capability message at the [", name, e, src[e.Offset:])
			}
		}
	})
	t.Run("m3 a bare type in value position", func(t *testing.T) {
		for _, c := range []struct{ name, first string }{
			{"func-type-value", "func"}, {"func-type-argument", "func"}, {"escaping-func-type-value", "escaping"},
			{"slice-type-value", "["}, {"slice-type-argument", "["}, {"slice-type-conversion", "["},
		} {
			src, e := malformedError(t, c.name)
			if e.Message != "a type is not a value; write a literal or a conversion" || !strings.HasPrefix(src[e.Offset:], c.first) {
				t.Errorf("%s: %v at %q, want the type message at %q", c.name, e, src[e.Offset:], c.first)
			}
		}
	})
	t.Run("types stay where a type belongs", func(t *testing.T) {
		// Type arguments, composite literals, conversions and literals.
		for _, src := range []string{
			"let x = Map[string, []int]{}\n",
			"let x = Option[func() int]{}\n",
			"let x = List[*T]{}\n",
			"let x = xs[[]int{1}[0]]\n",
			"let x = m[func() int { 1 }()]\n",
			"let x = []int{1, 2}\n",
			"let x = int64(y)\n",
			"let f = func(x) { x }\n",
			"func main() {\n\trun(func() needs A, B { g() })\n}\n",
		} {
			if _, err := Parse(src); err != nil {
				t.Errorf("%q: %v", src, err)
			}
		}
	})
}

// TestRoundFiveForms: the round-4 review's small items.
func TestRoundFiveForms(t *testing.T) {
	t.Run("m1 a bare Map type in value position", func(t *testing.T) {
		for _, name := range []string{"map-type-value", "map-type-argument"} {
			src, e := malformedError(t, name)
			if e.Message != "a type is not a value; write a literal or a conversion" || !strings.HasPrefix(src[e.Offset:], "Map[") {
				t.Errorf("%s: %v at %q, want the type message at Map", name, e, src[e.Offset:])
			}
		}
	})
	t.Run("O1 a composite literal in a header", func(t *testing.T) {
		for _, c := range []struct{ name, at, msg string }{
			{"header-keyed-literal", "Cfg{", "write (Cfg{…}), or bind it with let first"},
			{"header-empty-literal", "cfg.Cfg{", "write (cfg.Cfg{…}), or bind it with let first"},
			{"header-positional-literal", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-names", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-one", "Box{", "write (Box{…}), or bind it with let first"},
			// Round 7 review m1: a complete element, not only one token.
			{"header-positional-unary", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-call", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-selector-index", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-paren", "Point{", "write (Point{…}), or bind it with let first"},
			{"header-positional-one-call", "Box{", "write (Box{…}), or bind it with let first"},
		} {
			src, e := malformedError(t, c.name)
			if !strings.HasSuffix(e.Message, c.msg) || !strings.HasPrefix(src[e.Offset:], c.at) {
				t.Errorf("%s: %v at %q, want %q at %q", c.name, e, src[e.Offset:], c.msg, c.at)
			}
		}
		// A label on the body's next line keeps the label habit.
		_, err := Parse("func main() {\n\tfor x in xs {\n\t\tdone: f()\n\t}\n}\n")
		if err == nil || !strings.Contains(err.Error(), "Ergo has no labels") {
			t.Errorf("label in a body: %v", err)
		}
		// A body whose first statement holds a comma only inside brackets,
		// or starts with a keyword, is a body.
		for _, src := range []string{
			"func main() {\n\tif ok {\n\t\tuse(1)\n\t}\n}\n",
			"func main() {\n\tif ok { use(a, b) }\n}\n",
			"func main() {\n\tif ok { xs[f(a, b)] = g(c, d) }\n}\n",
			"func main() {\n\tif ok { use(Point{1, 2}) }\n}\n",
			"func f() int {\n\tif ok { return g(1, 2) }\n\treturn 3\n}\n",
			"func main() {\n\tif ok { let p = Point{-1, 2} }\n}\n",
			"func main() {\n\tif ok { run() { use(a, b) } }\n}\n",
		} {
			if _, err := Parse(src); err != nil {
				t.Errorf("%q: %v", src, err)
			}
		}
	})
	t.Run("Map stays a literal and a type argument", func(t *testing.T) {
		for _, src := range []string{
			"let m = Map[string, int]{}\n",
			"let m = Option[Map[string, int]]{}\n",
			"let m = Pair[Map[string, int], int]{}\n",
			"let x = xs[Map[string, int]{}[\"a\"]]\n",
			"func main() {\n\tfor k, v in (Map[string, int]{}) {\n\t\tuse(k, v)\n\t}\n}\n",
			"func main() {\n\twith (Cfg{Port: 1}) {\n\t\tuse(1)\n\t}\n}\n",
		} {
			if _, err := Parse(src); err != nil {
				t.Errorf("%q: %v", src, err)
			}
		}
	})
}

// TestNestingLimit: every recursive construct, and every chain that
// deepens the tree (selectors, calls, indexes, binary operators, else if),
// charges the nesting limit, so a tree deeper than MaxNesting is S001
// before any consumer walks it (go/parser charges the same).
func TestNestingLimit(t *testing.T) {
	chains := []struct {
		name       string
		src        func(n int) string
		deep, flat int
	}{
		{"parentheses", func(n int) string {
			return "let x = " + strings.Repeat("(", n) + "1" + strings.Repeat(")", n) + "\n"
		}, MaxNesting, 400},
		{"selectors", func(n int) string { return "let x = a" + strings.Repeat(".a", n) + "\n" }, MaxNesting, 900},
		{"calls", func(n int) string { return "let x = f" + strings.Repeat("()", n) + "\n" }, MaxNesting, 900},
		{"indexes", func(n int) string { return "let x = a" + strings.Repeat("[0]", n) + "\n" }, MaxNesting, 900},
		{"binary operators", func(n int) string { return "let x = 1" + strings.Repeat(" + 1", n) + "\n" }, MaxNesting, 900},
		{"unary operators", func(n int) string { return "let x = " + strings.Repeat("!", n) + "x\n" }, MaxNesting, 900},
		{"else if", func(n int) string {
			return "func f() {\n\tif x {\n\t}" + strings.Repeat(" else if x {\n\t}", n) + "\n}\n"
		}, MaxNesting, 900},
		{"blocks", func(n int) string {
			return "func f() {" + strings.Repeat(" if x {", n) + strings.Repeat(" }", n) + " }\n"
		}, MaxNesting, 300},
		{"slice types", func(n int) string { return "let x " + strings.Repeat("[]", n) + "int\n" }, MaxNesting, 900},
	}
	for _, c := range chains {
		if _, err := Parse(c.src(c.flat)); err != nil {
			t.Errorf("%s: %d levels: %v", c.name, c.flat, err)
		}
		_, err := Parse(c.src(c.deep + 1))
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeSyntax || !strings.Contains(e.Message, "nesting deeper than") {
			t.Errorf("%s: %d levels: %v, want the nesting S001", c.name, c.deep+1, err)
		}
	}
}

// nestingAt parses src and returns the offset of its nesting S001, or -1
// when src parses. Any other result fails the test.
func nestingAt(t *testing.T, src string) int {
	t.Helper()
	_, err := Parse(src)
	if err == nil {
		return -1
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeSyntax || !strings.Contains(e.Message, "nesting deeper than") {
		t.Fatalf("%v, want the nesting S001 or a parse", err)
	}
	return e.Offset
}

// TestNestingLimitBoundsTheTree: the limit bounds the height of the
// returned tree, not only the path the parser has open. A chain whose
// operand is itself deep counts the operand in full, and the S001 is at the
// first token past the limit.
func TestNestingLimitBoundsTheTree(t *testing.T) {
	// `let x = E` opens one level for E; `( … )` opens two (the operand and
	// the expression inside). So `(a` + 900 links + `)` + k links is
	// 1 + 2 + 900 + k levels: k = 97 is exactly the limit.
	chain := "let x = (a" + strings.Repeat(".a", 900) + ")"
	if at := nestingAt(t, chain+strings.Repeat(".a", 97)+"\n"); at != -1 {
		t.Fatalf("1000 levels: S001 at %d", at)
	}
	if at, want := nestingAt(t, chain+strings.Repeat(".a", 900)+"\n"), len(chain)+2*97; at != want {
		t.Fatalf("1803 levels: S001 at %d, want %d (the 98th link after the parenthesis)", at, want)
	}

	// 32 groups of 900 links, each wrapped in parentheses and extended.
	x := "a" + strings.Repeat(".a", 900)
	for range 31 {
		x = "(" + x + ")" + strings.Repeat(".a", 900)
	}
	if at := nestingAt(t, "let x = "+x+"\n"); at == -1 {
		t.Fatal("the 32-group mixed chain parsed")
	}

	// A binary operand counts in full too, and so does a deep argument.
	for _, src := range []string{
		"let x = (" + strings.Repeat("!", 600) + "a)" + strings.Repeat(" + a", 600) + "\n",
		"let x = f(" + strings.Repeat("(", 300) + "a" + strings.Repeat(")", 300) + ")" + strings.Repeat("[0]", 500) + "\n",
		"let x = " + strings.Repeat("!", 500) + "(a" + strings.Repeat(" + a", 500) + ")\n",
	} {
		if nestingAt(t, src) == -1 {
			t.Errorf("parsed: %.60s…", src)
		}
	}

	// The open path alone never refuses a tree within the limit: the
	// argument of the last of 900 links sits one level below the call.
	shallow := "let x = a" + strings.Repeat(".a", 900) + "(" + strings.Repeat("(", 200) + "a" + strings.Repeat(")", 200) + ")\n"
	if at := nestingAt(t, shallow); at != -1 {
		t.Fatalf("a 902-level tree: S001 at %d", at)
	}
}

// TestNestingLimitAtHeadersAndOperators: the body brace of a control clause
// is a sibling of its header, never a link of the header's chain; a prefix
// operator past the limit is the S001's token.
func TestNestingLimitAtHeadersAndOperators(t *testing.T) {
	// The block, the statement and the header expression are 3 levels, so
	// 997 links are exactly the limit and the 998th link is past it.
	for _, keyword := range []string{"if", "match", "for"} {
		prefix := "func f() { " + keyword + " a"
		for _, links := range []int{996, 997} {
			if at := nestingAt(t, prefix+strings.Repeat(".a", links)+" {} }\n"); at != -1 {
				t.Errorf("%s with %d links: S001 at %d", keyword, links, at)
			}
		}
		if at, want := nestingAt(t, prefix+strings.Repeat(".a", 998)+" {} }\n"), len(prefix)+2*997; at != want {
			t.Errorf("%s with 998 links: S001 at %d, want %d (the 998th link)", keyword, at, want)
		}
	}
	// `let x = E` opens 1 level and each prefix operator 1, so the 1000th
	// operator is past the limit.
	prefix := "let x = "
	for _, op := range []string{"! ", "- ", "+ ", "^ ", "* ", "& "} {
		if at := nestingAt(t, prefix+strings.Repeat(op, 999)+"a\n"); at != -1 {
			t.Errorf("%q × 999: S001 at %d", op, at)
		}
		if at, want := nestingAt(t, prefix+strings.Repeat(op, 1000)+"a\n"), len(prefix)+len(op)*999; at != want {
			t.Errorf("%q × 1000: S001 at %d, want %d (the 1000th operator)", op, at, want)
		}
	}
}

// genNested renders a random expression of postfix links, binary and unary
// operators and parentheses, and returns its height in levels: a link,
// operator or unary operator is one level, parentheses are two, and an
// argument or index is one more below its link (grammar.md, Parser rules).
// class is 0 for a primary expression, 1 for a unary one, 2 for a binary
// one; a lower class is needed in some operand positions.
func genNested(r *rand.Rand, budget int) (src string, height, class int) {
	paren := func(s string, h, c, limit int) (string, int) {
		if c > limit {
			return "(" + s + ")", h + 2
		}
		return s, h
	}
	if budget < 3 || r.IntN(8) == 0 {
		return "a", 0, 0
	}
	run := 1 + r.IntN(budget/2)
	switch r.IntN(6) {
	case 0:
		s, h, _ := genNested(r, budget-2)
		return "(" + s + ")", h + 2, 0
	case 1:
		s, h := paren(genNestedAt(r, budget-run, 0))
		return s + strings.Repeat(".a", run), h + run, 0
	case 2, 3:
		s, h := paren(genNestedAt(r, budget/2, 0))
		a, ha, _ := genNested(r, budget/2)
		open, close := "(", ")"
		if r.IntN(2) == 0 {
			open, close = "[", "]"
		}
		return s + open + a + close, 1 + max(h, 1+ha), 0
	case 4:
		s, h := paren(genNestedAt(r, budget-run, 1))
		return strings.Repeat("!", run) + s, h + run, 1
	default:
		s, h, _ := genNested(r, budget/2)
		var b strings.Builder
		b.WriteString(s)
		for range 1 + r.IntN(4) {
			y, hy := paren(genNestedAt(r, budget/4, 1))
			b.WriteString(" + " + y)
			h = max(h, hy) + 1
		}
		return b.String(), h, 2
	}
}

// genNestedAt is genNested with the class limit its caller needs, in the
// argument order of the caller's paren helper.
func genNestedAt(r *rand.Rand, budget, limit int) (string, int, int, int) {
	s, h, c := genNested(r, budget)
	return s, h, c, limit
}

// TestNestingLimitOnCompositions: for random compositions of links,
// operators and parentheses, a tree within the limit parses and one level
// more is S001 at the first token past the limit.
func TestNestingLimitOnCompositions(t *testing.T) {
	r := rand.New(rand.NewPCG(195, 3))
	exact, over := 0, 0
	// Each composition is a declaration's value (1 level above it) or a
	// control clause header (block, statement and header: 3 levels).
	contexts := []struct {
		open, close string
		above       int
	}{
		{"let x = ", "\n", 1},
		{"func f() { if ", " {} }\n", 3},
		{"func f() { match ", " {} }\n", 3},
		{"func f() { for ", " {} }\n", 3},
	}
	for i := range 400 {
		ctx := contexts[i%len(contexts)]
		s, h, c := genNested(r, 600+r.IntN(4400))
		if c > 0 {
			s, h = "("+s+")", h+2
		}
		// k more links reach the limit.
		prefix := ctx.open + s
		if ctx.above+h > MaxNesting {
			if nestingAt(t, prefix+ctx.close) == -1 {
				t.Fatalf("%d levels parsed: %.80s…", ctx.above+h, s)
			}
			over++
			continue
		}
		k := MaxNesting - ctx.above - h
		if at := nestingAt(t, prefix+strings.Repeat(".a", k)+ctx.close); at != -1 {
			t.Fatalf("%d levels: S001 at %d in %.80s…", MaxNesting, at, s)
		}
		if at, want := nestingAt(t, prefix+strings.Repeat(".a", k+1)+ctx.close), len(prefix)+2*k; at != want {
			t.Fatalf("%d levels: S001 at %d, want %d (the link past the limit) in %.80s…", MaxNesting+1, at, want, s)
		}
		exact++
	}
	if exact < 100 || over < 50 {
		t.Fatalf("the generator reached the limit %d times and passed it %d times", exact, over)
	}
	t.Logf("reached the limit exactly %d times, passed it %d times", exact, over)
}
