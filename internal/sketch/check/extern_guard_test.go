package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The classes of TestExternPlacementGuard (check-v1 E4, A4).
const (
	// The extern there is a direct call: no diagnostic.
	guardCallee = "callee"
	// The extern there is a value: since G1 it gets the diagnostics an
	// ordinary function of its signature gets there (externGuardControl),
	// and never the P3 message.
	guardValue = "value"
	// A type position: exactly one diagnostic, and not the P3 message.
	guardType = "type"
	// A name that is not a use of the extern: a name being bound (a let, a
	// parameter, a pattern, a declaration's name), or a name that resolves
	// outside Ergo's value scope (a selector's member, an import path, an
	// extern's Go target, a string's text). Never the P3 message.
	guardBinder = "binder"
	// The decided parser never builds the field: node_inventory_test.go
	// lists it in notBuiltFields, or its node type in the not-built family,
	// and TestDecidedNodeInventory proves that no decided row builds it.
	guardNotBuilt = "notBuilt"
)

// externGuardDecl declares the extern every row refers to, after the row's
// program, unless the row declares an extern itself.
const externGuardDecl = "extern go strings.ToUpper func Upper(s string) string\n"

// externGuardControl is the control of a value row: an ordinary function
// with the extern's name and signature, on one line as the extern is.
const externGuardControl = "func Upper(s string) string { s }\n"

// externPlaceRow is one Expr field of the syntax package: its class and a
// program with the extern's name in that field, marked by @ before it.
type externPlaceRow struct {
	class   string
	program string
}

