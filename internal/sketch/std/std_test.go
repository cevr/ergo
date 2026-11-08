package std_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
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
	Console.Println("\(ys[0])")
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

// unguardedHosts are the host providers that do not follow the rule yet,
// each with the unit that moves it under the rule. H2 empties the set.
var unguardedHosts = map[string]string{
	"prelude.LiveScheduler": "host-authority-sched (H2)",
}

// TestStdHostProvidersHaveAuthority is host-authority H1's structural
// guard (stdlib.md, Host packages): a std defined type T, whatever its
// underlying type, is a host provider when a function whose first
// parameter is T reaches the host, that is, it is a std extern whose Go
// body is not a pure runtime/v1 body, or its body names such a function
// of its package (at any depth).
// Every host provider has construction authority, so no other package
// builds one, and each exported function of its package that hands out T,
// or a record T needs (check.Authorities), at any depth of its result
// (check.Exposes), lists Ambient in its written row or takes a value that
// needs authority; no exported let holds one (fu-ambient-ctor;
// TestStdHostHandOuts). Whether another package builds T
// is one checker fact, check.Buildable (fu-constructible): a basic type,
// a slice and a Map always (a literal, an empty literal or the zero value
// holds no element that needs authority); a union when some variant is
// (a variant value is a value of the union, and a union has no zero
// value, types.md Unions 3); a record when it has no unexported field and
// each field is, so an exported []Cap field of authority records lets
// another package build it from an empty slice. An open operation type is
// built (another package implements it); a sealed one when a type of its
// package that implements it is (fu-hostguard2). Program packages have
// their own guard, TestProgramPackagesHaveNoHostProvider.
//
// So a later host package (io, clock) follows the rule, or this test
// names it. The set found today is pinned, and a fixture table with one
// provider per kind checks the walk and the rule.
func TestStdHostProvidersHaveAuthority(t *testing.T) {
	outside, ds := check.CheckSource("let x = LiveScheduler\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	var prelude *check.Package
	for id, obj := range outside.Info.Uses {
		if tn, ok := obj.(*check.TypeName); ok && id.Name == "LiveScheduler" {
			prelude = tn.Pkg
		}
	}
	if prelude == nil {
		t.Fatal("no prelude package behind LiveScheduler")
	}
	pkgs := []*check.Package{prelude}
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		pkgs = append(pkgs, pkg)
	}
	var found []string
	for _, pkg := range pkgs {
		for _, tn := range hostProviderTypes(pkg) {
			name := pkg.Name + "." + tn.Name
			found = append(found, name)
			if _, ok := unguardedHosts[name]; ok {
				continue
			}
			for _, v := range hostAuthorityViolations(pkg, tn, outside) {
				t.Errorf("%s: %s", name, v)
			}
		}
	}
	slices.Sort(found)
	if want := []string{"fs.Host", "os.Host", "prelude.LiveScheduler"}; !slices.Equal(found, want) {
		t.Errorf("host providers %v, want %v: a new one follows the rule, and this list names it", found, want)
	}
	for name, unit := range unguardedHosts {
		if !slices.Contains(found, name) {
			t.Errorf("unguardedHosts lists %s (%s), which is not a host provider", name, unit)
		}
	}

	// One fixture package per kind of Host's underlying type. Each is
	// std-shaped: Host reaches the host through a helper declared after
	// it, which takes no Host (fu-hostguard).
	const reach = `
type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
	const strict = "another package builds it from a literal or its zero value: a defined type over %s holds no authority; it needs a record with an unexported field, or a union of such records"
	noAmbient := "fixture.NewHost returns it and does not list Ambient in its written row"
	const sealedText = "another package makes a value of it: an operation type needs an unexported operation, and each type of its package that implements it needs authority"
	for _, row := range []struct {
		kind, decls string
		want        []string
	}{
		{"record with no unexported field", `type Host {}

func NewHost() Host { Host }
`, []string{"another package builds it: it needs an unexported field", noAmbient}},
		{"record with an unexported field", `type Host { root string }

func NewHost() Host needs Ambient { Host{root: "/"} }
`, nil},
		// The verifier's probe (b9 D2 r1, M1): no other package builds a
		// variant, and a union has no zero value.
		{"union whose every variant has authority", `type Host = Local | Remote

type Local { root string }

type Remote { addr string }

func NewHost() Host needs Ambient { Local{root: "/"} }
`, nil},
		{"union with a variant without authority", `type Host = Local | Remote

type Local { root string }

type Remote { Addr string }

func NewHost() Host needs Ambient { Local{root: "/"} }
`, []string{"another package builds it: variant fixture.Remote needs an unexported field"}},
		// fu-constructible (b9 D2 r2 verify, On base P4): an exported field
		// of authority records inside a slice or Map, in a record or in a
		// variant, is built empty by another package. check.Constructible
		// is false for each, so the guard read it as authority.
		{"record with an exported slice of records with authority", `type Host { Caps []Cap }

type Cap { root string }

func NewHost() Host needs Ambient { Host{Caps: []Cap{}} }
`, []string{"another package builds it: it needs an unexported field"}},
		{"record with an exported Map of records with authority", `type Host { Caps Map[string, Cap] }

type Cap { root string }

func NewHost() Host needs Ambient { Host{Caps: Map[string, Cap]{}} }
`, []string{"another package builds it: it needs an unexported field"}},
		{"union with a variant holding an exported slice of records with authority", `type Host = Local | Remote

type Local { root string }

type Remote { Caps []Local }

func NewHost() Host needs Ambient { Local{root: "/"} }
`, []string{"another package builds it: variant fixture.Remote needs an unexported field"}},
		// Control: an exported field of a record with authority, held by
		// value, is not built by another package.
		{"record with an exported field of a record with authority", `type Host { C Cap }

type Cap { root string }

func NewHost() Host needs Ambient { Host{C: Cap{root: "/"}} }
`, nil},
		// fu-ambient-ctor: the same Host, but its package hands out a Cap
		// with no row, so another package builds
		// fixture.Host{C: fixture.NewCap()}.
		{"record with an exported field of a record whose constructor has no row", `type Host { C Cap }

type Cap { root string }

func NewHost() Host needs Ambient { Host{C: Cap{root: "/"}} }

func NewCap() Cap { Cap{root: "/"} }
`, []string{"fixture.NewCap hands out fixture.Cap, which fixture.Host needs, and does not list Ambient in its written row or take a value that needs authority"}},
		// fu-hostguard2: an operation type is judged by check.Buildable
		// too. A sealed one is built only through its package's
		// implementers, so a sealed Host whose implementer has authority
		// passes (before, every operation type was reported).
		{"sealed operation type whose implementer has authority", `type Host { peek() int }

type Live { root string }

func peek(_ Live) int { 1 }

func NewHost() Host needs Ambient { Live{root: "/"} }
`, nil},
		{"sealed operation type with an open implementer", `type Host { peek() int }

type Live { Root string }

func peek(_ Live) int { 1 }

func NewHost() Host needs Ambient { Live{Root: "/"} }
`, []string{sealedText}},
		{"open operation type", `type Host { Peek() int }

type Live { root string }

func Peek(_ Live) int { 1 }

func NewHost() Host needs Ambient { Live{root: "/"} }
`, []string{sealedText}},
		{"basic", `type Host int

func NewHost() Host needs Ambient { 0 }
`, []string{fmt.Sprintf(strict, "int")}},
		// check.Constructible is false here, but the empty literal and
		// the zero value need no authority.
		{"slice of records with authority", `type Host []Cap

type Cap { root string }

func NewHost() Host needs Ambient { Host{} }
`, []string{fmt.Sprintf(strict, "[]fixture.Cap")}},
		{"Map", `type Host Map[string, string]

func NewHost() Host { Host{} }
`, []string{fmt.Sprintf(strict, "Map[string, string]"), noAmbient}},
	} {
		f, err := syntax.Parse(row.decls + reach)
		if err != nil {
			t.Fatalf("%s: %v", row.kind, err)
		}
		fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
		// Only std binds runtime/v1 (S108); the fixture is checked as a
		// program package, so its extern has that one diagnostic.
		for _, d := range ds {
			if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
				t.Fatalf("%s: %v", row.kind, ds)
			}
		}
		hosts := hostProviderTypes(fake)
		if len(hosts) != 1 || hosts[0].Name != "Host" {
			t.Errorf("%s: the host providers are %v, want its Host", row.kind, hosts)
			continue
		}
		if got := hostAuthorityViolations(fake, hosts[0], outside); !slices.Equal(got, row.want) {
			t.Errorf("%s: the violations are %q, want %q", row.kind, got, row.want)
		}
	}
}

// handShape is a result type built from a base by a word of constructors
// (TestStdHostHandOuts), with whether a value of it hands out Cap.
type handShape struct {
	name, decls string
	typ         string
	exposes     bool
	fn          bool // a function type: no record field holds it
}

// handShapes are the words of at most depth constructors over Cap (the
// record that fixture.Host needs), Other (a record with authority that
// Host does not need) and int. The model: a value hands out Cap when the
// base is Cap and no constructor is P (a record whose field holding it is
// unexported, so another package cannot read it).
func handShapes(depth int) []handShape {
	var out []handShape
	var grow func(s handShape, n int)
	grow = func(s handShape, n int) {
		out = append(out, s)
		if n == depth {
			return
		}
		w := fmt.Sprintf("W%d", n)
		x := s.typ
		words := []handShape{
			{name: "S" + s.name, decls: s.decls, typ: "[]" + x, exposes: s.exposes},
			{name: "M" + s.name, decls: s.decls, typ: "Map[string, " + x + "]", exposes: s.exposes},
			{name: "F" + s.name, decls: s.decls, typ: "func() " + x, exposes: s.exposes, fn: true},
			{name: "T" + s.name, decls: s.decls + "type " + w + " (" + x + ", int)\n", typ: w, exposes: s.exposes},
			// An operation type: a call of its operation returns X.
			{name: "Q" + s.name, decls: s.decls + "type " + w + " { Get() " + x + " }\n", typ: w, exposes: s.exposes},
		}
		if !s.fn {
			words = append(words,
				handShape{name: "H" + s.name, decls: s.decls + "type " + w + " { X " + x + " }\n", typ: w, exposes: s.exposes},
				handShape{name: "P" + s.name, decls: s.decls + "type " + w + " { x " + x + ", N int }\n", typ: w},
				handShape{name: "U" + s.name, decls: s.decls + "type " + w + " = " + w + "A | " + w + "B\ntype " + w + "A { X " + x + " }\ntype " + w + "B {}\n", typ: w, exposes: s.exposes},
				handShape{name: "O" + s.name, decls: s.decls, typ: "Option[" + x + "]", exposes: s.exposes},
			)
		}
		for _, word := range words {
			grow(word, n+1)
		}
	}
	for _, b := range []handShape{{name: "Cap", typ: "Cap", exposes: true}, {name: "Other", typ: "Other"}, {name: "int", typ: "int"}} {
		grow(b, 0)
	}
	return out
}

// handModes are the ways package fixture hands out a value of R: an
// exported function with no row, with Ambient, with a parameter that needs
// authority (a Host), with an open parameter, a function that passes R to
// its caller's function, and an exported let. Every body calls itself, so
// no value is built: only the signature is judged.
var handModes = []struct {
	name    string
	src     func(r string) string
	guarded bool   // a caller needs authority: no violation
	what    string // the violation's subject
}{
	{"plain", func(r string) string { return "func Give() " + r + " { Give() }\n" }, false, "fixture.Give hands out"},
	{"ambient", func(r string) string { return "func Give() " + r + " needs Ambient { Give() }\n" }, true, "fixture.Give hands out"},
	{"authority-param", func(r string) string { return "func Give(h Host) " + r + " { Give(h) }\n" }, true, ""},
	{"open-param", func(r string) string { return "func Give(n int) " + r + " { Give(n) }\n" }, false, "fixture.Give hands out"},
	{"callback", func(r string) string { return "func Give(f func(v " + r + ")) {}\n" }, false, "fixture.Give hands out"},
	{"let", func(r string) string { return "let Given " + r + " = given()\n\nfunc given() " + r + " { given() }\n" }, false, "fixture.Given is an exported let that holds"},
}

// endsInFuncType reports whether the type text ends in a function type
// at its top level, as `[]func() Cap` and `func() Map[string, Cap]` do and
// `Map[string, func() Cap]` does not: a row written after it is that
// function type's.
func endsInFuncType(typ string) bool {
	i := strings.LastIndex(typ, "func() ")
	if i < 0 {
		return false
	}
	rest := typ[i:]
	return strings.Count(rest, "[") == strings.Count(rest, "]") && strings.Count(rest, "(") == strings.Count(rest, ")")
}

// TestStdHostHandOuts is fu-ambient-ctor's guard: a host provider that
// holds Cap by value, `type Host { C Cap }` with `type Cap { root string }`,
// is built by any package that gets a Cap. So the guard asks every exported
// function and let of the provider's package that hands out a Cap, at any
// depth of its result, to need authority. Before, it asked only the
// functions whose result is Host itself, so `func NewCap() Cap` with no row
// passed. For each generated result shape and each mode, the violations
// are the model's.
func TestStdHostHandOuts(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	const common = `type Cap { root string }

type Other { x int }

type Gone { Path string }

func NewHost() Host needs Ambient { NewHost() }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
	// The ways Host needs Cap (check.Authorities): a field, a union
	// variant (the other variant has authority), a defined tuple in a
	// field, and the implementer of a sealed operation type. A Host with
	// an unexported field needs only its own authority: a Cap in another
	// package's hands builds no Host, so handing one out is no violation.
	hostForms := []struct {
		name, decls string
		needsCap    bool
	}{
		{"field", "type Host { C Cap }\n", true},
		{"variant", "type Host = HA | HB\n\ntype HA { C Cap }\n\ntype HB { root string }\n", true},
		{"tuple", "type Host { P HP }\n\ntype HP (Cap, int)\n", true},
		{"sealed", "type Host { peek() int }\n\ntype Live { C Cap }\n\nfunc peek(_ Live) int { 1 }\n", true},
		{"own-authority", "type Host { c Cap }\n", false},
	}
	shapes := handShapes(2)
	// Per base: itself, 9 words, and their children: 9 each, 5 under F.
	if want := 3 * (1 + 9 + 8*9 + 5); len(shapes) != want {
		t.Fatalf("generated %d shapes, want %d", len(shapes), want)
	}
	flagged := 0
	for _, h := range hostForms {
		for _, s := range shapes {
			for _, m := range handModes {
				src := h.decls + common + s.decls + m.src(s.typ)
				f, err := syntax.Parse(src)
				if err != nil {
					t.Fatalf("%s/%s: %v\n%s", s.name, m.name, err, src)
				}
				fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
				for _, d := range ds {
					if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
						t.Fatalf("%s/%s: %v\n%s", s.name, m.name, ds, src)
					}
				}
				var host *check.TypeName
				for _, tn := range hostProviderTypes(fake) {
					if tn.Name == "Host" {
						host = tn
					}
				}
				if host == nil {
					t.Fatalf("%s/%s: no host provider Host", s.name, m.name)
				}
				// In `func Give() []func() Cap needs Ambient` the row is the
				// result's function type's, not Give's, so Give has no row:
				// reported (conservative: a call of the value needs Ambient).
				guarded := m.guarded && !(m.name == "ambient" && endsInFuncType(s.typ))
				var want []string
				if h.needsCap && s.exposes && !guarded {
					want = []string{m.what + " fixture.Cap, which fixture.Host needs,"}
					flagged++
				}
				got := hostAuthorityViolations(fake, host, outside)
				ok := len(got) == len(want)
				for i := range got {
					ok = ok && strings.HasPrefix(got[i], want[i])
				}
				if !ok {
					t.Errorf("%s/%s/%s: the violations are %q, want one starting %q\n%s", h.name, s.name, m.name, got, want, s.decls+m.src(s.typ))
				}
			}
		}
	}
	t.Logf("%d host forms x %d shapes x %d modes, %d violations", len(hostForms), len(shapes), len(handModes), flagged)
}

