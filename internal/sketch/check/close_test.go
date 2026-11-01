package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func TestCloseRequiresEmptyRow(t *testing.T) {
	for _, src := range []string{
		"type R {}\ntype Fault {}\nfunc Close(_ R) fails Fault { return Fault }\n",
		"type R { Close() fails Fault }\ntype Fault {}\n",
	} {
		_, ds := check.CheckSource(src)
		found := false
		for _, d := range ds {
			found = found || strings.Contains(d.Message, "Close must have an empty failure row")
		}
		if !found {
			t.Errorf("fallible release was admitted: %s\ndiagnostics: %v", src, ds)
		}
	}
}

func TestCloseIdentity(t *testing.T) {
	const src = `type R {}
type P {}
type G[T] { Value T }
type Releaser { Close() }
type Other { Finish() }
type Field { Close func() }
type WithArg { Close(n int) }
type WithResult { Close() int }
type Extra {}
type Returning {}
func Close(_ R) {}
func Close(_ *P) {}
func Close[T](_ G[T]) {}
func Close(_ Extra, n int) {}
func Close(_ Returning) int { 1 }
func Finish(_ R) {}
func direct(r R) { r.Close() }
func pointer(p *P) { p.Close() }
func generic(g G[int]) { g.Close() }
func operation(r Releaser) { r.Close() }
func capability() needs Releaser { Releaser.Close() }
func methodValue(r Releaser) { let f = r.Close; f() }
func otherName(r R) { r.Finish() }
func otherOperation(r Other) { r.Finish() }
func field(r Field) { r.Close() }
func withArg(r WithArg) { r.Close(1) }
func withResult(r WithResult) int { r.Close() }
func extra(r Extra) { r.Close(1) }
func returning(r Returning) int { r.Close() }
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	// The capability's Close keeps its identity fact, which the wire
	// ownership refusal reads (resources.md R24): the one diagnostic.
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "Releaser.Close releases the capability's provider") {
		t.Fatalf("diagnostics: %v", ds)
	}
	want := map[string]bool{
		"direct": true, "pointer": true, "generic": true, "operation": true,
		"capability": true, "methodValue": true, "otherName": false,
		"otherOperation": false, "field": false, "withArg": false, "withResult": false,
		"extra": false, "returning": false,
	}
	for _, fn := range pkg.Funcs {
		expect, ok := want[fn.Name]
		if !ok {
			continue
		}
		t.Run(fn.Name, func(t *testing.T) {
			count := 0
			for n, fact := range pkg.Info.Closes {
				if n.Pos() < fn.Decl.Body.Pos() || n.End() > fn.Decl.Body.End() {
					continue
				}
				count++
				if fact.Receiver == nil || (fact.Func == nil) == (fact.Op == nil) {
					t.Errorf("incomplete identity at %T: %+v", n, fact)
				}
				if call, ok := n.(*syntax.CallExpr); ok {
					resolved := pkg.Info.Calls[call]
					if fact.Func != resolved.Func || fact.Op != resolved.Op {
						t.Error("Close identity differs from resolved callee")
					}
				}
			}
			if (count > 0) != expect {
				t.Errorf("Close identities = %d, want present=%t", count, expect)
			}
		})
	}
}

func TestCloseFunctionIdentity(t *testing.T) {
	f, err := syntax.Parse("type R {}\nfunc Close(_ R) {}\nfunc f(r R) { Close(r); let release = Close; release(r) }\n")
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	decl := pkg.Funcs[0]
	if fact := pkg.Info.Closes[decl.Decl]; fact == nil || fact.Func != decl {
		t.Fatal("written Close declaration lost its identity")
	}
	refs := 0
	for id, object := range pkg.Info.Uses {
		if object == decl {
			refs++
			if fact := pkg.Info.Closes[id]; fact == nil || fact.Func != decl {
				t.Errorf("Close reference at %d lost its declaration identity", id.Pos())
			}
		}
	}
	if refs != 2 {
		t.Fatalf("Close references = %d, want 2", refs)
	}
}

func TestUnresolvedCloseHasNoIdentity(t *testing.T) {
	f, err := syntax.Parse("type R {}\nfunc f(r R) { r.Close() }\n")
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) == 0 {
		t.Fatal("unresolved Close was admitted")
	}
	if len(pkg.Info.Closes) != 0 {
		t.Fatal("unresolved spelling was recorded as a Close identity")
	}
}

func TestCloseSelectorUsesResolvedCallee(t *testing.T) {
	const src = "type R { Close() }\nfunc Close(_ R, n int) {}\nfunc f(r R) { r.Close() }\n"
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	for call, resolution := range pkg.Info.Calls {
		fact := pkg.Info.Closes[call]
		if fact == nil || fact.Op == nil || fact.Op != resolution.Op || fact.Func != nil {
			t.Fatal("Close identity differs from the resolved operation")
		}
	}
}

func TestCloseStdOperationIdentity(t *testing.T) {
	pkg, ds := check.CheckSource("import queue\nfunc f(q queue.Queue[int]) { q.Close() }\n")
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	for call, resolution := range pkg.Info.Calls {
		fact := pkg.Info.Closes[call]
		if fact == nil || fact.Op == nil || fact.Op != resolution.Op || fact.Func != nil {
			t.Fatal("std Close identity differs from the resolved operation")
		}
	}
}

func TestUnrelatedCloseFunctionHasNoIdentity(t *testing.T) {
	pkg, ds := check.CheckSource("func Close() {}\nfunc f() { Close() }\n")
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	if len(pkg.Info.Closes) != 0 {
		t.Fatal("a package function without a receiver became a release by name")
	}
}