// externPlaceRows has exactly one row for each field externExprFields
// derives.
var externPlaceRows = map[string]externPlaceRow{
	// The callee of a call: a direct call.
	"CallExpr.Fun": {guardCallee, "func main() { _ = @Upper(\"x\") }\n"},

	// Values.
	"AssertStmt.X":             {guardValue, "test T {\n\tassert @Upper\n}\n"},
	"AssignStmt.Lhs":           {guardValue, "func main() {\n\t@Upper = upper\n}\nfunc upper(s string) string { s }\n"},
	"AssignStmt.Rhs":           {guardValue, "func main() {\n\t_ = @Upper\n}\n"},
	"BinaryExpr.X":             {guardValue, "func main() {\n\tlet k = 1\n\t_ = @Upper == k\n}\n"},
	"BinaryExpr.Y":             {guardValue, "func main() {\n\tlet k = 1\n\t_ = k == @Upper\n}\n"},
	"CallExpr.Args":            {guardValue, "func take(f func(string) string) {}\nfunc main() {\n\ttake(@Upper)\n}\n"},
	"CatchExpr.X":              {guardValue, "type Bang {}\nfunc main() {\n\tlet v = @Upper catch {\n\tcase Bang: return\n\t}\n\t_ = v\n}\n"},
	"CompositeLit.Elts":        {guardValue, "func main() {\n\t_ = []func(string) string{@Upper}\n}\n"},
	"CompositeLit.Base":        {guardValue, "type Box { F int }\nfunc main() {\n\t_ = Box{..@Upper, F: 1}\n}\n"},
	"ContextEntry.Value":       {guardValue, "type C { M() }\nlet W = context { C: @Upper }\n"},
	"ExprStmt.X":               {guardValue, "func main() {\n\t@Upper\n}\n"},
	"ForStmt.Cond":             {guardValue, "func main() {\n\tfor @Upper { }\n}\n"},
	"IfStmt.Cond":              {guardValue, "func main() {\n\tif @Upper { }\n}\n"},
	"IndexExpr.Indices":        {guardValue, "func main() {\n\tlet xs = []int{1}\n\t_ = xs[@Upper]\n}\n"},
	"IndexExpr.X":              {guardValue, "func main() {\n\t_ = @Upper[0]\n}\n"},
	"InterpolatedString.Exprs": {guardValue, "func main() {\n\t_ = \"\\(@Upper)\"\n}\n"},
	"KeyValueExpr.Key":         {guardValue, "func main() {\n\t_ = Map[string, int]{@Upper: 1}\n}\n"},
	"KeyValueExpr.Value":       {guardValue, "type Holder { F func(string) string }\nfunc main() {\n\t_ = Holder{F: @Upper}\n}\n"},
	"MachineEntry.Value":       {guardValue, machineGuard("initial: Idle,\n\tstep: @Upper,")},
	"MachineType.Initial":      {guardValue, machineGuard("initial: @Upper,\n\tstep: step,")},
	"MachineType.Step":         {guardValue, machineGuard("initial: Idle,\n\tstep: @Upper,")},
	"MachineType.After":        {guardValue, machineGuard("initial: Idle,\n\tstep: step,\n\tafter: @Upper,")},
	"MachineType.Work":         {guardValue, machineGuard("initial: Idle,\n\tstep: step,\n\twork: @Upper,")},
	"ParenExpr.X":              {guardValue, "func main() {\n\t_ = (@Upper)\n}\n"},
	"RangeStmt.X":              {guardValue, "func main() {\n\tfor x in @Upper { _ = x }\n}\n"},
	"ReturnStmt.Results":       {guardValue, "func get() func(string) string { return @Upper }\n"},
	"SelectorExpr.X":           {guardValue, "func main() {\n\t_ = @Upper.Len\n}\n"},
	"SliceExpr.X":              {guardValue, "func main() {\n\t_ = @Upper[0:1]\n}\n"},
	"SliceExpr.Low":            {guardValue, "func main() {\n\tlet xs = []int{1}\n\t_ = xs[@Upper:]\n}\n"},
	"SliceExpr.High":           {guardValue, "func main() {\n\tlet xs = []int{1}\n\t_ = xs[:@Upper]\n}\n"},
	"SliceExpr.Max":            {guardValue, "func main() {\n\tlet xs = []int{1}\n\t_ = xs[0:1:@Upper]\n}\n"},
	"SwitchStmt.Tag":           {guardValue, "func main() {\n\tlet m = match @Upper {\n\tcase \"a\": 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n"},
	"TryExpr.X":                {guardValue, "func main() {\n\tlet t = try @Upper\n\t_ = t\n}\n"},
	"TupleExpr.Elts":           {guardValue, "func main() {\n\tlet t = (@Upper, 1)\n\t_ = t\n}\n"},
	"TypeGuard.X":              {guardValue, typeMatchGuard},
	"TypeSwitchStmt.Subjects":  {guardValue, typeMatchGuard},
	"UnaryExpr.X":              {guardValue, "func main() {\n\t_ = !@Upper\n}\n"},
	"VarSpec.Values":           {guardValue, "func main() {\n\tlet v = @Upper\n\t_ = v\n}\n"},

	// Types.
	"ArrayType.Elt":        {guardType, "func f(xs []@Upper) {}\n"},
	"CompositeLit.Type":    {guardType, "func main() {\n\t_ = @Upper{}\n}\n"},
	"ContextEntry.Key":     {guardType, "type C { M() }\ntype P {}\nfunc M(_ P) {}\nlet W = context { @Upper: P }\n"},
	"ContextEntry.Keys":    {guardType, "type C { M() }\ntype P {}\nfunc M(_ P) {}\nlet W = context { C, @Upper: P }\n"},
	"ErrorRow.Types":       {guardType, "func f() fails @Upper {}\n"},
	"Field.Type":           {guardType, "func f(x @Upper) {}\n"},
	"FuncDecl.Type":        {guardType, "func f(x @Upper) {}\n"},
	"FuncLit.Type":         {guardType, "func main() {\n\t_ = func(x @Upper) {}\n}\n"},
	"InterfaceType.Union":  {guardType, "type A {}\ntype U = A | @Upper\n"},
	"MachineType.State":    {guardType, "type E = Ping\ntype Ping {}\ntype M machine[@Upper, E] { initial: 1, step: step }\nfunc step(s int, e E) Decision[int, int] { return Ignore }\n"},
	"MachineType.Signal":   {guardType, "type S = Idle\ntype Idle {}\ntype M machine[S, @Upper] { initial: Idle, step: step }\nfunc step(s S, e int) Decision[S, int] { return Ignore }\n"},
	"LawReach.Targets":     {guardType, machineGuard("initial: Idle,\n\tstep: step,\n\tlaw L reaches @Upper,")},
	"MapType.Key":          {guardType, "func f(m Map[@Upper, int]) {}\n"},
	"MapType.Value":        {guardType, "func f(m Map[int, @Upper]) {}\n"},
	"NeedsClause.Names":    {guardType, "func f() needs @Upper {}\n"},
	"ProvidesClause.Names": {guardType, "type C { M() }\ntype P {}\nfunc M(_ P) {}\nfunc f() { with context provides @Upper { C: P } { C.M() } }\n"},
	"StarExpr.X":           {guardType, "func f(p *@Upper) {}\n"},
	"TypeDecl.Spec":        {guardType, "type T = @Upper\n"},
	// A where reads only its subject and literals (refine.go): an extern
	// there is its one S108, as a name in a type position is.
	"TypeDecl.Where": {guardType, "type T int where @Upper > 0\n"},
	"Field.Where":    {guardType, "law L(x int where @Upper > 0) { true }\n"},
	"VarSpec.Type":   {guardType, "func main() {\n\tlet v @Upper\n\t_ = v\n}\n"},
	"CatchArm.Types": {guardType, "type Bang {}\nfunc load() int fails Bang { 1 }\nfunc f() int {\n\tload() catch {\n\tcase @Upper: 0\n\t}\n}\n"},

	// Names that are not a use. A pattern name binds, even where it
	// names a function (rows.md, Totality 1); a selector's member is a
	// field or method lookup, never the package's extern.
	"CaseClause.List": {guardBinder, "func main() {\n\tlet f = upper\n\tlet m = match f {\n\tcase @Upper: 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n" +
		"func upper(s string) string { s }\n"},
	"AsPattern.X": {guardBinder, "func main() {\n\tlet f = upper\n\tlet m = match f {\n\tcase @Upper as g: g(\"x\")\n\tcase _: \"y\"\n\t}\n\t_ = m\n}\n" +
		"func upper(s string) string { s }\n"},
	"SelectorExpr.Sel":         {guardBinder, "func main() {\n\t_ = \"x\".@Upper\n}\n"},
	"AsPattern.Name":           {guardBinder, "type A {}\ntype B {}\ntype U = A | B\nfunc f(u U) string {\n\tmatch u {\n\tcase A: \"a\"\n\tcase _ as @Upper: \"b\"\n\t}\n}\n"},
	"ContextDecl.Name":         {guardBinder, "type C { M() }\ntype P {}\nfunc M(_ P) {}\nlet @Upper = context { C: P }\n"},
	"ExternSpec.Target":        {guardBinder, "extern go strings.@Upper func Upper(s string) string\n"},
	"Field.Names":              {guardBinder, "func f(@Upper int) int { Upper }\n"},
	"FuncDecl.Name":            {guardBinder, "extern go strings.ToUpper func @Upper(s string) string\n"},
	"ImportSpec.Name":          {guardBinder, "import go @Upper strings\n"},
	"TakesClause.Names":        {guardBinder, "func f(Upper int) takes @Upper {}\n"},
	"ImportSpec.Path":          {guardBinder, "import @Upper\n"},
	"InterpolatedString.Texts": {guardBinder, "func main() {\n\t_ = \"@Upper \\(1)\"\n}\n"},
	"LawDecl.Name":             {guardBinder, "type C {\n\tGet() int,\n\n\tlaw @Upper(v int) {\n\t\tGet() == v\n\t},\n}\n"},
	"RangeStmt.Key":            {guardBinder, "func main() {\n\tfor @Upper, v in []int{1} { _ = Upper + v }\n}\n"},
	"RangeStmt.Value":          {guardBinder, "func main() {\n\tfor i, @Upper in []int{1} { _ = i + Upper }\n}\n"},
	"TestDecl.Name":            {guardBinder, "test @Upper {\n}\n"},
	"TypeDecl.Name":            {guardBinder, "type @Upper {}\n"},
	"VarSpec.Names":            {guardBinder, "func main() {\n\tlet @Upper = 1\n\t_ = Upper\n}\n"},
	"VarSpec.Pattern":          {guardBinder, "func main() {\n\tlet (@Upper, b) = (1, 2)\n\t_ = Upper + b\n}\n"},
	"VariantSpec.Name":         {guardBinder, "type U = @Upper{ ID string } | Pending{}\n"},
}