// TestProgramPackagesHaveNoHostProvider is the host guard over program
// packages (fu-hostguard2). A program package binds no Go that reaches the
// host: an extern of runtime/v1 is S108 and one outside the pure allowlist
// is S109 (check, externTargetRefusal). So a program package the checker
// accepts has no host provider of its own, by the same walk as std's; it
// reaches the host only through a std provider it is given or holds, and
// that provider's construction authority is std's (a program record
// holding fs.Host is not Buildable outside fs). Each row is a program
// package: either the checker refuses it with the named code, or it
// checks clean and the walk finds no host provider.
func TestProgramPackagesHaveNoHostProvider(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	for _, row := range []struct {
		name, src string
		refused   string // the refusal's text, or "" for a clean package
	}{
		// The walk before fu-hostguard2 counted any non-runtime extern as
		// reaching the host, so this reported Name.
		{"pure allowlist extern", `type Name string

func Up(n Name) string { up(string(n)) }

extern go strings.ToUpper func up(s string) string
`, ""},
		{"host Go extern", `type Env {}

func Get(_ Env) string { getenv("HOME") }

extern go os.Getenv func getenv(k string) string
`, "reaches the host"},
		{"runtime extern", `type Disk {}

func Read(_ Disk) string { hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string
`, "only std binds the Ergo runtime"},
		{"record holding a std host provider", `import fs

type Disk { H fs.Host }

func Read(d Disk) string {
	let t = fs.ReadFile(d.H, "/nonexistent/loop-probe-x") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "fail"
	}
	t
}
`, ""},
	} {
		f, err := syntax.Parse(row.src)
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		app, ds := check.Check("app", []*syntax.File{f}, nil)
		if row.refused != "" {
			if len(ds) == 0 || !strings.Contains(ds[0].Message, row.refused) {
				t.Errorf("%s: diagnostics %v, want one saying %q", row.name, ds, row.refused)
			}
			continue
		}
		if len(ds) != 0 {
			t.Errorf("%s: diagnostics %v, want none", row.name, ds)
			continue
		}
		if hosts := hostProviderTypes(app); len(hosts) != 0 {
			t.Errorf("%s: a checked program package has host providers %v", row.name, hosts)
		}
		for _, tn := range app.Types {
			if tn.Name == "Disk" && check.Buildable(tn.Type, outside) {
				t.Errorf("%s: another package builds Disk, which holds fs.Host", row.name)
			}
		}
	}
}

