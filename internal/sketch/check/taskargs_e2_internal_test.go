package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Stage E2, C13 (taskescape.go rule 4): a type argument, written or
// inferred, holds no Task. Two tests close the rule's domain, each over the
// checker's own list rather than a sample:
//
//   - taskArgGaps, the output invariant, runs inside infoGaps, so every
//     accepted program of TestInfoComplete, FuzzCheck and the differential
//     generators is checked: no type argument of any type, instance or call
//     that Info records holds a Task, by an oracle written apart from
//     holdsTask.
//   - TestTypeArgSitesPassTheTaskRule, the structural guard, reads the
//     checker's Go source: recordInstantiation, which every binding of type
//     parameters to arguments calls (the expansion check needs each one), is
//     reached only through typeArgsAdmitted; every function that checks a
//     type argument against its constraint also calls typeArgsAdmitted; and
//     the callers of typeArgsAdmitted are exactly taskArgSites, the list the
//     behaviour matrix (stage_e2_test.go) covers.

// oracleHoldsTask reports whether t holds a Task: it is the prelude's Task,
// or one is among its type arguments, its declared body's types, its
// elements, fields or variants. A function type and an operation type hold
// none. It is a plain recursion with an on-path set, written apart from
// holdsTask's Tarjan walk.
func oracleHoldsTask(t Type, onPath map[*Named]bool) bool {
	switch t := t.(type) {
	case *Named:
		if isTask(t) {
			return true
		}
		for _, a := range t.TArgs {
			if oracleHoldsTask(a, onPath) {
				return true
			}
		}
		o := t.origin()
		if onPath[o] || o.under == nil {
			return false
		}
		onPath[o] = true
		defer delete(onPath, o)
		return oracleHoldsTask(o.under, onPath)
	case *Slice:
		return oracleHoldsTask(t.Elem, onPath)
	case *Map:
		return oracleHoldsTask(t.Key, onPath) || oracleHoldsTask(t.Elem, onPath)
	case *Pointer:
		return oracleHoldsTask(t.Elem, onPath)
	case *Tuple:
		for _, e := range t.Types {
			if oracleHoldsTask(e, onPath) {
				return true
			}
		}
	case *Struct:
		for _, f := range t.Fields {
			if oracleHoldsTask(f.Type, onPath) {
				return true
			}
		}
	case *Union:
		for _, v := range t.Variants {
			if oracleHoldsTask(v, onPath) {
				return true
			}
		}
	}
	return false
}

// taskArgGaps returns one line for each type argument in info that holds a
// Task: a Named type's argument anywhere in a recorded type (every part of
// it, function types and operation types included), an instance's and a
// call's type arguments.
func taskArgGaps(info *Info) []string {
	var gaps []string
	seen := map[Type]bool{}
	arg := func(where string, a Type) {
		if a != nil && oracleHoldsTask(a, map[*Named]bool{}) {
			gaps = append(gaps, "a type argument that holds a Task ("+typeString(a)+") in "+where)
		}
	}
	var visit func(t Type)
	visit = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *Named:
			for _, a := range t.TArgs {
				arg(typeString(t), a)
				visit(a)
			}
		case *Slice:
			visit(t.Elem)
		case *Map:
			visit(t.Key)
			visit(t.Elem)
		case *Pointer:
			visit(t.Elem)
		case *Tuple:
			for _, e := range t.Types {
				visit(e)
			}
		case *Struct:
			for _, f := range t.Fields {
				visit(f.Type)
			}
		case *Union:
			for _, v := range t.Variants {
				visit(v)
			}
		case *Signature:
			for _, p := range t.Params {
				visit(p.Type)
			}
			for _, r := range t.Results {
				visit(r)
			}
		case *Interface:
			for _, m := range t.Methods {
				visit(m.Sig)
			}
		}
	}
	object := func(o Object) {
		switch o := o.(type) {
		case *Var:
			visit(o.Type)
		case *Func:
			visit(o.Sig)
		case *TypeName:
			visit(o.Type)
		}
	}
	for _, t := range info.Types {
		visit(t)
	}
	for _, o := range info.Defs {
		object(o)
	}
	for _, o := range info.Uses {
		object(o)
	}
	for _, s := range info.Literals {
		visit(s)
	}
	for _, t := range info.Tails {
		visit(t)
	}
	for _, inst := range info.Instances {
		for _, a := range inst.TypeArgs {
			arg("an instance of "+inst.Func.Name, a)
			visit(a)
		}
	}
	for _, c := range info.Calls {
		for _, a := range c.TypeArgs {
			arg("a call's type arguments", a)
			visit(a)
		}
	}
	slices.Sort(gaps)
	return slices.Compact(gaps)
}

