package check_test

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The classes of TestLocalContextPlacementGuard beyond the extern guard's
// (check-v1 E5, A3).
const (
	// A with operand: the context is clean there.
	guardWith = "with"
	// An embedding entry of a context literal: clean.
	guardEmbed = "embed"
	// A value slot only a package-level name reaches (a machine's
	// entries): a local context is out of scope there, never the Design A2
	// message; a package context there is one S108 with it.
	guardPackage = "package"
)

// The Design A2 messages (context_local.go).
const (
	contextValueMsg  = "a context is used only by with or embedded in a context: with w { … } or context { w, … }"
	contextRebindMsg = "a context is bound once by let; it is not reassigned"
)

// contextGuardPrelude is what every guard program names.
const contextGuardPrelude = `type Greeter { Greet(name string) string }
type Plain {}
func Greet(_ Plain, name string) string { name }
type Bang {}
type Box { F int }
`

// contextPlaceRows has exactly one row for each Expr field of the syntax
// package (externExprFields): its class and a program with the context's
// name w in that field, marked by @, and $W where w is bound in a body.
var contextPlaceRows = map[string]externPlaceRow{
	// Where a context is used.
	"CallExpr.Args":    {guardWith, "func main() {\n\t$W\n\t_ = with @w { Greeter.Greet(\"x\") }\n}\n"},
	"ContextEntry.Key": {guardEmbed, "func main() {\n\t$W\n\t_ = with context { @w } { Greeter.Greet(\"x\") }\n}\n"},

	// Values.
	"AssertStmt.X":             {guardValue, "test T {\n\t$W\n\tassert @w\n}\n"},
	"AssignStmt.Lhs":           {guardValue, "func main() {\n\t$W\n\t@w = 1\n}\n"},
	"AssignStmt.Rhs":           {guardValue, "func main() {\n\t$W\n\t_ = @w\n}\n"},
	"BinaryExpr.X":             {guardValue, "func main() {\n\t$W\n\t_ = @w == 1\n}\n"},
	"BinaryExpr.Y":             {guardValue, "func main() {\n\t$W\n\t_ = 1 == @w\n}\n"},
	"CallExpr.Fun":             {guardValue, "func main() {\n\t$W\n\t_ = @w()\n}\n"},
	"CatchExpr.X":              {guardValue, "func main() {\n\t$W\n\tlet v = @w catch {\n\tcase Bang: return\n\t}\n\t_ = v\n}\n"},
	"CompositeLit.Elts":        {guardValue, "func main() {\n\t$W\n\t_ = []int{@w}\n}\n"},
	"CompositeLit.Base":        {guardValue, "func main() {\n\t$W\n\t_ = Box{..@w, F: 1}\n}\n"},
	"ContextEntry.Value":       {guardValue, "func main() {\n\t$W\n\t_ = with context { Greeter: @w } { Greeter.Greet(\"x\") }\n}\n"},
	"ExprStmt.X":               {guardValue, "func main() {\n\t$W\n\t@w\n}\n"},
	"ForStmt.Cond":             {guardValue, "func main() {\n\t$W\n\tfor @w { }\n}\n"},
	"IfStmt.Cond":              {guardValue, "func main() {\n\t$W\n\tif @w { }\n}\n"},
	"IndexExpr.Indices":        {guardValue, "func main() {\n\t$W\n\tlet xs = []int{1}\n\t_ = xs[@w]\n}\n"},
	"IndexExpr.X":              {guardValue, "func main() {\n\t$W\n\t_ = @w[0]\n}\n"},
	"InterpolatedString.Exprs": {guardValue, "func main() {\n\t$W\n\t_ = \"\\(@w)\"\n}\n"},
	"KeyValueExpr.Key":         {guardValue, "func main() {\n\t$W\n\t_ = Map[int, int]{@w: 1}\n}\n"},
	"KeyValueExpr.Value":       {guardValue, "func main() {\n\t$W\n\t_ = Box{F: @w}\n}\n"},
	"MachineType.Initial":      {guardPackage, machineGuard("initial: @w,\n\tstep: step,")},
	"ParenExpr.X":              {guardValue, "func main() {\n\t$W\n\t_ = (@w)\n}\n"},
	"RangeStmt.X":              {guardValue, "func main() {\n\t$W\n\tfor x in @w { _ = x }\n}\n"},
	"ReturnStmt.Results":       {guardValue, "func get() int {\n\t$W\n\treturn @w\n}\n"},
	"SelectorExpr.X":           {guardValue, "func main() {\n\t$W\n\t_ = @w.Len\n}\n"},
	"SliceExpr.X":              {guardValue, "func main() {\n\t$W\n\t_ = @w[0:1]\n}\n"},
	"SliceExpr.Low":            {guardValue, "func main() {\n\t$W\n\tlet xs = []int{1}\n\t_ = xs[@w:]\n}\n"},
	"SliceExpr.High":           {guardValue, "func main() {\n\t$W\n\tlet xs = []int{1}\n\t_ = xs[:@w]\n}\n"},
	"SliceExpr.Max":            {guardValue, "func main() {\n\t$W\n\tlet xs = []int{1}\n\t_ = xs[0:1:@w]\n}\n"},
	"SwitchStmt.Tag":           {guardValue, "func main() {\n\t$W\n\tlet m = match @w {\n\tcase \"a\": 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n"},
	"TryExpr.X":                {guardValue, "func main() {\n\t$W\n\tlet t = try @w\n\t_ = t\n}\n"},
	"TupleExpr.Elts":           {guardValue, "func main() {\n\t$W\n\tlet t = (@w, 1)\n\t_ = t\n}\n"},
	"TypeGuard.X":              {guardValue, contextTypeMatchGuard},
	"TypeSwitchStmt.Subjects":  {guardValue, contextTypeMatchGuard},
	"UnaryExpr.X":              {guardValue, "func main() {\n\t$W\n\t_ = !@w\n}\n"},
	"VarSpec.Values":           {guardValue, "func main() {\n\t$W\n\tlet v = @w\n\t_ = v\n}\n"},

	// Types: a context names no type.
	"ArrayType.Elt":        {guardType, "func f(xs []@w) {}\n"},
	"CompositeLit.Type":    {guardType, "func main() {\n\t$W\n\t_ = @w{}\n}\n"},
	"ContextEntry.Keys":    {guardType, "func main() {\n\t$W\n\t_ = with context { Greeter, @w: Plain } { Greeter.Greet(\"x\") }\n}\n"},
	"ErrorRow.Types":       {guardType, "func f() fails @w {}\n"},
	"Field.Type":           {guardType, "func f(x @w) {}\n"},
	"FuncDecl.Type":        {guardType, "func f(x @w) {}\n"},
	"FuncLit.Type":         {guardType, "func main() {\n\t$W\n\t_ = func(x @w) {}\n}\n"},
	"InterfaceType.Union":  {guardType, "type A {}\ntype U = A | @w\n"},
	"MachineType.State":    {guardType, "type E = Ping\ntype Ping {}\ntype M machine[@w, E] { initial: 1, step: step }\nfunc step(s int, e E) Decision[int, int] { return Ignore }\n"},
	"MachineType.Signal":   {guardType, "type S = Idle\ntype Idle {}\ntype M machine[S, @w] { initial: Idle, step: step }\nfunc step(s S, e int) Decision[S, int] { return Ignore }\n"},
	"MapType.Key":          {guardType, "func f(m Map[@w, int]) {}\n"},
	"MapType.Value":        {guardType, "func f(m Map[int, @w]) {}\n"},
	"NeedsClause.Names":    {guardType, "func f() needs @w {}\n"},
	"ProvidesClause.Names": {guardType, "func main() {\n\t$W\n\t_ = with context provides @w { Greeter: Plain } { Greeter.Greet(\"x\") }\n}\n"},
	"StarExpr.X":           {guardType, "func f(p *@w) {}\n"},
	"TypeDecl.Spec":        {guardType, "type T = @w\n"},
	"VarSpec.Type":         {guardType, "func main() {\n\t$W\n\tlet v @w\n\t_ = v\n}\n"},
	// A machine's step names a function: a context's name there is no
	// function, one diagnostic, not the Design A2 message.
	"MachineEntry.Value": {guardType, machineGuard("initial: Idle,\n\tstep: @w,")},
	"MachineType.Step":   {guardType, machineGuard("initial: Idle,\n\tstep: @w,")},
	"MachineType.After":  {guardType, machineGuard("initial: Idle,\n\tstep: step,\n\tafter: @w,")},
	"LawReach.Targets":   {guardType, machineGuard("initial: Idle,\n\tstep: step,\n\tlaw L reaches @w,")},

	// Names that are not a use of the context: a binder, a member, text. A
	// catch arm's name that names no type binds the failure.
	"CatchArm.Types":           {guardBinder, "func load() int fails Bang { 1 }\nfunc f() int {\n\t$W\n\tload() catch {\n\tcase @w: 0\n\t}\n}\n"},
	"CaseClause.List":          {guardBinder, "func main() {\n\tlet f = 1\n\tlet m = match f {\n\tcase @w: 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n"},
	"AsPattern.X":              {guardBinder, "func main() {\n\tlet f = 1\n\tlet m = match f {\n\tcase @w as g: g\n\tcase _: 2\n\t}\n\t_ = m\n}\n"},
	"SelectorExpr.Sel":         {guardBinder, "func main() {\n\t$W\n\t_ = \"x\".@w\n}\n"},
	"AsPattern.Name":           {guardBinder, "type A {}\ntype B {}\ntype U = A | B\nfunc f(u U) string {\n\tmatch u {\n\tcase A: \"a\"\n\tcase _ as @w: \"b\"\n\t}\n}\n"},
	"ContextDecl.Name":         {guardBinder, "let @w = context { Greeter: Plain }\n"},
	"ExternSpec.Target":        {guardBinder, "extern go strings.@w func Upper(s string) string\n"},
	"Field.Names":              {guardBinder, "func f(@w int) int { w }\n"},
	"FuncDecl.Name":            {guardBinder, "func @w() {}\n"},
	"ImportSpec.Name":          {guardBinder, "import go @w strings\n"},
	"ImportSpec.Path":          {guardBinder, "import @w\n"},
	"InterpolatedString.Texts": {guardBinder, "func main() {\n\t$W\n\t_ = \"@w \\(1)\"\n}\n"},
	"LawDecl.Name":             {guardBinder, "type C {\n\tGet() int,\n\n\tlaw @w(v int) {\n\t\tGet() == v\n\t},\n}\n"},
	"RangeStmt.Key":            {guardBinder, "func main() {\n\tfor @w, v in []int{1} { _ = w + v }\n}\n"},
	"RangeStmt.Value":          {guardBinder, "func main() {\n\tfor i, @w in []int{1} { _ = i + w }\n}\n"},
	"TestDecl.Name":            {guardBinder, "test @w {\n}\n"},
	"TypeDecl.Name":            {guardBinder, "type @w {}\n"},
	"VarSpec.Names":            {guardBinder, "func main() {\n\tlet @w = context { Greeter: Plain }\n\t_ = with w { Greeter.Greet(\"x\") }\n}\n"},
	"VarSpec.Pattern":          {guardBinder, "func main() {\n\tlet (@w, b) = (1, 2)\n\t_ = w + b\n}\n"},
	"VariantSpec.Name":         {guardBinder, "type U = @w{ ID string } | Pending{}\n"},
}

