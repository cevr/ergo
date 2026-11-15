package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// recvBorrowGo is a Go package whose methods take a pointer of their own
// receiver's type. Merge has a pointer receiver and its pointer is its
// second argument; Same has a value receiver and its pointer is its first.
// No standard-library binding has either shape, so a synthetic one stands
// for them.
const recvBorrowGo = `package recvborrow

type P struct{ N int }

func (p *P) Merge(n int, q *P) {}

func (p P) Same(q *P) bool { return false }
`

// The fixture's bodies keep nothing: its arguments and receivers are lent,
// so the overlap rule is the only rule the calls meet. Kept in this test
// binary only, as hostres's are (host_resource_test.go).
func init() {
	const reason = "recvBorrowGo: an empty body keeps nothing"
	v1.GoArgClauses["recvborrow.P.Merge"] = map[int]v1.GoArgClause{0: {Lent: true, Reason: reason}, 2: {Lent: true, Reason: reason}}
	v1.GoArgClauses["recvborrow.P.Same"] = map[int]v1.GoArgClause{0: {Lent: true, Reason: reason}, 1: {Lent: true, Reason: reason}}
}

// recvBorrowSrc calls each method on a borrow p with p. Merge receives p
// itself (a pointer receiver): the call takes p twice (types.md, Borrows
// 2). Same receives a copy of *p (a value receiver): it takes p once.
const recvBorrowSrc = `import go recvborrow

func merge(p *recvborrow.P) needs go.Foreign {
	p.Merge(1, p)
}

func same(p *recvborrow.P) bool needs go.Foreign {
	p.Same(p)
}
`

// TestGoMethodReceiverBorrows is the smallest repro of the ESC-idx r2 bug:
// the overlap rule (check/borrows.go) read a binding method's first
// written argument as its receiver, since a 196m function's receiver is
// its first parameter. It admitted Merge's two borrows of p and refused
// Same. The binding is the file ergo bind go writes (gobind), as in
// newHresImporterOf. Mutants: escidx-borrows-first-param and
// escidx-recv-borrow-binding (check ledger).
func TestGoMethodReceiverBorrows(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "recvborrow.go", recvBorrowGo, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{IgnoreFuncBodies: true}).Check("recvborrow", fs, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := gobind.File([]*types.Package{pkg}, []gobind.Platform{{GOOS: "linux", GOARCH: "amd64"}}, "go1.27")
	if err != nil {
		t.Fatal(err)
	}
	imp := hresImporter{check.NewGoBindings(func(path string) (string, bool) {
		if path == "recvborrow" {
			return binding, true
		}
		return gostd.Binding(path)
	})}
	file, err := syntax.Parse(recvBorrowSrc)
	if err != nil {
		t.Fatal(err)
	}
	_, diags := check.Check("main", []*syntax.File{file}, imp)
	if len(diags) != 1 || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "p and p are borrows of one place") {
		t.Errorf("want one S108 at line 4, p.Merge(1, p) passing p to a pointer receiver and as an argument, and none for p.Same(p), whose value receiver copies *p; got %v\n%s", diags, binding)
	}
}
