package std_test

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// v1Packages are the std packages whose Go bodies are runtime/v1's
// (docs/spec/stdlib.md, v1 packages).
var v1Packages = []string{"log", "queue", "scope", "slice", "task", "trace"}

const v1Path = "ergo.local/prototype/runtime/v1"

// unbound are the exported Go methods, of a type a std extern returns,
// that no Ergo operation binds, each with the reason. A body that binds no
// declaration and is not listed here fails TestEachExternHasItsGoBody.
var unbound = map[string]string{
	// The return of a Task (Tasks rule 8): only the lowering calls it.
	"Task.Move": "lowering only",
}

// TestEachModuleChecks checks each v1 package with the checker that
// checks user code, so no std declaration is a compiler branch.
func TestEachModuleChecks(t *testing.T) {
	for _, path := range v1Packages {
		if _, ok := std.Package(path); !ok {
			t.Errorf("std.Package(%q): not embedded", path)
			continue
		}
		if _, err := check.Std.Import(path); err != nil {
			t.Errorf("check.Std.Import(%q): %v", path, err)
		}
	}
}

// TestCallsToEachFunctionCheck checks a program that imports each v1
// package and calls each function and each operation it declares; and,
// as a control, that the signatures carry their rows: a Push whose
// failure is not handled is refused.
func TestCallsToEachFunctionCheck(t *testing.T) {
	const program = `import task
import scope
import slice
import queue

type Fault {}

func work(n int64) int64 fails Fault {
	if n < 0 {
		return Fault
	}
	return n * 2
}

func produce(q queue.Queue[int64]) fails queue.Closed {
	try q.Push(1)
	try q.Push(2)
	q.Close()
}

func main() fails Fault | queue.Closed needs Console {
	scope.OnExit() { Console.Println("bye") }
	let t = task.Go() { work(21) }
	let v = try t.Wait()
	let u = task.Go() { Console.Println("child") }
	u.Stop()
	let q = queue.New[int64](2)
	try produce(q)
	let a = try q.Pop()
	let b = try q.Pop()
	let xs = slice.WithCapacity[int64](4)
	let ys = xs.Append(a + b + v)
	Console.Println("\(ys)")
}
`
	if _, diags := check.CheckSource(program); len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	const unhandled = `import queue

func main() {
	let q = queue.New[string](1)
	q.Push("x")
}
`
	// Each generic takes a type that is not comparable, as its Go body's
	// T any does (matchTypeParams ties the two constraints together).
	const notComparable = `import task
import slice
import queue

func main() fails queue.Closed {
	let xs = slice.WithCapacity[[]int64](3)
	let q = queue.New[[]int64](1)
	try q.Push([]int64{2})
	_ = xs
	let t = task.Go() { []int64{1} }
	let ys = t.Wait()
	_ = ys
	let zs = try q.Pop()
	_ = zs
}
`
	if _, diags := check.CheckSource(notComparable); len(diags) > 0 {
		t.Fatalf("non-comparable type arguments: %v", diags)
	}
	// The code is the checker's (S101 since check-v1 stage C); this test
	// pins only that the row reaches the call.
	_, diags := check.CheckSource(unhandled)
	if len(diags) != 1 || diags[0].Line != 5 || !strings.Contains(diags[0].Message, "queue.Closed") {
		t.Fatalf("an unhandled queue.Closed: got %v, want one diagnostic at line 5 naming queue.Closed", diags)
	}
}