// contextTypeMatchGuard is a type match whose subject is the context.
const contextTypeMatchGuard = "type A {}\ntype B {}\nfunc main() {\n\t$W\n\tlet m = match @w {\n\tcase A: 1\n\tcase _: 2\n\t}\n\t_ = m\n}\n"

// contextVariant is one binding of the name w a guard program runs with.
type contextVariant struct {
	name  string
	local string // the statement at $W
	pkg   string // a package-level declaration
}

var contextVariants = []contextVariant{
	{"local", "let w = context { Greeter: Plain }", ""},
	{"named", "", "let w = context { Greeter: Plain }\n"},
	{"value", "let w = 1", ""},
}

// guardProgram is a row's program under a variant: the binding at $W (or
// none), the package declaration after the program, and the offset of the
// marked name.
func guardProgram(t *testing.T, program string, v contextVariant) (string, int) {
	t.Helper()
	src := contextGuardPrelude + program
	if strings.Contains(src, "import") {
		src = program + contextGuardPrelude // an import comes first
	}
	src = strings.Replace(src, "$W", v.local, 1)
	src += v.pkg
	off := strings.Index(src, "@")
	if off < 0 || strings.Count(src, "@") != 1 {
		t.Fatalf("the program marks no single reference with @\n%s", src)
	}
	src = src[:off] + src[off+1:]
	if !strings.HasPrefix(src[off:], "w") {
		t.Fatalf("the marked name is not w\n%s", src)
	}
	return src, off
}

