package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Stage E2, C16 and C17 (escaping.go): a non-escaping function value is
// called, passed to a non-escaping parameter or bound to a local, and
// captured only by a non-escaping frame.
//
// The domain is every position of the syntax where a local's name can
// stand: every field of type Expr, []Expr, *Ident or []*Ident of every
// node type, read from the syntax package's source
// (TestEscapePositionsCoverTheSyntax). Each position has programs that put
// f, a non-escaping parameter, there, with their verdicts, or a reason no
// checked program puts a use of a local there. A node type or field the
// decided parser never builds (node_inventory_test.go) is outside the
// domain by that table's reason.

const escHead = `import task

type Holder { Run escaping func(int) int }
type Box[T] { V T }
type F func(int) int
type Runner {
	Run(g func(int) int) int,
	Keep(g escaping func(int) int) int,
}
type Svc { Get() int }

func take(g escaping func(int) int) int { 0 }
func pass(g func(int) int) int { g(1) }
func gen[T](x T) int { 0 }
func later(body escaping func() int) int { 0 }
func now(body func() int) int { body() }
func give(g func(int) int) func(int) int { func(x int) int { x } }
func each(body func(k func(int) int) int) int { body(func(x int) int { x }) }

`

// escCase puts f, a non-escaping parameter of type func(int) int, at the
// position marked $. ok is the verdict: true when the checker admits the
// program; false when it is refused, by one non-escaping S108 at the
// line holding $ when want is empty, or with want as the first code when
// the position refuses f for another reason first.
type escCase struct {
	src  string
	ok   bool
	want string
}

