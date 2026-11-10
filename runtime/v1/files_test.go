package v1

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// TestBodiesFollowTheHiddenConvention reads the Go bodies of the std
// packages from the embedded source, as the lowering reads them, and pins
// each one's hidden parameters and the parameters and results that stand
// for its Ergo signature (docs/spec/stdlib.md, Hidden arguments). The
// internal/sketch/std test ties each to its Ergo declaration.
func TestBodiesFollowTheHiddenConvention(t *testing.T) {
	// Every generic body takes one T, constrained by any: the checker admits
	// any type for T, a non-comparable one included.
	anyT := []TypeParam{{Name: "T", Constraint: "any"}}
	task := &Body{Hidden: []Hidden{CallerTask}, Results: []string{"T", "error"}}
	want := map[string]Body{
		// task.Go: the caller's frame owns the task, the site names it, and
		// the block receives the child's task.
		"Go": {TypeParams: anyT, Hidden: []Hidden{Frame, CallSite}, Params: []Param{{Type: "func(*rt.Task) (T, error)", Func: task}}, Results: []string{"*Task[T]"}},
		// scope.OnExit: the caller's frame takes the block.
		"OnExit": {Hidden: []Hidden{Frame}, Params: []Param{{Type: "func()", Func: &Body{}}}},
		// queue.New and slices.WithCapacity take no hidden argument.
		"New":          {TypeParams: anyT, Params: []Param{{Type: "int"}}, Results: []string{"*Queue[T]"}},
		"WithCapacity": {TypeParams: anyT, Params: []Param{{Type: "int"}}, Results: []string{"[]T"}},
		// The operations of queue.Queue: the caller's task where they wait.
		"Queue.Push":   {TypeParams: anyT, Hidden: []Hidden{CallerTask}, Params: []Param{{Type: "T"}}, Results: []string{"error"}},
		"Queue.Pop":    {TypeParams: anyT, Hidden: []Hidden{CallerTask}, Results: []string{"T", "error"}},
		"Queue.Values": {TypeParams: anyT, Hidden: []Hidden{CallerTask}, Results: []string{"iter.Seq[T]"}},
		"Queue.End":    {TypeParams: anyT},
		// The prelude's Task operations; Close, the release, masks on the
		// caller's task (#197 L8 B3t).
		"Task.Wait":    {TypeParams: anyT, Results: []string{"T", "error"}},
		"Task.Stop":    {TypeParams: anyT},
		"Task.Close":   {TypeParams: anyT, Hidden: []Hidden{CallerTask}},
		"ContextBound": {Hidden: []Hidden{CallerTask, ReadingPackage}, Params: []Param{{Type: "string"}}, Results: []string{"bool"}},
		"ContextValue": {Hidden: []Hidden{CallerTask, ReadingPackage}, Params: []Param{{Type: "string"}}, Results: []string{"string"}},
		// timeout.After: the site names the body's task, the caller's task
		// is its parent, and the caller's Scheduler gives the deadline; the
		// block receives the child's task.
		"After": {TypeParams: []TypeParam{{Name: "A", Constraint: "any"}}, Hidden: []Hidden{CallSite, CallerTask, CallerScheduler}, Params: []Param{{Type: "time.Duration"}, {Type: "func(*rt.Task) (A, error)", Func: &Body{Hidden: []Hidden{CallerTask}, Results: []string{"A", "error"}}}}, Results: []string{"A", "error"}},
	}
	for name, w := range want {
		got, err := BodyOf(name)
		if err != nil {
			t.Errorf("BodyOf(%q): %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("BodyOf(%q) = %+v, want %+v", name, got, w)
		}
	}
	if _, err := BodyOf("Missing"); err == nil {
		t.Error("BodyOf(Missing) gave no error")
	}
}

// TestHiddenConventionRefusals feeds bodies that break the convention, and
// a few that keep it, through the same reader.
func TestHiddenConventionRefusals(t *testing.T) {
	src := `package v1

import rtx "ergo.local/prototype/runtime/rt"
import other "example.com/other/rt"

type Scope struct{}
type Site string
type Task[T any] struct{}

func Ordered(s *Scope, at Site, t *rtx.Task, pkg ContextPackage, n int) {}
func Scheduled(at Site, t *rtx.Task, s Sleeper, pkg ContextPackage, n int) {}
func SleeperAfterParam(n int, s Sleeper) {}
func SleeperOutOfOrder(s Sleeper, t *rtx.Task) {}
func SleeperTwice(a, b Sleeper) {}
func AfterParam(n int, t *rtx.Task) {}
func OutOfOrder(t *rtx.Task, s *Scope) {}
func Twice(s *Scope, p *Scope) {}
func PackageAfterParam(n int, pkg ContextPackage) {}
func PackageOutOfOrder(pkg ContextPackage, t *rtx.Task) {}
func PackageTwice(a, b ContextPackage) {}
func Grouped(s, p *Scope) {}
func OwnTask(t *Task[int]) {}
func OtherRt(t *other.Task) {}
func BadBlock(f func(n int, t *rtx.Task)) {}
func unexported(s *Scope) {}
`
	decls, err := parseDecls(fstest.MapFS{"a.go": {Data: []byte(src)}, "a_test.go": {Data: []byte("package v1\nfunc Skipped() {}\n")}})
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string][]Hidden{
		"Ordered":   {Frame, CallSite, CallerTask, ReadingPackage},
		"Scheduled": {CallSite, CallerTask, CallerScheduler, ReadingPackage},
		"OwnTask":   nil, // v1's Task is an Ergo value, not the hidden task
		"OtherRt":   nil, // only runtime/rt's Task is the hidden task
	}
	for name, hidden := range ok {
		b, err := decls.body(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(b.Hidden, hidden) {
			t.Errorf("%s: hidden %v, want %v", name, b.Hidden, hidden)
		}
	}
	bad := map[string]string{
		"AfterParam":        "follows an Ergo parameter",
		"OutOfOrder":        "out of order",
		"Twice":             "repeated",
		"Grouped":           "repeated",
		"PackageAfterParam": "follows an Ergo parameter",
		"PackageOutOfOrder": "out of order",
		"PackageTwice":      "repeated",
		"SleeperAfterParam": "follows an Ergo parameter",
		"SleeperOutOfOrder": "out of order",
		"SleeperTwice":      "repeated",
		"BadBlock":          "function parameter 1",
		"unexported":        "no exported Go body",
		"Skipped":           "no exported Go body",
	}
	for name, msg := range bad {
		_, err := decls.body(name)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
}

// TestStructFields reads the struct types a record of a v1 package binds
// (docs/spec/stdlib.md, Types): the real Closed, and synthetic types that
// are not a plain struct.
func TestStructFields(t *testing.T) {
	if got, err := StructFields("Closed"); err != nil || len(got) != 0 {
		t.Errorf("StructFields(Closed) = %v, %v; want no fields", got, err)
	}
	src := `package v1

type Rec struct {
	A    int
	B, C string
}
type Gen[T any] struct{ V T }
type Alias = struct{}
type Emb struct{ Rec }
type Named int
type hidden struct{}

func Rec2() {}
`
	decls, err := parseDecls(fstest.MapFS{"a.go": {Data: []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decls.structFields("Rec"); err != nil || !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Errorf("Rec: %v, %v; want [A B C]", got, err)
	}
	for name, msg := range map[string]string{
		"Gen":    "not a non-generic struct",
		"Alias":  "not a non-generic struct",
		"Named":  "not a non-generic struct",
		"Emb":    "embeds a field",
		"hidden": "no exported Go type",
		"Rec2":   "no exported Go type",
	} {
		if _, err := decls.structFields(name); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
	if _, err := decls.body("Rec"); err == nil || !strings.Contains(err.Error(), "no exported Go body") {
		t.Errorf("a type is not a body: %v", err)
	}
}

// TestBodyTypeParams reads the type parameters of a function and of a
// method's receiver, renamed as the receiver names them.
func TestBodyTypeParams(t *testing.T) {
	src := `package v1

type Box[K comparable, V any] struct{}
type Plain struct{}

func F[T comparable, U any](t T, u U) {}
func (b *Box[A, B]) M() {}
func (p Plain) N() {}
func (b Box[K]) Short() {}
func (q *Missing[T]) Lost() {}
`
	decls, err := parseDecls(fstest.MapFS{"a.go": {Data: []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]TypeParam{
		"F":       {{"T", "comparable"}, {"U", "any"}},
		"Box.M":   {{"A", "comparable"}, {"B", "any"}},
		"Plain.N": nil,
	} {
		b, err := decls.body(name)
		if err != nil || !reflect.DeepEqual(b.TypeParams, want) {
			t.Errorf("%s: type params %v, %v; want %v", name, b.TypeParams, err, want)
		}
	}
	for name, msg := range map[string]string{
		"Box.Short":    "names 1 type parameters of Box's 2",
		"Missing.Lost": "receiver type Missing is not declared",
	} {
		if _, err := decls.body(name); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
}