// TestContextPackagesSurface: trace and log export the four declarations
// of G1 and nothing else (the rest is #206); their values bind through
// with by the context-binding protocol and their reads check in a program
// with no capability.
func TestContextPackagesSurface(t *testing.T) {
	want := map[string][]string{"log": {"Annotate", "Annotation"}, "trace": {"Current", "Span"}}
	for path, names := range want {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		var got []string
		for _, f := range pkg.Funcs {
			if isExportedName(f.Name) {
				got = append(got, f.Name)
			}
		}
		for _, tn := range pkg.Types {
			if isExportedName(tn.Name) {
				got = append(got, tn.Name)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, names) {
			t.Errorf("std %s exports %v, want %v", path, got, names)
		}
	}
	const program = `import log
import trace

func region() string {
	match log.Annotation("region") {
	case Some[string] as s:
		s.Value
	case None:
		"none"
	}
}

func span() string {
	match trace.Current() {
	case Some[string] as s:
		s.Value
	case None:
		"none"
	}
}

func main() needs Console {
	with log.Annotate("region", "eu") {
		with trace.Span("work") {
			Console.Println(region() + " " + span())
		}
	}
}
`
	if _, diags := check.CheckSource(program); len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
}

// TestEachExternHasItsGoBody ties each extern declaration of the v1
// packages, and each operation of a type such an extern returns, to the Go
// body that implements it, read from the embedded runtime source as the
// lowering reads it (v1.BodyOf). The body's parameters after its hidden
// ones stand for the Ergo parameters, one each, with a Go function for a
// function parameter; its results are the Ergo results and error when the
// Ergo signature has an error row; and a function whose result holds a
// Task takes the caller's frame, the hidden destination (Tasks rule 8).
func TestEachExternHasItsGoBody(t *testing.T) {
	bound := map[string]bool{}
	for _, path := range v1Packages {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		externs := 0
		for _, f := range pkg.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil {
				continue
			}
			externs++
			target, name := f.Decl.Extern.Split()
			if target != v1Path {
				t.Errorf("%s.%s binds %s.%s; a v1 package binds %s", path, f.Name, target, name, v1Path)
				continue
			}
			body, err := v1.BodyOf(name)
			if err != nil {
				t.Errorf("%s.%s: %v", path, f.Name, err)
				continue
			}
			bound[name] = true
			where := path + "." + f.Name
			matchSig(t, where, f.Sig, body)
			matchTypeParams(t, where, f.Sig.TParams, body)
			for i, r := range f.Sig.Results {
				if isTask(r) && !hasHidden(body, v1.Frame) {
					t.Errorf("%s returns a Task but its body takes no hidden frame: %+v", where, body)
				}
				ops, ok := operations(r)
				if !ok {
					continue
				}
				goType := baseName(body.Results[i])
				for _, m := range ops.Methods {
					opBody, err := v1.BodyOf(goType + "." + m.Name)
					if err != nil {
						t.Errorf("%s's %s: %v", where, m.Name, err)
						continue
					}
					bound[goType+"."+m.Name] = true
					matchSig(t, where+" "+m.Name, m.Sig, opBody)
					matchTypeParams(t, where+" "+m.Name, ownerParams(r), opBody)
				}
			}
		}
		if externs == 0 {
			t.Errorf("std package %s declares no extern", path)
		}
	}
	for _, name := range []string{"Go", "OnExit", "New", "WithCapacity", "Queue.Push", "Queue.Pop", "Queue.Close", "Task.Wait", "Task.Stop", "ContextBound", "ContextValue"} {
		if !bound[name] {
			t.Errorf("the Go body %s binds no std declaration", name)
		}
	}
	for name, why := range unbound {
		if bound[name] {
			t.Errorf("%s is bound now; remove it from unbound (%s)", name, why)
		}
		if _, err := v1.BodyOf(name); err != nil {
			t.Errorf("unbound %s: %v", name, err)
		}
	}
}

// errorValues are the runtime/v1 values of the error types the v1 packages
// declare, by Ergo name. Every record type of a v1 package is listed here
// or in TestStdTypesBindRuntimeTypes fails.
var errorValues = map[string]error{
	"queue.Closed": v1.Closed{},
}

