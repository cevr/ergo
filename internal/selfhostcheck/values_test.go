package selfhostcheck

import (
	"fmt"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The value types CHECK-VALUE-TYPES adds to compiler/check/expr: every
// package let's name, and every if, match or catch whose value is used,
// has the Go checker's type. Unlike the expression agreement, where the
// Ergo side may leave any expression untyped, these are compared whole,
// bounded only by the Ergo side's own untyped inputs: a value is required
// when the Ergo side types each of its inputs (each valued branch tail, a
// catch's caught expression, a let's value) and reads its context (the
// value sits in a statement, or under an expression the Ergo side types,
// or is the tail of a required value). A value whose input the typer
// leaves untyped (a capability call, a prelude slice method, a derived
// function) is excluded and logged by cause, never pinned; those causes
// are units of their own (CHECK-CALL-COVERAGE, PRELUDE-ONE-SOURCE), and
// when they land the exclusion goes and every value is required. A value
// of a record literal of a package outside the run (a module the corpus
// does not hold, which the Ergo side reads only as an interface) has a
// field type the Ergo side cannot read: it lists those values (its X
// lines) and they are excluded as "untyped field outside" until the
// loader types such packages (LOAD-MODULE-DEPS).

// goValue is one value the Go checker types: its line ("FILE START END
// TYPE"), the keys of its inputs, and its context: the key of the
// expression it sits under ("" in a statement), or of the value whose
// tail it is (tailOf).
type goValue struct {
	line   string
	inputs []string
	parent string
	tailOf string
}

// goValues is pkg's package lets' names and value ifs, matches and
// catches, as the Go checker types them.
func goValues(pkg *check.Package, unnamed string) []goValue {
	var out []goValue
	for sf, name := range pkgFiles(pkg, unnamed) {
		key := func(x syntax.Node) string { return fmt.Sprintf("%s %d %d", name, x.Pos(), end(x)) }
		// Every typed expression's span, for the one a value sits under.
		type span struct {
			pos, end int
			key      string
		}
		var spans []span
		stmtPos := map[syntax.Expr]bool{}
		tailOf := map[syntax.Expr]string{}
		var values []syntax.Expr
		syntax.Inspect(sf, func(n syntax.Node) bool {
			switch s := n.(type) {
			case *syntax.VarSpec:
				for _, v := range s.Values {
					stmtPos[unparenX(v)] = true
				}
			case *syntax.ReturnStmt:
				for _, v := range s.Results {
					stmtPos[unparenX(v)] = true
				}
			case *syntax.AssignStmt:
				for _, v := range s.Rhs {
					stmtPos[unparenX(v)] = true
				}
			case *syntax.ExprStmt:
				stmtPos[unparenX(s.X)] = true
			}
			x, ok := n.(syntax.Expr)
			if !ok {
				return true
			}
			if _, typed := pkg.Info.Types[x]; !typed {
				return true
			}
			spans = append(spans, span{int(x.Pos()), int(end(x)), key(x)})
			switch x.(type) {
			case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.CatchExpr:
				values = append(values, x)
			}
			return true
		})
		for _, x := range values {
			for _, t := range valueTails(x) {
				tailOf[unparenX(t)] = key(x)
			}
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].end-spans[i].pos < spans[j].end-spans[j].pos })
		for _, x := range values {
			v := goValue{line: key(x) + " " + pkg.Info.Types[x].String(), tailOf: tailOf[x]}
			if c, ok := x.(*syntax.CatchExpr); ok {
				v.inputs = []string{key(c.X)}
			} else {
				for _, t := range valueTails(x) {
					v.inputs = append(v.inputs, key(t))
				}
			}
			if v.tailOf == "" && !stmtPos[x] {
				p, e := int(x.Pos()), int(end(x))
				for _, s := range spans {
					if s.pos <= p && e <= s.end && s.end-s.pos > e-p {
						v.parent = s.key
						break
					}
				}
			}
			out = append(out, v)
		}
		for _, d := range sf.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, sp := range vd.Specs {
				var inputs []string
				if sp.Type == nil {
					for _, x := range sp.Values {
						inputs = append(inputs, key(x))
					}
				}
				for _, id := range sp.Names {
					v, ok := pkg.Info.Defs[id].(*check.Var)
					if !ok || v.Type == nil || id.Name == "_" {
						continue
					}
					out = append(out, goValue{line: fmt.Sprintf("%s %s", key(id), v.Type), inputs: inputs})
				}
			}
		}
	}
	return out
}