// taskArgSites are the checker functions that bind a generic declaration's
// type parameters to arguments, each an instantiation typeArgsAdmitted
// checks. The behaviour matrix (TestTaskTypeArgumentMatrix) has a template
// for each, or the reason none can carry a Task.
var taskArgSites = []string{"callGeneric", "instantiateExprs", "instantiateValue", "literalType", "selfCall"}

// taskArgExempt are the functions that check a type argument against its
// constraint without being an instantiation of their own, with the reason
// its arguments already passed typeArgsAdmitted.
var taskArgExempt = map[string]string{
	// The witness of an instance recorded at a site: its arguments are the
	// site's, and an implementing function's are parts of them.
	"instanceWitnesses": "the arguments of an instance a site admitted",
}

func TestTypeArgSitesPassTheTaskRule(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string][]string{} // callee → enclosing functions
	files := 0
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
		files++
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "recordInstantiation", "typeArgsAdmitted", "satisfies":
						if !slices.Contains(callers[sel.Sel.Name], fd.Name.Name) {
							callers[sel.Sel.Name] = append(callers[sel.Sel.Name], fd.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	if files < 20 {
		t.Fatalf("read %d checker files; the guard reads the package directory", files)
	}
	if got := callers["recordInstantiation"]; !slices.Equal(got, []string{"typeArgsAdmitted"}) {
		t.Errorf("recordInstantiation is called from %v; every instantiation passes the Task rule first, so only typeArgsAdmitted calls it", got)
	}
	got := slices.Sorted(slices.Values(callers["typeArgsAdmitted"]))
	if !slices.Equal(got, taskArgSites) {
		t.Errorf("typeArgsAdmitted is called from %v; taskArgSites lists %v: add the new site to the list and to TestTaskTypeArgumentMatrix", got, taskArgSites)
	}
	for _, fn := range callers["satisfies"] {
		if slices.Contains(taskArgSites, fn) || taskArgExempt[fn] != "" {
			continue
		}
		t.Errorf("%s checks a type argument against its constraint but does not pass it through typeArgsAdmitted", fn)
	}
}

// The oracle agrees with holdsTask on the types a program can write and on
// the instances only the checker builds.
func TestTaskOracleAgreesWithHoldsTask(t *testing.T) {
	c, diags := checkFor("type Fault {}\ntype Box[T] { V T }\ntype R { kids []R, f func() Task[int, Fault] }\n" +
		"func f(t Task[int, Fault], b Box[int], r R, g func(t Task[int, Fault]) int, s []int) {}\n")
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	var params []*Var
	for _, f := range c.funcs {
		if f.Name == "f" {
			params = f.Sig.Params
		}
	}
	box := c.pkg.Scope.names["Box"].(*TypeName).Type.(*Named)
	types := []Type{c.instantiate(box, []Type{params[0].Type}), c.instantiate(box, []Type{&Slice{Elem: params[0].Type}})}
	for _, p := range params {
		types = append(types, p.Type)
	}
	held := 0
	for _, typ := range types {
		want := c.holdsTask(typ)
		if got := oracleHoldsTask(typ, map[*Named]bool{}); got != want {
			t.Errorf("oracle %v, holdsTask %v for %s", got, want, typ)
		}
		if want {
			held++
		}
	}
	if held != 3 {
		t.Errorf("%d of the types hold a Task, want 3 (Box[Task], Box[[]Task], Task)", held)
	}
}

// taskArgHead declares what the matrix templates use: a generic record, a
// generic function, a function that returns a Task and one that takes one.
const taskArgHead = "import task\ntype Fault {}\ntype Box[T] { V T }\nfunc id[T](x T) T { x }\n" +
	"func start() Task[int, Fault] { task.Go() { 1 } }\nfunc wait(t Task[int, Fault]) int { 0 }\n"

// taskArgTemplate is one instantiation site: $S is the argument written as
// a type, $V a value of it; the instantiation stands on the line that holds
// the first placeholder.
type taskArgTemplate struct {
	site, name, src string
}

var taskArgTemplates = []taskArgTemplate{
	{"instantiateExprs", "parameter-type", "func use(b Box[$S]) {}\n"},
	{"instantiateExprs", "task-argument", "func use(t Task[$S, Fault]) {}\n"},
	{"instantiateExprs", "local-type", "func use() {\n\tlet _ Option[$S] = None\n}\n"},
	{"instantiateExprs", "union-variant", "type U = Fault | Box[$S]\n"},
	{"instantiateValue", "explicit-value", "func use() {\n\tlet _ = id[$S]\n}\n"},
	{"callGeneric", "explicit-call", "func use() {\n\tlet _ = id[$S]($V)\n}\n"},
	{"callGeneric", "inferred-call", "func use() {\n\tlet _ = id($V)\n}\n"},
	{"callGeneric", "inferred-from-block", "func use() {\n\tlet t = task.Go() { $V }\n\tt.Stop()\n}\n"},
	{"literalType", "record-literal", "func use() {\n\tlet _ = Box{V: $V}\n}\n"},
	{"literalType", "variant-literal", "func use() {\n\tlet _ = Some{$V}\n}\n"},
}

// taskArgUnreachable names each site no program can reach with a Task, and
// why; typeArgsAdmitted still runs there, so the rule has no hole if the
// reason stops holding.
var taskArgUnreachable = map[string]string{
	"selfCall": "x.f binds f's type parameters to the type arguments of x's own type, a type of the package that was admitted where it was written or inferred",
}

// taskArgShape is a type argument: its text, a value of it, and whether it
// holds a Task (by the oracle, checked against the text).
type taskArgShape struct {
	typ, value string
}

var taskArgShapes = []taskArgShape{
	{"Task[int, Fault]", "start()"},
	{"int", "1"},
	{"[]int", "[]int{1}"},
	{"Option[int]", "Some{1}"},
	{"func() Task[int, Fault]", "start"},
	{"func(t Task[int, Fault]) int", "wait"},
}

// The rule's whole domain: every instantiation site against every kind of
// argument. An argument that holds a Task is one S108 at the site with the
// rule's text; every other argument checks clean, a function type that
// returns or takes a Task included (it holds none).
func TestTaskTypeArgumentMatrix(t *testing.T) {
	covered := map[string]bool{}
	for site := range taskArgUnreachable {
		covered[site] = true
	}
	for _, tmpl := range taskArgTemplates {
		covered[tmpl.site] = true
	}
	for _, site := range taskArgSites {
		if !covered[site] {
			t.Errorf("the site %s has no template and no reason it is unreachable", site)
		}
	}
	head := strings.Count(taskArgHead, "\n")
	for _, tmpl := range taskArgTemplates {
		for _, shape := range taskArgShapes {
			src := strings.NewReplacer("$S", shape.typ, "$V", shape.value).Replace(tmpl.src)
			line := head + 1 + strings.Count(tmpl.src[:strings.IndexByte(tmpl.src, '$')], "\n")
			_, diags := checkFor(taskArgHead + src)
			// The oracle decides the expectation from the argument's type as
			// the checker resolved it in the head's context.
			holds := strings.HasPrefix(shape.typ, "Task[")
			if holds && len(diags) == 1 && diags[0].Code == CodeType && diags[0].Line == line &&
				strings.Contains(diags[0].Message, "holds a Task, and a type argument never does") {
				continue
			}
			if !holds && len(diags) == 0 {
				continue
			}
			t.Errorf("%s/%s with %s: holds a Task %v, want %s; got %v\n%s", tmpl.site, tmpl.name, shape.typ, holds,
				map[bool]string{true: "one S108 on line " + strconv.Itoa(line), false: "a clean check"}[holds], diags, taskArgHead+src)
		}
	}
	// The shapes' verdicts are the oracle's.
	c, diags := checkFor(taskArgHead + "func shapes(" + shapeParams() + ") {}\n")
	if len(diags) != 0 {
		t.Fatalf("shape parameters: %v", diags)
	}
	for _, f := range c.funcs {
		if f.Name != "shapes" {
			continue
		}
		for i, p := range f.Sig.Params {
			if got, want := oracleHoldsTask(p.Type, map[*Named]bool{}), strings.HasPrefix(taskArgShapes[i].typ, "Task["); got != want {
				t.Errorf("shape %s: the oracle says %v, the matrix %v", taskArgShapes[i].typ, got, want)
			}
		}
	}
}

func shapeParams() string {
	var ps []string
	for i, s := range taskArgShapes {
		ps = append(ps, "p"+strconv.Itoa(i)+" "+s.typ)
	}
	return strings.Join(ps, ", ")
}