// hostProviderTypes are pkg's host providers, in source order.
func hostProviderTypes(pkg *check.Package) []*check.TypeName {
	// reaches: an extern with an impure body, or a function whose body
	// names one of pkg's functions that reaches; a fixpoint over pkg.
	reaches := map[*check.Func]bool{}
	for _, f := range pkg.Funcs {
		if f.Decl != nil && f.Decl.Extern != nil {
			// A runtime/v1 body reaches unless it is pure; any other Go
			// package unless it is in the pure allowlist (G9), which a
			// program package's extern binds (fu-hostguard2).
			target, name := f.Decl.Extern.Split()
			if target == v1Path {
				body, err := v1.BodyOf(name)
				reaches[f] = err != nil || !body.Pure
			} else {
				reaches[f] = !check.PureGoPackage(target)
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range pkg.Funcs {
			if reaches[f] || f.Decl == nil || f.Decl.Body == nil {
				continue
			}
			syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					if g, ok := pkg.Info.Uses[id].(*check.Func); ok && g.Pkg == pkg && reaches[g] {
						reaches[f] = true
					}
				}
				return !reaches[f]
			})
			changed = changed || reaches[f]
		}
	}
	// Every defined type is a candidate, whatever its underlying type: a
	// record, a basic type, a slice, a Map, a union (fu-hostguard).
	var out []*check.TypeName
	for _, tn := range pkg.Types {
		if _, ok := tn.Type.(*check.Named); !ok {
			continue
		}
		for _, f := range pkg.Funcs {
			if !reaches[f] || f.Sig == nil || len(f.Sig.Params) == 0 {
				continue
			}
			if p, ok := f.Sig.Params[0].Type.(*check.Named); ok && p.Obj == tn {
				out = append(out, tn)
				break
			}
		}
	}
	return out
}

