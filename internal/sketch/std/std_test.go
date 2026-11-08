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
// builds one, and each exported function of every std package whose
// signature mentions T or a record T needs (check.Authorities), at any
// depth (check.Mentions), lists Ambient in its written row or takes a
// parameter of one of them; no exported let or named context mentions one
// (fu-ambient-ctor r5; TestStdHostHandOuts). Whether another package builds T
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
			for _, v := range hostAuthorityViolationsIn(pkgs, pkg, tn, outside) {
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
	// Ambient has one source, the program entry (fu-ambient-ctor r7): no
	// std type implements it and no std context binds it. The fixture rows
	// check the walk: the batch-10 r6 forge's implementer, and contexts
	// that bind a value of Ambient (S106 since r7, still reported here).
	var ambient *check.TypeName
	for _, tn := range prelude.Types {
		if tn.Name == "Ambient" {
			ambient = tn
		}
	}
	if ambient == nil {
		t.Fatal("no Ambient in the prelude")
	}
	for _, v := range ambientSources(pkgs, ambient) {
		t.Errorf("%s", v)
	}
	for _, row := range []struct {
		kind, src string
		want      []string
	}{
		{"implementer", "type Fake {}\n\nfunc ambient(_ Fake) {}\n", []string{"fixture.Fake implements Ambient, whose one provider is the program entry"}},
		{"implementer by borrow", "type Fake { n int }\n\nfunc ambient(_ *Fake) {}\n", []string{"fixture.Fake implements Ambient, whose one provider is the program entry"}},
		{"a function of another name", "type Fake {}\n\nfunc other(_ Fake) {}\n", nil},
		{"unexported named context", "type Fake {}\n\nfunc ambient(_ Fake) {}\n\nlet c = context { Ambient: Fake }\n", []string{"fixture.Fake implements Ambient, whose one provider is the program entry", "fixture.c is a named context that binds Ambient"}},
		{"named context of another capability", "let C = context { Scheduler: LiveScheduler }\n", nil},
		{"named context", "type Fake {}\n\nfunc ambient(_ Fake) {}\n\nlet C = context { Ambient: Fake }\n", []string{"fixture.C is a named context that binds Ambient", "fixture.Fake implements Ambient, whose one provider is the program entry"}},
		{"context literal in a with", "func g() int needs Ambient { 1 }\n\nfunc Rebind(a Ambient) int {\n\twith context { Ambient: a } { g() }\n}\n", []string{"fixture: a context literal binds Ambient"}},
	} {
		f, err := syntax.Parse(row.src)
		if err != nil {
			t.Fatalf("%s: %v", row.kind, err)
		}
		fake, _ := check.Check("fixture", []*syntax.File{f}, nil)
		if got := ambientSources([]*check.Package{fake}, ambient); !slices.Equal(got, row.want) {
			t.Errorf("Ambient source %s: %q, want %q", row.kind, got, row.want)
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
	// mentions is the violation of an exported function item of fixture
	// whose signature mentions names (fu-ambient-ctor r5), and letMentions
	// an exported let's.
	mentions := func(item, names string) string {
		return "fixture." + item + " mentions " + names + " (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"
	}
	letMentions := func(item, names string) string {
		return "fixture." + item + " is an exported let that mentions " + names + " (fixture.Host or a record it needs), and a let has no row"
	}
	// builds is the mint rule's violation of a function item of fixture
	// that builds what (fu-ambient-ctor r6), and placeBuilds one of a
	// place with no row.
	builds := func(item, what string) string {
		return "fixture." + item + " builds " + what + " and neither lists Ambient in its written row nor takes a parameter of a held type"
	}
	placeBuilds := func(item, what, place string) string {
		return "fixture." + item + " builds " + what + " in " + place + ", which has no row"
	}
	noAmbient := mentions("NewHost", "fixture.Host")
	const sealedText = "another package makes a value of it: an operation type needs an unexported operation, and each type of its package that implements it needs authority"
	// tokBase is the laundering probe's package without its hand-out: Wrap
	// builds a Host from a Tok, so Wrap is reported exactly when another
	// package gets a Tok with no authority.
	const tokBase = `type Host { root string }

type Tok { v int }

func Wrap(_ Tok) Host { Host{root: "/"} }

func NewHost() Host needs Ambient { Host{root: "/"} }

`
	// Under the r5 rule Wrap mentions Host and takes no Host, so it is
	// reported in every tokBase row, whatever hands out the Tok: the rows
	// of rounds 1 to 4 stay, and the hostBase rows below put Host itself
	// in each channel.
	wrapped := []string{mentions("Wrap", "fixture.Host"), builds("Wrap", "fixture.Host (a literal)")}
	const hostBase = `type Host { root string }

func NewHost() Host needs Ambient { NewHost() }

`
	// hostLit is a provider whose constructor builds it under Ambient.
	const hostLit = `type Host { root string }

func NewHost() Host needs Ambient { Host{root: "/"} }

`
	// readerBase is the batch-10 r5 B1 probes' package: Host implements
	// the open operation type Reader (Get), so a Host can leave as a
	// Reader, which no signature walk sees.
	const readerBase = hostLit + `type Reader { Get() string fails Gone }

func Get(h Host) string fails Gone { try Read(h) }

`
	// sealedBase is a sealed operation-type provider (stdlib.md, Host
	// packages 6 allows one): its value is a Live, so a mention of Host
	// mentions Live too (batch-10 r4 M1).
	const sealedBase = `type Host { peek() int }

type Live { root string }

func peek(_ Live) int { 1 }

func NewHost() Host needs Ambient { Live{root: "/"} }

`
	give := []string{mentions("Give", "fixture.Host")}
	sealedGive := []string{mentions("Give", "fixture.Host, fixture.Live")}
	for _, row := range []struct {
		kind, decls string
		want        []string
	}{
		{"record with no unexported field", `type Host {}

func NewHost() Host { Host }
`, []string{"another package builds it: it needs an unexported field", noAmbient, builds("NewHost", "fixture.Host (a bare record)")}},
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
`, []string{mentions("NewCap", "fixture.Cap"), builds("NewCap", "fixture.Cap (a literal)")}},
		// fu-ambient-ctor r2 (batch-10 verifier M1, the laundering probe):
		// Tok has authority but MakeTok hands it out with no row, so
		// fixture.Wrap(fixture.MakeTok()) is a Host with no Ambient. A
		// parameter is authority only when no free hand-out gives it.
		{"record built from a parameter that a free constructor hands out", `type Host { root string }

type Tok { v int }

func MakeTok() Tok { Tok{v: 1} }

func Wrap(_ Tok) Host { Host{root: "/"} }

func NewHost() Host needs Ambient { Host{root: "/"} }
`, wrapped},
		// fu-ambient-ctor r3 (batch-10 r2 verifier M1): the same Tok handed
		// out as a failure. Another package writes
		// fixture.Wrap(fixture.MakeTok() catch { case fixture.Tok as t: t }).
		{"record built from a parameter that a free function hands out as its failure", `type Host { root string }

type Tok { v int }

func MakeTok() int fails Tok { return Tok{v: 1} }

func Wrap(_ Tok) Host { Host{root: "/"} }

func NewHost() Host needs Ambient { Host{root: "/"} }
`, wrapped},
		// The same Tok from a with of an exported named context.
		{"record built from a parameter that an exported context hands out", `type Host { root string }

type Tok { v int }

type Toks { Next() Tok }

type Mint { n int }

func Next(_ Mint) Tok { Tok{v: 1} }

let Minting = context { Toks: Mint{n: 1} }

func Wrap(_ Tok) Host { Host{root: "/"} }

func NewHost() Host needs Ambient { Host{root: "/"} }
`, wrapped},
		// Was a control (MakeTok lists Ambient, so Wrap's caller had
		// authority); the r5 rule asks no such question and reports Wrap.
		{"record built from a parameter whose constructor needs Ambient", `type Host { root string }

type Tok { v int }

func MakeTok() Tok needs Ambient { Tok{v: 1} }

func Wrap(_ Tok) Host { Host{root: "/"} }

func NewHost() Host needs Ambient { Host{root: "/"} }
`, wrapped},
		// fu-ambient-ctor r4 (batch-10 r3 verifier M1): a needs row. The
		// caller binds its own Sink, so Put receives the Tok Give passes.
		{"record built from a parameter that a function passes to its needs row", tokBase + `type Sink { Put(t Tok) }

func Give() needs Sink { Sink.Put(Tok{v: 1}) }
`, wrapped},
		// Was a control (the caller's Src gives Take a Tok); Wrap is
		// reported under the r5 rule.
		{"record that a needs row gives the function, not its caller", tokBase + `type Src { Get() Tok }

func Take() needs Src { Take() }
`, wrapped},
		// A needs row of a handed-out function value. (An operation
		// writes no needs row: S108, types.md Capabilities 4.)
		{"record that a function value passes to its needs row", tokBase + `type Sink { Put(t Tok) }

func Give() func() int needs Sink { Give() }
`, wrapped},
		// The batch-10 r3 verifier's other channel probes.
		{"record in a generic record instance", tokBase + `type Box[T] { V T }

func Give() Box[Tok] { Give() }
`, wrapped},
		{"record from a generic operation instance", tokBase + `type Src[T] { Get() T }

func Give() Src[Tok] { Give() }
`, wrapped},
		{"record from a Seq", tokBase + "func Give() Seq[Tok] { Give() }\n", wrapped},
		{"record from an exported let of function type", tokBase + "let MakeTok = func() Tok { Tok{v: 1} }\n", wrapped},
		{"record from an operation of an unexported type in an exported let", tokBase + `type mint {}

func Next(_ mint) Tok { Tok{v: 1} }

let Minter = mint
`, wrapped},
		{"record passed by borrow to a function the caller supplies", tokBase + "func Give(f func(p *Tok)) {}\n", wrapped},
		// The inferred failure row of an unexported function, handed out
		// as a value.
		{"record in the inferred failure row of a function value", tokBase + `func raise() int fails Tok { return Tok{v: 1} }

func mk() int { try raise() }

let Mk = mk
`, wrapped},
		// A type parameter: the caller picks it, and a generic function
		// makes no value of it (TestStdHostHandOutsAcrossPackages), so
		// Make gives no Tok; Give passes one to its constraint's operation,
		// which the caller's type implements.
		{"record that a generic function cannot make", tokBase + "func Make[T]() T { Make[T]() }\n", wrapped},
		{"record passed to a type parameter's operation", tokBase + `type Sink { Put(t Tok) }

func Give[T Sink](s T) { Give(s) }
`, wrapped},
		// A record met first where the caller supplies it, then where the
		// caller gets it (batch-10 r3 T1: the walk's seen-set keeps both).
		{"record met at a supplied position before a received one", tokBase + `type W { X Tok }

type Pair { F func(a W), Y W }

func Give() Pair { Give() }
`, wrapped},
		// fu-ambient-ctor r5: Host itself in each channel of rounds 1 to 4.
		{"Host passed to a needs row", hostBase + `type Sink { Put(h Host) }

func Give() needs Sink { Give() }
`, give},
		{"Host from a needs row's provider", hostBase + `type Src { Get() Host }

func Give() needs Src { Give() }
`, give},
		{"Host passed to a function value's needs row", hostBase + `type Sink { Put(h Host) }

func Give() func() int needs Sink { Give() }
`, give},
		{"Host in a generic record instance", hostBase + "type Box[T] { V T }\n\nfunc Give() Box[Host] { Give() }\n", give},
		{"Host from a generic operation instance", hostBase + "type Src[T] { Get() T }\n\nfunc Give() Src[Host] { Give() }\n", give},
		{"Host from a Seq", hostBase + "func Give() Seq[Host] { Give() }\n", give},
		{"Host from an exported let of function type", hostBase + "let Mk = func() Host { Host{root: \"/\"} }\n", []string{letMentions("Mk", "fixture.Host"), placeBuilds("Mk", "fixture.Host (a literal)", "a let")}},
		{"Host from a function of an unexported type in an exported let", hostBase + `type mint {}

func Next(_ mint) Host { Host{root: "/"} }

let Minter = mint
`, []string{mentions("Next", "fixture.Host"), builds("Next", "fixture.Host (a literal)")}},
		{"Host passed by borrow to a function the caller supplies", hostBase + "func Give(f func(p *Host)) {}\n", give},
		{"Host in the inferred failure row of a function value", hostBase + `type Oops { H Host }

func raise() int fails Oops { try raise() }

func mk() int { try raise() }

let Mk = mk
`, []string{letMentions("Mk", "fixture.Host")}},
		{"Host passed to a type parameter's operation", hostBase + "type Sink { Put(h Host) }\n\nfunc Give[T Sink](s T) { Give(s) }\n", give},
		{"Host as a phantom type argument", hostBase + "type Ph[T] { n int }\n\nfunc Give() Ph[Host] { Give() }\n", give},
		{"Host in the constraint of a type parameter no parameter uses", hostBase + "type Sink { Put(h Host) }\n\nfunc Give[T Sink]() int { 1 }\n", give},
		{"generic function that names no Host", hostBase + "func Make[T]() T { Make[T]() }\n", nil},
		{"Host met at a supplied position before a received one", hostBase + `type W { X Host }

type Pair { F func(a W), Y W }

func Give() Pair { Give() }
`, give},
		{"Host in a failure's field", hostBase + "type Oops { H Host }\n\nfunc Give() int fails Oops { Give() }\n", give},
		{"Host in a Task's failure row argument", hostBase + "type Oops { H Host }\n\nfunc Give() Task[int, Oops] { Give() }\n", give},
		{"Host in a generic record's row argument", hostBase + "type Oops { H Host }\n\ntype Rw[E] { F func() int fails E }\n\nfunc Give() Rw[Oops] { Give() }\n", give},
		{"Host as a Map key", hostBase + "func Give() Map[Host, int] { Give() }\n", give},
		{"Host in a machine's state", hostBase + `type Idle { H Host }

type Busy {}

type MS = Idle | Busy

type Go {}

type Stop {}

type ME = Go | Stop

func step(s MS, e ME) Decision[MS, int] { Ignore }

type M machine[MS, ME] { initial: Busy, step: step }

func Give() M { Give() }
`, give},
		{"Host in a machine's signal", hostBase + `type Busy {}

type Idle {}

type MS = Idle | Busy

type Go { H Host }

type Stop {}

type ME = Go | Stop

func step(s MS, e ME) Decision[MS, int] { Ignore }

type M machine[MS, ME] { initial: Busy, step: step }

func Give() M { Give() }
`, give},
		// fu-ambient-ctor r6 (batch-10 r5 B1): a Host hidden behind a type
		// no signature names: an open operation type it implements, a
		// closure that captures it, a call's argument. Only the mint rule
		// sees these; each has a control that lists Ambient.
		{"P1 Host as an open operation type, function result", readerBase + "func Open() Reader { Host{root: \"/\"} }\n", []string{builds("Open", "fixture.Host (a literal)")}},
		{"P1 control: lists Ambient", readerBase + "func Open() Reader needs Ambient { Host{root: \"/\"} }\n", nil},
		{"P2 Host as an open operation type, named context", readerBase + "let Disk = context { Reader: Host{root: \"/\"} }\n", []string{placeBuilds("Disk", "fixture.Host (a literal)", "a named context")}},
		// An inferred row is not a written one: the mint rule, as the
		// mention rule, reads only what the source writes (decided by
		// "simpler, reversible"; std writes Ambient where it builds).
		{"build under an inferred Ambient", readerBase + "func mk() Host {\n\tlet h = NewHost()\n\tHost{..h, root: \"x\"}\n}\n", []string{builds("mk", "fixture.Host (a record update)")}},
		{"build in a closure under an inferred Ambient", readerBase + "func mk() string fails Gone {\n\tlet f = func() Host {\n\t\tlet h = NewHost()\n\t\tHost{..h, root: \"x\"}\n\t}\n\ttry Read(f())\n}\n", []string{builds("mk", "fixture.Host (a record update)")}},
		{"generated test parameter that holds a Host", readerBase + "type Box { H Host }\n\ntest T(b Box) {\n\tassert true\n}\n", []string{placeBuilds("T", "fixture.Host (a generated parameter)", "a test")}},
		{"let's zero value of a record that holds a Host", readerBase + "type Wrap { H Host, N int }\n\nfunc F() string fails Gone {\n\tlet w Wrap\n\ttry Read(w.H)\n}\n", []string{builds("F", "fixture.Host (a let's zero value)")}},
		{"build in a trailing block", readerBase + "func run(f func() string fails Gone) string fails Gone { try f() }\n\nfunc F() string fails Gone {\n\ttry run() {\n\t\ttry Read(Host{root: \"/\"})\n\t}\n}\n", []string{builds("F", "fixture.Host (a literal)")}},
		// fu-ambient-ctor r7 (batch-10 r6 Unproven E1, E3): an extern has
		// no Ergo body to ask. One whose signature mentions Host lists
		// Ambient; a generic extern's result at Host is a build at the call.
		{"E1 extern whose result is Host", readerBase + "extern go ergo.local/prototype/runtime/v1.FSReadFile func mk() Host\n\nfunc Open() Reader { mk() }\n", []string{"fixture.mk is an extern that mentions fixture.Host (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"}},
		{"E1 extern whose failure holds a Host", readerBase + "type Oops { H Host }\n\nextern go ergo.local/prototype/runtime/v1.FSReadFile func mk() int fails Oops\n", []string{"fixture.mk is an extern that mentions fixture.Host (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"}},
		{"E1 control: the extern lists Ambient", readerBase + "extern go ergo.local/prototype/runtime/v1.FSReadFile func mk() Host needs Ambient\n\nfunc Open() Reader needs Ambient { mk() }\n", nil},
		{"E3 generic extern's result at Host", readerBase + "extern go ergo.local/prototype/runtime/v1.WithCapacity func zero[T](n int) []T\n\nfunc Open() Reader { zero[Host](1)[0] }\n", []string{builds("Open", "fixture.Host (a generic extern's result)")}},
		{"E3 control: the call lists Ambient", readerBase + "extern go ergo.local/prototype/runtime/v1.WithCapacity func zero[T](n int) []T\n\nfunc Open() Reader needs Ambient { zero[Host](1)[0] }\n", nil},
		// No P2 control: a named context is closed, so a provider that
		// needs Ambient there is S102 (rows.md, Context 4).
		{"P3 Host as an open operation type, let", readerBase + "let Default Reader = Host{root: \"/\"}\n", []string{placeBuilds("Default", "fixture.Host (a literal)", "a let")}},
		{"P6 closure over a Host", readerBase + "func Reading() func() string fails Gone {\n\tlet h = Host{root: \"/\"}\n\tfunc() string fails Gone { try Read(h) }\n}\n", []string{builds("Reading", "fixture.Host (a literal)")}},
		{"P6 control: lists Ambient", readerBase + "func Reading() (func() string fails Gone) needs Ambient {\n\tlet h = Host{root: \"/\"}\n\tfunc() string fails Gone { try Read(h) }\n}\n", nil},
		{"P7 Host as a call's argument", readerBase + "func ReadNow() string fails Gone { try Read(Host{root: \"/\"}) }\n", []string{builds("ReadNow", "fixture.Host (a literal)")}},
		{"P7 control: lists Ambient", readerBase + "func ReadNow() string fails Gone needs Ambient { try Read(Host{root: \"/\"}) }\n", nil},
		{"P7 control: takes a Host", readerBase + "func ReadAt(h Host, root string) string fails Gone { try Read(Host{..h, root: root}) }\n", nil},
		// The one exemption: a parameter of Host (or of a record it needs)
		// itself. A borrow of it, or a record holding it, is none.
		{"parameter of Host itself", hostBase + "func Clone(h Host) Host { h }\n", nil},
		{"parameter of a borrow of Host", hostBase + "func Peek(h *Host) int { 1 }\n", []string{mentions("Peek", "fixture.Host")}},
		{"parameter of a record holding Host", hostBase + "type Box { H Host }\n\nfunc Unbox(b Box) Host { b.H }\n", []string{mentions("Unbox", "fixture.Host")}},
		// batch-10 r4 M1: a sealed operation-type provider handed out in an
		// Option, a slice, a callback, a let, a failure or a needs row.
		{"sealed Host in an Option", sealedBase + "func Give() Option[Host] { Give() }\n", sealedGive},
		{"sealed Host in a slice", sealedBase + "func Give() []Host { Give() }\n", sealedGive},
		{"sealed Host passed to a callback", sealedBase + "func Give(f func(h Host)) {}\n", sealedGive},
		{"sealed Host passed to a needs row", sealedBase + "type Sink { Put(h Host) }\n\nfunc Give() needs Sink { Give() }\n", sealedGive},
		{"sealed Host in an exported let", sealedBase + "let Given []Host = given()\n\nfunc given() []Host { given() }\n", []string{letMentions("Given", "fixture.Host, fixture.Live")}},
		{"sealed Host in a failure", sealedBase + "type Oops { H Host }\n\nfunc Give() int fails Oops { Give() }\n", sealedGive},
		// batch-10 r4 M2: a hidden field of a generic record filled through
		// its generic constructor, at a supplied position. With Tok and
		// Wrap (the verifier's probe), and with Host itself.
		{"Tok in a generic record's hidden field, supplied", tokBase + `type Box[T] { f T }

func NewBox[T](f T) Box[T] { Box[T]{f: f} }

func Run(b Box[func(t Tok)]) { Run(b) }
`, wrapped},
		{"Host in a generic record's hidden field, supplied", hostBase + `type Box[T] { f T }

func NewBox[T](f T) Box[T] { Box[T]{f: f} }

func Run(b Box[func(h Host)]) { Run(b) }
`, []string{mentions("Run", "fixture.Host")}},
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
`, []string{fmt.Sprintf(strict, "Map[string, string]"), noAmbient, builds("NewHost", "fixture.Host (a literal)")}},
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
// (TestStdHostHandOuts); base is the type it is built over.
type handShape struct {
	name, decls string
	typ         string
	base        string
	fn          bool // a function type: no record field holds it
}

// handShapes are the words of at most depth constructors over Cap (the
// record that fixture.Host needs), Host itself, Other (a record with
// authority that Host does not need) and int. The model of the r5 rule: a
// shape mentions its base whatever the constructors, P (a record whose
// field holding it is unexported) included.
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
			{name: "S" + s.name, decls: s.decls, typ: "[]" + x, base: s.base},
			{name: "M" + s.name, decls: s.decls, typ: "Map[string, " + x + "]", base: s.base},
			{name: "F" + s.name, decls: s.decls, typ: "func() " + x, base: s.base, fn: true},
			{name: "T" + s.name, decls: s.decls + "type " + w + " (" + x + ", int)\n", typ: w, base: s.base},
			// An operation type: a call of its operation returns X.
			{name: "Q" + s.name, decls: s.decls + "type " + w + " { Get() " + x + " }\n", typ: w, base: s.base},
		}
		if !s.fn {
			words = append(words,
				handShape{name: "H" + s.name, decls: s.decls + "type " + w + " { X " + x + " }\n", typ: w, base: s.base},
				handShape{name: "P" + s.name, decls: s.decls + "type " + w + " { x " + x + ", N int }\n", typ: w, base: s.base},
				handShape{name: "U" + s.name, decls: s.decls + "type " + w + " = " + w + "A | " + w + "B\ntype " + w + "A { X " + x + " }\ntype " + w + "B {}\n", typ: w, base: s.base},
				handShape{name: "O" + s.name, decls: s.decls, typ: "Option[" + x + "]", base: s.base},
			)
		}
		for _, word := range words {
			grow(word, n+1)
		}
	}
	for _, b := range []string{"Cap", "Host", "Other", "int"} {
		b := handShape{name: b, typ: b, base: b}
		grow(b, 0)
	}
	return out
}