var escPositions = map[string][]escCase{
	"CallExpr.Fun": {
		{src: "func u(f func(int) int) int {\n\t$f(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$f(1) + pass(f)\n}\n", ok: true},
	},
	"CallExpr.Args": {
		{src: "func u(f func(int) int) int {\n\t$pass(f)\n}\n", ok: true},
		{src: "func u(f func(int) int, k func(g func(int) int) int) int {\n\t$k(f)\n}\n", ok: true},
		{src: "func u(f func(int) int, r Runner) int {\n\t$r.Run(f)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$take(f)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$gen(f)\n}\n"},
		{src: "func u(f func(int) int, r Runner) int {\n\t$r.Keep(f)\n}\n"},
		{src: "func u(f func(int) int, k func(g escaping func(int) int) int) int {\n\t$k(f)\n}\n"},
		{src: "func u(f func(int) int) F {\n\t$F(f)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$len(f)\n}\n", want: "S108"},
	},
	"ParenExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$(f)(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$take((f))\n}\n"},
	},
	"CompositeLit.Elts": {
		{src: "func u(f func(int) int) int {\n\t$let xs = []func(int) int{f}\n\txs[0](1)\n}\n"},
	},
	"KeyValueExpr.Value": {
		{src: "func u(f func(int) int) Holder {\n\t$Holder{Run: f}\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$Box{V: f}.V(1)\n}\n"},
	},
	"ReturnStmt.Results": {
		{src: "func u(f func(int) int) func(int) int {\n\t$return f\n}\n"},
	},
	"ExprStmt.X": {
		{src: "func u(f func(int) int) func(int) int {\n\t$f\n}\n"},
		{src: "func u(c bool, f func(int) int) func(int) int {\n\tif c {\n\t\treturn give(f)\n\t}\n\t$f\n}\n"},
	},
	"TupleExpr.Elts": {
		{src: "func u(f func(int) int) (func(int) int, int) {\n\t$(f, 1)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$let (g, n) = (f, 1)\n\tg(n)\n}\n"},
	},
	"AssignStmt.Rhs": {
		{src: "func u(f func(int) int, g func(int) int) int {\n\tlet mut h = g\n\t$h = f\n\th(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\tlet mut h = func(x int) int { x }\n\t$h = f\n\th(1)\n}\n"},
		// The target outlives the value: k lives only while each runs.
		{src: "func u(f func(int) int) int {\n\tlet mut h = f\n\teach(func(k func(int) int) int {\n$\t\th = k\n\t\t0\n\t})\n\th(1)\n}\n", want: ""},
		// A discard keeps nothing.
		{src: "func u(f func(int) int) int {\n\t$_ = f\n\t0\n}\n", ok: true},
	},
	"AssignStmt.Lhs": {
		{src: "func u(f func(int) int, g func(int) int) int {\n\tlet mut h = g\n\t$h = f\n\th(1)\n}\n", ok: true},
		{src: "func u(f func(int) int, g func(int) int) int {\n\t$f = g\n\tf(1)\n}\n", want: "S108"},
	},
	"VarSpec.Values": {
		{src: "func u(f func(int) int) int {\n\t$let g = f\n\tpass(g)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$let _ = f\n\t0\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$let g func(int) int = f\n\tg(1)\n}\n"},
		{src: "func u(f func(int) int) int {\n\tlet g = f\n\t$take(g)\n}\n"},
	},
	// A dot call's receiver has the named type its function is declared
	// on (196m), so f.pass() is no call of pass.
	"SelectorExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$f.pass()\n}\n", want: "S108"},
		{src: "func u(f func(int) int) int {\n\t$f.V\n}\n", want: "S108"},
	},
	"IndexExpr.X":       {{src: "func u(f func(int) int) int {\n\t$f[0]\n}\n", want: "S108"}},
	"IndexExpr.Indices": {{src: "func u(f func(int) int, xs []int) int {\n\t$xs[f]\n}\n", want: "S108"}},
	"SliceExpr.X":       {{src: "func u(f func(int) int) int {\n\t$len(f[0:1])\n}\n", want: "S108"}},
	"SliceExpr.Low":     {{src: "func u(f func(int) int, xs []int) int {\n\t$len(xs[f:])\n}\n", want: "S108"}},
	"SliceExpr.High":    {{src: "func u(f func(int) int, xs []int) int {\n\t$len(xs[:f])\n}\n", want: "S108"}},
	"TypeGuard.X": {
		{src: "func u(f func(int) int) int {\n\t$match f {\n\tcase _:\n\t\t1\n\t}\n}\n"},
	},
	"StarExpr.X":  {{src: "func u(f func(int) int) int {\n\t$*f\n}\n", want: "S108"}},
	"UnaryExpr.X": {{src: "func u(f func(int) int) bool {\n\t$!f\n}\n", want: "S108"}},
	"BinaryExpr.X": {
		{src: "func u(f func(int) int) bool {\n\t$f == f\n}\n", want: "S108"},
	},
	"BinaryExpr.Y": {
		{src: "func u(f func(int) int) int {\n\t$1 + f\n}\n", want: "S108"},
	},
	"TryExpr.X": {{src: "func u(f func(int) int) int {\n\t$try f\n}\n", want: "S108"}},
	"CatchExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$f catch {\n\tcase _:\n\t\t0\n\t}\n}\n", want: "S108"},
	},
	"IfStmt.Cond":  {{src: "func u(f func(int) int) int {\n\t$if f {\n\t\t1\n\t} else {\n\t\t2\n\t}\n}\n", want: "S108"}},
	"ForStmt.Cond": {{src: "func u(f func(int) int) int {\n\t$for f {\n\t}\n\t0\n}\n", want: "S108"}},
	"RangeStmt.X":  {{src: "func u(f func(int) int) int {\n\t$for x in f {\n\t}\n\t0\n}\n", want: "S108"}},
	"CaseClause.List": {
		{src: "func u(f func(int) int, n int) int {\n\tmatch n {\n\t$case f:\n\t\t1\n\tcase _:\n\t\t2\n\t}\n}\n", want: "S108"},
	},
	"AsPattern.X": {
		{src: "func u(f func(int) int, n int) int {\n\tmatch n {\n\t$case f as m:\n\t\tm\n\tcase _:\n\t\t2\n\t}\n}\n", want: "S108"},
	},
	"AssertStmt.X": {
		// assert stands in a test body (stage E3a); a function value is
		// not a bool there, whatever its escaping verdict.
		{src: "func g(n int) int {\n\tn\n}\n\ntest T {\n\t$assert g\n}\n", want: "S108"},
	},
	"InterpolatedString.Exprs": {
		// A function part has no text form (E6a): the text-form S108, and
		// no second, non-escaping S108 for the same part.
		{src: "func u(f func(int) int) string {\n\t$\"\\(f)\"\n}\n", want: "S108"},
	},
	"KeyValueExpr.Key": {
		{src: "func u(f func(int) int) int {\n\t$len(Map[func(int) int, int]{f: 1})\n}\n", want: "S108"},
	},
	"ContextEntry.Value": {
		{src: "func u(f func(int) int) int {\n\t$with context { Svc: f } {\n\t\tSvc.Get()\n\t}\n}\n", want: "S106"},
	},
}

