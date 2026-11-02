package check

import (
	"fmt"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The file of a diagnostic (stage E2 round 3). A syntax.Pos is an offset
// within one file, so a diagnostic takes its file from c.cur, the file the
// running pass selected. A pass that reports without first selecting the
// file of what it reports names whichever file an earlier pass left
// current, at an offset of the wrong source (E1 round 2 M2, E2 round 1 M1,
// E2 round 2 M1).
//
// The invariant, asserted at every diagnostic reported: the node it is
// reported at lies in c.cur's file. Every report carries that node (errorf
// reports the node's span, errorIn a span inside it), so no report escapes
// the assertion. A violation is an internal compiler error: a panic that
// names the reporting pass and both files, so every test and every fuzz
// run that reaches the report fails.
//
// The driver (run, decl.go) clears c.cur before each pass. A pass that
// reports without selecting a file then reports with no file current, and
// the assertion fails in a one-file package too: every test of the checker
// and of the lowerer exercises the selection of each pass it reaches, not
// only the multi-file guard (filesplit_e2_test.go).
//
// The test is exact: it compares nodes, not positions, since the offsets of
// two files overlap. The index from node to file is built once per check,
// at the first diagnostic reported, so a clean program pays nothing and a
// program with diagnostics pays one walk of its trees.

// fileOf returns the file whose tree holds n, or nil. A node the checker
// made from a file's nodes (a variant's record declaration, shapes.go)
// lies in the file of the tree nodes it holds.
func (c *checker) fileOf(n syntax.Node) *fileInfo {
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
		return fi
	}
	var found *fileInfo
	syntax.Inspect(n, func(m syntax.Node) bool {
		if found == nil {
			found = c.owners[m]
		}
		return found == nil
	})
	return found
}

// assertOwner panics with an internal compiler error unless n lies in the
// current file.
func (c *checker) assertOwner(n syntax.Node, code, format string, args []any) {
	owner := c.fileOf(n)
	if owner != nil && owner == c.cur {
		return
	}
	panic(fmt.Sprintf("check: internal compiler error: %s reported %s %q at a %T of %s while %s was current (a pass that reports must select the file of what it reports)",
		reporter(), code, fmt.Sprintf(format, args...), n, fileName(owner, "no file"), fileName(c.cur, "no file")))
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
