package features

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// nodesOf is the record's nodes of a file, in source order: the module
// first, then each function, type, test and law at the top level and each
// catch in a body.
func nodesOf(file *syntax.File) []Node {
	src := file.Src
	module := Node{Kind: Module, Offset: 0, Length: len(src), Line: 1, Column: 1}
	module.Statements, module.MaxDepth = statements(file)
	module.Shape = shape(file)
	out := []Node{module}
	add := func(kind int, n syntax.Node, name *syntax.Ident, params *syntax.FieldList, body syntax.Node) {
		line, col := file.Position(n.Pos())
		node := Node{Kind: kind, Offset: int(n.Pos()), Length: int(n.End() - n.Pos()), Line: line, Column: col}
		if name != nil {
			node.NameWords = Sketch(Words(nil, name.Name))
		}
		if params != nil {
			for _, f := range params.List {
				node.Params += max(1, len(f.Names))
			}
		}
		if body != nil {
			node.Statements, node.MaxDepth = statements(body)
			node.Shape = shape(body)
			syntax.Inspect(body, func(m syntax.Node) bool {
				if c, ok := m.(*syntax.CatchExpr); ok {
					for _, a := range c.Arms {
						node.Handlers = append(node.Handlers, armShape(a))
					}
				}
				return true
			})
		}
		out = append(out, node)
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if derived(n) {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncDecl:
			var params *syntax.FieldList
			if n.Type != nil {
				params = n.Type.Params
			}
			var body syntax.Node
			if n.Body != nil {
				body = n.Body
			}
			add(Function, n, n.Name, params, body)
		case *syntax.TypeDecl:
			add(Declared, n, n.Name, nil, nil)
		case *syntax.TestDecl:
			add(Test, n, n.Name, n.Params, n.Body)
		case *syntax.LawDecl:
			var body syntax.Node
			if n.Body != nil {
				body = n.Body
			}
			add(Test, n, n.Name, n.Params, body)
		case *syntax.CatchExpr:
			line, col := file.Position(n.Catch)
			node := Node{Kind: Handler, Offset: int(n.Catch), Length: int(n.End() - n.Catch), Line: line, Column: col}
			node.Statements, node.MaxDepth = statements(n)
			node.Shape = shape(n)
			for _, a := range n.Arms {
				node.Handlers = append(node.Handlers, armShape(a))
			}
			out = append(out, node)
		}
		return true
	})
	return out
}

// statements is the number of statements under n (blocks are not
// statements) and the deepest nesting of blocks under it.
func statements(n syntax.Node) (count, deepest int) {
	depth := 0
	var visit func(m syntax.Node) bool
	visit = func(m syntax.Node) bool {
		if derived(m) {
			return false
		}
		switch m.(type) {
		case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CatchArm:
			depth++
			deepest = max(deepest, depth)
			for _, c := range children(m) {
				syntax.Inspect(c, visit)
			}
			depth--
			return false
		case syntax.Stmt:
			count++
		}
		return true
	}
	syntax.Inspect(n, visit)
	return count, deepest
}

// children is the direct children of n, in order.
func children(n syntax.Node) []syntax.Node {
	var out []syntax.Node
	syntax.Inspect(n, func(m syntax.Node) bool {
		if m == n {
			return true
		}
		out = append(out, m)
		return false
	})
	return out
}

// shape is a SimHash of the node kinds under n in walk order, with the
// names erased: for each run of three kinds, the 64 bits of its FNV-1a
// hash vote, and a bit of the shape is set when more of the runs set it
// than clear it.
func shape(n syntax.Node) uint64 {
	var kinds []string
	syntax.Inspect(n, func(m syntax.Node) bool {
		if derived(m) {
			return false
		}
		kinds = append(kinds, fmt.Sprintf("%T", m))
		return true
	})
	var votes [64]int
	for i := 0; i+3 <= len(kinds); i++ {
		h := uint64(14695981039346656037)
		for _, k := range kinds[i : i+3] {
			for j := 0; j < len(k); j++ {
				h ^= uint64(k[j])
				h *= 1099511628211
			}
			h ^= '/'
			h *= 1099511628211
		}
		for b := 0; b < 64; b++ {
			if h>>b&1 == 1 {
				votes[b]++
			} else {
				votes[b]--
			}
		}
	}
	var s uint64
	for b, v := range votes {
		if v > 0 {
			s |= 1 << b
		}
	}
	return s
}

// armShape is what a catch arm does with the failure it caught.
func armShape(a *syntax.CatchArm) int {
	if len(a.Body) == 0 {
		return Empty
	}
	bound := ""
	for _, t := range a.Types {
		if p, ok := t.(*syntax.AsPattern); ok && p.Name != nil {
			bound = p.Name.Name
		}
	}
	var value syntax.Expr
	switch s := a.Body[len(a.Body)-1].(type) {
	case *syntax.ReturnStmt:
		if len(s.Results) == 1 {
			value = s.Results[0]
		}
	case *syntax.ExprStmt:
		value = s.X
	}
	if u, ok := value.(*syntax.UnaryExpr); ok {
		if _, lit := u.X.(*syntax.BasicLit); lit {
			return YieldsLiteral
		}
	}
	switch v := value.(type) {
	case *syntax.BasicLit, *syntax.CompositeLit:
		return YieldsLiteral
	case *syntax.Ident:
		// A name is a literal only by its type (None, a constant), which the
		// parser does not know; only the bound failure is read here.
		if bound != "" && v.Name == bound {
			return Rethrows
		}
	}
	for _, s := range a.Body[:len(a.Body)-1] {
		if e, ok := s.(*syntax.ExprStmt); ok {
			if _, call := e.X.(*syntax.CallExpr); call {
				return Records
			}
		}
	}
	return Translates
}