// escFrames are the captures: f read inside a frame nested in u's body.
var escFrames = []struct {
	name string
	src  string
	ok   bool
}{
	{"literal stored", "func u(f func(int) int) int {\n\tlet k = func() int { $f(1) }\n\tk()\n}\n", false},
	{"literal passed to a non-escaping parameter", "func u(f func(int) int) int {\n\tnow(func() int { $f(1) })\n}\n", true},
	{"literal passed to an escaping parameter", "func u(f func(int) int) int {\n\tlater(func() int { $f(1) })\n}\n", false},
	{"literal passed to a generic parameter", "func u(f func(int) int) int {\n\tgen(func() int { $f(1) })\n}\n", false},
	{"literal called at once", "func u(f func(int) int) int {\n\t(func() int { $f(1) })()\n}\n", true},
	{"literal returned", "func u(f func(int) int) func() int {\n\tfunc() int { $f(1) }\n}\n", false},
	{"trailing block of a non-escaping parameter", "func u(f func(int) int) int {\n\tnow() { $f(1) }\n}\n", true},
	{"trailing block of an escaping parameter", "func u(f func(int) int) int {\n\tlater() { $f(1) }\n}\n", false},
	{"task.Go block", "type Fault {}\nfunc u(f func(int) int) Task[int, Fault] {\n\ttask.Go() { $f(1) }\n}\n", false},
	{"task.Go literal", "type Fault {}\nfunc u(f func(int) int) Task[int, Fault] {\n\ttask.Go(func() int { $f(1) })\n}\n", false},
	{"nested non-escaping frames", "func u(f func(int) int) int {\n\tnow() {\n\t\tnow(func() int { $f(1) })\n\t}\n}\n", true},
	{"non-escaping inside escaping", "func u(f func(int) int) int {\n\tlater() {\n\t\tnow() { $f(1) }\n\t}\n}\n", false},
	{"escaping inside non-escaping", "func u(f func(int) int) int {\n\tnow() {\n\t\tlater() { $f(1) }\n\t}\n}\n", false},
	{"a literal's own non-escaping parameter", "func u() int {\n\tlet k = func(f func(int) int) int { now() { $f(1) } }\n\tk(func(x int) int { x })\n}\n", true},
	{"a stored literal's own parameter, called in it", "func u() int {\n\tlet k = func(f func(int) int) int { $f(1) }\n\tk(func(x int) int { x })\n}\n", true},
	{"an untyped literal parameter takes the expected type", "func u() int {\n\teach(func(k) { now() { $k(1) } })\n}\n", true},
	{"an untyped literal parameter captured by an escaping block", "func u() int {\n\teach(func(k) { later() { $k(1) } })\n}\n", false},
}

func escLine(head, src string) int {
	return strings.Count(head, "\n") + 1 + strings.Count(src[:strings.IndexByte(src, '$')], "\n")
}