// machineGuard is a machine declaration with the given entries.
func machineGuard(entries string) string {
	return "type S = Idle\ntype Idle {}\ntype E = Ping\ntype Ping {}\ntype M machine[S, E] {\n\t" + entries + "\n}\n" +
		"func step(s S, e E) Decision[S, int] { return Ignore }\n"
}

// typeMatchGuard is a type match whose subject is the extern.
const typeMatchGuard = "type A {}\ntype B {}\nfunc main() {\n\tlet m = match @Upper {\n\tcase A: 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n"

// externExprFields derives, from the syntax sources, every struct field
// whose type is Expr, []Expr, or a pointer (or a slice of pointers) to a
// node type that implements Expr.
func externExprFields(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "syntax", "ast*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no syntax sources: %v", err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	exprs := map[string]bool{} // node types with an exprNode method
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "exprNode" || len(fd.Recv.List) != 1 {
				continue
			}
			if star, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok {
					exprs[id.Name] = true
				}
			}
		}
	}
	if len(exprs) == 0 {
		t.Fatal("no exprNode methods in the syntax sources")
	}
	pointsToExpr := func(x ast.Expr) bool {
		star, ok := x.(*ast.StarExpr)
		if !ok {
			return false
		}
		id, ok := star.X.(*ast.Ident)
		return ok && exprs[id.Name]
	}
	holds := func(x ast.Expr) bool {
		if id, ok := x.(*ast.Ident); ok {
			return id.Name == "Expr"
		}
		if arr, ok := x.(*ast.ArrayType); ok {
			if id, ok := arr.Elt.(*ast.Ident); ok {
				return id.Name == "Expr"
			}
			return pointsToExpr(arr.Elt)
		}
		return pointsToExpr(x)
	}
	derived := map[string]bool{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				if !holds(fld.Type) {
					continue
				}
				for _, name := range fld.Names {
					derived[ts.Name.Name+"."+name.Name] = true
				}
			}
			return true
		})
	}
	return derived
}

