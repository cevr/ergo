package check

import (
	"fmt"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The file of a diagnostic (stage E2 round 3, source provenance E2b). A
// syntax.Pos is an offset within one file, so a diagnostic takes its file
// from c.cur, the file the running pass selected. A pass that reports
// without first selecting the file of what it reports names whichever file
// an earlier pass left current, at an offset of the wrong source (E1 round 2
// M2, E2 round 1 M1, E2 round 2 M1).
//
// The invariant, asserted at every diagnostic reported: the node it is
// reported at lies in c.cur's file. Every report carries that node (errorf
// reports the node's span, errorIn a span inside it), so no report escapes
// the assertion. A violation is an internal compiler error in every build:
// a panic that names the reporting pass and both files, so every test and
// every fuzz run that reaches the report fails, and a release compilation
// stops rather than point at an unrelated source (Go gc's FatalfAt, rustc's
// ICE).
//
// The driver (run, decl.go) clears c.cur before each pass. A pass that
// reports without selecting a file then reports with no file current, and
// the assertion fails in a one-file package too: every test of the checker
// and of the lowerer exercises the selection of each pass it reaches, not
// only the multi-file guard (filesplit_e2_test.go).
//
// The test is exact: it compares nodes, not positions, since the offsets of
// two files overlap. A node lies in a file when the file's tree holds it,
// or when the checker made it from a node that lies in the file. Every node
// the checker makes (a variant's record declaration and its type
// parameters, shapes.go; the copied let of a pattern and the operation of a
// compound assignment, stmt.go) records the node it was made from, its
// origin, through made or clone; owner_e2_test.go inventories every
// construction of a syntax node in the checker and fails on one that does
// not. An unrecorded node, a cycle of origins, a node recorded twice with
// two origins, and a made node that holds a node of another file are
// internal compiler errors. Ownership is never inferred from positions,
// from a node's children, or from the current file.
//
// The index from a tree node to its file is built once per check, at the
// first diagnostic reported, so a clean program pays one map entry per
// node it made and a program with diagnostics pays one walk of its trees.
// When a syntax.Pos carries its file (the parser's lane), the position
// names the file, and the index, the origins and this lookup are deleted;
// errorf and errorIn keep their contract.

// made records that the checker made n from origin, a node of a file's tree
// or a node the checker made earlier, and returns n. A diagnostic at n is
// reported at n's own span, in origin's file.
func made[N syntax.Node](c *checker, n N, origin syntax.Node) N {
	switch {
	case syntax.IsNil(n):
		panic("check: internal compiler error: a made syntax node is nil")
	case syntax.IsNil(origin):
		panic(fmt.Sprintf("check: internal compiler error: the %T the checker made has no origin", n))
	case syntax.Node(n) == origin:
		panic(fmt.Sprintf("check: internal compiler error: the %T the checker made is its own origin", n))
	}
	if prev, ok := c.origins[n]; ok && prev != origin {
		panic(fmt.Sprintf("check: internal compiler error: the %T the checker made has two origins, a %T and a %T", n, prev, origin))
	}
	c.origins[n] = origin
	return n
}

// madeNodes returns each node the checker made, with its origin: the
// registry the provenance guards sweep (owner_e2_test.go).
func (c *checker) madeNodes() map[syntax.Node]syntax.Node {
	return c.origins
}

// clone returns a copy of the node n, made from n.
func clone[T any, P interface {
	*T
	syntax.Node
}](c *checker, n P) P {
	m := P(new(T))
	*m = *n
	return made(c, m, syntax.Node(n))
}

// fileOf returns the file n lies in, or nil when n lies in none.
func (c *checker) fileOf(n syntax.Node) *fileInfo {
	fi, _ := c.ownerOf(n)
	return fi
}

// ownerOf returns the file n lies in, or nil and why n lies in none.
func (c *checker) ownerOf(n syntax.Node) (*fileInfo, string) {
	if syntax.IsNil(n) {
		return nil, "a nil node"
	}
	if c.owners == nil {
		c.owners = map[syntax.Node]*fileInfo{}
		for _, fi := range c.files {
			syntax.Inspect(fi.syntax, func(m syntax.Node) bool {
				c.owners[m] = fi
				return true
			})
		}
	}
	if fi, ok := c.owners[n]; ok {
		if _, isMade := c.origins[n]; isMade {
			return nil, fmt.Sprintf("a %T of %s that is recorded as made by the checker", n, fileName(fi, "no file"))
		}
		return fi, ""
	}
	fi, why := c.originFile(n)
	if fi == nil {
		return nil, why
	}
	// A made node holds tree nodes and made nodes; each must lie in its
	// origin's file, or the node spans two sources.
	syntax.Inspect(n, func(m syntax.Node) bool {
		if why != "" || m == n {
			return why == ""
		}
		if mf, ok := c.owners[m]; ok {
			if mf != fi {
				why = fmt.Sprintf("a %T made from %s that holds a %T of %s", n, fileName(fi, "no file"), m, fileName(mf, "no file"))
			}
			return false
		}
		mf, mwhy := c.originFile(m)
		switch {
		case mf == nil:
			why = fmt.Sprintf("a %T that holds %s", n, mwhy)
		case mf != fi:
			why = fmt.Sprintf("a %T made from %s that holds a %T made from %s", n, fileName(fi, "no file"), m, fileName(mf, "no file"))
		}
		return why == ""
	})
	if why != "" {
		return nil, why
	}
	return fi, ""
}

// originFile follows the origins of the node n, which no tree holds, to a
// tree node and returns its file.
func (c *checker) originFile(n syntax.Node) (*fileInfo, string) {
	seen := map[syntax.Node]bool{}
	for m := n; ; {
		origin, ok := c.origins[m]
		if !ok {
			if m == n {
				return nil, fmt.Sprintf("a %T in no file's tree, with no recorded origin", n)
			}
			return nil, fmt.Sprintf("a %T made from a %T in no file's tree, with no recorded origin", n, m)
		}
		if seen[m] {
			return nil, fmt.Sprintf("a %T whose origins form a cycle", n)
		}
		seen[m] = true
		if fi, ok := c.owners[origin]; ok {
			if _, isMade := c.origins[origin]; isMade {
				return nil, fmt.Sprintf("a %T made from a %T of %s that is recorded as made by the checker", n, origin, fileName(fi, "no file"))
			}
			return fi, ""
		}
		m = origin
	}
}

// assertOwner panics with an internal compiler error unless n lies in the
// current file.
func (c *checker) assertOwner(n syntax.Node, code, format string, args []any) {
	owner, why := c.ownerOf(n)
	if owner != nil && owner == c.cur {
		return
	}
	where := fileName(owner, "no file")
	if why != "" {
		where += " (" + why + ")"
	}
	panic(fmt.Sprintf("check: internal compiler error: %s reported %s %q at a %T of %s while %s was current (a pass that reports must select the file of what it reports)",
		reporter(), code, fmt.Sprintf(format, args...), n, where, fileName(c.cur, "no file")))
}

func fileName(fi *fileInfo, none string) string {
	if fi == nil {
		return none
	}
	return fmt.Sprintf("file %d", fi.index)
}

// reporter names the pass that reported: the checker functions on the
// stack above the report, innermost first.
func reporter() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(4, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var names []string
	for {
		f, more := frames.Next()
		name := f.Function
		if i := strings.LastIndex(name, "/check."); i >= 0 {
			name = name[i+len("/check."):]
			if !strings.HasPrefix(name, "(*checker).errorf") && !strings.HasPrefix(name, "(*checker).errorIn") {
				names = append(names, name)
			}
		}
		if !more || len(names) == 4 {
			break
		}
	}
	return strings.Join(names, " < ")
}