// end is n's end offset.
func end(n syntax.Node) syntax.Pos { return n.(interface{ End() syntax.Pos }).End() }

// valueTails is the expressions an if or a match yields: each branch's
// last expression statement, through nested ifs and matches; a branch
// that leaves (return, break, continue, Unreachable(…)) yields none.
func valueTails(x syntax.Expr) []syntax.Expr {
	var out []syntax.Expr
	var list func([]syntax.Stmt)
	var stmt func(syntax.Stmt)
	list = func(l []syntax.Stmt) {
		if len(l) > 0 {
			stmt(l[len(l)-1])
		}
	}
	stmt = func(s syntax.Stmt) {
		switch s := s.(type) {
		case *syntax.ExprStmt:
			if c, ok := unparenX(s.X).(*syntax.CallExpr); ok {
				if id, ok := unparenX(c.Fun).(*syntax.Ident); ok && id.Name == "Unreachable" {
					return
				}
			}
			out = append(out, s.X)
		case *syntax.IfStmt:
			list(s.Body.List)
			if s.Else != nil {
				stmt(s.Else)
			}
		case *syntax.BlockStmt:
			list(s.List)
		case *syntax.SwitchStmt:
			for _, c := range s.Clauses {
				list(c.Body)
			}
		case *syntax.TypeSwitchStmt:
			for _, c := range s.Clauses {
				list(c.Body)
			}
		}
	}
	switch x := x.(type) {
	case *syntax.IfStmt:
		stmt(x)
	case *syntax.SwitchStmt:
		stmt(x)
	case *syntax.TypeSwitchStmt:
		stmt(x)
	}
	return out
}

// valueDiff is the required values of want the Ergo side's got (its E
// and L lines) misses or types otherwise, and the L lines of got the Go
// checker has no let for; and the values excluded, by cause: the Go
// checker's form (forms, by key) of the first input, or the parent, the
// Ergo side leaves untyped, or a field of a record outside the run whose
// type it cannot read (outside, its X lines), so each excluded value is
// counted under one cause.
func valueDiff(want []goValue, got, gotLets []string, outside map[string]bool, forms map[string]string) (string, map[string]int) {
	formOf := func(key string) string {
		if f := forms[key]; f != "" {
			return f
		}
		return "a form the Go checker lists no type for"
	}
	byKey := map[string]string{}
	for _, l := range got {
		byKey[keyOf(l, 3)] = l
	}
	for _, l := range gotLets {
		byKey[keyOf(l, 3)] = l
	}
	wantKeys := map[string]bool{}
	required := map[string]bool{}
	excluded := map[string]int{}
	var b strings.Builder
	// A value is required when its inputs are typed and its context is
	// read; a tail's context is its value's, which comes first in want
	// (goValues lists a file's values outermost first).
	for _, v := range want {
		k := keyOf(v.line, 3)
		wantKeys[k] = true
		cause := ""
		for _, in := range v.inputs {
			if _, ok := byKey[in]; !ok && cause == "" {
				cause = "untyped input: " + formOf(in)
			}
		}
		switch {
		case cause != "":
		case v.tailOf != "" && !required[v.tailOf]:
			cause = "the tail of an excluded value"
		case outside[k]:
			cause = "untyped field outside"
		case v.parent != "" && byKey[v.parent] == "":
			cause = "untyped parent: " + formOf(v.parent)
		}
		required[k] = cause == ""
		g, ok := byKey[k]
		switch {
		case ok && g != v.line:
			fmt.Fprintf(&b, "- %s\n+ %s\n", v.line, g)
		case ok:
		case cause != "":
			excluded[cause]++
		default:
			fmt.Fprintf(&b, "- %s (no Ergo type)\n", v.line)
		}
	}
	for _, l := range gotLets {
		if !wantKeys[keyOf(l, 3)] {
			fmt.Fprintf(&b, "+ %s (no Go let)\n", l)
		}
	}
	return b.String(), excluded
}