func escCheck(t *testing.T, name, src string, ok bool, want string) {
	t.Helper()
	head := escHead
	if !strings.Contains(src, "task.") {
		head = strings.Replace(head, "import task\n", "", 1)
	}
	line := escLine(head, src)
	full := head + strings.Replace(src, "$", "", 1)
	_, diags := checkFor(full)
	switch {
	case ok && len(diags) == 0:
		return
	case !ok && want == "" && len(diags) == 1 && diags[0].Code == CodeType && diags[0].Line == line &&
		strings.Contains(diags[0].Message, "non-escaping function value"):
		return
	case !ok && want != "" && len(diags) >= 1 && diags[0].Code == want && diags[0].Line == line:
		return
	}
	verdict := "a clean check"
	switch {
	case !ok && want == "":
		verdict = "one non-escaping S108 at line " + strconv.Itoa(line)
	case !ok:
		verdict = want + " at line " + strconv.Itoa(line)
	}
	t.Errorf("%s: want %s; got %v\n%s", name, verdict, diags, full)
}

func TestEscapingPositions(t *testing.T) {
	for pos, cases := range escPositions {
		for i, c := range cases {
			escCheck(t, pos+"#"+strconv.Itoa(i), c.src, c.ok, c.want)
		}
	}
}

func TestEscapingFrames(t *testing.T) {
	for _, c := range escFrames {
		escCheck(t, c.name, c.src, c.ok, "")
	}
}

// TestEscapingMarker: `escaping` is admitted on every function type; it
// changes the type only on a parameter, where the two spellings are
// distinct types, so a function value cannot launder a non-escaping
// argument through a parameter that keeps it.
func TestEscapingMarker(t *testing.T) {
	for _, src := range []string{
		"type H { Run escaping func() }\nfunc r() escaping func() { func() {} }\nfunc main() { let h = H{Run: r()}\n\th.Run() }\n",
		"type B[T] { V T }\nfunc main() { let b = B[escaping func()]{V: func() {}}\n\tb.V() }\n",
		"func keep(g escaping func()) H { H{Run: g} }\ntype H { Run func() }\nfunc main() { let k func(g escaping func()) H = keep\n\tk(func() {}).Run() }\n",
	} {
		if _, diags := checkFor(src); len(diags) != 0 {
			t.Errorf("want a clean check; got %v\n%s", diags, src)
		}
	}
	// The laundering route: keep's parameter is escaping, k's is not.
	src := "type H { Run func() }\nfunc keep(g escaping func()) H { H{Run: g} }\nfunc main() {\n\tlet k func(g func()) H = keep\n\tk(func() {}).Run()\n}\n"
	_, diags := checkFor(src)
	if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "escaping func()") {
		t.Errorf("want one S108 at line 4 naming the escaping parameter; got %v\n%s", diags, src)
	}
}

// TestEscapingAddsNothingToAnUncheckedCall: a call whose callee is in error
// is never checked, so it has no Calls entry; its argument and its literal
// add no diagnostic to the callee's own.
func TestEscapingAddsNothingToAnUncheckedCall(t *testing.T) {
	src := "type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc a(run func(func()), r func(func())) {\n\tr(func() { a(run, r) })\n\ttry mark()\n}\n"
	_, diags := checkFor(src)
	if len(diags) != 1 || diags[0].Line != 3 || strings.Contains(diags[0].Message, "non-escaping") {
		t.Errorf("want only the recursive group's S108 at line 3; got %v\n%s", diags, src)
	}
}