// guardFields walks the parsed file and reports, for each syntax field
// ("Type.Field") that holds a node around the byte offset off, whether it
// holds one, and whether one it holds starts at off.
func guardFields(file *syntax.File, off int) (holds, starts map[string]bool) {
	holds, starts = map[string]bool{}, map[string]bool{}
	nodeType := reflect.TypeOf((*syntax.Node)(nil)).Elem()
	seen := map[uintptr]bool{}
	var walk func(v reflect.Value)
	visit := func(label string, v reflect.Value) {
		if !v.IsValid() || (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) && v.IsNil() {
			return
		}
		n, ok := v.Interface().(syntax.Node)
		if !ok {
			return
		}
		if p, e := int(n.Pos()), int(n.End()); p >= 0 && p <= off && off < e {
			holds[label] = true
			if p == off {
				starts[label] = true
			}
		}
		walk(v)
	}
	walk = func(v reflect.Value) {
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			if v.Kind() == reflect.Pointer {
				if seen[v.Pointer()] {
					return
				}
				seen[v.Pointer()] = true
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			label := v.Type().Name() + "." + f.Name
			fv := v.Field(i)
			switch {
			case fv.Kind() == reflect.Slice:
				for j := 0; j < fv.Len(); j++ {
					visit(label, fv.Index(j))
				}
			case fv.Type().Implements(nodeType) || fv.Kind() == reflect.Interface:
				visit(label, fv)
			case fv.Kind() == reflect.Struct:
				walk(fv)
			}
		}
	}
	walk(reflect.ValueOf(file))
	return holds, starts
}