// hostAuthorityViolations are the ways the host provider tn of pkg breaks
// the rule, as seen from the package outside.
func hostAuthorityViolations(pkg *check.Package, tn *check.TypeName, outside *check.Package) []string {
	var out []string
	// Does another package build tn? One checker fact, check.Buildable;
	// the kind of tn's underlying type only words the message.
	if check.Buildable(tn.Type, outside) {
		switch under := tn.Type.(*check.Named).Underlying().(type) {
		case *check.Basic, *check.Slice, *check.Map:
			out = append(out, "another package builds it from a literal or its zero value: a defined type over "+under.String()+" holds no authority; it needs a record with an unexported field, or a union of such records")
		case *check.Union:
			for _, v := range under.Variants {
				if check.Buildable(v, outside) {
					out = append(out, "another package builds it: variant "+v.String()+" needs an unexported field")
				}
			}
		case *check.Interface:
			out = append(out, "another package makes a value of it: an operation type needs an unexported operation, and each type of its package that implements it needs authority")
		default:
			out = append(out, "another package builds it: it needs an unexported field")
		}
	}
	// Who hands out what tn needs (fu-ambient-ctor)? The records another
	// package needs to build tn are check.Authorities; an exported
	// function that hands out a value of one (check.Exposes of its
	// results, and of what it passes to a function parameter) lists Ambient
	// or takes a value that needs authority itself, and no exported let
	// holds one.
	need := check.Authorities(tn.Type, outside)
	needed := func(ts []check.Type) []*check.TypeName {
		var hit []*check.TypeName
		for _, t := range ts {
			// tn itself, whatever its kind (an authority record or not).
			if n, ok := t.(*check.Named); ok && n.Obj == tn && !slices.Contains(hit, tn) {
				hit = append(hit, tn)
			}
			for _, e := range check.Exposes(t, outside) {
				if slices.Contains(need, e) && !slices.Contains(hit, e) {
					hit = append(hit, e)
				}
			}
		}
		return hit
	}
	what := func(e *check.TypeName) string {
		return e.Pkg.Name + "." + e.Name + ", which " + pkg.Name + "." + tn.Name + " needs,"
	}
	for _, f := range pkg.Funcs {
		if !isExportedName(f.Name) || f.Sig == nil || writesAmbient(f) || takesAuthority(f, outside) {
			continue
		}
		for _, e := range needed(handsOut(f.Sig)) {
			if e == tn {
				out = append(out, pkg.Name+"."+f.Name+" returns it and does not list Ambient in its written row")
			} else {
				out = append(out, pkg.Name+"."+f.Name+" hands out "+what(e)+" and does not list Ambient in its written row or take a value that needs authority")
			}
		}
	}
	for _, o := range pkg.Exported() {
		if v, ok := o.(*check.Var); ok {
			for _, e := range needed([]check.Type{v.Type}) {
				out = append(out, pkg.Name+"."+v.Name+" is an exported let that holds "+what(e)+" and a let has no row")
			}
		}
	}
	return out
}