// TestLocalContextPlacementGuard is A3 of check-v1 E5: every Expr field of
// the syntax package, derived from its sources, has exactly one row with a
// class; each row but notBuilt runs its program with w a local context, a
// package context and an ordinary value. A local context is clean as a
// with operand or an embedding and exactly one S108 with the Design A2
// message in a value slot; a package context gets the same verdict class;
// an ordinary value never gets the Design A2 message. A new Expr field
// fails until it has a row; a row whose field is gone fails too.
func TestLocalContextPlacementGuard(t *testing.T) {
	derived := externExprFields(t)
	var missing, extra []string
	for k := range derived {
		if _, ok := contextPlaceRows[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range contextPlaceRows {
		if !derived[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("Expr fields with no context placement row: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("context placement rows with no Expr field: %v", extra)
	}
	classes := map[string]int{}
	for key, row := range contextPlaceRows {
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
			want := contextValueMsg
			if key == "AssignStmt.Lhs" {
				want = contextRebindMsg
			}
			for _, v := range contextVariants {
				src, off := guardProgram(t, row.program, v)
				file, err := syntax.Parse(src)
				if err != nil {
					t.Fatalf("%s: parse: %v\n%s", v.name, err, src)
				}
				holds, starts := guardFields(file, off)
				if !holds[key] {
					t.Errorf("%s: the marked name is not in %s (fields around it: %v)\n%s", v.name, key, sortedKeys(holds), src)
				}
				if (row.class == guardValue || row.class == guardWith) && !starts[key] {
					t.Errorf("%s: %s holds no node that starts at the marked name (fields that do: %v)\n%s", v.name, key, sortedKeys(starts), src)
				}
				_, diags := check.CheckSource(src)
				a2 := 0
				for _, d := range diags {
					if d.Message == contextValueMsg || d.Message == contextRebindMsg {
						a2++
					}
				}
				line := strings.Count(src[:off], "\n") + 1
				oneA2 := len(diags) == 1 && diags[0].Code == "S108" && diags[0].Message == want && diags[0].Line == line
				class := row.class
				switch {
				case v.name == "value":
					if a2 != 0 {
						t.Errorf("an ordinary value got the Design A2 message\n%s\n\t%s", src, describe(diags))
					}
					continue
				case class == guardPackage && v.name == "local":
					// Out of a local's scope: never the Design A2 message.
					if a2 != 0 {
						t.Errorf("a local out of scope got the Design A2 message\n%s\n\t%s", src, describe(diags))
					}
					continue
				case class == guardPackage:
					class = guardValue
				}
				switch class {
				case guardWith, guardEmbed:
					if len(diags) != 0 {
						t.Errorf("%s: a %s: want no diagnostic\n%s\n\t%s", v.name, class, src, describe(diags))
					}
				case guardValue:
					if !oneA2 {
						t.Errorf("%s: want exactly one S108 %q at line %d\n%s\n\t%s", v.name, want, line, src, describe(diags))
					}
				case guardType:
					if len(diags) != 1 || a2 != 0 {
						t.Errorf("%s: a type position: want exactly one diagnostic, not the Design A2 message\n%s\n\t%s", v.name, src, describe(diags))
					}
				case guardBinder:
					if a2 != 0 {
						t.Errorf("%s: a name that is not a use got the Design A2 message\n%s\n\t%s", v.name, src, describe(diags))
					}
				default:
					t.Errorf("unknown class %q", row.class)
				}
			}
		})
	}
	t.Logf("%d Expr fields: %v", len(derived), classes)
}