// TestStdTypesBindRuntimeTypes: a record type declared in a v1 package,
// which is not an operations type, is the exported runtime/v1 struct type
// of the same name with the same fields, and an error type's Go text is
// its Ergo name (docs/spec/stdlib.md, Types). So the lowering binds
// queue.Closed to v1.Closed, the value Push and Pop return, and a catch of
// queue.Closed matches it; it never generates a fresh error type for it.
func TestStdTypesBindRuntimeTypes(t *testing.T) {
	seen := 0
	for _, path := range v1Packages {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, tn := range pkg.Types {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				t.Errorf("%s.%s is not a named type", path, tn.Name)
				continue
			}
			if _, ops := operations(n); ops {
				continue // bound through the extern that returns it (goType)
			}
			if !isExportedName(tn.Name) {
				// An unexported record (log's annotation, trace's span) is
				// the program's declaration: no import names it, and the
				// runtime has no struct for it (lower runtimeType).
				if _, err := v1.StructFields(tn.Name); err == nil {
					t.Errorf("%s.%s is unexported, and v1 declares a struct of its name", path, tn.Name)
				}
				continue
			}
			st, ok := n.Underlying().(*check.Struct)
			if !ok {
				t.Errorf("%s.%s is neither a record nor an operations type: it needs a binding rule", path, tn.Name)
				continue
			}
			seen++
			var want []string
			for _, f := range st.Fields {
				want = append(want, f.Name)
			}
			got, err := v1.StructFields(tn.Name)
			if err != nil {
				t.Errorf("%s.%s: %v", path, tn.Name, err)
				continue
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s.%s has fields %v, its Go type v1.%s has %v", path, tn.Name, want, tn.Name, got)
			}
			name := path + "." + tn.Name
			v, ok := errorValues[name]
			if !ok {
				t.Errorf("%s is a record of a v1 package with no entry in errorValues", name)
				continue
			}
			if v.Error() != name {
				t.Errorf("v1.%s's text is %q, want its Ergo name %q", tn.Name, v.Error(), name)
			}
		}
	}
	if seen != len(errorValues) {
		t.Errorf("checked %d records of v1 packages, errorValues lists %d", seen, len(errorValues))
	}
}

// matchTypeParams checks a body's Go type parameters against the Ergo type
// parameters after row erasure (docs/spec/stdlib.md, Types 2): the same
// names, in order, with the same constraint. So the checker admits a type
// argument exactly when the Go call does: an Ergo T with no constraint
// takes a non-comparable type, which a Go T comparable would refuse.
func matchTypeParams(t *testing.T, where string, ergo []*check.TypeParam, body v1.Body) {
	t.Helper()
	var want []v1.TypeParam
	for _, p := range ergo {
		if p.Kind != check.KindType {
			continue
		}
		c, ok := goConstraint(p.Constraint)
		if !ok {
			t.Errorf("%s: type parameter %s has a constraint with no Go rule", where, p.Name)
			return
		}
		want = append(want, v1.TypeParam{Name: p.Name, Constraint: c})
	}
	if !slices.Equal(want, body.TypeParams) {
		t.Errorf("%s: the Ergo type parameters after row erasure are %v, the Go body's are %v", where, want, body.TypeParams)
	}
}

// goConstraint is the Go constraint of an Ergo one: none is any,
// comparable is comparable; ordered and number have no Go name here.
func goConstraint(c check.Type) (string, bool) {
	if c == nil {
		return "any", true
	}
	i, ok := c.(*check.Interface)
	switch {
	case !ok || len(i.Methods) > 0:
		return "", false
	case i.Set == check.SetComparable:
		return "comparable", true
	case i.Set == check.SetAll:
		return "any", true
	}
	return "", false
}

// ownerParams are the type parameters of the operations type t, an
// instance or its origin.
func ownerParams(t check.Type) []*check.TypeParam {
	n := t.(*check.Named)
	if n.Orig != nil {
		n = n.Orig
	}
	return n.TParams
}