// TestExternPlacementGuard is A4 of check-v1 stage E4: every Expr field of
// the syntax package, derived from its sources, has exactly one row with a
// class; each row but notBuilt runs a program with the extern's name in
// that field, checked against the parsed tree, and its diagnostics against
// the class. A new Expr field fails until it has a row; a row whose field
// is gone fails too.
func TestExternPlacementGuard(t *testing.T) {
	derived := externExprFields(t)
	var missing, extra []string
	for k := range derived {
		if _, ok := externPlaceRows[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range externPlaceRows {
		if !derived[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("Expr fields with no extern placement row: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("extern placement rows with no Expr field: %v", extra)
	}
	classes := map[string]int{}
	for key, row := range externPlaceRows {
		classes[row.class]++
		t.Run(key, func(t *testing.T) {
			if row.class == guardNotBuilt {
				typ, _, _ := strings.Cut(key, ".")
				if notBuiltFields[key] == "" && nodeTable[typ].family != notBuiltFamily {
					t.Errorf("%s is notBuilt, and node_inventory_test.go lists neither the field nor its type as never built", key)
				}
				if row.program != "" {
					t.Errorf("a notBuilt row runs no program")
				}
				return
			}
			src := row.program
			if !strings.Contains(src, "extern go") {
				src += externGuardDecl // last: an import comes first
			}
			off := strings.Index(src, "@")
			if off < 0 || strings.Count(src, "@") != 1 {
				t.Fatalf("the program marks no single reference with @\n%s", src)
			}
			src = src[:off] + src[off+1:]
			if !strings.HasPrefix(src[off:], "Upper") {
				t.Fatalf("the marked name is not Upper\n%s", src)
			}
			file, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, src)
			}
			holds, starts := guardFields(file, off)
			if !holds[key] {
				t.Errorf("the marked name is not in %s (fields around it: %v)\n%s", key, sortedKeys(holds), src)
			}
			if (row.class == guardValue || row.class == guardCallee) && !starts[key] {
				t.Errorf("%s holds no node that starts at the marked name (fields that do: %v)\n%s", key, sortedKeys(starts), src)
			}
			_, diags := check.CheckSource(src)
			p3 := 0
			for _, d := range diags {
				if isP3(d.Message) {
					p3++
				}
			}
			line := strings.Count(src[:off], "\n") + 1
			switch row.class {
			case guardCallee:
				if len(diags) != 0 {
					t.Errorf("a direct call: want no diagnostic\n%s\n\t%s", src, describe(diags))
				}
			case guardValue:
				// Since G1 a user extern is a function value: in every
				// value field it is checked as an ordinary function of its
				// signature is, the control, and never gets P3.
				ctl := strings.Replace(src, externGuardDecl, externGuardControl, 1)
				_, ctlDiags := check.CheckSource(ctl)
				if p3 != 0 || codesAndLines(diags, 0) != codesAndLines(ctlDiags, 0) {
					t.Errorf("an extern value at line %d is not checked as its control\n%s\n\t%s\ncontrol:\n\t%s", line, src, describe(diags), describe(ctlDiags))
				}
			case guardType:
				if len(diags) != 1 || p3 != 0 {
					t.Errorf("a type position: want exactly one diagnostic, not P3\n%s\n\t%s", src, describe(diags))
				}
			case guardBinder:
				if p3 != 0 {
					t.Errorf("a name that is not a use got the P3 message\n%s\n\t%s", src, describe(diags))
				}
			default:
				t.Errorf("unknown class %q", row.class)
			}
		})
	}
	t.Logf("%d Expr fields: %v", len(derived), classes)
}