// handModes are the ways package fixture names R in an exported item: an
// exported function with no row, with Ambient, with a parameter of Host
// (the one exemption), with an open parameter, a function that passes R to
// its caller's function, an exported let, and with a parameter of a record
// with authority that the package hands out with no row or under Ambient
// (fu-ambient-ctor r2), and through a failure, a function the caller
// supplies, a result's function or operation, and an exported named
// context (fu-ambient-ctor r3), a needs row and its source (r4), and a
// plain parameter. Every body calls itself, so no value is built: only the
// signature is judged. Under the r5 rule every mode but the guarded ones
// is reported when R mentions a held type: no mode is a control.
var handModes = []struct {
	name    string
	src     func(r string) string
	guarded bool   // Ambient in the row, or a parameter of Host: no violation
	what    string // the violation's subject
	also    string // a first violation's subject, before what: the context's provider function
}{
	{"plain", func(r string) string { return "func Give() " + r + " { Give() }\n" }, false, "fixture.Give mentions", ""},
	{"ambient", func(r string) string { return "func Give() " + r + " needs Ambient { Give() }\n" }, true, "fixture.Give mentions", ""},
	{"authority-param", func(r string) string { return "func Give(h Host) " + r + " { Give(h) }\n" }, true, "", ""},
	{"open-param", func(r string) string { return "func Give(n int) " + r + " { Give(n) }\n" }, false, "fixture.Give mentions", ""},
	{"callback", func(r string) string { return "func Give(f func(v " + r + ")) {}\n" }, false, "fixture.Give mentions", ""},
	{"let", func(r string) string { return "let Given " + r + " = given()\n\nfunc given() " + r + " { given() }\n" }, false, "fixture.Given is an exported let that mentions", ""},
	// fu-ambient-ctor r2: a parameter of a record with authority that the
	// package hands out with no row is no authority (the laundering probe);
	// the same parameter is when its hand-out needs Ambient.
	{"laundered-param", func(r string) string {
		return "type Tok { v int }\n\nfunc MakeTok() Tok { MakeTok() }\n\nfunc Give(t Tok) " + r + " { Give(t) }\n"
	}, false, "fixture.Give mentions", ""},
	{"let-param", func(r string) string {
		return "type Tok { v int }\n\nlet Given Tok = tok()\n\nfunc tok() Tok { tok() }\n\nfunc Give(t Tok) " + r + " { Give(t) }\n"
	}, false, "fixture.Give mentions", ""},
	{"ambient-param", func(r string) string {
		return "type Tok { v int }\n\nfunc MakeTok() Tok needs Ambient { MakeTok() }\n\nfunc Give(t Tok) " + r + " { Give(t) }\n"
	}, false, "fixture.Give mentions", ""},
	// fu-ambient-ctor r3 (batch-10 r2 verifier M1): every other way a call
	// gives its caller a value. A failure type is a named record (types.md,
	// Error types 1), so R is the exported field of one: the caller
	// catches it and reads the field.
	{"failure", func(r string) string {
		return "type GiveFail { X " + r + " }\n\nfunc Give() int fails GiveFail { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	{"failure-ambient", func(r string) string {
		return "type GiveFail { X " + r + " }\n\nfunc Give() int fails GiveFail needs Ambient { Give() }\n"
	}, true, "", ""},
	// Give calls f with a function that calls the caller's h with an R.
	{"callback-deep", func(r string) string { return "func Give(f func(g func(h func(v " + r + ")))) {}\n" }, false, "fixture.Give mentions", ""},
	// Give calls f with a function that fails with R.
	{"callback-failure", func(r string) string {
		return "type GiveFail { X " + r + " }\n\nfunc Give(f func(g func() int fails GiveFail)) {}\n"
	}, false, "fixture.Give mentions", ""},
	// The result is a function that calls the caller's f with an R.
	{"result-callback", func(r string) string { return "func Give() func(f func(v " + r + ")) { Give() }\n" }, false, "fixture.Give mentions", ""},
	// The result is an operation-type value whose operation fails with R,
	// or calls the caller's f with an R.
	{"operation-failure", func(r string) string {
		return "type GiveFail { X " + r + " }\n\ntype GiveSrc { Get() int fails GiveFail }\n\nfunc Give() GiveSrc { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	{"operation-callback", func(r string) string {
		return "type GiveSink { Put(f func(v " + r + ")) }\n\nfunc Give() GiveSink { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	// A with of an exported named context binds GiveSrc; GiveSrc.Get()
	// gives an R, and a named context needs no authority (rows.md,
	// Context 4). The same through an embedded unexported context.
	{"context", func(r string) string {
		return "type GiveSrc { Get() " + r + " }\n\ntype GiveImpl { n int }\n\nfunc Get(_ GiveImpl) " + r + " { Get(GiveImpl{n: 1}) }\n\nlet Given = context { GiveSrc: GiveImpl{n: 1} }\n"
	}, false, "fixture.Given is an exported context that mentions", "fixture.Get mentions"},
	{"context-embedded", func(r string) string {
		return "type GiveSrc { Get() " + r + " }\n\ntype GiveImpl { n int }\n\nfunc Get(_ GiveImpl) " + r + " { Get(GiveImpl{n: 1}) }\n\nlet given = context { GiveSrc: GiveImpl{n: 1} }\n\nlet Given = context { given }\n"
	}, false, "fixture.Given is an exported context that mentions", "fixture.Get mentions"},
	// fu-ambient-ctor r4 (batch-10 r3 verifier M1): the caller binds the
	// capability of Give's needs row: its Put receives the R.
	{"needs", func(r string) string {
		return "type GiveSink { Put(v " + r + ") }\n\nfunc Give() needs GiveSink { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	{"needs-ambient", func(r string) string {
		return "type GiveSink { Put(v " + r + ") }\n\nfunc Give() needs GiveSink | Ambient { Give() }\n"
	}, true, "", ""},
	// The record holds R where the caller supplies it (F's parameter)
	// before it holds it where the caller gets it (Y): batch-10 r3 T1.
	{"supplied-first", func(r string) string {
		return "type GiveW { X " + r + " }\n\ntype GivePair { F func(a GiveW), Y GiveW }\n\nfunc Give() GivePair { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	// Controls until r4 (the caller supplies the R, as its provider's
	// result or as an argument); the r5 rule reports a mention here too.
	{"needs-source", func(r string) string {
		return "type GiveSrc { Get() " + r + " }\n\nfunc Give() needs GiveSrc { Give() }\n"
	}, false, "fixture.Give mentions", ""},
	{"param", func(r string) string { return "func Give(x " + r + ") { Give(x) }\n" }, false, "fixture.Give mentions", ""},
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
// function, let and named context of the provider's package that mentions
// Host or Cap, at any depth of its type, to list Ambient or take a Host or
// a Cap (fu-ambient-ctor r5). For each generated shape and each mode, the
// violations are the model's: a mode that is not guarded is reported when
// the shape's base is Host, or Cap and the form needs Cap.
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
	// hosts is what a mention of Host names of the held types (Host and
	// what it needs, in walk order): a variant Host needs HB too.
	hostForms := []struct {
		name, decls string
		needsCap    bool
		hosts       string
	}{
		{"field", "type Host { C Cap }\n", true, "fixture.Host, fixture.Cap"},
		{"variant", "type Host = HA | HB\n\ntype HA { C Cap }\n\ntype HB { root string }\n", true, "fixture.Host, fixture.Cap, fixture.HB"},
		{"tuple", "type Host { P HP }\n\ntype HP (Cap, int)\n", true, "fixture.Host, fixture.Cap"},
		{"sealed", "type Host { peek() int }\n\ntype Live { C Cap }\n\nfunc peek(_ Live) int { 1 }\n", true, "fixture.Host, fixture.Cap"},
		{"own-authority", "type Host { c Cap }\n", false, "fixture.Host"},
	}
	shapes := handShapes(2)
	// Per base: itself, 9 words, and their children: 9 each, 5 under F.
	if want := 4 * (1 + 9 + 8*9 + 5); len(shapes) != want {
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
				// A parameter of a held type itself is the exemption.
				guarded = guarded || m.name == "param" && (s.typ == "Host" || s.typ == "Cap" && h.needsCap)
				names := ""
				switch {
				case s.base == "Host":
					names = h.hosts
				case s.base == "Cap" && h.needsCap:
					names = "fixture.Cap"
				}
				var want []string
				if names != "" && !guarded {
					if m.also != "" {
						want = append(want, m.also+" "+names+" (")
					}
					want = append(want, m.what+" "+names+" (")
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

// TestStdHostHandOutChains is fu-ambient-ctor r2's guard (batch-10
// verifier M1): a parameter is authority only when another package cannot
// obtain its value without authority, through any chain of the package's
// exported functions. Each case is a chain of records with authority
// T1..Tk (k = 1..3): Make1() T1, Make<i>(_ P) T<i> for a parameter P over
// T<i-1>, and Wrap(_ P) Host, where Host has an unexported field and
// NewHost needs Ambient. P is T<i-1> itself, a record with an exported
// field of it, or a union whose other variant holds a record that is
// never handed out. Each Make may need Ambient, and hands out its T<i> as
// its result, as its failure, to a function its caller supplies
// (fu-ambient-ctor r3: a failure is a hand-out too), or to an operation of
// its needs row (r4: the caller binds that capability). The functions are declared
// from Wrap down to Make1, so one pass over them in source order does not
// reach Wrap. The model until r4: Wrap is reported exactly when no link
// needs Ambient. The r5 rule asks no chain question: Wrap mentions Host
// and its parameter is not a Host, so it is reported in every chain, and
// no Make<i> is (T<i> is neither Host nor a record Host needs). The
// chains stay as a check that no link's form changes that.
func TestStdHostHandOutChains(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	const common = `type Host { root string }

type Other { x int }

type Gone { Path string }

func NewHost() Host needs Ambient { NewHost() }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
	// param is the parameter type over T<i> and its declarations.
	wraps := []string{"direct", "box", "union"}
	param := func(w string, i int) (string, string) {
		ti := fmt.Sprintf("T%d", i)
		switch w {
		case "box":
			b := fmt.Sprintf("B%d", i)
			return b, "type " + b + " { T " + ti + " }\n\n"
		case "union":
			u := fmt.Sprintf("U%d", i)
			return u, "type " + u + " = " + u + "A | " + u + "B\n\ntype " + u + "A { T " + ti + " }\n\ntype " + u + "B { O Other }\n\n"
		}
		return ti, ""
	}
	// channels are the ways Make<i> hands out T<i> (fu-ambient-ctor r3):
	// its result, its failure (the caller catches the T<i>), or the
	// caller's function it calls with a T<i>, or the operation of its
	// needs row that it calls with a T<i> (r4: the caller binds it).
	channels := []string{"result", "failure", "callback", "needs"}
	type link struct {
		ambient bool
		wrap    string // the parameter's form over the record before it
		channel string
	}
	var chains [][]link
	var grow func(c []link, depth int)
	grow = func(c []link, depth int) {
		if len(c) == depth {
			chains = append(chains, slices.Clone(c))
			return
		}
		for _, amb := range []bool{false, true} {
			ws := wraps
			if len(c) == 0 {
				ws = []string{""} // Make1 takes no parameter
			}
			for _, w := range ws {
				for _, ch := range channels {
					grow(append(c, link{amb, w, ch}), depth)
				}
			}
		}
	}
	for depth := 1; depth <= 3; depth++ {
		grow(nil, depth)
	}
	if want := 8 + 8*24 + 8*24*24; len(chains) != want {
		t.Fatalf("generated %d chains, want %d", len(chains), want)
	}
	flagged := 0
	for _, c := range chains {
		for _, wrapForm := range wraps {
			var b strings.Builder
			k := len(c)
			p, decl := param(wrapForm, k)
			b.WriteString(decl)
			b.WriteString("func Wrap(x " + p + ") Host { Wrap(x) }\n\n")
			for i := k; i >= 1; i-- {
				l := c[i-1]
				row := ""
				if l.ambient {
					row = " needs Ambient"
				}
				fmt.Fprintf(&b, "type T%d { v int }\n\n", i)
				var params, args string
				if i > 1 {
					p, decl := param(l.wrap, i-1)
					b.WriteString(decl)
					params, args = "x "+p, "x"
				}
				switch l.channel {
				case "result":
					fmt.Fprintf(&b, "func Make%d(%s) T%d%s { Make%d(%s) }\n\n", i, params, i, row, i, args)
				case "failure":
					fmt.Fprintf(&b, "func Make%d(%s) int fails T%d%s { Make%d(%s) }\n\n", i, params, i, row, i, args)
				case "callback":
					if params != "" {
						params, args = params+", ", args+", "
					}
					fmt.Fprintf(&b, "func Make%d(%sf func(v T%d))%s { Make%d(%sf) }\n\n", i, params, i, row, i, args)
				case "needs":
					needs := fmt.Sprintf(" needs Sink%d", i)
					if l.ambient {
						needs += " | Ambient"
					}
					fmt.Fprintf(&b, "type Sink%d { Put%d(v T%d) }\n\nfunc Make%d(%s)%s { Make%d(%s) }\n\n", i, i, i, i, params, needs, i, args)
				}
			}
			src := b.String() + common
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%v\n%s", err, src)
			}
			fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
			for _, d := range ds {
				if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
					t.Fatalf("%v\n%s", ds, src)
				}
			}
			var host *check.TypeName
			for _, tn := range hostProviderTypes(fake) {
				if tn.Name == "Host" {
					host = tn
				}
			}
			if host == nil {
				t.Fatalf("no host provider Host\n%s", src)
			}
			want := []string{"fixture.Wrap mentions fixture.Host (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"}
			flagged++
			if got := hostAuthorityViolations(fake, host, outside); !slices.Equal(got, want) {
				t.Errorf("the violations are %q, want %q\n%s", got, want, b.String())
			}
		}
	}
	t.Logf("%d chains x %d Wrap parameters, %d violations", len(chains), len(wraps), flagged)
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
// the rule, as seen from the package outside, when pkg is the only
// package whose exported names outside uses.
func hostAuthorityViolations(pkg *check.Package, tn *check.TypeName, outside *check.Package) []string {
	return hostAuthorityViolationsIn([]*check.Package{pkg}, pkg, tn, outside)
}

// hostAuthorityViolationsIn are the ways the host provider tn of pkg
// breaks the rule when outside may use the exported names of every
// package of surface (pkg among them). A record tn needs may be declared
// in another package, which hands it out (fu-ambient-ctor r4); another
// package cannot hand out what it got under Ambient without listing
// Ambient itself (rows: S102 at the call, in a function or a let's
// initializer), so the exported names of surface are every way in.
func hostAuthorityViolationsIn(surface []*check.Package, pkg *check.Package, tn *check.TypeName, outside *check.Package) []string {
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
	// Who mentions tn or what it needs (fu-ambient-ctor r5)? Held is tn and
	// the records another package needs to build it (check.Authorities).
	// An exported function whose signature mentions one (check.Mentions:
	// every part of every type, hidden fields, type arguments, rows and a
	// sealed operation type's implementers included) lists Ambient in its
	// written row, unless a parameter's type is one of held: its caller
	// holds the authority already (`Clone(h Host) Host`). An exported let
	// or named context has no row, so it mentions none. The rule
	// over-reports on purpose: it asks no question about what a caller
	// can obtain, which four rounds of a precise rule each got wrong.
	held := []*check.TypeName{tn}
	for _, e := range check.Authorities(tn.Type, outside) {
		if !slices.Contains(held, e) {
			held = append(held, e)
		}
	}
	mentioned := func(ts []check.Type) string {
		var hit []string
		for _, t := range ts {
			for _, e := range check.Mentions(t, outside) {
				if name := e.Pkg.Name + "." + e.Name; slices.Contains(held, e) && !slices.Contains(hit, name) {
					hit = append(hit, name)
				}
			}
		}
		if len(hit) == 0 {
			return ""
		}
		return strings.Join(hit, ", ") + " (" + pkg.Name + "." + tn.Name + " or a record it needs)"
	}
	for _, p := range surface {
		for _, f := range p.Funcs {
			if !isExportedName(f.Name) || f.Sig == nil || writesAmbient(f) || takesHeld(f, held) {
				continue
			}
			if m := mentioned([]check.Type{f.Sig}); m != "" {
				out = append(out, p.Name+"."+f.Name+" mentions "+m+" and neither lists Ambient in its written row nor takes a parameter of one")
			}
		}
		for _, o := range p.Exported() {
			if v, ok := o.(*check.Var); ok {
				if m := mentioned([]check.Type{v.Type}); m != "" {
					out = append(out, p.Name+"."+v.Name+" is an exported let that mentions "+m+", and a let has no row")
				}
			}
		}
		for _, c := range exportedContexts(p) {
			if m := mentioned(contextCaps(c)); m != "" {
				out = append(out, p.Name+"."+c.Name+" is an exported context that mentions "+m+", and a named context has no row")
			}
		}
	}
	// An extern has no Ergo body for the mint rule to ask (fu-ambient-ctor
	// r7, batch-10 r6 Unproven E1): one whose signature mentions a held
	// type lists Ambient in its written row, or takes a held parameter. A
	// generic extern's result is asked at each call (check.MintExtern).
	for _, p := range surface {
		for _, f := range p.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil || writesAmbient(f) || takesHeld(f, held) {
				continue
			}
			if m := mentioned([]check.Type{f.Sig}); m != "" {
				out = append(out, p.Name+"."+f.Name+" is an extern that mentions "+m+" and neither lists Ambient in its written row nor takes a parameter of one")
			}
		}
	}
	return append(out, mintViolations(surface, held)...)
}

// ambientSources are the ways a package of pkgs provides the prelude's
// Ambient other than the program entry (fu-ambient-ctor r7, batch-10 r6
// B1): a type with a function for each operation of Ambient, whose first
// parameter is the type or a borrow of it (it implements Ambient where
// Ambient's package allows it: the prelude), and a context, named or
// in a with, that binds Ambient (the checker refuses that too, S106). The
// mint rule's "a call is no build" holds only while Ambient has one
// source, so each is a violation.
func ambientSources(pkgs []*check.Package, ambient *check.TypeName) []string {
	var out []string
	it, _ := ambient.Type.(*check.Named).Underlying().(*check.Interface)
	isAmbient := func(t check.Type) bool {
		n, ok := t.(*check.Named)
		return ok && n.Obj == ambient
	}
	for _, p := range pkgs {
		for _, tn := range p.Types {
			if _, ok := tn.Type.(*check.Named).Underlying().(*check.Interface); ok || it == nil || len(it.Methods) == 0 {
				continue
			}
			all := true
			for _, m := range it.Methods {
				found := false
				for _, f := range p.Funcs {
					if f.Name != m.Name || f.Sig == nil || len(f.Sig.Params) == 0 {
						continue
					}
					t := f.Sig.Params[0].Type
					if b, ok := t.(*check.Pointer); ok {
						t = b.Elem
					}
					if n, ok := t.(*check.Named); ok && n.Obj == tn {
						found = true
					}
				}
				all = all && found
			}
			if all {
				out = append(out, p.Name+"."+tn.Name+" implements Ambient, whose one provider is the program entry")
			}
		}
		binds := func(c *check.Context) bool {
			return slices.ContainsFunc(contextCaps(c), isAmbient)
		}
		for _, c := range exportedContexts(p) {
			if binds(c) {
				out = append(out, p.Name+"."+c.Name+" is a named context that binds Ambient")
			}
		}
		for name, c := range p.Contexts {
			if !isExportedName(name) && binds(c) {
				out = append(out, p.Name+"."+name+" is a named context that binds Ambient")
			}
		}
		for _, w := range p.Info.Withs {
			for _, c := range w.Contexts {
				if c != nil && c.Lit != nil && binds(c) {
					out = append(out, p.Name+": a context literal binds Ambient")
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// mintViolations are the mint rule's violations (fu-ambient-ctor r6;
// stdlib.md, Host packages 6): every place of a package of surface that
// builds a value of a held type (check.Mints: a literal, a record update,
// an omitted field's or a let's zero value, a bare record, a conversion,
// a constant, a generated parameter) is in a function, or a closure in
// one, whose written row lists Ambient or that takes a parameter of a held
// type. A let, a named context, a type declaration, a test and a law have
// no row, so any build in one is reported. One violation per declaration,
// naming what it builds, in walk order.
func mintViolations(surface []*check.Package, held []*check.TypeName) []string {
	var out []string
	for _, p := range surface {
		type item struct {
			place check.MintPlace
			name  string
		}
		var order []item
		built := map[item][]string{}
		for _, m := range check.Mints(p) {
			n, ok := m.Type.(*check.Named)
			if !ok || !slices.Contains(held, n.Obj) || mintAllowed(m, held) {
				continue
			}
			it := item{m.Place, m.Name}
			if _, ok := built[it]; !ok {
				order = append(order, it)
			}
			what := n.Obj.Pkg.Name + "." + n.Obj.Name + " (" + mintFormText[m.Form] + ")"
			if !slices.Contains(built[it], what) {
				built[it] = append(built[it], what)
			}
		}
		for _, it := range order {
			what := strings.Join(built[it], ", ")
			if it.place == check.MintInFunc {
				out = append(out, p.Name+"."+it.name+" builds "+what+" and neither lists Ambient in its written row nor takes a parameter of a held type")
			} else {
				out = append(out, p.Name+"."+it.name+" builds "+what+" in "+mintPlaceText[it.place]+", which has no row")
			}
		}
	}
	return out
}

// mintAllowed reports whether a mint is in a function, or a closure in
// one, that holds the authority: its written row lists Ambient, or a
// parameter's type is a held type.
func mintAllowed(m check.Mint, held []*check.TypeName) bool {
	// Only a mint in a function has frames: a let, a context, a type, a
	// test and a law have none, so they hold no authority.
	if len(m.Frames) == 0 {
		return false
	}
	for _, f := range m.Frames {
		if f.Sig == nil {
			continue
		}
		if f.Written && rowHasAmbient(f.Sig.Uses) {
			return true
		}
		for _, p := range f.Sig.Params {
			if n, ok := p.Type.(*check.Named); ok && slices.Contains(held, n.Obj) {
				return true
			}
		}
	}
	return false
}

// mintFormText and mintPlaceText word a mint rule violation.
var mintFormText = map[check.MintForm]string{
	check.MintLiteral:    "a literal",
	check.MintUpdate:     "a record update",
	check.MintZeroField:  "an omitted field's zero value",
	check.MintZeroLet:    "a let's zero value",
	check.MintZeroRecord: "a bare record",
	check.MintConversion: "a conversion",
	check.MintConstant:   "a constant",
	check.MintFunc:       "a function literal",
	check.MintTuple:      "a tuple",
	check.MintString:     "an interpolated string",
	check.MintGenerated:  "a generated parameter",
	check.MintExtern:     "a generic extern's result",
}

var mintPlaceText = map[check.MintPlace]string{
	check.MintInLet:     "a let",
	check.MintInContext: "a named context",
	check.MintInType:    "a type declaration",
	check.MintInTest:    "a test",
	check.MintInLaw:     "a law",
}

// takesHeld reports whether a parameter of f has a type of held (the host
// provider or a record it needs, an instance of one included): the
// caller holds that authority already. The one exemption of the rule; a
// borrow, a wrapper or any other type that another package cannot build
// is no exemption.
func takesHeld(f *check.Func, held []*check.TypeName) bool {
	for _, p := range f.Sig.Params {
		if n, ok := p.Type.(*check.Named); ok && slices.Contains(held, n.Obj) {
			return true
		}
	}
	return false
}

// exportedContexts are pkg's exported named contexts, by name. A with of
// one in another package binds each capability of its entries and of the
// contexts it embeds, so the with body gets what that capability's
// operations give (check.Mentions of the capability type). A named context
// is closed (rows.md, Context 4): a provider that needs Ambient is S102,
// so no with of it needs authority (fu-ambient-ctor r3).
func exportedContexts(pkg *check.Package) []*check.Context {
	var out []*check.Context
	for name, c := range pkg.Contexts {
		if isExportedName(name) && c.Decl != nil {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *check.Context) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// contextCaps are the capability types a with of c binds: its entries'
// and, through each embedding, the embedded contexts'.
func contextCaps(c *check.Context) []check.Type {
	var out []check.Type
	seen := map[*check.Context]bool{}
	var walk func(*check.Context)
	walk = func(c *check.Context) {
		if c == nil || seen[c] {
			return
		}
		seen[c] = true
		for _, e := range c.Entries {
			out = append(out, e.Caps...)
			walk(e.Embedded)
		}
	}
	walk(c)
	return out
}

// writesAmbient reports whether f's written row names the prelude's
// Ambient.
func writesAmbient(f *check.Func) bool {
	if f.Decl == nil || f.Decl.Type == nil || f.Decl.Type.Needs == nil {
		return false
	}
	return rowHasAmbient(f.Sig.Uses)
}

// rowHasAmbient reports whether a needs row names the prelude's Ambient.
func rowHasAmbient(r *check.Row) bool {
	for _, e := range r.Elems() {
		if n, ok := e.(*check.Named); ok && n.Obj.Name == "Ambient" && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" {
			return true
		}
	}
	return false
}
