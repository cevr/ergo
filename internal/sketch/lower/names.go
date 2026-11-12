package lower

import (
	"fmt"
	"go/token"
)

// A namer allocates Go identifiers in one Go scope. Every Ergo entity gets
// its Go name from its identity (a check object, a declaration), never from
// its spelling alone: the spelling is only the preferred name, taken when no
// Go keyword, predeclared identifier, enclosing name or earlier allocation
// holds it. So a source name never captures a generated one, two
// same-named objects (shadowing, 196m functions) never share a Go name, and
// a generated name never captures a source one.
//
// Prior art: go/types-based generators (stringer, gopls renames) and
// ReScript's and Gleam's code generators rename by binding identity and
// keep the source spelling where it is free.
type namer struct {
	parent *namer
	used   map[string]bool
	next   map[string]int // per base, the first suffix not yet tried here
	work   *namerWork
}

// namerWork counts a lowering's naming work, shared by its scopes: names
// given and candidate names tested. Each scope resumes a base's suffix
// search where it stopped, so a scope that gives k names of one base tests
// O(k) candidates, not O(k*k) (TestNamingWorkIsLinear).
type namerWork struct {
	allocs, probes int
}

// goReserved holds what a Go identifier in a package or a function may not
// be, or may not shadow because lowered code uses it: Go's keywords, its
// predeclared identifiers, the entry points main and init, and the blank
// identifier.
var goReserved = func() map[string]bool {
	m := map[string]bool{"main": true, "init": true, "_": true}
	for tok := token.BREAK; tok <= token.VAR; tok++ {
		if tok.IsKeyword() {
			m[tok.String()] = true
		}
	}
	for _, name := range []string{
		"any", "bool", "byte", "comparable", "complex64", "complex128", "error", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32",
		"uint64", "uintptr", "true", "false", "iota", "nil", "append", "cap", "clear", "close", "complex",
		"copy", "delete", "imag", "len", "make", "max", "min", "new", "panic", "print", "println", "real", "recover",
	} {
		m[name] = true
	}
	return m
}()

func newNamer(parent *namer) *namer {
	n := &namer{parent: parent, used: map[string]bool{}, next: map[string]int{}}
	if parent != nil {
		n.work = parent.work
	} else {
		n.work = &namerWork{}
	}
	return n
}

func (n *namer) taken(name string) bool {
	if goReserved[name] {
		return true
	}
	for s := n; s != nil; s = s.parent {
		if s.used[name] {
			return true
		}
	}
	return false
}

// alloc returns base when it is free in this scope and every enclosing one,
// else base_N for the smallest free N, and marks it used here.
func (n *namer) alloc(base string) string {
	if base == "" {
		base = "x"
	}
	name := base
	n.work.allocs++
	n.work.probes++
	if n.taken(name) {
		// A name taken here stays taken: names are only added, so the
		// search resumes after the last suffix tried for this base.
		i := max(n.next[base], 1)
		for {
			name = fmt.Sprintf("%s_%d", base, i)
			i++
			n.work.probes++
			if !n.taken(name) {
				break
			}
		}
		n.next[base] = i
	}
	n.used[name] = true
	return name
}

// memberName is the Go name of a record field or an operation: its source
// name, unless that is a Go keyword, which no selector may be. A keyword
// gets a suffix; a collision of the result with another member of the same
// type is refused where the type is declared (members).
func memberName(name string) string {
	if token.IsKeyword(name) {
		return name + "_"
	}
	return name
}