// handsOut are the types of the values a call of a function of sig gives
// its caller: its results, and the parameters of each function-typed
// parameter, which the function passes to the caller's function.
func handsOut(sig *check.Signature) []check.Type {
	out := slices.Clone(sig.Results)
	for _, p := range sig.Params {
		t := p.Type
		if n, ok := t.(*check.Named); ok {
			t = n.Underlying()
		}
		if fs, ok := t.(*check.Signature); ok {
			for _, q := range fs.Params {
				out = append(out, q.Type)
			}
		}
	}
	return out
}

// takesAuthority reports whether a caller of f needs authority already: a
// parameter of a type that outside cannot build.
func takesAuthority(f *check.Func, outside *check.Package) bool {
	for _, p := range f.Sig.Params {
		if !check.Buildable(p.Type, outside) {
			return true
		}
	}
	return false
}

// writesAmbient reports whether f's written row names the prelude's
// Ambient.
func writesAmbient(f *check.Func) bool {
	if f.Decl == nil || f.Decl.Type == nil || f.Decl.Type.Needs == nil {
		return false
	}
	for _, e := range f.Sig.Uses.Elems() {
		if n, ok := e.(*check.Named); ok && n.Obj.Name == "Ambient" && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" {
			return true
		}
	}
	return false
}