// matchSig checks a Go body's shape against an Ergo signature.
func matchSig(t *testing.T, where string, sig *check.Signature, body v1.Body) {
	t.Helper()
	if len(body.Params) != len(sig.Params) {
		t.Errorf("%s: %d Ergo parameters, but the Go body has %d after its hidden ones: %+v", where, len(sig.Params), len(body.Params), body)
		return
	}
	for i, p := range sig.Params {
		inner, isFunc := p.Type.(*check.Signature)
		switch {
		case isFunc && body.Params[i].Func == nil:
			t.Errorf("%s: parameter %s is a function, the Go parameter %s is not", where, p.Name, body.Params[i].Type)
		case !isFunc && body.Params[i].Func != nil:
			t.Errorf("%s: parameter %s is not a function, the Go parameter %s is", where, p.Name, body.Params[i].Type)
		case isFunc:
			matchSig(t, where+" "+p.Name, inner, *body.Params[i].Func)
		default:
			if g, ok := goType(p.Type); !ok || g != body.Params[i].Type {
				t.Errorf("%s: parameter %s lowers to Go %q, but the Go parameter is %s", where, p.Name, g, body.Params[i].Type)
			}
		}
	}
	want := len(sig.Results)
	if sig.Fallible {
		want++
	}
	if len(body.Results) != want {
		t.Errorf("%s: want %d Go results (fallible %v), got %v", where, want, sig.Fallible, body.Results)
		return
	}
	for i, r := range sig.Results {
		if g, ok := goType(r); !ok || g != body.Results[i] {
			t.Errorf("%s: result %d lowers to Go %q, but the Go result is %s", where, i+1, g, body.Results[i])
		}
	}
	if sig.Fallible && body.Results[want-1] != "error" {
		t.Errorf("%s: an error row needs a last Go result error, got %v", where, body.Results)
	}
}

// goType is the Go type, as a body in runtime/v1 writes it, of an Ergo type
// a std signature uses (docs/spec/stdlib.md, Types): a basic type is its
// Go type; a type parameter keeps its name; []T is a Go slice; a type
// declared in a v1 package, or the prelude's Task, is the runtime/v1 type
// of the same name, by pointer for an operations type, with its type
// arguments but not its row arguments. ok is false for any other type, so
// a signature that uses one needs a rule here first.
func goType(t check.Type) (string, bool) {
	switch t := t.(type) {
	case *check.Basic:
		return t.Name, true
	case *check.TypeParam:
		return t.Name, t.Kind == check.KindType
	case *check.Slice:
		e, ok := goType(t.Elem)
		return "[]" + e, ok
	case *check.Named:
		pkg := ""
		if t.Obj.Pkg != nil {
			pkg = t.Obj.Pkg.Path
		}
		// The prelude's Seq[T] is Go's iter.Seq[T] at a std body (D-D4).
		if pkg == "prelude" && t.Obj.Name == "Seq" && len(t.TArgs) == 1 {
			e, ok := goType(t.TArgs[0])
			return "iter.Seq[" + e + "]", ok
		}
		if !slices.Contains(v1Packages, pkg) && !isTask(t) {
			return "", false
		}
		var args []string
		for _, a := range t.TArgs {
			if p, ok := a.(*check.TypeParam); ok && p.Kind != check.KindType {
				continue // a row argument has no Go type argument
			}
			if _, ok := a.(*check.RowArg); ok {
				continue
			}
			g, ok := goType(a)
			if !ok {
				return "", false
			}
			args = append(args, g)
		}
		name := t.Obj.Name
		if len(args) > 0 {
			name += "[" + strings.Join(args, ", ") + "]"
		}
		if _, ops := operations(t); ops {
			name = "*" + name
		}
		return name, true
	}
	return "", false
}

func hasHidden(b v1.Body, h v1.Hidden) bool {
	for _, x := range b.Hidden {
		if x == h {
			return true
		}
	}
	return false
}

// isTask reports the prelude's Task.
func isTask(t check.Type) bool {
	n, ok := t.(*check.Named)
	return ok && n.Obj.Name == "Task" && n.Obj.Pkg != nil && n.Obj.Pkg.Path == std.PreludePath
}

// operations is the operation set of an operations type.
func operations(t check.Type) (*check.Interface, bool) {
	n, ok := t.(*check.Named)
	if !ok {
		return nil, false
	}
	i, ok := n.Underlying().(*check.Interface)
	return i, ok && len(i.Methods) > 0
}

// baseName is the Go type name of a result: Queue for *Queue[T].
// isExportedName reports a name that starts with an upper-case letter.
func isExportedName(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

func baseName(goType string) string {
	s := strings.TrimPrefix(goType, "*")
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	return s
}