// escUnreachable are the positions no checked program puts a use of a
// local in, each with its reason.
var escUnreachable = map[string]string{
	// Type positions: the walker skips a type node (typeNodes), and a type
	// holds no value.
	"Field.Type":           "a parameter's, result's or field's type",
	"VarSpec.Type":         "a let's written type",
	"TypeDecl.Spec":        "a type declaration's body",
	"CompositeLit.Type":    "a composite literal's type",
	"MapType.Key":          "a Map type's key type",
	"MapType.Value":        "a Map type's element type",
	"ArrayType.Elt":        "a slice type's element type",
	"InterfaceType.Union":  "a constraint's type set",
	"ErrorRow.Types":       "an error row lists types",
	"CatchArm.Types":       "a catch arm lists error types",
	"NeedsClause.Names":    "a needs clause names capability types",
	"ProvidesClause.Names": "a provides clause names capability types",
	"MachineType.State":    "a machine's state type",
	"MachineType.Signal":   "a machine's signal type",
	"ContextEntry.Key":     "a context entry's key names a capability type",
	"ContextEntry.Keys":    "a context entry's further keys name capability types",
	// A machine is a type declaration's body (resolveNamed): its entries
	// are read at the package level, where no local is in scope.
	"MachineType.Initial": "a machine's initial value is a package-level expression",
	"MachineType.Step":    "a machine's step function is a package-level expression",
	"MachineEntry.Value":  "a machine entry is a package-level expression",
	// Binding positions declare names (Info.Defs); none is a use.
	"VarSpec.Pattern":  "a let's pattern binds names",
	"RangeStmt.Key":    "a for loop's key binds a name",
	"RangeStmt.Value":  "a for loop's value binds a name",
	"VarSpec.Names":    "a let binds names",
	"AsPattern.Name":   "`as` binds a name",
	"Field.Names":      "a parameter's or field's name",
	"FuncDecl.Name":    "a function's name",
	"TypeDecl.Name":    "a type's name",
	"ContextDecl.Name": "a context's name",
	"ImportSpec.Name":  "an import's name",
	"TestDecl.Name":    "a test's name",
	"LawDecl.Name":     "a law's name",
	"VariantSpec.Name": "a variant's name",
	"SelectorExpr.Sel": "a selector names a field, an operation or a package member, never a local",
	"SliceExpr.Max":    "a three-index slice does not parse (S001)",
	"SwitchStmt.Tag":   "an expression switch is a match on a value: a TypeSwitchStmt whose subject is a TypeGuard",
	"paramEntry.typ":   "the parser's own record of a parameter, not a node of the tree",
	"paramEntry.name":  "the parser's own record of a parameter, not a node of the tree",
}

// escNotBuilt are the node types the decided parser never builds
// (node_inventory_test.go, notBuiltFamily, in the external test package).
// Stage 5 (D10) deleted them from the syntax package, so the list is
// empty.
var escNotBuilt = []string{}

// escDomain reads the positions where a local's name can stand: every
// field of type Expr, []Expr, *Ident or []*Ident of every struct of the
// syntax package, less the node types the decided parser never builds
// (escNotBuilt).
func escDomain(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../syntax/*.go")
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return false
			}
			if slices.Contains(escNotBuilt, ts.Name.Name) {
				return false
			}
			for _, fl := range st.Fields.List {
				typ := fl.Type
				if a, ok := typ.(*ast.ArrayType); ok {
					typ = a.Elt
				}
				if s, ok := typ.(*ast.StarExpr); ok {
					if id, ok := s.X.(*ast.Ident); ok && id.Name == "Ident" {
						typ = id
					}
				}
				if id, ok := typ.(*ast.Ident); !ok || id.Name != "Expr" && id.Name != "Ident" {
					continue
				}
				for _, name := range fl.Names {
					fields = append(fields, ts.Name.Name+"."+name.Name)
				}
			}
			return false
		})
	}
	sort.Strings(fields)
	return fields
}

// TestEscapePositionsCoverTheSyntax is the structural guard: a position
// the syntax gains has a program or a reason before the suite passes.
func TestEscapePositionsCoverTheSyntax(t *testing.T) {
	fields := escDomain(t)
	if len(fields) < 40 {
		t.Fatalf("found %d positions; the guard reads the syntax package", len(fields))
	}
	for _, f := range fields {
		if len(escPositions[f]) == 0 && escUnreachable[f] == "" {
			t.Errorf("the position %s has no program and no reason it holds no use of a local", f)
		}
		if len(escPositions[f]) > 0 && escUnreachable[f] != "" {
			t.Errorf("the position %s has both programs and a reason", f)
		}
	}
	for f := range escUnreachable {
		if !slices.Contains(fields, f) {
			t.Errorf("escUnreachable lists %s, which is no position", f)
		}
	}
	for f := range escPositions {
		if !slices.Contains(fields, f) {
			t.Errorf("escPositions lists %s, which is no position", f)
		}
	}
}
